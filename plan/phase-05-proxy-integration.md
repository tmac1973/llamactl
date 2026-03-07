# Phase 5 — Proxy & Settings

> OpenAI /v1/* proxy, API key auth, connection test, settings page

---

## Goal

LlamaCtl proxies all OpenAI-compatible API requests (`/v1/*`) to the running llama-server instance, with optional API key authentication. A settings page lets users configure the proxy, manage their HuggingFace token, and test connectivity.

---

## Deliverables

- Reverse proxy for `/v1/*` → llama-server's OpenAI-compatible API
- Optional API key middleware (Bearer token auth)
- Connection test endpoint
- Settings page with proxy config, API key management, and HF token
- Display the proxy endpoint URL for copying into other tools

---

## Files Created / Modified

### `internal/api/proxy.go`

Reverse proxy handler for OpenAI-compatible API passthrough.

```go
package api

import (
    "fmt"
    "net/http"
    "net/http/httputil"
    "net/url"
)

// newProxyHandler creates a reverse proxy to the llama-server OpenAI API.
// All requests to /v1/* on the LlamaCtl port are forwarded to
// http://localhost:<llama_port>/v1/* on the llama-server port.
func (s *Server) newProxyHandler() http.Handler {
    target := &url.URL{
        Scheme: "http",
        Host:   fmt.Sprintf("localhost:%d", s.cfg.LlamaPort),
    }

    proxy := httputil.NewSingleHostReverseProxy(target)

    // Custom error handler: return 503 JSON when llama-server is down
    proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusServiceUnavailable)
        fmt.Fprintf(w, `{"error":{"message":"llama-server is not running","type":"server_error","code":"service_unavailable"}}`)
    }

    return proxy
}
```

### `internal/api/middleware.go` — Modified

Add API key authentication middleware.

```go
package api

import (
    "net/http"
    "strings"
)

// apiKeyAuth returns middleware that checks for a valid Bearer token.
// If no API key is configured, all requests are allowed.
func (s *Server) apiKeyAuth(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if s.cfg.APIKey == "" {
            next.ServeHTTP(w, r)
            return
        }

        auth := r.Header.Get("Authorization")
        if !strings.HasPrefix(auth, "Bearer ") {
            http.Error(w, `{"error":{"message":"missing API key","type":"auth_error"}}`,
                http.StatusUnauthorized)
            return
        }

        token := strings.TrimPrefix(auth, "Bearer ")
        if token != s.cfg.APIKey {
            http.Error(w, `{"error":{"message":"invalid API key","type":"auth_error"}}`,
                http.StatusUnauthorized)
            return
        }

        next.ServeHTTP(w, r)
    })
}
```

### `internal/api/server.go` — Modified

Mount proxy and settings routes:

```go
// In buildRouter():

// OpenAI-compatible proxy with optional API key auth
proxy := s.newProxyHandler()
r.Route("/v1", func(r chi.Router) {
    r.Use(s.apiKeyAuth)
    r.Handle("/*", proxy)
})

// Settings API
r.Route("/api/settings", func(r chi.Router) {
    r.Get("/", s.handleGetSettings)
    r.Put("/", s.handleUpdateSettings)
    r.Post("/test-connection", s.handleTestConnection)
})
```

### `internal/api/settings.go` — New

Settings handlers for configuration management.

```go
package api

// GET  /api/settings           → current settings (JSON, secrets redacted)
// PUT  /api/settings           → update settings (JSON body)
// POST /api/settings/test-connection → test llama-server connectivity

type SettingsResponse struct {
    ListenAddr    string `json:"listen_addr"`
    LlamaPort     int    `json:"llama_port"`
    ProxyEndpoint string `json:"proxy_endpoint"` // computed: "http://<host>:3000/v1"
    HasAPIKey     bool   `json:"has_api_key"`     // true if API key is set (don't expose key)
    HasHFToken    bool   `json:"has_hf_token"`    // true if HF token is set
    DataDir       string `json:"data_dir"`
}

type SettingsUpdate struct {
    LlamaPort *int    `json:"llama_port,omitempty"`
    APIKey    *string `json:"api_key,omitempty"`    // empty string = remove
    HFToken   *string `json:"hf_token,omitempty"`   // empty string = remove
}

type ConnectionTestResult struct {
    OK      bool     `json:"ok"`
    Models  []string `json:"models,omitempty"` // model IDs from /v1/models
    Error   string   `json:"error,omitempty"`
    Latency string   `json:"latency"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request)
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request)
func (s *Server) handleTestConnection(w http.ResponseWriter, r *http.Request)
```

**Test connection flow:**
1. Send GET to `http://localhost:<llama_port>/v1/models`
2. Measure response time
3. Parse response for model list
4. Return success/failure with latency

**Settings update flow:**
1. Validate input
2. Update in-memory config
3. Write updated YAML to `/data/config/llamactl.yaml`
4. If llama_port changed, warn that proxy target has changed (no restart needed — proxy uses config dynamically)

### `web/templates/settings.html`

Settings page.

```html
{{define "content"}}
<h1>Settings</h1>

<article>
    <header>OpenAI-Compatible Proxy</header>
    <p>Point your tools at this endpoint:</p>
    <pre id="proxy-url">{{.ProxyEndpoint}}</pre>

    <form hx-post="/api/settings/test-connection"
          hx-target="#connection-result"
          hx-swap="innerHTML">
        <div class="grid">
            <label>
                llama-server Port
                <input type="number" name="llama_port" value="{{.LlamaPort}}">
            </label>
            <button type="submit" class="outline">Test Connection</button>
        </div>
    </form>
    <div id="connection-result"></div>
</article>

<article>
    <header>API Key Authentication</header>
    <p>Optional Bearer token required for /v1/* requests.</p>
    <form hx-put="/api/settings"
          hx-target="#api-key-status"
          hx-swap="innerHTML">
        <label>
            API Key
            <input type="password" name="api_key"
                   placeholder="{{if .HasAPIKey}}(key is set — leave blank to keep){{else}}No key set (proxy is open){{end}}">
        </label>
        <button type="submit">Save</button>
    </form>
    <div id="api-key-status"></div>
</article>

<article>
    <header>HuggingFace Token</header>
    <p>Required for gated model downloads.</p>
    <form hx-put="/api/settings"
          hx-target="#hf-token-status"
          hx-swap="innerHTML">
        <label>
            HF Token
            <input type="password" name="hf_token"
                   placeholder="{{if .HasHFToken}}(token is set){{else}}hf_...{{end}}">
        </label>
        <button type="submit">Save</button>
    </form>
    <div id="hf-token-status"></div>
</article>

<article>
    <header>Data Directory</header>
    <p><code>{{.DataDir}}</code></p>
</article>
{{end}}
```

---

## Connection Test Response Partial

```html
{{define "connection_result"}}
{{if .OK}}
<p>
    <ins>Connected</ins> — {{.Latency}}
    {{if .Models}}
    <br>Loaded models: {{range .Models}}<kbd>{{.}}</kbd> {{end}}
    {{end}}
</p>
{{else}}
<p><del>Failed</del> — {{.Error}}</p>
{{end}}
{{end}}
```

---

## Proxy Behavior

| Scenario | Behavior |
|----------|----------|
| llama-server running, no API key configured | All `/v1/*` requests forwarded transparently |
| llama-server running, API key configured | Only requests with valid `Bearer <key>` forwarded |
| llama-server not running | Return `503` with JSON error body |
| Streaming response (`/v1/chat/completions` with `stream: true`) | Proxy passes through SSE stream from llama-server |

The proxy is transparent — it doesn't modify request or response bodies. llama-server handles all OpenAI API logic.

---

## Config File Updates

`/data/config/llamactl.yaml` is the single source of truth:

```yaml
listen_addr: ":3000"
data_dir: "/data"
llama_port: 8080
hf_token: "hf_..."
api_key: "sk-..."
log_level: "info"
```

Settings changes write back to this file. The config struct is accessed through the `Server` and changes take effect immediately (no restart needed).

---

## What You Can Do at End of Phase

- Any OpenAI-compatible tool (Open WebUI, SillyTavern, curl) can use `http://host:3000/v1/` as the API endpoint
- API key auth protects the proxy if configured
- Streaming completions work through the proxy
- Settings page shows the proxy endpoint URL for easy copying
- "Test Connection" button verifies llama-server is reachable and shows loaded models
- HF token and API key can be configured from the UI
- `503` JSON error returned gracefully when llama-server is down
