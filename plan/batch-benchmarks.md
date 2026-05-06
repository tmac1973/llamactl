# Batch Benchmark Jobs

Refactor the benchmarks system around **jobs**: named, persistent batch
runs that sweep a Cartesian matrix of `{models} × {builds} × {presets}`,
capture full reproducibility context, and surface results grouped by
job. Replace `llama-bench` with **llama-benchy** to remove the
multi-file (sharded) GGUF limitation and gain a benchmarking path that
can later be pointed at non-llama.cpp inference engines. Disclose the
prompts and commands the tool runs.

---

## Goals

1. **Define jobs** — name, description, multi-select models, builds, and
   presets; one optional set of config overrides applied to every cell.
2. **Run jobs serially** — only one job runs at a time, cells run one
   after another within a job, continue-on-failure with explicit retry.
3. **Snapshot fully** — every run records the build (incl. CMake flags,
   git ref, vendor), the model config in effect, and the GPU state at
   benchmark time, so deleting the build/model later does not lose
   context.
4. **Replace `llama-bench` with llama-benchy** — API-only tool, MIT
   licensed, works with sharded GGUFs, OpenAI-compatible so it can be
   pointed at vLLM/SGLang/etc. in the future.
5. **Expose what we run** — the "About benchmarks" modal shows the
   internal prompt text, repetition prefix logic, and the exact
   llama-benchy command line generated for each cell.
6. **Group the page by job** — backfill all existing runs into a
   synthetic "Ad-Hoc Runs" job; drop the standalone single-run path
   going forward (a "Quick benchmark" still works — it just auto-creates
   a 1-cell job).
7. **Compare across jobs** — selection works within a job and across
   jobs. Wider model column, native tooltips, sortable.
8. **Export** — CSV (per-cell rows), CSV (per-cell summaries), and JSON,
   per job and per arbitrary selection.

---

## Non-goals

- Editable prompts in the UI (planned for a follow-up).
- Parameter sweeps, e.g. testing the same model with `ngl=99` vs
  `ngl=50` as separate cells — first pass uses one override set per job.
- Concurrent jobs, parallel cells, distributed orchestration.
- Auto-building missing build variants when a job references a build
  that hasn't been compiled yet.

---

## llama-benchy

Repository: <https://github.com/eugr/llama-benchy> (MIT). Python tool,
runs via `uvx llama-benchy …` (no install if `uv` is present) or
`pip install llama-benchy`. Hits any OpenAI-compatible endpoint
(`/v1/models`, `/v1/chat/completions`) — we point it at our own router.

### Why replace, not coexist

- **Multi-file model support** — llama-benchy never touches a GGUF
  file; it only sends API requests, so sharded models work without any
  awareness on our side.
- **Engine-agnostic** — same tool can later benchmark vLLM, SGLang, or
  any OpenAI-compatible endpoint.
- **Concurrency support** — exposes `t/s (total)` and `t/s (req)` under
  load (out of scope for the first pass, but the data path is there).
- **Standard-ish output** — JSON schema documented upstream; fields
  align with what we already capture.

### Runtime requirement

`uv` must be available on `PATH` in whatever environment runs llama-toolchest:

- **Container installs** — `uv` is baked into the runtime images. Add a
  short `RUN curl -LsSf https://astral.sh/uv/install.sh | sh` (or distro
  package where available) to `Dockerfile.cpu`, `Dockerfile.cuda`, and
  `Dockerfile.rocm`, with the resulting binary placed on the system
  `PATH` (e.g. `/usr/local/bin/uv`). No prompting at container build time.
- **Host installs** — `setup.sh install --host` checks for `uv` as part
  of the host prereq pass and, when missing, prompts with the standard
  `prompt_confirm` flow (same pattern as the existing build-toolchain
  prompt) before running the installer. A `--no-uv` (or equivalent) opt-out
  isn't needed first pass; users can decline at the prompt.
- **Runtime detection** — at startup the benchmark subsystem still calls
  `uv --version`; if missing, `benchy-*` presets are disabled in the UI
  with an inline hint pointing at `setup.sh install --host` (or
  re-pulling the container image). `internal-*` presets keep working.

