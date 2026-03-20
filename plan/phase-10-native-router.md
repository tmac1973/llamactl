# Phase 10 — Migrate to llama.cpp Native Router Mode

> Replace our multi-process manager with llama.cpp's built-in router mode for model management, routing, and lifecycle

---

## Background

llama.cpp server (post Dec 2025) includes a built-in router mode that manages multiple models as subprocesses with automatic routing, LRU eviction, and per-model configuration via INI presets. This is architecturally identical to what we built (multiple llama-server processes, one per model, with a routing proxy), but integrated into llama-server itself.

### How llama.cpp Router Mode Works

**Architecture:**
- Start `llama-server` with no `--model` flag — it enters router mode
- The router discovers models from `--models-dir` (local GGUF files) and/or `--models-preset` (INI config file)
- When a request arrives with a `model` field, the router spawns a child `llama-server` subprocess for that model (if not already loaded)
- Each model runs in its own process with its own settings, providing crash isolation
- The router proxies requests to the correct child process

**Configuration cascade (CSS-like precedence):**
1. **Global `[*]` section** in the preset INI — lowest precedence, base for all models
2. **Per-model `[name]` section** in the preset INI — overrides globals for that model
3. **CLI args** passed to the parent router — highest precedence, overrides everything

**Preset INI format:**
```ini
# Global defaults for all models
[*]
gpu-layers = 999
flash-attn = on
jinja = true
threads = 8

# Per-model overrides
[my-small-model]
model = /data/models/small-model.gguf
ctx-size = 8192
cache-type-k = f16
cache-type-v = f16
alias = small,fast

[my-large-model]
model = /data/models/large-model/large-00001-of-00004.gguf
ctx-size = 32768
cache-type-k = q8_0
cache-type-v = q8_0
direct-io = true
tensor-split = 0.25,0.25,0.25,0.25
alias = large,main
```

**Valid INI keys:** Any llama-server CLI flag name with leading dashes removed. This includes:
- `model` — path to the GGUF file
- `ctx-size` — context window
- `gpu-layers` / `n-gpu-layers` — GPU offload
- `tensor-split` — multi-GPU distribution
- `cache-type-k` / `cache-type-v` — KV cache quantization
- `flash-attn` — flash attention (on/off/auto)
- `direct-io` — bypass page cache
- `jinja` — Jinja template support
- `threads` — CPU threads
- `alias` — comma-separated model name aliases for API routing
- `batch-size` / `ubatch-size` — batch sizes
- `mmap` / `no-mmap` — memory mapping
- All sampling parameters (`temp`, `top-k`, `top-p`, `min-p`, etc.)

**Key flags for router operation:**
- `--models-dir PATH` — auto-discover GGUF files in this directory
- `--models-preset PATH` — load per-model settings from INI file
- `--models-max N` — max models loaded simultaneously (default: 4, 0 = unlimited)
- `--no-models-autoload` — don't auto-load on first request; require explicit `/models/load`

**API endpoints:**
- `GET /models` — list all known models with status (`loaded`, `loading`, `unloaded`)
- `POST /models/load` — `{"model": "name"}` — explicitly load a model
- `POST /models/unload` — `{"model": "name"}` — explicitly unload a model
- `GET /v1/models` — OpenAI-compatible model list
- All standard OpenAI endpoints route by the `model` field in the request

**LRU eviction:** When `--models-max` is reached and a new model is requested, the least-recently-used model is automatically unloaded to free VRAM.

---

## What We Replace

| Current Component | Replacement |
|---|---|
| `internal/process/manager.go` — multi-instance process management, port allocation, health polling | llama.cpp router handles all subprocess lifecycle |
| `internal/api/proxy.go` — model routing, request parsing, sampling injection | llama.cpp router handles routing; we keep proxy for API key auth and sampling injection |
| VRAM budget check in `handleActivateModel` | LRU eviction via `--models-max` |
| Per-model port allocation (8080-8099) | Router assigns ports to child processes internally |
| `handleServiceLogs` per-model SSE | Read from router's stdout (all models combined), or query child process logs |

## What We Keep

| Component | Reason |
|---|---|
| HuggingFace search/download | llama.cpp doesn't do this |
| Build management | We compile llama.cpp from source |
| GGUF metadata parsing | For VRAM estimation in UI before loading |
| GPU monitoring | For dashboard display |
| API key authentication | Proxy layer for `/v1` endpoints |
| Sampling parameter injection | Per-model defaults injected at proxy level |
| Web management UI | Our core value-add |
| Model registry (`models.json`) | Tracks downloaded models, configs, GGUF metadata |

---

## Architecture After Migration

```
User request → llamactl proxy (:3000/v1)
  → API key auth
  → sampling injection (per-model defaults from registry)
  → forward to llama-server router (:8080)
    → router matches model field
    → spawns/routes to child subprocess
    → response back through proxy to client

llamactl management UI (:3000)
  → model config changes → regenerate preset.ini → POST /models/load to apply
  → start/stop models → POST /models/load, /models/unload
  → monitor → query /models for status
```

