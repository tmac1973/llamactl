package models

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
	GPULayers   int    `json:"gpu_layers"`
	TensorSplit string `json:"tensor_split"`
	ContextSize int    `json:"context_size"`
	Threads     int    `json:"threads"`
	ExtraFlags  string `json:"extra_flags"`
	BuildID     string `json:"build_id"`
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
			GPULayers:   999,
			TensorSplit: "0.5,0.5",
			ContextSize: 8192,
			Threads:     8,
		}
	}
	r.save()
	return nil
}

// List returns all models.
func (r *Registry) List() []*Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Model, 0, len(r.data.Models))
	for _, m := range r.data.Models {
		out = append(out, m)
	}
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
