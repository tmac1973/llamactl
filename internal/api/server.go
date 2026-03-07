package api

import (
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/tmlabonte/llamactl/internal/config"
	"github.com/tmlabonte/llamactl/web"
)

type Server struct {
	cfg    *config.Config
	pages  map[string]*template.Template
	router chi.Router
}

func NewServer(cfg *config.Config) *Server {
	s := &Server{cfg: cfg}
	s.pages = s.parseTemplates()
	s.router = s.buildRouter()
	return s
}

// parseTemplates parses the layout+partials as a base, then clones it
// per page so each page's {{define "content"}} doesn't collide.
func (s *Server) parseTemplates() map[string]*template.Template {
	base := template.Must(template.ParseFS(web.Templates,
		"templates/layout.html",
		"templates/partials/*.html",
	))

	pages := map[string]*template.Template{}
	pageFiles := []string{
		"index.html",
		"builds.html",
		"models.html",
		"models_browse.html",
		"service.html",
		"settings.html",
	}
	for _, pf := range pageFiles {
		clone := template.Must(base.Clone())
		pages[pf] = template.Must(clone.ParseFS(web.Templates, "templates/"+pf))
	}
	return pages
}

func (s *Server) Router() http.Handler {
	return s.router
}

func (s *Server) buildRouter() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))

	// Static assets (htmx, Pico CSS)
	staticFS, _ := fs.Sub(web.Static, "static")
	r.Handle("/static/*", http.StripPrefix("/static/",
		http.FileServer(http.FS(staticFS))))

	// Page routes (server-rendered HTML)
	r.Get("/", s.handleIndex)
	r.Get("/builds", s.handleBuildsPage)
	r.Get("/models", s.handleModelsPage)
	r.Get("/models/browse", s.handleModelsBrowsePage)
	r.Get("/service", s.handleServicePage)
	r.Get("/settings", s.handleSettingsPage)

	// API routes mounted in later phases
	r.Route("/api", func(r chi.Router) {
		// Phase 2: r.Route("/builds", ...)
		// Phase 3: r.Route("/models", ...), r.Route("/hf", ...)
		// Phase 4: r.Route("/service", ...)
		// Phase 5: r.Route("/settings", ...)
	})

	// Phase 5: r.Handle("/v1/*", proxyHandler)

	return r
}

// pageData holds common template data for page rendering.
type pageData struct {
	Title string
	Nav   string
}

// Page handlers — render full HTML pages
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.render(w, "index.html", pageData{Nav: "home"})
}

func (s *Server) handleBuildsPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "builds.html", pageData{Title: "Builds", Nav: "builds"})
}

func (s *Server) handleModelsPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "models.html", pageData{Title: "Models", Nav: "models"})
}

func (s *Server) handleModelsBrowsePage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "models_browse.html", pageData{Title: "Browse HuggingFace", Nav: "browse"})
}

func (s *Server) handleServicePage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "service.html", pageData{Title: "Service", Nav: "service"})
}

func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "settings.html", pageData{Title: "Settings", Nav: "settings"})
}

// render executes the "layout" template for the given page.
func (s *Server) render(w http.ResponseWriter, name string, data any) {
	tmpl, ok := s.pages[name]
	if !ok {
		slog.Error("template not found", "name", name)
		http.Error(w, "page not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("template render error", "name", name, "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}
