package api

import (
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/tmlabonte/llamactl/internal/benchmark"
	"github.com/tmlabonte/llamactl/internal/builder"
	"github.com/tmlabonte/llamactl/internal/models"
)

// builderResolver adapts the builder's List() into a benchmark.BuildResolver
// closure so the benchmark package can backfill Build snapshots without
// importing builder. Returns the zero value when the build is no longer
// known, which the migration treats as "fall back to the legacy flat
// fields."
func builderResolver(b *builder.Builder) benchmark.BuildResolver {
	return func(buildID string) benchmark.BuildSnapshot {
		for _, br := range b.List() {
			if br.ID == buildID {
				return benchmark.BuildSnapshot{
					ID:         br.ID,
					Tag:        br.Tag,
					Profile:    br.Profile,
					Vendor:     br.Profile,
					GitSHA:     br.GitSHA,
					GitRef:     br.GitRef,
					CMakeFlags: br.CMakeFlags,
					BinaryPath: br.BinaryPath,
				}
			}
		}
		return benchmark.BuildSnapshot{}
	}
}

// handleListBenchmarks returns benchmark runs, optionally filtered:
//   - ?job=<id>          returns only runs belonging to that job
//   - ?scope=adhoc       returns only runs in the synthetic adhoc job
//   - ?scope=batch       returns only runs belonging to a real batch job
func (s *Server) handleListBenchmarks(w http.ResponseWriter, r *http.Request) {
	runs := s.bench.List()

	if jobID := r.URL.Query().Get("job"); jobID != "" {
		runs = filterRunsByJob(runs, jobID)
	}
	if scope := r.URL.Query().Get("scope"); scope != "" {
		switch scope {
		case "adhoc":
			runs = filterRunsByJob(runs, benchmark.AdhocJobID)
		case "batch":
			batchJobIDs := map[string]bool{}
			for _, j := range s.bench.ListJobs() {
				if j.ID != benchmark.AdhocJobID {
					batchJobIDs[j.ID] = true
				}
			}
			filtered := runs[:0]
			for _, run := range runs {
				if batchJobIDs[run.JobID] {
					filtered = append(filtered, run)
				}
			}
			runs = filtered
		default:
			http.Error(w, "scope must be 'adhoc' or 'batch'", http.StatusBadRequest)
			return
		}
	}

	if isHTMX(r) {
		respondHTML(w)
		s.renderBenchmarkList(w, runs)
		return
	}

	respondJSON(w, runs)
}

func filterRunsByJob(runs []benchmark.BenchmarkRun, jobID string) []benchmark.BenchmarkRun {
	out := runs[:0]
	for _, r := range runs {
		if r.JobID == jobID {
			out = append(out, r)
		}
	}
	return out
}

