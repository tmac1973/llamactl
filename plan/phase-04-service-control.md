# Phase 4 — Service Control

> Subprocess lifecycle (spawn/stop/restart), model config, live logs, health checks

---

## Goal

Users can start, stop, and restart `llama-server` as a managed subprocess, configure per-model launch parameters, watch live server output, and swap between models — all from the browser. The process manager replaces systemd with direct `os/exec` subprocess control.

---

## Deliverables

- Process manager for llama-server subprocess lifecycle
- Model activation (stop → reconfigure → restart)
- Per-model launch configuration (GPU layers, tensor split, context size, etc.)
- Live log streaming from subprocess stdout/stderr via SSE
- Health checking via llama-server's `/health` endpoint
- Service control UI with status, controls, config editor, and log viewer

---

## Files Created / Modified

### `internal/process/manager.go`

Subprocess lifecycle manager. Spawns llama-server, captures output, handles signals.

```go
package process

import (
    "context"
    "os/exec"
    "sync"
    "time"
)

type Status struct {
    State      string    `json:"state"`       // "stopped", "starting", "running", "failed"
    PID        int       `json:"pid,omitempty"`
    Model      string    `json:"model,omitempty"`
    BuildID    string    `json:"build_id,omitempty"`
    Uptime     string    `json:"uptime,omitempty"`
    StartedAt  time.Time `json:"started_at,omitempty"`
    Error      string    `json:"error,omitempty"`
    HealthOK   bool      `json:"health_ok"`
}

type LaunchConfig struct {
    BinaryPath  string   // path to llama-server binary
    ModelPath   string   // path to .gguf file
    GPULayers   int      // --n-gpu-layers
    TensorSplit string   // --tensor-split
    ContextSize int      // --ctx-size
    Threads     int      // --threads
    Host        string   // --host (default "0.0.0.0")
    Port        int      // --port (default 8080)
    ExtraFlags  []string // additional CLI flags
}

type Manager struct {
    mu         sync.Mutex
    cmd        *exec.Cmd
    cancel     context.CancelFunc
    status     Status
    logCh      chan string    // buffered channel for log lines
    config     *LaunchConfig
    healthURL  string        // e.g. "http://localhost:8080/health"
}

func NewManager() *Manager { ... }

// Start spawns llama-server with the given config.
// Returns error if already running (must Stop first).
// Captures stdout + stderr → logCh for SSE streaming.
// Starts background goroutine to monitor process exit.
func (m *Manager) Start(cfg LaunchConfig) error {
    // Build command line:
    // /data/builds/<build>/llama-server \
    //   --model <path> \
    //   --n-gpu-layers <n> \
    //   --tensor-split <split> \
    //   --ctx-size <size> \
    //   --threads <n> \
    //   --host 0.0.0.0 \
    //   --port 8080 \
    //   <extra flags>
    ...
}

// Stop sends SIGTERM, waits up to 10s, then SIGKILL.
func (m *Manager) Stop() error { ... }

// Restart stops then starts with the same config.
func (m *Manager) Restart() error { ... }

// Status returns current process status.
func (m *Manager) Status() Status { ... }

// LogChannel returns the channel for reading log lines.
func (m *Manager) LogChannel() <-chan string { ... }

// CheckHealth pings the llama-server /health endpoint.
func (m *Manager) CheckHealth() bool { ... }
```

Key implementation details:

**Output capture**: Pipe stdout and stderr through a `bufio.Scanner` in a goroutine, sending each line to `logCh`. The channel is buffered (e.g. 1000 lines) and uses a ring-buffer pattern to drop old lines if the consumer falls behind.

**Process monitoring**: A background goroutine calls `cmd.Wait()`. On unexpected exit, sets state to `"failed"` with the exit error.

**Graceful stop**: Send SIGTERM → wait up to 10 seconds → SIGKILL if still running.

**Health checking**: After Start, poll `http://localhost:<port>/health` every 2 seconds until it responds 200, then set state to `"running"`. If it doesn't respond within 60 seconds, set state to `"failed"`.

### `internal/api/service.go`

HTTP handlers for service control.

