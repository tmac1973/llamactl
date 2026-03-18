#!/usr/bin/env bash
# Smoke test for LlamaCtl API endpoints.
# Usage: ./scripts/test-api.sh [base_url]
#
# Tests page routes, management API, and OpenAI proxy including
# multi-model routing behavior.

BASE="${1:-http://localhost:3000}"
PASS=0
FAIL=0

GREEN=$'\033[32m'
RED=$'\033[31m'
CYAN=$'\033[36m'
DIM=$'\033[2m'
NC=$'\033[0m'

pass() { ((PASS++)); echo "    ${GREEN}✓${NC} $1"; }
fail() { ((FAIL++)); echo "    ${RED}✗${NC} $1"; }

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
        echo "      ${DIM}$content${NC}" | head -3
        return
    fi

    if [[ -n "$pattern" ]]; then
        if echo "$content" | grep -q "$pattern"; then
            pass "$desc"
        else
            fail "$desc → pattern '$pattern' not found"
            echo "      ${DIM}$content${NC}" | head -3
        fi
    else
        pass "$desc"
    fi
}

echo ""
echo "${CYAN}Testing LlamaCtl at $BASE${NC}"
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
test_status GET  "$BASE/api/service/status"         200 "Service status"
test_status GET  "$BASE/api/service/health"         200 "Service health"
test_status GET  "$BASE/api/service/log-tabs"       200 "Log tabs"
test_status GET  "$BASE/api/settings/"              200 "Settings"
test_status POST "$BASE/api/settings/test-connection" 200 "Test connection"
echo ""

# ─── OpenAI Proxy ────────────────────────────────────────────────────────────

echo "=== OpenAI Proxy ==="
resp=$(curl -s -w "\n__HTTP__%{http_code}" "$BASE/v1/models")
v1_code=$(echo "$resp" | grep '__HTTP__' | sed 's/__HTTP__//')
v1_body=$(echo "$resp" | sed '/__HTTP__/d')

if [[ "$v1_code" == "200" ]]; then
    pass "/v1/models → $v1_code"
elif [[ "$v1_code" == "503" ]]; then
    pass "/v1/models → $v1_code (no model active)"
    echo ""
    echo "${DIM}  Skipping proxy tests — no model is active.${NC}"
    echo ""
    echo "=== Results: ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC} ==="
    [[ $FAIL -eq 0 ]] && exit 0 || exit 1
else
    fail "/v1/models → $v1_code (expected 200 or 503)"
fi

# Count active models from the /v1/models response
# llama.cpp returns a "data" array with one entry per loaded model
active_count=$(echo "$v1_body" | python3 -c "
import json,sys
try:
    d = json.load(sys.stdin)
    print(len(d.get('data', [])))
except:
    print(0)
" 2>/dev/null)
echo "  ${DIM}Active models: $active_count${NC}"

# Get the first model name for targeted tests
first_model=$(echo "$v1_body" | python3 -c "
import json,sys
try:
    d = json.load(sys.stdin)
    print(d['data'][0]['id'])
except:
    print('')
" 2>/dev/null)
echo ""

# ─── Chat Completion: basic ──────────────────────────────────────────────────

echo "=== Chat Completions ==="

# Test with a valid model name (first active)
if [[ -n "$first_model" ]]; then
    test_json POST "$BASE/v1/chat/completions" \
        "{\"model\":\"$first_model\",\"messages\":[{\"role\":\"user\",\"content\":\"Say hi\"}],\"max_tokens\":8}" \
        '"choices"' \
        "Valid model name → completion"
fi

# ─── Routing behavior depends on how many models are loaded ──────────────────

if [[ "$active_count" -le 1 ]]; then
    echo ""
    echo "--- Single-model routing (permissive) ---"

    # With a bogus model name — should still route to the one active model
    test_json POST "$BASE/v1/chat/completions" \
        '{"model":"nonexistent-model","messages":[{"role":"user","content":"Say hi"}],"max_tokens":8}' \
        '"choices"' \
        "Wrong model name → routes to active model anyway"

    # With empty model field
    test_json POST "$BASE/v1/chat/completions" \
        '{"model":"","messages":[{"role":"user","content":"Say hi"}],"max_tokens":8}' \
        '"choices"' \
        "Empty model field → routes to active model"

    # With no model field at all
    test_json POST "$BASE/v1/chat/completions" \
        '{"messages":[{"role":"user","content":"Say hi"}],"max_tokens":8}' \
        '"choices"' \
        "Missing model field → routes to active model"
else
    echo ""
    echo "--- Multi-model routing (strict) ---"

    # With a bogus model name — should return an error listing available models
    test_json POST "$BASE/v1/chat/completions" \
        '{"model":"nonexistent-model","messages":[{"role":"user","content":"Say hi"}],"max_tokens":8}' \
        '"model_not_found"' \
        "Wrong model name → error with available models" \
        400

    # With empty model field — should also error
    test_json POST "$BASE/v1/chat/completions" \
        '{"model":"","messages":[{"role":"user","content":"Say hi"}],"max_tokens":8}' \
        '"model_not_found"' \
        "Empty model field → error with available models" \
        400

    # With no model field — should also error
    test_json POST "$BASE/v1/chat/completions" \
        '{"messages":[{"role":"user","content":"Say hi"}],"max_tokens":8}' \
        '"model_not_found"' \
        "Missing model field → error with available models" \
        400

    # Get second model name and test routing to it specifically
    second_model=$(echo "$v1_body" | python3 -c "
import json,sys
try:
    d = json.load(sys.stdin)
    print(d['data'][1]['id'])
except:
    print('')
" 2>/dev/null)

    if [[ -n "$second_model" ]]; then
        test_json POST "$BASE/v1/chat/completions" \
            "{\"model\":\"$second_model\",\"messages\":[{\"role\":\"user\",\"content\":\"Say hi\"}],\"max_tokens\":8}" \
            '"choices"' \
            "Second model by name → completion"
    fi
fi

echo ""
echo "=== Results: ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC} ==="
[[ $FAIL -eq 0 ]] && exit 0 || exit 1