`README.md` host-requirements section gets a `uv` bullet next to the
existing entries.

### Invocation

```
uvx llama-benchy \
  --base-url   http://127.0.0.1:{routerPort}/v1 \
  --api-key    EMPTY \
  --model      {hfRepoIDForTokenizer} \
  --served-model-name {router model ID} \
  --pp         {prompt token sizes...} \
  --tg         {gen tokens} \
  --runs       {repetitions} \
  --concurrency {concurrency levels...} \
  --format     json \
  --save-result {tempfile.json}
```

We capture the JSON file llama-benchy writes (rather than parsing
stdout, which is markdown by default).

### Result fields captured

Mapped from llama-benchy's JSON output (see upstream
`schemas/benchmark_report_schema.json`):

```go
type LlamaBenchyResult struct {
    Test           string   `json:"test"`            // e.g. "pp2048", "tg32"
    PromptTokens   int      `json:"pp"`
    GenTokens      int      `json:"tg"`
    Depth          int      `json:"depth"`
    Concurrency    int      `json:"concurrency"`
    AvgTokPerSec   float64  `json:"t_s"`             // "t/s"
    PeakTokPerSec  float64  `json:"peak_t_s"`        // "peak t/s"
    TTFRMs         float64  `json:"ttfr_ms"`         // time-to-first-response
    EstPPTMs       float64  `json:"est_ppt_ms"`      // estimated prompt-processing
    E2ETTFTMs      float64  `json:"e2e_ttft_ms"`     // end-to-end TTFT
    TotalTokPerSec float64  `json:"t_s_total"`       // present when concurrency>1
    PerReqTokPerSec float64 `json:"t_s_req"`         // present when concurrency>1
}
```

A run carries `[]LlamaBenchyResult` instead of the previous single
`LlamaBenchResult`. The legacy `LlamaBenchResult` type is removed
along with `runner.runLlamaBench()`.

---

## Internal benchmark — disclosure & new presets

The internal API benchmark stays. The fixed prose passage in
`runner.go:283` and the per-repetition prefix in
`buildPrompt()` (`runner.go:285–302`) are now exposed verbatim in the
"About benchmarks" modal, alongside the parameters for each preset and
the llama-benchy command line each cell generates.

### Preset list (after this change)

| Preset key            | Source        | Prompt sizes        | Gen | Reps | Concurrency | Use case                           |
|-----------------------|---------------|---------------------|-----|------|-------------|------------------------------------|
| `internal-quick`      | internal API  | 256                 | 128 | 1    | 1           | Smoke test (~10s)                  |
| `internal-standard`   | internal API  | 128, 512, 2048      | 128 | 3    | 1           | Default real-prose run (~1 min)    |
| `internal-thorough`   | internal API  | 128, 512, 2048, 8192| 256 | 5    | 1           | Long compare run (~5 min)          |
| `internal-long-ctx`   | internal API  | 32768               | 512 | 1    | 1           | Stress KV / FA / quant on big ctx  |
| `benchy-quick`        | llama-benchy  | 512                 | 32  | 1    | 1           | Smoke test, llama-benchy path      |
| `benchy-standard`     | llama-benchy  | 2048                | 128 | 3    | 1           | Default benchy run                 |
| `benchy-throughput`   | llama-benchy  | 2048                | 128 | 3    | 1, 4, 8 †   | Concurrency scaling                |

