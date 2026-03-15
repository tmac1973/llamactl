package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/tmlabonte/llamactl/internal/models"
	"github.com/tmlabonte/llamactl/internal/process"
)

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
	status := s.process.GetStatus()
	if status.State == "running" || status.State == "starting" {
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			s.renderPartial(w, "service_status", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
		return
	}

	http.Error(w, "No model active. Activate a model from the Models page first.", http.StatusBadRequest)
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
				sse.SendEvent("done", "Process exited")
				return
			}
			sse.SendLine(line)
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleServiceHealth(w http.ResponseWriter, r *http.Request) {
	healthy := s.process.CheckHealth()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"healthy": healthy})
}

// handleActivateModel stops the current service, applies model config, and starts.
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

	// Stop current process if running
	s.process.Stop()

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
		Port:           s.cfg.LlamaPort,
		ExtraFlags:     extraFlags,
	}

	if err := s.process.Start(launchCfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	status := s.process.GetStatus()
	status.Model = model.ModelID
	status.BuildID = cfg.BuildID

	if r.Header.Get("HX-Request") == "true" {
		// Re-render the model list so the active model is highlighted
		s.handleListModels(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
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
	}

	if err := s.registry.SetConfig(id, &cfg); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Return updated config form
	s.handleGetModelConfig(w, r)
}
