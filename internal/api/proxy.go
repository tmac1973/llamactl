package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/tmlabonte/llamactl/internal/models"
)

// autoLoadTimeout caps how long an auto-load triggered by /v1/chat/completions
// (or any proxied POST with a `model` field) is allowed to take. Cold loads
// of a 35B model can exceed 30 seconds when reading from disk, so we set
// this generously and let the client wait — the alternative is a 400 from
// the upstream router, which is what bug 2 was about in the first place.
const autoLoadTimeout = 90 * time.Second

// autoLoadPollInterval is how often we re-poll the router for a model's
// load state while waiting for an auto-load to complete.
const autoLoadPollInterval = 250 * time.Millisecond

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
		// JSON POSTs can carry a "model" field that would 400 upstream if
		// the named model is enabled in the registry but not currently
		// resident in the router. Read the body, ensure the model is
		// loaded, then forward. We restrict to JSON so multipart uploads
		// (audio transcription etc.) stream through without buffering.
		if r.Method == http.MethodPost && isJSONContentType(r.Header.Get("Content-Type")) {
			body, err := io.ReadAll(r.Body)
			r.Body.Close()
			if err == nil {
				if loadErr := s.ensureModelLoadedForRequest(r.Context(), body); loadErr != nil {
					writeProxyError(w, http.StatusServiceUnavailable, loadErr.Error())
					return
				}
				if strings.HasSuffix(r.URL.Path, "/chat/completions") {
					body = s.injectSamplingDefaults(body)
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				r.ContentLength = int64(len(body))
			}
		}
		proxy.ServeHTTP(w, r)
	})
}

// isJSONContentType returns true for application/json content types,
// tolerating optional charset suffixes.
func isJSONContentType(ct string) bool {
	if ct == "" {
		return false
	}
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	return strings.EqualFold(strings.TrimSpace(ct), "application/json")
}

// ensureModelLoadedForRequest inspects a proxied request body for a
// "model" field. If the model is one we know but the router does not
// currently have loaded, it triggers a load and waits for the router to
// report the model ready. Errors here surface as 5xx to the client; missing
// "model" or unknown models are ignored so the upstream router can produce
// its own (more specific) error.
func (s *Server) ensureModelLoadedForRequest(ctx context.Context, body []byte) error {
	if len(body) == 0 {
		return nil
	}
	var req struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &req) != nil || req.Model == "" {
		return nil
	}
	regModel, _ := s.findModelByAny(req.Model)
	if regModel == nil {
		return nil // unknown — let the upstream return its own error
	}

	routerName := s.registry.RouterName(regModel.ID)

	state, knownToRouter := s.lookupRouterState(routerName, regModel)
	if state == "loaded" {
		return nil
	}
	if !knownToRouter {
		// The router doesn't know this model at all — typically because
		// preset.ini was generated before the model was added or before
		// section-collision was fixed. A restart would pick it up; until
		// then there's nothing /models/load can do for us.
		return fmt.Errorf("model %q is not in the router preset; restart the router to pick it up", regModel.PublicName())
	}
	if state != "loading" {
		if err := s.process.LoadModel(routerName); err != nil {
			return fmt.Errorf("auto-load %s: %w", regModel.PublicName(), err)
		}
		slog.Info("auto-loading model for inference request", "model", regModel.PublicName(), "router_name", routerName)
	}
	return s.waitForModelLoaded(ctx, routerName, regModel)
}

// lookupRouterState returns the router's current state for a model, plus
// whether the router knows about the model at all. The router may report
// the model under its primary ID, public name, or any alias, so we check
// all three before declaring it unknown.
func (s *Server) lookupRouterState(routerName string, m *models.Model) (string, bool) {
	if !s.process.IsRunning() {
		return "", false
	}
	loaded, err := s.process.ListModels()
	if err != nil {
		return "", false
	}
	for _, lm := range loaded {
		if lm.ID == routerName || lm.Model == routerName {
			return lm.Status.Value, true
		}
		for _, a := range lm.Aliases {
			if a == routerName || a == m.ID || a == m.PublicName() {
				return lm.Status.Value, true
			}
		}
	}
	return "", false
}

// waitForModelLoaded polls the router until the named model reports
// "loaded", the request context is cancelled, or autoLoadTimeout elapses.
func (s *Server) waitForModelLoaded(ctx context.Context, routerName string, m *models.Model) error {
	deadline := time.Now().Add(autoLoadTimeout)
	for {
		state, _ := s.lookupRouterState(routerName, m)
		switch state {
		case "loaded":
			return nil
		case "":
			return fmt.Errorf("model %q disappeared from router during load", m.PublicName())
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for model %q to load", autoLoadTimeout, m.PublicName())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(autoLoadPollInterval):
		}
	}
}

func writeProxyError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "server_error",
			"code":    "auto_load_failed",
		},
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
