# scripts/lib/host.sh
#
# Host install logic — installs llama-toolchest directly on the host system
# rather than inside a container. Sourced by setup.sh; depends on common
# helpers (log/ok/warn/err/prompt_confirm) and on scripts/lib/service.sh.
#
# Strategy: build the binary from source via `go build`. Once a tagged
# release exists on GitHub, this can switch to fetching the prebuilt
# package; until then, from-source is the only working path.
#
# Layout (Linux):
#   user install (default)
#     binary:   ~/.local/bin/llama-toolchest
#     config:   $XDG_CONFIG_HOME/llama-toolchest/llama-toolchest.yaml
#     data:     $XDG_DATA_HOME/llama-toolchest
#     unit:     ~/.config/systemd/user/llama-toolchest.service
#   system install (root)
#     binary:   /usr/local/bin/llama-toolchest
#     config:   /etc/llama-toolchest/llama-toolchest.yaml
#     data:     /var/lib/llama-toolchest
#     unit:     /etc/systemd/system/llama-toolchest.service
#
# Public functions:
#   host_install
#   host_uninstall
#   host_status

host_scope() {
    if [[ $EUID -eq 0 ]]; then
        echo "system"
    else
        echo "user"
    fi
}

host_bin_dir() {
    if [[ "$(host_scope)" == "system" ]]; then
        echo "/usr/local/bin"
    else
        echo "${HOME}/.local/bin"
    fi
}

host_config_dir() {
    if [[ "$(host_scope)" == "system" ]]; then
        echo "/etc/llama-toolchest"
    else
        echo "${XDG_CONFIG_HOME:-$HOME/.config}/llama-toolchest"
    fi
}

host_data_dir() {
    if [[ "$(host_scope)" == "system" ]]; then
        echo "/var/lib/llama-toolchest"
    else
        echo "${XDG_DATA_HOME:-$HOME/.local/share}/llama-toolchest"
    fi
}

host_binary_path() {
    echo "$(host_bin_dir)/llama-toolchest"
}

host_config_path() {
    echo "$(host_config_dir)/llama-toolchest.yaml"
}

# Verify that the prerequisites for an in-tree `go build` are present. Does
# not install anything — leaves that to the user, since the build toolchain
# differs by distro and the existing setup.sh prereq logic already handles
# it for the container path.
host_check_build_toolchain() {
    local missing=()
    command -v go      >/dev/null 2>&1 || missing+=("go (>= 1.25)")
    command -v cmake   >/dev/null 2>&1 || missing+=("cmake")
    command -v ninja   >/dev/null 2>&1 || command -v ninja-build >/dev/null 2>&1 || missing+=("ninja-build")
    command -v git     >/dev/null 2>&1 || missing+=("git")

    if [[ ${#missing[@]} -gt 0 ]]; then
        warn "Missing build prerequisites: ${missing[*]}"
        log "These are needed to build the llama-toolchest binary and (later) llama.cpp from the UI."
        log "Install via your package manager:"
        echo "    Debian/Ubuntu:  sudo apt-get install golang cmake ninja-build git build-essential"
        echo "    Fedora:         sudo dnf install golang cmake ninja-build git gcc-c++ make"
        echo "    Arch:           sudo pacman -S go cmake ninja git base-devel"
        return 1
    fi
    return 0
}

# Map a GPU backend to the package list needed for llama.cpp to compile
# against it on this distro family. Echoes a space-separated list (empty
# if everything is already present, or if we don't know how to detect
# packages on this distro). Returns 0 always.
host_missing_gpu_sdk_packages() {
    local backend="$1"
    local -a need=()

    case "$backend" in
        rocm)
            case "$DISTRO_FAMILY" in
                fedora)
                    # Fedora's native ROCm packages. Versioned together by
                    # the distro, so dnf resolves a consistent set.
                    for pkg in rocm-hip-devel rocblas-devel hipblas-devel rocm-cmake rocwmma-devel; do
                        rpm -q "$pkg" >/dev/null 2>&1 || need+=("$pkg")
                    done
                    ;;
                debian)
                    # Debian/Ubuntu don't ship ROCm in default repos —
                    # users add AMD's apt repo first. Names below match
                    # AMD's repo (https://repo.radeon.com).
                    for pkg in rocm-hip-runtime-dev rocblas-dev hipblas-dev rocm-cmake rocwmma-dev; do
                        dpkg -s "$pkg" >/dev/null 2>&1 || need+=("$pkg")
                    done
                    ;;
            esac
            ;;
        cuda)
            # Detect by binary presence — CUDA toolkit ships nvcc; if it's
            # on PATH we have what we need to compile.
            command -v nvcc >/dev/null 2>&1 || need+=("cuda-toolkit")
            ;;
        vulkan)
            # llama.cpp's Vulkan backend pulls in the Vulkan loader/headers
            # AND the SPIR-V C++ headers (spirv/unified1/spirv.hpp). The GPU
            # driver typically lays down the runtime loader, but the dev
            # headers and shader compiler need to be requested explicitly.
            # vulkan-tools provides vulkaninfo, which the post-install
            # backend probe (internal/builder/detect.go) uses to enumerate
            # GPUs — without it the UI marks the vulkan backend unavailable
            # even after a clean SDK install.
            case "$DISTRO_FAMILY" in
                fedora)
                    for pkg in glslc vulkan-headers vulkan-loader-devel spirv-headers-devel vulkan-tools; do
                        rpm -q "$pkg" >/dev/null 2>&1 || need+=("$pkg")
                    done
                    ;;
                debian)
                    # glslc is its own package on Debian (frontend to
                    # shaderc); glslang-tools ships glslangValidator
                    # which find_package(Vulkan) does not use.
                    for pkg in glslc libvulkan-dev spirv-headers vulkan-tools; do
                        dpkg -s "$pkg" >/dev/null 2>&1 || need+=("$pkg")
                    done
                    ;;
                *)
                    # Best-effort fallback for unknown distros: at least flag
                    # the shader compiler if it's missing.
                    command -v glslc >/dev/null 2>&1 || need+=("glslc")
                    ;;
            esac
            ;;
        cpu|metal)
            : # nothing extra
            ;;
    esac

    echo "${need[*]}"
}

