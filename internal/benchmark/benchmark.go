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
	JobID     string    `json:"job_id,omitempty"` // owning job; "adhoc" for migrated/legacy and quick-bench runs
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

	// Build info. The flat BuildID/BuildRef/BuildProfile fields predate
	// the Build snapshot and are preserved for already-persisted runs;
	// new runs populate Build with the full snapshot. Use EffectiveBuild()
	// to read; it falls back to the flat fields for legacy data.
	BuildID      string        `json:"build_id"`
	BuildRef     string        `json:"build_ref"`
	BuildProfile string        `json:"build_profile"`
	Build        BuildSnapshot `json:"build,omitempty"`

	// Hardware
	GPUs []GPUSnapshot `json:"gpus"`

	// Parameters
	Preset       string `json:"preset"`
	PromptTokens []int  `json:"prompt_tokens"`
	GenTokens    int    `json:"gen_tokens"`

	// Results
	Results     []BenchmarkResult   `json:"results,omitempty"`
	Summary     *BenchmarkSummary   `json:"summary,omitempty"`
	LlamaBench  *LlamaBenchResult   `json:"llama_bench,omitempty"`
	LlamaBenchy []LlamaBenchyResult `json:"llama_benchy,omitempty"`

	// Command line that was actually executed for benchy presets, captured
	// at run time so the detail view and "About" modal can disclose it.
	BenchyCommand string `json:"benchy_command,omitempty"`

	// Warnings (non-fatal issues during the run)
	Warnings []string `json:"warnings,omitempty"`

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
	DirectIO       bool   `json:"direct_io,omitempty"`
	Threads        int    `json:"threads"`
	SpecType       string `json:"spec_type,omitempty"`
}

// GPUSnapshot captures GPU hardware at benchmark time.
type GPUSnapshot struct {
	Index       int    `json:"index"`
	Name        string `json:"name"`
	VRAMTotalMB int    `json:"vram_total_mb"`
}

