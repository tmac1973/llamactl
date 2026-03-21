#!/usr/bin/env bash
# Test tool/function calling via the OpenAI-compatible chat completions API.
# Usage: ./scripts/test-tools.sh [--host HOST] [--port PORT] [model-name]

set -euo pipefail
source "$(dirname "$0")/lib-test.sh"

if [[ ${#ARGS[@]} -gt 0 ]]; then
    MODEL="${ARGS[0]}"
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

echo "$RESPONSE" | jq .

TOOL_CALLS=$(echo "$RESPONSE" | jq '.choices[0].message.tool_calls // [] | length' 2>/dev/null || echo 0)
if [[ "$TOOL_CALLS" -gt 0 ]]; then
    echo ""
    echo "Tool calling: PASS"
else
    echo ""
    echo "Tool calling: FAIL (model responded with text instead of tool call)"
    echo "This may indicate the model does not support tool calling,"
    echo "or the chat template does not handle tools correctly."
fi

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

echo "$RESPONSE" | jq .

CONTENT=$(echo "$RESPONSE" | jq -r '.choices[0].message.content // ""' 2>/dev/null)
if echo "$CONTENT" | grep -qiE '22|tokyo|cloudy|celsius'; then
    echo ""
    echo "Tool result integration: PASS"
else
    echo ""
    echo "Tool result integration: UNCLEAR (response may not reference the tool data)"
fi
