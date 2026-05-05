# scripts/lib/migrate.sh
#
# Bidirectional migration between container and host installs. Sourced by
# setup.sh after host.sh; relies on globals set by setup.sh's detect_*
# helpers (CONTAINER_CMD, GPU_VENDOR, LLAMA_TOOLCHEST_PORT, …) and on
# host.sh / service.sh helpers.
#
# Public functions:
#   migrate_to_host       Container → host. Snapshots registry, stops
#                         container, installs the .deb/.rpm, restores
#                         registry (sans builds.json), starts service.
#   migrate_to_container  Reverse. Snapshots host registry, stops the
#                         systemd service, builds + starts a fresh
#                         container, populates the volume.
#
# In both directions builds.json is wiped: container-built llama-server
# binaries don't run on the host (different glibc/CUDA/ROCm runtime)
# and vice versa, so every migration requires a fresh llama.cpp build.

# ─── Pre-flight ──────────────────────────────────────────────────────────────

# Refuse when the target side already has state. Symmetric in both
# directions: forces the user to explicitly uninstall first, so we never
# clobber config or merge state in surprising ways.

migrate_check_no_host_install() {
    if dpkg -s llama-toolchest >/dev/null 2>&1 \
       || rpm -q llama-toolchest >/dev/null 2>&1 \
       || [[ -f "$(host_config_path)" ]]; then
        err "A host install is already present."
        log "Run './setup.sh uninstall --host' first if you want to redo the migration."
        return 1
    fi
}

migrate_check_no_container() {
    [[ -z "${CONTAINER_CMD:-}" ]] && return 0
    if $CONTAINER_CMD inspect llama-toolchest >/dev/null 2>&1; then
        err "A 'llama-toolchest' container already exists (running or stopped)."
        log "Run './setup.sh uninstall' first."
        return 1
    fi
    if $CONTAINER_CMD volume inspect llama-toolchest-data >/dev/null 2>&1; then
        err "Container volume 'llama-toolchest-data' already exists."
        log "Remove with: $CONTAINER_CMD volume rm llama-toolchest-data"
        return 1
    fi
}

# ─── Snapshot ────────────────────────────────────────────────────────────────

# Copy the four registry files plus the YAML config into <staging>. Per-file
# failures are warned (the file may not exist yet on a fresh install) but
# don't abort. Returns 1 only if nothing was copied.
migrate_snapshot_from_container() {
    local staging="$1"; mkdir -p "$staging"
    local copied=0
    for f in models.json builds.json preset.ini benchmarks.json llama-toolchest.yaml; do
        if $CONTAINER_CMD cp "llama-toolchest:/data/config/$f" "$staging/$f" 2>/dev/null; then
            copied=$((copied + 1))
        else
            warn "Could not snapshot $f (may not exist)"
        fi
    done
    if [[ $copied -eq 0 ]]; then
        err "No registry files could be copied from container."
        return 1
    fi
    ok "Snapshotted $copied file(s) from container"
}

migrate_snapshot_from_host() {
    local staging="$1"; mkdir -p "$staging"
    local cfg_dir; cfg_dir="$(host_data_dir)/config"
    local copied=0
    for f in models.json builds.json preset.ini benchmarks.json; do
        if [[ -f "$cfg_dir/$f" ]]; then
            cp "$cfg_dir/$f" "$staging/$f"
            copied=$((copied + 1))
        fi
    done
    if [[ -f "$(host_config_path)" ]]; then
        cp "$(host_config_path)" "$staging/llama-toolchest.yaml"
        copied=$((copied + 1))
    fi
    if [[ $copied -eq 0 ]]; then
        err "No registry files found in host install."
        return 1
    fi
    ok "Snapshotted $copied file(s) from host"
}

# ─── Config translation ──────────────────────────────────────────────────────

