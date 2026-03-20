#!/usr/bin/env bash
# Test multimodal vision support by sending an image to the chat completions API.
# Usage: ./scripts/test-vision.sh <model-name> [image-url-or-path]
#
# Requires a vision-capable model with an mmproj file configured.
# The model must be loaded (available) in the router.
#
# If llama.cpp was built with LLAMA_OPENSSL=ON, remote URLs are fetched
# server-side. Otherwise, local files are sent as base64 data URIs.

set -euo pipefail

HOST="${LLAMACTL_HOST:-localhost}"
PORT="${LLAMACTL_PORT:-3000}"
BASE_URL="http://${HOST}:${PORT}/v1"

MODEL="${1:-}"
IMAGE_SRC="${2:-https://upload.wikimedia.org/wikipedia/commons/thumb/3/3a/Cat03.jpg/1200px-Cat03.jpg}"

if [[ -z "$MODEL" ]]; then
    echo "No model specified. Listing available models..."
    curl -s "${BASE_URL}/models" | python3 -m json.tool 2>/dev/null || curl -s "${BASE_URL}/models"
    echo ""
    echo "Usage: $0 <model-name> [image-url-or-path]"
    echo "Pick a vision-capable model from the list above."
    exit 1
fi

# Resolve image source to a URL the server can use
if [[ -f "$IMAGE_SRC" ]]; then
    echo "==> Encoding local file as base64: ${IMAGE_SRC}"
    MIME=$(file -b --mime-type "$IMAGE_SRC" 2>/dev/null || echo "image/jpeg")
    IMAGE_URL="data:${MIME};base64,$(base64 -w0 "$IMAGE_SRC")"
else
    echo "==> Image URL: ${IMAGE_SRC}"
    IMAGE_URL="$IMAGE_SRC"
fi

echo "==> Model: ${MODEL}"
echo ""

# Build request body as a temp file (base64 images can exceed argv limits)
REQFILE=$(mktemp /tmp/test-vision-XXXXXX.json)
trap "rm -f $REQFILE" EXIT

python3 -c "
import json, sys
body = {
    'model': sys.argv[1],
    'messages': [{
        'role': 'user',
        'content': [
            {'type': 'text', 'text': 'Describe this image in one or two sentences.'},
            {'type': 'image_url', 'image_url': {'url': sys.argv[2]}}
        ]
    }],
    'max_tokens': 256
}
json.dump(body, open(sys.argv[3], 'w'))
" "$MODEL" "$IMAGE_URL" "$REQFILE"

RESPONSE=$(curl -s "${BASE_URL}/chat/completions" \
    -H "Content-Type: application/json" \
    -d @"$REQFILE")

echo "==> Response:"
echo "$RESPONSE" | python3 -m json.tool 2>/dev/null || echo "$RESPONSE"

# Extract just the content if possible
CONTENT=$(echo "$RESPONSE" | python3 -c "
import sys, json
r = json.load(sys.stdin)
print(r['choices'][0]['message']['content'])
" 2>/dev/null || true)
if [[ -n "$CONTENT" ]]; then
    echo ""
    echo "==> Model says: ${CONTENT}"
fi
