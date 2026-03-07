package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/tmlabonte/llamactl/internal/builder"
)

func (s *Server) handleListBackends(w http.ResponseWriter, r *http.Request) {
	backends := builder.DetectBackends()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(backends)
}

func (s *Server) handleListBuilds(w http.ResponseWriter, r *http.Request) {
	builds := s.builder.List()

	// If request is from htmx, return HTML partial
	if r.Header.Get("HX-Request") == "true" {
		if len(builds) == 0 {
			w.Write([]byte("<p>No builds yet.</p>"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<table role="grid"><thead><tr><th>ID</th><th>Profile</th><th>SHA</th><th>Ref</th><th>Status</th><th>Date</th><th></th></tr></thead><tbody>`))
		for _, b := range builds {
			s.renderPartial(w, "build_card", b)
		}
		w.Write([]byte(`</tbody></table>`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(builds)
}

func (s *Server) handleTriggerBuild(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profile string `json:"profile"`
		GitRef  string `json:"git_ref"`
	}

	// Support both JSON and form-encoded
	if r.Header.Get("Content-Type") == "application/json" {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		r.ParseForm()
		req.Profile = r.FormValue("profile")
		req.GitRef = r.FormValue("git_ref")
	}

	result, err := s.builder.Build(r.Context(), req.Profile, req.GitRef)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Return the log streaming partial for htmx to swap in
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		s.renderPartial(w, "build_log", result)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(result)
}

func (s *Server) handleBuildLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ch, ok := s.builder.LogChannel(id)
	if !ok {
		http.NotFound(w, r)
		return
	}

	sse, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for {
		select {
		case line, ok := <-ch:
			if !ok {
				sse.SendEvent("done", "Build complete")
				return
			}
			sse.SendData(line)
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleDeleteBuild(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.builder.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// htmx: return empty to remove the row
	if r.Header.Get("HX-Request") == "true" {
		w.WriteHeader(http.StatusOK)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// renderPartial executes a partial template, writing directly to w.
func (s *Server) renderPartial(w http.ResponseWriter, name string, data any) {
	// Partials are defined in the first page's template set; use any page clone
	for _, tmpl := range s.pages {
		if err := tmpl.ExecuteTemplate(w, name, data); err == nil {
			return
		}
	}
}
