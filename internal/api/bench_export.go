package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tmlabonte/llamactl/internal/benchmark"
)

// ExportEnvelope is the on-disk JSON shape for both per-job and
// per-selection exports. Always includes a `version` so we can grow
// the shape without breaking re-importers.
//
//   - Per-job exports: Jobs has one entry (the requested job), Runs
//     contains every run belonging to it.
//   - Per-selection exports: Jobs is the unique set of jobs the runs
//     belong to (lookup by id), Runs is the user's exact selection.
type ExportEnvelope struct {
	Version int                       `json:"version"`
	Jobs    []*benchmark.BenchmarkJob `json:"jobs,omitempty"`
	Runs    []benchmark.BenchmarkRun  `json:"runs"`
}

const exportEnvelopeVersion = 1

const (
	exportFormatCSV  = "csv"
	exportFormatJSON = "json"
	exportScopeCells = "cells"
	exportScopeSum   = "summary"
)

// jobByID looks up a job pointer in a slice without scanning twice.
type jobLookup map[string]*benchmark.BenchmarkJob

func newJobLookup(jobs []*benchmark.BenchmarkJob) jobLookup {
	m := make(jobLookup, len(jobs))
	for _, j := range jobs {
		m[j.ID] = j
	}
	return m
}

// writeJSONExport serializes the envelope with stable indentation.
func writeJSONExport(w http.ResponseWriter, filename string, env ExportEnvelope) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

// writeCSVExport writes either per-cell rows (one per result) or per-cell
// summary rows (one per run), with full job/build/preset/source context
// per the column schema in plan/batch-benchmarks.md.
func writeCSVExport(w http.ResponseWriter, filename string, runs []benchmark.BenchmarkRun, jobs jobLookup, scope string) error {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	cw := csv.NewWriter(w)
	defer cw.Flush()

	switch scope {
	case exportScopeSum:
		return writeCSVSummary(cw, runs, jobs)
	case exportScopeCells, "":
		return writeCSVCells(cw, runs, jobs)
	default:
		return fmt.Errorf("unknown scope %q (want cells or summary)", scope)
	}
}

