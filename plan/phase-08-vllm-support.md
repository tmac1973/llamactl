# Phase 8 — vLLM Inference Engine Support

> Run vLLM as a sidecar container alongside llamactl, with unified management UI

---

## Goal

Add vLLM as an alternative inference engine that coexists with llama.cpp. vLLM runs in a separate sidecar container managed via Docker Compose, while llamactl orchestrates both engines through its existing UI. Users can download models in either GGUF format (for llama.cpp) or HuggingFace safetensors format (for vLLM), create profiles for either engine, and switch between them. Only one inference engine runs at a time. Model storage for each engine can optionally be mapped to external host directories.

**Priority: AMD ROCm (RDNA4) first, NVIDIA CUDA second.** The primary test target is dual AMD Radeon 9700 AI Pro (gfx1201) GPUs with tensor parallelism.

---

## Deliverables

- `Dockerfile.vllm-rocm` — custom vLLM image for AMD RDNA4, adapted from [kyuz0/amd-r9700-vllm-toolboxes](https://github.com/kyuz0/amd-r9700-vllm-toolboxes)
- NVIDIA vLLM support using the official `vllm/vllm-openai` image
- Sidecar container definitions in all GPU compose files
- Generalized profile system: llama.cpp profiles (cmake builds) and vLLM profiles (launch configuration)
- Dual model registries with engine-aware HuggingFace search and download
- Process/container manager extended to start/stop the vLLM sidecar
- UI updates: engine selector on Browse/Models tabs, vLLM profile creation on Builds tab
- Optional external model storage via `.env` mount points
- Updated `setup.sh` to write mount point env vars

---

## Architecture Decisions

### Why a sidecar container instead of installing vLLM in-process?

Alternatives considered:

| Approach | Pros | Cons |
|----------|------|------|
| **Same container** | Single process manager, simple | vLLM ROCm build is extremely complex (TheRock SDK, patched vLLM, Clang ABI fixes); bloats images by 10-15 GB; fragile |
| **DooD (Docker-outside-of-Docker)** | Full control | Requires socket mount, security concerns, Podman compat issues |
| **Sidecar compose service** | Clean separation, leverage existing images, independent updates | Cross-container networking, shared volume coordination |

**The sidecar approach wins** because:

1. **vLLM's ROCm build is too complex to embed.** The [kyuz0/amd-r9700-vllm-toolboxes](https://github.com/kyuz0/amd-r9700-vllm-toolboxes) Dockerfile demonstrates the level of effort required for RDNA4: TheRock nightly SDK, `amdsmi` patches, Clang compiler override to fix ABI segfaults, `tcmalloc` preload for double-free crashes, Flash Attention from source, bitsandbytes from source. Embedding this into our ROCm image would make it unmaintainable.

2. **CUDA has a pre-built official image.** `vllm/vllm-openai:latest` works out of the box — there's no reason to rebuild it ourselves.

3. **Independent update cycles.** vLLM releases frequently with breaking changes. A separate container means users can update the vLLM image without rebuilding the llamactl image (and vice versa).

4. **Compose handles the orchestration natively.** Docker/Podman Compose manages the sidecar lifecycle, networking, and shared volumes — no Docker socket needed.

5. **GPU sharing works.** Both containers can access `/dev/dri` and `/dev/kfd`. Since only one engine runs inference at a time, there's no GPU memory contention.

### Reference: kyuz0 Dockerfile for RDNA4

The [kyuz0/amd-r9700-vllm-toolboxes](https://github.com/kyuz0/amd-r9700-vllm-toolboxes) project provides a working vLLM container for the Radeon 9700 AI Pro (gfx1201). Key techniques we adopt:

| Technique | Why It's Needed |
|-----------|----------------|
| **TheRock nightly ROCm SDK** (tarball install to `/opt/rocm`) | Official ROCm 7.0/7.2 packages lack full RDNA4 support; TheRock nightlies have the latest gfx1201 fixes |
| **PyTorch from ROCm nightlies** (`rocm.nightlies.amd.com/v2-staging/gfx120X-all/`) | Stock PyTorch wheels don't include RDNA4 GPU targets |
| **Patch vLLM `amdsmi` detection** | `amdsmi` fails in containers, causing vLLM to fall back to CPU; patches force `is_rocm=True` and hardcode `gfx1201` |
| **ROCm Clang as host compiler** (`CC=/opt/rocm/llvm/bin/clang`) | Fedora's GCC produces ABI-mismatched `.so` files → segfaults at runtime |
| **`tcmalloc` preload** (`LD_PRELOAD=libtcmalloc_minimal.so.4`) | Fixes double-free crash on vLLM shutdown |
| **Flash Attention from ROCm fork** (`ROCm/flash-attention`, `main_perf` branch) | Upstream flash-attn doesn't support RDNA4 |
| **bitsandbytes from ROCm fork** | Enables 4-bit quantization (AWQ/GPTQ) on RDNA4 |
| **`PYTORCH_ROCM_ARCH=gfx1201`** | Compile vLLM C++ extensions only for the target GPU |

**Tested models on R9700 AI Pro (from kyuz0 benchmarks):**

| Model | Quant | GPUs | Max Context | Notes |
|-------|-------|------|-------------|-------|
| Llama 3.1 8B | FP16 | 1 | 127k | 98% VRAM utilization |
| Qwen3 Coder 30B | 4-bit | 1 | 151k | bitsandbytes quantization |
| Qwen3 Coder 30B | 4-bit | 2 (TP=2) | 262k | Tensor parallelism doubles context |
| Qwen3-Next 80B | 4-bit | 2 (TP=2) | 156k | Dual-GPU required |

### Why one engine at a time (for now)?

- Both llama-server and vLLM bind to a port for their OpenAI-compatible API
- GPU memory is typically fully consumed by one loaded model
- The `/v1` proxy needs a single upstream target
- Future: could run both on different ports if GPU memory allows, with the proxy routing by model name

### Profile system: builds vs configurations

The current "profile" concept is tightly coupled to llama.cpp cmake builds. For vLLM, there's no compilation step — the sidecar image is pre-built. A vLLM "profile" is a named configuration that controls how `vllm serve` is launched.

Generalize the profile concept into two types:

| | llama.cpp Profile | vLLM Profile |
|---|---|---|
| **What it represents** | A compiled llama-server binary with specific cmake flags | A named launch configuration for the vLLM sidecar |
| **Created by** | Git clone + cmake + ninja (existing build pipeline) | Saving a configuration form (no build step) |
| **Stored as** | Binary at `/data/builds/{id}/llama-server` | JSON at `/data/config/vllm-profiles.json` |
| **Key settings** | Backend (cuda/rocm/cpu), cmake flags, git ref | Quantization method, tensor parallelism, dtype, max model len, attention backend |
| **Selected at** | Model activation time (existing `BuildID` in ModelConfig) | Model activation time (new `VLLMProfileID` in ModelConfig) |

### Model format differences

| | llama.cpp | vLLM |
|---|---|---|
| **Format** | GGUF (single file or sharded) | HuggingFace safetensors (directory of files) |
| **Quantization** | Baked into GGUF (Q4_K_M, Q5_K_S, etc.) | Applied at runtime (AWQ, GPTQ, FP16, BF16) or pre-quantized |
| **Typical size** | 4-15 GB (quantized) | 15-150 GB (full precision or light quant) |
| **HF search filter** | `gguf` tag | `text-generation` pipeline tag |
| **Storage layout** | `/data/models/llamacpp/{safeName}/{file}.gguf` | `/data/models/vllm/{safeName}/` (full model dir) |
| **Download** | Single file (or sharded set) | Multiple files: config, tokenizer, weights |

---

## Container Architecture

### Overview

```
┌──────────────────────────────────────────────────────┐
│  Docker/Podman Compose                               │
│                                                      │
│  ┌──────────────┐         ┌────────────────────┐     │
│  │  llamactl    │         │  vllm (sidecar)    │     │
│  │              │  HTTP   │                    │     │
│  │  :3000 (UI)  │◄───────►│  :8080 (OpenAI)   │     │
│  │  :8080 (proxy│         │                    │     │
│  │   to llama-  │         │  vllm/vllm-openai  │     │
│  │   server OR  │         │  OR                │     │
│  │   vllm)      │         │  Dockerfile.vllm-  │     │
│  │              │         │  rocm (RDNA4)      │     │
│  └──────┬───────┘         └────────┬───────────┘     │
│         │                          │                 │
│    ┌────┴──────────────────────────┴────┐            │
│    │  Shared Volumes                    │            │
│    │  - vllm-models:/data/models/vllm   │            │
│    │  - llamactl-data:/data             │            │
│    └────────────────────────────────────┘            │
└──────────────────────────────────────────────────────┘
```

When llama.cpp is active, llamactl runs `llama-server` internally on port 8080 and the vLLM sidecar is stopped. When vLLM is active, llamactl stops `llama-server` and starts the vLLM sidecar, then proxies `/v1` to the sidecar's port.

### Networking

The vLLM sidecar and llamactl share a compose network. llamactl reaches vLLM at `http://vllm:8080` (compose service DNS). The vLLM port is **not** published to the host — all external access goes through llamactl's proxy on port 3000.

When llama-server is active instead, it binds to port 8080 inside the llamactl container (existing behavior). The `/v1` proxy target switches between `localhost:8080` (llama-server) and `vllm:8080` (vLLM sidecar) based on which engine is active.

### Sidecar lifecycle

llamactl manages the vLLM sidecar via the compose/container CLI:

```go
// Start vLLM sidecar with a specific model
func (m *Manager) startVLLM(cfg LaunchConfig) error {
    // 1. Stop llama-server if running
    // 2. Write vllm launch args to a shared config file or env
    // 3. Start the sidecar: exec "docker compose -f <file> up -d vllm"
    //    or "podman compose -f <file> start vllm"
    // 4. Poll health endpoint until ready
}

// Stop vLLM sidecar
func (m *Manager) stopVLLM() error {
    // exec "docker compose -f <file> stop vllm"
}
```

The compose file defines the vLLM service with `profiles: [vllm]` so it doesn't start by default. When a user activates a vLLM model, llamactl starts the sidecar with the appropriate `command` override.

Alternatively, the vLLM sidecar can run with `entrypoint: sleep infinity` and llamactl uses `docker exec` to launch `vllm serve` inside it — this avoids container restart on model switch and is more responsive.

---

## Container Files

### `Dockerfile.vllm-rocm` — New

Adapted from [kyuz0/amd-r9700-vllm-toolboxes](https://github.com/kyuz0/amd-r9700-vllm-toolboxes). This is a standalone image for the vLLM sidecar on AMD RDNA4 GPUs.

```dockerfile
FROM registry.fedoraproject.org/fedora:43

# System base + build tools
RUN dnf -y install --setopt=install_weak_deps=False --nodocs \
    python3.13 python3.13-devel git rsync libatomic bash ca-certificates curl \
    gcc gcc-c++ binutils make cmake ninja-build tar xz \
    libdrm-devel zlib-devel openssl-devel numactl-devel \
    gperftools-libs \
    && dnf clean all

# TheRock nightly ROCm SDK (bleeding-edge RDNA4 support)
ARG ROCM_MAJOR_VER=7
ARG GFX=gfx120X-all
RUN set -euo pipefail; \
    BASE="https://therock-nightly-tarball.s3.amazonaws.com"; \
    PREFIX="therock-dist-linux-${GFX}-${ROCM_MAJOR_VER}"; \
    KEY="$(curl -s "${BASE}?list-type=2&prefix=${PREFIX}" \
        | tr '<' '\n' \
        | grep -o "${PREFIX}\..*\.tar\.gz" \
        | sort -V | tail -n1)"; \
    curl -fSL "${BASE}/${KEY}" -o /tmp/therock.tar.gz && \
    mkdir -p /opt/rocm && \
    tar xzf /tmp/therock.tar.gz -C /opt/rocm --strip-components=1 && \
    rm /tmp/therock.tar.gz

# ROCm environment
ENV ROCM_PATH=/opt/rocm \
    HIP_PLATFORM=amd \
    HIP_PATH=/opt/rocm \
    HIP_CLANG_PATH=/opt/rocm/llvm/bin \
    PATH=/opt/rocm/bin:/opt/rocm/llvm/bin:$PATH \
    LD_LIBRARY_PATH=/opt/rocm/lib:/opt/rocm/lib64:/opt/rocm/llvm/lib \
    ROCBLAS_USE_HIPBLASLT=1 \
    TORCH_ROCM_AOTRITON_ENABLE_EXPERIMENTAL=1 \
    VLLM_TARGET_DEVICE=rocm \
    HIP_FORCE_DEV_KERNARG=1 \
    LD_PRELOAD=/usr/lib64/libtcmalloc_minimal.so.4

# Python venv + PyTorch (TheRock nightly, RDNA4-enabled)
RUN python3.13 -m venv /opt/venv
ENV VIRTUAL_ENV=/opt/venv PATH=/opt/venv/bin:$PATH PIP_NO_CACHE_DIR=1
RUN pip install --upgrade pip wheel packaging "setuptools<80.0.0" && \
    pip install --index-url https://rocm.nightlies.amd.com/v2-staging/gfx120X-all/ \
        --pre torch torchaudio torchvision

# Flash Attention (ROCm fork, RDNA4-optimized)
RUN git clone https://github.com/ROCm/flash-attention.git /tmp/flash-attn && \
    cd /tmp/flash-attn && git checkout main_perf && \
    FLASH_ATTENTION_TRITON_AMD_ENABLE=TRUE python setup.py install && \
    rm -rf /tmp/flash-attn

# vLLM (from source, with amdsmi patches for container compatibility)
ARG VLLM_GIT_REF=main
RUN git clone https://github.com/vllm-project/vllm.git /opt/vllm
WORKDIR /opt/vllm
RUN git checkout ${VLLM_GIT_REF}

# Patch: force ROCm detection (amdsmi fails in containers)
COPY scripts/patch-vllm-amdsmi.py /tmp/
RUN python /tmp/patch-vllm-amdsmi.py

# Build vLLM with ROCm Clang (avoids GCC ABI mismatch segfaults)
ENV CC=/opt/rocm/llvm/bin/clang CXX=/opt/rocm/llvm/bin/clang++ \
    PYTORCH_ROCM_ARCH=gfx1201 HIP_ARCHITECTURES=gfx1201 AMDGPU_TARGETS=gfx1201 \
    ROCM_HOME=/opt/rocm MAX_JOBS=4
RUN HIP_DEVICE_LIB_PATH=$(find /opt/rocm -type d -name bitcode -print -quit) && \
    CMAKE_ARGS="-DROCM_PATH=/opt/rocm -DHIP_PATH=/opt/rocm -DAMDGPU_TARGETS=gfx1201" \
    pip wheel --no-build-isolation --no-deps -w /tmp/dist -v . && \
    pip install /tmp/dist/*.whl && rm -rf /tmp/dist

# bitsandbytes (ROCm fork, enables 4-bit quantization)
RUN git clone -b rocm_enabled_multi_backend https://github.com/ROCm/bitsandbytes.git /tmp/bnb && \
    cd /tmp/bnb && \
    cmake -S . -DGPU_TARGETS=gfx1201 -DBNB_ROCM_ARCH=gfx1201 \
        -DCOMPUTE_BACKEND=hip \
        -DCMAKE_HIP_COMPILER=/opt/rocm/llvm/bin/clang++ \
        -DCMAKE_CXX_COMPILER=/opt/rocm/llvm/bin/clang++ && \
    make -j$(nproc) && \
    pip install --no-build-isolation --no-deps . && \
    rm -rf /tmp/bnb

# Cleanup
RUN find /opt/venv -type d -name "__pycache__" -prune -exec rm -rf {} + && \
    dnf clean all && rm -rf /var/cache/dnf/*

EXPOSE 8080

ENTRYPOINT ["vllm", "serve"]
```

> **Note:** The `amdsmi` patch script (`scripts/patch-vllm-amdsmi.py`) will be extracted from the kyuz0 Dockerfile's inline Python into a standalone file for maintainability.

### `Dockerfile.vllm-cuda` — Optional / Future

For NVIDIA, we use the official pre-built image directly:

```yaml
vllm:
  image: vllm/vllm-openai:latest
```

No custom Dockerfile needed. If we later need customizations, we can create one that uses `vllm/vllm-openai` as the base.

### Docker Compose changes

Each GPU compose file gets a vLLM sidecar service. The sidecar uses `profiles: [vllm]` so it only starts when explicitly requested.

**docker-compose.rocm.yml:**

```yaml
services:
  llamactl:
    build:
      context: .
      dockerfile: Dockerfile.rocm
    container_name: llamactl
    ports:
      - "3000:3000"
      - "8080:8080"
    volumes:
      - llamactl-data:/data:z
      - ${LLAMA_MODELS_DIR:-llamactl-llama-models}:/data/models/llamacpp:z
      - ${VLLM_MODELS_DIR:-llamactl-vllm-models}:/data/models/vllm:z
    devices:
      - /dev/kfd:/dev/kfd
      - /dev/dri:/dev/dri
    group_add:
      - "${HOST_VIDEO_GID:-video}"
      - "${HOST_RENDER_GID:-render}"
    ipc: host
    security_opt:
      - seccomp=unconfined
    ulimits:
      memlock: { soft: -1, hard: -1 }
    env_file:
      - path: .env
        required: false
    restart: unless-stopped

  vllm:
    build:
      context: .
      dockerfile: Dockerfile.vllm-rocm
    container_name: llamactl-vllm
    volumes:
      - ${VLLM_MODELS_DIR:-llamactl-vllm-models}:/models:z
    devices:
      - /dev/kfd:/dev/kfd
      - /dev/dri:/dev/dri
    group_add:
      - "${HOST_VIDEO_GID:-video}"
      - "${HOST_RENDER_GID:-render}"
    ipc: host
    security_opt:
      - seccomp=unconfined
    ulimits:
      memlock: { soft: -1, hard: -1 }
    env_file:
      - path: .env
        required: false
    # Don't start by default — llamactl manages lifecycle
    profiles:
      - vllm
    # Default: sleep until llamactl exec's vllm serve
    entrypoint: ["sleep", "infinity"]
    restart: "no"

volumes:
  llamactl-data:
    driver: local
```

**docker-compose.cuda.yml:**

```yaml
services:
  llamactl:
    # ... existing llamactl service (unchanged except model volumes) ...
    volumes:
      - llamactl-data:/data:z
      - ${LLAMA_MODELS_DIR:-llamactl-llama-models}:/data/models/llamacpp:z
      - ${VLLM_MODELS_DIR:-llamactl-vllm-models}:/data/models/vllm:z

  vllm:
    image: vllm/vllm-openai:latest
    container_name: llamactl-vllm
    volumes:
      - ${VLLM_MODELS_DIR:-llamactl-vllm-models}:/models:z
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: all
              capabilities: [gpu]
    devices:
      - nvidia.com/gpu=all     # Podman CDI
    profiles:
      - vllm
    entrypoint: ["sleep", "infinity"]
    restart: "no"

volumes:
  llamactl-data:
    driver: local
```

**docker-compose.cpu.yml** — no vLLM service (vLLM requires GPU).

### Model volume sharing

The vLLM sidecar mounts the vLLM model volume at `/models`. When llamactl tells the sidecar to serve a model, it passes the path as `/models/{safeName}`.

llamactl mounts the same volume at `/data/models/vllm` for downloading models into it. The download happens inside the llamactl container; the sidecar only reads from the shared volume at serve time.

```
llamactl container:  /data/models/vllm/{safeName}/  ← downloads here
vllm sidecar:        /models/{safeName}/             ← serves from here
                     (same underlying volume)
```

### `.env` model directory configuration

```bash
# Written by setup.sh — optional host bind mounts for model storage
# If unset, Docker/Podman named volumes are used (default)

# LLAMA_MODELS_DIR=/mnt/nvme/models/llamacpp
# VLLM_MODELS_DIR=/mnt/nvme/models/vllm
```

### setup.sh changes

Add prompts during `install` to optionally configure external model directories:

```bash
# After GPU detection, before build
if [[ "$GPU_VENDOR" != "cpu" ]]; then
    echo ""
    echo "Model storage (optional — press Enter to use container volumes):"
    read -rp "  llama.cpp models directory: " llama_dir
    read -rp "  vLLM models directory:      " vllm_dir
    [[ -n "$llama_dir" ]] && echo "LLAMA_MODELS_DIR=${llama_dir}" >> "$env_file"
    [[ -n "$vllm_dir" ]]  && echo "VLLM_MODELS_DIR=${vllm_dir}" >> "$env_file"
fi
```

---

## Data Model Changes

### New: Engine type

```go
type Engine string

const (
    EngineLlamaCpp Engine = "llamacpp"
    EngineVLLM     Engine = "vllm"
)
```

### Extended: Model

```go
type Model struct {
    ID           string    `json:"id"`
    Engine       Engine    `json:"engine"`       // NEW: "llamacpp" or "vllm"
    ModelID      string    `json:"model_id"`     // HuggingFace repo ID
    Filename     string    `json:"filename"`     // GGUF filename (llamacpp only)
    Quant        string    `json:"quant"`        // quantization label
    SizeBytes    int64     `json:"size_bytes"`
    FilePath     string    `json:"file_path"`    // path to GGUF file or model directory
    VRAMEstGB    float64   `json:"vram_est_gb"`
    DownloadedAt time.Time `json:"downloaded_at"`
}
```

### New: VLLMProfile

```go
type VLLMProfile struct {
    ID                    string  `json:"id"`
    Name                  string  `json:"name"`
    Dtype                 string  `json:"dtype"`                   // "auto", "float16", "bfloat16"
    MaxModelLen           int     `json:"max_model_len"`           // 0 = use model default
    GPUMemoryUtilization  float64 `json:"gpu_memory_utilization"` // 0.0-1.0, default 0.9
    TensorParallelSize    int     `json:"tensor_parallel_size"`    // number of GPUs
    Quantization          string  `json:"quantization"`            // "", "awq", "gptq", "squeezellm"
    EnforceEager          bool    `json:"enforce_eager"`           // disable CUDA graphs
    AttentionBackend      string  `json:"attention_backend"`       // "triton" (stable) or "rocm" (experimental, higher throughput)
    ExtraArgs             string  `json:"extra_args"`              // arbitrary CLI flags
}
```

### Extended: ModelConfig

```go
type ModelConfig struct {
    // llama.cpp settings (existing)
    GPULayers      int    `json:"gpu_layers"`
    TensorSplit    string `json:"tensor_split"`
    ContextSize    int    `json:"context_size"`
    Threads        int    `json:"threads"`
    FlashAttention bool   `json:"flash_attention"`
    Jinja          bool   `json:"jinja"`
    KVCacheQuant   string `json:"kv_cache_quant"`
    ExtraFlags     string `json:"extra_flags"`
    BuildID        string `json:"build_id"`

    // vLLM settings (new)
    VLLMProfileID  string `json:"vllm_profile_id,omitempty"`
}
```

### Extended: Config

```yaml
# llamactl.yaml
listen_addr: ":3000"
data_dir: "/data"
llama_port: 8080
vllm_host: "vllm"         # compose service name for sidecar
vllm_port: 8080            # port inside sidecar container
external_url: ""
hf_token: ""
api_key: ""
log_level: "info"
```

---

## Storage Layout

```
/data/
├── builds/                          # llama.cpp compiled binaries (unchanged)
│   ├── b3805-rocm/
│   │   ├── llama-server
│   │   └── *.so
│   └── b3805-cuda/
│       └── llama-server
├── models/
│   ├── llamacpp/                    # GGUF models (NEW subdirectory)
│   │   ├── meta-llama--Llama-2-7b/
│   │   │   ├── Q4_K_M.gguf
│   │   │   └── meta.json
│   │   └── ...
│   └── vllm/                        # HuggingFace format models (NEW)
│       ├── meta-llama--Llama-2-7b/
│       │   ├── config.json
│       │   ├── tokenizer.json
│       │   ├── model-00001-of-00002.safetensors
│       │   ├── model-00002-of-00002.safetensors
│       │   └── meta.json
│       └── ...
└── config/
    ├── llamactl.yaml
    ├── builds.json                  # llama.cpp build metadata (unchanged)
    ├── models.json                  # all models (both engines, with engine field)
    └── vllm-profiles.json           # vLLM profile configurations (NEW)
```

### Migration note

Existing GGUF models are stored at `/data/models/{safeName}/`. These need to be migrated to `/data/models/llamacpp/{safeName}/` on first startup. The migration should:
1. Detect models in the old location
2. Move them to the new `llamacpp/` subdirectory
3. Update `FilePath` in `models.json`
4. Set `Engine: "llamacpp"` on all existing entries

---

## Go Code Changes

### `internal/models/engine.go` — New

Engine type definition and helpers shared across packages.

```go
package models

type Engine string

const (
    EngineLlamaCpp Engine = "llamacpp"
    EngineVLLM     Engine = "vllm"
)

func (e Engine) String() string   { return string(e) }
func (e Engine) ModelDir() string { return string(e) }
```

### `internal/models/registry.go` — Modified

- `Add()` takes an `Engine` parameter, stores models under `/data/models/{engine}/{safeName}/`
- `List()` accepts optional `Engine` filter
- `Get()` unchanged (ID lookup)
- `Delete()` unchanged (path from model metadata)
- `DefaultConfig()` returns engine-appropriate defaults:
  - llama.cpp: 999 GPU layers, 8192 context, etc. (current behavior)
  - vLLM: empty VLLMProfileID (user must select one)
- Add `Migrate()` method for one-time old → new path migration

### `internal/builder/profiles.go` — Modified

Add vLLM profile CRUD alongside existing llama.cpp build profiles.

```go
// Existing: BuildProfile = llama.cpp build profile (cmake flags + compiled binary)

// New
type VLLMProfile struct { ... }  // as defined in data model above

func DefaultVLLMProfile() VLLMProfile {
    return VLLMProfile{
        Name:                 "default",
        Dtype:                "auto",
        GPUMemoryUtilization: 0.9,
        TensorParallelSize:   1,
        AttentionBackend:     "triton",
    }
}

func LoadVLLMProfiles(path string) ([]VLLMProfile, error)  { ... }
func SaveVLLMProfiles(path string, profiles []VLLMProfile) error { ... }
```

### `internal/process/manager.go` — Modified

The process manager gains awareness of two engine types. For llama.cpp, it manages a subprocess (existing). For vLLM, it manages the sidecar container via exec.

```go
type LaunchConfig struct {
    Engine Engine

    // llama.cpp fields (existing)
    BinaryPath     string
    ModelPath      string
    GPULayers      int
    TensorSplit    string
    ContextSize    int
    Threads        int
    FlashAttention bool
    Jinja          bool
    KVCacheQuant   string

    // vLLM fields (new — used to construct vllm serve args for docker exec)
    VLLMModelPath          string  // "/models/{safeName}" inside sidecar
    VLLMDtype              string
    VLLMMaxModelLen        int
    VLLMGPUMemUtilization  float64
    VLLMTensorParallel     int
    VLLMQuantization       string
    VLLMEnforceEager       bool
    VLLMAttentionBackend   string

    // Shared
    Host       string
    Port       int
    ExtraFlags []string
}
```

Starting a vLLM model:

```go
func (m *Manager) startVLLM(cfg LaunchConfig) error {
    // 1. Ensure vLLM sidecar container is running
    //    exec: docker compose -f <compose-file> up -d vllm
    //    (or: podman compose ...)
    //
    // 2. Build vllm serve command
    args := []string{
        "exec", "llamactl-vllm",
        "vllm", "serve", cfg.VLLMModelPath,
        "--host", "0.0.0.0",
        "--port", strconv.Itoa(cfg.Port),
    }
    // ... append profile args (dtype, tensor-parallel, etc.)
    //
    // 3. Launch via: docker exec llamactl-vllm vllm serve /models/... --host 0.0.0.0 ...
    //    (as a background process, capture stdout/stderr for log streaming)
    //
    // 4. Poll health endpoint: http://vllm:8080/health
    //
    // 5. Update proxy target to vllm:8080
}
```

### `internal/api/proxy.go` — Modified

The `/v1` reverse proxy needs a switchable upstream:

```go
type ProxyTarget struct {
    mu     sync.RWMutex
    target *url.URL  // http://localhost:8080 (llama.cpp) or http://vllm:8080 (vLLM)
}

func (p *ProxyTarget) Switch(engine Engine, cfg config.Config) {
    p.mu.Lock()
    defer p.mu.Unlock()
    switch engine {
    case EngineLlamaCpp:
        p.target, _ = url.Parse(fmt.Sprintf("http://localhost:%d", cfg.LlamaPort))
    case EngineVLLM:
        p.target, _ = url.Parse(fmt.Sprintf("http://%s:%d", cfg.VLLMHost, cfg.VLLMPort))
    }
}
```

### `internal/huggingface/client.go` — Modified

Add engine-aware search:

```go
func (c *Client) Search(ctx context.Context, query string, engine Engine) ([]ModelSearchResult, error) {
    params := url.Values{"search": {query}, "sort": {"downloads"}, "direction": {"-1"}}
    switch engine {
    case EngineLlamaCpp:
        params.Set("filter", "gguf")
    case EngineVLLM:
        params.Set("filter", "text-generation")
    }
    // ... rest unchanged
}
```

### `internal/huggingface/downloader.go` — Modified

For vLLM models, download all required files (safetensors, config, tokenizer) as a group:

```go
func (d *Downloader) StartVLLM(modelID string, destDir string) (string, error) {
    // 1. Fetch file list from HF API
    // 2. Filter to required files: *.safetensors, config.json,
    //    tokenizer.json, tokenizer_config.json, special_tokens_map.json,
    //    generation_config.json
    // 3. Download each file to destDir (resumable, same as GGUF)
    // 4. Track aggregate progress across all files
    // 5. On completion, call registry.Add() with Engine=vllm
}
```

### `internal/api/build.go` — Modified

- `GET /api/builds/` — list llama.cpp builds AND vLLM profiles
- `POST /api/builds/` — create llama.cpp build (existing) or vLLM profile (new, based on `engine` form field)
- `GET /api/vllm/profiles` — list vLLM profiles
- `POST /api/vllm/profiles` — create/update vLLM profile
- `DELETE /api/vllm/profiles/{id}` — delete vLLM profile

### `internal/api/models.go` — Modified

- `GET /api/models?engine=llamacpp` — filter by engine
- `PUT /api/models/{id}/activate` — check model's engine, launch with appropriate process config
- `GET /api/models/{id}/config` — return engine-appropriate config form

### `internal/api/hf.go` — Modified

- `GET /api/hf/search?q=query&engine=llamacpp` — pass engine to client
- `GET /api/hf/model?id=repo&engine=vllm` — engine-aware file listing
- `POST /api/hf/download` — route to GGUF or vLLM download flow based on engine

---

## UI Changes

### Browse tab (models_browse.html)

Add engine selector at the top of the search form:

```html
<fieldset role="group">
  <label>
    <input type="radio" name="engine" value="llamacpp" checked
           hx-get="/api/hf/search" hx-include="[name='q']">
    llama.cpp (GGUF)
  </label>
  <label>
    <input type="radio" name="engine" value="vllm">
    vLLM (safetensors)
  </label>
</fieldset>
```

When vLLM is selected:
- Search results show text-generation models instead of GGUF-tagged models
- Model detail page shows total model size (all weight files combined) instead of per-quant file list
- Download button downloads the full model directory, not a single GGUF file
- Progress shows aggregate bytes across all files

### Models tab (models.html)

Add engine filter toggle:

```html
<fieldset role="group">
  <label><input type="radio" name="engine" value="" checked> All</label>
  <label><input type="radio" name="engine" value="llamacpp"> llama.cpp</label>
  <label><input type="radio" name="engine" value="vllm"> vLLM</label>
</fieldset>
```

Model cards show the engine badge (small tag: "GGUF" or "vLLM").

**Configure modal changes:**
- For llama.cpp models: existing form (GPU layers, context size, build selector, etc.)
- For vLLM models: vLLM profile selector dropdown, with link to create new profile
- The active form fields swap based on the model's engine type

### Builds tab (builds.html)

This tab now manages both llama.cpp builds and vLLM profiles.

**Unified list with type badge** (preferred — simpler):
- The existing build list shows llama.cpp builds with a "llama.cpp" badge
- Below or interleaved, vLLM profiles appear with a "vLLM" badge
- The "New Build" form gets an engine selector at the top:
  - **llama.cpp selected**: show existing form (git ref, profile, cmake options)
  - **vLLM selected**: show vLLM profile form (name, dtype, GPU memory util, tensor parallel, attention backend, quantization, extra args). No build step — saves immediately.

### Dashboard (index.html)

- Show which engine is currently active alongside the model name
- Status line: "Running: Llama-2-7b (llama.cpp, rocm)" or "Running: Llama-2-70b (vLLM)"

---

## Implementation Order

### Step 1: Dockerfile.vllm-rocm + compose sidecar

**Priority: get vLLM running on the dual 9700 AI Pro setup.**

- Create `Dockerfile.vllm-rocm` adapted from kyuz0's Dockerfile
- Extract `amdsmi` patch into `scripts/patch-vllm-amdsmi.py`
- Add vLLM sidecar service to `docker-compose.rocm.yml` (with `profiles: [vllm]`)
- Add model volume mounts to compose files
- Test manually: `docker compose --profile vllm up -d` → `docker exec llamactl-vllm vllm serve /models/... --host 0.0.0.0 --port 8080`
- Verify inference works on gfx1201, including TP=2 across both GPUs

### Step 2: Data model + storage migration

- Add `Engine` type
- Add `engine` field to `Model` struct
- Create `VLLMProfile` struct and persistence
- Implement model directory migration (`/data/models/` → `/data/models/llamacpp/`)
- Update `registry.go` for engine-aware storage paths
- Backfill `engine: "llamacpp"` on existing models

### Step 3: vLLM profiles

- Add vLLM profile CRUD (load/save/list/delete from `vllm-profiles.json`)
- Add API endpoints for vLLM profiles
- Update Builds page UI with engine selector and vLLM profile form
- Create default vLLM profile on first run (dtype=auto, gpu_mem_util=0.9, TP=1, attention=triton)

### Step 4: Engine-aware HuggingFace integration

- Add engine parameter to search and file listing
- Implement vLLM model download (multi-file directory download with aggregate progress)
- Update Browse page UI with engine radio buttons
- Track per-file and aggregate download progress for multi-file downloads

### Step 5: Sidecar management + model activation

- Implement sidecar start/stop via compose/container CLI
- Implement `docker exec` launch of `vllm serve` inside sidecar
- Implement switchable `/v1` proxy target (localhost vs vllm sidecar)
- Update model activation to detect engine and route appropriately
- Update model config UI to show engine-appropriate form
- Pass `HF_TOKEN` env var to sidecar for gated model tokenizer downloads

### Step 6: CUDA sidecar support

- Add vLLM sidecar to `docker-compose.cuda.yml` using `vllm/vllm-openai:latest`
- Test on NVIDIA 5080
- Verify same management flow works for both GPU vendors

### Step 7: setup.sh + UI polish

- Add model directory prompts to setup.sh
- Update `.env` writing for model mount points
- Add engine badges to model cards
- Update dashboard to show active engine
- Update Models tab with engine filter
- End-to-end testing: download vLLM model → create profile → activate → chat

---

## Open Questions

1. **vLLM version pinning** — The kyuz0 Dockerfile builds from `main` branch. We should pin to a tagged release for stability, but RDNA4 fixes may only be on `main`. Consider a build arg (`VLLM_GIT_REF`) defaulting to a known-good commit.

2. **TheRock nightly stability** — The ROCm SDK comes from nightly tarballs that could break at any time. We should cache/mirror known-good tarballs and document which nightly was tested.

3. **HuggingFace token for vLLM** — vLLM needs the HF token at serve time for gated model tokenizer downloads. Pass via `HF_TOKEN` env var to the sidecar container (add to `.env` and compose).

4. **Disk space for vLLM models** — vLLM models are 5-10x larger than quantized GGUF files. The UI should prominently show total size before download and warn about disk space. External mount points become almost essential for vLLM.

5. **Container runtime detection for exec** — llamactl needs to know whether to use `docker exec` or `podman exec` to interact with the sidecar. Can detect from compose command already stored in config, or from the container runtime binary.

6. **Sidecar health on startup** — The vLLM sidecar with `sleep infinity` entrypoint is always "healthy" from Docker's perspective but not serving. llamactl must track whether `vllm serve` is actually running inside it (via PID check or health endpoint polling).

7. **Multi-file download resumability** — If a vLLM model download (5+ files) fails partway through, we need to resume from where we left off, not re-download completed files. The existing `.part` file mechanism works per-file; need to track which files in a set are complete.

8. **Attention backend selection** — The kyuz0 project documents two backends for RDNA4: Triton (stable, default) and ROCm native (experimental, higher throughput). Expose this as a vLLM profile option.

9. **Compose file complexity** — Adding a sidecar to every compose file means more YAML to maintain. Consider whether a compose override file (`docker-compose.vllm.yml`) that layers on top would be cleaner than embedding in each GPU-specific file.
