# LlamaCtl

A web-based management interface for [llama.cpp](https://github.com/ggerganov/llama.cpp) inference servers. Build llama.cpp from source, download models from HuggingFace, configure and run inference, and expose an OpenAI-compatible API — all from a single containerized application.

**Linux only.** Supports NVIDIA CUDA, AMD ROCm, and CPU backends. Works with Docker and Podman on all major Linux distributions. GPU passthrough to containers is not available on macOS or Windows.

## Features

- **Build Management** — Clone and compile llama.cpp inside the container with CUDA, ROCm, or CPU backends. View real-time build logs via SSE streaming.
- **Model Management** — Download GGUF models directly from HuggingFace. Search repos, browse available quantizations, and track download progress. Configure per-model inference parameters (GPU layers, context size, threads, tensor split).
- **Service Control** — Start, stop, and restart the llama-server process. Live health monitoring and streaming server logs.
- **OpenAI-Compatible Proxy** — Reverse proxy at `/v1` forwards to llama-server's OpenAI API. Works with any client that supports the OpenAI chat completions format (Goose, Continue, Open WebUI, etc.). Optional Bearer token authentication.
- **Dashboard** — At-a-glance view of service status, active model, build/model inventory, and API endpoint URL.
- **Built-in Chat UI** — llama.cpp's native chat interface is accessible on port 8080 when the server is running.

## Quick Start

```bash
git clone https://github.com/tmlabonte/llamactl.git
cd llamactl
./setup.sh install
```

The setup script will:
1. Detect your GPU (NVIDIA, AMD, or CPU-only)
2. Detect your container runtime (Docker or Podman)
3. Install any missing prerequisites (e.g., NVIDIA Container Toolkit)
4. Show a summary and ask for confirmation
5. Build and start the container

The management UI will be available at `http://localhost:3000`.

### Supported GPUs

| GPU | Backend | Build Profiles | Notes |
|-----|---------|---------------|-------|
| NVIDIA (Maxwell+) | CUDA 12.8 | cuda, cpu | GTX 900 series and newer. Requires driver >= 570. |
| AMD | ROCm 7.2 | rocm, cpu | RDNA and newer. |
| None | CPU-only | cpu | No GPU required. |

**NVIDIA generation support (CUDA 12.8):**

| Generation | Example Cards | Supported |
|------------|--------------|-----------|
| Blackwell (50xx) | RTX 5080, 5090 | Yes |
| Ada Lovelace (40xx) | RTX 4060–4090 | Yes |
| Ampere (30xx) | RTX 3060–3090 | Yes |
| Turing (20xx) | RTX 2060–2080 | Yes |
| Pascal (10xx) | GTX 1060–1080 Ti | Yes (driver >= 570) |
| Maxwell (900) | GTX 970–980 | Yes (driver >= 570) |
| Kepler and older | GTX 700 and below | No (dropped in CUDA 12) |

**Backend performance:** CUDA and ROCm provide native GPU compute for best performance. Each container image supports multiple build profiles — an NVIDIA user can build with CUDA or CPU from the same container.

### Supported Distros

| Distro Family | Package Manager | Tested |
|---------------|-----------------|--------|
| Debian / Ubuntu | apt | Yes |
| Fedora / RHEL | dnf | Yes |
| Arch / CachyOS | pacman | Yes |
| openSUSE | zypper | Planned |

Both Docker and Podman (including rootless) are supported on all distros.

### Setup Script Reference

```
./setup.sh <command>

Lifecycle:
  install     Detect environment, install prerequisites, build & start
  uninstall   Stop container, disable auto-start, remove container + image
  rebuild     Full rebuild with no cache, then start

Runtime:
  up          Start a stopped container
  down        Stop the container
  logs        Follow container logs

Auto-start:
  enable      Start llamactl on boot
  disable     Stop starting on boot

Info:
  status      Show detected environment and planned actions
  detect      Print detected GPU backend (cuda/rocm/cpu)
  help        Show full help with details
```

Override detection with environment variables:

```bash
GPU=cpu ./setup.sh install          # force CPU-only backend
RUNTIME=podman ./setup.sh install   # force Podman runtime
```

### First Run

1. Open `http://localhost:3000`
2. Go to **Builds** and compile llama.cpp (select the appropriate backend for your GPU)
3. Go to **Browse** to search HuggingFace and download a GGUF model
4. Go to **Models**, click **Configure** on your model to set GPU layers and context size, then **Activate**
5. The service will start and the OpenAI API becomes available at `http://localhost:3000/v1`

## Configuration

LlamaCtl uses a YAML config file at `/data/config/llamactl.yaml` inside the container. Settings can also be changed from the Settings page in the UI.

```yaml
listen_addr: ":3000"       # Management UI listen address
data_dir: "/data"           # Base directory for builds, models, config
llama_port: 8080            # Port for llama-server inference
external_url: ""            # Public URL (e.g. "http://myserver:3000") for link generation
hf_token: ""                # HuggingFace token for gated model downloads
api_key: ""                 # Bearer token for /v1 proxy authentication
log_level: "info"
```

### External URL

Set `external_url` when accessing LlamaCtl from a remote machine. This configures the displayed API endpoint URL and Chat UI link on the dashboard. Can be set from the Settings page.

### API Key Authentication

When `api_key` is set, all requests to `/v1/*` require a `Authorization: Bearer <key>` header. This secures the inference endpoint without affecting the management UI.

## Ports

| Port | Service |
|------|---------|
| 3000 | LlamaCtl management UI + OpenAI proxy (`/v1`) |
| 8080 | llama-server inference + built-in chat UI |

## GPU Backend Notes

### ROCm

The setup script auto-detects the AMD GPU architecture and sets `HSA_OVERRIDE_GFX_VERSION` in a `.env` file when needed. This override is only required for older GPUs not natively supported by ROCm 7.2 (e.g., RDNA 1 maps to `10.1.0`, Vega maps to `9.0.0`). Natively supported architectures (RDNA 2, RDNA 3, RDNA 4) don't need the override.

### CUDA

CUDA 12.8 requires an NVIDIA driver >= 570. The llama.cpp CUDA build auto-detects the GPU architecture at compile time — no manual target configuration is needed (unlike ROCm's `AMDGPU_TARGETS`).

## Architecture

LlamaCtl is a single Go binary that serves a web UI and manages the llama-server subprocess.

```
cmd/llamactl/          Entry point
internal/
  api/                 HTTP handlers, SSE streaming, reverse proxy
  builder/             llama.cpp build pipeline (git clone, cmake, ninja)
  config/              YAML configuration
  huggingface/         HF API client and model downloader
  models/              Local model registry and VRAM estimation
  process/             llama-server process lifecycle manager
web/
  static/              htmx, Pico CSS
  templates/           Go html/template pages and partials
```

The UI uses server-rendered HTML with [htmx](https://htmx.org/) for interactivity and [Pico CSS](https://picocss.com/) for styling. No JavaScript build step required.

### Container Files

| File | Purpose |
|------|---------|
| `Dockerfile.cuda` | NVIDIA CUDA 12.8 runtime |
| `Dockerfile.rocm` | AMD ROCm 7.2 runtime |
| `Dockerfile.cpu` | CPU-only (lightweight Debian) |
| `docker-compose.cuda.yml` | Compose for NVIDIA (works with Docker and Podman) |
| `docker-compose.rocm.yml` | Compose for AMD |
| `docker-compose.cpu.yml` | Compose for CPU-only |
| `setup.sh` | Auto-detect and setup script |

## Development

### Local (without container)

```bash
make dev          # go run with hot reload
make build        # compile to bin/llamactl
make run          # build and run
```

### API Smoke Test

```bash
./scripts/test-api.sh http://localhost:3000
```

Tests all management API endpoints and optionally runs a chat completion if the server is running.

## API Endpoints

### Management API

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/dashboard` | Dashboard HTML fragment |
| GET | `/api/builds/` | List builds |
| POST | `/api/builds/` | Trigger new build |
| GET | `/api/builds/backends` | List available backends |
| GET | `/api/builds/{id}/logs` | Stream build logs (SSE) |
| DELETE | `/api/builds/{id}` | Delete a build |
| GET | `/api/models/` | List models |
| GET | `/api/models/{id}` | Get model details |
| DELETE | `/api/models/{id}` | Delete a model |
| PUT | `/api/models/{id}/activate` | Activate model (start serving) |
| GET | `/api/models/{id}/config` | Get model config |
| PUT | `/api/models/{id}/config` | Update model config |
| GET | `/api/hf/search` | Search HuggingFace |
| GET | `/api/hf/model` | Get HF model details |
| POST | `/api/hf/download` | Start model download |
| GET | `/api/hf/download/{id}/progress` | Download progress (SSE) |
| DELETE | `/api/hf/download/{id}` | Cancel download |
| GET | `/api/service/status` | Service status |
| POST | `/api/service/start` | Start llama-server |
| POST | `/api/service/stop` | Stop llama-server |
| POST | `/api/service/restart` | Restart llama-server |
| GET | `/api/service/logs` | Stream server logs (SSE) |
| GET | `/api/service/health` | Health check |
| GET | `/api/settings/` | Get settings |
| PUT | `/api/settings/` | Update settings |

### OpenAI-Compatible Proxy

All requests to `/v1/*` are forwarded to the running llama-server. Supports streaming chat completions via SSE.

```bash
curl http://localhost:3000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "any",
    "messages": [{"role": "user", "content": "Hello!"}],
    "max_tokens": 64
  }'
```

## License

[GNU Affero General Public License v3.0](LICENSE)
