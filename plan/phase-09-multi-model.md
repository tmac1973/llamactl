# Phase 9 — Multi-Model Concurrent Loading

> Load and serve multiple llama.cpp models simultaneously with per-model GPU allocation and VRAM budget tracking

---

## Goal

Allow llamactl to run multiple llama-server processes concurrently, each serving a different model. The OpenAI-compatible proxy routes requests to the correct backend based on the `model` field. A VRAM budget system tracks GPU memory across all loaded models to prevent oversubscription. Users can activate and deactivate individual models independently.

---

## Architecture: Multiple Processes

Each model gets its own llama-server process on a dedicated port. This approach was chosen over llama-server's built-in multi-model support because it provides:

- Independent lifecycle management (start/stop/restart per model)
- Per-model resource tuning (threads, GPU layers, context size, sampling)
- Per-model log streams
- Fault isolation (one model crashing doesn't affect others)
- Optional GPU pinning (assign different models to different GPUs)

### Request Routing

```
Client → /v1/chat/completions (model: "qwen3-32b")
       → llamactl proxy
       → parse "model" field from request body
       → look up port for "qwen3-32b"
       → forward to localhost:{port}
       → stream response back
```

If the `model` field is empty or unrecognized, the proxy falls back to a configurable default model (or returns a clear error).

---

## Deliverables

- Process manager refactored from single-process to multi-process
- Dynamic port allocation for llama-server instances
- Proxy routing layer based on `model` field in request body
- VRAM budget tracker with per-GPU accounting
- Optional GPU pinning via `ROCR_VISIBLE_DEVICES` / `CUDA_VISIBLE_DEVICES`
- UI updates: multiple active models, per-model controls, VRAM usage display
- Default model configuration

---

## Implementation

### 1. Process Manager (`internal/process/manager.go`)

**Current state:** `Manager` holds a single `*exec.Cmd`, `*LaunchConfig`, and `Status`.

**New design:** `Manager` becomes a container for multiple named instances.

```go
type Instance struct {
    ID        string          // model registry ID
    Cmd       *exec.Cmd
    Cancel    context.CancelFunc
    Config    *LaunchConfig
    Status    Status
    Port      int
    Done      chan struct{}
    HealthURL string
}

type Manager struct {
    mu        sync.Mutex
    instances map[string]*Instance  // keyed by model ID
    portRange [2]int                // e.g. [8080, 8099]
    // ... subscribers, log history per instance
}
```

Key changes:
- `Start(id string, cfg LaunchConfig) error` — starts a named instance, auto-assigns port
- `Stop(id string) error` — stops a specific instance
- `StopAll() error` — stops everything (for shutdown)
- `GetStatus(id string) Status` — per-instance status
- `ListActive() []Status` — all running instances
- `Subscribe(id string) chan string` — per-instance log stream

Port allocation: maintain a pool from `portRange`, assign next available on Start, reclaim on Stop. Ports are internal (not exposed outside the container), so a fixed range like 8080-8099 is fine.

### 2. Launch Config Changes

Add a `Port` override and GPU pinning:

```go
type LaunchConfig struct {
    // ... existing fields ...
    Port           int       // assigned by manager, not user
    VisibleDevices string    // "0", "1", "0,1" — maps to ROCR_VISIBLE_DEVICES
}
```

When `VisibleDevices` is set, the process environment gets `ROCR_VISIBLE_DEVICES={value}` (ROCm) or `CUDA_VISIBLE_DEVICES={value}` (CUDA). When empty, the process sees all GPUs (current behavior).

### 3. Model Config Changes (`internal/models/registry.go`)

Add GPU assignment to `ModelConfig`:

```go
type ModelConfig struct {
    // ... existing fields ...
    GPUDevices string `json:"gpu_devices"` // "", "0", "1", "0,1" — empty = all
}
```

This maps to `VisibleDevices` in `LaunchConfig`. The UI presents it as a dropdown or multi-select populated from detected GPU list.

### 4. VRAM Budget Tracker (`internal/models/vram.go`)

New component that tracks VRAM capacity and usage per GPU.

```go
type GPUInfo struct {
    Index     int
    Name      string
    VRAMTotal float64  // GB
}

type VRAMBudget struct {
    GPUs       []GPUInfo
    Allocated  map[int]float64  // GPU index → total allocated GB
}

func (b *VRAMBudget) CanFit(model *Model, cfg *ModelConfig) (bool, string)
func (b *VRAMBudget) Allocate(modelID string, model *Model, cfg *ModelConfig)
func (b *VRAMBudget) Release(modelID string)
func (b *VRAMBudget) Usage() map[int]GPUUsage  // for UI display
```

VRAM estimation per model uses the existing `VRAMEstGB` field (computed at download time from file size and quantization), plus a KV cache estimate based on `ContextSize`. The estimate is conservative — it's better to block an activation than to OOM.

For tensor-split models, the estimate is divided across GPUs according to the split ratio.

GPU detection at startup:
- ROCm: parse `rocm-smi --showmeminfo vram` or read from `/sys/class/drm/card*/device/mem_info_vram_total`
- CUDA: parse `nvidia-smi --query-gpu=index,name,memory.total --format=csv`
- Fallback: manual configuration in settings

### 5. Proxy Routing (`internal/api/proxy.go`)

**Current state:** single reverse proxy to one port.

**New design:** the proxy reads the `model` field from the request body and routes to the correct backend.

```go
func (s *Server) newProxyHandler() http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Read and parse request body
        body, _ := io.ReadAll(r.Body)
        r.Body = io.NopCloser(bytes.NewReader(body))

        var req struct {
            Model string `json:"model"`
        }
        json.Unmarshal(body, &req)

        // Resolve target instance
        instance := s.resolveModel(req.Model)
        if instance == nil {
            // JSON error: model not loaded
            return
        }

        // Inject sampling defaults (existing logic, now per-model)
        s.injectSamplingDefaults(r, instance.ID)

        // Proxy to instance port
        target := &url.URL{
            Scheme: "http",
            Host:   fmt.Sprintf("localhost:%d", instance.Port),
        }
        proxy := httputil.NewSingleHostReverseProxy(target)
        proxy.FlushInterval = 50 * time.Millisecond
        proxy.ServeHTTP(w, r)
    })
}
```

`resolveModel` logic:
1. Exact match on model ID in active instances
2. Fuzzy match (e.g., "qwen3-32b" matches "Qwen/Qwen3-32B-GGUF")
3. If model field is empty, use the configured default model
4. If no match, return error listing available models

**Performance note:** creating a new `httputil.ReverseProxy` per request is lightweight (it's just a struct with a URL). For higher throughput, we could cache proxies per port, but this is unlikely to matter for local inference workloads.

### 6. Server Changes (`internal/api/server.go`, `service.go`)

- Replace `activeModelID string` with access to `process.Manager.ListActive()`
- `handleActivateModel` no longer calls `Stop()` first — it just starts an additional instance
- Add `handleDeactivateModel` endpoint: `DELETE /api/models/{id}/activate`
- `handleServiceStop` stops all instances
- Add `GET /api/models/active` — returns all running models with their ports and status

### 7. UI Changes (`web/templates/`)

**Models page:**
- Multiple models can show "Active" status simultaneously
- Each active model gets a "Stop" button (instead of the current "activating replaces" behavior)
- "Start" button activates a model without stopping others
- VRAM usage bar per GPU, showing allocated vs. total
- Warning indicator when VRAM is near capacity

**Model config form:**
- New "GPU Devices" field: dropdown with options like "All GPUs", "GPU 0", "GPU 1", "GPU 0 + GPU 1"
- Populated dynamically from detected hardware

**Service/dashboard:**
- Show list of active models with individual status, uptime, and controls
- Per-model log viewer (tab or dropdown to switch between instances)

### 8. Default Model

Add to application config:

```go
type Config struct {
    // ... existing fields ...
    DefaultModel string `yaml:"default_model"`
}
```

When a request arrives with no `model` field, or the `model` field doesn't match any active instance, the default model handles it. Configurable in Settings page.

---

## Migration

Existing single-model configs and behavior remain compatible:
- If only one model is activated, behavior is identical to current
- The `activeModelID` field on `Server` is replaced by the multi-instance map
- Existing `models.json` configs are unchanged (new `gpu_devices` field defaults to empty = all GPUs)
- Port assignment is automatic and internal

---

## Constraints and Limits

- **Port range:** 20 ports (8080-8099) by default, configurable. Each active model uses one port.
- **VRAM hard limit:** activation is blocked (with error message) if estimated VRAM would exceed GPU capacity. Override flag available for users who know better.
- **No hot-reload:** changing a model's config while it's running requires deactivate + reactivate.
- **Container networking:** all ports are internal to the container. Only the llamactl port (3000) is exposed. The proxy handles all external traffic.

---

## Order of Implementation

1. **Process manager refactor** — multi-instance support with port allocation
2. **Activation flow** — start without stop, add deactivate endpoint
3. **Proxy routing** — model-based request routing
4. **GPU detection + VRAM budget** — hardware discovery and memory accounting
5. **Model config: GPU devices** — per-model GPU pinning
6. **UI updates** — multi-active display, VRAM bars, per-model controls
7. **Default model config** — fallback routing
8. **Testing** — concurrent model loading, routing accuracy, VRAM edge cases
