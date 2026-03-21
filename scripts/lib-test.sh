# Shared helpers for test scripts. Source this, don't execute it.
# Usage: source "$(dirname "$0")/lib-test.sh"
#
# Parses --host and --port flags from the caller's arguments.
# Remaining args are left in ARGS array for the script to use.
#
# Examples:
#   ./scripts/test-tools.sh --host myserver --port 4000
#   ./scripts/test-tools.sh --host myserver --port 4000 my-model-name

ARGS=()
_HOST="${LLAMACTL_HOST:-localhost}"
_PORT="${LLAMACTL_PORT:-3000}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --host) _HOST="$2"; shift 2 ;;
        --port) _PORT="$2"; shift 2 ;;
        *)      ARGS+=("$1"); shift ;;
    esac
done

BASE_URL="http://${_HOST}:${_PORT}/v1"
MGMT_URL="http://${_HOST}:${_PORT}"

# pick_model [filter]
# Shows numbered list of available models, lets user pick one.
# Optional filter: "chat", "embedding", or "" for all.
# Sets MODEL variable. Exits if user cancels.
pick_model() {
    local filter="${1:-}"

    local models_json
    models_json=$(curl -s "${BASE_URL}/models")
    if [[ -z "$models_json" ]] || ! echo "$models_json" | jq empty &>/dev/null; then
        echo "Error: Could not reach ${BASE_URL}/models" >&2
        echo "Is the server running?" >&2
        exit 1
    fi

    local model_list
    model_list=$(echo "$models_json" | jq -r --arg filter "$filter" '
        .data[] |
        .id as $id |
        (.meta.capabilities // []) as $caps |
        (if ($caps | index("embedding")) then true else false end) as $is_emb |
        # Apply filter
        if $filter == "chat" and $is_emb then empty
        elif $filter == "embedding" and ($is_emb | not) then empty
        else
            $id + "|" + $id +
            (if $is_emb then " (embedding)" else "" end)
        end
    ' 2>/dev/null)

    if [[ -z "$model_list" ]]; then
        local msg="No models available"
        [[ -n "$filter" ]] && msg="No ${filter} models available"
        echo "${msg}. Is the server running with models enabled?" >&2
        exit 1
    fi

    local count
    count=$(echo "$model_list" | wc -l)

    if [[ "$count" -eq 1 ]]; then
        MODEL=$(echo "$model_list" | head -1 | cut -d'|' -f1)
        echo "==> Using model: ${MODEL}" >&2
        return
    fi

    echo "" >&2
    echo "Available models:" >&2
    local idx=1
    while IFS='|' read -r _ display; do
        printf "  %d) %s\n" "$idx" "$display" >&2
        ((idx++))
    done <<< "$model_list"
    echo "" >&2

    local choice
    read -p "Pick a model [1-${count}]: " choice </dev/tty
    if [[ -z "$choice" ]] || [[ "$choice" -lt 1 ]] || [[ "$choice" -gt "$count" ]]; then
        echo "Cancelled." >&2
        exit 1
    fi

    MODEL=$(echo "$model_list" | sed -n "${choice}p" | cut -d'|' -f1)
    echo "==> Using model: ${MODEL}" >&2
}
