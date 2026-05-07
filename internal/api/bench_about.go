package api

import (
	"net/http"

	"github.com/tmlabonte/llamactl/internal/benchmark"
)

// AboutBenchmarks is the JSON shape for /api/benchmarks/about. The HTMX
// branch renders the same data through templates/partials/bench_about.html.
type AboutBenchmarks struct {
	InternalPrompt struct {
		Text             string `json:"text"`
		RepetitionPrefix string `json:"repetition_prefix"`
		CharsPerToken    int    `json:"chars_per_token"`
	} `json:"internal_prompt"`
	Presets        []benchmark.Preset `json:"presets"`
	BenchyCommands []BenchyCommandExample `json:"benchy_commands,omitempty"`
}

// BenchyCommandExample is the exact uvx invocation we'd run for one of
// the benchy-* presets. Placeholder values mark the per-cell variables
// (router URL, served model name, HuggingFace tokenizer id) so the
// modal can show the shape of the command without committing to a
// specific model.
type BenchyCommandExample struct {
	PresetName string `json:"preset_name"`
	Command    string `json:"command"`
}

// handleBenchmarksAbout returns the disclosure data the "About benchmarks"
// modal needs: the internal prompt prose, per-rep prefix template, the
// live preset table, and an example uvx llama-benchy command per benchy
// preset. HTMX requests get the rendered partial; JSON callers get the
// raw struct.
func (s *Server) handleBenchmarksAbout(w http.ResponseWriter, r *http.Request) {
	about := AboutBenchmarks{
		Presets: benchmark.Presets(),
	}
	about.InternalPrompt.Text = benchmark.BenchPromptText
	about.InternalPrompt.RepetitionPrefix = benchmark.BenchPromptPrefixTemplate
	about.InternalPrompt.CharsPerToken = benchmark.BenchPromptCharsPerToken

	for _, p := range about.Presets {
		if p.EffectiveSource() != benchmark.PresetSourceBenchy {
			continue
		}
		conc := p.Concurrency
		if len(conc) == 0 {
			conc = []int{1}
		}
		cmd := benchmark.FormatBenchyCommand(benchmark.BenchyConfig{
			BaseURL:         "http://127.0.0.1:{routerPort}/v1",
			APIKey:          "EMPTY",
			ServedModelName: "{router-served-model-name}",
			Tokenizer:       "{hf-repo-id}",
			PromptSizes:     p.PromptTokens,
			GenSizes:        []int{p.GenTokens},
			Runs:            p.Repetitions,
			Concurrency:     conc,
			SaveResultPath:  "{tmpfile.json}",
		})
		about.BenchyCommands = append(about.BenchyCommands, BenchyCommandExample{
			PresetName: p.Name,
			Command:    cmd,
		})
	}

	if isHTMX(r) {
		respondHTML(w)
		s.renderPartial(w, "bench_about", about)
		return
	}
	respondJSON(w, about)
}
