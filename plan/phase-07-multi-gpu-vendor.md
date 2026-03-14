# Phase 7 — Multi-GPU Vendor Support (NVIDIA + Intel)

> Per-vendor Dockerfiles, auto-detection setup script, CUDA and SYCL build profiles

---

## Goal

Support NVIDIA (CUDA) and Intel (SYCL/oneAPI) GPUs alongside the existing AMD (ROCm) backend. Each vendor requires different base images, driver libraries, and device passthrough — so the container layer must be split per-vendor, with a setup script that detects the host GPU and builds/starts the correct image. The Go application code and llama.cpp build logic remain largely shared; the differences are isolated to Dockerfiles, compose files, build profiles, and GPU detection.

---

## Deliverables

- `Dockerfile.rocm` (existing Dockerfile renamed), `Dockerfile.cuda`, `Dockerfile.intel`
- `docker-compose.rocm.yml`, `docker-compose.cuda.yml`, `docker-compose.intel.yml`
- `setup.sh` — detects host GPU vendor, builds and starts the correct stack
- CUDA and SYCL build profiles in `profiles.go`
- NVIDIA and Intel GPU detection in `detect.go`
- Updated Makefile targets

---

## Architecture Decisions

### Why separate Dockerfiles instead of one unified image?

The base images and SDK packages are fundamentally different and very large:

| Vendor | Base Image | SDK Size | Device Nodes |
|--------|-----------|----------|-------------|
| AMD | `fedora:43` + ROCm 7.2 repo | ~8 GB | `/dev/kfd`, `/dev/dri` |
| NVIDIA | `nvidia/cuda:12.8.1-devel-ubuntu24.04` | ~5 GB | `/dev/nvidia*` (managed by nvidia-container-toolkit) |
| Intel | `intel/oneapi-basekit:2025.1` | ~10 GB | `/dev/dri` (render nodes) |

A unified image would be 20+ GB and require all three SDKs installed. Per-vendor images stay lean (~8-12 GB) and only ship what's needed.

### Why not runtime GPU detection inside the container?

The container needs the correct SDK libraries *installed at build time* for llama.cpp compilation. You can't compile a CUDA build inside a ROCm container. GPU detection must happen on the host *before* selecting which container to build and run.

---

## Files Created / Modified

### `setup.sh` — New

Host-side script that detects GPU vendor and orchestrates the correct container stack.

```bash
#!/usr/bin/env bash
set -euo pipefail

# Detect GPU vendor
detect_gpu() {
    # NVIDIA: check for nvidia-smi or /dev/nvidia0
    if command -v nvidia-smi &>/dev/null || [ -e /dev/nvidia0 ]; then
        echo "cuda"
        return
    fi

    # AMD: check for /dev/kfd (ROCm kernel driver)
    if [ -e /dev/kfd ]; then
        echo "rocm"
        return
    fi

    # Intel: check for Intel render nodes in /dev/dri
    if [ -d /dev/dri ]; then
        for card in /dev/dri/renderD*; do
            if [ -e "$card" ]; then
                # Check if the device is Intel via sysfs
                local pci_path
                pci_path=$(udevadm info -q path "$card" 2>/dev/null || true)
                if grep -qi intel /sys/class/drm/*/device/vendor 2>/dev/null; then
                    echo "intel"
                    return
                fi
            fi
        done
    fi

    echo "cpu"
}

GPU=$(detect_gpu)
echo "Detected GPU backend: $GPU"

COMPOSE_FILE="docker-compose.${GPU}.yml"
DOCKERFILE="Dockerfile.${GPU}"

if [ ! -f "$COMPOSE_FILE" ]; then
    echo "Error: $COMPOSE_FILE not found"
    exit 1
fi

case "${1:-up}" in
    up)
        docker compose -f "$COMPOSE_FILE" up -d --build
        ;;
    down)
        docker compose -f "$COMPOSE_FILE" down
        ;;
    rebuild)
        docker compose -f "$COMPOSE_FILE" down
        docker compose -f "$COMPOSE_FILE" build --no-cache
        docker compose -f "$COMPOSE_FILE" up -d
        ;;
    logs)
        docker compose -f "$COMPOSE_FILE" logs -f
        ;;
    *)
        echo "Usage: $0 [up|down|rebuild|logs]"
        exit 1
        ;;
esac
```

