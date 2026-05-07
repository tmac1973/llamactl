package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/tmlabonte/llamactl/internal/benchmark"
	"github.com/tmlabonte/llamactl/internal/models"
)

// handleListJobs returns all benchmark jobs.
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	jobs := s.bench.ListJobs()
	if isHTMX(r) {
		respondHTML(w)
		s.renderJobList(w, jobs)
		return
	}
	respondJSON(w, jobs)
}

// renderJobList renders the collapsible jobs list partial. Each job row
// shows status + cell-progress; expanding fetches the detail partial via
// HTMX so the matrix view doesn't render on every list refresh.
func (s *Server) renderJobList(w http.ResponseWriter, jobs []benchmark.BenchmarkJob) {
	enriched := make([]jobListEntry, 0, len(jobs))
	for _, j := range jobs {
		j := j
		var done, failed, total int
		for _, c := range j.Cells {
			total++
			switch c.Status {
			case benchmark.CellStatusCompleted:
				done++
			case benchmark.CellStatusFailed:
				failed++
			}
		}
		enriched = append(enriched, jobListEntry{
			Job:       &j,
			Done:      done,
			Failed:    failed,
			Total:     total,
			AdhocRuns: 0,
		})
	}
	if len(enriched) > 0 {
		// The synthetic adhoc job carries no Cells, so its progress
		// column should show the total run count instead.
		for i := range enriched {
			if enriched[i].Job.ID == benchmark.AdhocJobID {
				enriched[i].AdhocRuns = len(s.bench.RunsForJob(benchmark.AdhocJobID))
			}
		}
	}
	s.renderPartial(w, "job_list", enriched)
}

type jobListEntry struct {
	Job       *benchmark.BenchmarkJob
	Done      int
	Failed    int
	Total     int
	AdhocRuns int
}

// jobCreateRequest is the JSON body POST /api/benchmark-jobs accepts.
type jobCreateRequest struct {
	Name        string                       `json:"name"`
	Description string                       `json:"description,omitempty"`
	ModelIDs    []string                     `json:"model_ids"`
	BuildIDs    []string                     `json:"build_ids"`
	Presets     []string                     `json:"presets"`
	Overrides   *benchmark.ConfigOverrides   `json:"overrides,omitempty"`
}

// handleCreateJob expands the matrix and submits the job to the queue.
// Returns 409 when another job is already running.
func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req jobCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if len(req.ModelIDs) == 0 || len(req.BuildIDs) == 0 || len(req.Presets) == 0 {
		http.Error(w, "model_ids, build_ids, and presets are all required", http.StatusBadRequest)
		return
	}

	job := benchmark.BenchmarkJob{
		ID:          newJobID(),
		Name:        req.Name,
		Description: req.Description,
		Kind:        benchmark.JobKindBatch,
		Status:      benchmark.JobStatusPending,
		CreatedAt:   time.Now(),
		ModelIDs:    req.ModelIDs,
		BuildIDs:    req.BuildIDs,
		Presets:     req.Presets,
		Overrides:   req.Overrides,
		Cells:       benchmark.ExpandCells(req.ModelIDs, req.BuildIDs, req.Presets),
	}

	if err := s.jobs.Submit(job); err != nil {
		if errors.Is(err, benchmark.ErrJobAlreadyRunning) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusCreated)
	respondJSON(w, job)
}

// handleGetJob returns one job (with its cells).
func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, err := s.bench.GetJob(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if isHTMX(r) {
		respondHTML(w)
		// The synthetic adhoc job has no cell matrix — render the
		// existing flat run list instead so legacy + quick-bench runs
		// stay visible.
		if job.ID == benchmark.AdhocJobID {
			s.renderBenchmarkList(w, s.bench.RunsForJob(benchmark.AdhocJobID))
			return
		}
		s.renderJobDetail(w, job)
		return
	}
	respondJSON(w, job)
}

// renderJobDetail enriches a job's cells with their linked run summary
// before rendering, so the matrix view can show TG t/s without a second
// round-trip per cell. Also tallies done/failed/total so the OOB
// summary update fragments at the top of the partial can patch the
// parent list row without re-rendering the entire list.
func (s *Server) renderJobDetail(w http.ResponseWriter, job *benchmark.BenchmarkJob) {
	type cellRow struct {
		Idx       int
		Cell      benchmark.JobCell
		ModelName string
		Quant     string
		BuildLbl  string
		TGTPS     string // formatted, "—" when no summary
		PPTPS     string
		ErrorShort string
	}
	rows := make([]cellRow, 0, len(job.Cells))
	var done, failed int
	for i, c := range job.Cells {
		switch c.Status {
		case benchmark.CellStatusCompleted:
			done++
		case benchmark.CellStatusFailed:
			failed++
		}
		row := cellRow{Idx: i, Cell: c, ModelName: shortenModelName(c.ModelID), BuildLbl: c.BuildID, TGTPS: "—", PPTPS: "—"}
		// Pull Quant from the registry first so pending cells (no run
		// yet) still show it; the run's value wins once it exists.
		if m, err := s.registry.Get(c.ModelID); err == nil {
			row.Quant = m.Quant
		}
		if c.BenchmarkRunID != "" {
			if run, err := s.bench.Get(c.BenchmarkRunID); err == nil {
				if run.ModelName != "" {
					row.ModelName = run.ModelName
				}
				if run.Quant != "" {
					row.Quant = run.Quant
				}
				if run.BuildID != "" {
					row.BuildLbl = run.BuildID
				}
				if run.Summary != nil {
					row.TGTPS = fmt.Sprintf("%.1f", run.Summary.AvgGenTokPerSec)
					row.PPTPS = fmt.Sprintf("%.0f", run.Summary.AvgPromptTokPerSec)
				}
			}
		}
		if c.Error != "" {
			row.ErrorShort = c.Error
			if len(row.ErrorShort) > 80 {
				row.ErrorShort = row.ErrorShort[:80] + "…"
			}
		}
		rows = append(rows, row)
	}
	s.renderPartial(w, "job_detail", struct {
		Job    *benchmark.BenchmarkJob
		Rows   []cellRow
		Done   int
		Failed int
		Total  int
	}{Job: job, Rows: rows, Done: done, Failed: failed, Total: len(job.Cells)})
}

