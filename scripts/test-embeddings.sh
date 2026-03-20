#!/usr/bin/env bash
# Test embedding model support via the OpenAI-compatible embeddings API.
# Usage: ./scripts/test-embeddings.sh [model-name]

set -euo pipefail
source "$(dirname "$0")/lib-test.sh"

if [[ -n "${1:-}" ]]; then
    MODEL="$1"
else
    pick_model embedding
fi
echo ""

# Single string embedding
echo "--- Single input ---"
RESPONSE=$(curl -s "${BASE_URL}/embeddings" \
    -H "Content-Type: application/json" \
    -d "{\"model\": \"${MODEL}\", \"input\": \"The quick brown fox jumps over the lazy dog\"}")

ERROR=$(echo "$RESPONSE" | python3 -c "import sys,json; r=json.load(sys.stdin); print(r.get('error',{}).get('message',''))" 2>/dev/null || true)
if [[ -n "$ERROR" ]]; then
    echo "Error: ${ERROR}"
    echo "$RESPONSE" | python3 -m json.tool 2>/dev/null || echo "$RESPONSE"
    exit 1
fi

echo "$RESPONSE" | python3 -c "
import sys, json
r = json.load(sys.stdin)
emb = r['data'][0]['embedding']
print(f'Dimensions: {len(emb)}')
print(f'First 5 values: {emb[:5]}')
print(f'Model: {r.get(\"model\", \"unknown\")}')
print(f'Usage: {r.get(\"usage\", {})}')
" 2>/dev/null || echo "$RESPONSE" | python3 -m json.tool 2>/dev/null || echo "$RESPONSE"

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
    print(f'Error: {r[\"error\"][\"message\"]}')
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
