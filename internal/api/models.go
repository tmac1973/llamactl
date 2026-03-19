package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/tmlabonte/llamactl/internal/models"
)

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	modelList := s.registry.List()

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if len(modelList) == 0 {
			w.Write([]byte(`<p>No models downloaded yet. <a href="/models/browse">Browse HuggingFace</a> to download models.</p>`))
			return
		}

		// Build set of active model IDs
		activeSet := make(map[string]string) // model registry ID → state
		for _, st := range s.process.ListActive() {
			activeSet[st.ID] = st.State
		}

		w.Write([]byte(`<table role="grid"><thead><tr><th>Model</th><th>Quant</th><th title="Base (weights) – Peak (full KV cache)">VRAM Est.</th><th>Size</th><th></th></tr></thead>`))
		for _, m := range modelList {
			state := activeSet[m.ID]

			// Compute VRAM range: base (weights + overhead) and peak (+ full KV cache)
			baseVRAM := models.EstimateVRAM(m.SizeBytes) // weights + overhead
			peakVRAM := baseVRAM                          // fallback if no GGUF metadata
			if cfg, err := s.registry.GetConfig(m.ID); err == nil {
				peakVRAM = models.VRAMEstimateForConfig(m, cfg)
			}

			data := struct {
				models.Model
				IsActive     bool
				ServiceState string
				BaseVRAMGB   float64
				PeakVRAMGB   float64
			}{
				Model:        *m,
				IsActive:     state == "running" || state == "starting",
				ServiceState: state,
				BaseVRAMGB:   baseVRAM,
				PeakVRAMGB:   peakVRAM,
			}
			s.renderPartial(w, "model_card", data)
		}
		w.Write([]byte(`</table>`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(modelList)
}

func (s *Server) handleScanModels(w http.ResponseWriter, r *http.Request) {
	found := s.registry.ScanModels()

	if r.Header.Get("HX-Request") == "true" {
		// Re-render the model list with any newly discovered models
		s.handleListModels(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"new_models": found})
}

func (s *Server) handleGetModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	m, err := s.registry.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(m)
}

func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.registry.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.WriteHeader(http.StatusOK)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