# Offer to install missing GPU SDK packages for the chosen backend.
# Returns 0 if everything is in place (or user declined), 1 on install
# failure. Distro-aware: fedora and debian get auto-install; others get
# instructions and a continue/abort prompt.
host_install_gpu_sdk() {
    local backend="$1"
    local missing
    missing="$(host_missing_gpu_sdk_packages "$backend")"

    if [[ -z "$missing" ]]; then
        case "$backend" in
            cpu|metal) ;;
            *) ok "GPU SDK ($backend): all required packages installed" ;;
        esac
        return 0
    fi

    warn "Missing $backend SDK packages on host: $missing"
    log "These are required for llama.cpp to compile against the $backend backend from the UI."

    case "$DISTRO_FAMILY" in
        fedora)
            log "Run: sudo dnf install $missing"
            if prompt_confirm "Install now?"; then
                # shellcheck disable=SC2086
                sudo dnf install -y $missing || return 1
                ok "$backend SDK packages installed"
            else
                warn "Skipped — first llama.cpp build with the $backend profile will fail until these are installed."
            fi
            ;;
        debian)
            log "Run: sudo apt-get install $missing"
            if [[ "$backend" == "rocm" ]]; then
                log "Note: ROCm is not in Debian/Ubuntu default repos. If apt can't find these, follow:"
                echo "    https://rocm.docs.amd.com/projects/install-on-linux/en/latest/install/quick-start.html"
                echo "    to add AMD's apt repo first, then re-run setup.sh install --host."
            fi
            if [[ "$backend" == "cuda" ]]; then
                log "Note: CUDA toolkit comes from NVIDIA's apt repo, not the distro default. See:"
                echo "    https://developer.nvidia.com/cuda-downloads?target_os=Linux"
            fi
            if prompt_confirm "Install now?"; then
                # shellcheck disable=SC2086
                sudo apt-get install -y $missing || return 1
                ok "$backend SDK packages installed"
            else
                warn "Skipped — first llama.cpp build with the $backend profile will fail until these are installed."
            fi
            ;;
        *)
            warn "Auto-install of $backend SDK is not implemented for distro family '$DISTRO_FAMILY'."
            log "Install manually using your package manager, then re-run setup.sh install --host."
            return 1
            ;;
    esac
    return 0
}

