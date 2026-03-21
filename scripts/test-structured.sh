#!/usr/bin/env bash
# Test structured output / JSON schema constrained generation.
# Usage: ./scripts/test-structured.sh [--host HOST] [--port PORT] [model-name]

set -euo pipefail
source "$(dirname "$0")/lib-test.sh"

if [[ ${#ARGS[@]} -gt 0 ]]; then
    MODEL="${ARGS[0]}"
else
    pick_model chat
fi
echo ""

# --- Test 1: response_format json_schema ---
echo "--- JSON Schema (response_format) ---"
RESPONSE=$(curl -s "${BASE_URL}/chat/completions" \
    -H "Content-Type: application/json" \
    -d "{
        \"model\": \"${MODEL}\",
        \"messages\": [{
            \"role\": \"user\",
            \"content\": \"List 3 colors with their hex codes.\"
        }],
        \"max_tokens\": 256,
        \"response_format\": {
            \"type\": \"json_schema\",
            \"json_schema\": {
                \"name\": \"color_list\",
                \"strict\": true,
                \"schema\": {
                    \"type\": \"object\",
                    \"properties\": {
                        \"colors\": {
                            \"type\": \"array\",
                            \"items\": {
                                \"type\": \"object\",
                                \"properties\": {
                                    \"name\": {\"type\": \"string\"},
                                    \"hex\": {\"type\": \"string\"}
                                },
                                \"required\": [\"name\", \"hex\"]
                            }
                        }
                    },
                    \"required\": [\"colors\"]
                }
            }
        }
    }")

echo "$RESPONSE" | jq .

CONTENT=$(echo "$RESPONSE" | jq -r '.choices[0].message.content // empty' 2>/dev/null || true)
if [[ -n "$CONTENT" ]] && echo "$CONTENT" | jq -e '.colors | length > 0' &>/dev/null; then
    echo ""
    echo "Parsed colors:"
    echo "$CONTENT" | jq -r '.colors[] | "  \(.name): \(.hex)"'
    echo ""
    echo "Schema validation: PASS"
else
    echo ""
    echo "Schema validation: FAIL"
fi

# --- Test 2: response_format json_object (simpler) ---
echo ""
echo "--- JSON Object (response_format type=json_object) ---"
RESPONSE=$(curl -s "${BASE_URL}/chat/completions" \
    -H "Content-Type: application/json" \
    -d "{
        \"model\": \"${MODEL}\",
        \"messages\": [{
            \"role\": \"user\",
            \"content\": \"Return a JSON object with fields: name (string), age (number), hobbies (array of strings). Make up a person.\"
        }],
        \"max_tokens\": 256,
        \"response_format\": {\"type\": \"json_object\"}
    }")

echo "$RESPONSE" | jq .

CONTENT=$(echo "$RESPONSE" | jq -r '.choices[0].message.content // empty' 2>/dev/null || true)
if [[ -n "$CONTENT" ]] && echo "$CONTENT" | jq empty &>/dev/null; then
    echo ""
    echo "Parsed object:"
    echo "$CONTENT" | jq .
    echo ""
    echo "JSON parsing: PASS"
else
    echo ""
    echo "JSON parsing: FAIL"
fi
