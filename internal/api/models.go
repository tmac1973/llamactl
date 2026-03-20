package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/tmlabonte/llamactl/internal/models"
)

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	modelList := s.registry.List()

	if isHTMX(r) {
		respondHTML(w)
		if len(modelList) == 0 {
			w.Write([]byte(`<p>No models downloaded yet. <a href="/models/browse">Browse HuggingFace</a> to download models.</p>`))
			return
		}

		// Build set of models the router knows about (any status)
		routerKnown := make(map[string]string) // name/alias → status value
		if routerModels, err := s.process.ListModels(); err == nil {
			for _, rm := range routerModels {
				routerKnown[rm.ID] = rm.Status.Value
				if rm.Model != "" {
					routerKnown[rm.Model] = rm.Status.Value
				}
				for _, alias := range rm.Aliases {
					routerKnown[alias] = rm.Status.Value
				}
			}
		}

		// Build set of orphaned model IDs (file missing on disk)
		orphanSet := make(map[string]bool)
		for _, m := range s.registry.FindOrphans() {
			orphanSet[m.ID] = true
		}

		w.Write([]byte(`<table role="grid"><thead><tr><th title="Enable model for the inference server">On</th><th>Model</th><th>Quant</th><th title="Base (weights) - Peak (full KV cache)">VRAM Est.</th><th>Size</th><th></th></tr></thead>`))
		for _, m := range modelList {
			state := routerKnown[m.ID]

			// Compute VRAM range: base (weights + overhead) and peak (+ full KV cache)
			weightsGB := models.BytesToGB(m.SizeBytes) + 0.2
			peakVRAM := weightsGB // fallback if no config
			enabled := true
			hasVision := false
			if cfg, err := s.registry.GetConfig(m.ID); err == nil {
				peakVRAM = models.VRAMEstimateForConfig(m, cfg)
				enabled = cfg.Enabled
				hasVision = cfg.MmprojPath != ""
			}
			baseVRAM := weightsGB

			// Restart indicators: the router caches preset.ini at startup.
			// All preset changes (enable/disable/config) require a restart.
			// pendingEnable: enabled in registry but router doesn't know about it
			// pendingDisable: disabled in registry but router still has it
			// configChanged: config modified since router started
			pendingEnable := enabled && state == "" && s.process.IsRunning()
			pendingDisable := !enabled && state != "" && s.process.IsRunning()
			configChanged := s.dirtyModels[m.ID] && state != "" && s.process.IsRunning()

			data := struct {
				models.Model
				IsActive       bool
				IsEnabled      bool
				PendingEnable  bool
				PendingDisable bool
				NeedsReload    bool
				HasVision      bool
				ServiceState   string
				BaseVRAMGB     float64
				PeakVRAMGB     float64
				IsOrphan       bool
			}{
				Model:          *m,
				IsActive:       state == "loaded" || state == "loading",
				IsEnabled:      enabled,
				PendingEnable:  pendingEnable,
				PendingDisable: pendingDisable,
				NeedsReload:    configChanged,
				HasVision:      hasVision,
				ServiceState:   state,
				BaseVRAMGB:     baseVRAM,
				PeakVRAMGB:     peakVRAM,
				IsOrphan:       orphanSet[m.ID],
			}
			s.renderPartial(w, "model_card", data)
		}
		w.Write([]byte(`</table>`))
		return
	}

	respondJSON(w, modelList)
}

func (s *Server) handleScanModels(w http.ResponseWriter, r *http.Request) {
	found := s.registry.ScanModels()

	if isHTMX(r) {
		// Re-render the model list with any newly discovered models
		s.handleListModels(w, r)
		return
	}

	respondJSON(w, map[string]int{"new_models": found})
}

func (s *Server) handleGetModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	m, err := s.registry.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	respondJSON(w, m)
}

func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var err error
	if r.URL.Query().Get("keep_files") == "true" {
		err = s.registry.Remove(id)
	} else {
		err = s.registry.Delete(id)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Regenerate preset INI so the router doesn't reference a deleted model
	if _, err := s.registry.WritePresetINI(); err != nil {
		slog.Warn("failed to regenerate preset INI after delete", "error", err)
	}

	if isHTMX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
