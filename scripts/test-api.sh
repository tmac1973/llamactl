#!/usr/bin/env bash
# Smoke test for Llama Toolchest API endpoints.
# Usage: ./scripts/test-api.sh [--host HOST] [--port PORT]
#
# Tests page routes, management API, OpenAI proxy (via llama.cpp router),
# and chat completions.

set -euo pipefail
source "$(dirname "$0")/lib-test.sh"

BASE="$MGMT_URL"
PASS=0
FAIL=0

GREEN=$'\033[32m'
RED=$'\033[31m'
CYAN=$'\033[36m'
DIM=$'\033[2m'
NC=$'\033[0m'

pass() { PASS=$((PASS + 1)); echo "    ${GREEN}✓${NC} $1"; }
fail() { FAIL=$((FAIL + 1)); echo "    ${RED}✗${NC} $1"; }

# test_status METHOD URL EXPECTED_CODE [DESCRIPTION]
test_status() {
    local method="$1" url="$2" expect="$3" desc="${4:-$method $url}"
    local code
    code=$(curl -s -o /dev/null -w "%{http_code}" -X "$method" "$url")
    if [[ "$code" == "$expect" ]]; then
        pass "$desc → $code"
    else
        fail "$desc → $code (expected $expect)"
    fi
}

# test_json METHOD URL BODY GREP_PATTERN DESCRIPTION [EXPECTED_CODE]
test_json() {
    local method="$1" url="$2" body="$3" pattern="$4" desc="$5" expect="${6:-200}"
    local resp code
    resp=$(curl -s -w "\n__HTTP__%{http_code}" -X "$method" "$url" \
        -H "Content-Type: application/json" -d "$body")
    code=$(echo "$resp" | grep '__HTTP__' | sed 's/__HTTP__//')
    local content
    content=$(echo "$resp" | sed '/__HTTP__/d')

    if [[ "$code" != "$expect" ]]; then
        fail "$desc → HTTP $code (expected $expect)"
        echo "      ${DIM}$(echo "$content" | head -3)${NC}"
        return
    fi

    if [[ -n "$pattern" ]]; then
        if echo "$content" | grep -q "$pattern"; then
            pass "$desc"
        else
            fail "$desc → pattern '$pattern' not found"
            echo "      ${DIM}$(echo "$content" | head -3)${NC}"
        fi
    else
        pass "$desc"
    fi
}

echo ""
echo "${CYAN}Testing Llama Toolchest at $BASE${NC}"
echo ""

# ─── Pages ───────────────────────────────────────────────────────────────────

echo "=== Pages ==="
test_status GET "$BASE/"                200 "Home page"
test_status GET "$BASE/builds"          200 "Builds page"
test_status GET "$BASE/models"          200 "Models page"
test_status GET "$BASE/models/browse"   200 "Browse page"
test_status GET "$BASE/settings"        200 "Settings page"
echo ""

# ─── Management API ──────────────────────────────────────────────────────────

echo "=== Management API ==="
test_status GET  "$BASE/api/dashboard"              200 "Dashboard"
test_status GET  "$BASE/api/builds/"                200 "List builds"
test_status GET  "$BASE/api/builds/backends"        200 "List backends"
test_status GET  "$BASE/api/models/"                200 "List models"
test_status POST "$BASE/api/models/scan"            200 "Scan for models"
test_status GET  "$BASE/api/service/status"         200 "Service status"
test_status GET  "$BASE/api/service/health"         200 "Service health"
test_status GET  "$BASE/api/settings/"              200 "Settings"
test_status POST "$BASE/api/settings/test-connection" 200 "Test connection"
echo ""

# ─── Router Status ───────────────────────────────────────────────────────────

echo "=== Router Status ==="
router_state=$(curl -s "$BASE/api/service/status" | jq -r '.state // "unknown"' 2>/dev/null)
echo "  ${DIM}Router state: $router_state${NC}"

if [[ "$router_state" == "running" ]]; then
    pass "Router is running"
elif [[ "$router_state" == "stopped" ]]; then
    pass "Router is stopped (no models started)"
    echo ""
    echo "${DIM}  Skipping proxy tests — router not running.${NC}"
    echo ""
    echo "=== Results: ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC} ==="
    [[ $FAIL -eq 0 ]] && exit 0 || exit 1
else
    fail "Router state: $router_state"
fi
echo ""

# ─── OpenAI Proxy ────────────────────────────────────────────────────────────

echo "=== OpenAI Proxy ==="
resp=$(curl -s -w "\n__HTTP__%{http_code}" "$BASE/v1/models")
v1_code=$(echo "$resp" | grep '__HTTP__' | sed 's/__HTTP__//')
v1_body=$(echo "$resp" | sed '/__HTTP__/d')

if [[ "$v1_code" == "200" ]]; then
    pass "/v1/models → $v1_code"
else
    fail "/v1/models → $v1_code (expected 200)"
fi

# Count models from /v1/models
model_count=$(echo "$v1_body" | jq '.data | length' 2>/dev/null || echo 0)
echo "  ${DIM}Models available: $model_count${NC}"

# Get the first chat model (skip embedding models)
first_model=$(echo "$v1_body" | jq -r '
    [.data[] | select(.meta.capabilities | index("embedding") | not)] |
    first // empty |
    .id
' 2>/dev/null)

if [[ -z "$first_model" ]]; then
    echo "  ${DIM}No chat models found. Skipping completion tests.${NC}"
    echo ""
    echo "=== Results: ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC} ==="
    [[ $FAIL -eq 0 ]] && exit 0 || exit 1
fi

echo "  ${DIM}Using model: $first_model${NC}"
echo ""

# ─── Chat Completions ────────────────────────────────────────────────────────

echo "=== Chat Completions ==="

test_json POST "$BASE/v1/chat/completions" \
    "{\"model\":\"$first_model\",\"messages\":[{\"role\":\"user\",\"content\":\"Say hi\"}],\"max_tokens\":8}" \
    '"choices"' \
    "Chat completion with model name"

# Test without model field — router requires model name in multi-model mode
test_json POST "$BASE/v1/chat/completions" \
    '{"messages":[{"role":"user","content":"Say hi"}],"max_tokens":8}' \
    '' \
    "Chat completion without model field (expect 400)" \
    400

echo ""
echo "=== Results: ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC} ==="
[[ $FAIL -eq 0 ]] && exit 0 || exit 1
