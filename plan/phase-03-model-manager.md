# Phase 3 — Model Manager

> HuggingFace API, resumable downloader, VRAM estimator, local model registry

---

## Goal

Users can search HuggingFace for GGUF models, see VRAM estimates per quantization variant, download models with resumable progress tracking, and manage a local registry of downloaded models.

---

## Deliverables

- HuggingFace API client (search, model info, file listing)
- Resumable GGUF downloader with progress streaming via SSE
- VRAM estimation based on quantization type and parameter count
- Local model registry (JSON manifest)
- Model browser UI (HF search + local models list)
- Download progress UI with htmx + SSE

---

## Files Created / Modified

### `internal/huggingface/client.go`

HuggingFace API client for searching and fetching model metadata.

```go
package huggingface

import (
    "context"
    "net/http"
)

type Client struct {
    httpClient *http.Client
    token      string // optional HF API token
}

type ModelSearchResult struct {
    ID        string `json:"id"`          // "bartowski/Qwen2.5-72B-Instruct-GGUF"
    Author    string `json:"author"`
    Downloads int    `json:"downloads"`
    Likes     int    `json:"likes"`
    Tags      []string `json:"tags"`
    License   string `json:"license,omitempty"`
}

type ModelFile struct {
    Filename string `json:"filename"`
    Size     int64  `json:"size"`
    Quant    string `json:"quant"`     // parsed from filename: "Q4_K_M", "Q8_0", etc.
    VRAMEstGB float64 `json:"vram_est_gb"` // estimated VRAM needed
}

type ModelDetail struct {
    ID     string      `json:"id"`
    Files  []ModelFile `json:"files"`
    Params int64       `json:"params,omitempty"` // parameter count if available
}

func NewClient(token string) *Client { ... }

// Search queries HuggingFace API with GGUF filter
// GET https://huggingface.co/api/models?search=<q>&filter=gguf&limit=20
func (c *Client) Search(ctx context.Context, query string) ([]ModelSearchResult, error) { ... }

// GetModel fetches model details and filters for .gguf files
// GET https://huggingface.co/api/models/<id>
// Parses sibling filenames, extracts quantization type, computes VRAM estimates
func (c *Client) GetModel(ctx context.Context, modelID string) (*ModelDetail, error) { ... }
```

### `internal/huggingface/downloader.go`

Resumable GGUF download manager.

```go
package huggingface

import (
    "context"
    "sync"
)

type DownloadStatus struct {
    ID              string  `json:"id"`
    ModelID         string  `json:"model_id"`
    Filename        string  `json:"filename"`
    BytesDownloaded int64   `json:"bytes_downloaded"`
    TotalBytes      int64   `json:"total_bytes"`
    SpeedBPS        int64   `json:"speed_bps"`
    Status          string  `json:"status"` // "downloading", "complete", "failed", "paused"
    Error           string  `json:"error,omitempty"`
}

type Downloader struct {
    dataDir    string
    token      string
    mu         sync.Mutex
    active     map[string]*download // download ID → state
    progressCh map[string]chan DownloadStatus
}

func NewDownloader(dataDir, token string) *Downloader { ... }

// Start begins a download. Returns immediately; progress sent to channel.
// Download URL: https://huggingface.co/<model_id>/resolve/main/<filename>
// Supports resume via Content-Range / partial .part files
func (d *Downloader) Start(ctx context.Context, modelID, filename string) (string, error) { ... }

// ProgressChannel returns the SSE channel for a download
func (d *Downloader) ProgressChannel(downloadID string) (<-chan DownloadStatus, bool) { ... }

// Cancel stops an active download
func (d *Downloader) Cancel(downloadID string) error { ... }
```

Download flow:
1. Create download dir: `/data/models/<model-id>/`
2. Check for existing `.part` file → resume with `Range` header
3. Stream response body to `.part` file, updating progress channel
4. On completion: rename `.part` → `model.gguf`
5. Write `meta.json` with model metadata
6. Register in model registry

### `internal/models/registry.go`

Local model registry backed by JSON.

