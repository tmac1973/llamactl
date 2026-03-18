package models

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
}

// ModelConfig holds per-model launch configuration for llama-server.
type ModelConfig struct {
	GPULayers      int    `json:"gpu_layers"`
	TensorSplit    string `json:"tensor_split"`
	ContextSize    int    `json:"context_size"`
	Threads        int    `json:"threads"`
	FlashAttention bool   `json:"flash_attention"`
	Jinja          bool   `json:"jinja"`
	KVCacheQuant   string `json:"kv_cache_quant"` // "", "q8_0", "q4_0"
	ExtraFlags     string `json:"extra_flags"`
	BuildID        string `json:"build_id"`
	GPUDevices     string `json:"gpu_devices"` // "", "0", "1", "0,1" — empty = all GPUs

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
			GPULayers:      999,
			TensorSplit:    "0.5,0.5",
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

// Delete removes a model and its files.
func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	m, ok := r.data.Models[id]
	if !ok {
		return fmt.Errorf("model not found: %s", id)
	}

	// Remove model directory
	modelDir := filepath.Dir(m.FilePath)
	os.RemoveAll(modelDir)

	delete(r.data.Models, id)
	delete(r.data.Configs, id)
	r.save()
	return nil
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
