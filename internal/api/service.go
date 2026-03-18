package api

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
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
	active := s.process.ListActive()

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// For htmx, render the first active instance status (backward compat)
		// or a stopped status if nothing is running.
		var status process.Status
		if len(active) > 0 {
			status = active[0]
		} else {
			status = process.Status{State: "stopped"}
		}
		s.renderPartial(w, "service_status", status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(active)
}

func (s *Server) handleServiceStart(w http.ResponseWriter, r *http.Request) {
	active := s.process.ListActive()
	if len(active) > 0 {
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			s.renderPartial(w, "service_status", active[0])
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(active)
		return
	}

	http.Error(w, "No model active. Activate a model from the Models page first.", http.StatusBadRequest)
}

func (s *Server) handleServiceStop(w http.ResponseWriter, r *http.Request) {
	if err := s.process.StopAll(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	status := process.Status{State: "stopped"}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		s.renderPartial(w, "service_status", status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleServiceRestart(w http.ResponseWriter, r *http.Request) {
	// Restart all active instances
	active := s.process.ListActive()
	if len(active) == 0 {
		http.Error(w, "no active instances to restart", http.StatusBadRequest)
		return
	}

	var lastErr error
	for _, st := range active {
		if err := s.process.Restart(st.ID); err != nil {
			lastErr = err
		}
	}
	if lastErr != nil {
		http.Error(w, lastErr.Error(), http.StatusInternalServerError)
		return
	}

	updated := s.process.ListActive()
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var status process.Status
		if len(updated) > 0 {
			status = updated[0]
		} else {
			status = process.Status{State: "stopped"}
		}
		s.renderPartial(w, "service_status", status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

func (s *Server) handleServiceLogs(w http.ResponseWriter, r *http.Request) {
	// Accept optional ?model= query param to select instance.
	// If not provided, use the first active instance.
	modelID := r.URL.Query().Get("model")
	if modelID == "" {
		active := s.process.ListActive()
		if len(active) == 0 {
			http.Error(w, "no active instances", http.StatusBadRequest)
			return
		}
		modelID = active[0].ID
	}

	sse, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ch, err := s.process.Subscribe(modelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer s.process.Unsubscribe(modelID, ch)

	for {
		select {
		case line, ok := <-ch:
			if !ok {
				sse.SendEvent("done", "Process exited")
				return
			}
			sse.SendLine(line)
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleServiceLogTabs(w http.ResponseWriter, r *http.Request) {
	active := s.process.ListActive()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for _, st := range active {
		// Use the model registry ID as the tab label, truncated for display
		label := st.ID
		if len(label) > 30 {
			label = label[:30] + "..."
		}
		escaped := html.EscapeString(st.ID)
		fmt.Fprintf(w, `<li><a href="#" data-model="%s" onclick="event.preventDefault();switchLogTab('%s')">%s</a></li>`,
			escaped, escaped, html.EscapeString(label))
	}
}

func (s *Server) handleServiceHealth(w http.ResponseWriter, r *http.Request) {
	// Check if any instance is healthy
	active := s.process.ListActive()
	healthy := false
	for _, st := range active {
		if st.HealthOK {
			healthy = true
			break
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"healthy": healthy})
}

// handleActivateModel starts a model instance (without stopping others).
func (s *Server) handleActivateModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	model, err := s.registry.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	cfg, err := s.registry.GetConfig(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Find the build binary
	binaryPath := ""
	if cfg.BuildID != "" {
		for _, b := range s.builder.List() {
			if b.ID == cfg.BuildID && b.Status == "success" {
				binaryPath = b.BinaryPath
				break
			}
		}
	}
	if binaryPath == "" {
		// Try to find any successful build
		for _, b := range s.builder.List() {
			if b.Status == "success" {
				binaryPath = b.BinaryPath
				break
			}
		}
	}
	if binaryPath == "" {
		http.Error(w, "No compiled build available. Build llama.cpp first.", http.StatusBadRequest)
		return
	}

	var extraFlags []string
	if cfg.ExtraFlags != "" {
		extraFlags = strings.Fields(cfg.ExtraFlags)
	}

	launchCfg := process.LaunchConfig{
		BinaryPath:     binaryPath,
		ModelPath:      model.FilePath,
		GPULayers:      cfg.GPULayers,
		TensorSplit:    cfg.TensorSplit,
		ContextSize:    cfg.ContextSize,
		Threads:        cfg.Threads,
		FlashAttention: cfg.FlashAttention,
		Jinja:          cfg.Jinja,
		KVCacheQuant:   cfg.KVCacheQuant,
		ExtraFlags:     extraFlags,
		VisibleDevices: cfg.GPUDevices,
	}

	if err := s.process.Start(id, launchCfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	status := s.process.GetStatus(id)
	status.Model = model.ModelID
	status.BuildID = cfg.BuildID

	if r.Header.Get("HX-Request") == "true" {
		s.handleListModels(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// handleDeactivateModel stops a specific model instance.
func (s *Server) handleDeactivateModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if err := s.process.Stop(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		s.handleListModels(w, r)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleModelVRAMEstimate returns a VRAM estimate for a model with given config params.
// Used by the UI for live VRAM updates as settings change.
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

	// Build a temporary config for estimation
	cfg := &models.ModelConfig{
		ContextSize:  contextSize,
		KVCacheQuant: kvCacheQuant,
	}

	total := models.VRAMEstimateForConfig(model, cfg)
	kvGB := models.EstimateKVCacheGB(model.NLayers, model.NKVHead, model.NHead, model.NEmbd, contextSize, kvCacheQuant)
	weightsGB := float64(model.SizeBytes) / (1024 * 1024 * 1024)

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<strong>%.1f GB</strong> <small>(weights: %.1f GB + KV cache: %.1f GB + overhead)</small>`,
			total, weightsGB, kvGB)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"total_gb":   total,
		"weights_gb": weightsGB,
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

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := struct {
			ModelID         string
			Config          *models.ModelConfig
			AvailableBuilds interface{}
			EffectiveFlags  string
		}{
			ModelID:         id,
			Config:          cfg,
			AvailableBuilds: s.builder.List(),
			EffectiveFlags:  cfg.EffectiveFlags(),
		}
		s.renderPartial(w, "model_config", data)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cfg)
}

// handleUpdateModelConfig updates the launch config for a model.
func (s *Server) handleUpdateModelConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var cfg models.ModelConfig

	if r.Header.Get("Content-Type") == "application/json" {
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
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
		cfg.ExtraFlags = r.FormValue("extra_flags")
		cfg.BuildID = r.FormValue("build_id")
		cfg.GPUDevices = r.FormValue("gpu_devices")

		// Sampling parameters — empty string means "default" (nil pointer).
		cfg.Temperature = parseOptionalFloat(r.FormValue("temperature"))
		cfg.TopP = parseOptionalFloat(r.FormValue("top_p"))
		cfg.TopK = parseOptionalInt(r.FormValue("top_k"))
		cfg.MinP = parseOptionalFloat(r.FormValue("min_p"))
		cfg.PresencePenalty = parseOptionalFloat(r.FormValue("presence_penalty"))
		cfg.RepeatPenalty = parseOptionalFloat(r.FormValue("repeat_penalty"))
	}

	if err := s.registry.SetConfig(id, &cfg); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Return updated config form
	s.handleGetModelConfig(w, r)
}
