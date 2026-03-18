package api

import (
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/tmlabonte/llamactl/internal/builder"
	"github.com/tmlabonte/llamactl/internal/config"
	"github.com/tmlabonte/llamactl/internal/huggingface"
	"github.com/tmlabonte/llamactl/internal/models"
	"github.com/tmlabonte/llamactl/internal/monitor"
	"github.com/tmlabonte/llamactl/internal/process"
	"github.com/tmlabonte/llamactl/web"
)

type Server struct {
	cfg            *config.Config
	pages          map[string]*template.Template
	router         chi.Router
	builder        *builder.Builder
	hfClient       *huggingface.Client
	downloader     *huggingface.Downloader
	registry       *models.Registry
	process        *process.Manager
	monitor        *monitor.Monitor
}

func NewServer(cfg *config.Config) *Server {
	mon := monitor.New(3 * time.Second)
	mon.Start()

	s := &Server{
		cfg:        cfg,
		builder:    builder.NewBuilder(cfg.DataDir),
		hfClient:   huggingface.NewClient(cfg.HFToken),
		downloader: huggingface.NewDownloader(cfg.DataDir, cfg.HFToken),
		registry:   models.NewRegistry(cfg.DataDir),
		process:    process.NewManager(),
		monitor:    mon,
	}
	s.downloader.SetOnComplete(s.onDownloadComplete)
	s.pages = s.parseTemplates()
	s.router = s.buildRouter()
	return s
}

