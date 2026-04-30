#!/usr/bin/env bash
set -euo pipefail

# ─────────────────────────────────────────────────────────────────────────────
# llama-toolchest setup — distro-agnostic, runtime-agnostic setup and launcher
# ─────────────────────────────────────────────────────────────────────────────

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly CDI_SYSTEM_DIR="/etc/cdi"
readonly CDI_USER_DIR="${HOME}/.config/containers/cdi"

# ─── Global state (populated by detect_* functions) ──────────────────────────

GPU_VENDOR=""           # cuda, rocm, cpu
GPU_INFO=""             # human-readable GPU description
AMD_GFX_VERSION=""      # HSA_OVERRIDE_GFX_VERSION value (empty = not needed)
HOST_VIDEO_GID=""       # host video group GID
HOST_RENDER_GID=""      # host render group GID

LLAMA_TOOLCHEST_PORT="3000"            # host port for management UI
LLAMA_TOOLCHEST_INFERENCE_PORT="8080"  # host port for inference API
LLAMA_TOOLCHEST_MODELS_DIR=""          # host path for model storage (empty = use docker volume)

CONTAINER_CMD=""        # docker or podman
COMPOSE_CMD=""          # "docker compose" or "podman-compose" or "podman compose"
CONTAINER_VERSION=""
COMPOSE_VERSION=""

INSTALL_MODE="${INSTALL_MODE:-container}"  # host or container; default container preserves existing behavior
HOST_INSTALL_MODE="${HOST_INSTALL_MODE:-package}"  # for --host: "package" (download released .deb/.rpm) or "source" (go build locally)

DISTRO_ID=""            # debian, ubuntu, fedora, arch, cachyos, opensuse-leap, etc.
DISTRO_NAME=""          # Pretty name from os-release
DISTRO_FAMILY=""        # debian, fedora, arch, suse
PKG_MANAGER=""          # apt, dnf, pacman, zypper

ACTIONS=()              # list of human-readable actions to perform
PREREQS=()             # list of prerequisite action keys

# ─── Utility ─────────────────────────────────────────────────────────────────

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log()   { echo -e "${BLUE}==>${NC} $*"; }
ok()    { echo -e "${GREEN}  ✓${NC} $*"; }
warn()  { echo -e "${YELLOW}  ⚠${NC} $*" >&2; }
err()   { echo -e "${RED}  ✗${NC} $*" >&2; }
fatal() { err "$@"; exit 1; }

need_cmd() {
    command -v "$1" &>/dev/null
}

run_sudo() {
    if [[ $EUID -eq 0 ]]; then
        "$@"
    else
        sudo "$@"
    fi
}

# ─── Detection: GPU ──────────────────────────────────────────────────────────

# Detect host video/render group GIDs for container device access.
# Container group_add needs the host's actual GIDs, not names,
# because the container's /etc/group may have different GID mappings.
detect_host_gpu_gids() {
    if need_cmd getent; then
        HOST_VIDEO_GID="$(getent group video 2>/dev/null | cut -d: -f3)" || true
        HOST_RENDER_GID="$(getent group render 2>/dev/null | cut -d: -f3)" || true
    fi
    # Fallback: try parsing /etc/group directly
    if [[ -z "$HOST_VIDEO_GID" ]]; then
        HOST_VIDEO_GID="$(grep '^video:' /etc/group 2>/dev/null | cut -d: -f3)" || true
    fi
    if [[ -z "$HOST_RENDER_GID" ]]; then
        HOST_RENDER_GID="$(grep '^render:' /etc/group 2>/dev/null | cut -d: -f3)" || true
    fi
}

# Detect the AMD GPU gfx target from sysfs and determine if
# HSA_OVERRIDE_GFX_VERSION is needed for ROCm compatibility.
detect_amd_gfx_version() {
    local gfx_target=""

    # Try rocminfo first (may not be installed on host)
    if need_cmd rocminfo; then
        gfx_target="$(rocminfo 2>/dev/null | grep -oP 'gfx\d+' | head -1)" || true
    fi

    # Fallback: read from sysfs ip_discovery or amdgpu firmware
    if [[ -z "$gfx_target" && -d /sys/class/drm ]]; then
        for card_dir in /sys/class/drm/card[0-9]*/device; do
            if [[ -f "$card_dir/vendor" && "$(cat "$card_dir/vendor")" == "0x1002" ]]; then
                # Try to read gfx target from pp_dpm_sclk or firmware info
                local fw_ver
                fw_ver="$(cat "$card_dir/gpu_id" 2>/dev/null)" || true
                break
            fi
        done
    fi

    [[ -z "$gfx_target" ]] && return

    # Map gfx target to HSA_OVERRIDE_GFX_VERSION
    # Only set the override for GPUs not natively supported by ROCm 7.2
    case "$gfx_target" in
        # RDNA 4 — natively supported in ROCm 7.2
        gfx1200|gfx1201)
            AMD_GFX_VERSION=""
            ;;
        # RDNA 3 — natively supported
        gfx1100|gfx1101|gfx1102|gfx1103)
            AMD_GFX_VERSION=""
            ;;
        # RDNA 2 — natively supported
        gfx1030|gfx1031|gfx1032|gfx1033|gfx1034|gfx1035|gfx1036)
            AMD_GFX_VERSION=""
            ;;
        # RDNA 1 — needs override
        gfx1010|gfx1011|gfx1012|gfx1013)
            AMD_GFX_VERSION="10.1.0"
            ;;
        # Vega — needs override
        gfx900|gfx902|gfx904|gfx906|gfx908|gfx909)
            AMD_GFX_VERSION="9.0.0"
            ;;
        *)
            # Unknown target — leave empty, let ROCm try natively
            AMD_GFX_VERSION=""
            ;;
    esac
}

detect_gpu() {
    # NVIDIA: check for nvidia-smi AND that it can talk to a GPU
    if need_cmd nvidia-smi; then
        if nvidia-smi --query-gpu=name --format=csv,noheader &>/dev/null; then
            GPU_VENDOR="cuda"
            GPU_INFO="$(nvidia-smi --query-gpu=name,driver_version --format=csv,noheader 2>/dev/null || true)"
            GPU_INFO="${GPU_INFO%%$'\n'*}"
            return
        fi
    fi
    # NVIDIA: fallback — device node exists but nvidia-smi missing/broken
    if [[ -e /dev/nvidia0 ]]; then
        GPU_VENDOR="cuda"
        GPU_INFO="NVIDIA GPU detected (nvidia-smi unavailable)"
        return
    fi

    # AMD: check for ROCm kernel driver
    if [[ -e /dev/kfd ]]; then
        GPU_VENDOR="rocm"
        GPU_INFO="AMD GPU"
        if need_cmd rocminfo; then
            local name
            # rocminfo lists CPU agents before GPU agents. Match GPU marketing
            # names (Radeon, Instinct, FirePro) rather than excluding CPU names,
            # so we don't break if AMD introduces new CPU branding.
            name="$(rocminfo 2>/dev/null | grep 'Marketing Name' | sed 's/.*: *//' \
                | grep -iE 'Radeon|Instinct|FirePro' | head -1)" || true
            [[ -n "$name" ]] && GPU_INFO="$name"
        elif [[ -d /sys/class/drm ]]; then
            for card_dir in /sys/class/drm/card[0-9]*/device; do
                if [[ -f "$card_dir/vendor" && "$(cat "$card_dir/vendor")" == "0x1002" ]]; then
                    GPU_INFO="AMD GPU ($(cat "$card_dir/device" 2>/dev/null || echo "unknown"))"
                    break
                fi
            done
        fi
        detect_amd_gfx_version
        detect_host_gpu_gids
        return
    fi

    GPU_VENDOR="cpu"
    GPU_INFO="No supported GPU detected"
}