# Read flat YAML scalar fields from <src>, translate the path/port-shaped
# ones for <direction>, write to <dst>. Bash sed-based — assumes the
# config follows the format setup.sh writes (no anchors, no multiline,
# no inline comments after values). Anything beyond that is undefined.
migrate_write_translated_config() {
    local src="$1" dst="$2" direction="$3"

    # Helper: extract a top-level scalar field, stripping surrounding quotes.
    _migrate_get() {
        grep "^$1:" "$src" 2>/dev/null | head -1 | sed "s/^$1: *//; s/^\"//; s/\"$//"
    }

    local models_dir external_url hf_token api_key log_level models_max auto_start
    models_dir=$(_migrate_get models_dir)
    external_url=$(_migrate_get external_url)
    hf_token=$(_migrate_get hf_token)
    api_key=$(_migrate_get api_key)
    log_level=$(_migrate_get log_level)
    models_max=$(_migrate_get models_max)
    auto_start=$(_migrate_get auto_start)

    local listen_addr data_dir
    case "$direction" in
        to-host)
            # The container's listen_addr is always :3000 internally. We
            # want the *host-side* port (i.e. what the user types in their
            # browser), pulled from the container's port mapping if we
            # can; fall back to 3000.
            local p=""
            if [[ -n "${CONTAINER_CMD:-}" ]]; then
                p=$($CONTAINER_CMD inspect llama-toolchest 2>/dev/null \
                    | grep -A 5 '"3000/tcp"' | grep -m1 HostPort \
                    | sed 's/.*"\([0-9]*\)".*/\1/')
            fi
            listen_addr=":${p:-3000}"
            data_dir="$(host_data_dir)"
            ;;
        to-container)
            # Inside the container, listen_addr is always :3000. The
            # host-side port is set in .env (LLAMA_TOOLCHEST_PORT) and
            # mapped via compose.
            listen_addr=":3000"
            data_dir="/data"
            # Translate the host listen_addr to the .env port, so the
            # caller's compose run maps the right host port. Strip the
            # leading ':'.
            local host_port; host_port=$(_migrate_get listen_addr)
            host_port="${host_port#:}"
            [[ -n "$host_port" ]] && LLAMA_TOOLCHEST_PORT="$host_port"
            ;;
        *)
            err "Unknown direction: $direction"
            return 1
            ;;
    esac

    {
        echo "# llama-toolchest configuration (migrated $direction on $(date -u +%FT%TZ))"
        echo ""
        echo "listen_addr: \"$listen_addr\""
        echo "data_dir: \"$data_dir\""
        [[ -n "$models_dir" ]] && echo "models_dir: \"$models_dir\""
        echo "llama_port: ${LLAMA_TOOLCHEST_INFERENCE_PORT:-8080}"
        echo "external_url: \"${external_url:-}\""
        echo "hf_token: \"${hf_token:-}\""
        echo "api_key: \"${api_key:-}\""
        echo "log_level: \"${log_level:-info}\""
        echo "models_max: ${models_max:-1}"
        echo "auto_start: ${auto_start:-true}"
    } > "$dst"

    unset -f _migrate_get
}

# ─── Forward: container → host ───────────────────────────────────────────────

migrate_to_host() {
    log "Migrating: container → host"
    echo ""

    if [[ -z "${CONTAINER_CMD:-}" ]]; then
        err "No container runtime detected — nothing to migrate from."
        return 1
    fi
    migrate_check_no_host_install || return 1

    if ! $CONTAINER_CMD inspect llama-toolchest >/dev/null 2>&1 \
       && ! $CONTAINER_CMD volume inspect llama-toolchest-data >/dev/null 2>&1; then
        err "No 'llama-toolchest' container or 'llama-toolchest-data' volume found."
        return 1
    fi

    local staging="${HOME}/llt-migrate-$(date +%s)"
    log "Snapshotting registry → $staging"
    migrate_snapshot_from_container "$staging" || return 1

    if $CONTAINER_CMD inspect llama-toolchest >/dev/null 2>&1; then
        log "Stopping and removing container..."
        container_down 2>/dev/null || true
        $CONTAINER_CMD rm -f llama-toolchest >/dev/null 2>&1 || true
    fi

    log "Running host install (from-package)..."
    HOST_INSTALL_MODE="package"
    MIGRATE_SKIP_START=1 host_install || { err "Host install failed."; return 1; }

    log "Translating config..."
    migrate_write_translated_config \
        "$staging/llama-toolchest.yaml" \
        "$(host_config_path)" \
        "to-host"

    local cfg_dir; cfg_dir="$(host_data_dir)/config"
    mkdir -p "$cfg_dir"
    for f in models.json preset.ini benchmarks.json; do
        [[ -f "$staging/$f" ]] && cp "$staging/$f" "$cfg_dir/$f"
    done
    echo "[]" > "$cfg_dir/builds.json"
    ok "Registry restored. builds.json wiped — rebuild llama.cpp from the UI."

    log "Starting service..."
    service_enable

    local port; port=$(grep '^listen_addr:' "$(host_config_path)" | sed 's/.*: *"//; s/".*//; s/^://')
    echo ""
    ok "Migration complete."
    echo ""
    echo "  Web UI:        http://localhost:${port:-3000}"
    echo "  Snapshot kept: $staging"
    echo "  Volume kept:   llama-toolchest-data"
    echo "                 (remove with: $CONTAINER_CMD volume rm llama-toolchest-data"
    echo "                  once you've confirmed the host install works)"
    echo ""
    echo "  NEXT STEPS:"
    echo "    1. Open the Builds page and run a fresh build. Container-built"
    echo "       binaries don't run natively, so builds.json was wiped."
    echo "    2. Set the new build as active, then restart the service."
    echo "    3. Optionally: sudo loginctl enable-linger \$USER  (survives logout)"
    echo ""
}