host_build_binary() {
    local out; out="$(host_binary_path)"
    local commit; commit="$(git -C "$SCRIPT_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)"
    local ver="dev-$commit"
    local date; date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

    log "Building llama-toolchest from source ($ver)..."
    mkdir -p "$(dirname "$out")"
    (cd "$SCRIPT_DIR" && \
        go build \
            -ldflags="-s -w -X main.version=$ver -X main.commit=$commit -X main.date=$date" \
            -o "$out" \
            ./cmd/llama-toolchest)
    ok "Installed binary: $out"
}

# Repo coordinates for fetching released packages. Adjust if the project
# moves; everything else in this file derives from these.
readonly HOST_RELEASE_REPO="tmac1973/llama-toolchest"
readonly HOST_RELEASE_API="https://api.github.com/repos/${HOST_RELEASE_REPO}/releases/latest"
readonly HOST_RELEASE_DOWNLOAD="https://github.com/${HOST_RELEASE_REPO}/releases/download"

# Map host architecture to the suffix used in goreleaser asset names.
host_pkg_arch() {
    case "$(uname -m)" in
        x86_64)  echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *) return 1 ;;
    esac
}

# Map distro family to the package extension shipped for it.
host_pkg_ext() {
    case "$DISTRO_FAMILY" in
        fedora) echo "rpm" ;;
        debian) echo "deb" ;;
        *) return 1 ;;
    esac
}

# Fetch the latest release tag from GitHub. Echoes the version (without the
# leading "v"). Honors GITHUB_TOKEN for higher rate limits.
host_latest_release_version() {
    local auth_args=()
    [[ -n "${GITHUB_TOKEN:-}" ]] && auth_args=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
    local json
    json="$(curl -fsSL "${auth_args[@]}" "$HOST_RELEASE_API")" || return 1
    # Cheap JSON parse: tag_name is on its own line with the value quoted.
    echo "$json" | grep -m1 '"tag_name":' | sed 's/.*"v\?\([^"]*\)".*/\1/'
}

# Install llama-toolchest from a published release. Default version is
# whatever GitHub considers latest; can be overridden via the LT_VERSION
# env var (useful for pinning a known-good release).
host_install_from_package() {
    local arch ext
    arch="$(host_pkg_arch)" || { err "Unsupported architecture: $(uname -m)"; return 1; }
    ext="$(host_pkg_ext)"   || { err "Package install isn't supported on distro family '$DISTRO_FAMILY'. Use --from-source."; return 1; }

    local version="${LT_VERSION:-}"
    if [[ -z "$version" ]]; then
        log "Looking up latest release of $HOST_RELEASE_REPO..."
        version="$(host_latest_release_version)" || { err "Failed to query GitHub releases API. Check network or set LT_VERSION=X.Y.Z to skip the lookup."; return 1; }
        [[ -z "$version" ]] && { err "Couldn't parse a version from the release JSON."; return 1; }
    fi
    log "Installing version v$version (${arch}, .${ext})"

    local asset="llama-toolchest_${version}_linux_${arch}.${ext}"
    local pkg_url="${HOST_RELEASE_DOWNLOAD}/v${version}/${asset}"
    local sums_url="${HOST_RELEASE_DOWNLOAD}/v${version}/checksums.txt"

    local tmpdir
    tmpdir="$(mktemp -d)"
    # Best-effort cleanup; even if the function returns early we don't want
    # leftover packages eating /tmp.
    trap "rm -rf '$tmpdir'" RETURN

    log "Downloading $asset..."
    if ! curl -fsSL --output "$tmpdir/$asset" "$pkg_url"; then
        err "Download failed: $pkg_url"
        return 1
    fi

    if curl -fsSL --output "$tmpdir/checksums.txt" "$sums_url" 2>/dev/null; then
        local expected actual
        expected="$(grep " ${asset}\$" "$tmpdir/checksums.txt" | awk '{print $1}')"
        actual="$(sha256sum "$tmpdir/$asset" | awk '{print $1}')"
        if [[ -z "$expected" ]]; then
            warn "$asset not found in checksums.txt; skipping verification."
        elif [[ "$expected" != "$actual" ]]; then
            err "Checksum mismatch for $asset"
            err "  expected: $expected"
            err "  actual:   $actual"
            return 1
        else
            ok "Checksum verified"
        fi
    else
        warn "Couldn't fetch checksums.txt; skipping verification."
    fi

    # Clean up a previous from-source binary if one exists, so PATH
    # resolution doesn't keep pointing at the old user-local copy.
    if [[ -f "$HOME/.local/bin/llama-toolchest" ]]; then
        log "Removing previous from-source binary at $HOME/.local/bin/llama-toolchest"
        rm -f "$HOME/.local/bin/llama-toolchest"
    fi

    log "Installing $asset (sudo)..."
    case "$DISTRO_FAMILY" in
        fedora) sudo dnf install -y "$tmpdir/$asset" || return 1 ;;
        debian) sudo apt-get install -y "$tmpdir/$asset" || return 1 ;;
    esac

    ok "Installed: $(/usr/bin/llama-toolchest --version 2>/dev/null || echo "v$version")"
}