Detection priority: NVIDIA → AMD → Intel → CPU fallback. This ordering works because `nvidia-smi` and `/dev/kfd` are unambiguous; Intel detection is a fallback that checks DRI render nodes.

---

### `Dockerfile.rocm` — Renamed from `Dockerfile`

No changes needed — just rename the existing file. Already uses Fedora 43 + ROCm 7.2.

---

### `Dockerfile.cuda` — New

```dockerfile
# Stage 1: Build the Go binary
FROM golang:1.25-bookworm AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o llamactl ./cmd/llamactl

# Stage 2: Runtime — NVIDIA CUDA 12.8
# Dev image needed because llama.cpp builds happen inside the container.
FROM nvidia/cuda:12.8.1-devel-ubuntu24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    cmake \
    ninja-build \
    git \
    curl \
    build-essential \
    vulkan-tools \
    libvulkan-dev \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /app/llamactl /usr/local/bin/llamactl

RUN mkdir -p /data/config /data/builds /data/models /data/llama.cpp

RUN cat > /data/config/llamactl.yaml <<'EOF'
listen_addr: ":3000"
data_dir: "/data"
llama_port: 8080
log_level: "info"
EOF

VOLUME ["/data"]
EXPOSE 3000
EXPOSE 8080

ENTRYPOINT ["llamactl", "--config", "/data/config/llamactl.yaml"]
```

Key differences from ROCm:
- Base image: `nvidia/cuda:12.8.1-devel-ubuntu24.04` (provides nvcc, cuBLAS, CUDA headers)
- No ROCm env vars needed — CUDA paths are pre-configured in the NVIDIA image
- Lighter package install — no need for hipblas, rocblas, rocm-llvm, etc.

---

### `Dockerfile.intel` — New

```dockerfile
# Stage 1: Build the Go binary
FROM golang:1.25-bookworm AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o llamactl ./cmd/llamactl

# Stage 2: Runtime — Intel oneAPI
# Dev image needed because llama.cpp builds happen inside the container.
FROM intel/oneapi-basekit:2025.1-0-devel-ubuntu24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    cmake \
    ninja-build \
    git \
    curl \
    build-essential \
    && rm -rf /var/lib/apt/lists/*

# Intel GPU tools for detection
RUN apt-get update && apt-get install -y --no-install-recommends \
    intel-gpu-tools \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /app/llamactl /usr/local/bin/llamactl

RUN mkdir -p /data/config /data/builds /data/models /data/llama.cpp

RUN cat > /data/config/llamactl.yaml <<'EOF'
listen_addr: ":3000"
data_dir: "/data"
llama_port: 8080
log_level: "info"
EOF

VOLUME ["/data"]
EXPOSE 3000
EXPOSE 8080

ENTRYPOINT ["llamactl", "--config", "/data/config/llamactl.yaml"]
```

Key differences:
- Base image: `intel/oneapi-basekit:2025.1-0-devel-ubuntu24.04` (provides icpx, oneMKL, SYCL runtime)
- oneAPI environment is pre-configured by the base image via `/opt/intel/oneapi/setvars.sh`
- Needs `intel-gpu-tools` for `intel_gpu_top` detection

---

### `docker-compose.rocm.yml` — Renamed from `docker-compose.yml`

Existing file renamed. No content changes.

---

### `docker-compose.cuda.yml` — New

```yaml
services:
  llamactl:
    build:
      context: .
      dockerfile: Dockerfile.cuda
    ports:
      - "3000:3000"
      - "8080:8080"
    volumes:
      - llamactl-data:/data:z
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: all
              capabilities: [gpu]
    restart: unless-stopped

volumes:
  llamactl-data:
```

Key differences from ROCm compose:
- No `/dev/kfd` or `/dev/dri` device mounts
- No `seccomp=unconfined` needed
- Uses Docker's native `deploy.resources.reservations.devices` for NVIDIA GPU access
- Requires `nvidia-container-toolkit` installed on the host

