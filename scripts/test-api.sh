#!/usr/bin/env bash
# Quick smoke test for LlamaCtl API endpoints.
# Usage: ./scripts/test-api.sh [base_url]

BASE="${1:-http://localhost:3000}"
PASS=0
FAIL=0

echo "Testing LlamaCtl at $BASE"
echo ""

test_endpoint() {
    local method="$1" url="$2" expect="$3"
    echo "  $ curl -s -o /dev/null -w '%{http_code}' -X $method $url"
    code=$(curl -s -o /dev/null -w "%{http_code}" -X "$method" "$url")
    if [[ "$code" == "$expect" ]]; then
        echo "    → $code ✓"
        ((PASS++))
    else
        echo "    → $code ✗ (expected $expect)"
        ((FAIL++))
    fi
    echo ""
}

echo "=== Pages ==="
test_endpoint GET "$BASE/"
test_endpoint GET "$BASE/builds"         200
test_endpoint GET "$BASE/models"         200
test_endpoint GET "$BASE/models/browse"  200
test_endpoint GET "$BASE/service"        200
test_endpoint GET "$BASE/settings"       200

echo "=== API ==="
test_endpoint GET  "$BASE/api/dashboard"        200
test_endpoint GET  "$BASE/api/builds/"           200
test_endpoint GET  "$BASE/api/builds/backends"   200
test_endpoint GET  "$BASE/api/models/"           200
test_endpoint GET  "$BASE/api/service/status"    200
test_endpoint GET  "$BASE/api/service/health"    200
test_endpoint GET  "$BASE/api/settings/"         200
test_endpoint POST "$BASE/api/settings/test-connection" 200

echo "=== OpenAI Proxy (/v1) ==="
echo "  $ curl -s -w '\\nHTTP %{http_code}' $BASE/v1/models"
resp=$(curl -s -w "\nHTTP %{http_code}" "$BASE/v1/models")
code=$(echo "$resp" | tail -1 | awk '{print $2}')
body=$(echo "$resp" | sed '$d')
echo "    → HTTP $code"
echo "    $body" | head -5
if [[ "$code" == "200" || "$code" == "503" ]]; then
    ((PASS++))
    echo "    ✓"
else
    echo "    ✗ (expected 200 or 503)"
    ((FAIL++))
fi
echo ""

if [[ "$code" == "200" ]]; then
    echo "=== Chat Completion ==="
    PAYLOAD='{"model":"test","messages":[{"role":"user","content":"Say hello in 5 words."}],"max_tokens":32}'
    echo "  $ curl -s $BASE/v1/chat/completions \\"
    echo "      -H 'Content-Type: application/json' \\"
    echo "      -d '$PAYLOAD'"
    echo ""
    resp=$(curl -s "$BASE/v1/chat/completions" \
        -H "Content-Type: application/json" \
        -d "$PAYLOAD")
    echo "  Response:"
    echo "$resp" | python3 -m json.tool 2>/dev/null || echo "  $resp"
    echo ""
    if echo "$resp" | grep -q '"choices"'; then
        echo "    ✓ Got valid chat completion"
        ((PASS++))
    else
        echo "    ✗ No 'choices' in response"
        ((FAIL++))
    fi
    echo ""
fi

echo "=== Results: $PASS passed, $FAIL failed ==="
[[ $FAIL -eq 0 ]] && exit 0 || exit 1
