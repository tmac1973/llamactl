# LlamaCtl — Implementation Plan

> Web-based llama.cpp inference manager for containerized GPU deployments

---

## Overview

**LlamaCtl** is a self-hosted web application for managing llama.cpp inference on Linux servers with AMD GPUs. It provides a browser-based UI for downloading models from HuggingFace, compiling llama.cpp against ROCm, configuring multi-GPU tensor splits, and controlling a `llama-server` subprocess — all without touching a terminal.

**Target environment:** Containerized (ROCm dev image), dual AMD Radeon AI PRO 9700 (64GB total VRAM).

---

## Architecture

```
┌──────────────────────────────────────────────────────┐
│              Container (ROCm dev image)               │
│                                                       │
│  ┌─────────────────────────────────────────────────┐  │
│  │            llamactl (Go binary)                 │  │
│  │  ┌─────────────────┐  ┌──────────────────────┐  │  │
│  │  │  Go templates   │  │     REST API         │  │  │
│  │  │  + htmx + Pico  │  │  /api/models         │  │  │
│  │  │  (embedded)     │  │  /api/build          │  │  │
│  │  └─────────────────┘  │  /api/service        │  │  │
│  │                       │  /api/hf             │  │  │
│  │                       │  /v1/* (proxy)       │  │  │
│  │                       └──────────┬───────────┘  │  │
│  └──────────────────────────────────┼──────────────┘  │
│                                     │                  │
│              os/exec subprocess     │                  │
│                                     ▼                  │
│  ┌──────────────────────────────────────────────────┐  │
│  │          llama-server (subprocess)               │  │
│  │   llama-server --model /data/models/...          │  │
│  │     --n-gpu-layers 999                           │  │
│  │     --tensor-split 0.5,0.5                       │  │
│  │     --host 0.0.0.0 --port 8080                   │  │
│  └──────────────────────────────────────────────────┘  │
│                                                       │
│  /data/                 ← single mounted volume       │
│    builds/     ← compiled llama.cpp binaries          │
│    models/     ← downloaded GGUF files                │
│    llama.cpp/  ← cloned source repo                   │
│    config/     ← llamactl.yaml + state                │
└──────────────────────────────────────────────────────┘
```

---

## Technology Stack

| Layer | Choice | Rationale |
|-------|--------|-----------|
| Backend | Go 1.22+ | Single static binary, excellent subprocess control |
| Frontend | Go `html/template` + htmx | No JS build step, server-rendered, progressive enhancement |
| CSS | Pico CSS | Classless semantic styling, minimal footprint |
| HTTP router | `chi` | Lightweight, idiomatic Go, middleware support |
| Templates | `//go:embed` | Templates + static assets embedded in binary |
| Process control | `os/exec` | Direct subprocess spawn/stop/restart, no systemd dependency |
| HF downloads | Go `net/http` + SSE | Native streaming, progress events to UI |
| Build process | `os/exec` → cmake + ninja | Subprocess with log streaming over SSE |
| Container | ROCm dev image | Includes SDK + build tools (builds happen inside container) |
| Chat UI | llama-server built-in | Ships its own SvelteKit WebUI at port 8080 — no need to build one |

---

## Project Structure

```
llamactl/
├── cmd/
│   └── llamactl/
│       └── main.go                # Entry point, flag parsing, startup
├── internal/
│   ├── api/
│   │   ├── server.go              # Server struct, chi router setup
│   │   ├── middleware.go          # Logging, recovery, API key auth
│   │   ├── models.go             # Model CRUD + activation handlers
│   │   ├── build.go              # llama.cpp build handlers
│   │   ├── service.go            # Subprocess control handlers
│   │   ├── hf.go                 # HuggingFace proxy/download handlers
│   │   ├── proxy.go              # OpenAI-compat passthrough (/v1/*)
│   │   ├── settings.go          # Settings + connection test handlers
│   │   └── sse.go                # Shared SSE streaming utility
│   ├── builder/
│   │   ├── builder.go            # CMake build orchestration
│   │   ├── profiles.go           # ROCm / Vulkan / CPU flag sets
│   │   └── detect.go             # GPU backend auto-detection
│   ├── config/
│   │   └── config.go             # YAML config loading + defaults
│   ├── huggingface/
│   │   ├── client.go             # HF API client (search, model info)
│   │   └── downloader.go         # Resumable GGUF download with progress
│   ├── models/
│   │   ├── registry.go           # Local model registry (JSON manifest)
│   │   └── vram.go               # VRAM estimation by quant + param count
│   └── process/
│       └── manager.go            # Subprocess lifecycle (spawn/stop/restart/logs)
├── web/
│   ├── templates/
│   │   ├── layout.html           # Base layout (nav, Pico CSS, htmx)
│   │   ├── index.html            # Dashboard / home page
│   │   ├── models.html           # Local models list
│   │   ├── models_browse.html    # HuggingFace model browser
│   │   ├── builds.html           # Build management page
│   │   ├── service.html          # Service control page
│   │   ├── settings.html         # Settings + proxy config
│   │   └── partials/
│   │       ├── model_card.html       # Single model row/card
│   │       ├── model_config.html     # Model config form
│   │       ├── build_card.html       # Single build row/card
│   │       ├── build_log.html        # Build log stream container
│   │       ├── hf_results.html       # HF search results list
│   │       ├── hf_files.html         # HF model file listing
│   │       ├── download_progress.html # Download progress bar
│   │       ├── service_status.html   # Service status badge + controls
│   │       └── service_logs.html     # Live log tail container
│   ├── embed.go                 # //go:embed declarations for templates + static
│   └── static/
│       ├── htmx.min.js           # htmx library
│       ├── htmx-sse.js           # htmx SSE extension
│       └── pico.min.css          # Pico CSS
├── Dockerfile
├── docker-compose.yml
├── Makefile
└── go.mod
```

