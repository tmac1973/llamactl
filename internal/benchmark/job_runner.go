package benchmark

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/tmlabonte/llamactl/internal/monitor"
)

// ErrJobAlreadyRunning is returned by Submit when a job is in flight.
var ErrJobAlreadyRunning = errors.New("a benchmark job is already running")

// JobEnv is the integration point the JobRunner needs from outside the
// benchmark package. The api layer implements it against the Server so
// this package stays free of imports for builder / models / process.
type JobEnv interface {
	// EnsureBuildActive switches the router to the build identified by
	// buildID, restarting llama-server when the active build differs.
	// Blocks until the router is reachable.
	EnsureBuildActive(ctx context.Context, buildID string) error

	// ResolveModel returns everything the cell needs about a model from
	// the registry (HF repo id for tokenizer, router-served name, saved
	// config to apply overrides on top of, display fields).
	ResolveModel(modelID string) (ModelInfo, error)

	// ResolveBuild returns the snapshot for buildID. Empty struct means
	// the build no longer exists; the cell will fail.
	ResolveBuild(buildID string) BuildSnapshot

	// CurrentMetrics returns the latest GPU metrics for snapshotting.
	CurrentMetrics() monitor.Metrics

	// RouterURL returns the base URL the runner should target.
	RouterURL() string
}

// ModelInfo bundles registry data for a single model so the JobRunner
// doesn't have to know about the models package.
type ModelInfo struct {
	HFRepoID    string         // model.ModelID — passed to llama-benchy --tokenizer
	Quant       string
	SizeGB      float64
	DisplayName string         // short, human-readable name for the run
	RouterName  string         // identifier the router responds to
	Config      ConfigSnapshot // saved baseline; ConfigOverrides overlay on this
}

// JobQueue serializes job execution: only one job runs at a time. Submit
// returns ErrJobAlreadyRunning when the queue is busy.
type JobQueue struct {
	mu      sync.Mutex
	store   *Store
	env     JobEnv
	runner  *Runner
	current *runningJob
}

type runningJob struct {
	id     string
	cancel context.CancelFunc
	done   chan struct{}
}

// NewJobQueue wires up the queue. The returned queue holds no goroutines
// until Submit is called.
func NewJobQueue(store *Store, env JobEnv) *JobQueue {
	return &JobQueue{
		store:  store,
		env:    env,
		runner: NewRunner(store),
	}
}

// Status returns the currently running job (if any).
func (q *JobQueue) Status() (*BenchmarkJob, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.current == nil {
		return nil, false
	}
	job, err := q.store.GetJob(q.current.id)
	if err != nil {
		return nil, false
	}
	return job, true
}

// Submit accepts a new job and starts it in a background goroutine.
// Returns ErrJobAlreadyRunning when another job is in flight.
func (q *JobQueue) Submit(job BenchmarkJob) error {
	q.mu.Lock()
	if q.current != nil {
		q.mu.Unlock()
		return ErrJobAlreadyRunning
	}
	if len(job.Cells) == 0 {
		q.mu.Unlock()
		return errors.New("job has no cells")
	}
	job.Status = JobStatusPending
	q.store.SaveJob(job)

	ctx, cancel := context.WithCancel(context.Background())
	rj := &runningJob{id: job.ID, cancel: cancel, done: make(chan struct{})}
	q.current = rj
	q.mu.Unlock()

	go q.run(ctx, job, rj)
	return nil
}

// Cancel signals the running job (if it matches id) to stop. It does
// not wait for the cell loop to wind down.
func (q *JobQueue) Cancel(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.current == nil || q.current.id != id {
		return fmt.Errorf("job %s is not running", id)
	}
	q.current.cancel()
	return nil
}

// RetryFailed re-runs only the cells in CellStatusFailed for the given
// job, treating completed cells as already done. The job must not
// currently be running.
func (q *JobQueue) RetryFailed(id string) error {
	job, err := q.store.GetJob(id)
	if err != nil {
		return err
	}
	any := false
	for i := range job.Cells {
		if job.Cells[i].Status == CellStatusFailed {
			job.Cells[i].Status = CellStatusPending
			job.Cells[i].Error = ""
			any = true
		}
	}
	if !any {
		return errors.New("no failed cells to retry")
	}
	return q.Submit(*job)
}

// run is the per-job orchestration loop. It assumes job.Cells is already
// ordered builds → models → presets so the prevBuildID amortization
// minimizes router restarts.
func (q *JobQueue) run(ctx context.Context, job BenchmarkJob, rj *runningJob) {
	defer func() {
		q.mu.Lock()
		q.current = nil
		q.mu.Unlock()
		close(rj.done)
	}()

	job.Status = JobStatusRunning
	job.StartedAt = time.Now()
	q.store.SaveJob(job)

	var prevBuildID string
	var anyCompleted bool

	for i := range job.Cells {
		cell := &job.Cells[i]

		// Already-completed cells (from a prior attempt that's being
		// resumed via RetryFailed) count toward "any completed" but
		// don't re-run.
		if cell.Status == CellStatusCompleted {
			anyCompleted = true
			continue
		}

		if ctx.Err() != nil {
			cell.Status = CellStatusSkipped
			q.store.SaveJob(job)
			continue
		}

		cell.Status = CellStatusRunning
		cell.Attempt++
		cell.Error = ""
		q.store.SaveJob(job)

		if err := q.runCell(ctx, &job, cell, &prevBuildID); err != nil {
			cell.Status = CellStatusFailed
			cell.Error = err.Error()
			q.store.SaveJob(job)
			slog.Warn("job cell failed", "job", job.ID, "model", cell.ModelID, "build", cell.BuildID, "preset", cell.Preset, "error", err)
			continue
		}

		cell.Status = CellStatusCompleted
		anyCompleted = true
		q.store.SaveJob(job)
	}

	job.FinishedAt = time.Now()
	switch {
	case ctx.Err() != nil:
		job.Status = JobStatusCanceled
	case anyCompleted:
		job.Status = JobStatusCompleted
	default:
		job.Status = JobStatusFailed
	}
	q.store.SaveJob(job)
}

