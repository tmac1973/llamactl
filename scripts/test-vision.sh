#!/usr/bin/env bash
# Test multimodal vision support by sending an image to the chat completions API.
# Usage: ./scripts/test-vision.sh [--host HOST] [--port PORT] [model-name] [image-url-or-path]

set -euo pipefail
source "$(dirname "$0")/lib-test.sh"

if [[ ${#ARGS[@]} -gt 0 ]]; then
    MODEL="${ARGS[0]}"
else
    pick_model chat
fi
IMAGE_SRC="${ARGS[1]:-https://upload.wikimedia.org/wikipedia/commons/thumb/3/3a/Cat03.jpg/1200px-Cat03.jpg}"

# Resolve image source to a URL the server can use
if [[ -f "$IMAGE_SRC" ]]; then
    echo "==> Encoding local file as base64: ${IMAGE_SRC}"
    MIME=$(file -b --mime-type "$IMAGE_SRC" 2>/dev/null || echo "image/jpeg")
    IMAGE_URL="data:${MIME};base64,$(base64 -w0 "$IMAGE_SRC")"
else
    echo "==> Image URL: ${IMAGE_SRC}"
    IMAGE_URL="$IMAGE_SRC"
fi
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
echo "$RESPONSE" | jq .

CONTENT=$(echo "$RESPONSE" | jq -r '.choices[0].message.content // empty' 2>/dev/null || true)
if [[ -n "$CONTENT" ]]; then
    echo ""
    echo "==> Model says: ${CONTENT}"
fi
