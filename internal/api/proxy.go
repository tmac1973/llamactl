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

// newProxyHandler creates a routing proxy that dispatches OpenAI-compatible
// requests to the correct llama-server instance based on the "model" field.
//
// Routing behavior:
//   - No models active → 503 error
//   - One model active → always route there (ignores model field for compat)
//   - Multiple models active → require a matching model field; error with
//     available model list if no match
func (s *Server) newProxyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read body once for routing + injection
		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			writeProxyError(w, http.StatusBadRequest, "failed to read request body")
			return
		}

		// Parse model field for routing
		var reqModel struct {
			Model string `json:"model"`
		}
		json.Unmarshal(body, &reqModel) // best-effort; may be non-JSON for some endpoints

		// Resolve which instance to route to
		port, modelID, errMsg := s.resolveInstance(reqModel.Model)
		if errMsg != "" {
			writeProxyError(w, http.StatusBadRequest, errMsg)
			return
		}
		if port == 0 {
			writeProxyError(w, http.StatusServiceUnavailable, "no active model instance")
			return
		}

		// Inject sampling defaults for chat completions
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/chat/completions") {
			body = s.injectSamplingDefaults(body, modelID)
		}

		// Replace body and update content length
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))

		target := &url.URL{
			Scheme: "http",
			Host:   fmt.Sprintf("localhost:%d", port),
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.FlushInterval = 50 * time.Millisecond
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			writeProxyError(w, http.StatusServiceUnavailable, "llama-server is not running")
		}
		proxy.ServeHTTP(w, r)
	})
}

// resolveInstance finds the right backend port for a request.
// Returns (port, modelID, errorMessage). errorMessage is non-empty only when
// multiple models are active and the request doesn't match any of them.
func (s *Server) resolveInstance(requestModel string) (int, string, string) {
	active := s.process.ListActive()
	if len(active) == 0 {
		return 0, "", ""
	}

	// Single model: always route there regardless of what the client sends.
	// This maximizes compatibility with clients that hardcode or omit the model field.
	if len(active) == 1 {
		return active[0].Port, active[0].ID, ""
	}

	// Multiple models: require a valid match.
	if requestModel != "" {
		// Exact match on instance ID
		for _, st := range active {
			if st.ID == requestModel {
				return st.Port, st.ID, ""
			}
		}
		// Fuzzy match: check if the model field is a substring of the instance ID or model path
		lower := strings.ToLower(requestModel)
		for _, st := range active {
			if strings.Contains(strings.ToLower(st.ID), lower) ||
				strings.Contains(strings.ToLower(st.Model), lower) {
				return st.Port, st.ID, ""
			}
		}
	}

	// No match — build an error listing available models
	names := make([]string, len(active))
	for i, st := range active {
		names[i] = st.ID
	}
	return 0, "", fmt.Sprintf(
		"model %q not found. Available models: %s",
		requestModel, strings.Join(names, ", "),
	)
}

// injectSamplingDefaults merges per-model sampling parameters into the
// request body for any parameters the client didn't already specify.
func (s *Server) injectSamplingDefaults(body []byte, modelID string) []byte {
	if modelID == "" {
		return body
	}

	cfg, err := s.registry.GetConfig(modelID)
	if err != nil {
		return body
	}

	overrides := cfg.SamplingOverrides()
	if len(overrides) == 0 {
		return body
	}

	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err != nil {
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

func writeProxyError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "invalid_request_error",
			"code":    "model_not_found",
		},
	})
}
