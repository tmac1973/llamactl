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

// applySpecDefaults resets speculative decoding parameters to recommended
// values for the selected mode. Call this only on a mode *change* — calling
// it on every save would clobber user-tuned values within an existing mode
// (the form parser already loaded them from the request into cfg).
func applySpecDefaults(cfg *models.ModelConfig) {
	switch cfg.SpecType {
	case "":
		cfg.DraftMax = 0
		cfg.DraftMin = 0
		cfg.DraftPMin = ""
		cfg.NgramSizeN = 0
		cfg.NgramSizeM = 0
	case "draft":
		cfg.DraftMax = 16
		cfg.DraftMin = 0
		cfg.DraftPMin = "0.75"
		cfg.NgramSizeN = 0
		cfg.NgramSizeM = 0
	case "ngram-simple", "ngram-cache", "ngram-map-k", "ngram-map-k4v":
		cfg.DraftMax = 16
		cfg.DraftMin = 0
		cfg.DraftPMin = ""
		cfg.NgramSizeN = 12
		cfg.NgramSizeM = 48
	case "ngram-mod":
		cfg.DraftMax = 64
		cfg.DraftMin = 48
		cfg.DraftPMin = ""
		cfg.NgramSizeN = 24
		cfg.NgramSizeM = 48
	}
}

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

// countNonZeroSplit counts the non-zero comma-separated entries in a
// tensor-split string. Used to derive GPU count from legacy configs.
func countNonZeroSplit(s string) int {
	if s == "" {
		return 0
	}
	n := 0
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" && p != "0" && p != "0.0" {
			n++
		}
	}
	return n
}

// handlePS returns currently loaded models with resource info, similar to Ollama's /api/ps.
func (s *Server) handlePS(w http.ResponseWriter, r *http.Request) {
	type psModel struct {
		Name        string  `json:"name"`
		Status      string  `json:"status"`
		VRAMEstGB   float64 `json:"vram_est_gb,omitempty"`
		ContextSize int     `json:"context_size,omitempty"`
		Arch        string  `json:"arch,omitempty"`
	}

	var result []psModel

	if s.process.IsRunning() {
		routerModels, err := s.process.ListModels()
		if err == nil {
			for _, rm := range routerModels {
				pm := psModel{
					Name:   rm.ID,
					Status: rm.Status.Value,
				}
				// Enrich with registry metadata if available
				// Try matching by router ID, then by aliases
				var regModel *models.Model
				for _, alias := range append([]string{rm.ID}, rm.Aliases...) {
					if m, err := s.registry.Get(alias); err == nil {
						regModel = m
						break
					}
				}
				if regModel != nil {
					pm.Arch = regModel.Arch
					pm.VRAMEstGB = regModel.VRAMEstGB
					if cfg, err := s.registry.GetConfig(regModel.ID); err == nil {
						pm.ContextSize = cfg.ContextSize
						pm.VRAMEstGB = models.VRAMEstimateForConfig(regModel, cfg)
					}
				}
				result = append(result, pm)
			}
		}
	}

	respondJSON(w, map[string]any{"models": result})
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
		// Clear dirty flags — fresh start uses the latest preset.ini
		s.dirtyModels = make(map[string]bool)
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
	// Clear dirty flags — the router just reloaded with the latest preset.ini
	s.dirtyModels = make(map[string]bool)
	s.handleServiceStatus(w, r)
}

func (s *Server) handleServiceLogs(w http.ResponseWriter, r *http.Request) {
	ch := s.process.Subscribe()
	defer s.process.Unsubscribe(ch)
	StreamLines(w, r.Context(), ch, "Router exited")
}

