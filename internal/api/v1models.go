package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/tmlabonte/llamactl/internal/models"
)

// openAIModel builds an OpenAI-compatible Model object with a meta extension.
func (s *Server) openAIModel(m *models.Model, cfg *models.ModelConfig) map[string]any {
	// Capabilities
	var capabilities []string
	isEmbedding := models.IsEmbeddingModel(m.ModelID) || models.IsEmbeddingModel(m.ID)
	if isEmbedding {
		capabilities = append(capabilities, "embedding")
	} else {
		capabilities = append(capabilities, "chat")
	}
	if m.SupportsTools {
		capabilities = append(capabilities, "tools")
	}
	if m.HasBuiltinVision || (cfg != nil && cfg.MmprojPath != "") {
		capabilities = append(capabilities, "vision")
	}

	meta := map[string]any{
		"arch":         m.Arch,
		"quant":        m.Quant,
		"n_layers":     m.NLayers,
		"n_embd":       m.NEmbd,
		"n_ctx_train":  m.ContextLength,
		"size":         m.SizeBytes,
		"capabilities": capabilities,
	}
	if cfg != nil {
		// 0 means "use model default" (n_ctx_train)
		if cfg.ContextSize > 0 {
			meta["context_size"] = cfg.ContextSize
		} else {
			meta["context_size"] = m.ContextLength
		}
	}

	obj := map[string]any{
		"id":       m.ID,
		"object":   "model",
		"created":  m.DownloadedAt.Unix(),
		"owned_by": "llamactl",
		"meta":     meta,
	}
	if cfg != nil && len(cfg.Aliases) > 0 {
		obj["aliases"] = cfg.Aliases
	}
	return obj
}

// handleV1Models returns an OpenAI-compatible model list with meta extensions.
func (s *Server) handleV1Models(w http.ResponseWriter, r *http.Request) {
	all := s.registry.List()

	var data []map[string]any
	for _, m := range all {
		cfg, _ := s.registry.GetConfig(m.ID)
		if cfg != nil && !cfg.Enabled {
			continue // only list enabled models
		}
		data = append(data, s.openAIModel(m, cfg))
	}

	respondJSON(w, map[string]any{
		"object": "list",
		"data":   data,
	})
}

// handleV1Model returns a single OpenAI-compatible model object with meta extensions.
func (s *Server) handleV1Model(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "model")

	m, cfg := s.findModelByAny(id)
	if m == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		respondJSON(w, map[string]any{
			"error": map[string]any{
				"message": "model not found: " + id,
				"type":    "invalid_request_error",
				"code":    "model_not_found",
			},
		})
		return
	}

	respondJSON(w, s.openAIModel(m, cfg))
}

// findModelByAny looks up a model by registry ID, router name, or alias.
func (s *Server) findModelByAny(name string) (*models.Model, *models.ModelConfig) {
	// Direct registry ID match
	if m, err := s.registry.Get(name); err == nil {
		cfg, _ := s.registry.GetConfig(m.ID)
		return m, cfg
	}

	// Search by router name or alias
	for _, m := range s.registry.List() {
		if s.registry.RouterName(m.ID) == name {
			cfg, _ := s.registry.GetConfig(m.ID)
			return m, cfg
		}
		if cfg, err := s.registry.GetConfig(m.ID); err == nil {
			for _, alias := range cfg.Aliases {
				if alias == name {
					return m, cfg
				}
			}
		}
	}

	return nil, nil
}
