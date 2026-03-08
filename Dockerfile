# Stage 1: Build the Go binary
FROM golang:1.25-bookworm AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o llamactl ./cmd/llamactl

# Stage 2: Runtime — ROCm dev image
# Using ROCm dev image because llama.cpp builds happen inside
# the container and need the full SDK (cmake, hipcc, ROCm headers).
FROM rocm/dev-ubuntu-24.04:6.3

RUN apt-get update && apt-get install -y --no-install-recommends \
    cmake \
    ninja-build \
    git \
    curl \
    vulkan-tools \
    libvulkan-dev \
    hipblas-dev \
    rocblas-dev \
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

# LlamaCtl web UI
EXPOSE 3000
# llama-server inference + chat UI
EXPOSE 8080

ENTRYPOINT ["llamactl", "--config", "/data/config/llamactl.yaml"]
