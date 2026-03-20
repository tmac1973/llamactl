# Stage 1: Build the Go binary
FROM golang:1.25-bookworm AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o llamactl ./cmd/llamactl

# Stage 2: Runtime — Fedora 43 + ROCm 7.2
# Using full dev toolchain because llama.cpp builds happen inside
# the container and need cmake, hipcc, ROCm headers, etc.
FROM registry.fedoraproject.org/fedora:43

# ROCm 7.2 repo
RUN <<'EOF'
tee /etc/yum.repos.d/rocm.repo <<REPO
[ROCm-7.2]
name=ROCm7.2
baseurl=https://repo.radeon.com/rocm/el9/7.2/main
enabled=1
priority=50
gpgcheck=1
gpgkey=https://repo.radeon.com/rocm/rocm.gpg.key
REPO
EOF

# Dev tools + ROCm SDK
RUN --mount=type=cache,target=/var/cache/dnf \
    dnf -y --nodocs --setopt=install_weak_deps=False --setopt=keepcache=True \
  --exclude='*sdk*' --exclude='*samples*' --exclude='*-doc*' --exclude='*-docs*' \
  install \
  make gcc cmake lld clang clang-devel compiler-rt ninja-build \
  rocm-llvm rocm-device-libs hip-runtime-amd hip-devel \
  rocblas rocblas-devel hipblas hipblas-devel rocm-cmake libomp-devel libomp \
  rocminfo \
  git-core curl openssl-devel

# ROCm environment
ENV ROCM_PATH=/opt/rocm \
  HIP_PATH=/opt/rocm \
  HIP_CLANG_PATH=/opt/rocm/llvm/bin \
  HIP_DEVICE_LIB_PATH=/opt/rocm/amdgcn/bitcode \
  PATH=/opt/rocm/bin:/opt/rocm/llvm/bin:$PATH

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