func writeCSVCells(cw *csv.Writer, runs []benchmark.BenchmarkRun, jobs jobLookup) error {
	header := []string{
		"job_id", "job_name", "run_id", "created_at",
		"model_id", "model_name", "quant",
		"build_id", "build_profile", "git_ref",
		"preset", "source",
		"prompt_tokens", "gen_tokens", "depth", "concurrency", "repetition",
		"pp_throughput", "pp_throughput_std",
		"tg_throughput", "tg_throughput_std",
		"peak_throughput", "peak_throughput_std",
		"ttft_ms", "ttfr_ms", "e2e_ttft_ms", "total_ms",
	}
	if err := cw.Write(header); err != nil {
		return err
	}

	for _, run := range runs {
		jobName := ""
		if j := jobs[run.JobID]; j != nil {
			jobName = j.Name
		}
		build := run.EffectiveBuild()
		base := []string{
			run.JobID, jobName, run.ID, run.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			run.ModelID, run.ModelName, run.Quant,
			build.ID, build.Profile, build.GitRef,
			run.Preset,
		}

		if len(run.Results) > 0 {
			for _, r := range run.Results {
				row := append([]string(nil), base...)
				row = append(row,
					"internal",
					itoa(r.PromptTokens), itoa(r.GenTokens), "", "1", itoa(r.Repetition),
					ftoa(r.PromptTokPerSec), "",
					ftoa(r.GenTokPerSec), "",
					"", "",
					ftoa(r.TTFTMs), "", "", ftoa(r.TotalMs),
				)
				if err := cw.Write(row); err != nil {
					return err
				}
			}
		}

		for _, b := range run.LlamaBenchy {
			row := append([]string(nil), base...)
			row = append(row,
				"benchy",
				itoa(b.PromptSize), itoa(b.ResponseSize), itoa(b.ContextSize), itoa(b.Concurrency), "",
				metricMean(b.PPThroughput), metricStd(b.PPThroughput),
				metricMean(b.TGThroughput), metricStd(b.TGThroughput),
				metricMean(b.PeakThroughput), metricStd(b.PeakThroughput),
				"", metricMean(b.TTFR), metricMean(b.E2ETTFT), "",
			)
			if err := cw.Write(row); err != nil {
				return err
			}
		}

		// If a run has neither internal results nor benchy results
		// (e.g. failed before producing any), emit a single
		// header-only row so the run still shows up in the export.
		if len(run.Results) == 0 && len(run.LlamaBenchy) == 0 {
			row := append([]string(nil), base...)
			source := "internal"
			if run.BenchyCommand != "" {
				source = "benchy"
			}
			row = append(row, source, "", "", "", "", "", "", "", "", "", "", "", "", "", "", "")
			if err := cw.Write(row); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeCSVSummary(cw *csv.Writer, runs []benchmark.BenchmarkRun, jobs jobLookup) error {
	header := []string{
		"job_id", "job_name", "run_id", "created_at",
		"model_id", "model_name", "quant",
		"build_id", "build_profile", "git_ref",
		"preset", "source", "status",
		"avg_pp_throughput", "avg_tg_throughput", "avg_ttft_ms",
		"min_tg_throughput", "max_tg_throughput",
		"result_count", "duration_ms",
	}
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, run := range runs {
		jobName := ""
		if j := jobs[run.JobID]; j != nil {
			jobName = j.Name
		}
		build := run.EffectiveBuild()
		source := "internal"
		if len(run.LlamaBenchy) > 0 || run.BenchyCommand != "" {
			source = "benchy"
		}
		count := len(run.Results) + len(run.LlamaBenchy)

		var avgPP, avgTG, avgTTFT, minTG, maxTG string
		if run.Summary != nil {
			avgPP = ftoa(run.Summary.AvgPromptTokPerSec)
			avgTG = ftoa(run.Summary.AvgGenTokPerSec)
			avgTTFT = ftoa(run.Summary.AvgTTFTMs)
			minTG = ftoa(run.Summary.MinGenTokPerSec)
			maxTG = ftoa(run.Summary.MaxGenTokPerSec)
		}

		row := []string{
			run.JobID, jobName, run.ID, run.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			run.ModelID, run.ModelName, run.Quant,
			build.ID, build.Profile, build.GitRef,
			run.Preset, source, run.Status,
			avgPP, avgTG, avgTTFT,
			minTG, maxTG,
			itoa(count), strconv.FormatInt(run.DurationMs, 10),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	return nil
}

func itoa(n int) string { return strconv.Itoa(n) }

func ftoa(f float64) string {
	if f == 0 {
		return "0"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func metricMean(m *benchmark.LlamaBenchyMetric) string {
	if m == nil {
		return ""
	}
	return ftoa(m.Mean)
}
func metricStd(m *benchmark.LlamaBenchyMetric) string {
	if m == nil {
		return ""
	}
	return ftoa(m.Std)
}

// parseExportFormat normalizes the format query param. Defaults to csv
// because the typical browser flow downloads a spreadsheet-friendly file.
func parseExportFormat(r *http.Request) (string, error) {
	v := strings.ToLower(r.URL.Query().Get("format"))
	switch v {
	case "":
		return exportFormatCSV, nil
	case exportFormatCSV, exportFormatJSON:
		return v, nil
	default:
		return "", fmt.Errorf("format must be %q or %q", exportFormatCSV, exportFormatJSON)
	}
}

func parseExportScope(r *http.Request) (string, error) {
	v := strings.ToLower(r.URL.Query().Get("scope"))
	switch v {
	case "":
		return exportScopeCells, nil
	case exportScopeCells, exportScopeSum:
		return v, nil
	default:
		return "", fmt.Errorf("scope must be %q or %q", exportScopeCells, exportScopeSum)
	}
}
