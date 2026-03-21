# Llama Toolchest

A web-based management interface for [llama.cpp](https://github.com/ggerganov/llama.cpp) inference servers. Build llama.cpp from source, download models from HuggingFace, configure and run inference, and expose an OpenAI-compatible API — all from a single containerized application.

**Linux only.** Supports NVIDIA CUDA, AMD ROCm, and CPU backends. Works with Docker and Podman on all major Linux distributions. GPU passthrough to containers is not available on macOS or Windows.

## Features

- **Build Management** — Clone and compile llama.cpp inside the container with CUDA, ROCm, or CPU backends. Toggleable build options including OpenSSL for HTTPS support. View real-time build logs via SSE streaming.
- **Model Management** — Download GGUF models directly from HuggingFace. Search repos, browse available quantizations, and track download progress. Configure per-model inference parameters. Scan directories for existing GGUF files.
- **Multi-Model Loading** — Run multiple models simultaneously via llama.cpp's native router mode. Each model runs in its own isolated subprocess with per-model configuration. LRU eviction automatically unloads least-used models when VRAM limits are reached.
- **Per-Model Configuration** — Each model can have its own context size, KV cache quantization, GPU layers, tensor split, flash attention, direct I/O, sampling parameters, user-defined aliases, and speculative decoding draft model. Settings are stored in the registry and translated to llama.cpp preset INI format.
- **Vision / Multimodal** — Automatic detection and association of mmproj (multimodal projector) files for vision-capable models. Vision badge shown on model cards. Send images via the OpenAI-compatible chat API using base64 or URL (requires OpenSSL build).
- **Embedding Models** — Separate embedding model section with curated one-click downloads (nomic-embed, bge, mxbai-embed, snowflake-arctic-embed). Simplified config for embedding models. Automatic `--embeddings` flag injection.
- **Speculative Decoding** — Pair a small draft model with a large model for faster inference. Draft model picker auto-filters by architecture and size. Config generates `model-draft` in the preset INI.
- **Model Capabilities** — Automatic detection of tool calling support from GGUF chat template. Vision and tools badges shown on model cards. Model info endpoint exposes capabilities for client discovery.
- **VRAM Estimation** — Architecture-aware VRAM estimation using GGUF metadata (layers, KV heads, embedding dimensions). Estimates account for model weights, KV cache at configured context size, and cache quantization.
- **Service Control** — Start, stop, and restart the inference server. Load and unload individual models. Live server log streaming. Health monitoring. Loaded models list with status indicators on the server page.
- **OpenAI-Compatible API** — Full OpenAI API compatibility at `/v1` including:
  - Chat completions (streaming, tool/function calling, structured output / JSON schema)
  - Completions
  - Embeddings
  - Model listing
  - Per-model sampling defaults injected automatically
  - Optional Bearer token authentication
- **Model Info & Status** — `/api/models/{id}/info` returns enriched metadata with capabilities list. `/api/ps` returns loaded models with status, similar to Ollama's ps endpoint.
- **Model Aliases** — Assign friendly names like `qwen3:latest` or `my-chat-model` to models. Clients can use aliases in the `model` field of API requests.
- **Dashboard** — At-a-glance view of service status, active models, build/model inventory, and API endpoint URL.
- **Agent CLI** — Lightweight terminal chat client (`cmd/agent`) that connects to the API with tool-use support for filesystem exploration.
- **Built-in Chat UI** — llama.cpp's native chat interface at port 8080 with model selector dropdown for switching between loaded models.

## Quick Start

```bash
git clone https://github.com/tmac1973/llama-toolchest.git
cd llama-toolchest
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
  quick       Fast rebuild (recompile Go code only, reuse cached base)
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
4. Go to **Models**, click **Configure** on your model to set GPU layers, context size, and sampling parameters, then enable it and restart the server
5. The model will load on first request and the OpenAI API becomes available at `http://localhost:3000/v1`
6. Optionally start additional models — the proxy routes requests by the `model` field

## Configuration

Llama Toolchest uses a YAML config file at `/data/config/llamactl.yaml` inside the container. Settings can also be changed from the Settings page in the UI.

```yaml
listen_addr: ":3000"       # Management UI listen address
data_dir: "/data"           # Base directory for builds, models, config
llama_port: 8080            # Port for llama-server inference
external_url: ""            # Public URL (e.g. "http://myserver:3000") for link generation
hf_token: ""                # HuggingFace token for gated model downloads
api_key: ""                 # Bearer token for /v1 proxy authentication
log_level: "info"
```

### Model Storage

By default, models are stored in the Docker volume (`llamactl-data`). To persist models on the host filesystem (so they survive volume removal):

```bash
# Add to .env (or set before running setup.sh)
LLAMACTL_MODELS_DIR=/path/to/your/models
```

The host directory is bind-mounted to `/data/models` inside the container. Existing models in the volume will not be visible when a host directory is mounted — move them first if needed.

### External URL

Set `external_url` when accessing LlamaCtl from a remote machine. This configures the displayed API endpoint URL and Chat UI link on the dashboard. Can be set from the Settings page.

### API Key Authentication

When `api_key` is set, all requests to `/v1/*` require a `Authorization: Bearer <key>` header. This secures the inference endpoint without affecting the management UI.

## Ports

| Port | Service |
|------|---------|
| 3000 | LlamaCtl management UI + OpenAI proxy (`/v1`) |
| 8080 | llama.cpp router + built-in chat UI with model dropdown |

## GPU Backend Notes

### ROCm

The setup script auto-detects the AMD GPU architecture and sets `HSA_OVERRIDE_GFX_VERSION` in a `.env` file when needed. This override is only required for older GPUs not natively supported by ROCm 7.2 (e.g., RDNA 1 maps to `10.1.0`, Vega maps to `9.0.0`). Natively supported architectures (RDNA 2, RDNA 3, RDNA 4) don't need the override.

### CUDA

CUDA 12.8 requires an NVIDIA driver >= 570. The llama.cpp CUDA build auto-detects the GPU architecture at compile time — no manual target configuration is needed (unlike ROCm's `AMDGPU_TARGETS`).

## Architecture

LlamaCtl is a single Go binary that serves a web UI and manages the llama-server subprocess.

```
cmd/
  llamactl/            Server entry point
  agent/               Terminal chat client with tool use
internal/
  api/                 HTTP handlers, SSE streaming, routing proxy
  builder/             llama.cpp build pipeline (git clone, cmake, ninja)
  config/              YAML configuration
  huggingface/         HF API client and model downloader
  models/              Model registry, GGUF parser, VRAM estimation, preset INI generator
  monitor/             GPU/CPU/memory metrics collection (ROCm + NVIDIA)
  process/             llama-server router lifecycle manager
web/
  static/              htmx, Pico CSS
  templates/           Go html/template pages and partials
scripts/
  test-api.sh          API smoke test
  test-embeddings.sh   Embedding model test (dimensions + similarity)
  test-info.sh         Model info and PS endpoint test
  test-structured.sh   JSON schema / structured output test
  test-tools.sh        Tool/function calling test
  test-vision.sh       Vision / multimodal test
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
make build        # compile bin/llamactl + bin/agent
make run          # build and run
make agent        # compile just the agent CLI
```

### Agent CLI

The agent is a lightweight terminal chat client that connects to the OpenAI-compatible API:

```bash
agent                                     # connect to localhost:3000
agent -host 192.168.1.50                  # remote server
agent -host gpu-box -port 8080            # custom port
agent -model qwen3-32b                    # target a specific model
agent -no-tools                           # plain chat mode (no filesystem tools)
agent -api-key sk-xxx                     # authenticate
```

### Test Scripts

All test scripts include an interactive model picker. Pass a model name to skip selection.

```bash
./scripts/test-api.sh                     # API smoke test (pages, management, proxy)
./scripts/test-embeddings.sh              # Embedding dimensions + cosine similarity
./scripts/test-info.sh                    # Model info endpoint + PS (loaded models)
./scripts/test-structured.sh              # JSON schema + json_object response format
./scripts/test-tools.sh                   # Tool/function calling + multi-turn
./scripts/test-vision.sh                  # Vision with remote URL or local image
```

## API Endpoints

### Management API

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/dashboard` | Dashboard HTML fragment |
| GET | `/api/ps` | Loaded models with status and resource info |
| GET | `/api/builds/` | List builds |
| POST | `/api/builds/` | Trigger new build |
| GET | `/api/builds/backends` | List available backends |
| GET | `/api/builds/{id}/logs` | Stream build logs (SSE) |
| DELETE | `/api/builds/{id}` | Delete a build |
| GET | `/api/models/` | List models |
| GET | `/api/models/embeddings` | List embedding models |
| GET | `/api/models/embedding-presets` | Curated embedding model presets |
| POST | `/api/models/embedding-presets/download` | Download curated embedding model |
| GET | `/api/models/{id}` | Get model details |
| GET | `/api/models/{id}/info` | Get enriched model metadata with capabilities |
| DELETE | `/api/models/{id}` | Delete a model |
| PUT | `/api/models/{id}/activate` | Load model into VRAM |
| DELETE | `/api/models/{id}/activate` | Unload model from VRAM |
| PUT | `/api/models/{id}/enable` | Enable model in preset (restart required) |
| GET | `/api/models/{id}/config` | Get model config |
| PUT | `/api/models/{id}/config` | Update model config |
| GET | `/api/models/{id}/vram-estimate` | VRAM estimate for given config |
| GET | `/api/hf/search` | Search HuggingFace |
| GET | `/api/hf/model` | Get HF model details |
| POST | `/api/hf/download` | Start model download |
| GET | `/api/hf/download/{id}/progress` | Download progress (SSE) |
| DELETE | `/api/hf/download/{id}` | Cancel download |
| GET | `/api/service/status` | Service status |
| POST | `/api/service/start` | Start llama-server |
| POST | `/api/service/stop` | Stop all model instances |
| POST | `/api/service/restart` | Restart all model instances |
| GET | `/api/service/logs` | Stream server logs (SSE) |
| GET | `/api/service/health` | Health check |
| GET | `/api/service/loaded-models` | Models available to router with status |
| GET | `/api/settings/` | Get settings |
| PUT | `/api/settings/` | Update settings |

### OpenAI-Compatible API

All requests to `/v1/*` are forwarded to the llama.cpp router. Supports:

- **Chat completions** — streaming, tool/function calling, structured output (JSON schema, json_object)
- **Completions** — text generation
- **Embeddings** — vector embeddings (requires embedding model with `--embeddings` flag, auto-configured)
- **Models** — list available models

```bash
# List available models
curl http://localhost:3000/v1/models

# Chat completion
curl http://localhost:3000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen35",
    "messages": [{"role": "user", "content": "Hello!"}],
    "max_tokens": 64
  }'

# Embeddings
curl http://localhost:3000/v1/embeddings \
  -H "Content-Type: application/json" \
  -d '{"model": "nomic-ai--nomic-embed-text-v1.5-GGUF", "input": "Hello world"}'

# Structured output
curl http://localhost:3000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen35",
    "messages": [{"role": "user", "content": "List 3 colors"}],
    "response_format": {"type": "json_schema", "json_schema": {"name": "colors", "schema": {"type": "object", "properties": {"colors": {"type": "array", "items": {"type": "string"}}}}}}
  }'
```

The router auto-loads models on first request. Per-model sampling defaults (configured in the UI) are injected by the proxy into requests that don't specify them. User-defined model aliases (e.g., `qwen35`) work in the `model` field.

## License

[GNU Affero General Public License v3.0](LICENSE)
