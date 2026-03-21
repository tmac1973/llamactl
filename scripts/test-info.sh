#!/usr/bin/env bash
# Test model info and PS endpoints.
# Usage: ./scripts/test-info.sh [--host HOST] [--port PORT]

set -euo pipefail
source "$(dirname "$0")/lib-test.sh"

echo "--- /api/ps (loaded models) ---"
curl -s "${MGMT_URL}/api/ps" | jq . || {
    echo "Failed to reach /api/ps"
    exit 1
}

echo ""
echo "--- /v1/models (model list with meta) ---"
curl -s "${BASE_URL}/models" | jq . || {
    echo "Failed to reach /v1/models"
    exit 1
}

echo ""
echo "--- /v1/models/{id} (single model detail) ---"

if [[ ${#ARGS[@]} -gt 0 ]]; then
    MODEL="${ARGS[0]}"
else
    pick_model
fi
echo ""

curl -s "${BASE_URL}/models/${MODEL}" | jq . || {
    echo "Failed to fetch model info"
}