// BuildSnapshot freezes the llama.cpp build that produced a benchmark
// run. Captured at run start so deleting/rebuilding the build later
// doesn't strand the result without context.
type BuildSnapshot struct {
	ID         string            `json:"id"`
	Tag        string            `json:"tag,omitempty"`
	Profile    string            `json:"profile"` // rocm | cuda | vulkan | metal | cpu
	Vendor     string            `json:"vendor"`  // currently == Profile; reserved for future split
	GitSHA     string            `json:"git_sha,omitempty"`
	GitRef     string            `json:"git_ref,omitempty"`
	CMakeFlags map[string]string `json:"cmake_flags,omitempty"`
	BinaryPath string            `json:"binary_path,omitempty"`
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

// PresetSourceInternal drives the in-process API benchmark loop in
// runner.go (real chat completions through the router). PresetSourceBenchy
// shells out to `uvx llama-benchy` against the same router. Empty defaults
// to internal so older preset definitions stay valid.
const (
	PresetSourceInternal = "internal"
	PresetSourceBenchy   = "benchy"
)

// Preset defines benchmark parameters.
type Preset struct {
	Name         string
	Label        string
	Description  string
	Source       string // "" | "internal" | "benchy"
	PromptTokens []int
	GenTokens    int
	Repetitions  int
	Concurrency  []int // benchy only; defaults to [1] if empty
}

// EffectiveSource returns the dispatch key, defaulting empty → internal.
func (p Preset) EffectiveSource() string {
	if p.Source == "" {
		return PresetSourceInternal
	}
	return p.Source
}

// Presets returns the available benchmark presets.
func Presets() []Preset {
	return []Preset{
		{
			Name:         "internal-quick",
			Label:        "internal-quick — 1 rep, 256-token prompt (~10s)",
			Description:  "Single end-to-end request with a 256-token prompt and 128 generated tokens. Sanity check that the model loads and runs.",
			Source:       PresetSourceInternal,
			PromptTokens: []int{256}, GenTokens: 128, Repetitions: 1,
		},
		{
			Name:         "internal-standard",
			Label:        "internal-standard — 3 reps × 3 prompt sizes (~1 min)",
			Description:  "Three repetitions of end-to-end requests at 128, 512, and 2048-token prompts (128 gen tokens each).",
			Source:       PresetSourceInternal,
			PromptTokens: []int{128, 512, 2048}, GenTokens: 128, Repetitions: 3,
		},
		{
			Name:         "internal-thorough",
			Label:        "internal-thorough — 5 reps × 4 prompt sizes up to 8K (~5 min)",
			Description:  "Five repetitions at 128 / 512 / 2048 / 8192-token prompts with 256 generated tokens each. Stresses long-context performance.",
			Source:       PresetSourceInternal,
			PromptTokens: []int{128, 512, 2048, 8192}, GenTokens: 256, Repetitions: 5,
		},
		{
			Name:         "internal-long-ctx",
			Label:        "internal-long-ctx — 1 rep, 32K prompt / 512 gen",
			Description:  "Single 32768-token prompt with 512 generated tokens. Stresses KV cache, flash-attention, and KV quantization on a long context.",
			Source:       PresetSourceInternal,
			PromptTokens: []int{32768}, GenTokens: 512, Repetitions: 1,
		},
		{
			Name:         "benchy-quick",
			Label:        "benchy-quick — 1 rep, 512 prompt / 32 gen via llama-benchy (~10s)",
			Description:  "Single-shot llama-benchy run against the router. Smoke test for the API path; works with sharded GGUFs.",
			Source:       PresetSourceBenchy,
			PromptTokens: []int{512}, GenTokens: 32, Repetitions: 1, Concurrency: []int{1},
		},
		{
			Name:         "benchy-standard",
			Label:        "benchy-standard — 3 reps, 2048 prompt / 128 gen via llama-benchy (~1 min)",
			Description:  "Three-run llama-benchy benchmark at 2048-token prompts. Replaces the legacy llama-bench raw inference test for sharded models.",
			Source:       PresetSourceBenchy,
			PromptTokens: []int{2048}, GenTokens: 128, Repetitions: 3, Concurrency: []int{1},
		},
	}
}

// presetAliases maps the pre-rename preset names (used by data persisted
// before the internal-* / benchy-* split) onto their current equivalents,
// so old runs render and re-runs find the right preset.
var presetAliases = map[string]string{
	"quick":    "internal-quick",
	"standard": "internal-standard",
	"thorough": "internal-thorough",
}

// GetPreset returns a preset by name, falling back to "internal-standard".
func GetPreset(name string) Preset {
	if alias, ok := presetAliases[name]; ok {
		name = alias
	}
	for _, p := range Presets() {
		if p.Name == name {
			return p
		}
	}
	return Presets()[1] // internal-standard
}

// EffectiveBuild returns the run's Build snapshot, falling back to a
// minimal snapshot synthesized from the legacy flat fields when the run
// was persisted before BuildSnapshot existed.
func (r *BenchmarkRun) EffectiveBuild() BuildSnapshot {
	if r.Build.ID != "" || r.Build.GitRef != "" {
		return r.Build
	}
	return BuildSnapshot{
		ID:      r.BuildID,
		GitRef:  r.BuildRef,
		Profile: r.BuildProfile,
		Vendor:  r.BuildProfile,
	}
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

// BuildResolver resolves a build ID to its full snapshot. Implemented by
// the api layer over *builder.Builder; passed in so the benchmark
// package doesn't import builder directly.
//
// A nil resolver, or one that returns the zero value, signals "not
// known" — the migration falls back to the legacy flat fields.
type BuildResolver func(buildID string) BuildSnapshot

// Store manages benchmark persistence and timing samples.
type Store struct {
	mu       sync.RWMutex
	dataDir  string
	runs     []BenchmarkRun
	jobs     []BenchmarkJob
	resolver BuildResolver

	timingsMu sync.RWMutex
	timings   map[string][]TimingSample // model ID → ring buffer
}

const maxTimingSamples = 1000

// schemaVersion is the on-disk envelope version this build writes. v1
// was a bare JSON array of runs; v2 wraps them with a jobs list.
const schemaVersion = 2

// benchmarkFile is the v2 envelope. v1 files are detected by an
// unmarshal failure into this shape and a successful retry as []BenchmarkRun.
type benchmarkFile struct {
	Version int            `json:"version"`
	Jobs    []BenchmarkJob `json:"jobs"`
	Runs    []BenchmarkRun `json:"runs"`
}

// NewStore creates a store and loads persisted benchmarks. resolver may
// be nil; if provided, the v1→v2 migration uses it to backfill Build
// snapshots for runs whose build still exists in the builder.
func NewStore(dataDir string, resolver BuildResolver) *Store {
	s := &Store{
		dataDir:  dataDir,
		resolver: resolver,
		timings:  make(map[string][]TimingSample),
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

// Save adds or updates a benchmark run. Runs with no JobID are assigned
// to the synthetic Ad-Hoc Runs job so the "every run belongs to a job"
// invariant holds across the existing single-run code path.
func (s *Store) Save(run BenchmarkRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.JobID == "" {
		run.JobID = AdhocJobID
		if !s.hasJobLocked(AdhocJobID) {
			s.jobs = append(s.jobs, newAdhocJob(run.CreatedAt))
		}
	}
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

// ListJobs returns all jobs, newest CreatedAt first. The synthetic
// AdhocJobID always sorts last, regardless of timestamp, so the user's
// real batch jobs surface above their history.
func (s *Store) ListJobs() []BenchmarkJob {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]BenchmarkJob, len(s.jobs))
	copy(out, s.jobs)
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].ID == AdhocJobID) != (out[j].ID == AdhocJobID) {
			return out[j].ID == AdhocJobID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// GetJob returns a job by ID.
func (s *Store) GetJob(id string) (*BenchmarkJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.jobs {
		if s.jobs[i].ID == id {
			job := s.jobs[i]
			return &job, nil
		}
	}
	return nil, fmt.Errorf("job not found: %s", id)
}

// SaveJob adds or updates a job.
func (s *Store) SaveJob(job BenchmarkJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.jobs {
		if s.jobs[i].ID == job.ID {
			s.jobs[i] = job
			s.persist()
			return
		}
	}
	s.jobs = append(s.jobs, job)
	s.persist()
}

// DeleteJob removes a job. The disposition controls what happens to its
// runs: DeleteCascade removes them with the job; DeleteOrphan reassigns
// them to the AdhocJobID. Deleting AdhocJobID itself is rejected — it's
// the migration target and the home of the existing single-run path.
func (s *Store) DeleteJob(id string, disposition DeleteDisposition) error {
	if id == AdhocJobID {
		return fmt.Errorf("cannot delete the synthetic %q job", AdhocJobID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.jobs {
		if s.jobs[i].ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("job not found: %s", id)
	}
	switch disposition {
	case DeleteCascade:
		filtered := s.runs[:0]
		for _, r := range s.runs {
			if r.JobID != id {
				filtered = append(filtered, r)
			}
		}
		s.runs = filtered
	case DeleteOrphan:
		if !s.hasJobLocked(AdhocJobID) {
			s.jobs = append(s.jobs, newAdhocJob(time.Now()))
		}
		for i := range s.runs {
			if s.runs[i].JobID == id {
				s.runs[i].JobID = AdhocJobID
			}
		}
	default:
		return fmt.Errorf("unknown disposition: %q (want %q or %q)", disposition, DeleteCascade, DeleteOrphan)
	}
	s.jobs = append(s.jobs[:idx], s.jobs[idx+1:]...)
	s.persist()
	return nil
}

// RunsForJob returns all runs belonging to the given job, newest first.
func (s *Store) RunsForJob(jobID string) []BenchmarkRun {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []BenchmarkRun
	for _, r := range s.runs {
		if r.JobID == jobID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
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

	dirty := false

	// Try v2 envelope first; on failure, fall back to v1 bare array.
	var file benchmarkFile
	if jsonErr := json.Unmarshal(data, &file); jsonErr == nil && file.Version >= 2 {
		s.jobs = file.Jobs
		s.runs = file.Runs
	} else {
		var runs []BenchmarkRun
		if v1Err := json.Unmarshal(data, &runs); v1Err != nil {
			slog.Error("failed to load benchmarks (neither v2 envelope nor v1 array)", "v2_error", jsonErr, "v1_error", v1Err)
			return
		}
		s.runs = runs
		dirty = true // forces a v2 rewrite at end of load
	}

	// Any benchmark still marked running at startup belongs to a previous
	// process that died mid-run — surface it as failed so it's deletable.
	for i := range s.runs {
		if s.runs[i].Status == StatusRunning {
			s.runs[i].Status = StatusFailed
			if s.runs[i].Error == "" {
				s.runs[i].Error = "interrupted: server restarted before benchmark finished"
			}
			dirty = true
		}
	}

	// Backfill JobID="adhoc" on every run that lacks one and ensure the
	// adhoc pseudo-job exists. Track the earliest run's CreatedAt so the
	// synthesized adhoc job sorts naturally with history.
	var earliest time.Time
	needsAdhoc := false
	for i := range s.runs {
		if s.runs[i].JobID == "" {
			s.runs[i].JobID = AdhocJobID
			needsAdhoc = true
			dirty = true
		}
		if s.runs[i].JobID == AdhocJobID {
			if earliest.IsZero() || s.runs[i].CreatedAt.Before(earliest) {
				earliest = s.runs[i].CreatedAt
			}
		}
	}
	if needsAdhoc && !s.hasJobLocked(AdhocJobID) {
		s.jobs = append(s.jobs, newAdhocJob(earliest))
	}

	// Backfill Build snapshot for runs that pre-date step 3. Try the
	// resolver first (gives full CMake flags / tag / SHA when the build
	// still exists), then fall back to the legacy flat fields.
	for i := range s.runs {
		if s.runs[i].Build.ID != "" || s.runs[i].Build.GitRef != "" {
			continue
		}
		if s.resolver != nil && s.runs[i].BuildID != "" {
			if snap := s.resolver(s.runs[i].BuildID); snap.ID != "" {
				s.runs[i].Build = snap
				dirty = true
				continue
			}
		}
		if s.runs[i].BuildID != "" || s.runs[i].BuildRef != "" {
			s.runs[i].Build = BuildSnapshot{
				ID:      s.runs[i].BuildID,
				GitRef:  s.runs[i].BuildRef,
				Profile: s.runs[i].BuildProfile,
				Vendor:  s.runs[i].BuildProfile,
			}
			dirty = true
		}
	}

	if dirty {
		s.persist()
	}
}

func (s *Store) hasJobLocked(id string) bool {
	for i := range s.jobs {
		if s.jobs[i].ID == id {
			return true
		}
	}
	return false
}

func (s *Store) persist() {
	os.MkdirAll(filepath.Dir(s.benchmarkPath()), 0o755)
	file := benchmarkFile{
		Version: schemaVersion,
		Jobs:    s.jobs,
		Runs:    s.runs,
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		slog.Error("failed to marshal benchmarks", "error", err)
		return
	}
	os.WriteFile(s.benchmarkPath(), data, 0o644)
}