# Write the example config to the user's config dir if it doesn't exist.
# Existing configs are left alone — we don't overwrite the user's settings.
host_write_config() {
    local cfg_path; cfg_path="$(host_config_path)"
    local cfg_dir; cfg_dir="$(host_config_dir)"
    local data_dir; data_dir="$(host_data_dir)"

    mkdir -p "$cfg_dir" "$data_dir/builds" "$data_dir/models" "$data_dir/config"

    if [[ -f "$cfg_path" ]]; then
        log "Config already exists at $cfg_path — leaving it alone."
        return 0
    fi

    log "Writing config: $cfg_path"
    cat > "$cfg_path" <<EOF
# llama-toolchest configuration (host install, $(host_scope) scope)
# Generated by setup.sh — edit this file or use the Settings UI.

listen_addr: ":${LLAMA_TOOLCHEST_PORT:-3000}"
data_dir: "$data_dir"
llama_port: ${LLAMA_TOOLCHEST_INFERENCE_PORT:-8080}
external_url: "http://localhost:${LLAMA_TOOLCHEST_PORT:-3000}"
log_level: "info"
models_max: 1
auto_start: false
EOF
}

# Write a systemd drop-in override carrying GPU env vars (e.g.
# HSA_OVERRIDE_GFX_VERSION) and, when needed, an ExecStart pointing at a
# non-standard binary path. The packaged unit file stays generic;
# per-machine knobs live in the override.
#
# For from-package installs we only write the override if there's
# something non-default to record (e.g., HSA_OVERRIDE_GFX_VERSION). The
# package's unit already invokes /usr/bin/llama-toolchest with the right
# defaults, so an empty override is just noise.
host_write_unit_override() {
    local scope; scope="$(service_scope)"
    local override_dir
    if [[ "$scope" == "system" ]]; then
        override_dir="/etc/systemd/system/llama-toolchest.service.d"
    else
        override_dir="${HOME}/.config/systemd/user/llama-toolchest.service.d"
    fi

    local need_execstart=false
    [[ "${HOST_INSTALL_MODE:-package}" == "source" ]] && need_execstart=true

    local need_env=false
    [[ -n "${AMD_GFX_VERSION:-}" ]] && need_env=true

    if [[ "$need_execstart" == false ]] && [[ "$need_env" == false ]]; then
        # Clean up any stale override left from a previous from-source install.
        local stale="$override_dir/override.conf"
        [[ -f "$stale" ]] && rm -f "$stale" && log "Removed stale unit override"
        return 0
    fi

    mkdir -p "$override_dir"
    local override_file="$override_dir/override.conf"

    {
        echo "[Service]"
        if [[ "$need_execstart" == true ]]; then
            echo "ExecStart="
            echo "ExecStart=$(host_binary_path) --config $(host_config_path)"
        fi
        if [[ "$need_env" == true ]]; then
            echo "Environment=HSA_OVERRIDE_GFX_VERSION=${AMD_GFX_VERSION}"
        fi
    } > "$override_file"

    log "Wrote unit override: $override_file"
}