---

## Data Directory Layout

```
/data/                              ← container volume mount
├── config/
│   └── llamactl.yaml              # All configuration
├── llama.cpp/                     # Cloned source repository
├── builds/
│   ├── rocm-abc1234/
│   │   └── llama-server
│   └── vulkan-def5678/
│       └── llama-server
└── models/
    └── <model-id>/
        ├── model.gguf
        └── meta.json
```

---

## API Reference

| Method | Path | Description | Phase |
|--------|------|-------------|-------|
| GET | `/` | Dashboard page | 1 |
| GET | `/models` | Local models page | 1 |
| GET | `/models/browse` | HuggingFace browser page | 1 |
| GET | `/builds` | Build management page | 1 |
| GET | `/service` | Service control page | 1 |
| GET | `/settings` | Settings page | 1 |
| GET | `/api/builds` | List compiled builds | 2 |
| POST | `/api/builds` | Trigger new build | 2 |
| GET | `/api/builds/{id}/logs` | SSE stream of build output | 2 |
| DELETE | `/api/builds/{id}` | Remove a build | 2 |
| GET | `/api/builds/backends` | Detected GPU backends | 2 |
| GET | `/api/models` | List local models | 3 |
| GET | `/api/models/{id}` | Get model detail | 3 |
| DELETE | `/api/models/{id}` | Delete model files | 3 |
| PUT | `/api/models/{id}/activate` | Activate model (restart service) | 4 |
| GET | `/api/models/{id}/config` | Get model launch config | 4 |
| PUT | `/api/models/{id}/config` | Update model launch config | 4 |
| GET | `/api/hf/search` | Search HuggingFace `?q=` | 3 |
| GET | `/api/hf/model` | Model file list `?id=owner/name` | 3 |
| POST | `/api/hf/download` | Start download | 3 |
| GET | `/api/hf/download/{id}/progress` | SSE download progress | 3 |
| DELETE | `/api/hf/download/{id}` | Cancel active download | 3 |
| GET | `/api/service/status` | Subprocess status | 4 |
| POST | `/api/service/start` | Start llama-server | 4 |
| POST | `/api/service/stop` | Stop llama-server | 4 |
| POST | `/api/service/restart` | Restart llama-server | 4 |
| GET | `/api/service/logs` | SSE stream of subprocess output | 4 |
| GET | `/api/service/health` | Health check (hits llama-server /health) | 4 |
| ANY | `/v1/*` | Proxy to llama-server OpenAI API | 5 |
| GET | `/api/settings` | Get current settings | 5 |
| PUT | `/api/settings` | Update settings | 5 |
| POST | `/api/settings/test-connection` | Test llama-server connectivity | 5 |

---

## Phases

| Phase | Focus | Deliverable | Detail |
|-------|-------|-------------|--------|
| 1 | Foundation | Go scaffold, chi router, htmx+Pico UI shell, config, Dockerfile | [phase-01-foundation.md](phase-01-foundation.md) |
| 2 | Build Manager | GPU detection, llama.cpp clone/build, SSE log streaming | [phase-02-build-manager.md](phase-02-build-manager.md) |
| 3 | Model Manager | HuggingFace API, resumable downloader, VRAM estimator, local registry | [phase-03-model-manager.md](phase-03-model-manager.md) |
| 4 | Service Control | Subprocess lifecycle, model config, live logs, health | [phase-04-service-control.md](phase-04-service-control.md) |
| 5 | Proxy & Settings | OpenAI /v1/* proxy, API key auth, connection test, settings page | [phase-05-proxy-integration.md](phase-05-proxy-integration.md) |
| 6 | Container & Deploy | Multi-stage Dockerfile, docker-compose, GPU passthrough, docs | [phase-06-container-deployment.md](phase-06-container-deployment.md) |

---

## Key Design Decisions

### No JS build step
Go `html/template` + htmx replaces SvelteKit. Templates are embedded in the binary via `//go:embed`. Pico CSS provides classless semantic styling. htmx handles dynamic updates (SSE streams, form submissions, partial page swaps) without writing JavaScript.

### Direct subprocess management
`os/exec` replaces systemd/D-Bus. The `process.Manager` spawns `llama-server` as a child process, captures stdout/stderr for log streaming, and handles stop/restart via process signals. This works identically on host or in a container with no init system dependency.

### Container-first
The application runs inside a ROCm dev image that includes the full SDK and build tools. llama.cpp compilation happens inside the container. A single `/data` volume persists builds, models, and configuration. GPU passthrough via `--device /dev/kfd --device /dev/dri`.

### No chat UI
llama-server ships its own SvelteKit-based chat WebUI on port 8080. LlamaCtl manages the server; users access chat directly at `http://host:8080` or via any OpenAI-compatible client pointed at LlamaCtl's `/v1/*` proxy.

---

## Non-Goals (v1)

- Multi-instance (multiple llama-server processes simultaneously)
- Model conversion (GGUF generation from base weights)
- Chat UI (llama-server has its own; use Open WebUI for more features)
- Fine-tuning, quantization, or dataset management
- Windows / macOS support

---

## Future (v2 Ideas)

- `rocm-smi` / `radeontop` live GPU stats dashboard (VRAM usage, GPU %, temp)
- Multiple named configurations per model (e.g. "fast" vs "quality")
- Scheduled model preloading
- Prometheus metrics endpoint
- Support for `whisper.cpp` and `stable-diffusion.cpp`
- Multi-node cluster management
