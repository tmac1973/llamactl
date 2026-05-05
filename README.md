# Llama Toolchest

Click this screenshot to watch the explainer video:

[![Watch the video](https://img.youtube.com/vi/N4Xe6OL1Og4/maxresdefault.jpg)](https://youtu.be/N4Xe6OL1Og4)

A web-based management interface for [llama.cpp](https://github.com/ggerganov/llama.cpp) inference servers. Build llama.cpp from source, download models from HuggingFace, configure and run inference, and expose an OpenAI-compatible API — all from a single containerized application.

**Linux only.** Supports NVIDIA CUDA, AMD ROCm, Vulkan (host install only), and CPU backends. Works with Docker and Podman on all major Linux distributions.

## Features

- **Build management** — Compile llama.cpp with CUDA / ROCm / Vulkan / CPU backends. Toggleable build options, real-time SSE log streaming.
- **Model management** — Download GGUF models from HuggingFace, scan existing files, configure per-model parameters.
- **Multi-model loading** — Run multiple models simultaneously via llama.cpp's router. Per-model isolated subprocess, LRU eviction at the VRAM limit.
- **Per-model config** — Context size, KV cache quant, GPU layers, tensor split, flash attention, sampling, aliases, and speculative-decoding draft model.
- **Vision / multimodal** — Auto-detect and pair `mmproj` files. Send images via the OpenAI chat API (requires OpenSSL build).
- **Embedding models** — Curated one-click downloads (nomic-embed, bge, mxbai-embed, snowflake-arctic-embed) with automatic `--embeddings` injection.
- **Speculative decoding** — Pair a small draft model with a large model; draft picker auto-filters by architecture.
- **Capability detection** — Tool calling and vision detection from GGUF metadata, surfaced as badges and via `/api/models/{id}/info`.
- **VRAM estimation** — Architecture-aware estimates from GGUF metadata, accounting for KV cache size and quantization.
- **Benchmarks** — Run llama-bench presets per model, compare runs, and export results.
- **OpenAI-compatible API** — Chat completions (streaming, tool calling, JSON schema), completions, embeddings, model listing. Optional Bearer auth.
- **Built-in chat UI** — llama.cpp's native chat interface with a model-selector dropdown.
- **Agent CLI** — Lightweight terminal chat client (`cmd/agent`) with optional filesystem tool use.

## Installation

### Quick start

```bash
git clone https://github.com/tmac1973/llama-toolchest.git
cd llama-toolchest
./setup.sh install              # default: container install
# or, for a containerless install on the host:
./setup.sh install --host
```

The setup script detects your GPU and container runtime, installs missing prerequisites, shows a summary, then builds and starts. The management UI is at `http://localhost:3000`.

### Install modes

| Mode | When to use | What it does |
|------|-------------|--------------|
| `--container` (default) | Most users — keeps GPU SDKs and the build toolchain isolated from the host. | Builds a Docker/Podman image and runs llama-toolchest inside it. The image installs the released `.deb`/`.rpm`, so the binary inside is byte-identical to a host install. |
| `--host` (default: `--from-package`) | You already have a working GPU driver and want a leaner setup, faster startup, or Vulkan support. | Downloads the latest released `.deb`/`.rpm` for your distro from the GitHub release, verifies its checksum, and installs via `dnf`/`apt`. Writes a config, registers a systemd user unit, and (optionally) enables the service. |
| `--host --from-source` | You're testing uncommitted changes from the source tree. | Builds the binary via `go build` and drops it in `~/.local/bin/llama-toolchest`. Otherwise the same flow. |

Host mode is managed via `systemctl --user start|stop|status llama-toolchest` (user install) or `sudo systemctl ...` (system install). Container `up`/`down`/`logs`/`enable`/`disable` are container-mode-only.

### Backend SDK selection (host mode)

By default `--host` auto-detects your primary GPU and asks whether to also install the Vulkan SDK as a portable fallback. To pick explicitly — including stacking multiple SDKs in one install — pass any combination of `--cuda`, `--rocm`, `--vulkan`. Each implies `--host`.

```bash
./setup.sh install --rocm --vulkan    # AMD GPU + Vulkan as a fallback
./setup.sh install --vulkan           # Vulkan-only (cross-vendor)
./setup.sh install --cuda             # NVIDIA only, skip the Vulkan prompt
```

For multi-GPU SDK installs, prefer the additive flags over `GPU=`.

### Switching modes

`./setup.sh migrate` moves your model registry, per-model configs, benchmarks, and main settings between sides:

```bash
./setup.sh migrate --to-host         # container → host
./setup.sh migrate --to-container    # host → container
```

Migration refuses to run if the destination side is already populated — uninstall the unwanted side first (`./setup.sh uninstall` or `./setup.sh uninstall --host`). The source's snapshot is kept at `~/llt-migrate-<timestamp>` as a safety net.

`builds.json` is wiped during migration: container-built `llama-server` binaries don't run on the host (different glibc/CUDA/ROCm runtime) and vice versa. Open the **Builds** page after migration and rebuild.

### Setup script reference

```
./setup.sh <command>

Lifecycle:
  install     Detect environment, install prerequisites, build & start
  uninstall   Stop, disable auto-start, remove container + image (or host package)
  migrate     Move state between container and host installs
              (--to-host or --to-container required)
  quick       Container only: pull the latest released package and reinstall
              it inside the existing image (reuses cached GPU SDK / base layers)
  rebuild     Container only: full rebuild with no cache, then start

Runtime (container only):
  up / down / logs           Start, stop, follow logs
  enable / disable           Auto-start on boot

Info:
  status      Show detected environment and planned actions
  deps        Verify prerequisites and print install commands for anything missing
  detect      Print detected GPU backend (cuda/rocm/vulkan/cpu)
  help        Show full help
```

Override detection: `GPU=cpu ./setup.sh install`, `RUNTIME=podman ./setup.sh install`.

### Manual install

If you'd rather skip `setup.sh` and install the released `.deb`/`.rpm` packages by hand, see [docs/manual-install.md](docs/manual-install.md).

### Supported GPUs

| GPU | Backend | Build profiles | Notes |
|-----|---------|----------------|-------|
| NVIDIA (Maxwell+) | CUDA 12.8 | cuda, cpu, vulkan† | GTX 900 series and newer. Driver >= 570. |
| AMD | ROCm 7.2 | rocm, cpu, vulkan† | RDNA and newer. |
| Other (Intel Arc, etc.) | Vulkan† | vulkan, cpu | Cross-vendor; install with `./setup.sh install --vulkan`. |
| None | CPU-only | cpu | No GPU required. |

† Vulkan is host-install only — see [GPU Backend Notes → Vulkan](#vulkan).

CUDA and ROCm provide native GPU compute for best performance; Vulkan is portable but typically slower than the vendor-specific backend on the same hardware.

### Supported distros

| Distro family | Package manager | Tested |
|---------------|-----------------|--------|
| Debian / Ubuntu | apt | Yes |
| Fedora / RHEL | dnf | Yes |
| Arch / CachyOS | pacman | Yes |
| openSUSE | zypper | Planned |

Both Docker and Podman (including rootless) are supported.

### First run

1. Open `http://localhost:3000`
2. Go to **Builds** and compile llama.cpp for your backend
3. Go to **Browse** to download a GGUF model from HuggingFace
4. Go to **Models**, click **Configure** to set GPU layers / context / sampling, then enable the model and restart
5. The OpenAI API is at `http://localhost:3000/v1`; the proxy routes by the `model` field

## Configuration

The YAML config lives at `/data/config/llama-toolchest.yaml` (container) or `~/.config/llama-toolchest/llama-toolchest.yaml` (host user install). Most settings are also editable from the Settings page.

```yaml
listen_addr: ":3000"        # Management UI listen address
data_dir: "/data"           # Base directory for builds, models, config
models_dir: ""              # Optional override; empty → <data_dir>/models
llama_port: 8080            # Port for llama-server inference
external_url: ""            # Public URL for link generation (e.g. http://myserver:3000)
hf_token: ""                # HuggingFace token for gated downloads
api_key: ""                 # Bearer token for /v1 proxy (empty = no auth)
log_level: "info"
active_build: ""            # Active llama.cpp build ID
models_max: 1               # Max simultaneously loaded models (0 = unlimited)
auto_start: false           # Start the inference router on container startup
```

To persist models on the host filesystem (so they survive `docker volume rm`), set `LLAMA_TOOLCHEST_MODELS_DIR=/path/to/models` in `.env` before running `setup.sh`. Existing models in the volume won't be visible after switching — move them first.

## Ports

| Port | Service |
|------|---------|
| 3000 | Management UI + OpenAI proxy (`/v1`) |
| 8080 | llama.cpp router + built-in chat UI |

## GPU Backend Notes

### ROCm

`setup.sh` auto-detects the AMD GPU architecture and sets `HSA_OVERRIDE_GFX_VERSION` in `.env` when needed. Only required for older GPUs not natively supported by ROCm 7.2 (RDNA 1 → `10.1.0`, Vega → `9.0.0`).

### CUDA

CUDA 12.8 requires NVIDIA driver >= 570. The CUDA build auto-detects GPU architecture at compile time — no manual target configuration needed.

### Vulkan

Vulkan is **host-install only** — container mode would need GPU driver / ICD passthrough that this project doesn't manage. Use it as a portable fallback alongside CUDA or ROCm, or as the sole backend on hardware where the vendor SDK isn't a fit.

`./setup.sh install --vulkan` (or `--rocm --vulkan`) installs:

| Distro | Packages |
|--------|----------|
| Debian / Ubuntu | `glslc libvulkan-dev spirv-headers vulkan-tools` |
| Fedora / RHEL | `glslc vulkan-headers vulkan-loader-devel spirv-headers-devel vulkan-tools` |

`vulkan-tools` provides `vulkaninfo`, which the backend probe uses to enumerate hardware Vulkan devices. The runtime loader (`libvulkan1` / `vulkan-loader`) is typically already installed by your GPU driver.

### Multi-GPU

Two modes:

- **Layer parallelism (default)** — Layers are split sequentially across GPUs. Best for most cases.
- **Tensor parallelism (experimental)** — Tensors are split across all GPUs simultaneously. More memory-efficient but needs fast interconnect (NVLink / PCIe 4.0+).

GPU selection: pick "All GPUs" (tensor mode), specific GPUs ("GPU 0", "GPUs 0–1"), or a custom tensor-split string like `1,1,0,0`.

## Architecture

A single Go binary serves the web UI and manages llama-server subprocesses. Server-rendered HTML with [htmx](https://htmx.org/) and [Pico CSS](https://picocss.com/) — no JS build step.

```
cmd/
  llama-toolchest/   Server entry point
  agent/             Terminal chat client with tool use
internal/
  api/               HTTP handlers, SSE streaming, /v1 proxy
  benchmark/         llama-bench runs, presets, comparison
  builder/           llama.cpp build pipeline (git, cmake, ninja)
  config/            YAML configuration
  huggingface/       HF API client and model downloader
  models/            Registry, GGUF parser, VRAM estimation, preset INI
  monitor/           GPU/CPU/memory metrics (ROCm + NVIDIA)
  process/           llama-server router lifecycle
web/                 Templates + static assets (htmx, Pico CSS)
scripts/             API smoke tests (test-api, test-embeddings, test-tools, etc.)
```

Container files: `Dockerfile.{cuda,rocm,cpu}`, `docker-compose.{cuda,rocm,cpu}.yml`, plus `setup.sh`.

## Development

```bash
make dev          # go run with hot reload
make build        # compile bin/llama-toolchest + bin/agent
make run          # build and run
make agent        # compile just the agent CLI
```

### Agent CLI

```bash
agent                                 # connect to localhost:3000
agent -host 192.168.1.50              # remote server
agent -port 8080                      # custom port
agent -url http://gpu-box:3000/v1     # full endpoint URL
agent -model qwen3-32b                # target a specific model
agent -system "You are..."            # set a system prompt
agent -no-tools                       # plain chat (no filesystem tools)
agent -work-dir /path/to/project      # working dir for tools
agent -api-key sk-xxx                 # authenticate
```

### Test scripts

All scripts include an interactive model picker; pass a model name to skip selection.

```bash
./scripts/test-api.sh           # API smoke test
./scripts/test-embeddings.sh    # embedding dimensions + cosine similarity
./scripts/test-info.sh          # /api/models/{id}/info + /api/ps
./scripts/test-structured.sh    # JSON schema + json_object output
./scripts/test-tools.sh         # tool / function calling
./scripts/test-vision.sh        # vision via URL or local image
```

## API

### OpenAI-compatible (`/v1/*`)

All requests are forwarded to llama.cpp's router. Per-model sampling defaults are injected for requests that don't specify them; user-defined model aliases work in the `model` field.

- `GET /v1/models` — list available models
- `GET /v1/models/{model}` — single model info
- `POST /v1/chat/completions` — streaming, tool calling, JSON schema / `json_object`
- `POST /v1/completions` — text completion
- `POST /v1/embeddings` — vector embeddings (auto-configured for `--embeddings` builds)

```bash
curl http://localhost:3000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "qwen35", "messages": [{"role": "user", "content": "Hello!"}]}'
```

When `api_key` is set, all `/v1/*` requests require `Authorization: Bearer <key>`. The management UI is unaffected.

### Management API

Routes under `/api/` cover builds, models, HuggingFace search & download, the inference service, settings, monitoring, and benchmarks. SSE endpoints stream build / download / log progress. Browse the routes in [`internal/api/server.go`](internal/api/server.go) — that's the source of truth.

A few useful ones:

- `GET /api/ps` — loaded models with status (Ollama-style)
- `GET /api/models/{id}/info` — enriched metadata with capabilities (tools, vision)
- `GET /api/models/{id}/vram-estimate` — VRAM estimate for a given config
- `GET /api/service/loaded-models` — models available to the router
- `GET /api/monitor/stream` — GPU/CPU/memory metrics over SSE

## License

[GNU Affero General Public License v3.0](LICENSE)