```go
package models

import (
    "sync"
    "time"
)

type Model struct {
    ID          string    `json:"id"`           // "<author>--<model>--<quant>"
    ModelID     string    `json:"model_id"`     // HF model ID "author/model"
    Filename   string    `json:"filename"`
    Quant      string    `json:"quant"`
    SizeBytes  int64     `json:"size_bytes"`
    FilePath   string    `json:"file_path"`     // absolute path to .gguf
    VRAMEstGB  float64   `json:"vram_est_gb"`
    DownloadedAt time.Time `json:"downloaded_at"`
}

type ModelConfig struct {
    GPULayers   int    `json:"gpu_layers"`    // default 999 (all layers)
    TensorSplit string `json:"tensor_split"`  // e.g. "0.5,0.5"
    ContextSize int    `json:"context_size"`  // e.g. 8192
    Threads     int    `json:"threads"`       // e.g. 8
    ExtraFlags  string `json:"extra_flags"`   // e.g. "--flash-attn"
    BuildID     string `json:"build_id"`      // which compiled build to use
}

type Registry struct {
    mu       sync.RWMutex
    dataDir  string
    models   map[string]*Model      // model ID → model
    configs  map[string]*ModelConfig // model ID → launch config
    // Persisted to /data/config/models.json
}

func NewRegistry(dataDir string) (*Registry, error) { ... }
func (r *Registry) Add(m *Model) error { ... }
func (r *Registry) List() []*Model { ... }
func (r *Registry) Get(id string) (*Model, error) { ... }
func (r *Registry) Delete(id string) error { ... }            // removes files + entry
func (r *Registry) GetConfig(id string) (*ModelConfig, error) { ... }
func (r *Registry) SetConfig(id string, cfg *ModelConfig) error { ... }
```

### `internal/models/vram.go`

VRAM estimation from quantization type.

```go
package models

// EstimateVRAM returns estimated VRAM in GB for a model file.
// Formula: file_size_bytes * 1.1 (overhead for KV cache and runtime buffers)
// For more accurate estimation with known param count:
//   param_count * bits_per_weight / 8 * 1.2
//
// Quant bits per weight (approximate):
//   F16: 16, Q8_0: 8.5, Q6_K: 6.6, Q5_K_M: 5.7
//   Q5_K_S: 5.5, Q4_K_M: 4.8, Q4_K_S: 4.5
//   Q3_K_M: 3.9, Q3_K_S: 3.5, Q2_K: 3.4
//   IQ4_XS: 4.3, IQ3_XXS: 3.1, IQ2_XXS: 2.5
func EstimateVRAM(sizeBytes int64) float64 { ... }

// ParseQuant extracts quantization type from a GGUF filename.
// e.g. "Qwen2.5-72B-Instruct-Q4_K_M.gguf" → "Q4_K_M"
func ParseQuant(filename string) string { ... }

// VRAMFitCategory returns a category based on VRAM estimate and available GPU memory.
// "single" = fits on one GPU (32GB), "dual" = fits across two (64GB), "too_large"
func VRAMFitCategory(estimatedGB float64) string { ... }
```

### `internal/api/hf.go`

HuggingFace proxy handlers.

```go
package api

// Mounted under r.Route("/api/hf", ...)

// GET  /api/hf/search          → search HF models (?q=query) → JSON
// GET  /api/hf/model           → model detail (?id=owner/name) → JSON
// POST /api/hf/download        → start download (JSON body) → returns download ID
// GET  /api/hf/download/{id}/progress → SSE download progress stream
// DELETE /api/hf/download/{id} → cancel active download

func (s *Server) handleHFSearch(w http.ResponseWriter, r *http.Request)
func (s *Server) handleHFModel(w http.ResponseWriter, r *http.Request)
func (s *Server) handleHFDownload(w http.ResponseWriter, r *http.Request)
func (s *Server) handleHFDownloadProgress(w http.ResponseWriter, r *http.Request)
func (s *Server) handleHFDownloadCancel(w http.ResponseWriter, r *http.Request)
```

### `internal/api/models.go`

Local model management handlers.

```go
package api

// Mounted under r.Route("/api/models", ...)

// GET    /api/models         → list local models → JSON
// GET    /api/models/{id}    → model detail → JSON
// DELETE /api/models/{id}    → delete model files + registry entry

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request)
func (s *Server) handleGetModel(w http.ResponseWriter, r *http.Request)
func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request)
```

### `web/templates/models.html`

Local models page.

```html
{{define "content"}}
<h1>Local Models</h1>

<div id="model-list" hx-get="/api/models" hx-trigger="load" hx-swap="innerHTML">
    Loading...
</div>
{{end}}
```

### `web/templates/models_browse.html`

HuggingFace model browser with search.

```html
{{define "content"}}
<h1>Browse HuggingFace</h1>

<form hx-get="/api/hf/search"
      hx-target="#hf-results"
      hx-swap="innerHTML"
      hx-indicator="#search-spinner">
    <div class="grid">
        <input type="search" name="q" placeholder="Search GGUF models..." autofocus>
        <button type="submit">Search</button>
    </div>
</form>

<span id="search-spinner" class="htmx-indicator" aria-busy="true">Searching...</span>

<div id="hf-results"></div>
<div id="hf-detail"></div>
<div id="download-progress"></div>
{{end}}
```

### `web/templates/partials/model_card.html`

Single local model displayed as a table row.