# ─── Detection: Container runtime ────────────────────────────────────────────

detect_container_runtime() {
    local user_override="${RUNTIME:-}"

    if [[ -n "$user_override" ]]; then
        case "$user_override" in
            docker)
                need_cmd docker || fatal "RUNTIME=docker specified but docker is not installed"
                CONTAINER_CMD="docker"
                ;;
            podman)
                need_cmd podman || fatal "RUNTIME=podman specified but podman is not installed"
                CONTAINER_CMD="podman"
                ;;
            *)
                fatal "Unknown RUNTIME=$user_override (expected: docker or podman)"
                ;;
        esac
    else
        # Auto-detect: prefer docker if available, fall back to podman
        if need_cmd docker && docker info &>/dev/null 2>&1; then
            # Make sure it's real Docker, not podman emulating docker
            if docker --version 2>/dev/null | grep -qi podman; then
                CONTAINER_CMD="podman"
            else
                CONTAINER_CMD="docker"
            fi
        elif need_cmd podman; then
            CONTAINER_CMD="podman"
        else
            fatal "No container runtime found. Install Docker or Podman first."
        fi
    fi

    # Get version (use `read` to grab first line — avoids SIGPIPE with pipefail)
    CONTAINER_VERSION="$($CONTAINER_CMD --version 2>/dev/null || true)"
    CONTAINER_VERSION="${CONTAINER_VERSION%%$'\n'*}"

    # Detect compose command
    if [[ "$CONTAINER_CMD" == "docker" ]]; then
        if docker compose version &>/dev/null 2>&1; then
            COMPOSE_CMD="docker compose"
            COMPOSE_VERSION="$(docker compose version 2>/dev/null || true)"
        elif need_cmd docker-compose; then
            COMPOSE_CMD="docker-compose"
            COMPOSE_VERSION="$(docker-compose --version 2>/dev/null || true)"
        else
            fatal "Docker is installed but neither 'docker compose' plugin nor 'docker-compose' found"
        fi
    else
        # Podman: try podman compose (Podman 5+), then podman-compose
        if podman compose version &>/dev/null 2>&1; then
            COMPOSE_CMD="podman compose"
            COMPOSE_VERSION="$(podman compose version 2>/dev/null || true)"
        elif need_cmd podman-compose; then
            COMPOSE_CMD="podman-compose"
            COMPOSE_VERSION="$(podman-compose --version 2>/dev/null || true)"
        else
            fatal "Podman is installed but neither 'podman compose' nor 'podman-compose' found"
        fi
    fi
    COMPOSE_VERSION="${COMPOSE_VERSION%%$'\n'*}"
}

# ─── Detection: Linux distribution ───────────────────────────────────────────

detect_distro() {
    if [[ ! -f /etc/os-release ]]; then
        fatal "Cannot detect distribution: /etc/os-release not found"
    fi

    # shellcheck disable=SC1091
    source /etc/os-release

    DISTRO_ID="${ID:-unknown}"
    DISTRO_NAME="${PRETTY_NAME:-$DISTRO_ID}"
    local id_like="${ID_LIKE:-}"

    # Map to distro family and package manager
    case "$DISTRO_ID" in
        debian|ubuntu|pop|linuxmint|elementary|zorin|kali)
            DISTRO_FAMILY="debian"
            PKG_MANAGER="apt"
            ;;
        fedora|rhel|centos|rocky|alma|nobara)
            DISTRO_FAMILY="fedora"
            PKG_MANAGER="dnf"
            ;;
        arch|cachyos|endeavouros|manjaro|garuda|artix)
            DISTRO_FAMILY="arch"
            PKG_MANAGER="pacman"
            ;;
        opensuse-leap|opensuse-tumbleweed|sles)
            DISTRO_FAMILY="suse"
            PKG_MANAGER="zypper"
            ;;
        *)
            # Fall back to ID_LIKE
            if [[ "$id_like" == *"debian"* || "$id_like" == *"ubuntu"* ]]; then
                DISTRO_FAMILY="debian"
                PKG_MANAGER="apt"
            elif [[ "$id_like" == *"fedora"* || "$id_like" == *"rhel"* ]]; then
                DISTRO_FAMILY="fedora"
                PKG_MANAGER="dnf"
            elif [[ "$id_like" == *"arch"* ]]; then
                DISTRO_FAMILY="arch"
                PKG_MANAGER="pacman"
            elif [[ "$id_like" == *"suse"* ]]; then
                DISTRO_FAMILY="suse"
                PKG_MANAGER="zypper"
            else
                warn "Unknown distro: $DISTRO_ID (ID_LIKE=$id_like)"
                warn "Will skip automatic prerequisite installation"
                DISTRO_FAMILY="unknown"
                PKG_MANAGER=""
            fi
            ;;
    esac
}

# ─── Prerequisite checks ─────────────────────────────────────────────────────

has_nvidia_toolkit() {
    need_cmd nvidia-ctk
}

has_cdi_spec() {
    # Check both system and user CDI directories
    [[ -f "$CDI_SYSTEM_DIR/nvidia.yaml" ]] || [[ -f "$CDI_USER_DIR/nvidia.yaml" ]]
}

docker_has_nvidia_runtime() {
    docker info 2>/dev/null | grep -qi "nvidia"
}

selinux_enforcing() {
    need_cmd getenforce && [[ "$(getenforce 2>/dev/null)" == "Enforcing" ]]
}

selinux_device_bool_set() {
    need_cmd getsebool && getsebool container_use_devices 2>/dev/null | grep -q "on"
}