// renderBenchmarkList emits the grouped, searchable benchmarks table.
// Groups by ModelName; each group is collapsed by default. The wrapping
// JS in benchmarks.html drives toggle/filter/compare/export/delete using
// data-model and data-search attributes plus the .bench-runs-container
// scope so the same renderer can serve multiple list contexts (today
// just the adhoc job's expanded view; tomorrow per-job filtered views).
func (s *Server) renderBenchmarkList(w http.ResponseWriter, runs []benchmark.BenchmarkRun) {
	w.Write([]byte(`<div class="bench-runs-container">`))
	defer w.Write([]byte(`</div>`))

	if len(runs) == 0 {
		w.Write([]byte("<p>No benchmarks yet. Run one above to get started.</p>"))
		return
	}

	type modelGroup struct {
		name string
		runs []benchmark.BenchmarkRun
	}
	idx := map[string]*modelGroup{}
	var names []string
	for _, r := range runs {
		key := r.ModelName
		if key == "" {
			key = "(unknown)"
		}
		g, ok := idx[key]
		if !ok {
			g = &modelGroup{name: key}
			idx[key] = g
			names = append(names, key)
		}
		g.runs = append(g.runs, r)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })

	w.Write([]byte(`<div class="model-list-controls">
		<input type="search" class="model-filter" placeholder="Filter by model, quant, build, preset…" oninput="filterBenchmarks(this, this.value)" autocomplete="off">
		<button type="button" class="outline secondary" onclick="collapseAllBenchGroups(this)">Collapse All</button>
		<button type="button" class="outline secondary" onclick="expandAllBenchGroups(this)">Expand All</button>
		<button type="button" class="outline secondary" onclick="compareSelectedRuns(this)">Compare</button>
		<button type="button" class="outline secondary" onclick="exportSelectedRuns(this)">Export CSV</button>
		<button type="button" class="outline secondary" onclick="deleteSelectedRuns(this)">Delete Selected</button>
	</div>`))

	w.Write([]byte(`<table role="grid">
		<thead><tr>
			<th style="width:2rem;"><input type="checkbox" style="margin:0;" title="Select all" onchange="document.querySelectorAll('.bench-check').forEach(function(c){c.checked=this.checked}.bind(this));"></th>
			<th>Model</th>
			<th>Quant</th>
			<th title="Prompt Processing tokens/sec — higher is better.">PP t/s</th>
			<th title="Token Generation tokens/sec — the speed you feel during chat.">TG t/s</th>
			<th title="Time To First Token — lower is better.">TTFT</th>
			<th>Build</th>
			<th>Preset</th>
			<th>Date</th>
			<th></th>
		</tr></thead>`))

	for _, name := range names {
		g := idx[name]
		fmt.Fprintf(w,
			`<tbody class="bench-group-header collapsed" data-model="%s"><tr onclick="toggleBenchGroup(this.parentElement)"><td colspan="10" class="org-cell"><span class="caret">▸</span> <strong>%s</strong> <small>(%d)</small></td></tr></tbody>`,
			html.EscapeString(name), html.EscapeString(name), len(g.runs))

		for _, run := range g.runs {
			search := strings.ToLower(strings.Join([]string{run.ModelName, run.Quant, run.BuildID, run.BuildRef, run.Preset}, " "))
			pp, tg, ttft := "—", "—", "—"
			if run.Summary != nil {
				pp = fmt.Sprintf("%.0f", run.Summary.AvgPromptTokPerSec)
				tg = fmt.Sprintf("<strong>%.1f</strong>", run.Summary.AvgGenTokPerSec)
				ttft = fmt.Sprintf("%.0f ms", run.Summary.AvgTTFTMs)
			}
			benchTag := ""
			if run.LlamaBench != nil {
				benchTag = ` <mark style="padding:0 0.3rem;font-size:0.75rem;">bench</mark>`
			}
			if len(run.LlamaBenchy) > 0 {
				benchTag += ` <mark style="padding:0 0.3rem;font-size:0.75rem;background:var(--pico-primary-background);color:var(--pico-primary-inverse);">benchy</mark>`
			}
			runningTitle := ""
			if run.Status == "running" {
				runningTitle = `title="Running — select to delete anyway"`
			}
			buildCell := "—"
			if run.BuildID != "" {
				buildCell = `<small><kbd>` + html.EscapeString(run.BuildID) + `</kbd></small>`
			} else if run.BuildRef != "" {
				buildCell = "<small>" + html.EscapeString(run.BuildRef) + "</small>"
			}

			fmt.Fprintf(w, `<tbody class="bench-row-group" data-model="%s" data-search="%s" style="display:none;">
				<tr>
					<td><input type="checkbox" class="bench-check" value="%s" style="margin:0;" %s></td>
					<td>%s%s</td>
					<td><kbd>%s</kbd></td>
					<td>%s</td>
					<td>%s</td>
					<td>%s</td>
					<td>%s</td>
					<td><small>%s</small></td>
					<td><small>%s</small></td>
					<td>
						<span style="display:flex;gap:0.25rem;">
							<button type="button" class="outline secondary" style="padding:0.2rem 0.5rem;font-size:0.8rem;width:auto;" hx-get="/api/benchmarks/%s" hx-target="next .bench-detail" hx-swap="innerHTML" hx-on::before-request="var d=this.closest('tr').nextElementSibling.querySelector('td');if(d.innerHTML.trim()){d.innerHTML='';event.preventDefault();}">Detail</button>
						</span>
					</td>
				</tr>
				<tr><td colspan="10" class="bench-detail"></td></tr>
			</tbody>`,
				html.EscapeString(name), html.EscapeString(search),
				html.EscapeString(run.ID), runningTitle,
				html.EscapeString(run.ModelName), benchTag,
				html.EscapeString(run.Quant),
				pp, tg, ttft,
				buildCell,
				html.EscapeString(run.Preset),
				run.CreatedAt.Format("Jan 2 15:04"),
				html.EscapeString(run.ID))
		}
	}

	w.Write([]byte(`</table>`))
}

// handleGetBenchmark returns a single benchmark run.
func (s *Server) handleGetBenchmark(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	run, err := s.bench.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if isHTMX(r) {
		respondHTML(w)
		s.renderPartial(w, "benchmark_detail", run)
		return
	}

	respondJSON(w, run)
}

