package benchmark

import (
	"time"
)

// AdhocJobID is the synthetic catch-all job that holds runs not produced
// by an explicit batch (legacy migrated runs and the still-existing
// single-run / quick-benchmark path).
const AdhocJobID = "adhoc"

const (
	JobKindBatch = "batch"
	JobKindAdhoc = "ad-hoc"
)

const (
	JobStatusPending   = "pending"
	JobStatusRunning   = "running"
	JobStatusCompleted = "completed"
	JobStatusFailed    = "failed"
	JobStatusCanceled  = "canceled"
)

const (
	CellStatusPending   = "pending"
	CellStatusRunning   = "running"
	CellStatusCompleted = "completed"
	CellStatusFailed    = "failed"
	CellStatusSkipped   = "skipped"
)

// DeleteDisposition controls what happens to a job's runs when the job
// is deleted: cascade removes them, orphan reassigns them to AdhocJobID.
type DeleteDisposition string

const (
	DeleteCascade DeleteDisposition = "cascade"
	DeleteOrphan  DeleteDisposition = "orphan"
)

// BenchmarkJob is a named, persistent batch run sweeping a Cartesian
// matrix of {ModelIDs} × {BuildIDs} × {Presets}. The expanded matrix
// lives in Cells.
type BenchmarkJob struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Kind        string `json:"kind"`   // "batch" | "ad-hoc"
	Status      string `json:"status"` // pending|running|completed|failed|canceled

	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`

	// Definition (immutable after first run)
	ModelIDs  []string         `json:"model_ids,omitempty"`
	BuildIDs  []string         `json:"build_ids,omitempty"`
	Presets   []string         `json:"presets,omitempty"`
	Overrides *ConfigOverrides `json:"overrides,omitempty"`

	// Expanded matrix
	Cells []JobCell `json:"cells,omitempty"`
}

// ConfigOverrides applies on top of each model's saved ModelConfig for
// every cell. Pointer fields so nil = "use the model's saved value".
type ConfigOverrides struct {
	GPULayers      *int     `json:"gpu_layers,omitempty"`
	ContextSize    *int     `json:"context_size,omitempty"`
	Threads        *int     `json:"threads,omitempty"`
	FlashAttention *bool    `json:"flash_attention,omitempty"`
	KVCacheQuant   *string  `json:"kv_cache_quant,omitempty"`
	DirectIO       *bool    `json:"direct_io,omitempty"`
	GPUAssign      *string  `json:"gpu_assign,omitempty"`
	TensorSplit    *string  `json:"tensor_split,omitempty"`
	SpecType       *string  `json:"spec_type,omitempty"`
	DraftModelPath *string  `json:"draft_model_path,omitempty"`
	Temperature    *float64 `json:"temperature,omitempty"`
	TopP           *float64 `json:"top_p,omitempty"`
	TopK           *int     `json:"top_k,omitempty"`
	MinP           *float64 `json:"min_p,omitempty"`
	RepeatPenalty  *float64 `json:"repeat_penalty,omitempty"`
}

// JobCell is one (model, build, preset) point in the matrix. The cell
// owns at most one BenchmarkRun at a time; on retry the run ID is
// rewritten to point at the latest attempt.
type JobCell struct {
	ModelID        string `json:"model_id"`
	BuildID        string `json:"build_id"`
	Preset         string `json:"preset"`
	Status         string `json:"status"` // pending|running|completed|failed|skipped
	Attempt        int    `json:"attempt"`
	BenchmarkRunID string `json:"benchmark_run_id,omitempty"`
	Error          string `json:"error,omitempty"`
}

// newAdhocJob synthesizes the catch-all "Ad-Hoc Runs" pseudo-job that
// holds runs not produced by an explicit batch. Its CreatedAt should
// match the oldest run when a v1 file is being migrated, so the entry
// sorts naturally with the user's history.
func newAdhocJob(createdAt time.Time) BenchmarkJob {
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	return BenchmarkJob{
		ID:        AdhocJobID,
		Name:      "Ad-Hoc Runs",
		Kind:      JobKindAdhoc,
		Status:    JobStatusCompleted,
		CreatedAt: createdAt,
	}
}