# ─── Reverse: host → container ───────────────────────────────────────────────

migrate_to_container() {
    log "Migrating: host → container"
    echo ""

    if [[ -z "${CONTAINER_CMD:-}" ]]; then
        err "No container runtime detected. Install Docker or Podman first."
        return 1
    fi
    migrate_check_no_container || return 1

    if ! dpkg -s llama-toolchest >/dev/null 2>&1 \
       && ! rpm -q llama-toolchest >/dev/null 2>&1 \
       && ! [[ -f "$(host_config_path)" ]]; then
        err "No host install detected."
        return 1
    fi

    local staging="${HOME}/llt-migrate-$(date +%s)"
    log "Snapshotting registry → $staging"
    migrate_snapshot_from_host "$staging" || return 1

    if service_is_active 2>/dev/null; then
        log "Stopping host service..."
        service_disable
    fi
    # Note: the host package itself stays installed. User can run
    # './setup.sh uninstall --host' once the container is healthy.

    # The translated config sets LLAMA_TOOLCHEST_PORT (read from the host's
    # listen_addr) before we write .env, so compose maps the right port.
    local tmp_cfg; tmp_cfg=$(mktemp)
    migrate_write_translated_config \
        "$staging/llama-toolchest.yaml" \
        "$tmp_cfg" \
        "to-container"

    write_env_file
    log "Building and starting container..."
    container_install || { err "Container install failed."; rm -f "$tmp_cfg"; return 1; }

    # Wait for the container to be running before we rewrite /data/config —
    # the entrypoint creates /data on first start.
    local i
    for i in 1 2 3 4 5 6 7 8 9 10; do
        if $CONTAINER_CMD inspect -f '{{.State.Running}}' llama-toolchest 2>/dev/null \
            | grep -q true; then break; fi
        sleep 1
    done

    # Stop, repopulate /data/config from the staging dir + translated yaml,
    # then restart. We run an alpine sidecar against the volume rather than
    # `docker cp`-ing into the running container so we don't race the entry
    # point's own writes.
    log "Restoring registry into container volume..."
    container_down
    $CONTAINER_CMD run --rm \
        -v llama-toolchest-data:/data \
        -v "$staging:/snap:ro" \
        -v "$tmp_cfg:/snap-cfg:ro" \
        alpine sh -c '
            mkdir -p /data/config
            for f in models.json preset.ini benchmarks.json; do
                [ -f "/snap/$f" ] && cp "/snap/$f" "/data/config/$f"
            done
            cp /snap-cfg /data/config/llama-toolchest.yaml
            echo "[]" > /data/config/builds.json
        ' || { err "Failed to populate volume."; rm -f "$tmp_cfg"; return 1; }
    rm -f "$tmp_cfg"

    container_up
    ok "Registry restored. builds.json wiped — rebuild llama.cpp from the UI."

    echo ""
    ok "Migration complete."
    echo ""
    echo "  Web UI:        http://localhost:${LLAMA_TOOLCHEST_PORT:-3000}"
    echo "  Snapshot kept: $staging"
    echo ""
    echo "  NEXT STEPS:"
    echo "    1. Open the Builds page and run a fresh build (host binaries"
    echo "       don't run inside the container; builds.json was wiped)."
    echo "    2. Set it active, then restart the inference server."
    echo "    3. When you're confident, remove the host package:"
    echo "       ./setup.sh uninstall --host"
    echo ""
}
