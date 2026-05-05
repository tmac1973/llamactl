package api

import (
	"log/slog"
	"net/http"
	"sort"
	"strings"

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
		s.renderModelList(w, r, embeddingModels, false)
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

		s.renderModelList(w, r, modelList, true)
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
		w.Header().Set("HX-Trigger", `{"gpuMapChanged":true,"modelsChanged":true}`)
		w.WriteHeader(http.StatusOK)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// routerKnownStates queries the router for all known models and returns a map
// from {model ID, name, alias} → status value. Empty map if the router is down.
func (s *Server) routerKnownStates() map[string]string {
	routerKnown := make(map[string]string)
	routerModels, err := s.process.ListModels()
	if err != nil {
		return routerKnown
	}
	for _, rm := range routerModels {
		routerKnown[rm.ID] = rm.Status.Value
		if rm.Model != "" {
			routerKnown[rm.Model] = rm.Status.Value
		}
		for _, alias := range rm.Aliases {
			routerKnown[alias] = rm.Status.Value
		}
	}
	return routerKnown
}

// renderModelCard writes one model_card partial.
func (s *Server) renderModelCard(w http.ResponseWriter, m *models.Model, routerKnown map[string]string, isOrphan bool) {
	// Look up router state under any of the names the router might know this
	// model by. The router's primary ID is the auto-discovery section name
	// (RouterName), but it may also surface m.ID or PublicName via aliases.
	// Falling back through all three avoids a false-positive "restart needed"
	// indicator when the router does know about the model under a different key.
	routerName := s.registry.RouterName(m.ID)
	state := routerKnown[routerName]
	if state == "" {
		state = routerKnown[m.ID]
	}
	if state == "" {
		state = routerKnown[m.PublicName()]
	}

	vramGB := models.BytesToGB(m.SizeBytes) + 0.2
	enabled := true
	hasVision := m.HasBuiltinVision
	gpuLabel := ""
	var aliases []string
	if cfg, err := s.registry.GetConfig(m.ID); err == nil {
		vramGB = models.VRAMEstimateForConfig(m, cfg)
		enabled = cfg.Enabled
		if cfg.MmprojPath != "" {
			hasVision = true
		}
		if cfg.GPUAssign != "" && cfg.GPUAssign != "all" {
			gpuLabel = models.GPUAssignLabel(cfg.GPUAssign)
		}
		aliases = cfg.Aliases
	}

	pendingEnable := enabled && state == "" && s.process.IsRunning()
	pendingDisable := !enabled && state != "" && s.process.IsRunning()
	configChanged := s.dirtyModels[m.ID] && state != "" && s.process.IsRunning()

	org, base := m.OrgAndBase()
	searchText := strings.ToLower(strings.Join([]string{
		org, base, m.Quant, m.PublicName(), m.ModelID, m.Arch,
		strings.Join(aliases, " "),
	}, " "))

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
		VRAMGB         float64
		IsOrphan       bool
		SearchText     string
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
		VRAMGB:         vramGB,
		IsOrphan:       isOrphan,
		SearchText:     searchText,
	}
	s.renderPartial(w, "model_card", data)
}

// renderModelList renders the shared model list used by both chat and embedding
// sections as a flat list of cards, sorted by base name then quant. When
// withFilter is true, a filter input is emitted above the list.
func (s *Server) renderModelList(w http.ResponseWriter, r *http.Request, modelList []*models.Model, withFilter bool) {
	routerKnown := s.routerKnownStates()

	orphanSet := make(map[string]bool)
	for _, m := range s.registry.FindOrphans() {
		orphanSet[m.ID] = true
	}

	sorted := make([]*models.Model, len(modelList))
	copy(sorted, modelList)
	sort.SliceStable(sorted, func(i, j int) bool {
		_, bi := sorted[i].OrgAndBase()
		_, bj := sorted[j].OrgAndBase()
		bi, bj = strings.ToLower(bi), strings.ToLower(bj)
		if bi != bj {
			return bi < bj
		}
		return strings.ToLower(sorted[i].Quant) < strings.ToLower(sorted[j].Quant)
	})

	if withFilter {
		w.Write([]byte(`<div class="model-list-controls"><input type="search" class="model-filter" placeholder="Filter by name, quant, architecture…" oninput="filterModels(this.value)" autocomplete="off"></div>`))
	}

	w.Write([]byte(`<div class="model-card-list">`))
	w.Write([]byte(`<div class="model-card-header"><span></span><span>Model</span><span>Quant</span><span title="Estimated VRAM at the configured context size">VRAM Est.</span><span>Size</span><span></span></div>`))
	for _, m := range sorted {
		s.renderModelCard(w, m, routerKnown, orphanSet[m.ID])
	}
	w.Write([]byte(`</div>`))
}
