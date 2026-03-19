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

	// VRAM budget check — warn but don't block (estimates aren't exact).
	// Skip check if force=true query param is set, or if no GPU info available.
	vramWarning := ""
	if r.URL.Query().Get("force") != "true" {
		vramWarning = s.checkVRAMBudget(id, model, cfg)
	}

	// If htmx request with a VRAM warning, return a confirmation dialog.
	// HX-Retarget + HX-Reswap tell htmx to append the dialog to <body>
	// instead of replacing the model list.
	if vramWarning != "" && r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("HX-Retarget", "body")
		w.Header().Set("HX-Reswap", "beforeend")
		fmt.Fprintf(w, `<dialog open style="max-width:500px;">
			<article>
				<header>VRAM Warning</header>
				<p>%s</p>
				<footer>
					<div role="group">
						<button type="button" class="secondary"
							onclick="this.closest('dialog').remove();">Cancel</button>
						<button type="button"
							hx-put="/api/models/%s/activate?force=true"
							hx-target="#model-list"
							hx-swap="innerHTML"
							onclick="this.closest('dialog').remove();">Start Anyway</button>
					</div>
				</footer>
			</article>
		</dialog>`, html.EscapeString(vramWarning), html.EscapeString(id))
		return
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

// checkVRAMBudget estimates whether adding a model would exceed GPU VRAM.
// Returns a warning message if over budget, or empty string if OK.
func (s *Server) checkVRAMBudget(newModelID string, newModel *models.Model, newCfg *models.ModelConfig) string {
	metrics := s.monitor.Current()
	if len(metrics.GPU) == 0 {
		return "" // no GPU info available, skip check
	}

	// Determine which GPUs the new model will use
	targetGPUs := gpuIndicesForDevices(newCfg.GPUDevices, len(metrics.GPU))

	// Compute total VRAM available across target GPUs
	var totalVRAM float64
	for _, idx := range targetGPUs {
		if idx < len(metrics.GPU) {
			totalVRAM += float64(metrics.GPU[idx].VRAMTotalMB) / 1024.0
		}
	}
	if totalVRAM == 0 {
		return ""
	}

	// Sum VRAM estimates for already-active models on the same GPUs
	var usedVRAM float64
	for _, st := range s.process.ListActive() {
		activeModel, err := s.registry.Get(st.ID)
		if err != nil {
			continue
		}
		activeCfg, err := s.registry.GetConfig(st.ID)
		if err != nil {
			continue
		}
		activeGPUs := gpuIndicesForDevices(activeCfg.GPUDevices, len(metrics.GPU))
		// Check if this active model shares any GPU with the new model
		if gpuOverlap(targetGPUs, activeGPUs) {
			usedVRAM += models.VRAMEstimateForConfig(activeModel, activeCfg)
		}
	}

	newModelVRAM := models.VRAMEstimateForConfig(newModel, newCfg)
	projected := usedVRAM + newModelVRAM

	if projected > totalVRAM {
		return fmt.Sprintf(
			"Estimated total VRAM usage (%.1f GB) would exceed available GPU memory (%.1f GB). "+
				"Currently loaded: %.1f GB, this model: %.1f GB. "+
				"The model may fail to load or performance may degrade.",
			projected, totalVRAM, usedVRAM, newModelVRAM,
		)
	}

	return ""
}

// gpuIndicesForDevices returns the GPU indices a model targets.
// Empty devices string means all GPUs.
func gpuIndicesForDevices(devices string, numGPUs int) []int {
	if devices == "" {
		indices := make([]int, numGPUs)
		for i := range indices {
			indices[i] = i
		}
		return indices
	}
	var indices []int
	for _, s := range strings.Split(devices, ",") {
		s = strings.TrimSpace(s)
		if idx, err := strconv.Atoi(s); err == nil && idx >= 0 {
			indices = append(indices, idx)
		}
	}
	return indices
}

// gpuOverlap returns true if the two GPU index lists share any GPU.
func gpuOverlap(a, b []int) bool {
	set := make(map[int]struct{}, len(a))
	for _, idx := range a {
		set[idx] = struct{}{}
	}
	for _, idx := range b {
		if _, ok := set[idx]; ok {
			return true
		}
	}
	return false
}

type gpuOption struct {
	Value string
	Label string
}

// buildGPUOptions returns GPU device options based on detected hardware.
func (s *Server) buildGPUOptions() []gpuOption {
	metrics := s.monitor.Current()
	numGPUs := len(metrics.GPU)
	if numGPUs == 0 {
		numGPUs = 1 // fallback: assume at least 1 GPU
	}

	var opts []gpuOption

	// Individual GPU options
	for i := 0; i < numGPUs; i++ {
		label := fmt.Sprintf("GPU %d only", i)
		if i < len(metrics.GPU) && metrics.GPU[i].Name != "" {
			label = fmt.Sprintf("GPU %d (%s)", i, metrics.GPU[i].Name)
		}
		opts = append(opts, gpuOption{
			Value: strconv.Itoa(i),
			Label: label,
		})
	}

	// Multi-GPU combination options (only if >1 GPU)
	if numGPUs >= 2 {
		// Pairs
		for i := 0; i < numGPUs-1; i++ {
			for j := i + 1; j < numGPUs; j++ {
				opts = append(opts, gpuOption{
					Value: fmt.Sprintf("%d,%d", i, j),
					Label: fmt.Sprintf("GPU %d + %d", i, j),
				})
			}
		}
	}

	// All-but-one combinations for 3+ GPUs
	if numGPUs >= 3 {
		for skip := 0; skip < numGPUs; skip++ {
			var indices []string
			var labels []string
			for i := 0; i < numGPUs; i++ {
				if i == skip {
					continue
				}
				indices = append(indices, strconv.Itoa(i))
				labels = append(labels, strconv.Itoa(i))
			}
			opts = append(opts, gpuOption{
				Value: strings.Join(indices, ","),
				Label: fmt.Sprintf("GPU %s", strings.Join(labels, " + ")),
			})
		}
	}

	return opts
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

	model, _ := s.registry.Get(id)

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		maxContext := 0
		if model != nil {
			maxContext = model.ContextLength
		}

		data := struct {
			ModelID         string
			Config          *models.ModelConfig
			AvailableBuilds interface{}
			EffectiveFlags  string
			MaxContext      int
			GPUOptions      []gpuOption
		}{
			ModelID:         id,
			Config:          cfg,
			AvailableBuilds: s.builder.List(),
			EffectiveFlags:  cfg.EffectiveFlags(),
			MaxContext:      maxContext,
			GPUOptions:      s.buildGPUOptions(),
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

	// Compute updated VRAM range and send it via HX-Trigger so the
	// client-side JS can update the VRAM cell without refreshing the model list.
	if r.Header.Get("HX-Request") == "true" {
		if model, err := s.registry.Get(id); err == nil {
			baseVRAM := models.EstimateVRAM(model.SizeBytes)
			peakVRAM := models.VRAMEstimateForConfig(model, &cfg)
			w.Header().Set("HX-Trigger", fmt.Sprintf(
				`{"vramUpdated":{"id":%q,"vram":"%.1f – %.1f GB"}}`,
				id, baseVRAM, peakVRAM))
		}
	}

	// Return updated config form
	s.handleGetModelConfig(w, r)
}
