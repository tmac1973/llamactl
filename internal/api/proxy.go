package api

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// newProxyHandler creates a reverse proxy to the llama-server OpenAI API.
func (s *Server) newProxyHandler() http.Handler {
	target := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("localhost:%d", s.cfg.LlamaPort),
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	// Flush immediately for streaming responses (SSE chat completions).
	proxy.FlushInterval = 50 * time.Millisecond

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":{"message":"llama-server is not running","type":"server_error","code":"service_unavailable"}}`)
	}

	return proxy
}
