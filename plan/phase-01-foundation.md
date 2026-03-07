# Phase 1 — Foundation

> Go scaffold, chi router, htmx+Pico UI shell, config, Dockerfile, embedded templates

---

## Goal

A running Go web server that serves an HTML UI shell with navigation, loads configuration from YAML, and can be built into a container image. No functional features yet — just the skeleton that all subsequent phases build on.

---

## Deliverables

- Go module with `cmd/llamactl/main.go` entry point
- `chi` router with middleware (logging, recovery)
- Embedded HTML templates using `html/template` + `//go:embed`
- Pico CSS + htmx served as static assets
- YAML configuration loading with sensible defaults
- Base `layout.html` template with sidebar navigation
- Placeholder pages for all sections (Models, Builds, Service, Settings)
- Dockerfile (basic, refined in Phase 6)
- Makefile with `build`, `run`, `dev` targets

---

## Files Created

### `cmd/llamactl/main.go`

Entry point. Parses flags, loads config, creates `Server`, starts HTTP listener with graceful shutdown.

```go
package main

import (
    "context"
    "flag"
    "log/slog"
    "net/http"
    "os"
    "os/signal"
    "syscall"

    "github.com/tmlabonte/llamactl/internal/api"
    "github.com/tmlabonte/llamactl/internal/config"
)

func main() {
    configPath := flag.String("config", "/data/config/llamactl.yaml", "config file path")
    flag.Parse()

    cfg, err := config.Load(*configPath)
    if err != nil {
        slog.Error("failed to load config", "error", err)
        os.Exit(1)
    }

    srv := api.NewServer(cfg)

    httpSrv := &http.Server{
        Addr:    cfg.ListenAddr,
        Handler: srv.Router(),
    }

    // Graceful shutdown
    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    go func() {
        slog.Info("listening", "addr", cfg.ListenAddr)
        if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
            slog.Error("server error", "error", err)
            os.Exit(1)
        }
    }()

    <-ctx.Done()
    slog.Info("shutting down")
    httpSrv.Shutdown(context.Background())
}
```

### `internal/config/config.go`

YAML config loading with defaults.

```go
package config

import (
    "os"
    "gopkg.in/yaml.v3"
)

type Config struct {
    ListenAddr   string `yaml:"listen_addr"`    // default ":3000"
    DataDir      string `yaml:"data_dir"`        // default "/data"
    LlamaPort    int    `yaml:"llama_port"`      // default 8080
    HFToken      string `yaml:"hf_token"`        // optional HuggingFace token
    APIKey       string `yaml:"api_key"`         // optional API key for /v1/* proxy
    LogLevel     string `yaml:"log_level"`       // default "info"
}

func Load(path string) (*Config, error) {
    cfg := &Config{
        ListenAddr: ":3000",
        DataDir:    "/data",
        LlamaPort:  8080,
        LogLevel:   "info",
    }

    data, err := os.ReadFile(path)
    if err != nil {
        if os.IsNotExist(err) {
            return cfg, nil // use defaults
        }
        return nil, err
    }

    if err := yaml.Unmarshal(data, cfg); err != nil {
        return nil, err
    }
    return cfg, nil
}
```

### `internal/api/server.go`

Central `Server` struct holding all dependencies. Creates chi router with middleware and mounts all routes.