```go
package api

// Mounted under r.Route("/api/service", ...)

// GET  /api/service/status    → current process status (JSON)
// POST /api/service/start     → start llama-server
// POST /api/service/stop      → stop llama-server
// POST /api/service/restart   → restart llama-server
// GET  /api/service/logs      → SSE stream of process output
// GET  /api/service/health    → health check result (JSON)

func (s *Server) handleServiceStatus(w http.ResponseWriter, r *http.Request)
func (s *Server) handleServiceStart(w http.ResponseWriter, r *http.Request)
func (s *Server) handleServiceStop(w http.ResponseWriter, r *http.Request)
func (s *Server) handleServiceRestart(w http.ResponseWriter, r *http.Request)
func (s *Server) handleServiceLogs(w http.ResponseWriter, r *http.Request)
func (s *Server) handleServiceHealth(w http.ResponseWriter, r *http.Request)
```

### `internal/api/models.go` — Modified

Add activation and config endpoints to the existing models routes:

```go
// Added to r.Route("/api/models", ...):
// PUT /api/models/{id}/activate → activate model (stop service, apply config, start)
// PUT /api/models/{id}/config   → update model launch config
// GET /api/models/{id}/config   → get model launch config

func (s *Server) handleActivateModel(w http.ResponseWriter, r *http.Request)
func (s *Server) handleUpdateModelConfig(w http.ResponseWriter, r *http.Request)
func (s *Server) handleGetModelConfig(w http.ResponseWriter, r *http.Request)
```

**Model activation flow:**
1. Stop current llama-server if running
2. Load model's `ModelConfig` from registry
3. Resolve build binary path from `config.BuildID`
4. Construct `LaunchConfig` from model config
5. Start llama-server with new config
6. Wait for health check to pass
7. Return new status

### `web/templates/service.html`

Service control page.

```html
{{define "content"}}
<h1>Service</h1>

<div id="service-status"
     hx-get="/api/service/status"
     hx-trigger="load, every 5s"
     hx-swap="innerHTML">
    Loading...
</div>

<article>
    <header>Server Logs</header>
    <pre id="service-logs"
         hx-ext="sse"
         sse-connect="/api/service/logs"
         sse-swap="message"
         hx-swap="beforeend"
         style="max-height: 500px; overflow-y: auto; font-size: 0.85rem;">
    </pre>
</article>

<article>
    <header>Model Configuration</header>
    <div id="model-config"
         hx-get="/api/models/{{.ActiveModelID}}/config"
         hx-trigger="load"
         hx-swap="innerHTML">
        No model active.
    </div>
</article>
{{end}}
```

### `web/templates/partials/service_status.html`

Service status display with control buttons.

```html
{{define "service_status"}}
<article>
    <header>
        Service Status:
        {{if eq .State "running"}}
            <ins>Running</ins>
        {{else if eq .State "starting"}}
            <mark>Starting...</mark>
        {{else if eq .State "failed"}}
            <del>Failed</del>
        {{else}}
            Stopped
        {{end}}
    </header>

    {{if .Model}}
    <p>Model: <strong>{{.Model}}</strong></p>
    {{end}}
    {{if .BuildID}}
    <p>Build: <kbd>{{.BuildID}}</kbd></p>
    {{end}}
    {{if .Uptime}}
    <p>Uptime: {{.Uptime}}</p>
    {{end}}
    {{if .Error}}
    <p><small style="color: var(--pico-del-color)">{{.Error}}</small></p>
    {{end}}

    <div role="group">
        {{if ne .State "running"}}
        <button hx-post="/api/service/start"
                hx-target="#service-status"
                hx-swap="innerHTML">
            Start
        </button>
        {{end}}
        {{if eq .State "running"}}
        <button hx-post="/api/service/stop"
                hx-target="#service-status"
                hx-swap="innerHTML"
                class="secondary">
            Stop
        </button>
        <button hx-post="/api/service/restart"
                hx-target="#service-status"
                hx-swap="innerHTML"
                class="outline">
            Restart
        </button>
        {{end}}
    </div>
</article>
{{end}}
```

### `web/templates/partials/model_config.html`

Model configuration form.

