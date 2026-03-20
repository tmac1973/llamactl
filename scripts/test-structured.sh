#!/usr/bin/env bash
# Test structured output / JSON schema constrained generation.
# Usage: ./scripts/test-structured.sh [model-name]

set -euo pipefail
source "$(dirname "$0")/lib-test.sh"

if [[ -n "${1:-}" ]]; then
    MODEL="$1"
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

CONTENT=$(echo "$RESPONSE" | python3 -c "
import sys, json
r = json.load(sys.stdin)
if 'error' in r:
    print(f'Error: {r[\"error\"][\"message\"]}')
    sys.exit(1)
content = r['choices'][0]['message']['content']
print('Raw:', content[:200])
# Verify it's valid JSON matching the schema
parsed = json.loads(content)
colors = parsed['colors']
print(f'Parsed {len(colors)} colors:')
for c in colors:
    print(f'  {c[\"name\"]}: {c[\"hex\"]}')
print()
print('Schema validation: PASS')
" 2>/dev/null)

if [[ -n "$CONTENT" ]]; then
    echo "$CONTENT"
else
    echo "Failed to parse response:"
    echo "$RESPONSE" | python3 -m json.tool 2>/dev/null || echo "$RESPONSE"
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

echo "$RESPONSE" | python3 -c "
import sys, json
r = json.load(sys.stdin)
if 'error' in r:
    print(f'Error: {r[\"error\"][\"message\"]}')
    sys.exit(1)
content = r['choices'][0]['message']['content']
parsed = json.loads(content)
print(json.dumps(parsed, indent=2))
print()
print('JSON parsing: PASS')
" 2>/dev/null || {
    echo "Failed:"
    echo "$RESPONSE" | python3 -m json.tool 2>/dev/null || echo "$RESPONSE"
}
