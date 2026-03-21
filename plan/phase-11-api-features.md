# Phase 11: API Features & Client Compatibility

Extend the management layer and API surface to improve client
compatibility (Open WebUI, Continue, etc.) and add performance features.

---

## 1. Model Info & PS Endpoints

**Goal:** Expose model metadata and loaded-model status at client-friendly
paths so tools like Open WebUI can discover capabilities without guessing.

**What we have:**
- Registry stores: arch, quant, context length, VRAM estimate, supports_tools,
  mmproj (vision), embedding detection
- Router returns model status (loaded/unloaded) via `GET /models`

**What to add:**

### GET /api/models/{id}/info
Returns enriched metadata for a single model:
```json
{
  "id": "unsloth--Qwen3.5-27B-GGUF--Qwen3.5-27B-Q4_K_M",
  "model_id": "unsloth/Qwen3.5-27B-GGUF",
  "arch": "qwen3",
  "quant": "Q4_K_M",
  "context_length": 131072,
  "size_bytes": 16400000000,
  "vram_est_gb": 12.3,
  "capabilities": ["chat", "tools", "vision"],
  "config": {
    "gpu_layers": 999,
    "context_size": 65536,
    "threads": 8,
    "flash_attention": true,
    "kv_cache_quant": "q8_0"
  }
}
```

### GET /api/ps
Returns currently loaded models with resource usage, similar to Ollama's
`/api/ps`:
```json
{
  "models": [
    {
      "name": "unsloth--Qwen3.5-27B-GGUF",
      "status": "loaded",
      "vram_est_gb": 12.3,
      "context_size": 65536
    }
  ]
}
```

Data sources: `process.ListModels()` for status, registry for metadata.

**Effort:** Small — data already exists, just wire into new handlers.

---

## 2. Reranking

**Goal:** Verify `/v1/rerank` works through our proxy and add a test script.

**What we have:**
- Our `/v1/*` proxy passes everything through to the router
- llama.cpp added `/v1/rerank` (cross-encoder scoring) in recent builds

**What to check:**
- Does `/v1/rerank` exist in the current build? (Try it — may just work)
- Does it need a flag like `--reranking` similar to `--embeddings`?
- If a flag is needed, auto-detect reranking models by name pattern
  (e.g., `rerank`, `cross-encoder`, `bge-reranker`) like we do for embeddings

**Expected request format (OpenAI-compatible):**
```json
{
  "model": "bge-reranker-v2-m3",
  "query": "What is deep learning?",
  "documents": [
    "Deep learning is a subset of machine learning...",
    "The weather today is sunny...",
    "Neural networks consist of layers..."
  ]
}
```

**What to add:**
- Test script: `scripts/test-rerank.sh`
- If flag needed: add to preset generation like `--embeddings`
- If model detection needed: add reranking patterns to auto-detect
- Curated reranking models in the embedding presets section (they're
  similarly small and purpose-built)

**Effort:** Very small if it just works through the proxy. Small if a flag
is needed.

---

## 3. Speculative Decoding

**Goal:** Let users pair a small "draft" model with a large model for
faster inference. The draft model proposes tokens that the main model
verifies in parallel — typically 2-3x speedup on large models.

**How it works in llama.cpp:**
- Preset INI: `model-draft = /path/to/small-model.gguf`
- Or CLI: `--model-draft /path/to/small-model.gguf`
- Additional params: `--draft-max` (max draft tokens, default 16),
  `--draft-min` (min draft tokens, default 2)
- The draft model should be the same architecture/family but smaller
  (e.g., Qwen3-0.6B as draft for Qwen3-30B)

**What to add:**

### ModelConfig fields
```go
DraftModelPath string `json:"draft_model_path,omitempty"`
DraftMax       int    `json:"draft_max,omitempty"`
DraftMin       int    `json:"draft_min,omitempty"`
```

### Auto-detection
When a user configures a model, show available draft candidates:
- Same arch family (Qwen ↔ Qwen, Llama ↔ Llama)
- Significantly smaller (< 25% of the main model's size)
- Already downloaded

### Preset generation
```ini
[qwen3-30b]
model = /data/models/.../Qwen3-30B-Q4_K_M.gguf
model-draft = /data/models/.../Qwen3-0.6B-Q8_0.gguf
draft-max = 16
draft-min = 2
```

### UI
- Add "Draft Model" dropdown in the config form (filtered to compatible
  models)
- Show draft-max / draft-min sliders
- Show "speculative" badge on model cards with a draft model configured

### Test script
`scripts/test-speculative.sh` — compare generation speed with and without
draft model on the same prompt.

**Effort:** Medium — similar pattern to mmproj (config field + preset
generation + UI picker), but needs the arch-matching logic for the
draft model selector.

---

## 4. Model Aliases / Tags

**Goal:** Let users refer to models with friendly names like
`qwen3:latest` or `my-coding-model` instead of full registry IDs like
`unsloth--Qwen3.5-27B-GGUF--Qwen3.5-27B-Q4_K_M`.

**How aliases work today:**
- The preset.ini `alias =` line already includes the section name and
  registry ID
- The router matches on any alias for inference requests
- But these are auto-generated, not user-friendly

**What to add:**

### ModelConfig field
```go
Aliases []string `json:"aliases,omitempty"` // user-defined friendly names
```

### Preset generation
Append user aliases to the `alias =` line:
```ini
alias = section-name,registry-id,qwen3:latest,my-chat-model
```

### UI
- Add "Aliases" text input in the config form (comma-separated)
- Show aliases on model cards as small tags
- Autocomplete or suggestions based on common patterns:
  `{family}:{quant}`, `{family}:latest`

### API compatibility
Clients can then use `"model": "qwen3:latest"` in requests, matching
Ollama's convention.

**Effort:** Small — one config field, one line in preset generation,
one input in the config form.

---

## Future Work (Not in Scope)

### LoRA Adapter Management
- llama.cpp supports `--lora /path/to/adapter.gguf` at launch
- Could add as a config field like mmproj/draft model
- Per-request adapter switching is more complex and may not work in
  router mode
- Defer until there's a concrete use case

### STT / Whisper
- Requires whisper.cpp (separate binary, separate build pipeline)
- Different model format (not GGUF — though whisper GGUF does exist)
- Needs a second process manager
- Significant infrastructure work — treat as a separate project

---

## Execution Order

| Phase | Items | Effort |
|-------|-------|--------|
| **11a** | Model info + PS endpoints | Small |
| **11b** | Reranking verification + test | Very small |
| **11c** | Speculative decoding | Medium |
| **11d** | Model aliases/tags | Small |
