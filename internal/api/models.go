package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/tmlabonte/llamactl/internal/models"
)

func (s *Server) handleListEmbeddingModels(w http.ResponseWriter, r *http.Request) {
	all := s.registry.List()
	var embeddingModels []*models.Model
	for _, m := range all {
		if models.IsEmbeddingModel(m.ModelID) || models.IsEmbeddingModel(m.ID) {
			embeddingModels = append(embeddingModels, m)
		}
	}

	if isHTMX(r) {
		respondHTML(w)
		if len(embeddingModels) == 0 {
			return
		}
		s.renderModelTable(w, r, embeddingModels)
		return
	}

	respondJSON(w, embeddingModels)
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	all := s.registry.List()
	// Filter out embedding models — they have their own section
	var modelList []*models.Model
	for _, m := range all {
		if !models.IsEmbeddingModel(m.ModelID) && !models.IsEmbeddingModel(m.ID) {
			modelList = append(modelList, m)
		}
	}

	if isHTMX(r) {
		respondHTML(w)
		if len(modelList) == 0 {
			w.Write([]byte(`<p>No models downloaded yet. <a href="/models/browse">Browse HuggingFace</a> to download models.</p>`))
			return
		}

		s.renderModelTable(w, r, modelList)
		return
	}

	respondJSON(w, modelList)
}

func (s *Server) handleEmbeddingPresets(w http.ResponseWriter, r *http.Request) {
	presets := models.CuratedEmbeddingModels()

	// Mark which ones are already downloaded
	allModels := s.registry.List()
	downloaded := make(map[string]bool)
	for _, m := range allModels {
		downloaded[m.ModelID] = true
	}

	if isHTMX(r) {
		respondHTML(w)
		s.renderPartial(w, "embedding_presets", struct {
			Presets    []models.EmbeddingModelPreset
			Downloaded map[string]bool
		}{Presets: presets, Downloaded: downloaded})
		return
	}

	respondJSON(w, presets)
}

func (s *Server) handleDownloadEmbeddingPreset(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	repo := r.FormValue("repo")
	filename := r.FormValue("filename")

	if repo == "" || filename == "" {
		http.Error(w, "missing repo or filename", http.StatusBadRequest)
		return
	}

	downloadID, err := s.downloader.Start(r.Context(), repo, filename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if isHTMX(r) {
		respondHTML(w)
		s.renderPartial(w, "download_progress", struct {
			DownloadID string
			Filename   string
		}{DownloadID: downloadID, Filename: filename})
		return
	}

	respondJSON(w, map[string]string{"download_id": downloadID})
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

// handleModelInfo returns enriched model metadata with capabilities and config.
func (s *Server) handleModelInfo(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	m, err := s.registry.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	cfg, _ := s.registry.GetConfig(id)

	// Build capabilities list
	var capabilities []string
	if models.IsEmbeddingModel(m.ModelID) || models.IsEmbeddingModel(m.ID) {
		capabilities = append(capabilities, "embedding")
	} else {
		capabilities = append(capabilities, "chat")
	}
	if m.SupportsTools {
		capabilities = append(capabilities, "tools")
	}
	if m.HasBuiltinVision || (cfg != nil && cfg.MmprojPath != "") {
		capabilities = append(capabilities, "vision")
	}

	info := map[string]any{
		"id":             m.ID,
		"model_id":       m.ModelID,
		"filename":       m.Filename,
		"arch":           m.Arch,
		"quant":          m.Quant,
		"context_length": m.ContextLength,
		"size_bytes":     m.SizeBytes,
		"vram_est_gb":    m.VRAMEstGB,
		"capabilities":   capabilities,
		"downloaded_at":  m.DownloadedAt,
	}

	if cfg != nil {
		configMap := map[string]any{
			"enabled":         cfg.Enabled,
			"gpu_layers":      cfg.GPULayers,
			"context_size":    cfg.ContextSize,
			"threads":         cfg.Threads,
			"flash_attention": cfg.FlashAttention,
		}
		if cfg.TensorSplit != "" {
			configMap["tensor_split"] = cfg.TensorSplit
		}
		if cfg.KVCacheQuant != "" {
			configMap["kv_cache_quant"] = cfg.KVCacheQuant
		}
		if cfg.MmprojPath != "" {
			configMap["mmproj_path"] = cfg.MmprojPath
		}
		info["config"] = configMap
	}

	respondJSON(w, info)
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

// renderModelTable renders the shared model table used by both chat and embedding lists.
func (s *Server) renderModelTable(w http.ResponseWriter, r *http.Request, modelList []*models.Model) {
	// Build set of models the router knows about (any status)
	routerKnown := make(map[string]string)
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

	orphanSet := make(map[string]bool)
	for _, m := range s.registry.FindOrphans() {
		orphanSet[m.ID] = true
	}

	w.Write([]byte(`<table role="grid"><thead><tr><th title="Enable model for the inference server">On</th><th>Model</th><th>Quant</th><th title="Base (weights) - Peak (full KV cache)">VRAM Est.</th><th>Size</th><th></th></tr></thead>`))
	for _, m := range modelList {
		state := routerKnown[m.ID]

		weightsGB := models.BytesToGB(m.SizeBytes) + 0.2
		peakVRAM := weightsGB
		enabled := true
		hasVision := m.HasBuiltinVision
		gpuLabel := ""
		if cfg, err := s.registry.GetConfig(m.ID); err == nil {
			peakVRAM = models.VRAMEstimateForConfig(m, cfg)
			enabled = cfg.Enabled
			if cfg.MmprojPath != "" {
				hasVision = true
			}
			if cfg.GPUAssign != "" && cfg.GPUAssign != "all" {
				gpuLabel = models.GPUAssignLabel(cfg.GPUAssign)
			}
		}
		baseVRAM := weightsGB

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
			GPULabel       string
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
			GPULabel:       gpuLabel,
			ServiceState:   state,
			BaseVRAMGB:     baseVRAM,
			PeakVRAMGB:     peakVRAM,
			IsOrphan:       orphanSet[m.ID],
		}
		s.renderPartial(w, "model_card", data)
	}
	w.Write([]byte(`</table>`))
}
