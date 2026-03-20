package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	status := s.process.GetStatus()

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		s.renderPartial(w, "service_status", status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleServiceStart(w http.ResponseWriter, r *http.Request) {
	if s.process.IsRunning() {
		status := s.process.GetStatus()
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			s.renderPartial(w, "service_status", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
		return
	}

	if err := s.startRouter(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	status := s.process.GetStatus()
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		s.renderPartial(w, "service_status", status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleServiceStop(w http.ResponseWriter, r *http.Request) {
	if err := s.process.Stop(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	status := s.process.GetStatus()
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		s.renderPartial(w, "service_status", status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleServiceRestart(w http.ResponseWriter, r *http.Request) {
	if err := s.process.Restart(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	status := s.process.GetStatus()
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		s.renderPartial(w, "service_status", status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleServiceLogs(w http.ResponseWriter, r *http.Request) {
	sse, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ch := s.process.Subscribe()
	defer s.process.Unsubscribe(ch)

	for {
		select {
		case line, ok := <-ch:
			if !ok {
				sse.SendEvent("done", "Router exited")
				return
			}
			sse.SendLine(line)
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleServiceLogTabs(w http.ResponseWriter, r *http.Request) {
	// With the native router, logs are combined — no tabs needed.
	// Return empty to hide the tab bar.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
}

func (s *Server) handleServiceHealth(w http.ResponseWriter, r *http.Request) {
	healthy := s.process.CheckHealth()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"healthy": healthy})
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

	// Load the model via the router API
	if err := s.process.LoadModel(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		s.handleListModels(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "loading", "model": id})
}

// handleDeactivateModel unloads a model via the router.
func (s *Server) handleDeactivateModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if err := s.process.UnloadModel(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
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
			if b.ID == s.cfg.ActiveBuild && b.Status == "success" {
				binaryPath = b.BinaryPath
				break
			}
		}
	}
	if binaryPath == "" {
		for _, b := range s.builder.List() {
			if b.Status == "success" {
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

	// Don't use --models-dir alongside --models-preset to avoid duplicate
	// entries (auto-discovery creates entries by filename, preset by our ID).
	// The preset INI already has model paths.
	return s.process.Start(process.RouterConfig{
		BinaryPath: binaryPath,
		PresetPath: presetPath,
		ModelsMax:  s.cfg.ModelsMax,
		Port:       s.cfg.LlamaPort,
	})
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
	weightsGB := float64(model.SizeBytes) / (1024 * 1024 * 1024)

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<strong>%.1f GB</strong> <small>(weights: %.1f GB + KV cache: %.1f GB + overhead)</small>`,
			total, weightsGB, kvGB)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
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

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		maxContext := 0
		if model != nil {
			maxContext = model.ContextLength
		}

		data := struct {
			ModelID        string
			Config         *models.ModelConfig
			EffectiveFlags string
			MaxContext     int
		}{
			ModelID:        id,
			Config:         cfg,
			EffectiveFlags: cfg.EffectiveFlags(),
			MaxContext:     maxContext,
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
		cfg.DirectIO = r.FormValue("direct_io") == "on"
		cfg.ExtraFlags = r.FormValue("extra_flags")

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

	// Regenerate preset INI so the router picks up changes on next load
	if _, err := s.registry.WritePresetINI(); err != nil {
		slog.Warn("failed to regenerate preset INI", "error", err)
	}

	// Update VRAM estimate in model list
	if r.Header.Get("HX-Request") == "true" {
		if model, err := s.registry.Get(id); err == nil {
			baseVRAM := float64(model.SizeBytes)/(1024*1024*1024) + 0.2
			peakVRAM := models.VRAMEstimateForConfig(model, &cfg)
			w.Header().Set("HX-Trigger", fmt.Sprintf(
				`{"vramUpdated":{"id":%q,"vram":"%.1f - %.1f GB"}}`,
				id, baseVRAM, peakVRAM))
		}
	}

	s.handleGetModelConfig(w, r)
}

