package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/tmlabonte/llamactl/internal/benchmark"
	"github.com/tmlabonte/llamactl/internal/builder"
	"github.com/tmlabonte/llamactl/internal/models"
)

// handleListBenchmarks returns all benchmark runs.
func (s *Server) handleListBenchmarks(w http.ResponseWriter, r *http.Request) {
	runs := s.bench.List()

	if isHTMX(r) {
		respondHTML(w)
		s.renderPartial(w, "benchmark_list", struct {
			Runs []benchmark.BenchmarkRun
		}{Runs: runs})
		return
	}

	respondJSON(w, runs)
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

// handleStartBenchmark starts a new benchmark run.
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
	cfg, err := s.registry.GetConfig(modelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if !s.process.IsRunning() {
		http.Error(w, "router is not running — start the server first", http.StatusBadRequest)
		return
	}

	preset := benchmark.GetPreset(presetName)
	metrics := s.monitor.Current()

	// Find active build info
	var activeBuild builder.BuildResult
	if s.cfg.ActiveBuild != "" {
		for _, b := range s.builder.List() {
			if b.ID == s.cfg.ActiveBuild && b.Status == builder.BuildStatusSuccess {
				activeBuild = b
				break
			}
		}
	}
	if activeBuild.ID == "" {
		for _, b := range s.builder.List() {
			if b.Status == builder.BuildStatusSuccess {
				activeBuild = b
				break
			}
		}
	}

	// Short model name for display
	modelName := model.ModelID
	if idx := strings.LastIndex(modelName, "/"); idx >= 0 {
		modelName = modelName[idx+1:]
	}
	modelName = strings.TrimSuffix(modelName, "-GGUF")
	modelName = strings.TrimSuffix(modelName, "-gguf")

	run := benchmark.BenchmarkRun{
		ID:        fmt.Sprintf("bench-%d", time.Now().UnixMilli()),
		CreatedAt: time.Now(),
		Status:    benchmark.StatusRunning,

		ModelID:   modelID,
		ModelName: modelName,
		Quant:     model.Quant,
		SizeGB:    models.BytesToGB(model.SizeBytes),

		Config: benchmark.ConfigSnapshot{
			GPULayers:      cfg.GPULayers,
			ContextSize:    cfg.ContextSize,
			GPUAssign:      cfg.GPUAssign,
			TensorSplit:    cfg.TensorSplit,
			FlashAttention: cfg.FlashAttention,
			KVCacheQuant:   cfg.KVCacheQuant,
			Threads:        cfg.Threads,
			SpecType:       cfg.SpecType,
		},

		BuildID:      activeBuild.ID,
		BuildRef:     activeBuild.GitRef,
		BuildProfile: activeBuild.Profile,

		GPUs: benchmark.GPUSnapshotsFromMetrics(metrics),

		Preset:       preset.Name,
		PromptTokens: preset.PromptTokens,
		GenTokens:    preset.GenTokens,
	}

	s.bench.Save(run)

	// Build runner config
	routerName := s.registry.RouterName(modelID)
	runCfg := benchmark.RunConfig{
		Run:        run,
		Preset:     preset,
		ModelPath:  model.FilePath,
		RouterURL:  fmt.Sprintf("http://localhost:%d", s.cfg.LlamaPort),
		RouterName: routerName,
		BinaryDir:  activeBuild.BinaryPath,
	}
	// BinaryDir should be the directory, not the binary itself
	if strings.HasSuffix(runCfg.BinaryDir, "/llama-server") {
		runCfg.BinaryDir = runCfg.BinaryDir[:len(runCfg.BinaryDir)-len("/llama-server")]
	}

	// Start benchmark in background
	progressCh := make(chan benchmark.ProgressUpdate, 16)
	s.benchProgressMu.Lock()
	s.benchProgress[run.ID] = progressCh
	s.benchProgressMu.Unlock()

	runner := benchmark.NewRunner(s.bench)
	go func() {
		runner.Run(context.Background(), runCfg, progressCh)
		// Clean up progress channel after completion
		s.benchProgressMu.Lock()
		delete(s.benchProgress, run.ID)
		s.benchProgressMu.Unlock()
	}()

	if isHTMX(r) {
		respondHTML(w)
		s.renderPartial(w, "benchmark_progress", struct {
			ID     string
			Status string
		}{ID: run.ID, Status: "running"})
		return
	}

	respondJSON(w, run)
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

// handleBenchmarkProgress returns the current state of a running benchmark.
// Called by HTMX polling from the progress partial.
func (s *Server) handleBenchmarkProgress(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	run, err := s.bench.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if isHTMX(r) {
		respondHTML(w)
		s.renderPartial(w, "benchmark_progress", struct {
			ID     string
			Status string
			Error  string
		}{ID: run.ID, Status: run.Status, Error: run.Error})
		return
	}

	respondJSON(w, run)
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

	data := struct {
		Models  []*models.Model
		Presets []benchmark.Preset
		Running bool
	}{
		Models:  enabledModels,
		Presets: benchmark.Presets(),
		Running: s.process.IsRunning(),
	}
	s.renderPartial(w, "benchmark_form", data)
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
