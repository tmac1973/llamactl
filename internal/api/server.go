package api

import (
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/tmlabonte/llamactl/internal/benchmark"
	"github.com/tmlabonte/llamactl/internal/builder"
	"github.com/tmlabonte/llamactl/internal/config"
	"github.com/tmlabonte/llamactl/internal/huggingface"
	"github.com/tmlabonte/llamactl/internal/models"
	"github.com/tmlabonte/llamactl/internal/monitor"
	"github.com/tmlabonte/llamactl/internal/process"
	"github.com/tmlabonte/llamactl/web"
)

type Server struct {
	cfg             *config.Config
	configPath      string // path the cfg was loaded from; saveConfig writes back here
	version         string // injected by main via SetVersion; "" = dev build
	pages           map[string]*template.Template
	router          chi.Router
	builder         *builder.Builder
	hfClient        *huggingface.Client
	downloader      *huggingface.Downloader
	registry        *models.Registry
	process         *process.Manager
	monitor         *monitor.Monitor
	bench           *benchmark.Store
	benchProgress   map[string]chan benchmark.ProgressUpdate
	benchProgressMu sync.RWMutex
	dirtyModels     map[string]bool // models whose config changed since last load
}

// SetVersion records the build's version string. main.go calls this with
// the ldflags-injected `version` package var so the sidebar can display
// "v1.2.3" or "dev-<sha>" depending on the build. Must be called before
// the first page render.
func (s *Server) SetVersion(v string) { s.version = v }

func NewServer(cfg *config.Config, configPath string) *Server {
	mon := monitor.New(3 * time.Second)
	mon.Start()

	s := &Server{
		cfg:           cfg,
		configPath:    configPath,
		builder:       builder.NewBuilder(cfg.DataDir),
		hfClient:      huggingface.NewClient(cfg.HFToken),
		downloader:    huggingface.NewDownloader(cfg.DataDir, cfg.ModelsPath(), cfg.HFToken),
		registry:      models.NewRegistry(cfg.DataDir, cfg.ModelsPath()),
		process:       process.NewManager(),
		monitor:       mon,
		bench:         benchmark.NewStore(cfg.DataDir),
		benchProgress: make(map[string]chan benchmark.ProgressUpdate),
		dirtyModels:   make(map[string]bool),
	}
	s.downloader.SetOnComplete(s.onDownloadComplete)
	s.registry.BackfillGGUFMeta()
	if n := s.registry.DeduplicateModels(); n > 0 {
		slog.Info("removed duplicate model entries", "count", n)
	}
	if n := s.registry.ScanModels(); n > 0 {
		slog.Info("discovered models on disk", "count", n)
	}
	if n := s.registry.AutoDetectMMProj(); n > 0 {
		slog.Info("auto-detected mmproj files", "count", n)
	}
	if orphans := s.registry.FindOrphans(); len(orphans) > 0 {
		for _, m := range orphans {
			slog.Warn("model file missing", "id", m.ID, "path", m.FilePath)
		}
	}
	s.pages = s.parseTemplates()
	s.router = s.buildRouter()

	if cfg.AutoStart {
		go func() {
			// Small delay so the HTTP listener is up and log subscribers
			// can attach before the router spews its startup output.
			time.Sleep(500 * time.Millisecond)
			if err := s.startRouter(); err != nil {
				slog.Warn("auto-start failed", "error", err)
			} else {
				slog.Info("auto-started inference server")
			}
		}()
	}

	return s
}

