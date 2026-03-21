package benchmark

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/tmlabonte/llamactl/internal/monitor"
)

const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// BenchmarkRun is one complete benchmark execution.
type BenchmarkRun struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`

	// What was tested
	ModelID   string  `json:"model_id"`
	ModelName string  `json:"model_name"`
	Quant     string  `json:"quant"`
	SizeGB    float64 `json:"size_gb"`

	// Configuration snapshot
	Config ConfigSnapshot `json:"config"`

	// Build info
	BuildID      string `json:"build_id"`
	BuildRef     string `json:"build_ref"`
	BuildProfile string `json:"build_profile"`

	// Hardware
	GPUs []GPUSnapshot `json:"gpus"`

	// Parameters
	Preset       string `json:"preset"`
	PromptTokens []int  `json:"prompt_tokens"`
	GenTokens    int    `json:"gen_tokens"`

	// Results
	Results    []BenchmarkResult  `json:"results,omitempty"`
	Summary    *BenchmarkSummary  `json:"summary,omitempty"`
	LlamaBench *LlamaBenchResult  `json:"llama_bench,omitempty"`

	// Progress (transient, not persisted — only meaningful while running)
	ProgressDetail string `json:"progress_detail,omitempty"`

	// Duration
	DurationMs int64 `json:"duration_ms,omitempty"`
}

// ConfigSnapshot freezes model config at benchmark time.
type ConfigSnapshot struct {
	GPULayers      int    `json:"gpu_layers"`
	ContextSize    int    `json:"context_size"`
	GPUAssign      string `json:"gpu_assign,omitempty"`
	TensorSplit    string `json:"tensor_split,omitempty"`
	FlashAttention bool   `json:"flash_attention"`
	KVCacheQuant   string `json:"kv_cache_quant,omitempty"`
	Threads        int    `json:"threads"`
	SpecType       string `json:"spec_type,omitempty"`
}

// GPUSnapshot captures GPU hardware at benchmark time.
type GPUSnapshot struct {
	Index       int    `json:"index"`
	Name        string `json:"name"`
	VRAMTotalMB int    `json:"vram_total_mb"`
}

// BenchmarkResult is one test point.
type BenchmarkResult struct {
	PromptTokens     int     `json:"prompt_tokens"`
	GenTokens        int     `json:"gen_tokens"`
	Repetition       int     `json:"repetition"`
	PromptTokPerSec  float64 `json:"prompt_tok_per_sec"`
	GenTokPerSec     float64 `json:"gen_tok_per_sec"`
	TTFTMs           float64 `json:"ttft_ms"`
	TotalMs          float64 `json:"total_ms"`
}

// BenchmarkSummary holds aggregated stats.
type BenchmarkSummary struct {
	AvgPromptTokPerSec float64 `json:"avg_prompt_tok_per_sec"`
	AvgGenTokPerSec    float64 `json:"avg_gen_tok_per_sec"`
	AvgTTFTMs          float64 `json:"avg_ttft_ms"`
	MinGenTokPerSec    float64 `json:"min_gen_tok_per_sec"`
	MaxGenTokPerSec    float64 `json:"max_gen_tok_per_sec"`
}

// LlamaBenchResult holds raw inference benchmark data.
type LlamaBenchResult struct {
	PromptTokPerSec float64 `json:"pp_avg_ts"`
	GenTokPerSec    float64 `json:"tg_avg_ts"`
	PromptTokens    int     `json:"pp_tokens"`
	GenTokens       int     `json:"tg_tokens"`
	Repetitions     int     `json:"repetitions"`
}

// TimingSample is one observed timing from real usage.
type TimingSample struct {
	Timestamp       time.Time `json:"ts"`
	ModelID         string    `json:"model"`
	PromptTokens    int       `json:"prompt_n"`
	GenTokens       int       `json:"gen_n"`
	PromptTokPerSec float64   `json:"prompt_tps"`
	GenTokPerSec    float64   `json:"gen_tps"`
}

// Preset defines benchmark parameters.
type Preset struct {
	Name         string
	Label        string
	PromptTokens []int
	GenTokens    int
	Repetitions  int
	RunLlamaBench bool
}

// Presets returns the available benchmark presets.
func Presets() []Preset {
	return []Preset{
		{Name: "quick", Label: "Quick (~10s)", PromptTokens: []int{256}, GenTokens: 128, Repetitions: 1, RunLlamaBench: false},
		{Name: "standard", Label: "Standard (~2min)", PromptTokens: []int{128, 512, 2048}, GenTokens: 128, Repetitions: 3, RunLlamaBench: true},
		{Name: "thorough", Label: "Thorough (~10min)", PromptTokens: []int{128, 512, 2048, 8192}, GenTokens: 256, Repetitions: 5, RunLlamaBench: true},
	}
}

// GetPreset returns a preset by name, falling back to "standard".
func GetPreset(name string) Preset {
	for _, p := range Presets() {
		if p.Name == name {
			return p
		}
	}
	return Presets()[1] // standard
}

// GPUSnapshotsFromMetrics converts monitor metrics to GPU snapshots.
func GPUSnapshotsFromMetrics(m monitor.Metrics) []GPUSnapshot {
	snaps := make([]GPUSnapshot, len(m.GPU))
	for i, g := range m.GPU {
		snaps[i] = GPUSnapshot{
			Index:       g.Index,
			Name:        g.Name,
			VRAMTotalMB: g.VRAMTotalMB,
		}
	}
	return snaps
}

// Store manages benchmark persistence and timing samples.
type Store struct {
	mu      sync.RWMutex
	dataDir string
	runs    []BenchmarkRun

	timingsMu sync.RWMutex
	timings   map[string][]TimingSample // model ID → ring buffer
}

const maxTimingSamples = 1000

// NewStore creates a store and loads persisted benchmarks.
func NewStore(dataDir string) *Store {
	s := &Store{
		dataDir: dataDir,
		timings: make(map[string][]TimingSample),
	}
	s.load()
	return s
}

// List returns all benchmark runs, newest first.
func (s *Store) List() []BenchmarkRun {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]BenchmarkRun, len(s.runs))
	copy(out, s.runs)
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// Get returns a benchmark run by ID.
func (s *Store) Get(id string) (*BenchmarkRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.runs {
		if s.runs[i].ID == id {
			run := s.runs[i]
			return &run, nil
		}
	}
	return nil, fmt.Errorf("benchmark not found: %s", id)
}

// Save adds or updates a benchmark run.
func (s *Store) Save(run BenchmarkRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.runs {
		if s.runs[i].ID == run.ID {
			s.runs[i] = run
			s.persist()
			return
		}
	}
	s.runs = append(s.runs, run)
	s.persist()
}

// Delete removes a benchmark run.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.runs {
		if s.runs[i].ID == id {
			s.runs = append(s.runs[:i], s.runs[i+1:]...)
			s.persist()
			return nil
		}
	}
	return fmt.Errorf("benchmark not found: %s", id)
}

// RecordTiming adds a passive timing sample.
func (s *Store) RecordTiming(sample TimingSample) {
	s.timingsMu.Lock()
	defer s.timingsMu.Unlock()
	samples := s.timings[sample.ModelID]
	samples = append(samples, sample)
	if len(samples) > maxTimingSamples {
		samples = samples[len(samples)-maxTimingSamples:]
	}
	s.timings[sample.ModelID] = samples
}

// Timings returns recent timing samples for a model (or all models if empty).
func (s *Store) Timings(modelID string) []TimingSample {
	s.timingsMu.RLock()
	defer s.timingsMu.RUnlock()
	if modelID != "" {
		out := make([]TimingSample, len(s.timings[modelID]))
		copy(out, s.timings[modelID])
		return out
	}
	var all []TimingSample
	for _, samples := range s.timings {
		all = append(all, samples...)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].Timestamp.After(all[j].Timestamp)
	})
	return all
}

// TimingSummary returns aggregated timing stats per model.
func (s *Store) TimingSummary() []TimingModelSummary {
	s.timingsMu.RLock()
	defer s.timingsMu.RUnlock()
	var out []TimingModelSummary
	for modelID, samples := range s.timings {
		if len(samples) == 0 {
			continue
		}
		var sumGen, sumPrompt float64
		for _, t := range samples {
			sumGen += t.GenTokPerSec
			sumPrompt += t.PromptTokPerSec
		}
		n := float64(len(samples))
		out = append(out, TimingModelSummary{
			ModelID:            modelID,
			Count:              len(samples),
			AvgGenTokPerSec:    sumGen / n,
			AvgPromptTokPerSec: sumPrompt / n,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ModelID < out[j].ModelID
	})
	return out
}

// TimingModelSummary is aggregated timing stats for one model.
type TimingModelSummary struct {
	ModelID            string  `json:"model_id"`
	Count              int     `json:"count"`
	AvgGenTokPerSec    float64 `json:"avg_gen_tok_per_sec"`
	AvgPromptTokPerSec float64 `json:"avg_prompt_tok_per_sec"`
}

func (s *Store) benchmarkPath() string {
	return filepath.Join(s.dataDir, "config", "benchmarks.json")
}

func (s *Store) load() {
	data, err := os.ReadFile(s.benchmarkPath())
	if err != nil {
		return
	}
	if err := json.Unmarshal(data, &s.runs); err != nil {
		slog.Error("failed to load benchmarks", "error", err)
	}
}

func (s *Store) persist() {
	os.MkdirAll(filepath.Dir(s.benchmarkPath()), 0o755)
	data, err := json.MarshalIndent(s.runs, "", "  ")
	if err != nil {
		slog.Error("failed to marshal benchmarks", "error", err)
		return
	}
	os.WriteFile(s.benchmarkPath(), data, 0o644)
}
