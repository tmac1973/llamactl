#!/usr/bin/env bash
# Test embedding model support via the OpenAI-compatible embeddings API.
# Usage: ./scripts/test-embeddings.sh [--host HOST] [--port PORT] [model-name]

set -euo pipefail
source "$(dirname "$0")/lib-test.sh"

if [[ ${#ARGS[@]} -gt 0 ]]; then
    MODEL="${ARGS[0]}"
else
    pick_model embedding
fi
echo ""

# Single string embedding
echo "--- Single input ---"
RESPONSE=$(curl -s "${BASE_URL}/embeddings" \
    -H "Content-Type: application/json" \
    -d "{\"model\": \"${MODEL}\", \"input\": \"The quick brown fox jumps over the lazy dog\"}")

if echo "$RESPONSE" | jq -e '.error' &>/dev/null; then
    echo "$RESPONSE" | jq .
    exit 1
fi

echo "$RESPONSE" | jq '{
    dimensions: (.data[0].embedding | length),
    first_5: (.data[0].embedding[:5]),
    model: .model,
    usage: .usage
}'

# Batch similarity test
echo ""
echo "--- Batch similarity test ---"
RESPONSE=$(curl -s "${BASE_URL}/embeddings" \
    -H "Content-Type: application/json" \
    -d "{\"model\": \"${MODEL}\", \"input\": [\"cat\", \"kitten\", \"automobile\"]}")

python3 -c "
import sys, json, math

r = json.load(sys.stdin)
if 'error' in r:
    print(json.dumps(r, indent=2))
    sys.exit(1)

embeddings = [d['embedding'] for d in r['data']]
labels = ['cat', 'kitten', 'automobile']

def cosine_sim(a, b):
    dot = sum(x*y for x,y in zip(a,b))
    na = math.sqrt(sum(x*x for x in a))
    nb = math.sqrt(sum(x*x for x in b))
    return dot / (na * nb) if na and nb else 0

for i in range(len(labels)):
    for j in range(i+1, len(labels)):
        sim = cosine_sim(embeddings[i], embeddings[j])
        print(f'  {labels[i]} <-> {labels[j]}: {sim:.4f}')

print()
print('(cat<->kitten should be much higher than cat<->automobile)')
" <<< "$RESPONSE"