// handleJobForm renders the new-job modal contents (multi-select models,
// builds, presets, optional overrides, live cell-count preview).
func (s *Server) handleJobForm(w http.ResponseWriter, r *http.Request) {
	respondHTML(w)
	var enabled []*models.Model
	for _, m := range s.registry.List() {
		if cfg, err := s.registry.GetConfig(m.ID); err == nil && cfg.Enabled {
			enabled = append(enabled, m)
		}
	}
	type buildOpt struct {
		ID, Profile, GitRef, Tag string
	}
	var builds []buildOpt
	for _, b := range s.builder.List() {
		if b.Status != "success" {
			continue
		}
		builds = append(builds, buildOpt{ID: b.ID, Profile: b.Profile, GitRef: b.GitRef, Tag: b.Tag})
	}
	s.renderPartial(w, "job_form", struct {
		Models  []*models.Model
		Builds  []buildOpt
		Presets []benchmark.Preset
		Running bool
	}{
		Models:  enabled,
		Builds:  builds,
		Presets: benchmark.Presets(),
		Running: s.process.IsRunning(),
	})
}

// handleDeleteJob removes a job. The runs query param controls what
// happens to its runs:
//   - cascade (default): runs deleted with the job
//   - orphan: runs reassigned to the synthetic adhoc job
func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	disposition := benchmark.DeleteCascade
	if v := r.URL.Query().Get("runs"); v != "" {
		switch v {
		case "cascade":
			disposition = benchmark.DeleteCascade
		case "orphan":
			disposition = benchmark.DeleteOrphan
		default:
			http.Error(w, "runs must be 'cascade' or 'orphan'", http.StatusBadRequest)
			return
		}
	}
	if err := s.bench.DeleteJob(id, disposition); err != nil {
		// Refusing to delete the synthetic adhoc job is a 400 (the
		// caller passed something they shouldn't); not-found is a 404.
		if strings.Contains(err.Error(), "synthetic") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCancelJob signals the queue to cancel the running job.
func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	if err := s.jobs.Cancel(chi.URLParam(r, "id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRetryFailedCells re-queues only failed cells of the given job.
func (s *Server) handleRetryFailedCells(w http.ResponseWriter, r *http.Request) {
	if err := s.jobs.RetryFailed(chi.URLParam(r, "id")); err != nil {
		if errors.Is(err, benchmark.ErrJobAlreadyRunning) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleJobProgress streams job snapshots over SSE. Uses a 500ms poll
// interval for now — the cell loop saves on every status transition, so
// polling the store is sufficient signal. A full pub/sub upgrade can
// land later if the dashboard ever shows multiple jobs at once.
//
// One event type:
//   - "snapshot": JSON-encoded BenchmarkJob with cells. Emitted only
//     when something changed since the previous send.
// Stream ends when the job reaches a terminal state.
func (s *Server) handleJobProgress(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.bench.GetJob(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	sse, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var lastSerialized []byte
	emit := func() bool {
		job, err := s.bench.GetJob(id)
		if err != nil {
			return false
		}
		data, err := json.Marshal(job)
		if err != nil {
			return false
		}
		if string(data) != string(lastSerialized) {
			lastSerialized = data
			_ = sse.SendEvent("snapshot", string(data))
		}
		return jobInTerminalState(job.Status)
	}

	if emit() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if emit() {
				return
			}
		}
	}
}

// handleExportJob serves a per-job export. Both formats accept the
// same scope param (?scope=cells|summary) since CSV honours it and
// JSON is always full-fidelity.
func (s *Server) handleExportJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, err := s.bench.GetJob(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	format, err := parseExportFormat(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	scope, err := parseExportScope(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	runs := s.bench.RunsForJob(id)
	jobs := newJobLookup([]*benchmark.BenchmarkJob{job})

	switch format {
	case exportFormatJSON:
		_ = writeJSONExport(w, fmt.Sprintf("job-%s.json", id), ExportEnvelope{
			Version: exportEnvelopeVersion,
			Jobs:    []*benchmark.BenchmarkJob{job},
			Runs:    runs,
		})
	default: // csv
		_ = writeCSVExport(w, fmt.Sprintf("job-%s-%s.csv", id, scope), runs, jobs, scope)
	}
}

func jobInTerminalState(status string) bool {
	switch status {
	case benchmark.JobStatusCompleted, benchmark.JobStatusFailed, benchmark.JobStatusCanceled:
		return true
	}
	return false
}

// newJobID returns a short, time-prefixed random ID. Time prefix keeps
// IDs sortable; the random suffix avoids collisions when a UI submits
// twice within the same millisecond.
func newJobID() string {
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("job-%d-%s", time.Now().UnixMilli(), hex.EncodeToString(buf[:]))
}