// parseTemplates parses the layout+partials as a base, then clones it
// per page so each page's {{define "content"}} doesn't collide.
func (s *Server) parseTemplates() map[string]*template.Template {
	funcMap := template.FuncMap{
		"divGB": models.BytesToGB,
		// cssID sanitizes a string so it's safe to use as both an HTML id
		// attribute and a CSS selector. Model IDs can contain '.' (e.g.
		// "Qwen3.6"), which CSS parses as a class separator and errors on.
		"cssID": func(s string) string {
			return strings.Map(func(r rune) rune {
				switch {
				case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
					return r
				default:
					return '_'
				}
			}, s)
		},
		"divf": func(a, b interface{}) float64 {
			af, bf := toFloat64(a), toFloat64(b)
			if bf == 0 {
				return 0
			}
			return af / bf
		},
		"pctOf": func(value, max float64) float64 {
			if max == 0 {
				return 0
			}
			return (value / max) * 100
		},
		"vramFit": func(estimatedGB float64) string {
			metrics := s.monitor.Current()
			numGPUs := len(metrics.GPU)
			perGPU := 32.0 // fallback
			if numGPUs > 0 {
				perGPU = float64(metrics.GPU[0].VRAMTotalMB) / 1024.0
			} else {
				numGPUs = 1
			}
			// Default to layer mode (no tensor parallelism) for rough estimates
			return models.VRAMFitLabel(estimatedGB, perGPU, numGPUs, 0)
		},
		// hasHFRepo reports whether a model_id looks like an org/repo
		// pair we can deep-link to on huggingface.co — i.e. it has at
		// least one slash and isn't an absolute path. Scanned local
		// models without that shape don't get a link.
		"hasHFRepo": func(modelID string) bool {
			return strings.Contains(modelID, "/") && !strings.HasPrefix(modelID, "/")
		},
		"version": func() string {
			v := s.version
			if v == "" || v == "dev" {
				return "dev"
			}
			// Released versions (e.g. "1.2.3") get a "v" prefix; dev builds
			// from goreleaser's --snapshot or the host_build_binary path
			// already include the marker (e.g. "dev-abc1234" or
			// "1.2.3-snapshot+abc1234") and read fine without one.
			if strings.HasPrefix(v, "dev") || strings.Contains(v, "snapshot") {
				return v
			}
			return "v" + v
		},
	}

	base := template.Must(template.New("").Funcs(funcMap).ParseFS(web.Templates,
		"templates/layout.html",
		"templates/partials/*.html",
	))

	pages := map[string]*template.Template{}
	pageFiles := []string{
		"builds.html",
		"models.html",
		"models_browse.html",
		"benchmarks.html",
		"service.html",
		"server.html",
		"settings.html",
		"help.html",
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
	r.Get("/", s.handleIndex) // redirects to /server
	r.Get("/builds", s.handleBuildsPage)
	r.Get("/models", s.handleModelsPage)
	r.Get("/models/browse", s.handleModelsBrowsePage)
	r.Get("/benchmarks", s.handleBenchmarksPage)
	r.Get("/server", s.handleServerPage)
	r.Get("/settings", s.handleSettingsPage)
	r.Get("/help", s.handleHelpPage)

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
			r.Get("/active-log", s.handleActiveBuildLog)
			r.Get("/{id}/logs", s.handleBuildLogs)
			r.Get("/{id}/info", s.handleBuildInfo)
			r.Delete("/{id}", s.handleDeleteBuild)
		})
		r.Get("/gpu-map", s.handleGPUMap)
		r.Route("/benchmarks", func(r chi.Router) {
			r.Get("/", s.handleListBenchmarks)
			r.Post("/", s.handleStartBenchmark)
			r.Get("/form", s.handleBenchmarkForm)
			r.Get("/compare", s.handleCompareBenchmarks)
			r.Get("/export", s.handleExportBenchmarks)
			r.Delete("/batch-delete", s.handleBatchDeleteBenchmarks)
			r.Get("/{id}", s.handleGetBenchmark)
			r.Delete("/{id}", s.handleDeleteBenchmark)
			r.Get("/{id}/progress", s.handleBenchmarkProgress)
		})
		r.Get("/timings", s.handleTimings)
		r.Get("/timings/{model_id}", s.handleTimings)
		r.Route("/models", func(r chi.Router) {
			r.Get("/", s.handleListModels)
			r.Get("/embeddings", s.handleListEmbeddingModels)
			r.Post("/scan", s.handleScanModels)
			r.Get("/embedding-presets", s.handleEmbeddingPresets)
			r.Post("/embedding-presets/download", s.handleDownloadEmbeddingPreset)
			r.Get("/{id}", s.handleGetModel)
			r.Get("/{id}/info", s.handleModelInfo)
			r.Delete("/{id}", s.handleDeleteModel)
			r.Put("/{id}/activate", s.handleActivateModel)
			r.Delete("/{id}/activate", s.handleDeactivateModel)
			r.Put("/{id}/enable", s.handleModelEnable)
			r.Get("/{id}/config", s.handleGetModelConfig)
			r.Put("/{id}/config", s.handleUpdateModelConfig)
			r.Get("/{id}/vram-estimate", s.handleModelVRAMEstimate)
		})
		r.Route("/hf", func(r chi.Router) {
			r.Get("/search", s.handleHFSearch)
			r.Get("/model", s.handleHFModel)
			r.Post("/download", s.handleHFDownload)
			r.Get("/downloads", s.handleHFActiveDownloads)
			r.Get("/download/{id}/progress", s.handleHFDownloadProgress)
			r.Delete("/download/{id}", s.handleHFDownloadCancel)
		})
		r.Route("/service", func(r chi.Router) {
			r.Get("/status", s.handleServiceStatus)
			r.Post("/start", s.handleServiceStart)
			r.Post("/stop", s.handleServiceStop)
			r.Post("/restart", s.handleServiceRestart)
			r.Get("/logs", s.handleServiceLogs)
			r.Delete("/logs", s.handleServiceLogsClear)
			r.Get("/log-tabs", s.handleServiceLogTabs)
			r.Get("/health", s.handleServiceHealth)
			r.Get("/loaded-models", s.handleLoadedModels)
		})
		r.Get("/ps", s.handlePS)
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

	// OpenAI-compatible endpoints: models are served directly,
	// everything else is proxied to llama-server.
	r.Route("/v1", func(r chi.Router) {
		r.Use(s.apiKeyAuth)
		r.Get("/models", s.handleV1Models)
		r.Get("/models/{model}", s.handleV1Model)
		r.Handle("/*", s.newProxyHandler())
	})

	return r
}

