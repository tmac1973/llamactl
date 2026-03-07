# Phase 6 — Container & Deployment

> Multi-stage Dockerfile, docker-compose, GPU passthrough, documentation

---

## Goal

Production-ready container deployment with a multi-stage Dockerfile, docker-compose for easy launch, AMD GPU passthrough, and persistent data volumes. Everything needed to go from `git clone` to running inference in minutes.

---

## Deliverables

- Multi-stage Dockerfile based on ROCm dev image
- `docker-compose.yml` with GPU passthrough and volume mounts
- Makefile targets for container builds
- Startup configuration and data directory initialization
- Deployment documentation

---

## Files Created / Modified

### `Dockerfile` — Rewritten

Multi-stage build using ROCm dev image as the final stage (since builds happen inside the container).

```dockerfile
# ============================================================
# Stage 1: Build the Go binary
# ============================================================
FROM golang:1.22-bookworm AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o llamactl ./cmd/llamactl

# ============================================================
# Stage 2: Runtime — ROCm dev image
# ============================================================
# Using ROCm dev image because llama.cpp builds happen inside
# the container and need the full SDK (cmake, hipcc, ROCm headers).
FROM rocm/dev-ubuntu-24.04:6.3

# Install build tools for llama.cpp compilation
RUN apt-get update && apt-get install -y --no-install-recommends \
    cmake \
    ninja-build \
    git \
    curl \
    vulkan-tools \
    libvulkan-dev \
    && rm -rf /var/lib/apt/lists/*

# Copy the Go binary
COPY --from=builder /app/llamactl /usr/local/bin/llamactl

# Create data directory structure
RUN mkdir -p /data/config /data/builds /data/models /data/llama.cpp

# Default config
RUN cat > /data/config/llamactl.yaml <<'EOF'
listen_addr: ":3000"
data_dir: "/data"
llama_port: 8080
log_level: "info"
EOF

VOLUME ["/data"]

# LlamaCtl web UI
EXPOSE 3000
# llama-server inference + chat UI
EXPOSE 8080

ENTRYPOINT ["llamactl", "--config", "/data/config/llamactl.yaml"]
```

Key decisions:
- **ROCm dev image**: Includes the full ROCm SDK (hipcc, ROCm headers, runtime libs) because llama.cpp builds happen inside the container. The dev image is larger (~8-10GB) but eliminates the need for a separate build sidecar or host-side compilation.
- **CGO_ENABLED=0**: The Go binary itself needs no C dependencies — it's a pure Go binary that shells out to cmake/ninja for builds and to the compiled llama-server for inference.
- **Two exposed ports**: 3000 for LlamaCtl's management UI, 8080 for llama-server's inference API + built-in chat UI.

### `docker-compose.yml`

```yaml
services:
  llamactl:
    build: .
    container_name: llamactl
    ports:
      - "3000:3000"   # LlamaCtl management UI
      - "8080:8080"   # llama-server inference + chat UI
    volumes:
      - llamactl-data:/data
    devices:
      - /dev/kfd:/dev/kfd       # AMD GPU kernel interface
      - /dev/dri:/dev/dri       # DRM render nodes
    group_add:
      - video                   # GPU access group
      - render                  # render node access (some distros)
    security_opt:
      - seccomp=unconfined      # required for ROCm
    environment:
      - HSA_OVERRIDE_GFX_VERSION=11.0.0   # may be needed for gfx1201 (RDNA4)
    restart: unless-stopped

volumes:
  llamactl-data:
    driver: local
```

Notes:
- `HSA_OVERRIDE_GFX_VERSION` is set as an env var for gfx1201 compatibility. Can be removed once ROCm fully supports RDNA4 without overrides.
- `seccomp=unconfined` is required by ROCm for GPU memory operations.
- The `video` and `render` groups grant access to GPU device nodes.

### `Makefile` — Updated

Add container targets:

```makefile
.PHONY: build run dev clean docker docker-run docker-compose-up

# Local development
build:
	go build -o bin/llamactl ./cmd/llamactl

run: build
	./bin/llamactl --config config.yaml

dev:
	go run ./cmd/llamactl --config config.yaml

clean:
	rm -rf bin/

# Container builds
docker:
	docker build -t llamactl .

docker-run: docker
	docker run -it --rm \
		-p 3000:3000 \
		-p 8080:8080 \
		-v llamactl-data:/data \
		--device /dev/kfd \
		--device /dev/dri \
		--group-add video \
		--group-add render \
		--security-opt seccomp=unconfined \
		llamactl

docker-compose-up:
	docker compose up -d

docker-compose-down:
	docker compose down

docker-compose-logs:
	docker compose logs -f
```

### `cmd/llamactl/main.go` — Modified

Add data directory initialization on startup:

```go
func initDataDir(dataDir string) error {
    dirs := []string{
        filepath.Join(dataDir, "config"),
        filepath.Join(dataDir, "builds"),
        filepath.Join(dataDir, "models"),
    }
    for _, dir := range dirs {
        if err := os.MkdirAll(dir, 0755); err != nil {
            return fmt.Errorf("creating %s: %w", dir, err)
        }
    }
    return nil
}
```

### Startup Sequence

When the container starts:

1. `llamactl` reads config from `/data/config/llamactl.yaml`
2. Creates data directories if they don't exist
3. Loads build metadata from `/data/config/builds.json`
4. Loads model registry from `/data/config/models.json`
5. Initializes the process manager (no llama-server started automatically)
6. Starts the HTTP server on `:3000`
7. User accesses `http://host:3000` to manage everything

---

## GPU Passthrough Details

### AMD ROCm

```
--device /dev/kfd        # Kernel Fusion Driver — required for ROCm compute
--device /dev/dri        # DRM render nodes — GPU device access
--group-add video        # group that owns /dev/kfd
--group-add render       # group that owns /dev/dri/renderD* (distro-dependent)
--security-opt seccomp=unconfined  # ROCm needs unrestricted syscalls
```

### Verifying GPU access inside container

```bash
# Should list both GPUs
docker exec llamactl rocminfo | grep "Name:" | grep "gfx"

# Should show GPU memory and utilization
docker exec llamactl rocm-smi
```

### RDNA4 / gfx1201 Considerations

- ROCm support for gfx1201 was added in ROCm 6.x
- `HSA_OVERRIDE_GFX_VERSION=11.0.0` may be needed if rocminfo doesn't fully recognize the GPU architecture
- The `AMDGPU_TARGETS=gfx1201` cmake flag in the build profile ensures llama.cpp compiles kernels for this architecture
- Test with `rocminfo` inside the container to verify GPU detection before triggering builds

---

## Volume Layout

```
/data/                          ← docker volume: llamactl-data
├── config/
│   ├── llamactl.yaml          # app configuration
│   ├── builds.json            # build metadata registry
│   └── models.json            # model + config registry
├── llama.cpp/                 # git-cloned source (managed by builder)
│   ├── .git/
│   └── ...
├── builds/
│   ├── rocm-abc1234/
│   │   └── llama-server       # compiled binary
│   └── vulkan-def5678/
│       └── llama-server
└── models/
    └── bartowski--Qwen2.5-72B-Instruct-GGUF--Q4_K_M/
        ├── model.gguf         # downloaded model file
        └── meta.json          # model metadata
```

All state is in `/data`. The container itself is stateless and can be rebuilt/upgraded without data loss.

---

## Deployment Scenarios

### Quick Start (docker-compose)

```bash
git clone https://github.com/tmlabonte/llamactl
cd llamactl
docker compose up -d
# Open http://localhost:3000
```

### Manual Docker Run

```bash
docker build -t llamactl .
docker run -d \
  --name llamactl \
  -p 3000:3000 \
  -p 8080:8080 \
  -v llamactl-data:/data \
  --device /dev/kfd \
  --device /dev/dri \
  --group-add video \
  --security-opt seccomp=unconfined \
  llamactl
```

### Using External Data Directory

```bash
# Use a host directory for data (e.g. for large model storage on a specific disk)
docker run -d \
  --name llamactl \
  -p 3000:3000 \
  -p 8080:8080 \
  -v /mnt/nvme/llamactl:/data \
  --device /dev/kfd \
  --device /dev/dri \
  --group-add video \
  --security-opt seccomp=unconfined \
  llamactl
```

---

## End-to-End Workflow

After `docker compose up`:

1. Open `http://localhost:3000` → LlamaCtl dashboard
2. Go to **Builds** → select ROCm backend, click "Start Build"
   - Watch llama.cpp compile with live log streaming
3. Go to **Browse HF** → search for a model (e.g. "Qwen2.5 72B GGUF")
   - See VRAM estimates, pick a quantization, click Download
4. Go to **Models** → click "Activate" on the downloaded model
   - llama-server starts as a subprocess with the configured parameters
5. Go to **Service** → verify it's running, watch logs
6. Open `http://localhost:8080` → llama-server's built-in chat UI
7. Or point any OpenAI client at `http://localhost:3000/v1/`
8. Go to **Settings** → optionally set an API key, test connectivity

---

## What You Can Do at End of Phase

- `docker compose up -d` → full stack running with GPU access
- All features from phases 1-5 work inside the container
- GPU passthrough verified with `rocminfo` and `rocm-smi`
- llama.cpp builds happen inside the container (full ROCm SDK available)
- Data persists across container restarts/rebuilds via volume
- Two ports exposed: 3000 (management) and 8080 (inference + chat)
- Production-ready deployment with a single `docker-compose.yml`