**Host prerequisite**: `nvidia-container-toolkit` must be installed and configured:
```bash
# Ubuntu/Debian
sudo apt install nvidia-container-toolkit
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker
```

---

### `docker-compose.intel.yml` — New

```yaml
services:
  llamactl:
    build:
      context: .
      dockerfile: Dockerfile.intel
    ports:
      - "3000:3000"
      - "8080:8080"
    volumes:
      - llamactl-data:/data:z
    devices:
      - /dev/dri:/dev/dri
    group_add:
      - render
      - video
    restart: unless-stopped

volumes:
  llamactl-data:
```

Key differences:
- Same DRI render node passthrough as AMD, but no `/dev/kfd`
- No `seccomp=unconfined` needed
- `render` and `video` group access for Intel GPU

---

### `internal/builder/detect.go` — Modified

Add CUDA and SYCL/oneAPI detection:

```go
func DetectBackends() []Backend {
    backends := []Backend{
        detectROCm(),
        detectCUDA(),
        detectSYCL(),
        detectVulkan(),
        {Name: "cpu", Available: true, Info: "CPU fallback (always available)"},
    }
    return backends
}

func detectCUDA() Backend {
    b := Backend{Name: "cuda"}

    out, err := exec.Command("nvidia-smi",
        "--query-gpu=name,compute_cap",
        "--format=csv,noheader,nounits").Output()
    if err != nil {
        b.Info = "nvidia-smi not found or failed"
        return b
    }

    for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
        parts := strings.SplitN(line, ",", 2)
        if len(parts) >= 1 {
            name := strings.TrimSpace(parts[0])
            b.GPUs = append(b.GPUs, name)
        }
    }

    if len(b.GPUs) > 0 {
        b.Available = true
        b.Info = strings.Join(b.GPUs, ", ")
    } else {
        b.Info = "nvidia-smi found but no GPUs detected"
    }
    return b
}

func detectSYCL() Backend {
    b := Backend{Name: "intel"}

    // Check for Intel's sycl-ls tool (part of oneAPI)
    out, err := exec.Command("sycl-ls").Output()
    if err != nil {
        b.Info = "sycl-ls not found or failed"
        return b
    }

    // Parse output for GPU devices (lines containing "gpu")
    for _, line := range strings.Split(string(out), "\n") {
        lower := strings.ToLower(line)
        if strings.Contains(lower, "gpu") {
            // Extract device name between brackets
            if start := strings.Index(line, "["); start != -1 {
                if end := strings.Index(line[start:], "]"); end != -1 {
                    name := line[start+1 : start+end]
                    b.GPUs = append(b.GPUs, name)
                }
            }
        }
    }

    if len(b.GPUs) > 0 {
        b.Available = true
        b.Info = strings.Join(b.GPUs, ", ")
    } else {
        b.Info = "oneAPI found but no GPU devices detected"
    }
    return b
}
```

`nvidia-smi --query-gpu` gives clean CSV output with GPU names and compute capabilities. `sycl-ls` is the standard oneAPI tool for enumerating SYCL devices.

---

### `internal/builder/profiles.go` — Modified

Add CUDA and Intel build profiles:

```go
func DefaultProfiles() []BuildProfile {
    // ... existing ROCm GPU target detection ...

    return []BuildProfile{
        // Existing ROCm profile (unchanged)
        {
            Name:    "rocm",
            Backend: "rocm",
            CMakeFlags: map[string]string{
                "GGML_HIP":                        "ON",
                "AMDGPU_TARGETS":                  gpuTargets,
                "CMAKE_BUILD_TYPE":                "Release",
                "LLAMA_HIP_UMA":                   "ON",
                "GGML_CUDA_ENABLE_UNIFIED_MEMORY": "ON",
            },
        },
        // NEW: CUDA profile
        {
            Name:    "cuda",
            Backend: "cuda",
            CMakeFlags: map[string]string{
                "GGML_CUDA":        "ON",
                "CMAKE_BUILD_TYPE": "Release",
            },
        },
        // NEW: Intel SYCL profile
        {
            Name:    "intel",
            Backend: "intel",
            CMakeFlags: map[string]string{
                "GGML_SYCL":          "ON",
                "GGML_SYCL_TARGET":   "INTEL",
                "CMAKE_BUILD_TYPE":   "Release",
                "CMAKE_C_COMPILER":   "icx",
                "CMAKE_CXX_COMPILER": "icpx",
            },
        },
        // Existing Vulkan and CPU profiles (unchanged)
        ...
    }
}
```

