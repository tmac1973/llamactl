package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	modelList := s.registry.List()

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if len(modelList) == 0 {
			w.Write([]byte("<p>No models downloaded yet. Browse HuggingFace to download models.</p>"))
			return
		}
		w.Write([]byte(`<table role="grid"><thead><tr><th>Model</th><th>Quant</th><th>VRAM Est.</th><th>Size</th><th></th></tr></thead>`))
		for _, m := range modelList {
			s.renderPartial(w, "model_card", m)
		}
		w.Write([]byte(`</table>`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(modelList)
}

func (s *Server) handleGetModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	m, err := s.registry.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(m)
}

func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.registry.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.WriteHeader(http.StatusOK)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
