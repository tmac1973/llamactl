package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// newProxyHandler creates a reverse proxy to the llama-server router.
// Injects per-model sampling defaults for chat completion requests.
func (s *Server) newProxyHandler() http.Handler {
	target := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("localhost:%d", s.cfg.LlamaPort),
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = 50 * time.Millisecond
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": "llama-server router is not running",
				"type":    "server_error",
				"code":    "service_unavailable",
			},
		})
	}

	// Capture timings from chat completion responses — both JSON and SSE streams.
	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp.Request == nil || resp.StatusCode != http.StatusOK {
			return nil
		}
		if !strings.HasSuffix(resp.Request.URL.Path, "/chat/completions") {
			return nil
		}

		ct := resp.Header.Get("Content-Type")
		if strings.Contains(ct, "text/event-stream") {
			// Wrap the body so we can scan SSE chunks for the final
			// timings event without blocking delivery to the client.
			resp.Body = newSSETimingCapture(resp.Body, s.captureTimings)
			return nil
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))

		var result struct {
			Model   string         `json:"model"`
			Timings map[string]any `json:"timings"`
		}
		if json.Unmarshal(body, &result) == nil && result.Timings != nil && result.Model != "" {
			go s.captureTimings(result.Model, result.Timings)
		}
		return nil
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Inject sampling defaults for chat completions
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/chat/completions") {
			body, err := io.ReadAll(r.Body)
			r.Body.Close()
			if err == nil {
				body = s.injectSamplingDefaults(body)
				r.Body = io.NopCloser(bytes.NewReader(body))
				r.ContentLength = int64(len(body))
			}
		}
		proxy.ServeHTTP(w, r)
	})
}

// sseTimingCapture wraps a streaming SSE response body, passing bytes
// through to the client while scanning for the final chunk that carries
// llama.cpp's `timings` field. When seen, captureFn is invoked once.
type sseTimingCapture struct {
	orig     io.ReadCloser
	capture  func(modelID string, timings map[string]any)
	buf      bytes.Buffer
	model    string
	captured bool
}

func newSSETimingCapture(orig io.ReadCloser, capture func(string, map[string]any)) *sseTimingCapture {
	return &sseTimingCapture{orig: orig, capture: capture}
}

func (s *sseTimingCapture) Read(p []byte) (int, error) {
	n, err := s.orig.Read(p)
	if n > 0 && !s.captured {
		s.buf.Write(p[:n])
		s.scan()
	}
	return n, err
}

func (s *sseTimingCapture) scan() {
	// SSE events are separated by a blank line ("\n\n").
	for {
		data := s.buf.Bytes()
		idx := bytes.Index(data, []byte("\n\n"))
		if idx < 0 {
			return
		}
		event := string(data[:idx])
		s.buf.Next(idx + 2)

		for _, line := range strings.Split(event, "\n") {
			line = strings.TrimPrefix(line, "data: ")
			line = strings.TrimPrefix(line, "data:") // tolerate no-space form
			line = strings.TrimSpace(line)
			if line == "" || line == "[DONE]" {
				continue
			}
			var chunk struct {
				Model   string         `json:"model"`
				Timings map[string]any `json:"timings"`
			}
			if json.Unmarshal([]byte(line), &chunk) != nil {
				continue
			}
			if s.model == "" && chunk.Model != "" {
				s.model = chunk.Model
			}
			if chunk.Timings != nil && s.model != "" {
				go s.capture(s.model, chunk.Timings)
				s.captured = true
				return
			}
		}
	}
}

func (s *sseTimingCapture) Close() error {
	return s.orig.Close()
}

// injectSamplingDefaults reads the model field from the request body,
// looks up per-model sampling config, and merges defaults for any
// parameters the client didn't specify.
func (s *Server) injectSamplingDefaults(body []byte) []byte {
	var req struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &req) != nil || req.Model == "" {
		return body
	}

	// Look up config by model name (which is the registry ID / alias)
	cfg, err := s.registry.GetConfig(req.Model)
	if err != nil {
		return body
	}

	overrides := cfg.SamplingOverrides()
	if len(overrides) == 0 {
		return body
	}

	var reqMap map[string]any
	if json.Unmarshal(body, &reqMap) != nil {
		return body
	}

	for k, v := range overrides {
		if _, exists := reqMap[k]; !exists {
			reqMap[k] = v
		}
	}

	modified, err := json.Marshal(reqMap)
	if err != nil {
		return body
	}
	return modified
}
