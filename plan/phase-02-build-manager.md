# Phase 2 — Build Manager

> GPU detection, llama.cpp clone/build, SSE log streaming, build profiles

---

## Goal

Users can detect available GPU backends, trigger llama.cpp builds with different configurations, watch build output in real-time via SSE, and manage compiled binaries — all from the browser.

---

## Deliverables

- GPU backend auto-detection (ROCm, Vulkan, CPU)
- llama.cpp git clone/fetch management
- CMake + ninja build orchestration with named profiles
- Real-time build log streaming via SSE
- Build listing, deletion, and metadata tracking
- Builds page UI with htmx interactions

---

## Files Created / Modified

### `internal/builder/detect.go`

GPU backend detection by probing system tools.

```go
package builder

type Backend struct {
    Name    string   // "rocm", "vulkan", "cpu"
    Available bool
    GPUs    []string // e.g. ["gfx1201", "gfx1201"] from rocminfo
    Info    string   // human-readable summary
}

// DetectBackends probes the system for available GPU compute backends.
// Checks:
//   - rocminfo exit code + parse GPU list → ROCm
//   - vulkaninfo exit code → Vulkan
//   - CPU always available as fallback
func DetectBackends() []Backend { ... }
```

### `internal/builder/profiles.go`

Named build profiles with cmake flags.

```go
package builder

type BuildProfile struct {
    Name      string            `json:"name"`
    Backend   string            `json:"backend"`    // "rocm", "vulkan", "cpu"
    CMakeFlags map[string]string `json:"cmake_flags"`
}

// DefaultProfiles returns built-in profiles for detected backends.
func DefaultProfiles() []BuildProfile {
    return []BuildProfile{
        {
            Name:    "rocm",
            Backend: "rocm",
            CMakeFlags: map[string]string{
                "GGML_ROCM":          "ON",
                "AMDGPU_TARGETS":     "gfx1201",
                "CMAKE_BUILD_TYPE":   "Release",
            },
        },
        {
            Name:    "vulkan",
            Backend: "vulkan",
            CMakeFlags: map[string]string{
                "GGML_VULKAN":        "ON",
                "CMAKE_BUILD_TYPE":   "Release",
            },
        },
        {
            Name:    "cpu",
            Backend: "cpu",
            CMakeFlags: map[string]string{
                "CMAKE_BUILD_TYPE":   "Release",
            },
        },
    }
}
```

### `internal/builder/builder.go`

Build orchestration — clone, cmake, ninja, collect artifacts.

```go
package builder

import (
    "context"
    "time"
)

type BuildRequest struct {
    Profile  string `json:"profile"`   // profile name or "rocm", "vulkan", "cpu"
    GitRef   string `json:"git_ref"`   // tag, branch, commit, or "latest"
}

type BuildResult struct {
    ID        string    `json:"id"`         // "<backend>-<short-sha>"
    Profile   string    `json:"profile"`
    GitSHA    string    `json:"git_sha"`
    GitRef    string    `json:"git_ref"`
    Status    string    `json:"status"`     // "building", "success", "failed"
    BinaryPath string  `json:"binary_path"`
    StartedAt time.Time `json:"started_at"`
    FinishedAt time.Time `json:"finished_at,omitempty"`
    Error     string    `json:"error,omitempty"`
}

type Builder struct {
    dataDir   string           // base data directory (/data)
    profiles  []BuildProfile
    builds    []BuildResult    // persisted to /data/config/builds.json
    logCh     map[string]chan string // build ID → log channel
}

func NewBuilder(dataDir string) *Builder { ... }

// Build runs the full build pipeline:
//   1. Ensure llama.cpp is cloned to /data/llama.cpp (or git fetch if exists)
//   2. Checkout the requested git ref
//   3. Create build dir, run cmake with profile flags
//   4. Run ninja/make, streaming stdout/stderr to logCh
//   5. Copy llama-server binary to /data/builds/<id>/
//   6. Update builds.json
func (b *Builder) Build(ctx context.Context, req BuildRequest) (*BuildResult, error) { ... }

// LogChannel returns the channel for streaming build logs
func (b *Builder) LogChannel(buildID string) (<-chan string, bool) { ... }

// List returns all builds
func (b *Builder) List() []BuildResult { ... }

// Delete removes a build and its files
func (b *Builder) Delete(id string) error { ... }
```

### `internal/api/build.go`

HTTP handlers for build management.

```go
package api

// Mounted in server.go under r.Route("/api/builds", ...)

// GET  /api/builds              → list all builds (JSON)
// POST /api/builds              → trigger build (JSON body: BuildRequest)
// GET  /api/builds/{id}/logs    → SSE stream of build output
// DELETE /api/builds/{id}       → delete a build
// GET  /api/builds/backends     → list detected backends (JSON)
```

Handler signatures:

```go
func (s *Server) handleListBuilds(w http.ResponseWriter, r *http.Request)
func (s *Server) handleTriggerBuild(w http.ResponseWriter, r *http.Request)
func (s *Server) handleBuildLogs(w http.ResponseWriter, r *http.Request)
func (s *Server) handleDeleteBuild(w http.ResponseWriter, r *http.Request)
func (s *Server) handleListBackends(w http.ResponseWriter, r *http.Request)
```

### `web/templates/builds.html`

Build management page with htmx interactions.

```html
{{define "content"}}
<h1>Builds</h1>

<article>
    <header>New Build</header>
    <form hx-post="/api/builds"
          hx-target="#build-output"
          hx-swap="innerHTML">
        <div class="grid">
            <label>
                Profile
                <select name="profile">
                    {{range .Backends}}
                    {{if .Available}}
                    <option value="{{.Name}}">{{.Name}}</option>
                    {{end}}
                    {{end}}
                </select>
            </label>
            <label>
                Git Ref
                <input type="text" name="git_ref" value="latest" placeholder="tag, branch, or commit">
            </label>
        </div>
        <button type="submit">Start Build</button>
    </form>
</article>

<div id="build-output"></div>

<article>
    <header>Compiled Builds</header>
    <div id="build-list" hx-get="/api/builds" hx-trigger="load" hx-swap="innerHTML">
        Loading...
    </div>
</article>
{{end}}
```

### `web/templates/partials/build_card.html`

Single build row rendered as a table row or card.

```html
{{define "build_card"}}
<tr>
    <td><kbd>{{.ID}}</kbd></td>
    <td><mark>{{.Profile}}</mark></td>
    <td><code>{{slice .GitSHA 0 7}}</code></td>
    <td>{{.GitRef}}</td>
    <td>{{.Status}}</td>
    <td>{{.StartedAt.Format "2006-01-02 15:04"}}</td>
    <td>
        <button hx-delete="/api/builds/{{.ID}}"
                hx-confirm="Delete this build?"
                hx-target="closest tr"
                hx-swap="outerHTML">
            Delete
        </button>
    </td>
</tr>
{{end}}
```

### `web/templates/partials/build_log.html`

Build log streaming container. Returned by `POST /api/builds` to initiate SSE connection.

```html
{{define "build_log"}}
<article>
    <header>Build Log — {{.BuildID}}</header>
    <pre id="log-output"
         hx-ext="sse"
         sse-connect="/api/builds/{{.BuildID}}/logs"
         sse-swap="message"
         hx-swap="beforeend"
         style="max-height: 400px; overflow-y: auto; font-size: 0.85rem;">
    </pre>
</article>
{{end}}
```

---

## Server.go Modifications

Add `Builder` to the `Server` struct and mount routes:

```go
type Server struct {
    cfg       *config.Config
    templates *template.Template
    router    chi.Router
    builder   *builder.Builder  // added
}

// In buildRouter():
r.Route("/api/builds", func(r chi.Router) {
    r.Get("/", s.handleListBuilds)
    r.Post("/", s.handleTriggerBuild)
    r.Get("/backends", s.handleListBackends)
    r.Get("/{id}/logs", s.handleBuildLogs)
    r.Delete("/{id}", s.handleDeleteBuild)
})
```

---

## SSE Log Streaming Pattern

The build process writes log lines to a channel. The SSE handler reads from the channel and sends each line as an event:

```go
func (s *Server) handleBuildLogs(w http.ResponseWriter, r *http.Request) {
    id := chi.URLParam(r, "id")
    ch, ok := s.builder.LogChannel(id)
    if !ok {
        http.NotFound(w, r)
        return
    }

    sse, err := NewSSEWriter(w)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    for {
        select {
        case line, ok := <-ch:
            if !ok {
                sse.SendEvent("done", "Build complete")
                return
            }
            sse.SendData(line)
        case <-r.Context().Done():
            return
        }
    }
}
```

---

## Build Pipeline Steps

1. **Clone or fetch**: If `/data/llama.cpp` doesn't exist, `git clone https://github.com/ggml-org/llama.cpp`. Otherwise `git fetch --all`.
2. **Checkout ref**: `git checkout <ref>`. If `ref == "latest"`, find the latest release tag via `git tag --sort=-v:refname | head -1`.
3. **Get SHA**: `git rev-parse --short HEAD`
4. **Create build dir**: `/data/llama.cpp/build-<backend>` (temp)
5. **Run cmake**: `cmake .. -G Ninja <flags from profile>`
6. **Run ninja**: `ninja -j$(nproc) llama-server`
7. **Install**: Copy `llama-server` binary to `/data/builds/<backend>-<sha>/llama-server`
8. **Record**: Append build result to `/data/config/builds.json`
9. **Cleanup**: Remove temp build dir

All stdout/stderr from steps 5-6 is streamed to the log channel.

---

## What You Can Do at End of Phase

- Visit `/builds` → see detected GPU backends
- Select a backend and git ref, click "Start Build"
- Watch live cmake/ninja output stream into the page via SSE
- See completed builds listed with backend badge, git SHA, and date
- Delete old builds
- Build artifacts stored persistently in `/data/builds/`