Notes:
- **CUDA**: `GGML_CUDA=ON` is all that's needed — cmake auto-detects the CUDA toolkit from the NVIDIA base image. For multi-GPU, llama.cpp handles it natively (same as ROCm with `--tensor-split`).
- **Intel SYCL**: Requires setting `icx`/`icpx` as compilers (from oneAPI). `GGML_SYCL_TARGET=INTEL` ensures Intel-specific optimizations. Multi-GPU on Intel also uses `--tensor-split`.

---

### `Makefile` — Modified

Replace the fixed docker targets with vendor-aware ones:

```makefile
# Auto-detect or override GPU vendor
GPU ?= $(shell ./setup.sh detect 2>/dev/null || echo "rocm")

# Container targets (vendor-aware)
docker:
	docker compose -f docker-compose.$(GPU).yml build

docker-rebuild:
	docker compose -f docker-compose.$(GPU).yml down
	docker compose -f docker-compose.$(GPU).yml build --no-cache
	docker compose -f docker-compose.$(GPU).yml up -d

docker-compose-up:
	docker compose -f docker-compose.$(GPU).yml up -d

docker-compose-down:
	docker compose -f docker-compose.$(GPU).yml down

docker-compose-logs:
	docker compose -f docker-compose.$(GPU).yml logs -f

# Convenience: explicit vendor targets
docker-rocm:    GPU=rocm    docker-rebuild
docker-cuda:    GPU=cuda    docker-rebuild
docker-intel:   GPU=intel   docker-rebuild
```

Users can override detection: `make docker-rebuild GPU=cuda`

---

## Implementation Order

### Step 1: File restructuring (no code changes)
- Rename `Dockerfile` → `Dockerfile.rocm`
- Rename `docker-compose.yml` → `docker-compose.rocm.yml`
- Create `setup.sh` with GPU detection
- Update Makefile for vendor-aware targets
- Verify existing AMD workflow still works

### Step 2: NVIDIA CUDA support
- Create `Dockerfile.cuda`
- Create `docker-compose.cuda.yml`
- Add `detectCUDA()` to `detect.go`
- Add CUDA profile to `profiles.go`
- Test on a machine with an NVIDIA GPU (or verify the build completes in the container)

### Step 3: Intel SYCL support
- Create `Dockerfile.intel`
- Create `docker-compose.intel.yml`
- Add `detectSYCL()` to `detect.go`
- Add Intel profile to `profiles.go`
- Test on a machine with an Intel GPU

### Step 4: Documentation and polish
- Update README with multi-vendor setup instructions
- Document host prerequisites per vendor (drivers, container toolkit)
- Add troubleshooting section for common GPU passthrough issues

---

## Host Prerequisites by Vendor

| Vendor | Host Driver | Container Toolkit | Kernel Modules |
|--------|-----------|------------------|---------------|
| AMD | `amdgpu` (in-kernel) | None needed | `amdgpu`, `amdkfd` |
| NVIDIA | NVIDIA proprietary driver (550+) | `nvidia-container-toolkit` | `nvidia`, `nvidia_uvm` |
| Intel | `i915` (in-kernel, Arc/Xe) | None needed | `i915`, `xe` |

AMD and Intel use kernel-native drivers with direct device passthrough. NVIDIA requires the proprietary driver and `nvidia-container-toolkit` to manage GPU device injection into containers.

---

## What You Can Do at End of Phase

- `./setup.sh` auto-detects GPU and brings up the correct container
- `./setup.sh rebuild` does a full no-cache rebuild
- `make docker-rebuild GPU=cuda` for explicit vendor targeting
- All three vendors share the same web UI, model management, and build pipeline
- Build profiles auto-detect GPU targets per vendor
- `--tensor-split` works for multi-GPU on all three vendors