---

## Implementation Plan

### 1. Preset INI Generator

**New:** `internal/models/preset.go`

Generates the preset INI file from our model registry and configs:

```go
func GeneratePresetINI(models []*Model, configs map[string]*ModelConfig) string
```

- Writes `[*]` global section from a "server defaults" config
- Writes `[model-name]` sections for each model in the registry
- Maps our `ModelConfig` fields to INI keys:
  - `GPULayers` → `gpu-layers`
  - `ContextSize` → `ctx-size`
  - `TensorSplit` → `tensor-split`
  - `FlashAttention` → `flash-attn`
  - `Jinja` → `jinja`
  - `KVCacheQuant` → `cache-type-k` + `cache-type-v`
  - `DirectIO` → `direct-io`
  - `Threads` → `threads`
  - `GPUDevices` → needs investigation (see Open Questions)
  - Sampling params → `temp`, `top-k`, `top-p`, `min-p`, `presence-penalty`, `repeat-penalty`
- Model path: `model = {FilePath}`
- Model alias: `alias = {ID}` (so our registry ID works as the model field in API requests)

The INI file is written to `/data/config/preset.ini` and regenerated whenever model config changes.

### 2. Process Manager Simplification

**Modify:** `internal/process/manager.go`

Replace the multi-instance manager with a single-process manager for the router:

```go
type Manager struct {
    cmd       *exec.Cmd
    routerURL string  // http://localhost:8080
    // ... log streaming, health check
}

func (m *Manager) Start(binaryPath, presetPath, modelsDir string, modelsMax int) error
func (m *Manager) Stop() error
func (m *Manager) LoadModel(name string) error    // POST /models/load
func (m *Manager) UnloadModel(name string) error  // POST /models/unload
func (m *Manager) ListModels() ([]ModelStatus, error)  // GET /models
```

The manager starts one `llama-server` process with:
```
llama-server --models-dir /data/models \
             --models-preset /data/config/preset.ini \
             --models-max 0 \
             --host 0.0.0.0 --port 8080
```

### 3. Activation Flow Changes

**Current:** "Start" button → process manager spawns a new llama-server subprocess
**New:** "Start" button → `POST /models/load {"model": "name"}` to the router

**Current:** "Stop" button → process manager sends SIGTERM to subprocess
**New:** "Stop" button → `POST /models/unload {"model": "name"}` to the router

**Current:** Config change → stop + restart subprocess with new args
**New:** Config change → regenerate `preset.ini` → unload + reload model

### 4. Proxy Simplification

**Modify:** `internal/api/proxy.go`

- Remove all routing logic (model field parsing, instance resolution, fuzzy matching)
- Remove `/v1/models` aggregation
- Keep: API key auth middleware
- Keep: sampling injection (read model field, look up config, inject defaults)
- Forward everything to `localhost:8080` (single router process)

The proxy becomes a thin layer again — auth + sampling injection + forward.

### 5. Build/Server Decoupling

**Current:** Each `ModelConfig` has a `BuildID` that determines which llama-server binary to use.
**New:** The server build is configured globally, not per-model.

Add to application config (`llamactl.yaml`):
```yaml
active_build: "b8399-rocm"   # which llama.cpp build to use for the router
```

Remove `BuildID` from `ModelConfig`. Add a build selector to the Settings page instead of per-model config.

### 6. UI Changes

**Models page:**
- "Start" / "Stop" buttons call `/models/load` and `/models/unload` instead of our process manager
- Status comes from `GET /models` (loaded/loading/unloaded) instead of our process manager
- Chat UI links: the router exposes child processes on internal ports; we need to determine if these are accessible or if we route through the router's port