func (s *Server) handleServiceLogsClear(w http.ResponseWriter, r *http.Request) {
	s.process.ClearLogs()
	w.WriteHeader(http.StatusNoContent)
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
	// Find the build binary. Explicit selection wins; otherwise fall back
	// to the successful build with the newest GitRef.
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
		if b := s.builder.LatestSuccessfulBuild(); b != nil {
			binaryPath = b.BinaryPath
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
		// Re-render only the toggled row so the rest of the list keeps its
		// expand/collapse state. Initial=false omits display:none so the new
		// row stays visible (the user's click proves the row was visible).
		m, err := s.registry.Get(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		org, base := "", ""
		if !models.IsEmbeddingModel(m.ModelID) && !models.IsEmbeddingModel(m.ID) {
			org, base = m.OrgAndBase()
			if org == "" {
				org = "(local)"
			}
		}
		isOrphan := false
		for _, om := range s.registry.FindOrphans() {
			if om.ID == id {
				isOrphan = true
				break
			}
		}
		w.Header().Set("HX-Trigger-After-Swap", `{"gpuMapChanged":true}`)
		respondHTML(w)
		s.renderModelCard(w, m, org, base, s.routerKnownStates(), isOrphan, false)
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
		isEmbedding := false
		var draftCandidates []models.DraftCandidate
		if model != nil {
			maxContext = model.ContextLength
			detectedMMProj = models.FindMMProj(model.FilePath)
			isEmbedding = models.IsEmbeddingModel(model.ModelID) || models.IsEmbeddingModel(model.ID)
			if !isEmbedding {
				draftCandidates = s.registry.FindDraftCandidates(id)
			}
		}

		hasBuiltinVision := model != nil && model.HasBuiltinVision

		// GPU assignment options
		metrics := s.monitor.Current()
		numGPUs := len(metrics.GPU)
		gpuOptions := models.GPUAssignOptions(numGPUs)

		// Migration: map legacy configs onto the unified dropdown values.
		if cfg.GPUAssign == "" || cfg.GPUAssign == "tensor" {
			switch {
			case cfg.SplitMode == "tensor":
				// Derive N from tensor-split (count of non-zero entries); fall
				// back to all GPUs if not set.
				n := countNonZeroSplit(cfg.TensorSplit)
				if n <= 0 || n > numGPUs {
					n = numGPUs
				}
				if n >= 2 && n < numGPUs {
					cfg.GPUAssign = fmt.Sprintf("tensor-%d", n)
				} else {
					cfg.GPUAssign = fmt.Sprintf("tensor-%d", numGPUs)
				}
			case cfg.TensorSplit != "":
				cfg.GPUAssign = "custom"
			}
		}

		// Mark disabled/recommended options
		if numGPUs > 0 && model != nil {
			perGPUGB := float64(metrics.GPU[0].VRAMTotalMB) / 1024.0
			modelVRAM := models.ModelWeightsGB(model)
			allModels := s.registry.List()
			allConfigs := make(map[string]*models.ModelConfig)
			for _, m := range allModels {
				if c, err := s.registry.GetConfig(m.ID); err == nil {
					allConfigs[m.ID] = c
				}
			}
			existing := models.ComputeAllocations(allModels, allConfigs, numGPUs)
			// Exclude the current model from existing allocations
			var filtered []models.GPUAllocation
			for _, a := range existing {
				if a.ModelID != id {
					filtered = append(filtered, a)
				}
			}
			models.MarkRecommended(gpuOptions, modelVRAM, perGPUGB, filtered)
		}

		data := struct {
			ModelID          string
			Config           *models.ModelConfig
			EffectiveFlags   string
			MaxContext       int
			HasMMProj        bool
			HasBuiltinVision bool
			IsEmbedding      bool
			DraftCandidates  []models.DraftCandidate
			GPUOptions       []models.GPUOption
			NumGPUs          int
		}{
			ModelID:          id,
			Config:           cfg,
			EffectiveFlags:   cfg.EffectiveFlagsFor(isEmbedding),
			MaxContext:       maxContext,
			HasMMProj:        cfg.MmprojPath != "" || detectedMMProj != "",
			HasBuiltinVision: hasBuiltinVision,
			IsEmbedding:      isEmbedding,
			DraftCandidates:  draftCandidates,
			GPUOptions:       gpuOptions,
			NumGPUs:          numGPUs,
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

		// GPU assignment — single dropdown drives tensor-split, split-mode,
		// and main-gpu. "custom" preserves the raw tensor_split.
		gpuAssign := r.FormValue("gpu_assign")
		cfg.GPUAssign = gpuAssign
		numGPUs := len(s.monitor.Current().GPU)
		if gpuAssign == "custom" {
			cfg.TensorSplit = r.FormValue("tensor_split")
			cfg.SplitMode = ""
			cfg.MainGPU = 0
		} else {
			ts, sm, mg := models.ResolveGPUAssign(gpuAssign, numGPUs)
			cfg.TensorSplit = ts
			cfg.SplitMode = sm
			cfg.MainGPU = mg
		}
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

		if r.Form.Has("mmproj_path") {
			cfg.MmprojPath = r.FormValue("mmproj_path")
		}
		// Parse aliases (comma-separated, trimmed)
		if aliasStr := strings.TrimSpace(r.FormValue("aliases")); aliasStr != "" {
			var aliases []string
			for _, a := range strings.Split(aliasStr, ",") {
				a = strings.TrimSpace(a)
				if a != "" {
					aliases = append(aliases, a)
				}
			}
			cfg.Aliases = aliases
		} else {
			cfg.Aliases = nil
		}

		// Speculative decoding. Capture the previous SpecType so we can tell
		// whether the user just switched modes vs. is saving an existing one
		// — applySpecDefaults wipes user-tuned values, so we only want to
		// run it on a mode change.
		prevSpecType := cfg.SpecType
		cfg.SpecType = r.FormValue("spec_type")
		if r.Form.Has("draft_model_path") {
			cfg.DraftModelPath = r.FormValue("draft_model_path")
		}
		if v, err := strconv.Atoi(r.FormValue("draft_max")); err == nil && v > 0 {
			cfg.DraftMax = v
		} else {
			cfg.DraftMax = 0
		}
		if v, err := strconv.Atoi(r.FormValue("draft_min")); err == nil && v > 0 {
			cfg.DraftMin = v
		} else {
			cfg.DraftMin = 0
		}
		cfg.DraftPMin = r.FormValue("draft_p_min")
		if v, err := strconv.Atoi(r.FormValue("ngram_size_n")); err == nil && v > 0 {
			cfg.NgramSizeN = v
		} else {
			cfg.NgramSizeN = 0
		}
		if v, err := strconv.Atoi(r.FormValue("ngram_size_m")); err == nil && v > 0 {
			cfg.NgramSizeM = v
		} else {
			cfg.NgramSizeM = 0
		}

		// Populate recommended defaults only when the user actually switched
		// modes — preserves any custom values they tuned within an existing
		// mode (e.g. lowering ngram-mod's draft_min from 48 to 12).
		if cfg.SpecType != prevSpecType {
			applySpecDefaults(cfg)
		}
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
				`{"vramUpdated":{"id":%q,"vram":"%.1f - %.1f GB"},"gpuMapChanged":true}`,
				id, baseVRAM, peakVRAM))
		}
	}

	s.handleGetModelConfig(w, r)
}