host_install() {
    local mode="${HOST_INSTALL_MODE:-package}"
    log "Host install — scope: $(host_scope), mode: $mode"
    log "GPU backend: ${GPU_VENDOR:-unknown} (${GPU_INFO:-no description})"
    # HOST_SDK_BACKENDS may carry multiple entries (e.g. rocm + vulkan); fall
    # back to GPU_VENDOR for callers that don't populate it.
    local -a sdk_backends=("${HOST_SDK_BACKENDS[@]:-}")
    if [[ ${#sdk_backends[@]} -eq 0 || -z "${sdk_backends[0]}" ]]; then
        sdk_backends=()
        [[ -n "${GPU_VENDOR:-}" ]] && sdk_backends=("$GPU_VENDOR")
    fi
    if [[ ${#sdk_backends[@]} -gt 1 ]]; then
        log "Host SDKs to install: ${sdk_backends[*]}"
    fi
    echo ""

    # GPU SDK packages so llama.cpp builds from the UI succeed first try.
    # Same step for both install modes; install each backend independently
    # so a failure in one (e.g. vulkan headers missing from a stale repo)
    # doesn't block the others.
    for backend in "${sdk_backends[@]}"; do
        host_install_gpu_sdk "$backend" || \
            warn "GPU SDK ($backend) install reported issues; continuing."
    done

    case "$mode" in
        package)
            host_install_from_package || return 1
            ;;
        source)
            if ! host_check_build_toolchain; then
                err "Resolve missing build prerequisites and re-run."
                return 1
            fi
            host_build_binary
            # Verify the bin dir is on PATH (user install only).
            if [[ "$(host_scope)" == "user" ]] && ! echo ":$PATH:" | grep -q ":$(host_bin_dir):"; then
                warn "$(host_bin_dir) is not on your PATH."
                log "Add it to your shell rc (e.g. ~/.bashrc or ~/.zshrc):"
                echo "    export PATH=\"\$HOME/.local/bin:\$PATH\""
                echo ""
            fi
            ;;
        *)
            err "Unknown HOST_INSTALL_MODE: $mode (expected 'package' or 'source')"
            return 1
            ;;
    esac

    # Config skeleton — only matters for user-scope; system installs that
    # want a /etc config can copy from the .yaml.example shipped in the package.
    if [[ "$(host_scope)" == "user" ]]; then
        host_write_config
    fi

    # Install the systemd unit. For from-package installs the package
    # already shipped a unit at /usr/lib/systemd/{system,user}/, so we
    # skip the copy and just rely on the packaged one. For from-source
    # we copy our local copy to the user's config dir.
    if [[ "$mode" == "source" ]]; then
        local unit_src="$SCRIPT_DIR/packaging/systemd/llama-toolchest.service"
        if [[ "$(host_scope)" == "user" ]]; then
            unit_src="$SCRIPT_DIR/packaging/systemd/llama-toolchest.user.service"
        fi
        service_install "$unit_src"
    else
        # Package's postinstall already ran daemon-reload, but a re-install
        # with new unit content benefits from another reload to be sure.
        if [[ "$(service_scope)" == "user" ]]; then
            systemctl --user daemon-reload >/dev/null 2>&1 || true
        else
            systemctl daemon-reload >/dev/null 2>&1 || true
        fi
    fi

    host_write_unit_override

    # MIGRATE_SKIP_START: when set, the migrate command takes ownership of
    # service start/stop sequencing (it needs to write the translated
    # config + restored registry before first start). Don't prompt and
    # don't restart here — the caller will handle it.
    if [[ "${MIGRATE_SKIP_START:-0}" == "1" ]]; then
        log "Skipping service start (caller will handle)."
    elif service_is_active; then
        log "Service is running; restarting to pick up the new binary..."
        service_restart
        ok "llama-toolchest service restarted"
    elif prompt_confirm "Enable and start the service now?"; then
        service_enable
        ok "llama-toolchest service enabled and started"
    else
        log "Service installed but not enabled. Start later with:"
        if [[ "$(host_scope)" == "user" ]]; then
            echo "    systemctl --user start llama-toolchest"
        else
            echo "    sudo systemctl start llama-toolchest"
        fi
    fi

    echo ""
    ok "Host install complete."
    echo ""
    case "$mode" in
        package) echo "  Binary:      /usr/bin/llama-toolchest (system, from package)" ;;
        source)  echo "  Binary:      $(host_binary_path)" ;;
    esac
    echo "  Config:      $(host_config_path)"
    echo "  Data dir:    $(host_data_dir)"
    echo "  Web UI:      http://localhost:${LLAMA_TOOLCHEST_PORT:-3000}"
    echo ""
}

host_uninstall() {
    log "Uninstalling host install — scope: $(host_scope)"

    service_uninstall

    local override_dir
    if [[ "$(service_scope)" == "system" ]]; then
        override_dir="/etc/systemd/system/llama-toolchest.service.d"
    else
        override_dir="${HOME}/.config/systemd/user/llama-toolchest.service.d"
    fi
    [[ -d "$override_dir" ]] && rm -rf "$override_dir"

    # Remove the binary. Two cases:
    #  (a) Installed via package — uninstall via dnf/apt so the system
    #      unit, /usr/bin binary, and the example config all go cleanly.
    #  (b) Installed from source — just rm the user-local binary.
    if rpm -q llama-toolchest >/dev/null 2>&1; then
        log "Removing llama-toolchest package (dnf, sudo)..."
        sudo dnf remove -y llama-toolchest || warn "Package removal returned non-zero; continuing."
    elif dpkg -s llama-toolchest >/dev/null 2>&1; then
        log "Removing llama-toolchest package (apt-get, sudo)..."
        sudo apt-get remove -y llama-toolchest || warn "Package removal returned non-zero; continuing."
    fi
    # Always check the user-local path too — we might have both if the user
    # switched modes without uninstalling first.
    local bin; bin="$(host_binary_path)"
    if [[ -f "$bin" ]]; then
        rm -f "$bin"
        log "Removed: $bin"
    fi

    local data_dir; data_dir="$(host_data_dir)"
    local cfg_dir; cfg_dir="$(host_config_dir)"
    log "Config dir ($cfg_dir) and data dir ($data_dir) preserved (contains your models and builds)."
    if prompt_confirm "Also remove config and data dirs? This DELETES all downloaded models and llama.cpp builds."; then
        rm -rf "$cfg_dir" "$data_dir"
        log "Removed config and data dirs."
    fi

    ok "Host uninstall complete."
}

# Whether a backend is applicable to this host. Used by deps/status to
# avoid reporting "missing cuda-toolkit" on a machine that has no NVIDIA
# GPU. Vulkan is always applicable — it's the cross-vendor fallback.
host_backend_applicable() {
    case "$1" in
        cuda)   command -v nvidia-smi >/dev/null 2>&1 || [[ -e /dev/nvidia0 ]] ;;
        rocm)   [[ -e /dev/kfd ]] ;;
        vulkan) return 0 ;;
        *)      return 1 ;;
    esac
}