// handleStartBenchmark is the Quick Benchmark entry point: it builds a
// 1-cell job from a (model, preset) pair and submits it to the JobQueue,
// using the currently-active build. Equivalent to the new-job form with
// {ModelIDs:[modelID], BuildIDs:[active], Presets:[preset]}.
func (s *Server) handleStartBenchmark(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	modelID := r.FormValue("model_id")
	presetName := r.FormValue("preset")

	if modelID == "" {
		http.Error(w, "model_id is required", http.StatusBadRequest)
		return
	}

	model, err := s.registry.Get(modelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if !s.process.IsRunning() {
		http.Error(w, "router is not running — start the server first", http.StatusBadRequest)
		return
	}

	// Resolve active build the same way startRouter does.
	buildID := s.cfg.ActiveBuild
	if buildID == "" {
		if b := s.builder.LatestSuccessfulBuild(); b != nil {
			buildID = b.ID
		}
	}
	if buildID == "" {
		http.Error(w, "no compiled build available — build llama.cpp first", http.StatusBadRequest)
		return
	}

	preset := benchmark.GetPreset(presetName)

	jobName := fmt.Sprintf("Quick: %s / %s", shortenModelName(model.ModelID), preset.Name)
	job := benchmark.BenchmarkJob{
		ID:        newJobID(),
		Name:      jobName,
		Kind:      benchmark.JobKindBatch,
		Status:    benchmark.JobStatusPending,
		CreatedAt: time.Now(),
		ModelIDs:  []string{modelID},
		BuildIDs:  []string{buildID},
		Presets:   []string{preset.Name},
		Cells:     benchmark.ExpandCells([]string{modelID}, []string{buildID}, []string{preset.Name}),
	}

	if err := s.jobs.Submit(job); err != nil {
		if errors.Is(err, benchmark.ErrJobAlreadyRunning) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if isHTMX(r) {
		respondHTML(w)
		// Tell the jobs list to refresh so the user sees the new job
		// appear; the response body itself is just a brief confirmation.
		w.Header().Set("HX-Trigger", "jobChanged")
		fmt.Fprintf(w, `<small style="color:var(--pico-muted-color);">Started: %s</small>`, html.EscapeString(jobName))
		return
	}

	respondJSON(w, job)
}

// handleDeleteBenchmark removes a benchmark run.
func (s *Server) handleDeleteBenchmark(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.bench.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if isHTMX(r) {
		s.handleListBenchmarks(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleBatchDeleteBenchmarks deletes multiple benchmark runs at once.
func (s *Server) handleBatchDeleteBenchmarks(w http.ResponseWriter, r *http.Request) {
	idsParam := r.URL.Query().Get("ids")
	if idsParam == "" {
		http.Error(w, "ids parameter required", http.StatusBadRequest)
		return
	}
	for _, id := range strings.Split(idsParam, ",") {
		id = strings.TrimSpace(id)
		if id != "" {
			s.bench.Delete(id)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// handleExportBenchmarks exports selected benchmark runs as CSV.
func (s *Server) handleExportBenchmarks(w http.ResponseWriter, r *http.Request) {
	idsParam := r.URL.Query().Get("ids")
	if idsParam == "" {
		http.Error(w, "ids parameter required", http.StatusBadRequest)
		return
	}

	var runs []benchmark.BenchmarkRun
	for _, id := range strings.Split(idsParam, ",") {
		id = strings.TrimSpace(id)
		if run, err := s.bench.Get(id); err == nil {
			runs = append(runs, *run)
		}
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=benchmarks.csv")

	// Header — one row per test point, with run metadata repeated
	fmt.Fprintln(w, "Model,Quant,Size (GB),Preset,Row Type,Prompt Tokens,Gen Tokens,Rep,PP t/s,TG t/s,TTFT (ms),Total (ms),GPU Layers,Context,GPU Assign,Tensor Split,Flash Attn,KV Quant,Direct IO,Threads,Spec Type,Build,Build Ref,GPUs,Date")

	for _, run := range runs {
		gpuNames := ""
		for i, g := range run.GPUs {
			if i > 0 {
				gpuNames += "; "
			}
			gpuNames += g.Name
		}

		common := fmt.Sprintf("%s,%s,%.1f,%s",
			run.ModelName, run.Quant, run.SizeGB, run.Preset)
		config := fmt.Sprintf("%d,%d,%s,%s,%t,%s,%t,%d,%s,%s,%s,\"%s\",%s",
			run.Config.GPULayers,
			run.Config.ContextSize,
			run.Config.GPUAssign,
			run.Config.TensorSplit,
			run.Config.FlashAttention,
			run.Config.KVCacheQuant,
			run.Config.DirectIO,
			run.Config.Threads,
			run.Config.SpecType,
			run.BuildID,
			run.BuildRef,
			gpuNames,
			run.CreatedAt.Format("2006-01-02 15:04"))

		// Individual test results
		for _, r := range run.Results {
			fmt.Fprintf(w, "%s,api,%d,%d,%d,%.0f,%.1f,%.0f,%.0f,%s\n",
				common, r.PromptTokens, r.GenTokens, r.Repetition,
				r.PromptTokPerSec, r.GenTokPerSec, r.TTFTMs, r.TotalMs,
				config)
		}

		// Summary row
		if run.Summary != nil {
			fmt.Fprintf(w, "%s,api-avg,,,,%.0f,%.1f,%.0f,,%s\n",
				common,
				run.Summary.AvgPromptTokPerSec, run.Summary.AvgGenTokPerSec, run.Summary.AvgTTFTMs,
				config)
		}

		// llama-bench results
		if run.LlamaBench != nil {
			fmt.Fprintf(w, "%s,llama-bench,%d,%d,%d,%.0f,%.1f,,,%s\n",
				common,
				run.LlamaBench.PromptTokens, run.LlamaBench.GenTokens, run.LlamaBench.Repetitions,
				run.LlamaBench.PromptTokPerSec, run.LlamaBench.GenTokPerSec,
				config)
		}
	}
}

// handleCompareBenchmarks returns comparison data for selected runs.
func (s *Server) handleCompareBenchmarks(w http.ResponseWriter, r *http.Request) {
	idsParam := r.URL.Query().Get("ids")
	if idsParam == "" {
		http.Error(w, "ids parameter required", http.StatusBadRequest)
		return
	}

	ids := strings.Split(idsParam, ",")
	var runs []benchmark.BenchmarkRun
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if run, err := s.bench.Get(id); err == nil {
			runs = append(runs, *run)
		}
	}

	if len(runs) < 2 {
		http.Error(w, "need at least 2 runs to compare", http.StatusBadRequest)
		return
	}

	comparison := benchmark.BuildComparison(runs)

	if isHTMX(r) {
		respondHTML(w)
		s.renderPartial(w, "benchmark_compare", comparison)
		return
	}

	respondJSON(w, comparison)
}

// handleTimings returns passive timing data.
func (s *Server) handleTimings(w http.ResponseWriter, r *http.Request) {
	modelID := chi.URLParam(r, "model_id")

	if isHTMX(r) {
		respondHTML(w)
		summary := s.bench.TimingSummary()
		s.renderPartial(w, "timings_summary", struct {
			Summary []benchmark.TimingModelSummary
		}{Summary: summary})
		return
	}

	samples := s.bench.Timings(modelID)
	respondJSON(w, samples)
}

// handleBenchmarkForm returns the benchmark form options (model list, presets).
func (s *Server) handleBenchmarkForm(w http.ResponseWriter, r *http.Request) {
	respondHTML(w)

	allModels := s.registry.List()
	var enabledModels []*models.Model
	for _, m := range allModels {
		if cfg, err := s.registry.GetConfig(m.ID); err == nil && cfg.Enabled {
			enabledModels = append(enabledModels, m)
		}
	}

	s.renderPartial(w, "benchmark_form", struct {
		Models  []*models.Model
		Presets []benchmark.Preset
		Running bool
	}{
		Models:  enabledModels,
		Presets: benchmark.Presets(),
		Running: s.process.IsRunning(),
	})
}

// captureTimings is called by the proxy to record passive timing data.
func (s *Server) captureTimings(modelID string, timings map[string]any) {
	promptN, _ := timings["prompt_n"].(float64)
	predictedN, _ := timings["predicted_n"].(float64)
	promptPerSec, _ := timings["prompt_per_second"].(float64)
	predictedPerSec, _ := timings["predicted_per_second"].(float64)

	if predictedN == 0 {
		return
	}

	s.bench.RecordTiming(benchmark.TimingSample{
		Timestamp:       time.Now(),
		ModelID:         modelID,
		PromptTokens:    int(promptN),
		GenTokens:       int(predictedN),
		PromptTokPerSec: promptPerSec,
		GenTokPerSec:    predictedPerSec,
	})

	slog.Debug("captured timing", "model", modelID,
		"prompt_tps", fmt.Sprintf("%.1f", promptPerSec),
		"gen_tps", fmt.Sprintf("%.1f", predictedPerSec))
}