```html
{{define "model_config"}}
<form hx-put="/api/models/{{.ModelID}}/config"
      hx-target="this"
      hx-swap="outerHTML">

    <label>
        Build
        <select name="build_id">
            {{range .AvailableBuilds}}
            <option value="{{.ID}}" {{if eq .ID $.Config.BuildID}}selected{{end}}>
                {{.ID}} ({{.Profile}})
            </option>
            {{end}}
        </select>
    </label>

    <div class="grid">
        <label>
            GPU Layers
            <input type="number" name="gpu_layers" value="{{.Config.GPULayers}}"
                   min="0" max="999" placeholder="999 = all layers">
        </label>
        <label>
            Context Size
            <input type="number" name="context_size" value="{{.Config.ContextSize}}"
                   min="512" step="512" placeholder="8192">
        </label>
    </div>

    <div class="grid">
        <label>
            Tensor Split
            <input type="text" name="tensor_split" value="{{.Config.TensorSplit}}"
                   placeholder="0.5,0.5 (for dual GPU)">
        </label>
        <label>
            Threads
            <input type="number" name="threads" value="{{.Config.Threads}}"
                   min="1" placeholder="8">
        </label>
    </div>

    <label>
        Extra Flags
        <input type="text" name="extra_flags" value="{{.Config.ExtraFlags}}"
               placeholder="--flash-attn --cont-batching">
    </label>

    <div role="group">
        <button type="submit">Save Config</button>
        <button type="button"
                hx-put="/api/models/{{.ModelID}}/activate"
                hx-target="#service-status"
                hx-swap="innerHTML"
                class="outline">
            Save &amp; Restart
        </button>
    </div>
</form>
{{end}}
```

### `web/templates/partials/service_logs.html`

Log streaming container (used as initial SSE target).

```html
{{define "service_logs"}}
<div hx-ext="sse"
     sse-connect="/api/service/logs"
     sse-swap="message"
     hx-swap="beforeend">
</div>
{{end}}
```

---

## Server.go Modifications

Add process manager to `Server`:

```go
type Server struct {
    cfg        *config.Config
    templates  *template.Template
    router     chi.Router
    builder    *builder.Builder
    hfClient   *huggingface.Client
    downloader *huggingface.Downloader
    registry   *models.Registry
    process    *process.Manager  // added
}

// In buildRouter():
r.Route("/api/service", func(r chi.Router) {
    r.Get("/status", s.handleServiceStatus)
    r.Post("/start", s.handleServiceStart)
    r.Post("/stop", s.handleServiceStop)
    r.Post("/restart", s.handleServiceRestart)
    r.Get("/logs", s.handleServiceLogs)
    r.Get("/health", s.handleServiceHealth)
})

// Add to existing /api/models route:
r.Put("/{id}/activate", s.handleActivateModel)
r.Put("/{id}/config", s.handleUpdateModelConfig)
r.Get("/{id}/config", s.handleGetModelConfig)
```

---

## Log Streaming Pattern

Service logs use the same SSE utility as builds, but with a fan-out pattern since multiple browser tabs may want to see logs simultaneously:

```go
func (s *Server) handleServiceLogs(w http.ResponseWriter, r *http.Request) {
    sse, err := NewSSEWriter(w)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    ch := s.process.LogChannel()
    for {
        select {
        case line, ok := <-ch:
            if !ok {
                sse.SendEvent("done", "Process exited")
                return
            }
            sse.SendData(line)
        case <-r.Context().Done():
            return
        }
    }
}
```

Note: The `Manager.LogChannel()` should return a new channel per caller (fan-out) so multiple SSE connections don't steal messages from each other. Use a broadcast pattern internally.

---

## What You Can Do at End of Phase

- Visit `/service` → see process status (running/stopped/failed) with uptime
- Click Start/Stop/Restart to control the llama-server subprocess
- Watch live llama-server output stream into the page
- Visit `/models` → click "Activate" on any model → service restarts with new model
- Configure per-model settings: GPU layers, tensor split, context size, threads, extra flags
- Saved configs persist across restarts
- Health check confirms llama-server is responding
- llama-server's own chat UI is accessible at `http://host:8080`