// pageData holds common template data for page rendering.
type pageData struct {
	Title string
	Nav   string
}

// Page handlers — render full HTML pages
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/server", http.StatusFound)
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

func (s *Server) handleBenchmarksPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "benchmarks.html", pageData{Title: "Benchmarks", Nav: "benchmarks"})
}

func (s *Server) handleModelsBrowsePage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "models_browse.html", pageData{Title: "Browse HuggingFace", Nav: "browse"})
}

func (s *Server) handleServicePage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "service.html", pageData{Title: "Service", Nav: "service"})
}

func (s *Server) handleServerPage(w http.ResponseWriter, r *http.Request) {
	data := struct {
		pageData
		ActiveBuild     string
		ModelsMax       int
		AvailableBuilds interface{}
	}{
		pageData:        pageData{Title: "Server", Nav: "server"},
		ActiveBuild:     s.cfg.ActiveBuild,
		ModelsMax:       s.cfg.ModelsMax,
		AvailableBuilds: s.builder.List(),
	}
	s.render(w, "server.html", data)
}

func (s *Server) handleHelpPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "help.html", pageData{Title: "Help", Nav: "help"})
}

func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	proxyEndpoint := strings.TrimRight(s.cfg.ExternalURL, "/") + "/v1"
	data := struct {
		pageData
		ProxyEndpoint    string
		LlamaPort        int
		HasAPIKey        bool
		HasHFToken       bool
		HasExtURL        bool
		ExternalURL      string
		DataDir          string
		ModelsDir        string
		DefaultModelsDir string
		AutoStart        bool
	}{
		pageData:         pageData{Title: "Settings", Nav: "settings"},
		ProxyEndpoint:    proxyEndpoint,
		LlamaPort:        s.cfg.LlamaPort,
		HasAPIKey:        s.cfg.APIKey != "",
		HasHFToken:       s.cfg.HFToken != "",
		HasExtURL:        s.cfg.ExternalURL != "",
		ExternalURL:      s.cfg.ExternalURL,
		DataDir:          s.cfg.DataDir,
		ModelsDir:        s.cfg.ModelsDir,
		DefaultModelsDir: filepath.Join(s.cfg.DataDir, "models"),
		AutoStart:        s.cfg.AutoStart,
	}
	s.render(w, "settings.html", data)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	routerStatus := s.process.GetStatus()
	builds := s.builder.List()
	registeredModels := s.registry.List()

	successBuilds := 0
	for _, b := range builds {
		if b.Status == builder.BuildStatusSuccess {
			successBuilds++
		}
	}

	apiURL := strings.TrimRight(s.cfg.ExternalURL, "/") + "/v1"
	chatURL := ""
	if u, err := url.Parse(s.cfg.ExternalURL); err == nil && u.Hostname() != "" {
		chatURL = fmt.Sprintf("%s://%s:%d", u.Scheme, u.Hostname(), s.cfg.LlamaPort)
	}

	// Router state badge
	var stateBadge string
	switch routerStatus.State {
	case process.StateRunning:
		stateBadge = `<ins>Running</ins>`
	case process.StateStarting:
		stateBadge = `<mark>Starting</mark>`
	case process.StateFailed:
		stateBadge = `<del>Failed</del>`
	default:
		stateBadge = `Stopped`
	}

	// "Available Models" card: all enabled chat models by public name, with
	// a load icon and a loaded/loading tag when the router has them resident.
	availableHTML := "<p>None</p>"
	if routerStatus.State == process.StateRunning {
		// Build lookup: router-known name → status
		loadedState := map[string]string{}
		if loaded, err := s.process.ListModels(); err == nil {
			for _, lm := range loaded {
				if lm.Status.Value != "loaded" && lm.Status.Value != "loading" {
					continue
				}
				loadedState[lm.ID] = lm.Status.Value
				if lm.Model != "" {
					loadedState[lm.Model] = lm.Status.Value
				}
				for _, a := range lm.Aliases {
					loadedState[a] = lm.Status.Value
				}
			}
		}

		var buf strings.Builder
		shown := 0
		for _, m := range registeredModels {
			if models.IsEmbeddingModel(m.ModelID) || models.IsEmbeddingModel(m.ID) {
				continue
			}
			cfg, err := s.registry.GetConfig(m.ID)
			if err != nil || !cfg.Enabled {
				continue
			}

			routerName := s.registry.RouterName(m.ID)
			state := loadedState[routerName]
			if state == "" {
				state = loadedState[m.ID]
			}
			if state == "" {
				state = loadedState[m.PublicName()]
			}

			tag := ""
			switch state {
			case "loaded":
				tag = ` <mark style="padding:0 0.3rem;font-size:0.65rem;">loaded</mark>`
			case "loading":
				tag = ` <mark style="padding:0 0.3rem;font-size:0.65rem;">loading</mark>`
			}

			const playSVG = `<svg viewBox="0 0 16 16" width="12" height="12" fill="currentColor" aria-hidden="true"><path d="M4 3l9 5-9 5z"/></svg>`
			const copySVG = `<svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><rect x="5" y="5" width="8" height="9" rx="1"/><path d="M3 11V3.5A.5.5 0 0 1 3.5 3H10"/></svg>`
			const checkSVG = `<svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M3 8l3 3 7-7"/></svg>`

			loadIcon := `<span class="action-icon-placeholder">&nbsp;</span>`
			if state == "" {
				loadIcon = fmt.Sprintf(`<button type="button" class="action-icon" title="Load into VRAM" `+
					`hx-put="/api/models/%s/activate" hx-swap="none" `+
					`hx-on::after-request="htmx.trigger('#dashboard-cards', 'load')">%s</button>`,
					html.EscapeString(m.ID), playSVG)
			}

			pn := m.PublicName()
			copyBtn := fmt.Sprintf(`<button type="button" class="action-icon" title="Copy model name" `+
				`data-icon="%s" data-icon-check="%s" data-copy="%s" `+
				`onclick="copyFromButton(this)">%s</button>`,
				html.EscapeString(copySVG), html.EscapeString(checkSVG), html.EscapeString(pn), copySVG)

			fmt.Fprintf(&buf, `<div class="available-model-row">%s%s<code>%s</code>%s</div>`,
				loadIcon, copyBtn, html.EscapeString(pn), tag)
			shown++
		}
		if shown > 0 {
			availableHTML = buf.String()
		}
	}

	_ = stateBadge // server-status-badge has its own poll endpoint now

	const copySVG = `<svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><rect x="5" y="5" width="8" height="9" rx="1"/><path d="M3 11V3.5A.5.5 0 0 1 3.5 3H10"/></svg>`
	const checkSVG = `<svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M3 8l3 3 7-7"/></svg>`
	apiCopyBtn := fmt.Sprintf(`<button type="button" class="action-icon" title="Copy endpoint URL" `+
		`data-icon="%s" data-icon-check="%s" data-copy="%s" `+
		`onclick="copyFromButton(this)">%s</button>`,
		html.EscapeString(copySVG), html.EscapeString(checkSVG), html.EscapeString(apiURL), copySVG)

	chatLinkHTML := ""
	if chatURL != "" {
		chatLinkHTML = fmt.Sprintf(`<p><a href="%s" target="_blank">Open Chat UI →</a></p>`, chatURL)
	}

	respondHTML(w)
	fmt.Fprintf(w, `<article>
    <header>Available Models</header>
    <div class="available-models-scroll">%s</div>
</article>
<article>
    <header>Inventory</header>
    <p><strong>%d</strong> builds · <strong>%d</strong> models</p>
    <p><a href="/builds">Builds →</a> · <a href="/models">Models →</a></p>
    <p><a href="/models/browse">Get New Models →</a></p>
</article>
<article>
    <header>API Endpoint</header>
    <div style="display:flex;align-items:center;gap:0.4rem;">
        <pre style="user-select: all; cursor: pointer; margin:0; flex:1; overflow-x:auto;">%s</pre>
        %s
    </div>
    %s
    <p><a href="/settings">Settings →</a></p>
</article>`,
		availableHTML,
		successBuilds,
		len(registeredModels),
		apiURL,
		apiCopyBtn,
		chatLinkHTML,
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
	respondHTML(w)
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("template render error", "name", name, "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}