```html
{{define "model_card"}}
<tr>
    <td>{{.ModelID}}</td>
    <td><kbd>{{.Quant}}</kbd></td>
    <td>{{printf "%.1f" .VRAMEstGB}} GB</td>
    <td>{{printf "%.1f" (divGB .SizeBytes)}} GB</td>
    <td>
        <div role="group">
            <button hx-put="/api/models/{{.ID}}/activate"
                    hx-target="#service-status"
                    hx-swap="innerHTML">
                Activate
            </button>
            <button hx-delete="/api/models/{{.ID}}"
                    hx-confirm="Delete this model?"
                    hx-target="closest tr"
                    hx-swap="outerHTML"
                    class="secondary">
                Delete
            </button>
        </div>
    </td>
</tr>
{{end}}
```

### `web/templates/partials/hf_results.html`

Search results from HuggingFace.

```html
{{define "hf_results"}}
{{if .Results}}
<table>
    <thead>
        <tr><th>Model</th><th>Downloads</th><th>Likes</th><th>License</th><th></th></tr>
    </thead>
    <tbody>
        {{range .Results}}
        <tr>
            <td>{{.ID}}</td>
            <td>{{.Downloads}}</td>
            <td>{{.Likes}}</td>
            <td>{{.License}}</td>
            <td>
                <button hx-get="/api/hf/model?id={{.ID}}"
                        hx-target="#hf-detail"
                        hx-swap="innerHTML"
                        class="outline">
                    Files
                </button>
            </td>
        </tr>
        {{end}}
    </tbody>
</table>
{{else}}
<p>No GGUF models found.</p>
{{end}}
{{end}}
```

### `web/templates/partials/hf_files.html`

File listing for a HuggingFace model, with VRAM estimates and download buttons.

```html
{{define "hf_files"}}
<article>
    <header>{{.ID}} — Files</header>
    <table>
        <thead>
            <tr><th>File</th><th>Quant</th><th>Size</th><th>VRAM Est.</th><th>Fit</th><th></th></tr>
        </thead>
        <tbody>
            {{range .Files}}
            <tr>
                <td><small>{{.Filename}}</small></td>
                <td><kbd>{{.Quant}}</kbd></td>
                <td>{{printf "%.1f" (divGB .Size)}} GB</td>
                <td>{{printf "%.1f" .VRAMEstGB}} GB</td>
                <td>
                    {{if eq (vramFit .VRAMEstGB) "single"}}
                        <ins>1 GPU</ins>
                    {{else if eq (vramFit .VRAMEstGB) "dual"}}
                        <mark>2 GPU</mark>
                    {{else}}
                        <del>Too Large</del>
                    {{end}}
                </td>
                <td>
                    <button hx-post="/api/hf/download"
                            hx-vals='{"model_id":"{{$.ID}}","filename":"{{.Filename}}"}'
                            hx-target="#download-progress"
                            hx-swap="innerHTML">
                        Download
                    </button>
                </td>
            </tr>
            {{end}}
        </tbody>
    </table>
</article>
{{end}}
```

### `web/templates/partials/download_progress.html`

Download progress display with SSE connection.

```html
{{define "download_progress"}}
<article>
    <header>Downloading {{.Filename}}</header>
    <div hx-ext="sse"
         sse-connect="/api/hf/download/{{.DownloadID}}/progress"
         sse-swap="progress"
         hx-swap="innerHTML">
        <progress></progress>
        <small>Starting...</small>
    </div>
</article>
{{end}}
```

---

## Server.go Modifications

Add HF client, downloader, and registry to `Server`:

```go
type Server struct {
    cfg        *config.Config
    templates  *template.Template
    router     chi.Router
    builder    *builder.Builder
    hfClient   *huggingface.Client   // added
    downloader *huggingface.Downloader // added
    registry   *models.Registry       // added
}

// In buildRouter():
r.Route("/api/hf", func(r chi.Router) {
    r.Get("/search", s.handleHFSearch)
    r.Get("/model", s.handleHFModel)
    r.Post("/download", s.handleHFDownload)
    r.Get("/download/{id}/progress", s.handleHFDownloadProgress)
    r.Delete("/download/{id}", s.handleHFDownloadCancel)
})

r.Route("/api/models", func(r chi.Router) {
    r.Get("/", s.handleListModels)
    r.Get("/{id}", s.handleGetModel)
    r.Delete("/{id}", s.handleDeleteModel)
})
```

---

## Template Functions

Add custom functions to the template FuncMap:

```go
funcMap := template.FuncMap{
    "divGB": func(bytes int64) float64 {
        return float64(bytes) / (1024 * 1024 * 1024)
    },
    "vramFit": models.VRAMFitCategory,
}
```

---

## What You Can Do at End of Phase

- Visit `/models/browse` → search HuggingFace for GGUF models
- Click a model → see file list with quantization, size, and VRAM estimates
- VRAM badges show green (1 GPU), blue (2 GPU), or red (too large)
- Click Download → progress bar streams via SSE with speed and ETA
- Download resumes if interrupted (partial file support)
- Visit `/models` → see all downloaded models with size, quant, VRAM info
- Delete downloaded models
- Model registry persisted to `/data/config/models.json`