check_prerequisites() {
    PREREQS=()
    ACTIONS=()

    if [[ "$GPU_VENDOR" == "cuda" ]]; then
        # NVIDIA Container Toolkit is needed for both Docker and Podman
        if ! has_nvidia_toolkit; then
            PREREQS+=("install_nvidia_toolkit")
            ACTIONS+=("Install NVIDIA Container Toolkit")
        fi

        if [[ "$CONTAINER_CMD" == "docker" ]]; then
            if has_nvidia_toolkit && ! docker_has_nvidia_runtime; then
                PREREQS+=("configure_docker_nvidia")
                ACTIONS+=("Configure Docker NVIDIA runtime + restart Docker daemon")
            elif ! has_nvidia_toolkit; then
                # Will need to configure after install
                PREREQS+=("configure_docker_nvidia")
                ACTIONS+=("Configure Docker NVIDIA runtime + restart Docker daemon")
            fi
        fi

        if [[ "$CONTAINER_CMD" == "podman" ]]; then
            if ! has_cdi_spec; then
                PREREQS+=("generate_cdi_spec")
                ACTIONS+=("Generate NVIDIA CDI spec for Podman")
            fi
        fi
    fi

    # SELinux: needed for ROCm device access on Fedora/RHEL
    if [[ "$GPU_VENDOR" == "rocm" ]] && selinux_enforcing && ! selinux_device_bool_set; then
        PREREQS+=("selinux_device_bool")
        ACTIONS+=("Enable SELinux container_use_devices boolean")
    fi

    # Build + run is always an action
    ACTIONS+=("Build container image (Dockerfile.${GPU_VENDOR})")
    ACTIONS+=("Start llama-toolchest service")
}

# ─── Prerequisite installation ────────────────────────────────────────────────

install_nvidia_toolkit_apt() {
    log "Adding NVIDIA Container Toolkit apt repository..."
    # Install prerequisites for adding repos
    run_sudo apt-get update -qq
    run_sudo apt-get install -y -qq curl gpg

    # Add NVIDIA GPG key and repo
    curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey \
        | run_sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
    curl -fsSL https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list \
        | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' \
        | run_sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list > /dev/null

    run_sudo apt-get update -qq
    run_sudo apt-get install -y nvidia-container-toolkit
    ok "NVIDIA Container Toolkit installed"
}

install_nvidia_toolkit_dnf() {
    log "Adding NVIDIA Container Toolkit dnf repository..."
    curl -fsSL https://nvidia.github.io/libnvidia-container/stable/rpm/nvidia-container-toolkit.repo \
        | run_sudo tee /etc/yum.repos.d/nvidia-container-toolkit.repo > /dev/null

    run_sudo dnf install -y nvidia-container-toolkit
    ok "NVIDIA Container Toolkit installed"
}

install_nvidia_toolkit_pacman() {
    log "Installing NVIDIA Container Toolkit via pacman..."
    run_sudo pacman -Sy --noconfirm nvidia-container-toolkit
    ok "NVIDIA Container Toolkit installed"
}

install_nvidia_toolkit_zypper() {
    log "Adding NVIDIA Container Toolkit zypper repository..."
    run_sudo zypper ar -f \
        https://nvidia.github.io/libnvidia-container/stable/rpm/nvidia-container-toolkit.repo \
        nvidia-container-toolkit 2>/dev/null || true

    run_sudo zypper --gpg-auto-import-keys install -y nvidia-container-toolkit
    ok "NVIDIA Container Toolkit installed"
}

install_nvidia_toolkit() {
    case "$PKG_MANAGER" in
        apt)    install_nvidia_toolkit_apt ;;
        dnf)    install_nvidia_toolkit_dnf ;;
        pacman) install_nvidia_toolkit_pacman ;;
        zypper) install_nvidia_toolkit_zypper ;;
        *)      fatal "Cannot install NVIDIA Container Toolkit: unsupported package manager" ;;
    esac
}

configure_docker_nvidia() {
    log "Configuring Docker NVIDIA runtime..."
    run_sudo nvidia-ctk runtime configure --runtime=docker
    log "Restarting Docker daemon..."
    run_sudo systemctl restart docker
    ok "Docker NVIDIA runtime configured"
}

generate_cdi_spec() {
    log "Generating NVIDIA CDI spec..."
    run_sudo mkdir -p "$CDI_SYSTEM_DIR"
    run_sudo nvidia-ctk cdi generate --output="$CDI_SYSTEM_DIR/nvidia.yaml"
    ok "CDI spec written to $CDI_SYSTEM_DIR/nvidia.yaml"

    # Verify
    if need_cmd nvidia-ctk; then
        log "Verifying CDI devices..."
        nvidia-ctk cdi list 2>/dev/null | head -5 || true
    fi
}

selinux_device_bool() {
    log "Enabling SELinux container_use_devices..."
    run_sudo setsebool -P container_use_devices 1
    ok "SELinux boolean set"
}

install_prerequisites() {
    for prereq in "${PREREQS[@]}"; do
        case "$prereq" in
            install_nvidia_toolkit)  install_nvidia_toolkit ;;
            configure_docker_nvidia) configure_docker_nvidia ;;
            generate_cdi_spec)       generate_cdi_spec ;;
            selinux_device_bool)     selinux_device_bool ;;
            *)                       warn "Unknown prerequisite: $prereq" ;;
        esac
    done
}

# ─── Port configuration ───────────────────────────────────────────────────────

is_port_available() {
    local port="$1"
    # Check if something is already listening on the port
    if need_cmd ss; then
        ! ss -tlnH "sport = :${port}" 2>/dev/null | grep -q .
    elif need_cmd netstat; then
        ! netstat -tln 2>/dev/null | grep -q ":${port} "
    else
        # Can't check — assume available
        return 0
    fi
}

# Returns 0 if the host port is bound by one of our own containers — i.e.,
# a llamactl (pre-rename) or llama-toolchest container that the install
# flow is going to stop and replace anyway. Lets prompt_ports treat such
# bindings as "not really a conflict" instead of asking the user to pick
# different ports.
is_port_held_by_our_container() {
    local port="$1"
    [[ -z "$CONTAINER_CMD" ]] && return 1
    for cname in llamactl llama-toolchest; do
        $CONTAINER_CMD container exists "$cname" 2>/dev/null || continue
        # `docker/podman port <c>` outputs lines like "3000/tcp -> 0.0.0.0:3001".
        # Match the trailing :PORT to confirm this container is the binder.
        local mappings
        mappings="$($CONTAINER_CMD port "$cname" 2>/dev/null)" || continue
        if echo "$mappings" | grep -qE ":${port}(\s|$)"; then
            return 0
        fi
    done
    return 1
}