// parseTemplates parses the layout+partials as a base, then clones it
// per page so each page's {{define "content"}} doesn't collide.
func (s *Server) parseTemplates() map[string]*template.Template {
	funcMap := template.FuncMap{
		"divGB": func(bytes int64) float64 {
			return float64(bytes) / (1024 * 1024 * 1024)
		},
		"vramFit": models.VRAMFitCategory,
	}

	base := template.Must(template.New("").Funcs(funcMap).ParseFS(web.Templates,
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
	r.Get("/service", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/models", http.StatusMovedPermanently)
	})
	r.Get("/settings", s.handleSettingsPage)

	// Dashboard API (outside /api group, htmx-only)
	r.Get("/api/dashboard", s.handleDashboard)

	// API routes
	r.Route("/api", func(r chi.Router) {
		r.Route("/builds", func(r chi.Router) {
			r.Get("/", s.handleListBuilds)
			r.Post("/", s.handleTriggerBuild)
			r.Get("/backends", s.handleListBackends)
			r.Get("/refs", s.handleListRefs)
			r.Get("/options", s.handleProfileOptions)
			r.Get("/{id}/logs", s.handleBuildLogs)
			r.Delete("/{id}", s.handleDeleteBuild)
		})
		r.Route("/models", func(r chi.Router) {
			r.Get("/", s.handleListModels)
			r.Get("/{id}", s.handleGetModel)
			r.Delete("/{id}", s.handleDeleteModel)
			r.Put("/{id}/activate", s.handleActivateModel)
			r.Delete("/{id}/activate", s.handleDeactivateModel)
			r.Get("/{id}/config", s.handleGetModelConfig)
			r.Put("/{id}/config", s.handleUpdateModelConfig)
		})
		r.Route("/hf", func(r chi.Router) {
			r.Get("/search", s.handleHFSearch)
			r.Get("/model", s.handleHFModel)
			r.Post("/download", s.handleHFDownload)
			r.Get("/download/{id}/progress", s.handleHFDownloadProgress)
			r.Delete("/download/{id}", s.handleHFDownloadCancel)
		})
		r.Route("/service", func(r chi.Router) {
			r.Get("/status", s.handleServiceStatus)
			r.Post("/start", s.handleServiceStart)
			r.Post("/stop", s.handleServiceStop)
			r.Post("/restart", s.handleServiceRestart)
			r.Get("/logs", s.handleServiceLogs)
			r.Get("/log-tabs", s.handleServiceLogTabs)
			r.Get("/health", s.handleServiceHealth)
		})
		r.Route("/settings", func(r chi.Router) {
			r.Get("/", s.handleGetSettings)
			r.Put("/", s.handleUpdateSettings)
			r.Post("/test-connection", s.handleTestConnection)
		})
		r.Route("/monitor", func(r chi.Router) {
			r.Get("/", s.handleMonitorStatus)
			r.Get("/stream", s.handleMonitorStream)
		})
	})

	// OpenAI-compatible proxy with optional API key auth
	r.Mount("/v1", s.apiKeyAuth(s.newProxyHandler()))

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
	data := struct {
		pageData
		Backends []builder.Backend
	}{
		pageData: pageData{Title: "Builds", Nav: "builds"},
		Backends: builder.DetectBackends(),
	}
	s.render(w, "builds.html", data)
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
	proxyEndpoint := strings.TrimRight(s.cfg.ExternalURL, "/") + "/v1"
	data := struct {
		pageData
		ProxyEndpoint string
		LlamaPort     int
		HasAPIKey     bool
		HasHFToken    bool
		HasExtURL     bool
		ExternalURL   string
		DataDir       string
	}{
		pageData:      pageData{Title: "Settings", Nav: "settings"},
		ProxyEndpoint: proxyEndpoint,
		LlamaPort:     s.cfg.LlamaPort,
		HasAPIKey:     s.cfg.APIKey != "",
		HasHFToken:    s.cfg.HFToken != "",
		HasExtURL:     s.cfg.ExternalURL != "",
		ExternalURL:   s.cfg.ExternalURL,
		DataDir:       s.cfg.DataDir,
	}
	s.render(w, "settings.html", data)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	active := s.process.ListActive()
	builds := s.builder.List()
	models := s.registry.List()

	successBuilds := 0
	for _, b := range builds {
		if b.Status == "success" {
			successBuilds++
		}
	}

	apiURL := strings.TrimRight(s.cfg.ExternalURL, "/") + "/v1"
	chatURL := ""
	if u, err := url.Parse(s.cfg.ExternalURL); err == nil {
		chatURL = fmt.Sprintf("%s://%s:%d", u.Scheme, u.Hostname(), s.cfg.LlamaPort)
	}

	// Aggregate state badge from all instances
	var stateBadge string
	activeCount := len(active)
	hasRunning := false
	hasStarting := false
	hasFailed := false
	for _, st := range active {
		switch st.State {
		case "running":
			hasRunning = true
		case "starting":
			hasStarting = true
		case "failed":
			hasFailed = true
		}
	}
	switch {
	case hasRunning:
		label := "Running"
		if activeCount > 1 {
			label = fmt.Sprintf("Running (%d models)", activeCount)
		}
		stateBadge = `<ins>` + label + `</ins>`
	case hasStarting:
		stateBadge = `<mark>Starting</mark>`
	case hasFailed:
		stateBadge = `<del>Failed</del>`
	default:
		stateBadge = `Stopped`
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div class="grid">
    <article>
        <header>Service</header>
        <p>%s</p>
        %s
        <p><a href="/models">Manage →</a></p>
    </article>
    <article>
        <header>Active Models</header>
        <p>%s</p>
        <p><a href="/models">Models →</a></p>
    </article>
    <article>
        <header>Inventory</header>
        <p><strong>%d</strong> builds · <strong>%d</strong> models</p>
        <p><a href="/builds">Builds →</a> · <a href="/models">Models →</a></p>
        <p><a href="/models/browse">Get New Models →</a></p>
    </article>
    <article>
        <header>API Endpoint</header>
        <pre style="user-select: all; cursor: pointer;">%s</pre>
        <p><a href="/settings">Settings →</a></p>
    </article>
</div>`,
		stateBadge,
		func() string {
			if chatURL != "" && hasRunning {
				return fmt.Sprintf(`<p><a href="%s" target="_blank">Open Chat UI →</a></p>`, chatURL)
			}
			return ""
		}(),
		func() string {
			if len(active) == 0 {
				return "None"
			}
			var parts []string
			for _, st := range active {
				parts = append(parts, `<strong>`+st.ID+`</strong>`)
			}
			return strings.Join(parts, "<br>")
		}(),
		successBuilds,
		len(models),
		apiURL,
	)
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