**Dashboard:**
- Active models list from `GET /models` (filter status == "loaded")
- Chat UI links may change (TBD — the router may proxy the child's web UI)

**Log viewer:**
- Single log stream from the router process (contains all model activity)
- Or: query the router for per-model logs (needs investigation)

**Config form:**
- Remove `BuildID` selector (moved to Settings)
- Everything else stays — config changes regenerate the preset INI

### 7. Port Exposure

**Current:** Ports 8080-8099 exposed, one per model.
**New:** Only port 8080 (router) needs to be exposed. The router handles internal port allocation for child processes.

However, the llama.cpp built-in chat web UI at the router port may not support model selection via URL. This needs investigation — if the router's web UI has a model dropdown (the blog article says it does), then a single port 8080 is sufficient.

### 8. Startup Sequence

1. llamactl starts, loads registry and configs
2. Generate `preset.ini` from registry
3. Start llama-server in router mode with `--models-dir /data/models --models-preset /data/config/preset.ini`
4. The router discovers models from both the directory and the preset
5. If `--models-max 0` (unlimited), no auto-eviction
6. Models load on first request (auto-load) or via explicit `/models/load`

---

## Open Questions

### GPU Pinning Per Model
Our current `GPUDevices` config sets `ROCR_VISIBLE_DEVICES` per subprocess. In the native router, child processes are spawned by the router, not by us. **Can the preset INI specify environment variables per model?** If not, we may lose per-model GPU pinning. Investigation needed — the router may pass through the parent's environment, or there may be a way to set env vars in presets.

**Potential workaround:** If presets don't support env vars, we could use `tensor-split` to achieve similar effect (e.g., `tensor-split = 1,0,0,0` to use only GPU 0).

### Per-Model Log Streaming
The current tabbed log viewer shows per-model logs. With the router, all logs from all child processes flow through the router's stdout. **Can we distinguish which model a log line came from?** The router prefixes log lines with instance names — need to verify format and parse accordingly.

### Chat UI Port Access
Currently each model's llama.cpp chat web UI is accessible on its own port (8080, 8081, etc.). With the router, child processes get internal ports that may not be exposed. **Does the router proxy the chat UI?** The blog says the router's web UI has a model dropdown, which would make individual port access unnecessary.

### Models Max Configuration
Should `--models-max` be:
- `0` (unlimited) — load everything the user asks for, rely on VRAM budget warnings
- Equal to the number of GPUs — practical default
- Configurable in Settings page

### Dynamic Model Loading vs Preset INI
The router supports loading models dynamically without a restart:

1. **Auto-load on first request** — if `--models-dir` points to the models directory and a request comes in with a matching `model` field, the router auto-discovers and loads the GGUF file. No preset entry needed.
2. **Explicit load** — `POST /models/load {"model": "name"}` loads a model immediately.

This means **starting a new model does not require an INI change or router restart** — the router finds the file in `--models-dir` and loads it with default/global settings.

The preset INI is for **persistent per-model configuration overrides** (custom context size, KV quant, tensor split, etc.). The question is: **does the router re-read the INI file when `/models/load` is called, or only at startup?**

From source analysis: `load_from_ini` is called in the constructor of `server_models`, which runs once at startup. The model mapping is populated then. When `/models/load` is called, it looks up the model in the existing mapping. **This suggests the INI is only read at startup.**

If confirmed, the implications are:
- **Starting a model with default settings** → just `POST /models/load`, no restart needed
- **Starting a model with custom settings for the first time** → need router restart to pick up the new INI section, OR use `POST /models/load` with the model path and accept global-only settings
- **Changing a running model's settings** → regenerate INI, unload, restart router, load

This needs **empirical testing** to confirm. If the router does NOT re-read the INI:
- We could work around it by using the router's global settings for common params and only using the INI for models that need specific overrides
- Or we could look into whether the `/models/load` endpoint accepts per-model parameters in the request body (needs investigation)
- Worst case: router restart is needed when adding new preset sections, but NOT when loading models that are already in `--models-dir` with acceptable defaults

### Preset Regeneration Timing
When we need to regenerate `preset.ini`:
- On model config save (if the model has custom settings that differ from global defaults)
- On model download complete (if we want the model to have a preset entry)
- On model delete/remove
- On startup (always, to sync registry with INI)

After regeneration, the router may need to be notified:
- If the router re-reads INI on `/models/load` → just load/reload the model
- If the router only reads INI at startup → restart required for new preset sections
- `/models/unload` + router restart + `/models/load` for changed settings

### Migration of Existing Configs
Existing `ModelConfig` entries have `BuildID`. We need to:
- Pick the first successful build as the active build on migration
- Remove `BuildID` from the config struct
- Add `active_build` to `Config`

---

## Order of Implementation

1. **Preset INI generator** — write and test the generator with existing model configs
2. **Build decoupling** — move BuildID to global config, add Settings UI
3. **Process manager simplification** — single router process, load/unload API
4. **Activation flow** — wire Start/Stop buttons to router API
5. **Proxy simplification** — remove routing logic, keep auth + sampling
6. **Status integration** — model status from `GET /models` instead of process manager
7. **Log viewer** — adapt to router's combined log stream
8. **Port cleanup** — reduce to single exposed port
9. **Testing** — validate routing, loading, unloading, preset changes, LRU eviction
10. **Cleanup** — remove dead code from old multi-process implementation

---

## Risk Assessment

| Risk | Impact | Mitigation |
|---|---|---|
| GPU pinning not supported in presets | Can't assign models to specific GPUs | Use tensor-split workaround; investigate env var support |
| Router log format changes | Log viewer breaks | Parse with prefix matching; degrade gracefully |
| Preset changes require model reload | Brief interruption when changing config | Only reload the affected model, not all |
| llama.cpp router mode has bugs | Models fail to load/route | Keep ability to fall back to direct llama-server (non-router) mode |
| Chat UI model dropdown missing | Can't use built-in chat UI for specific models | Expose child ports as fallback, or build our own chat UI |