prompt_ports() {
    echo ""
    echo -e "${BOLD}Port configuration${NC}"
    echo ""
    echo "  Current ports:"
    echo "    Management UI:  ${LLAMA_TOOLCHEST_PORT}"
    echo "    Inference API:  ${LLAMA_TOOLCHEST_INFERENCE_PORT}"
    echo ""

    # Check if current ports are available. If a port is bound by an existing
    # llama-toolchest (or pre-rename llamactl) container, that container is
    # going to be stopped+removed during install, so it's not a real conflict.
    local ports_ok=true
    for cfg in "UI:$LLAMA_TOOLCHEST_PORT" "Inference:$LLAMA_TOOLCHEST_INFERENCE_PORT"; do
        local label="${cfg%%:*}"
        local p="${cfg##*:}"
        if is_port_available "$p"; then
            continue
        fi
        if is_port_held_by_our_container "$p"; then
            log "Port ${p} is held by an existing llama-toolchest container; it'll be replaced during install."
        else
            warn "Port ${p} (${label}) is already in use by another process"
            ports_ok=false
        fi
    done

    if [[ "$ports_ok" == true ]]; then
        if prompt_confirm "Use these ports?"; then
            return
        fi
    else
        echo ""
        echo "  One or more ports are in use by something other than llama-toolchest."
        echo "  Pick alternative ports, or stop the offending process and re-run."
    fi

    echo ""
    local port
    while true; do
        read -rp "$(echo -e "  ${BOLD}Management UI port${NC} [${LLAMA_TOOLCHEST_PORT}]: ")" port
        port="${port:-$LLAMA_TOOLCHEST_PORT}"
        if [[ "$port" =~ ^[0-9]+$ ]] && (( port >= 1 && port <= 65535 )); then
            LLAMA_TOOLCHEST_PORT="$port"
            break
        fi
        err "Invalid port number: $port"
    done

    while true; do
        read -rp "$(echo -e "  ${BOLD}Inference API port${NC} [${LLAMA_TOOLCHEST_INFERENCE_PORT}]: ")" port
        port="${port:-$LLAMA_TOOLCHEST_INFERENCE_PORT}"
        if [[ "$port" =~ ^[0-9]+$ ]] && (( port >= 1 && port <= 65535 )); then
            if [[ "$port" == "$LLAMA_TOOLCHEST_PORT" ]]; then
                err "Cannot use the same port as management UI ($LLAMA_TOOLCHEST_PORT)"
                continue
            fi
            LLAMA_TOOLCHEST_INFERENCE_PORT="$port"
            break
        fi
        err "Invalid port number: $port"
    done
}

