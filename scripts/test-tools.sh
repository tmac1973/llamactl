#!/usr/bin/env bash
# Test tool/function calling via the OpenAI-compatible chat completions API.
# Usage: ./scripts/test-tools.sh [model-name]

set -euo pipefail
source "$(dirname "$0")/lib-test.sh"

if [[ -n "${1:-}" ]]; then
    MODEL="$1"
else
    pick_model chat
fi
echo ""

# --- Test 1: Model should call a tool ---
echo "--- Tool call (expecting function call) ---"
RESPONSE=$(curl -s "${BASE_URL}/chat/completions" \
    -H "Content-Type: application/json" \
    -d "{
        \"model\": \"${MODEL}\",
        \"messages\": [{
            \"role\": \"user\",
            \"content\": \"What's the weather like in San Francisco?\"
        }],
        \"max_tokens\": 256,
        \"tools\": [{
            \"type\": \"function\",
            \"function\": {
                \"name\": \"get_weather\",
                \"description\": \"Get the current weather for a location\",
                \"parameters\": {
                    \"type\": \"object\",
                    \"properties\": {
                        \"location\": {
                            \"type\": \"string\",
                            \"description\": \"City name\"
                        },
                        \"unit\": {
                            \"type\": \"string\",
                            \"enum\": [\"celsius\", \"fahrenheit\"]
                        }
                    },
                    \"required\": [\"location\"]
                }
            }
        }],
        \"tool_choice\": \"auto\"
    }")

echo "$RESPONSE" | python3 -c "
import sys, json

r = json.load(sys.stdin)
if 'error' in r:
    print(f'Error: {r[\"error\"][\"message\"]}')
    sys.exit(1)

msg = r['choices'][0]['message']
role = msg.get('role', '')
content = msg.get('content', '')
tool_calls = msg.get('tool_calls', [])

if tool_calls:
    print(f'Role: {role}')
    print(f'Tool calls: {len(tool_calls)}')
    for tc in tool_calls:
        fn = tc['function']
        args = json.loads(fn['arguments']) if isinstance(fn['arguments'], str) else fn['arguments']
        print(f'  Function: {fn[\"name\"]}')
        print(f'  Arguments: {json.dumps(args, indent=4)}')
        print(f'  Call ID: {tc.get(\"id\", \"n/a\")}')
    print()
    print('Tool calling: PASS')
else:
    print(f'Role: {role}')
    print(f'Content: {content[:200]}')
    print()
    print('Tool calling: FAIL (model responded with text instead of tool call)')
    print('This may indicate the model does not support tool calling,')
    print('or the chat template does not handle tools correctly.')
" 2>/dev/null || {
    echo "Failed to parse response:"
    echo "$RESPONSE" | python3 -m json.tool 2>/dev/null || echo "$RESPONSE"
}

# --- Test 2: Multi-turn with tool result ---
echo ""
echo "--- Multi-turn with tool result ---"
RESPONSE=$(curl -s "${BASE_URL}/chat/completions" \
    -H "Content-Type: application/json" \
    -d "{
        \"model\": \"${MODEL}\",
        \"messages\": [
            {\"role\": \"user\", \"content\": \"What's the weather in Tokyo?\"},
            {\"role\": \"assistant\", \"content\": null, \"tool_calls\": [{
                \"id\": \"call_1\",
                \"type\": \"function\",
                \"function\": {\"name\": \"get_weather\", \"arguments\": \"{\\\"location\\\": \\\"Tokyo\\\"}\"}}]},
            {\"role\": \"tool\", \"tool_call_id\": \"call_1\", \"content\": \"{\\\"temp\\\": 22, \\\"unit\\\": \\\"celsius\\\", \\\"condition\\\": \\\"partly cloudy\\\"}\"}
        ],
        \"max_tokens\": 256
    }")

echo "$RESPONSE" | python3 -c "
import sys, json

r = json.load(sys.stdin)
if 'error' in r:
    print(f'Error: {r[\"error\"][\"message\"]}')
    sys.exit(1)

msg = r['choices'][0]['message']
content = msg.get('content', '')
print(f'Model response: {content[:300]}')

# Check that model incorporated the tool result
lower = content.lower()
if any(w in lower for w in ['22', 'tokyo', 'cloudy', 'celsius']):
    print()
    print('Tool result integration: PASS')
else:
    print()
    print('Tool result integration: UNCLEAR (response may not reference the tool data)')
" 2>/dev/null || {
    echo "Failed to parse response:"
    echo "$RESPONSE" | python3 -m json.tool 2>/dev/null || echo "$RESPONSE"
}
