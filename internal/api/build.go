package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/tmlabonte/llamactl/internal/builder"
)

func (s *Server) handleListBackends(w http.ResponseWriter, r *http.Request) {
	backends := builder.DetectBackends()
	respondJSON(w, backends)
}

func (s *Server) handleProfileOptions(w http.ResponseWriter, r *http.Request) {
	profile := r.URL.Query().Get("profile")
	options := builder.ProfileOptions(profile)

	// Read current toggle states from query params (sent on re-fetch)
	overrides := make(map[string]bool)
	hasOverrides := false
	for _, opt := range options {
		key := "opt_" + opt.Flag
		if r.URL.Query().Has(key) {
			hasOverrides = true
			overrides[opt.Flag] = r.URL.Query().Get(key) == "on"
		}
	}
	extraCMake := r.URL.Query().Get("extra_cmake")

	if isHTMX(r) {
		respondHTML(w)
		if len(options) == 0 {
			return
		}

		// Wrap in a div that re-fetches itself on toggle/input changes
		fmt.Fprintf(w, `<div id="build-options"
			hx-get="/api/builds/options"
			hx-target="this"
			hx-swap="outerHTML"
			hx-trigger="change delay:300ms"
			hx-include="#build-profile, #build-options input">`)

		w.Write([]byte(`<div style="display:grid;grid-template-columns:1fr 1fr;gap:0.25rem 1.5rem;margin-top:0.5rem;align-items:center;">`))
		for _, opt := range options {
			checked := ""
			if hasOverrides {
				if overrides[opt.Flag] {
					checked = "checked"
				}
			} else if opt.Default {
				checked = "checked"
			}
			fmt.Fprintf(w, `<label title="%s" style="display:flex;align-items:center;gap:0.5rem;margin:0;white-space:nowrap;">
				<input type="checkbox" name="opt_%s" role="switch" %s style="margin:0;">
				%s
			</label>`, html.EscapeString(opt.Description), opt.Flag, checked, html.EscapeString(opt.Label))
		}
		w.Write([]byte(`</div>`))

		// Extra cmake flags input
		fmt.Fprintf(w, `<label title="Additional cmake flags passed directly to the build. Use -DFLAG=VALUE format.">Extra CMake Flags
			<input type="text" name="extra_cmake" value="%s" placeholder="-DFOO=BAR -DBAZ=ON">
		</label>`, html.EscapeString(extraCMake))

		// Show effective cmake flags with current toggle states
		prof, ok := builder.FindProfile(profile)
		if ok {
			effectiveOverrides := make(map[string]bool)
			for _, opt := range options {
				if hasOverrides {
					effectiveOverrides[opt.Flag] = overrides[opt.Flag]
				} else {
					effectiveOverrides[opt.Flag] = opt.Default
				}
			}
			flags := effectiveCMakeFlags(prof, options, effectiveOverrides)
			if extraCMake != "" {
				flags += " " + extraCMake
			}
			fmt.Fprintf(w, `<label title="The full set of cmake flags that will be passed to the build.">Effective CMake Flags
				<input type="text" value="%s" readonly style="opacity:0.7;cursor:default;font-size:0.85rem;">
			</label>`, html.EscapeString(flags))
		}

		w.Write([]byte(`</div>`))
		return
	}

	respondJSON(w, options)
}