# Per-backend SDK report. Echoes one line per backend with state +
# remediation command. Skips backends that aren't applicable on this
# host (e.g. cuda when there's no NVIDIA GPU). Returns non-zero if any
# applicable backend has missing packages.
host_report_sdk_deps() {
    local exit_code=0
    local backend missing inst_cmd
    case "$DISTRO_FAMILY" in
        debian) inst_cmd="sudo apt-get install -y" ;;
        fedora) inst_cmd="sudo dnf install -y" ;;
        *)      inst_cmd="<distro $DISTRO_FAMILY: install manually>" ;;
    esac
    for backend in cuda rocm vulkan; do
        if ! host_backend_applicable "$backend"; then
            printf "    %-7s %s\n" "$backend" "n/a (no matching GPU detected)"
            continue
        fi
        missing="$(host_missing_gpu_sdk_packages "$backend")"
        if [[ -z "$missing" ]]; then
            printf "    %-7s %s\n" "$backend" "OK"
        else
            printf "    %-7s missing: %s\n" "$backend" "$missing"
            printf "            %s %s\n" "$inst_cmd" "$missing"
            exit_code=1
        fi
    done
    return $exit_code
}

# Build/runtime toolchain verification. cmake/ninja/git are pulled in as
# package depends by the .deb/.rpm so they should always be present after
# a from-package install; we still re-check because users on from-source
# may have skipped the install dance.
host_report_toolchain_deps() {
    local exit_code=0
    local item bin pkgs_debian pkgs_fedora
    # name | binary | debian package | fedora package
    local -a checks=(
        "cmake|cmake|cmake|cmake"
        "ninja|ninja|ninja-build|ninja-build"
        "git|git|git|git"
        "gcc|cc|build-essential|gcc-c++ make"
    )
    local inst_cmd
    case "$DISTRO_FAMILY" in
        debian) inst_cmd="sudo apt-get install -y" ;;
        fedora) inst_cmd="sudo dnf install -y" ;;
        *)      inst_cmd="<distro $DISTRO_FAMILY>" ;;
    esac
    for c in "${checks[@]}"; do
        IFS='|' read -r name bin pkg_d pkg_f <<<"$c"
        if command -v "$bin" >/dev/null 2>&1; then
            printf "    %-7s %s\n" "$name" "OK"
        else
            local pkg="$pkg_d"
            [[ "$DISTRO_FAMILY" == "fedora" ]] && pkg="$pkg_f"
            printf "    %-7s missing\n" "$name"
            printf "            %s %s\n" "$inst_cmd" "$pkg"
            exit_code=1
        fi
    done
    return $exit_code
}