prompt_models_dir() {
    echo ""
    echo -e "${BOLD}Model storage${NC}"
    echo ""
    if [[ -n "$LLAMA_TOOLCHEST_MODELS_DIR" ]]; then
        echo "  Current: ${LLAMA_TOOLCHEST_MODELS_DIR} (host directory)"
    else
        echo "  Current: Docker volume (default)"
    fi
    echo ""
    echo "  Mount a host directory so models persist even if the"
    echo "  container volume is removed."
    echo ""

    local path
    read -rp "$(echo -e "  ${BOLD}Host path${NC} [${LLAMA_TOOLCHEST_MODELS_DIR:-none}]: ")" path

    if [[ -z "$path" ]]; then
        # Keep current setting (or none)
        return
    fi

    if [[ "$path" == "none" || "$path" == "-" ]]; then
        LLAMA_TOOLCHEST_MODELS_DIR=""
        echo "  → Models will use Docker volume"
        return
    fi

    # Expand ~ to home directory
    path="${path/#\~/$HOME}"

    # Resolve to absolute path
    if [[ "$path" != /* ]]; then
        path="$(cd "$SCRIPT_DIR" && realpath -m "$path" 2>/dev/null || echo "$SCRIPT_DIR/$path")"
    fi

    # Create if it doesn't exist
    if [[ ! -d "$path" ]]; then
        log "Creating directory: $path"
        mkdir -p "$path" || { err "Cannot create $path"; return; }
    fi

    LLAMA_TOOLCHEST_MODELS_DIR="$path"
    export LLAMA_TOOLCHEST_MODELS_DIR
    echo "  → Models will be stored at: $path"
}

load_env_ports() {
    local env_file="${SCRIPT_DIR}/.env"
    [[ ! -f "$env_file" ]] && return 0

    # Legacy rename: pre-rebrand .env files used LLAMACTL_*. Detect and rewrite
    # in place so the user's port and models-dir customizations carry over.
    if grep -qE '^LLAMACTL_(PORT|INFERENCE_PORT|MODELS_DIR|HOST)=' "$env_file" 2>/dev/null; then
        log "Migrating legacy LLAMACTL_* vars in .env to LLAMA_TOOLCHEST_*..."
        sed -i \
            -e 's/^LLAMACTL_PORT=/LLAMA_TOOLCHEST_PORT=/' \
            -e 's/^LLAMACTL_INFERENCE_PORT=/LLAMA_TOOLCHEST_INFERENCE_PORT=/' \
            -e 's/^LLAMACTL_MODELS_DIR=/LLAMA_TOOLCHEST_MODELS_DIR=/' \
            -e 's/^LLAMACTL_HOST=/LLAMA_TOOLCHEST_HOST=/' \
            "$env_file"
    fi

    local val
    val="$(grep '^LLAMA_TOOLCHEST_PORT=' "$env_file" 2>/dev/null | cut -d= -f2)" || true
    [[ -n "$val" ]] && LLAMA_TOOLCHEST_PORT="$val" || true
    val="$(grep '^LLAMA_TOOLCHEST_INFERENCE_PORT=' "$env_file" 2>/dev/null | cut -d= -f2)" || true
    [[ -n "$val" ]] && LLAMA_TOOLCHEST_INFERENCE_PORT="$val" || true
    val="$(grep '^LLAMA_TOOLCHEST_MODELS_DIR=' "$env_file" 2>/dev/null | cut -d= -f2)" || true
    [[ -n "$val" ]] && LLAMA_TOOLCHEST_MODELS_DIR="$val" || true
}

# Legacy rename: copy contents of pre-rebrand 'llamactl-data' Docker volume
# into the new 'llama-toolchest-data' volume so the renamed container starts
# with the user's existing models, builds, and config. No-op once the new
# volume exists. Called from install/rebuild/quick before the new container
# is started.
migrate_legacy_volume() {
    local old_vol="llamactl-data"
    local new_vol="llama-toolchest-data"

    [[ -z "$CONTAINER_CMD" ]] && return 0

    # Old volume gone → nothing to do (fresh install or already migrated).
    if ! $CONTAINER_CMD volume inspect "$old_vol" >/dev/null 2>&1; then
        return 0
    fi

    # New volume already there → assume migration happened previously, or the
    # user has both manually. Leave both alone; warn so they know.
    if $CONTAINER_CMD volume inspect "$new_vol" >/dev/null 2>&1; then
        warn "Both '$old_vol' and '$new_vol' volumes exist; using '$new_vol' as-is and leaving '$old_vol' untouched."
        return 0
    fi

    echo ""
    warn "Detected legacy Docker volume '$old_vol' from the pre-rename project."
    log "Will copy its contents into '$new_vol' so the renamed container keeps your existing models, builds, and configs."
    log "Disk usage will roughly double during the copy. The old volume stays in place — remove it manually after confirming the new install works."
    echo ""
    if ! prompt_confirm "Migrate volume now?"; then
        err "Migration is required to continue. Run './setup.sh install' again when ready, or remove '$old_vol' manually if you don't need its contents."
        exit 1
    fi

    # Stop and remove any container that may be holding the volume open OR
    # binding the inference port. We handle three legacy container names:
    #   - llamactl: original pre-rename container
    #   - llama-toolchest: post-rename container that may be running an old
    #     image (binary inside still calls itself llamactl) against the
    #     pre-rename volume. Common state for users who rebuilt their
    #     container partway through the rename.
    # Also strip any pre-rename podman Quadlet unit so systemd doesn't
    # bring the old container back on next boot to fight the new one.
    local old_quadlet_user="${HOME}/.config/containers/systemd/llamactl.container"
    local old_quadlet_sys="/etc/containers/systemd/llamactl.container"
    for unit in "$old_quadlet_user" "$old_quadlet_sys"; do
        if [[ -f "$unit" ]]; then
            log "Removing legacy Quadlet unit: $unit"
            if [[ "$unit" == "$old_quadlet_sys" ]]; then
                sudo systemctl stop llamactl.service 2>/dev/null || true
                sudo rm -f "$unit"
                sudo systemctl daemon-reload 2>/dev/null || true
            else
                systemctl --user stop llamactl.service 2>/dev/null || true
                rm -f "$unit"
                systemctl --user daemon-reload 2>/dev/null || true
            fi
        fi
    done

    for cname in llamactl llama-toolchest; do
        if $CONTAINER_CMD container exists "$cname" 2>/dev/null; then
            log "Stopping and removing existing container '$cname'..."
            $CONTAINER_CMD stop "$cname" >/dev/null 2>&1 || true
            $CONTAINER_CMD rm "$cname" >/dev/null 2>&1 || true
        fi
    done

    log "Creating new volume $new_vol..."
    $CONTAINER_CMD volume create "$new_vol" >/dev/null

    log "Copying contents (can take several minutes for large model collections)..."
    if ! $CONTAINER_CMD run --rm \
        -v "${old_vol}:/from:ro" \
        -v "${new_vol}:/to" \
        docker.io/library/alpine:3 \
        sh -c "set -e; \
               cp -a /from/. /to/; \
               if [ -f /to/config/llamactl.yaml ] && [ ! -f /to/config/llama-toolchest.yaml ]; then \
                   mv /to/config/llamactl.yaml /to/config/llama-toolchest.yaml; \
                   echo 'Renamed config llamactl.yaml -> llama-toolchest.yaml'; \
               fi"; then
        err "Volume copy failed. The partially-populated '$new_vol' can be removed with:"
        echo "    $CONTAINER_CMD volume rm $new_vol"
        exit 1
    fi

    ok "Volume migrated."
    log "The old volume '$old_vol' is preserved. Once you've confirmed the new install works, reclaim its disk with:"
    echo "    $CONTAINER_CMD volume rm $old_vol"
    echo ""
}

# ─── Container operations ────────────────────────────────────────────────────

compose_file() {
    echo "docker-compose.${GPU_VENDOR}.yml"
}

# compose_cmd builds the full compose command with all required -f flags.
compose_cmd() {
    local cmd="$COMPOSE_CMD -f $(compose_file)"
    # Add models volume override if a host directory is configured
    if [[ -n "${LLAMA_TOOLCHEST_MODELS_DIR:-}" ]]; then
        cmd+=" -f docker-compose.models.yml"
    fi
    echo "$cmd"
}

dockerfile() {
    echo "Dockerfile.${GPU_VENDOR}"
}

has_quadlet() {
    [[ "$CONTAINER_CMD" == "podman" && $EUID -ne 0 ]] \
        && [[ -f "${QUADLET_USER_DIR}/${PODMAN_SERVICE_NAME}.container" ]]
}

# Write .env file for docker-compose variable substitution
write_env_file() {
    local env_file="${SCRIPT_DIR}/.env"
    : > "$env_file"

    # Port configuration
    echo "LLAMA_TOOLCHEST_PORT=${LLAMA_TOOLCHEST_PORT}" >> "$env_file"
    echo "LLAMA_TOOLCHEST_INFERENCE_PORT=${LLAMA_TOOLCHEST_INFERENCE_PORT}" >> "$env_file"

    # Model storage — bind-mount a host directory so models survive volume removal
    if [[ -n "$LLAMA_TOOLCHEST_MODELS_DIR" ]]; then
        echo "LLAMA_TOOLCHEST_MODELS_DIR=${LLAMA_TOOLCHEST_MODELS_DIR}" >> "$env_file"
        export LLAMA_TOOLCHEST_MODELS_DIR
    fi

    # GPU-specific settings
    if [[ -n "$AMD_GFX_VERSION" ]]; then
        echo "HSA_OVERRIDE_GFX_VERSION=${AMD_GFX_VERSION}" >> "$env_file"
    fi
    if [[ -n "$HOST_VIDEO_GID" ]]; then
        echo "HOST_VIDEO_GID=${HOST_VIDEO_GID}" >> "$env_file"
    fi
    if [[ -n "$HOST_RENDER_GID" ]]; then
        echo "HOST_RENDER_GID=${HOST_RENDER_GID}" >> "$env_file"
    fi
}

container_up() {
    if has_quadlet; then
        log "Starting llama-toolchest via systemd (Quadlet)..."
        systemctl_cmd start "${PODMAN_SERVICE_NAME}.service"
    else
        $(compose_cmd) up -d
    fi
}

container_down() {
    if has_quadlet; then
        log "Stopping llama-toolchest via systemd (Quadlet)..."
        systemctl_cmd stop "${PODMAN_SERVICE_NAME}.service"
    else
        $(compose_cmd) down
    fi
}

container_install() {
    migrate_legacy_volume
    write_env_file
    $(compose_cmd) up -d --build
}

container_rebuild() {
    local quadlet_active=false
    has_quadlet && quadlet_active=true

    migrate_legacy_volume
    container_down
    $CONTAINER_CMD rm llama-toolchest 2>/dev/null || true
    write_env_file
    $(compose_cmd) build --no-cache

    if [[ "$quadlet_active" == true ]]; then
        log "Starting via systemd (Quadlet)..."
        systemctl_cmd start "${PODMAN_SERVICE_NAME}.service"
    else
        $(compose_cmd) up -d
    fi
}

# Quick rebuild: only rebuild layers that changed (Go code), reuse cached base layers.
container_quick_rebuild() {
    migrate_legacy_volume
    container_down
    write_env_file
    $(compose_cmd) up -d --build
}

container_logs() {
    if has_quadlet; then
        journalctl --user -u "${PODMAN_SERVICE_NAME}.service" -n 100 -f
    else
        $(compose_cmd) logs -f
    fi
}

# ─── Auto-start (enable/disable) ─────────────────────────────────────────────

readonly QUADLET_USER_DIR="${HOME}/.config/containers/systemd"
readonly QUADLET_SYSTEM_DIR="/etc/containers/systemd"
readonly PODMAN_SERVICE_NAME="llama-toolchest"

quadlet_dir() {
    if [[ $EUID -eq 0 ]]; then
        echo "$QUADLET_SYSTEM_DIR"
    else
        echo "$QUADLET_USER_DIR"
    fi
}

systemctl_cmd() {
    if [[ $EUID -eq 0 ]]; then
        systemctl "$@"
    else
        systemctl --user "$@"
    fi
}

# Get the restart policy for the container
get_restart_policy() {
    $CONTAINER_CMD inspect --format '{{.HostConfig.RestartPolicy.Name}}' llama-toolchest 2>/dev/null || echo ""
}

is_autostart_enabled() {
    if [[ "$CONTAINER_CMD" == "docker" ]]; then
        # Docker: check if the container has a restart policy that auto-starts
        local policy
        policy="$(get_restart_policy)"
        [[ "$policy" == "always" || "$policy" == "unless-stopped" ]]
    else
        # Podman rootless needs a Quadlet unit to survive reboot
        # Podman rootful can use restart policy like Docker
        if [[ $EUID -eq 0 ]]; then
            local policy
            policy="$(get_restart_policy)"
            [[ "$policy" == "always" || "$policy" == "unless-stopped" ]]
        else
            [[ -f "$(quadlet_dir)/${PODMAN_SERVICE_NAME}.container" ]]
        fi
    fi
}

get_volume_name() {
    # Get the actual volume name from the running container, or fall back to compose default
    $CONTAINER_CMD inspect --format '{{range .Mounts}}{{.Name}}{{end}}' llama-toolchest 2>/dev/null \
        || echo "llama-toolchest-data"
}

generate_quadlet() {
    local image_name="localhost/llama-toolchest:latest"
    local volume_name
    volume_name="$(get_volume_name)"
    local gpu_args=""

    if [[ "$GPU_VENDOR" == "cuda" ]]; then
        gpu_args="AddDevice=nvidia.com/gpu=all"
    elif [[ "$GPU_VENDOR" == "rocm" ]]; then
        local hsa_env=""
        if [[ -n "$AMD_GFX_VERSION" ]]; then
            hsa_env="Environment=HSA_OVERRIDE_GFX_VERSION=${AMD_GFX_VERSION}"
        fi
        gpu_args="AddDevice=/dev/kfd
AddDevice=/dev/dri
SecurityLabelDisable=true
PodmanArgs=--ipc=host
GroupAdd=${HOST_VIDEO_GID:-video}
GroupAdd=${HOST_RENDER_GID:-render}
${hsa_env}"
    fi

    cat <<EOF
# Auto-generated by llama-toolchest setup.sh
# GPU backend: ${GPU_VENDOR}
# Runtime: ${CONTAINER_CMD}

[Unit]
Description=llama-toolchest - local LLM management
After=network-online.target

[Container]
Image=${image_name}
ContainerName=llama-toolchest
PublishPort=${LLAMA_TOOLCHEST_PORT}:3000
PublishPort=${LLAMA_TOOLCHEST_INFERENCE_PORT}:8080
Volume=${volume_name}:/data:z
${gpu_args}

[Service]
Restart=on-failure
TimeoutStartSec=900

[Install]
WantedBy=default.target
EOF
}

autostart_enable() {
    if is_autostart_enabled; then
        ok "Auto-start is already enabled"
        return
    fi

    if [[ "$CONTAINER_CMD" == "docker" ]]; then
        # Docker: set restart policy on the container
        if ! docker container exists llama-toolchest 2>/dev/null; then
            # docker doesn't have "container exists" — check via inspect
            if ! docker inspect llama-toolchest &>/dev/null; then
                fatal "Container 'llama-toolchest' not found. Run './setup.sh install' first."
            fi
        fi
        log "Setting restart policy to 'unless-stopped'..."
        docker update --restart unless-stopped llama-toolchest
        ok "Auto-start enabled"
    elif [[ $EUID -eq 0 ]]; then
        # Podman rootful: restart policy works like Docker
        if ! podman container exists llama-toolchest 2>/dev/null; then
            fatal "Container 'llama-toolchest' not found. Run './setup.sh install' first."
        fi
        log "Setting restart policy to 'unless-stopped'..."
        podman update --restart unless-stopped llama-toolchest
        ok "Auto-start enabled"
    else
        # Podman rootless: need Quadlet + linger to survive reboot
        local qdir
        qdir="$(quadlet_dir)"

        # If the container is already running outside of systemd, stop it first
        # so the Quadlet service can take over without a name conflict.
        local was_running=false
        if podman container exists llama-toolchest 2>/dev/null; then
            was_running=true
            log "Stopping existing container so systemd can take over..."
            podman stop llama-toolchest 2>/dev/null || true
            podman rm llama-toolchest 2>/dev/null || true
        fi

        log "Installing Quadlet unit: ${qdir}/${PODMAN_SERVICE_NAME}.container"

        mkdir -p "$qdir"
        generate_quadlet > "${qdir}/${PODMAN_SERVICE_NAME}.container"

        systemctl_cmd daemon-reload

        # Enable lingering so user services run without an active login session
        local linger_status
        linger_status="$(loginctl show-user "$USER" --property=Linger 2>/dev/null || true)"
        if [[ "$linger_status" != *"yes"* ]]; then
            log "Enabling loginctl linger for user $USER..."
            if ! loginctl enable-linger "$USER" 2>/dev/null; then
                run_sudo loginctl enable-linger "$USER"
            fi
        fi

        # Start the service now if the container was previously running
        if [[ "$was_running" == true ]]; then
            log "Starting llama-toolchest via systemd..."
            systemctl_cmd start "${PODMAN_SERVICE_NAME}.service"
        fi

        # Quadlet units are auto-activated by systemd's generator via
        # WantedBy= in the [Install] section — no explicit enable needed.
        ok "Auto-start enabled via Podman Quadlet"

        echo ""
        echo "  llama-toolchest will auto-start on boot."
        echo "  Manage with: systemctl --user {start,stop,status} ${PODMAN_SERVICE_NAME}"
    fi
}

autostart_disable() {
    if ! is_autostart_enabled; then
        ok "Auto-start is already disabled"
        return
    fi

    if [[ "$CONTAINER_CMD" == "docker" ]]; then
        log "Setting restart policy to 'no'..."
        docker update --restart no llama-toolchest
        ok "Auto-start disabled"
    elif [[ $EUID -eq 0 ]]; then
        log "Setting restart policy to 'no'..."
        podman update --restart no llama-toolchest
        ok "Auto-start disabled"
    else
        local qdir
        qdir="$(quadlet_dir)"

        log "Removing Quadlet unit: ${qdir}/${PODMAN_SERVICE_NAME}.container"

        # Stop the service, remove the Quadlet file, then reload so
        # systemd's generator drops the unit entirely.
        systemctl_cmd stop "${PODMAN_SERVICE_NAME}.service" 2>/dev/null || true
        rm -f "${qdir}/${PODMAN_SERVICE_NAME}.container"
        systemctl_cmd daemon-reload
        ok "Auto-start disabled"
    fi
}

# ─── Uninstall ────────────────────────────────────────────────────────────────

container_uninstall() {
    local actions=()
    local has_autostart=false
    local has_container=false
    local has_image=false
    local image_name="localhost/llama-toolchest:latest"

    # Check what exists
    if is_autostart_enabled; then
        has_autostart=true
        actions+=("Disable auto-start on boot")
    fi

    if $CONTAINER_CMD container exists llama-toolchest 2>/dev/null; then
        has_container=true
        actions+=("Stop and remove container 'llama-toolchest'")
    fi

    if $CONTAINER_CMD image exists "$image_name" 2>/dev/null; then
        has_image=true
        actions+=("Remove image '${image_name}'")
    fi

    if [[ ${#actions[@]} -eq 0 ]]; then
        ok "Nothing to uninstall — llama-toolchest is not installed"
        return
    fi

    echo ""
    echo -e "${BOLD}The following will be removed:${NC}"
    echo ""
    local i=1
    for action in "${actions[@]}"; do
        echo -e "  ${i}. ${action}"
        ((i++))
    done
    echo ""
    # Compose prefixes volume names with the project directory name
    local volume_name
    volume_name="$($CONTAINER_CMD inspect --format '{{range .Mounts}}{{.Name}}{{end}}' llama-toolchest 2>/dev/null || echo "llama-toolchest-data")"
    echo -e "  ${YELLOW}Note:${NC} The data volume (models, builds, config) will be kept."
    echo -e "        To remove it: ${CONTAINER_CMD} volume rm ${volume_name}"
    echo ""

    if ! prompt_confirm "Proceed with uninstall?"; then
        echo "Aborted."
        exit 0
    fi

    echo ""

    if [[ "$has_autostart" == true ]]; then
        autostart_disable
    fi

    if [[ "$has_container" == true ]]; then
        log "Stopping and removing container..."
        $CONTAINER_CMD stop llama-toolchest 2>/dev/null || true
        $CONTAINER_CMD rm llama-toolchest 2>/dev/null || true
        ok "Container removed"
    fi

    if [[ "$has_image" == true ]]; then
        log "Removing image..."
        $CONTAINER_CMD rmi "$image_name" 2>/dev/null || true
        ok "Image removed"
    fi

    echo ""
    ok "llama-toolchest uninstalled"
}

# ─── Summary and confirmation ─────────────────────────────────────────────────

print_summary() {
    local cf df
    cf="$(compose_file)"
    df="$(dockerfile)"

    echo ""
    echo -e "${BOLD}════════════════════════════════════════════════${NC}"
    echo -e "${BOLD}  llama-toolchest setup${NC}"
    echo -e "${BOLD}════════════════════════════════════════════════${NC}"
    echo ""
    echo -e "  ${CYAN}GPU${NC}           ${GPU_INFO}"
    echo -e "  ${CYAN}Backend${NC}       ${GPU_VENDOR}"
    echo -e "  ${CYAN}Runtime${NC}       ${CONTAINER_VERSION}"
    echo -e "  ${CYAN}Compose${NC}       ${COMPOSE_VERSION}"
    echo -e "  ${CYAN}Distro${NC}        ${DISTRO_NAME}"
    echo -e "  ${CYAN}Dockerfile${NC}    ${df}"
    echo -e "  ${CYAN}Compose file${NC}  ${cf}"
    echo -e "  ${CYAN}UI port${NC}       ${LLAMA_TOOLCHEST_PORT}"
    echo -e "  ${CYAN}Inference port${NC} ${LLAMA_TOOLCHEST_INFERENCE_PORT}"
    if [[ -n "$LLAMA_TOOLCHEST_MODELS_DIR" ]]; then
        echo -e "  ${CYAN}Models dir${NC}    ${LLAMA_TOOLCHEST_MODELS_DIR}"
    fi
    if [[ -n "$AMD_GFX_VERSION" ]]; then
        echo -e "  ${CYAN}HSA Override${NC}  ${AMD_GFX_VERSION}"
    fi

    local autostart_status="disabled"
    if is_autostart_enabled; then
        autostart_status="enabled"
    fi
    echo -e "  ${CYAN}Auto-start${NC}    ${autostart_status}"
    echo ""

    if [[ ! -f "${SCRIPT_DIR}/${cf}" ]]; then
        err "Compose file ${cf} not found!"
        echo "  Available compose files:"
        ls -1 "${SCRIPT_DIR}"/docker-compose.*.yml 2>/dev/null | sed 's|.*/|    |' || echo "    (none)"
        echo ""
        fatal "Cannot proceed without compose file"
    fi

    if [[ ${#ACTIONS[@]} -gt 0 ]]; then
        echo -e "  ${BOLD}Actions:${NC}"
        local i=1
        for action in "${ACTIONS[@]}"; do
            echo -e "    ${i}. ${action}"
            ((i++))
        done
        echo ""
    fi

    # Show if any actions need sudo
    if [[ ${#PREREQS[@]} -gt 0 ]]; then
        echo -e "  ${YELLOW}Note:${NC} Prerequisite steps require sudo"
        echo ""
    fi
}

prompt_confirm() {
    local prompt="$1"
    local answer
    read -rp "$(echo -e "${BOLD}${prompt}${NC} [Y/n] ")" answer
    case "${answer:-Y}" in
        [Yy]*|"") return 0 ;;
        *)        return 1 ;;
    esac
}

# ─── Library: host install + service helpers ─────────────────────────────────

# shellcheck source=scripts/lib/service.sh
source "${SCRIPT_DIR}/scripts/lib/service.sh"
# shellcheck source=scripts/lib/host.sh
source "${SCRIPT_DIR}/scripts/lib/host.sh"

# ─── Main ─────────────────────────────────────────────────────────────────────

usage() {
    cat <<'USAGE'
llama-toolchest setup — auto-detect GPU + container runtime, build & run

Usage: ./setup.sh <command> [--host|--container]

Install modes:
  --container     (default) Run llama-toolchest inside a Docker/Podman
                  container. Isolates the GPU SDK install; works with the
                  existing flow.
  --host          Run llama-toolchest directly on the host system. By
                  default, downloads and installs the latest released
                  .deb/.rpm package (use --from-source to opt out).
  --from-package  Implies --host. Download the latest GitHub release
                  for this distro+arch and install via dnf/apt. This is
                  the default for --host.
  --from-source   Implies --host. Build the binary from the local source
                  via `go build` instead of installing a release package.
                  Useful for testing uncommitted changes.

Pin a specific released version with `LT_VERSION=1.0.0 ./setup.sh
install --host` to avoid hitting the GH API for the latest tag.

Lifecycle:
  install     Detect, install prerequisites, build, and start
  uninstall   Stop and remove (container or host install)
  quick       Container only: fast rebuild — only recompile Go code,
              reuse cached base layers
  rebuild     Container only: full rebuild with no cache, then start

Runtime (container only):
  up          Start a stopped container
  down        Stop the container
  logs        Follow container logs (Ctrl-C to stop)

Auto-start (container only):
  enable      Enable auto-start on boot
  disable     Disable auto-start on boot

Info:
  status      Show detected environment and planned actions, then exit
              (works for both modes)
  detect      Print detected GPU backend (cuda/rocm/cpu/vulkan/metal) and exit
  help        Show this help message

Host-mode lifecycle is managed via systemd directly:
  systemctl --user start|stop|status llama-toolchest    (user install)
  sudo systemctl start|stop|status llama-toolchest      (system install, when run as root)

Environment variables:
  GPU=cuda|rocm|vulkan|cpu      Override GPU auto-detection
  RUNTIME=docker|podman         Override container runtime auto-detection
  INSTALL_MODE=host|container   Same as --host / --container

Port configuration is stored in .env (see .env.example for details).
You can edit .env directly instead of using the interactive setup.

Examples:
  ./setup.sh install                    # detect, install prereqs, build & run (container)
  ./setup.sh install --host             # install latest released package on the host
  ./setup.sh install --from-source      # host install, build from local source
  ./setup.sh status --host              # show host install status
  ./setup.sh uninstall --host           # remove host install
  ./setup.sh status                     # container dry run
  ./setup.sh quick                      # fast container rebuild
  GPU=cpu ./setup.sh install            # force CPU-only backend (container)
  LT_VERSION=1.0.0 ./setup.sh install --host  # pin a specific package version
USAGE
}

main() {
    local command="${1:-help}"
    shift || true

    cd "$SCRIPT_DIR"

    # ── Parse flags after the command ──
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --host)         INSTALL_MODE="host" ;;
            --container)    INSTALL_MODE="container" ;;
            --from-source)  INSTALL_MODE="host"; HOST_INSTALL_MODE="source" ;;
            --from-package) INSTALL_MODE="host"; HOST_INSTALL_MODE="package" ;;
            -h|--help)      usage; exit 0 ;;
            *)
                err "Unknown flag: $1"
                echo ""
                usage
                exit 1
                ;;
        esac
        shift
    done

    # ── Validate command ──
    case "$command" in
        install|uninstall|up|down|rebuild|quick|logs|detect|status|enable|disable) ;;
        -h|--help|help)
            usage
            exit 0
            ;;
        *)
            err "Unknown command: $command"
            echo ""
            usage
            exit 1
            ;;
    esac

    # ── Host mode: short circuit before any container detection ──
    if [[ "$INSTALL_MODE" == "host" ]]; then
        # Detect GPU so HSA_OVERRIDE_GFX_VERSION etc. are populated for the
        # unit override.
        if [[ -n "${GPU:-}" ]]; then
            GPU_VENDOR="$GPU"
            GPU_INFO="(manually set: $GPU)"
        else
            detect_gpu
        fi
        # Detect distro family so host_install_from_package knows which
        # package manager + extension to use, and host_install_gpu_sdk
        # knows which package names to install.
        detect_distro

        case "$command" in
            install)   host_install ;;
            uninstall) host_uninstall ;;
            status)    host_status ;;
            detect)    echo "$GPU_VENDOR" ;;
            up|down|logs|enable|disable|rebuild|quick)
                err "'$command' is not supported in --host mode."
                log "For host installs, manage the service via systemd directly:"
                if [[ "$(host_scope 2>/dev/null)" == "system" ]]; then
                    echo "    sudo systemctl start|stop|status|enable|disable llama-toolchest"
                    echo "    sudo journalctl -u llama-toolchest -f      # logs"
                else
                    echo "    systemctl --user start|stop|status|enable|disable llama-toolchest"
                    echo "    journalctl --user -u llama-toolchest -f    # logs"
                fi
                exit 1
                ;;
        esac
        exit 0
    fi

    # ── Detect everything ──
    if [[ -n "${GPU:-}" ]]; then
        GPU_VENDOR="$GPU"
        GPU_INFO="(manually set: $GPU)"
    else
        detect_gpu
    fi

    # Short-circuit for detect command
    if [[ "$command" == "detect" ]]; then
        echo "$GPU_VENDOR"
        exit 0
    fi

    detect_container_runtime
    detect_distro
    load_env_ports

    # ── Commands that don't need prerequisite checks ──
    case "$command" in
        up)
            container_up
            ok "llama-toolchest started"
            exit 0
            ;;
        down)
            container_down
            ok "llama-toolchest stopped"
            exit 0
            ;;
        logs)
            container_logs
            exit 0
            ;;
        enable)
            autostart_enable
            exit 0
            ;;
        disable)
            autostart_disable
            exit 0
            ;;
        uninstall)
            container_uninstall
            exit 0
            ;;
        quick)
            log "Quick rebuild (cached)..."
            container_quick_rebuild
            ok "llama-toolchest is running"
            echo ""
            echo "  Web UI:     http://localhost:${LLAMA_TOOLCHEST_PORT}"
            echo ""
            exit 0
            ;;
    esac

    # ── Check prerequisites and show summary (install, rebuild, status) ──
    check_prerequisites
    print_summary

    if [[ "$command" == "status" ]]; then
        exit 0
    fi

    # ── Configure ports and storage ──
    prompt_ports
    prompt_models_dir

    # ── Install prerequisites if needed ──
    if [[ ${#PREREQS[@]} -gt 0 ]]; then
        if ! prompt_confirm "Install prerequisites?"; then
            echo "Aborted."
            exit 0
        fi
        echo ""
        install_prerequisites
        echo ""
    fi

    # ── Build and run ──
    if ! prompt_confirm "Build and start llama-toolchest?"; then
        echo "Aborted."
        exit 0
    fi

    echo ""
    case "$command" in
        install)  container_install ;;
        rebuild)  container_rebuild ;;
    esac

    echo ""
    ok "llama-toolchest is running"
    echo ""
    echo "  Web UI:     http://localhost:${LLAMA_TOOLCHEST_PORT}"
    echo "  Inference:  http://localhost:${LLAMA_TOOLCHEST_INFERENCE_PORT}"
    echo ""
    echo "  Logs:       ./setup.sh logs"
    echo "  Stop:       ./setup.sh down"
    echo "  Auto-start: ./setup.sh enable"
    echo ""
}

main "$@"