func effectiveCMakeFlags(prof builder.BuildProfile, options []builder.BuildOption, overrides map[string]bool) string {
	flags := make(map[string]string)
	for k, v := range prof.CMakeFlags {
		flags[k] = v
	}
	for _, opt := range options {
		enabled := opt.Default
		if overrides != nil {
			if v, ok := overrides[opt.Flag]; ok {
				enabled = v
			}
		}
		if enabled {
			flags[opt.Flag] = "ON"
		}
	}
	var parts []string
	for k, v := range flags {
		parts = append(parts, fmt.Sprintf("-D%s=%s", k, v))
	}
	// Sort for stable display
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func (s *Server) handleListRefs(w http.ResponseWriter, r *http.Request) {
	refresh := r.URL.Query().Get("refresh") == "1"

	var refs []string
	var err error
	if refresh {
		refs, err = s.builder.FetchRefs()
		if err != nil {
			// Return cached if fetch fails
			refs = s.builder.CachedRefs()
		}
	} else {
		refs = s.builder.CachedRefs()
	}

	if isHTMX(r) {
		respondHTML(w)
		w.Write([]byte(`<option value="latest">latest</option>`))
		for _, ref := range refs {
			w.Write([]byte(`<option value="` + ref + `">` + ref + `</option>`))
		}
		return
	}

	respondJSON(w, refs)
}

func (s *Server) handleListBuilds(w http.ResponseWriter, r *http.Request) {
	builds := s.builder.List()

	// If request is from htmx, return HTML partial
	if isHTMX(r) {
		if len(builds) == 0 {
			w.Write([]byte("<p>No builds yet.</p>"))
			return
		}
		respondHTML(w)
		w.Write([]byte(`<table role="grid"><thead><tr><th>Build</th><th>SHA</th><th>Status</th><th>Date</th><th></th></tr></thead><tbody>`))
		for _, b := range builds {
			s.renderPartial(w, "build_card", b)
		}
		w.Write([]byte(`</tbody></table>`))
		return
	}

	respondJSON(w, builds)
}

func (s *Server) handleTriggerBuild(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profile string `json:"profile"`
		GitRef  string `json:"git_ref"`
		Force   bool   `json:"force"`
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
		req.Force = r.FormValue("force") == "1"
	}

	// Collect option overrides and extra cmake flags from form
	var optionOverrides map[string]bool
	var extraCMake string
	if r.Header.Get("Content-Type") != "application/json" {
		options := builder.ProfileOptions(req.Profile)
		optionOverrides = make(map[string]bool)
		for _, opt := range options {
			// Checkboxes only send a value when checked
			optionOverrides[opt.Flag] = r.FormValue("opt_"+opt.Flag) == "on"
		}
		extraCMake = r.FormValue("extra_cmake")
	}

	// Use background context — the build must outlive the HTTP request.
	result, err := s.builder.Build(context.Background(), req.Profile, req.GitRef, req.Force, optionOverrides, extraCMake)
	if err != nil {
		if dup, ok := err.(*builder.DuplicateBuildError); ok {
			if isHTMX(r) {
				respondHTML(w)
				fmt.Fprintf(w, `<article>
					<p>Build <strong>%s</strong> already exists. Rebuild it?</p>
					<form hx-post="/api/builds" hx-target="#build-output" hx-swap="innerHTML">
						<input type="hidden" name="profile" value="%s">
						<input type="hidden" name="git_ref" value="%s">
						<input type="hidden" name="force" value="1">
						<div role="group">
							<button type="submit">Rebuild</button>
							<button type="button" class="secondary"
								onclick="this.closest('article').remove()">Cancel</button>
						</div>
					</form>
				</article>`, dup.ID, req.Profile, req.GitRef)
				return
			}
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Return the log streaming partial for htmx to swap in
	if isHTMX(r) {
		respondHTML(w)
		s.renderPartial(w, "build_log", result)
		return
	}

	w.WriteHeader(http.StatusAccepted)
	respondJSON(w, result)
}

func (s *Server) handleBuildLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Try the live channel first (original SSE connection during build)
	ch, ok := s.builder.LogChannel(id)
	if ok {
		StreamLines(w, r.Context(), ch, "Build complete")
		return
	}

	// Fall back to subscribe (replays history + streams new lines)
	sub := s.builder.SubscribeLogs(id)
	if sub == nil {
		http.NotFound(w, r)
		return
	}
	defer s.builder.UnsubscribeLogs(id, sub)
	StreamLines(w, r.Context(), sub, "Build complete")
}

// handleActiveBuildLog returns the build_log partial for the most recent build,
// allowing the builds page to reconnect after tab switches.
func (s *Server) handleActiveBuildLog(w http.ResponseWriter, r *http.Request) {
	respondHTML(w)
	lastID := s.builder.LastBuildID()
	if lastID == "" {
		return
	}

	status := s.builder.BuildStatus(lastID)
	if status == "" {
		return
	}

	// Show the log panel for running or recently completed builds
	s.renderPartial(w, "build_log", &builder.BuildResult{ID: lastID, Status: status})
}

func (s *Server) handleDeleteBuild(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.builder.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// htmx: return empty to remove the row
	if isHTMX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// renderPartial executes a partial template, writing directly to w.
func (s *Server) renderPartial(w http.ResponseWriter, name string, data any) {
	// Partials are shared across all page clones. Try each until one
	// succeeds. Buffer output to avoid writing partial results on error.
	var lastErr error
	for _, tmpl := range s.pages {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, name, data); err == nil {
			buf.WriteTo(w)
			return
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		slog.Error("renderPartial failed on all page templates", "name", name, "error", lastErr)
	}
}
