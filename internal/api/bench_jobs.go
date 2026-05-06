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
)

// handleListJobs returns all benchmark jobs as JSON.
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, s.bench.ListJobs())
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
	job, err := s.bench.GetJob(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	respondJSON(w, job)
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

// handleExportJob is a minimal JSON-only export stub. Step 9 replaces
// it with full CSV (per-cell rows + summary) and self-contained JSON.
func (s *Server) handleExportJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, err := s.bench.GetJob(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	if format != "json" {
		http.Error(w, "only format=json is wired up; CSV ships with step 9", http.StatusNotImplemented)
		return
	}
	payload := struct {
		Job  *benchmark.BenchmarkJob   `json:"job"`
		Runs []benchmark.BenchmarkRun  `json:"runs"`
	}{
		Job:  job,
		Runs: s.bench.RunsForJob(id),
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="job-%s.json"`, id))
	respondJSON(w, payload)
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
