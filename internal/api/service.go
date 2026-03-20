package api

import (
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/tmlabonte/llamactl/internal/builder"
	"github.com/tmlabonte/llamactl/internal/models"
	"github.com/tmlabonte/llamactl/internal/process"
)

// parseOptionalFloat returns a *float64 if s is non-empty and valid, else nil.
func parseOptionalFloat(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

// parseOptionalInt returns a *int if s is non-empty and valid, else nil.
func parseOptionalInt(s string) *int {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &v
}

func (s *Server) handleServiceStatus(w http.ResponseWriter, r *http.Request) {
	status := s.process.GetStatus()

	if isHTMX(r) {
		respondHTML(w)
		var badge string
		switch status.State {
		case process.StateRunning:
			badge = `<ins>Running</ins>`
		case process.StateStarting:
			badge = `<mark>Starting...</mark>`
		case process.StateFailed:
			badge = fmt.Sprintf(`<del>Failed</del> <small style="color:var(--pico-del-color)">%s</small>`, status.Error)
		default:
			badge = `Stopped`
		}
		if status.Uptime != "" {
			badge += fmt.Sprintf(` <small>(%s)</small>`, status.Uptime)
		}
		fmt.Fprint(w, badge)
		return
	}

	respondJSON(w, status)
}

func (s *Server) handleServiceStart(w http.ResponseWriter, r *http.Request) {
	if !s.process.IsRunning() {
		if err := s.startRouter(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	s.handleServiceStatus(w, r)
}

func (s *Server) handleServiceStop(w http.ResponseWriter, r *http.Request) {
	if err := s.process.Stop(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.handleServiceStatus(w, r)
}

func (s *Server) handleServiceRestart(w http.ResponseWriter, r *http.Request) {
	if err := s.process.Restart(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.handleServiceStatus(w, r)
}

func (s *Server) handleServiceLogs(w http.ResponseWriter, r *http.Request) {
	ch := s.process.Subscribe()
	defer s.process.Unsubscribe(ch)
	StreamLines(w, r.Context(), ch, "Router exited")
}

// handleLoadedModels returns a list of models known to the router for the server page.
func (s *Server) handleLoadedModels(w http.ResponseWriter, r *http.Request) {
	respondHTML(w)
	if !s.process.IsRunning() {
		return
	}

	routerModels, err := s.process.ListModels()
	if err != nil {
		slog.Debug("failed to list router models", "error", err)
		return
	}
	if len(routerModels) == 0 {
		return
	}

	fmt.Fprint(w, `<div style="margin-top: 0.5rem;"><small><strong>Models:</strong></small>`)
	for _, m := range routerModels {
		name := html.EscapeString(m.ID)
		switch m.Status.Value {
		case "loaded":
			fmt.Fprintf(w, `<br><small>&nbsp;&nbsp;● %s</small>`, name)
		case "loading":
			fmt.Fprintf(w, `<br><small>&nbsp;&nbsp;● %s <mark style="padding:0 0.2rem;">loading</mark></small>`, name)
		default: // "unloaded" or empty
			fmt.Fprintf(w, `<br><small>&nbsp;&nbsp;○ %s</small>`, name)
		}
	}
	fmt.Fprint(w, `</div>`)
}

// handleDebugRouterModels dumps the raw JSON from the router's /models endpoint.
func (s *Server) handleDebugRouterModels(w http.ResponseWriter, r *http.Request) {
	raw, err := s.process.ListModelsRaw()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

func (s *Server) handleServiceLogTabs(w http.ResponseWriter, r *http.Request) {
	// With the native router, logs are combined — no tabs needed.
	// Return empty to hide the tab bar.
	respondHTML(w)
}

func (s *Server) handleServiceHealth(w http.ResponseWriter, r *http.Request) {
	healthy := s.process.CheckHealth()
	respondJSON(w, map[string]bool{"healthy": healthy})
}

// handleActivateModel loads a model via the router.
func (s *Server) handleActivateModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Ensure router is running
	if !s.process.IsRunning() {
		if err := s.startRouter(); err != nil {
			http.Error(w, "Failed to start router: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Wait for router to be ready (up to 10 seconds)
		for i := 0; i < 20; i++ {
			time.Sleep(500 * time.Millisecond)
			if s.process.CheckHealth() {
				break
			}
		}
		if !s.process.IsRunning() {
			http.Error(w, "Router failed to start", http.StatusInternalServerError)
			return
		}
	}

	// Try router name, fall back to file path
	routerName := s.registry.RouterName(id)
	if err := s.process.LoadModel(routerName); err != nil {
		filePath := s.registry.ModelFilePath(id)
		if filePath == "" || s.process.LoadModel(filePath) != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if isHTMX(r) {
		s.handleListModels(w, r)
		return
	}

	respondJSON(w, map[string]string{"status": "loading", "model": id})
}

// handleDeactivateModel unloads a model via the router.
func (s *Server) handleDeactivateModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if err := s.process.UnloadModel(s.registry.RouterName(id)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if isHTMX(r) {
		s.handleListModels(w, r)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// startRouter starts the llama-server in router mode using the active build.
func (s *Server) startRouter() error {
	// Find the build binary
	binaryPath := ""
	if s.cfg.ActiveBuild != "" {
		for _, b := range s.builder.List() {
			if b.ID == s.cfg.ActiveBuild && b.Status == builder.BuildStatusSuccess {
				binaryPath = b.BinaryPath
				break
			}
		}
	}
	if binaryPath == "" {
		for _, b := range s.builder.List() {
			if b.Status == builder.BuildStatusSuccess {
				binaryPath = b.BinaryPath
				break
			}
		}
	}
	if binaryPath == "" {
		return fmt.Errorf("no compiled build available — build llama.cpp first")
	}

	// Generate preset INI from model configs
	presetPath, err := s.registry.WritePresetINI()
	if err != nil {
		slog.Warn("failed to write preset INI", "error", err)
	}

	return s.process.Start(process.RouterConfig{
		BinaryPath: binaryPath,
		PresetPath: presetPath,
		ModelsMax:  s.cfg.ModelsMax,
		Port:       s.cfg.LlamaPort,
	})
}

// handleModelEnable toggles a model's enabled state and updates the preset.
func (s *Server) handleModelEnable(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	r.ParseForm()
	enabled := r.FormValue("enabled") == "true"

	cfg, err := s.registry.GetConfig(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	cfg.Enabled = enabled
	if err := s.registry.SetConfig(id, cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Regenerate preset so the router picks up the change
	if _, err := s.registry.WritePresetINI(); err != nil {
		slog.Warn("failed to regenerate preset INI", "error", err)
	}

	// The router reads preset.ini at startup only. Changing which models
	// are available requires a restart. If the router is running, mark
	// that a restart is needed so the UI can show an indicator.
	// Note: /models/load and /models/unload control VRAM, not the
	// available list, so we don't call them here.

	if isHTMX(r) {
		s.handleListModels(w, r)
		return
	}

	respondJSON(w, map[string]bool{"enabled": enabled})
}

// handleModelVRAMEstimate returns a VRAM estimate for a model with given config params.
func (s *Server) handleModelVRAMEstimate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	model, err := s.registry.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	r.ParseForm()
	contextSize, _ := strconv.Atoi(r.FormValue("context_size"))
	kvCacheQuant := r.FormValue("kv_cache_quant")

	cfg := &models.ModelConfig{
		ContextSize:  contextSize,
		KVCacheQuant: kvCacheQuant,
	}

	total := models.VRAMEstimateForConfig(model, cfg)
	kvGB := models.EstimateKVCacheGB(model.NLayers, model.NKVHead, model.NHead, model.NEmbd, contextSize, kvCacheQuant)
	weightsGB := models.BytesToGB(model.SizeBytes)

	if isHTMX(r) {
		respondHTML(w)
		fmt.Fprintf(w, `<strong>%.1f GB</strong> <small>(weights: %.1f GB + KV cache: %.1f GB + overhead)</small>`,
			total, weightsGB, kvGB)
		return
	}

	respondJSON(w, map[string]any{
		"total_gb":    total,
		"weights_gb":  weightsGB,
		"kv_cache_gb": kvGB,
	})
}

// handleGetModelConfig returns the launch config for a model.
func (s *Server) handleGetModelConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	cfg, err := s.registry.GetConfig(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	model, _ := s.registry.Get(id)

	if isHTMX(r) {
		respondHTML(w)

		maxContext := 0
		detectedMMProj := ""
		if model != nil {
			maxContext = model.ContextLength
			detectedMMProj = models.FindMMProj(model.FilePath)
		}

		data := struct {
			ModelID        string
			Config         *models.ModelConfig
			EffectiveFlags string
			MaxContext     int
			HasMMProj      bool
		}{
			ModelID:        id,
			Config:         cfg,
			EffectiveFlags: cfg.EffectiveFlags(),
			MaxContext:     maxContext,
			HasMMProj:      cfg.MmprojPath != "" || detectedMMProj != "",
		}
		s.renderPartial(w, "model_config", data)
		return
	}

	respondJSON(w, cfg)
}

// handleUpdateModelConfig updates the launch config for a model.
func (s *Server) handleUpdateModelConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Fetch existing config to preserve fields not in the form (e.g. Enabled).
	cfg, err := s.registry.GetConfig(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if r.Header.Get("Content-Type") == "application/json" {
		if err := json.NewDecoder(r.Body).Decode(cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		r.ParseForm()
		cfg.GPULayers, _ = strconv.Atoi(r.FormValue("gpu_layers"))
		cfg.TensorSplit = r.FormValue("tensor_split")
		cfg.ContextSize, _ = strconv.Atoi(r.FormValue("context_size"))
		cfg.Threads, _ = strconv.Atoi(r.FormValue("threads"))
		cfg.FlashAttention = r.FormValue("flash_attention") == "on"
		cfg.Jinja = r.FormValue("jinja") == "on"
		cfg.KVCacheQuant = r.FormValue("kv_cache_quant")
		cfg.DirectIO = r.FormValue("direct_io") == "on"
		cfg.ExtraFlags = r.FormValue("extra_flags")

		cfg.Temperature = parseOptionalFloat(r.FormValue("temperature"))
		cfg.TopP = parseOptionalFloat(r.FormValue("top_p"))
		cfg.TopK = parseOptionalInt(r.FormValue("top_k"))
		cfg.MinP = parseOptionalFloat(r.FormValue("min_p"))
		cfg.PresencePenalty = parseOptionalFloat(r.FormValue("presence_penalty"))
		cfg.RepeatPenalty = parseOptionalFloat(r.FormValue("repeat_penalty"))

		cfg.MmprojPath = r.FormValue("mmproj_path")
	}

	if err := s.registry.SetConfig(id, cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Regenerate preset INI so the router picks up changes on next load/reload
	if _, err := s.registry.WritePresetINI(); err != nil {
		slog.Warn("failed to regenerate preset INI", "error", err)
	}

	// Mark model as needing reload (config changed but model not reloaded yet).
	// Sampling params are injected at the proxy layer and don't need a reload.
	if cfg.Enabled && s.process.IsRunning() {
		s.dirtyModels[id] = true
	}

	// Update VRAM estimate in model list
	if isHTMX(r) {
		if model, err := s.registry.Get(id); err == nil {
			baseVRAM := models.BytesToGB(model.SizeBytes) + 0.2
			peakVRAM := models.VRAMEstimateForConfig(model, cfg)
			w.Header().Set("HX-Trigger", fmt.Sprintf(
				`{"vramUpdated":{"id":%q,"vram":"%.1f - %.1f GB"}}`,
				id, baseVRAM, peakVRAM))
		}
	}

	s.handleGetModelConfig(w, r)
}