# Top-level `deps --host` entry point. Returns 0 if everything's healthy,
# 1 if anything's missing.
host_deps() {
    local rc=0
    echo "Host install dependencies (scope: $(host_scope), distro: ${DISTRO_FAMILY:-unknown}):"
    echo ""
    echo "  Package:"
    if dpkg -s llama-toolchest >/dev/null 2>&1; then
        printf "    %-20s %s\n" "llama-toolchest" "OK ($(dpkg-query -W -f='${Version}' llama-toolchest))"
    elif rpm -q llama-toolchest >/dev/null 2>&1; then
        printf "    %-20s %s\n" "llama-toolchest" "OK ($(rpm -q --qf '%{VERSION}' llama-toolchest))"
    elif [[ -x "$(host_binary_path)" ]]; then
        printf "    %-20s %s\n" "llama-toolchest" "OK (from-source: $(host_binary_path))"
    else
        printf "    %-20s %s\n" "llama-toolchest" "not installed"
        echo "            ./setup.sh install --host"
        rc=1
    fi
    echo ""
    echo "  Build toolchain (for compiling llama.cpp from the UI):"
    host_report_toolchain_deps || rc=1
    echo ""
    echo "  Backend SDKs:"
    host_report_sdk_deps || rc=1
    echo ""
    if [[ $rc -eq 0 ]]; then
        ok "All host dependencies satisfied."
    else
        warn "One or more dependencies are missing — see commands above."
    fi
    return $rc
}

host_status() {
    echo "Scope:       $(host_scope)"

    # Show whichever binary is actually present. Package install lives at
    # /usr/bin; from-source lives in the user's bin dir.
    local pkg_marker="" src_marker=""
    if rpm -q llama-toolchest >/dev/null 2>&1; then
        pkg_marker="  (package: $(rpm -q --qf '%{VERSION}' llama-toolchest))"
    elif dpkg -s llama-toolchest >/dev/null 2>&1; then
        pkg_marker="  (package: $(dpkg-query -W -f='${Version}' llama-toolchest))"
    fi
    if [[ -x /usr/bin/llama-toolchest ]]; then
        echo "Binary:      /usr/bin/llama-toolchest${pkg_marker}"
    fi
    if [[ -x "$(host_binary_path)" ]]; then
        src_marker="  (from-source)"
        echo "Binary:      $(host_binary_path)${src_marker}"
    fi
    if [[ ! -x /usr/bin/llama-toolchest ]] && [[ ! -x "$(host_binary_path)" ]]; then
        echo "Binary:      (not installed)"
    fi

    echo "Config:      $(host_config_path)$( [[ -f "$(host_config_path)" ]] && echo "" || echo "  (missing)")"
    echo "Data dir:    $(host_data_dir)"
    echo "Service:     $(service_unit_path)"
    echo ""
    echo "Backend SDKs:"
    host_report_sdk_deps || true
    echo ""
    service_status
}
