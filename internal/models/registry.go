package models

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sort"
	"sync"
	"time"
)

// Model represents a locally downloaded GGUF model.
type Model struct {
	ID           string    `json:"id"`
	ModelID      string    `json:"model_id"`
	Filename     string    `json:"filename"`
	Quant        string    `json:"quant"`
	SizeBytes    int64     `json:"size_bytes"`
	FilePath     string    `json:"file_path"`
	VRAMEstGB    float64   `json:"vram_est_gb"`
	DownloadedAt time.Time `json:"downloaded_at"`

	// Architecture parameters parsed from GGUF header.
	Arch          string `json:"arch,omitempty"`
	NLayers       int    `json:"n_layers,omitempty"`
	NEmbd         int    `json:"n_embd,omitempty"`
	NHead         int    `json:"n_head,omitempty"`
	NKVHead       int    `json:"n_kv_head,omitempty"`
	ContextLength int    `json:"context_length,omitempty"` // max trained context
}

// ModelConfig holds per-model launch configuration for llama-server.
type ModelConfig struct {
	Enabled        bool   `json:"enabled"`
	GPULayers      int    `json:"gpu_layers"`
	TensorSplit    string `json:"tensor_split"`
	ContextSize    int    `json:"context_size"`
	Threads        int    `json:"threads"`
	FlashAttention bool   `json:"flash_attention"`
	Jinja          bool   `json:"jinja"`
	KVCacheQuant   string `json:"kv_cache_quant"` // "", "q8_0", "q4_0"
	DirectIO       bool   `json:"direct_io"`       // bypass page cache, load straight to VRAM
	MmprojPath     string `json:"mmproj_path,omitempty"` // path to mmproj GGUF for vision models
	ExtraFlags     string `json:"extra_flags"`

	// Sampling parameters — nil means use llama.cpp server default.
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"top_p,omitempty"`
	TopK            *int     `json:"top_k,omitempty"`
	MinP            *float64 `json:"min_p,omitempty"`
	PresencePenalty *float64 `json:"presence_penalty,omitempty"`
	RepeatPenalty   *float64 `json:"repeat_penalty,omitempty"`
}

// SamplingOverrides returns a map of non-nil sampling parameters suitable
// for merging into an OpenAI-compatible request body.
func (c *ModelConfig) SamplingOverrides() map[string]any {
	m := make(map[string]any)
	if c.Temperature != nil {
		m["temperature"] = *c.Temperature
	}
	if c.TopP != nil {
		m["top_p"] = *c.TopP
	}
	if c.TopK != nil {
		m["top_k"] = *c.TopK
	}
	if c.MinP != nil {
		m["min_p"] = *c.MinP
	}
	if c.PresencePenalty != nil {
		m["presence_penalty"] = *c.PresencePenalty
	}
	if c.RepeatPenalty != nil {
		m["repeat_penalty"] = *c.RepeatPenalty
	}
	return m
}

// EffectiveFlags returns the full set of llama-server flags (excluding
// binary, model path, host, and port) that will be used at launch.
func (c *ModelConfig) EffectiveFlags() string {
	var parts []string
	parts = append(parts, "--n-gpu-layers", strconv.Itoa(c.GPULayers))
	parts = append(parts, "--ctx-size", strconv.Itoa(c.ContextSize))
	parts = append(parts, "--threads", strconv.Itoa(c.Threads))
	if c.TensorSplit != "" {
		parts = append(parts, "--tensor-split", c.TensorSplit)
	}
	if c.FlashAttention {
		parts = append(parts, "--flash-attn", "on")
	}
	if c.Jinja {
		parts = append(parts, "--jinja")
	}
	if c.KVCacheQuant != "" {
		parts = append(parts, "--cache-type-k", c.KVCacheQuant, "--cache-type-v", c.KVCacheQuant)
	}
	if c.DirectIO {
		parts = append(parts, "--direct-io")
	}
	if c.MmprojPath != "" {
		parts = append(parts, "--mmproj", c.MmprojPath)
	}
	if c.ExtraFlags != "" {
		parts = append(parts, strings.Fields(c.ExtraFlags)...)
	}
	return strings.Join(parts, " ")
}

type registryData struct {
	Models  map[string]*Model       `json:"models"`
	Configs map[string]*ModelConfig `json:"configs"`
}

// Registry manages local model storage and metadata.
type Registry struct {
	mu      sync.RWMutex
	dataDir string
	data    registryData
}

// NewRegistry creates a registry and loads persisted state.
func NewRegistry(dataDir string) *Registry {
	r := &Registry{
		dataDir: dataDir,
		data: registryData{
			Models:  make(map[string]*Model),
			Configs: make(map[string]*ModelConfig),
		},
	}
	r.load()
	return r
}

// Add registers a new model.
func (r *Registry) Add(m *Model) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data.Models[m.ID] = m
	// Set default config
	if _, exists := r.data.Configs[m.ID]; !exists {
		r.data.Configs[m.ID] = &ModelConfig{
			Enabled:        true,
			GPULayers:      999,
			TensorSplit:    "",
			ContextSize:    8192,
			Threads:        8,
			FlashAttention: true,
			Jinja:          true,
		}
	}
	r.save()
	return nil
}

// List returns all models, sorted alphabetically by ModelID.
func (r *Registry) List() []*Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Model, 0, len(r.data.Models))
	for _, m := range r.data.Models {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ModelID < out[j].ModelID
	})
	return out
}

// Get returns a model by ID.
func (r *Registry) Get(id string) (*Model, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.data.Models[id]
	if !ok {
		return nil, fmt.Errorf("model not found: %s", id)
	}
	return m, nil
}

// Remove removes a model entry from the registry without deleting files.
func (r *Registry) Remove(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.data.Models[id]; !ok {
		return fmt.Errorf("model not found: %s", id)
	}

	delete(r.data.Models, id)
	delete(r.data.Configs, id)
	r.save()
	return nil
}

// Delete removes a model entry and deletes its files from disk.
func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	m, ok := r.data.Models[id]
	if !ok {
		return fmt.Errorf("model not found: %s", id)
	}

	// Delete the GGUF file(s) — for sharded models, delete all parts
	shards := findShards(filepath.Dir(m.FilePath), filepath.Base(m.FilePath))
	for _, shard := range shards {
		os.Remove(shard)
		os.Remove(shard + ".part") // clean up any partial downloads too
	}

	// Remove empty directories left behind
	dir := filepath.Dir(m.FilePath)
	removeEmptyDirs(dir)

	delete(r.data.Models, id)
	delete(r.data.Configs, id)
	r.save()
	return nil
}

// removeEmptyDirs removes dir and its parent if they're empty, stopping at the models dir.
func removeEmptyDirs(dir string) {
	for {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			break
		}
		parent := filepath.Dir(dir)
		os.Remove(dir) // only succeeds if empty
		if parent == dir {
			break
		}
		dir = parent
	}
}

// GetConfig returns the launch config for a model.
func (r *Registry) GetConfig(id string) (*ModelConfig, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cfg, ok := r.data.Configs[id]
	if !ok {
		return nil, fmt.Errorf("config not found: %s", id)
	}
	return cfg, nil
}

// SetConfig updates the launch config for a model.
func (r *Registry) SetConfig(id string, cfg *ModelConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data.Models[id]; !ok {
		return fmt.Errorf("model not found: %s", id)
	}
	r.data.Configs[id] = cfg
	r.save()
	return nil
}

// BackfillGGUFMeta parses GGUF metadata for any models missing architecture
// info. Called at startup to handle models downloaded before GGUF parsing existed.
func (r *Registry) BackfillGGUFMeta() {
	r.mu.Lock()
	defer r.mu.Unlock()

	changed := false
	for _, m := range r.data.Models {
		if m.NLayers > 0 {
			continue // already has metadata
		}
		meta, err := ParseGGUFMeta(m.FilePath)
		if err != nil {
			slog.Warn("failed to parse GGUF metadata", "model", m.ID, "error", err)
			continue
		}
		m.Arch = meta.Architecture
		m.NLayers = meta.NLayers
		m.NEmbd = meta.NEmbd
		m.NHead = meta.NHead
		m.NKVHead = meta.NKVHead
		m.ContextLength = meta.ContextLength
		changed = true
		slog.Info("backfilled GGUF metadata", "model", m.ID, "arch", meta.Architecture,
			"layers", meta.NLayers, "kv_heads", meta.NKVHead, "ctx", meta.ContextLength)
	}
	if changed {
		r.save()
	}
}

// DeduplicateModels removes duplicate registry entries that point to the same file.
// Keeps the first entry found (by ID sort order) and removes the rest.
func (r *Registry) DeduplicateModels() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	seen := make(map[string]string) // file path → first model ID
	var dupes []string

	// Sort IDs for deterministic behavior
	ids := make([]string, 0, len(r.data.Models))
	for id := range r.data.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		m := r.data.Models[id]
		if existing, ok := seen[m.FilePath]; ok {
			slog.Info("removing duplicate model entry", "id", id, "kept", existing, "path", m.FilePath)
			dupes = append(dupes, id)
		} else {
			seen[m.FilePath] = id
		}
	}

	for _, id := range dupes {
		delete(r.data.Models, id)
		delete(r.data.Configs, id)
	}

	if len(dupes) > 0 {
		r.save()
	}
	return len(dupes)
}

// FindOrphans returns registry entries whose model files no longer exist on disk.
func (r *Registry) FindOrphans() []*Model {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var orphans []*Model
	for _, m := range r.data.Models {
		if _, err := os.Stat(m.FilePath); os.IsNotExist(err) {
			orphans = append(orphans, m)
		}
	}
	return orphans
}

// ScanModels walks the models directory for GGUF files not already in the
// registry and adds them. Returns the number of new models found.
func (r *Registry) ScanModels() int {
	modelsDir := filepath.Join(r.dataDir, "models")
	if _, err := os.Stat(modelsDir); err != nil {
		return 0
	}

	// Build set of known file paths for fast lookup
	r.mu.RLock()
	knownPaths := make(map[string]bool, len(r.data.Models))
	for _, m := range r.data.Models {
		knownPaths[m.FilePath] = true
	}
	r.mu.RUnlock()

	// Walk looking for .gguf files
	var found []*Model
	filepath.Walk(modelsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(info.Name()), ".gguf") {
			return nil
		}
		// Skip .part files (incomplete downloads)
		if strings.HasSuffix(path, ".part") {
			return nil
		}
		// Skip if already registered
		if knownPaths[path] {
			return nil
		}
		// Skip shard parts beyond the first (we'll register the first shard as the model)
		if isNonFirstShard(info.Name()) {
			return nil
		}
		// Skip mmproj files — they're vision projectors, not models
		if IsMMProjFile(info.Name()) {
			return nil
		}

		// Derive model info from directory structure and filename
		// Expected: /data/models/{org--repo}/{filename}.gguf
		// or:       /data/models/{org--repo}/{subdir}/{filename}.gguf
		rel, _ := filepath.Rel(modelsDir, path)
		parts := strings.SplitN(rel, string(filepath.Separator), 2)

		dirName := parts[0]                              // e.g., "unsloth--Qwen3.5-27B-GGUF"
		filename := info.Name()                          // e.g., "Qwen3.5-27B-Q4_K_M.gguf"
		modelID := strings.ReplaceAll(dirName, "--", "/") // e.g., "unsloth/Qwen3.5-27B-GGUF"

		safeName := dirName
		safeFilename := strings.ReplaceAll(strings.TrimSuffix(rel, ".gguf"), string(filepath.Separator), "--")
		id := fmt.Sprintf("%s--%s", safeName, strings.TrimSuffix(filename, ".gguf"))
		if len(parts) > 1 {
			// Has subdirectory — use the full relative path for the ID
			id = safeFilename
			// Prefix with org--repo if not already
			if !strings.HasPrefix(id, safeName) {
				id = safeName + "--" + id
			}
		}

		// Calculate total size (sum shards if multi-part)
		totalSize := info.Size()
		shardFiles := findShards(filepath.Dir(path), filename)
		if len(shardFiles) > 1 {
			totalSize = 0
			for _, sf := range shardFiles {
				if si, err := os.Stat(sf); err == nil {
					totalSize += si.Size()
				}
			}
		}

		m := &Model{
			ID:           id,
			ModelID:      modelID,
			Filename:     filename,
			Quant:        ParseQuant(filename),
			SizeBytes:    totalSize,
			FilePath:     path,
			VRAMEstGB:    EstimateVRAM(totalSize),
			DownloadedAt: info.ModTime(),
		}

		// Parse GGUF metadata
		if meta, err := ParseGGUFMeta(path); err == nil {
			m.Arch = meta.Architecture
			m.NLayers = meta.NLayers
			m.NEmbd = meta.NEmbd
			m.NHead = meta.NHead
			m.NKVHead = meta.NKVHead
			m.ContextLength = meta.ContextLength
		}

		found = append(found, m)
		return nil
	})

	for _, m := range found {
		r.Add(m)
		slog.Info("scanned model", "id", m.ID, "file", m.FilePath,
			"size_gb", fmt.Sprintf("%.1f", float64(m.SizeBytes)/(1024*1024*1024)),
			"arch", m.Arch)
	}

	return len(found)
}

// IsMMProjFile returns true if the filename looks like a multimodal projector.
func IsMMProjFile(filename string) bool {
	return strings.Contains(strings.ToLower(filename), "mmproj")
}

// FindMMProj looks for mmproj GGUF files in the same directory as the model.
// Returns the path to the first one found, or empty string.
func FindMMProj(modelFilePath string) string {
	dir := filepath.Dir(modelFilePath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(strings.ToLower(name), ".gguf") && IsMMProjFile(name) {
			return filepath.Join(dir, name)
		}
	}
	return ""
}

// AutoDetectMMProj scans all registered models and sets MmprojPath on
// configs where an mmproj file exists in the model directory but isn't
// configured yet.
func (r *Registry) AutoDetectMMProj() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	found := 0
	for id, m := range r.data.Models {
		cfg := r.data.Configs[id]
		if cfg == nil || cfg.MmprojPath != "" {
			continue
		}
		if mmproj := FindMMProj(m.FilePath); mmproj != "" {
			cfg.MmprojPath = mmproj
			found++
		}
	}
	if found > 0 {
		r.save()
	}
	return found
}

// shardRe matches shard filenames like "model-00002-of-00005.gguf"
var shardRe = regexp.MustCompile(`-(\d{5})-of-(\d{5})\.gguf$`)

// isNonFirstShard returns true if filename is a shard part other than 00001.
func isNonFirstShard(filename string) bool {
	m := shardRe.FindStringSubmatch(filename)
	if m == nil {
		return false
	}
	return m[1] != "00001"
}

// findShards returns all shard file paths if filename is part of a multi-part set.
// Returns a single-element slice for non-sharded files.
func findShards(dir, filename string) []string {
	m := shardRe.FindStringSubmatch(filename)
	if m == nil {
		return []string{filepath.Join(dir, filename)}
	}

	total, err := strconv.Atoi(m[2])
	if err != nil || total < 2 {
		return []string{filepath.Join(dir, filename)}
	}

	// Extract the base name (everything before -NNNNN-of-NNNNN.gguf)
	loc := shardRe.FindStringIndex(filename)
	base := filename[:loc[0]]

	var shards []string
	for i := 1; i <= total; i++ {
		shard := filepath.Join(dir, fmt.Sprintf("%s-%05d-of-%05d.gguf", base, i, total))
		shards = append(shards, shard)
	}
	return shards
}

// ParseQuant extracts quantization type from a GGUF filename.
// Exported so it can be shared across packages.
func ParseQuant(filename string) string {
	name := strings.TrimSuffix(filepath.Base(filename), ".gguf")
	name = strings.TrimSuffix(name, ".GGUF")

	// Remove shard suffix if present
	if idx := strings.LastIndex(name, "-00001-of-"); idx > 0 {
		name = name[:idx]
	}

	// Normalize dashes to underscores so "UD-Q8_K_XL" matches "UD_Q8_K_XL"
	upper := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))

	// Longest match first to avoid partial matches (e.g., Q8_K_XL before Q8_K)
	quants := []string{
		// Ultra-dynamic
		"UD_Q8_K_XL", "UD_Q6_K_XL", "UD_Q4_K_XL",
		// Ternary / ultra-low bit
		"TQ1_0", "TQ2_0",
		// Importance-weighted quants
		"IQ1_S", "IQ1_M", "IQ2_XXS", "IQ2_XS", "IQ2_S", "IQ2_M",
		"IQ3_XXS", "IQ3_XS", "IQ3_S", "IQ3_M", "IQ4_XS", "IQ4_NL",
		// K-quants (longest suffixes first)
		"Q2_K_S", "Q2_K",
		"Q3_K_S", "Q3_K_M", "Q3_K_L", "Q3_K_XL", "Q3_K",
		"Q4_K_S", "Q4_K_M", "Q4_K_L", "Q4_K_XL", "Q4_K", "Q4_0", "Q4_1",
		"Q5_K_S", "Q5_K_M", "Q5_K_L", "Q5_K_XL", "Q5_K", "Q5_0", "Q5_1",
		"Q6_K_L", "Q6_K_XL", "Q6_K",
		"Q8_K_XL", "Q8_K_L", "Q8_K", "Q8_0", "Q8_1",
		// Full precision
		"F16", "F32", "BF16",
	}

	for _, q := range quants {
		if strings.Contains(upper, q) {
			return q
		}
	}
	return "unknown"
}

func (r *Registry) registryPath() string {
	return filepath.Join(r.dataDir, "config", "models.json")
}

func (r *Registry) load() {
	data, err := os.ReadFile(r.registryPath())
	if err != nil {
		return
	}
	if err := json.Unmarshal(data, &r.data); err != nil {
		slog.Error("failed to load model registry", "error", err)
	}
	if r.data.Models == nil {
		r.data.Models = make(map[string]*Model)
	}
	if r.data.Configs == nil {
		r.data.Configs = make(map[string]*ModelConfig)
	}
}

func (r *Registry) save() {
	os.MkdirAll(filepath.Dir(r.registryPath()), 0o755)
	data, err := json.MarshalIndent(r.data, "", "  ")
	if err != nil {
		slog.Error("failed to marshal model registry", "error", err)
		return
	}
	os.WriteFile(r.registryPath(), data, 0o644)
}