```go
package api

import (
    "html/template"
    "net/http"

    "github.com/go-chi/chi/v5"
    "github.com/go-chi/chi/v5/middleware"
    "github.com/tmlabonte/llamactl/internal/config"
    "github.com/tmlabonte/llamactl/web"
)

type Server struct {
    cfg       *config.Config
    templates *template.Template
    router    chi.Router
}

func NewServer(cfg *config.Config) *Server {
    s := &Server{cfg: cfg}
    s.templates = template.Must(template.ParseFS(web.Templates, "templates/*.html", "templates/partials/*.html"))
    s.router = s.buildRouter()
    return s
}

func (s *Server) Router() http.Handler {
    return s.router
}

func (s *Server) buildRouter() chi.Router {
    r := chi.NewRouter()
    r.Use(middleware.Logger)
    r.Use(middleware.Recoverer)
    r.Use(middleware.Compress(5))

    // Static assets (htmx, Pico CSS)
    r.Handle("/static/*", http.StripPrefix("/static/",
        http.FileServer(http.FS(web.Static))))

    // Page routes (server-rendered HTML)
    r.Get("/", s.handleIndex)
    r.Get("/builds", s.handleBuildsPage)
    r.Get("/models", s.handleModelsPage)
    r.Get("/models/browse", s.handleModelsBrowsePage)
    r.Get("/service", s.handleServicePage)
    r.Get("/settings", s.handleSettingsPage)

    // API routes mounted in later phases
    r.Route("/api", func(r chi.Router) {
        // Phase 2: r.Route("/builds", ...)
        // Phase 3: r.Route("/models", ...), r.Route("/hf", ...)
        // Phase 4: r.Route("/service", ...)
        // Phase 5: r.Route("/settings", ...)
    })

    // Phase 5: r.Handle("/v1/*", proxyHandler)

    return r
}

// Page handlers — render full HTML pages
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
    s.render(w, "index.html", nil)
}

func (s *Server) handleBuildsPage(w http.ResponseWriter, r *http.Request) {
    s.render(w, "builds.html", nil)
}

func (s *Server) handleModelsPage(w http.ResponseWriter, r *http.Request) {
    s.render(w, "models.html", nil)
}

func (s *Server) handleModelsBrowsePage(w http.ResponseWriter, r *http.Request) {
    s.render(w, "models_browse.html", nil)
}

func (s *Server) handleServicePage(w http.ResponseWriter, r *http.Request) {
    s.render(w, "service.html", nil)
}

func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
    s.render(w, "settings.html", nil)
}

// render executes a named template inside the layout
func (s *Server) render(w http.ResponseWriter, name string, data any) {
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
        http.Error(w, "template error", http.StatusInternalServerError)
    }
}
```

### `internal/api/middleware.go`

Placeholder for custom middleware (API key auth added in Phase 5).

```go
package api

// Custom middleware added in Phase 5 (API key auth for /v1/* proxy)
```

### `internal/api/sse.go`

Shared SSE utility used by build logs (Phase 2), download progress (Phase 3), and service logs (Phase 4).

```go
package api

import (
    "fmt"
    "net/http"
)

// SSEWriter wraps an http.ResponseWriter for Server-Sent Events
type SSEWriter struct {
    w       http.ResponseWriter
    flusher http.Flusher
}

func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, error) {
    flusher, ok := w.(http.Flusher)
    if !ok {
        return nil, fmt.Errorf("streaming not supported")
    }

    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")

    return &SSEWriter{w: w, flusher: flusher}, nil
}

func (s *SSEWriter) SendEvent(event, data string) error {
    if event != "" {
        fmt.Fprintf(s.w, "event: %s\n", event)
    }
    fmt.Fprintf(s.w, "data: %s\n\n", data)
    s.flusher.Flush()
    return nil
}

func (s *SSEWriter) SendData(data string) error {
    return s.SendEvent("", data)
}
```

### `web/embed.go`

Go embed declarations for templates and static files.

```go
package web

import "embed"

//go:embed templates/*.html templates/partials/*.html
var Templates embed.FS

//go:embed static/*
var Static embed.FS
```

### `web/templates/layout.html`

Base layout with Pico CSS, htmx, and sidebar navigation.