† `benchy-throughput` always includes `concurrency=1` as a baseline
because the model under test may not have multi-slot serving enabled.
If the effective config (model's saved config + job overrides) reports
fewer parallel slots than a higher level (e.g. `--parallel 1` while
`concurrency=4` is requested), the runner skips that level for the cell
and records a per-result note rather than failing the cell. The C=1
result is always produced.

The `internal-*` presets no longer invoke `llama-bench` — that integration
is removed entirely. The `benchy-*` presets replace it, plus add
options that didn't exist before (long context, concurrency).

`internal-synthetic` (random-token prompts) was considered and dropped:
the existing per-rep prefix already defeats prefix caching, and the
prose-vs-random distinction in tok/s is small enough that it's not worth
maintaining a fourth internal variant.

---

## Data model changes

### New: `BenchmarkJob`

```go
type BenchmarkJob struct {
    ID          string    `json:"id"`
    Name        string    `json:"name"`
    Description string    `json:"description,omitempty"`
    Kind        string    `json:"kind"`        // "batch" | "ad-hoc"
    Status      string    `json:"status"`      // "pending","running","completed","failed","canceled"

    CreatedAt   time.Time `json:"created_at"`
    StartedAt   time.Time `json:"started_at,omitempty"`
    FinishedAt  time.Time `json:"finished_at,omitempty"`

    // Definition (immutable after first run)
    ModelIDs    []string  `json:"model_ids"`
    BuildIDs    []string  `json:"build_ids"`
    Presets     []string  `json:"presets"`
    Overrides   *ConfigOverrides `json:"overrides,omitempty"`

    // Expanded matrix
    Cells       []JobCell `json:"cells"`
}

// ConfigOverrides — pointer fields so nil = "use saved model config".
// Whatever is non-nil overrides the model's saved ModelConfig for every
// cell that uses that model.
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

type JobCell struct {
    ModelID         string `json:"model_id"`
    BuildID         string `json:"build_id"`
    Preset          string `json:"preset"`
    Status          string `json:"status"`            // "pending","running","completed","failed","skipped"
    Attempt         int    `json:"attempt"`
    BenchmarkRunID  string `json:"benchmark_run_id,omitempty"` // set when run starts
    Error           string `json:"error,omitempty"`
}
```

### Changes to `BenchmarkRun`

Additive (no removals):

```go
type BenchmarkRun struct {
    // ... existing fields ...

    JobID         string         `json:"job_id"`         // every run belongs to one job
    Build         BuildSnapshot  `json:"build"`          // full snapshot, not just IDs
    LlamaBenchy   []LlamaBenchyResult `json:"llama_benchy,omitempty"`

    // REMOVED: LlamaBench *LlamaBenchResult
}

type BuildSnapshot struct {
    ID         string            `json:"id"`
    Tag        string            `json:"tag"`
    Profile    string            `json:"profile"`     // rocm | cuda | vulkan | cpu
    Vendor     string            `json:"vendor"`      // derived; same set as profile
    GitSHA     string            `json:"git_sha"`
    GitRef     string            `json:"git_ref"`
    CMakeFlags map[string]string `json:"cmake_flags"`
    BinaryPath string            `json:"binary_path"`
}
```

Existing `BuildID`, `BuildRef`, `BuildProfile` fields on `BenchmarkRun`
stay (read by old code paths) but become redundant with `Build.*`. New
code reads from `Build`. A future cleanup can drop the flat fields.

### Storage schema migration

`{dataDir}/config/benchmarks.json` switches from a bare array to a
versioned wrapper:

```json
{
  "version": 2,
  "jobs": [ ... ],
  "runs": [ ... ]
}
```

Migration on load (in `Store.Load`):

1. If file is a JSON array (v1), wrap it: `{version:2, jobs:[adhoc], runs: <array>}`.
2. Synthesize one `BenchmarkJob{ID:"adhoc", Kind:"ad-hoc", Name:"Ad-Hoc Runs", Status:"completed"}`.
3. Backfill `JobID="adhoc"` on every run that lacks one.
4. Backfill `Build` snapshot best-effort by looking up `BuildID` in the
   builder; if the build is gone, fill `Build.ID/Profile/GitRef` from
   the existing flat fields, leave `CMakeFlags` empty.
5. Drop `LlamaBench` (legacy field) — it stays in JSON but is no longer
   read; new code reads `LlamaBenchy`. Old data is preserved as raw
   bytes for the detail view to render in a "Legacy llama-bench result"
   section if present.
6. Save back in v2 format.

---

## Job execution

### Queue and serialization

A package-level `JobQueue` enforces "one job at a time":

```go
type JobQueue struct {
    mu      sync.Mutex
    current *BenchmarkJob
    runner  *Runner
}

func (q *JobQueue) Submit(job *BenchmarkJob) error  // 409 if a job is running
func (q *JobQueue) Cancel(jobID string) error       // best-effort
func (q *JobQueue) Status() (*BenchmarkJob, bool)   // current running job
```

Cells within a job are processed strictly sequentially in the order
`builds → models → presets` (see "Build switching" below — this ordering
exists specifically to minimize router restarts).

### Per-cell flow

For each cell:

1. Mark cell `running`, persist.
2. Resolve build's `llama-server` binary; switch the active build if it
   differs from the previous cell (re-launch the router with the cell's
   build binary). This is the only step that crosses cell boundaries.
3. Apply the job's `Overrides` on top of the model's saved config to
   get an effective `ConfigSnapshot`. This snapshot is stored on the run.
4. Create a `BenchmarkRun{JobID, ModelID, Config, Build: snapshot}`,
   set status `running`, save, set `cell.BenchmarkRunID`.
5. Drive the run via the existing `runner.RunConfig` for `internal-*`
   presets, or via a new `runLlamaBenchy()` for `benchy-*` presets.
6. On success, mark run `completed`, mark cell `completed`. On error,
   mark run `failed` with error message, mark cell `failed`, **continue
   to the next cell** (do not abort the job).
7. After the last cell, mark job `completed` if any cell completed,
   `failed` if all cells failed.

### Retry

`POST /api/benchmark-jobs/{id}/retry-failed` re-queues only cells with
`status="failed"`, increments their `Attempt`, and runs them through
the same flow. The previous failed `BenchmarkRun` records are kept (a
new run is created on retry); the cell's `BenchmarkRunID` is updated to
the latest attempt.

### Cancel

Best-effort: stops accepting new cells, signals the in-flight cell's
context to cancel, marks remaining cells `skipped`. The in-flight run is
marked `failed` with `error="canceled"`.

### Build switching

The router currently runs one build at a time. Switching builds means
restarting the router process with a different binary, which is the most
expensive transition in a job (multi-second restart + model unload).
**Builds are therefore the outermost dimension of cell ordering**: run
every selected `(model × preset)` combination against build A, then
restart the router onto build B and run every `(model × preset)`
against B, and so on. Iteration order is strictly
`builds → models → presets`, so each build switch happens at most once
per build per job. Each switch waits for the router to come up before
the next cell starts.

---

## API endpoints

### Jobs

| Method | Path | Purpose |
|--------|------|---------|
| `GET`    | `/api/benchmark-jobs` | List jobs (HTMX or JSON) |
| `POST`   | `/api/benchmark-jobs` | Create + start a job |
| `GET`    | `/api/benchmark-jobs/{id}` | Job detail + cells |
| `DELETE` | `/api/benchmark-jobs/{id}?runs=cascade\|orphan` | Delete job; `cascade` (default) deletes its runs, `orphan` reassigns them to the synthetic `adhoc` job |
| `POST`   | `/api/benchmark-jobs/{id}/cancel` | Cancel running job |
| `POST`   | `/api/benchmark-jobs/{id}/retry-failed` | Re-run failed cells |
| `GET`    | `/api/benchmark-jobs/{id}/progress` | SSE stream: cell-level + run-level updates |
| `GET`    | `/api/benchmark-jobs/{id}/export?format=csv\|json&scope=cells\|summary` | Export |

### Runs (existing, augmented)

| Method | Path | Change |
|--------|------|--------|
| `GET`    | `/api/benchmarks` | Adds `?job=<id>`, `?scope=adhoc\|batch` filters |
| `GET`    | `/api/benchmarks/compare` | Already accepts `?ids=`; no schema change, response gains `build` block |
| `GET`    | `/api/benchmarks/export` | Adds `format=json` alongside existing CSV |

### "About" disclosure endpoint (new)

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/api/benchmarks/about` | Returns prompt text, repetition prefix template, preset table, and a per-preset example llama-benchy command line. Rendered into the "About benchmarks" modal. |

---

## UI

### Benchmarks page redesign

```
┌──────────────────────────────────────────────────────────────┐
│  Benchmark Jobs                       [About] [+ New Job ▾] │
│                                       (Quick / Batch)        │
├──────────────────────────────────────────────────────────────┤
│  ▶ Q4_K_M sweep across 3 builds        running   12/27 cells │
│      Qwen3.5-27B, granite-4.0          rocm/cuda/vulkan     │
│      started 4m ago                                          │
│  ▼ KV-quant comparison                  completed  9/9 cells │
│      Qwen3.5-27B  ·  rocm b8461  ·  3 presets               │
│      ┌──────────────────────────────────────────────────┐   │
│      │ Build × Preset matrix                            │   │
│      │              std    long-ctx   benchy-std        │   │
│      │  Q4_K_M       ✓ 48.2  ✓ 39.1   ✓ 51.0           │   │
│      │  Q8_0         ✓ 31.7  ✗ OOM    ✓ 33.2           │   │
│      └──────────────────────────────────────────────────┘   │
│      [Compare selected] [Retry failed] [Export ▾] [Delete ▾] │
│      Delete menu: "Delete job and its runs" / "Delete job, keep runs (move to Ad-Hoc)" │
│  ▶ Ad-Hoc Runs                          n/a       142 runs  │
│      (legacy + quick-benchmark runs)                         │
└──────────────────────────────────────────────────────────────┘
```

Each job row is collapsible. The "Ad-Hoc Runs" job is a synthetic
container that holds all pre-migration runs and any future runs created
via the Quick form.

### New job form (modal or page)

```
┌────────────────────────────────────────────────────────────┐
│  New Batch Job                                              │
│                                                              │
│  Name        [_______________________________________]      │
│  Description [_______________________________________]      │
│                                                              │
│  Models      ☑ Qwen3.5-27B Q4_K_M                          │
│              ☑ Qwen3.5-27B Q8_0                            │
│              ☑ DeepSeek-R1-0528-Qwen3-8B                   │
│              ☐ granite-4.0                                  │
│              [Select all] [Clear]                           │
│                                                              │
│  Builds      ☑ b8461 (rocm)                                │
│              ☑ b8461 (cuda)                                │
│              ☑ b8461 (vulkan)                              │
│              ☐ b8390 (rocm)                                │
│                                                              │
│  Presets     ☑ internal-standard                           │
│              ☑ benchy-standard                             │
│              ☐ internal-long-ctx                           │
│              ☐ benchy-throughput                           │
│                                                              │
│  ▼ Overrides (apply to all cells; leave blank to use       │
│    each model's saved settings)                             │
│      GPU layers    [   ]                                    │
│      Context size  [   ]                                    │
│      Flash attn    ( ) on  ( ) off  (•) inherit            │
│      KV quant      [_____ ▾]                                │
│      ...                                                    │
│                                                              │
│  Matrix preview: 3 models × 3 builds × 2 presets = 18 cells │
│                                                              │
│                                  [Cancel] [Create & Start]  │
└────────────────────────────────────────────────────────────┘
```

The matrix preview updates live. A "deselect cells" grid (advanced) is
out of scope; users prune by adjusting the multi-selects.

### Quick benchmark (preserves existing UX)

The existing form (model dropdown + preset dropdown + Run) becomes a
"Quick Benchmark" entry that creates a 1-cell job with `Kind="batch"`,
`Name="Quick: {model} / {preset}"`. The single-run code path is
removed; it's now a thin wrapper that builds a 1-cell job spec.

### Compare view fixes

In `web/templates/partials/benchmark_compare.html`:

1. Bump model-name column from `width:150px` → `width:280px` at
   lines 18, 35, 52.
2. Add `title="{{ .ModelFullName }}"` on the truncating div for native
   tooltips.
3. Add a `Build` column in the details table: shows
   `{Build.Profile} · {Build.GitRef}` with `title` showing the full
   `Tag` and `GitSHA`.
4. Add a sort control above the bar charts:
   `Sort by: [Name | Gen tok/s ▼ | Prompt tok/s | TTFT]`. Default:
   Gen tok/s descending. Sort is client-side (no server round-trip),
   re-orders both bar charts and the details table.

Build CMake flags / full hardware snapshot are **not** shown in the
compare view — they're available in the per-run detail view and JSON
export only.

### Detail view additions

`benchmark_detail.html` gains a "Build" section:

```
Build
  Tag         b8461
  Profile     rocm
  Git ref     b8461 (sha: a1b2c3d...)
  CMake flags GGML_HIPBLAS=ON
              GGML_BLAS=OFF
              CMAKE_BUILD_TYPE=Release
```

And a "Command" section showing the exact llama-benchy invocation that
produced the result (for benchy presets) or the prompt-construction
parameters (for internal presets).

### "About benchmarks" modal

Reworked to render data from `GET /api/benchmarks/about`:

1. **Internal benchmark** — full prompt passage in a `<pre>` block, the
   per-rep prefix template, and the chars-per-token target.
2. **llama-benchy** — short paragraph linking upstream, plus the exact
   command line we generate for each `benchy-*` preset.
3. **Preset table** — same data as the table in this doc, but rendered
   live so it stays in sync if presets change.
4. **Metrics glossary** — PP t/s, TG t/s, TTFT, TTFR (new), peak t/s
   (new), e2e_ttft (new).

---

## Export formats

### CSV — per-cell rows (`scope=cells`)

One row per `BenchmarkResult` (existing internal-benchmark row format),
plus llama-benchy result rows. Columns prefixed by job context:

```
job_id, job_name, model_id, model_name, quant, build_id, build_profile,
git_ref, preset, source, prompt_tokens, gen_tokens, depth, concurrency,
repetition, prompt_tps, gen_tps, peak_tps, ttft_ms, ttfr_ms,
e2e_ttft_ms, total_ms
```

`source` is `"internal"` or `"benchy"`. Fields irrelevant to a row's
source are blank.

### CSV — per-cell summary (`scope=summary`)

One row per cell with averaged stats — handy for spreadsheets.

### JSON

Full `BenchmarkJob` with all `Cells`, all linked `BenchmarkRun` objects
(including `Build` snapshot, `Config` snapshot, `GPUs`, raw results,
llama-benchy results). Self-contained; can be re-imported in the future.

---

## Modified / new files

### New
- `internal/benchmark/job.go` — `BenchmarkJob`, `JobCell`, `ConfigOverrides`, queue
- `internal/benchmark/job_runner.go` — per-cell orchestration, build switching, retry
- `internal/benchmark/benchy.go` — `runLlamaBenchy()`, `LlamaBenchyResult`, JSON parsing
- `internal/api/bench_jobs.go` — HTTP handlers for `/api/benchmark-jobs/*`
- `web/templates/partials/job_list.html`, `job_detail.html`, `job_form.html`, `job_progress.html`

### Modified
- `internal/benchmark/benchmark.go` — add `JobID`, `Build` snapshot to `BenchmarkRun`; v2 schema with migration; remove `LlamaBenchResult`
- `internal/benchmark/runner.go` — drop `runLlamaBench()`, drop llama-bench from preset definitions
- `internal/api/server.go` — register new routes and templates; serve `/api/benchmarks/about`
- `internal/api/bench.go` — add filters to list endpoint, JSON export option
- `web/templates/benchmarks.html` — replace top-level UI with jobs list; modal sources data from `/api/benchmarks/about`
- `web/templates/partials/benchmark_compare.html` — column width, sort controls, build column, tooltips
- `web/templates/partials/benchmark_detail.html` — build section, command section, legacy-llama-bench fallback render
- `web/templates/partials/benchmark_form.html` — becomes the "Quick Benchmark" form that creates a 1-cell job
- `Dockerfile.cpu`, `Dockerfile.cuda`, `Dockerfile.rocm` — install `uv` into each runtime image so `benchy-*` presets work out of the box in container mode
- `setup.sh` / `scripts/lib/host.sh` — add `uv` to the host prereq check; offer to install via the upstream installer (`prompt_confirm` flow, mirroring existing toolchain prompts)
- `README.md` — add `uv` to host requirements

### Removed
- `runner.go`'s `runLlamaBench()`, `parseLlamaBenchOutput()`, `findLlamaBench()`

---

## Implementation order

1. **llama-benchy integration first, no jobs yet.** Add `benchy.go`,
   wire it into the existing single-run path under a new
   `benchy-quick` / `benchy-standard` preset. Confirm sharded models
   benchmark cleanly. Disclose command in the modal.
2. **Drop llama-bench.** Remove `runLlamaBench` and llama-bench preset
   data. Migration for existing v1 data (`LlamaBench` field hidden but
   not deleted from JSON for legacy detail view).
3. **Build snapshot.** Add `Build` field to `BenchmarkRun`, populate at
   start time, render in detail view. Migrate existing runs in v1→v2.
4. **Job model & storage.** Types, schema v2 migration with synthetic
   "Ad-Hoc Runs" job, `Store` CRUD for jobs.
5. **Job runner & queue.** Sequential per-cell execution, build
   switching, continue-on-fail, retry-failed, cancel.
6. **Job API endpoints** + SSE progress.
7. **UI: jobs list, job detail, job form.** Backfilled "Ad-Hoc Runs"
   group renders as the existing list view inside that pseudo-job.
8. **Compare view fixes** (column width, tooltips, sort, build column).
   Decoupled from the rest — could ship earlier as a small PR.
9. **Exports** (CSV per-cell, CSV summary, JSON).
10. **About modal rework** — server-side render of presets + commands.

Steps 1, 2, 8 can ship independently. Steps 3–7 are tightly coupled.

---

## Verification

1. `go build ./...` clean compile after each step.
2. `uv` missing → benchy preset shows clear error in UI, internal
   presets still work.
3. Existing v1 `benchmarks.json` loads cleanly, all runs appear under
   "Ad-Hoc Runs", legacy llama-bench data still renders in detail view.
4. Create a 2×2×2 job (2 models × 2 builds × 2 presets = 8 cells), run
   to completion, confirm all 8 runs persisted with full Build snapshot.
5. Force one cell to fail (e.g. point a model at an unavailable build);
   confirm job continues, Retry Failed re-runs only that cell.
6. Cancel a running job mid-cell; confirm in-flight run marked failed,
   remaining cells skipped, no router process leak.
7. Run a sharded GGUF (e.g. a multi-shard 70B Q4) under
   `benchy-standard`. Confirm results captured (this is the headline
   reason for the swap).
8. Concurrent job submissions → second submission rejected with 409.
9. Compare view: select 4 runs from 2 different jobs, confirm rendering
   with full model names visible, hover shows tooltip, sort orders
   work, build column populated.
10. Export CSV (cells + summary) and JSON for one job; confirm fields
    match the schema in this doc and JSON round-trips through a
    re-import test.
11. Quick benchmark form still works end-to-end and produces a 1-cell
    job under the user's name.
12. Delete a job with `runs=cascade` → job and all linked runs gone.
    Delete a different job with `runs=orphan` → job gone, its runs now
    appear under "Ad-Hoc Runs" with `JobID="adhoc"`.
13. Build a container image fresh; confirm `uv --version` succeeds inside
    the container and `benchy-*` presets run without further setup.
14. On a host with `uv` absent, run `setup.sh install --host`; confirm
    the prereq prompt appears, accepting it installs `uv`, declining
    leaves `uv` missing and disables `benchy-*` presets at runtime with
    the documented hint.

---

## Resolved decisions

These were open questions in an earlier draft; the answers are now folded
into the plan body and listed here for traceability.

- **Delete-job cascade** — Both options exposed. The DELETE endpoint
  takes `?runs=cascade|orphan`; the UI presents a two-option Delete
  menu (delete with runs, or keep runs and move them to "Ad-Hoc Runs").
  See API endpoints + UI sections.
- **`uv` install bootstrap** — Containers ship `uv` baked into the
  runtime images. Host installs check for it during `setup.sh` and offer
  to install via the upstream installer with a `prompt_confirm`. See
  llama-benchy → Runtime requirement.
- **Preset names** — `internal-*` / `benchy-*` prefix scheme retained.
- **`benchy-throughput` concurrency levels** — Default `[1, 4, 8]` with
  `concurrency=1` always included as a baseline; higher levels are
  silently skipped per-cell when the effective config can't honour them.
  See preset table footnote.
- **Build switching cost** — Builds are the outermost iteration
  dimension specifically to minimize router restarts (run every model
  against build A, restart, run every model against build B, …). See
  "Build switching".