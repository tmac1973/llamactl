# LlamaCtl

A web-based management interface for [llama.cpp](https://github.com/ggerganov/llama.cpp) inference servers. Build llama.cpp from source, download models from HuggingFace, configure and run inference, and expose an OpenAI-compatible API — all from a single containerized application.

Designed for self-hosted GPU servers running AMD ROCm, with support for CPU and Vulkan backends.

## Features

- **Build Management** — Clone and compile llama.cpp inside the container with ROCm, Vulkan, or CPU backends. View real-time build logs via SSE streaming.
- **Model Management** — Download GGUF models directly from HuggingFace. Search repos, browse available quantizations, and track download progress. Configure per-model inference parameters (GPU layers, context size, threads, tensor split).
- **Service Control** — Start, stop, and restart the llama-server process. Live health monitoring and streaming server logs.
- **OpenAI-Compatible Proxy** — Reverse proxy at `/v1` forwards to llama-server's OpenAI API. Works with any client that supports the OpenAI chat completions format (Goose, Continue, Open WebUI, etc.). Optional Bearer token authentication.
- **Dashboard** — At-a-glance view of service status, active model, build/model inventory, and API endpoint URL.
- **Built-in Chat UI** — llama.cpp's native chat interface is accessible on port 8080 when the server is running.

## Quick Start

### Requirements

- Docker or Podman with Compose
- AMD GPU with ROCm support (for GPU inference)

### Deploy

```bash
git clone https://github.com/tmlabonte/llamactl.git
cd llamactl
docker compose up -d
```

The management UI will be available at `http://localhost:3000`.

### SELinux (Fedora/RHEL)

If running with SELinux enforcing, allow container GPU access:

```bash
sudo setsebool -P container_use_devices 1
```

### First Run

1. Open `http://localhost:3000`
2. Go to **Builds** and compile llama.cpp (select ROCm backend for GPU)
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

## Docker Compose

The default `docker-compose.yml` exposes two ports:

| Port | Service |
|------|---------|
| 3000 | LlamaCtl management UI + OpenAI proxy (`/v1`) |
| 8080 | llama-server inference + built-in chat UI |

GPU access is configured for AMD ROCm. The `HSA_OVERRIDE_GFX_VERSION` environment variable may need adjustment for your GPU architecture.

### Vulkan Support

To enable Vulkan backend builds, uncomment the host Vulkan mounts in `docker-compose.yml`:

```yaml
volumes:
  - /etc/vulkan:/etc/vulkan:ro
  - /usr/share/vulkan:/usr/share/vulkan:ro
```

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

## Development

### Local (without container)

```bash
make dev          # go run with hot reload
make build        # compile to bin/llamactl
make run          # build and run
```

### Container

```bash
make docker-compose-up      # start container
make docker-compose-down    # stop container
make docker-compose-logs    # tail logs
make docker-rebuild         # full rebuild (no cache)
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

MIT
