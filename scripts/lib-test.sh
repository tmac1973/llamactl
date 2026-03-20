# Shared helpers for test scripts. Source this, don't execute it.
# Usage: source "$(dirname "$0")/lib-test.sh"

HOST="${LLAMACTL_HOST:-localhost}"
PORT="${LLAMACTL_PORT:-3000}"
BASE_URL="http://${HOST}:${PORT}/v1"

# pick_model [filter]
# Shows numbered list of available models, lets user pick one.
# Optional filter: "chat", "embedding", or "" for all.
# Sets MODEL variable. Exits if user cancels.
pick_model() {
    local filter="${1:-}"

    local models_json
    models_json=$(curl -s "${BASE_URL}/models")
    if [[ -z "$models_json" ]] || ! echo "$models_json" | python3 -c "import json,sys; json.load(sys.stdin)" &>/dev/null; then
        echo "Error: Could not reach ${BASE_URL}/models" >&2
        echo "Is the server running?" >&2
        exit 1
    fi

    local model_list
    model_list=$(echo "$models_json" | python3 -c "
import json, sys

data = json.load(sys.stdin)['data']
filter_type = '${filter}'

# Common embedding model patterns
embed_patterns = ['embed', 'bge-', 'e5-', 'gte-', 'arctic-embed', 'mxbai-embed', 'jina-embed']

def is_embedding(m):
    mid = m.get('id', '').lower()
    return any(p in mid for p in embed_patterns)

for i, m in enumerate(data):
    mid = m['id']
    status = m.get('status', {})
    state = status.get('value', '') if isinstance(status, dict) else ''
    is_emb = is_embedding(m)

    if filter_type == 'chat' and is_emb:
        continue
    if filter_type == 'embedding' and not is_emb:
        continue

    tag = ''
    if state == 'loaded':
        tag = ' [loaded]'
    elif state == 'loading':
        tag = ' [loading]'
    if is_emb:
        tag += ' (embedding)'

    print(f'{i}|{mid}|{mid}{tag}')
" 2>/dev/null)

    if [[ -z "$model_list" ]]; then
        local msg="No models available"
        [[ -n "$filter" ]] && msg="No ${filter} models available"
        echo "${msg}. Is the server running with models enabled?" >&2
        exit 1
    fi

    local count
    count=$(echo "$model_list" | wc -l)

    if [[ "$count" -eq 1 ]]; then
        MODEL=$(echo "$model_list" | head -1 | cut -d'|' -f2)
        echo "==> Using model: ${MODEL}" >&2
        return
    fi

    echo "" >&2
    echo "Available models:" >&2
    local idx=1
    while IFS='|' read -r _ id display; do
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

    MODEL=$(echo "$model_list" | sed -n "${choice}p" | cut -d'|' -f2)
    echo "==> Using model: ${MODEL}" >&2
}