// runCell drives one (model, build, preset) cell to completion. The
// caller is responsible for setting the cell to running and recording
// the final status; runCell only returns an error or nil.
//
// prevBuildID is updated when this cell's build differs from the
// previous one so the next iteration knows whether a switch already
// happened.
func (q *JobQueue) runCell(ctx context.Context, job *BenchmarkJob, cell *JobCell, prevBuildID *string) error {
	if cell.BuildID != *prevBuildID {
		if err := q.env.EnsureBuildActive(ctx, cell.BuildID); err != nil {
			return fmt.Errorf("activate build %s: %w", cell.BuildID, err)
		}
		*prevBuildID = cell.BuildID
	}

	modelInfo, err := q.env.ResolveModel(cell.ModelID)
	if err != nil {
		return fmt.Errorf("resolve model %s: %w", cell.ModelID, err)
	}
	buildSnap := q.env.ResolveBuild(cell.BuildID)
	if buildSnap.ID == "" {
		return fmt.Errorf("build %s no longer exists", cell.BuildID)
	}
	preset := GetPreset(cell.Preset)
	cfg := applyOverrides(modelInfo.Config, job.Overrides)

	run := BenchmarkRun{
		ID:           fmt.Sprintf("bench-%d-%d", time.Now().UnixMilli(), cell.Attempt),
		JobID:        job.ID,
		CreatedAt:    time.Now(),
		Status:       StatusRunning,
		ModelID:      cell.ModelID,
		ModelName:    modelInfo.DisplayName,
		Quant:        modelInfo.Quant,
		SizeGB:       modelInfo.SizeGB,
		Config:       cfg,
		BuildID:      buildSnap.ID,
		BuildRef:     buildSnap.GitRef,
		BuildProfile: buildSnap.Profile,
		Build:        buildSnap,
		GPUs:         GPUSnapshotsFromMetrics(q.env.CurrentMetrics()),
		Preset:       preset.Name,
		PromptTokens: preset.PromptTokens,
		GenTokens:    preset.GenTokens,
	}
	q.store.Save(run)
	cell.BenchmarkRunID = run.ID
	q.store.SaveJob(*job)

	q.runner.Run(ctx, RunConfig{
		Run:        run,
		Preset:     preset,
		RouterURL:  q.env.RouterURL(),
		RouterName: modelInfo.RouterName,
		HFRepoID:   modelInfo.HFRepoID,
	}, nil)

	final, err := q.store.Get(run.ID)
	if err != nil {
		return fmt.Errorf("read back run: %w", err)
	}
	if final.Status != StatusCompleted {
		if final.Error != "" {
			return errors.New(final.Error)
		}
		return fmt.Errorf("run ended with status %s", final.Status)
	}
	return nil
}

// applyOverrides returns base with non-nil ConfigOverrides fields
// applied on top. A nil overrides argument returns base unchanged.
func applyOverrides(base ConfigSnapshot, overrides *ConfigOverrides) ConfigSnapshot {
	if overrides == nil {
		return base
	}
	out := base
	if overrides.GPULayers != nil {
		out.GPULayers = *overrides.GPULayers
	}
	if overrides.ContextSize != nil {
		out.ContextSize = *overrides.ContextSize
	}
	if overrides.Threads != nil {
		out.Threads = *overrides.Threads
	}
	if overrides.FlashAttention != nil {
		out.FlashAttention = *overrides.FlashAttention
	}
	if overrides.KVCacheQuant != nil {
		out.KVCacheQuant = *overrides.KVCacheQuant
	}
	if overrides.DirectIO != nil {
		out.DirectIO = *overrides.DirectIO
	}
	if overrides.GPUAssign != nil {
		out.GPUAssign = *overrides.GPUAssign
	}
	if overrides.TensorSplit != nil {
		out.TensorSplit = *overrides.TensorSplit
	}
	if overrides.SpecType != nil {
		out.SpecType = *overrides.SpecType
	}
	return out
}

// ExpandCells builds the cell matrix in builds → models → presets order.
// Builds is the outermost dimension specifically so EnsureBuildActive
// fires at most once per build per job.
func ExpandCells(modelIDs, buildIDs, presets []string) []JobCell {
	cells := make([]JobCell, 0, len(buildIDs)*len(modelIDs)*len(presets))
	for _, b := range buildIDs {
		for _, m := range modelIDs {
			for _, p := range presets {
				cells = append(cells, JobCell{
					ModelID: m,
					BuildID: b,
					Preset:  p,
					Status:  CellStatusPending,
				})
			}
		}
	}
	return cells
}