```html
{{define "layout"}}
<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>LlamaCtl{{if .Title}} — {{.Title}}{{end}}</title>
    <link rel="stylesheet" href="/static/pico.min.css">
    <script src="/static/htmx.min.js"></script>
    <script src="/static/htmx-sse.js"></script>
    <style>
        body { display: flex; min-height: 100vh; }
        nav.sidebar { width: 220px; padding: 1rem; border-right: 1px solid var(--pico-muted-border-color); }
        nav.sidebar ul { list-style: none; padding: 0; }
        nav.sidebar li { margin-bottom: 0.5rem; }
        nav.sidebar a[aria-current="page"] { font-weight: bold; }
        main { flex: 1; padding: 1.5rem; max-width: 1200px; }
    </style>
</head>
<body>
    <nav class="sidebar">
        <hgroup>
            <h3>LlamaCtl</h3>
            <p>Inference Manager</p>
        </hgroup>
        <ul>
            <li><a href="/"{{if eq .Nav "home"}} aria-current="page"{{end}}>Dashboard</a></li>
            <li><a href="/builds"{{if eq .Nav "builds"}} aria-current="page"{{end}}>Builds</a></li>
            <li><a href="/models"{{if eq .Nav "models"}} aria-current="page"{{end}}>Models</a></li>
            <li><a href="/models/browse"{{if eq .Nav "browse"}} aria-current="page"{{end}}>Browse HF</a></li>
            <li><a href="/service"{{if eq .Nav "service"}} aria-current="page"{{end}}>Service</a></li>
            <li><a href="/settings"{{if eq .Nav "settings"}} aria-current="page"{{end}}>Settings</a></li>
        </ul>
    </nav>
    <main class="container">
        {{template "content" .}}
    </main>
</body>
</html>
{{end}}
```

### `web/templates/index.html`

Dashboard placeholder.

```html
{{define "content"}}
<h1>Dashboard</h1>
<p>Welcome to LlamaCtl. Use the sidebar to manage builds, models, and the inference service.</p>

<div class="grid">
    <article>
        <header>Service</header>
        <p id="service-status">Loading...</p>
    </article>
    <article>
        <header>Active Model</header>
        <p id="active-model">None</p>
    </article>
    <article>
        <header>Builds</header>
        <p id="build-count">—</p>
    </article>
</div>
{{end}}
```

### Other page templates

`builds.html`, `models.html`, `models_browse.html`, `service.html`, `settings.html` — each follows the same pattern:

```html
{{define "content"}}
<h1>Page Title</h1>
<!-- Populated in their respective phases -->
{{end}}
```

### `web/static/`

- `htmx.min.js` — htmx library (downloaded, ~14KB gzipped)
- `htmx-sse.js` — htmx SSE extension
- `pico.min.css` — Pico CSS (downloaded, ~10KB gzipped)

### `Makefile`

```makefile
.PHONY: build run dev clean

build:
	go build -o bin/llamactl ./cmd/llamactl

run: build
	./bin/llamactl --config config.yaml

dev:
	go run ./cmd/llamactl --config config.yaml

clean:
	rm -rf bin/
```

### `Dockerfile`

Basic single-stage for development (refined in Phase 6).

```dockerfile
FROM golang:1.22-bookworm AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o llamactl ./cmd/llamactl

FROM debian:trixie-slim
COPY --from=builder /app/llamactl /usr/local/bin/llamactl
VOLUME ["/data"]
EXPOSE 3000
ENTRYPOINT ["llamactl"]
```

### `go.mod`

```
module github.com/tmlabonte/llamactl

go 1.22

require (
    github.com/go-chi/chi/v5 v5.1.0
    gopkg.in/yaml.v3 v3.0.1
)
```

---

## Template Rendering Pattern

All pages use Go's `html/template` with a shared layout. The pattern:

1. `layout.html` defines the `{{define "layout"}}` block with nav, head, and a `{{template "content" .}}` slot
2. Each page template defines `{{define "content"}}` with page-specific HTML
3. htmx attributes on elements trigger partial updates by fetching partials from `/api/*` endpoints
4. Partials in `templates/partials/` return HTML fragments (no layout wrapper) for htmx swaps

Example htmx interaction (Phase 2+):
```html
<!-- In builds.html -->
<button hx-post="/api/builds"
        hx-vals='{"backend":"rocm","ref":"latest"}'
        hx-target="#build-log"
        hx-swap="innerHTML">
    Start Build
</button>
<div id="build-log"></div>
```

---

## What You Can Do at End of Phase

- Run `make dev` → Go server starts on `:3000`
- Browser shows the UI shell with sidebar navigation
- All nav links work (Dashboard, Builds, Models, Browse HF, Service, Settings)
- Pages show placeholder content
- Static assets (Pico CSS, htmx) load correctly
- Config file loads from YAML with defaults
- Graceful shutdown on SIGINT/SIGTERM
- `docker build` produces a working image (no GPU features yet)
