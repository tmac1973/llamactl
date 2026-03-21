package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RunConfig holds everything needed to execute a benchmark.
type RunConfig struct {
	Run        BenchmarkRun
	Preset     Preset
	ModelPath  string // GGUF file path for llama-bench
	RouterURL  string // e.g. "http://localhost:8080"
	RouterName string // model name the router knows
	BinaryDir  string // build dir containing llama-bench
}

// ProgressUpdate is sent during benchmark execution.
type ProgressUpdate struct {
	Stage   string // "loading", "warmup", "benchmark", "llama-bench", "done", "error"
	Detail  string // e.g. "512 tokens, rep 2/3"
	Pct     int    // 0-100
}

// Runner executes benchmarks.
type Runner struct {
	store *Store
}

// NewRunner creates a benchmark runner.
func NewRunner(store *Store) *Runner {
	return &Runner{store: store}
}

// Run executes a benchmark. Sends progress updates to the channel (may be nil).
// The RunConfig.Run must already be saved to the store with StatusRunning.
func (r *Runner) Run(ctx context.Context, cfg RunConfig, progress chan<- ProgressUpdate) {
	run := cfg.Run
	startTime := time.Now()

	defer func() {
		run.DurationMs = time.Since(startTime).Milliseconds()
		r.store.Save(run)
		if progress != nil {
			close(progress)
		}
	}()

	send := func(stage, detail string, pct int) {
		// Update the run's progress detail so the polling endpoint can serve it
		run.ProgressDetail = detail
		r.store.Save(run)
		if progress != nil {
			select {
			case progress <- ProgressUpdate{Stage: stage, Detail: detail, Pct: pct}:
			default:
			}
		}
	}

	// Step 0: Unload all models to ensure clean VRAM for benchmarking
	send("loading", "Unloading all models for clean benchmark...", 3)
	r.unloadAllModels(cfg.RouterURL)

	// Step 1: Load the benchmark target model
	send("loading", "Loading model into VRAM — this may take a minute for large models...", 5)
	if err := r.ensureModelLoaded(ctx, cfg.RouterURL, cfg.RouterName); err != nil {
		run.Status = StatusFailed
		run.Error = fmt.Sprintf("failed to load model: %v", err)
		send("error", run.Error, 0)
		return
	}

	// Step 2: Warmup — retry with backoff since model may still be initializing
	send("warmup", "Warming up — sending test request to initialize GPU kernels...", 10)
	var warmupErr error
	for attempt := 1; attempt <= 5; attempt++ {
		warmupErr = r.sendCompletion(ctx, cfg.RouterURL, cfg.RouterName, 64, 16)
		if warmupErr == nil {
			break
		}
		slog.Warn("benchmark warmup attempt failed, retrying...", "attempt", attempt, "error", warmupErr)
		select {
		case <-ctx.Done():
			run.Status = StatusFailed
			run.Error = "cancelled during warmup"
			send("error", run.Error, 0)
			return
		case <-time.After(time.Duration(attempt*3) * time.Second):
		}
	}
	if warmupErr != nil {
		run.Status = StatusFailed
		run.Error = fmt.Sprintf("warmup failed after retries: %v", warmupErr)
		send("error", run.Error, 0)
		return
	}

	// Step 3: Run benchmarks
	totalTests := 0
	for _, pp := range cfg.Preset.PromptTokens {
		_ = pp
		totalTests += cfg.Preset.Repetitions
	}
	completedTests := 0

	var lastErr error
	for _, promptTokens := range cfg.Preset.PromptTokens {
		for rep := 1; rep <= cfg.Preset.Repetitions; rep++ {
			if ctx.Err() != nil {
				run.Status = StatusFailed
				run.Error = "cancelled"
				send("error", "Cancelled", 0)
				return
			}

			completedTests++
			pct := 15 + (completedTests*70)/totalTests
			send("benchmark", fmt.Sprintf("API benchmark: %d prompt tokens, generating %d tokens (rep %d/%d)", promptTokens, cfg.Preset.GenTokens, rep, cfg.Preset.Repetitions), pct)

			result, err := r.runOneTest(ctx, cfg.RouterURL, cfg.RouterName, promptTokens, cfg.Preset.GenTokens, rep)
			if err != nil {
				lastErr = err
				slog.Error("benchmark test failed", "prompt_tokens", promptTokens, "rep", rep, "error", err)
				continue
			}
			slog.Info("benchmark result", "prompt_tokens", result.PromptTokens,
				"gen_tokens", result.GenTokens, "pp_tps", result.PromptTokPerSec,
				"tg_tps", result.GenTokPerSec)
			run.Results = append(run.Results, *result)
			// Save intermediate results
			r.store.Save(run)
		}
	}

	if len(run.Results) == 0 && lastErr != nil {
		run.Status = StatusFailed
		run.Error = fmt.Sprintf("all tests failed: %v", lastErr)
		send("error", run.Error, 0)
		return
	}

	// Step 4: llama-bench (if preset says so and binary exists)
	if cfg.Preset.RunLlamaBench && cfg.BinaryDir != "" && cfg.ModelPath != "" {
		benchBinary := filepath.Join(cfg.BinaryDir, "llama-bench")
		if _, err := os.Stat(benchBinary); err == nil {
			// Unload model from server to free VRAM for llama-bench
			send("llama-bench", "Unloading model from server for raw benchmark...", 88)
			r.unloadModel(cfg.RouterURL, cfg.RouterName)

			send("llama-bench", "Running llama-bench — raw inference without server overhead...", 90)
			lb, benchErr := r.runLlamaBench(ctx, cfg)
			if benchErr != nil {
				slog.Warn("llama-bench failed", "error", benchErr)
				run.Warnings = append(run.Warnings, fmt.Sprintf("llama-bench failed: %v", benchErr))
			} else {
				run.LlamaBench = lb
			}

			// Don't reload — the router will auto-load on next request,
			// and reloading here causes conflicts with back-to-back benchmarks.
		} else {
			run.Warnings = append(run.Warnings, "llama-bench binary not found — rebuild llama.cpp to include it")
		}
	}

	// Step 5: Compute summary
	run.Summary = ComputeSummary(run.Results)
	run.Status = StatusCompleted
	send("done", "Benchmark complete", 100)
}

// ensureModelLoaded loads the model via the router and waits for it.
// The router's /models/load blocks until the model is ready, so a successful
// response means the model is loaded and ready for inference.
func (r *Runner) ensureModelLoaded(ctx context.Context, routerURL, modelName string) error {
	slog.Info("benchmark: loading model", "name", modelName, "url", routerURL)
	body, _ := json.Marshal(map[string]string{"model": modelName})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, routerURL+"/models/load", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	// The load request can take minutes for large models — use a generous timeout
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("load request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusOK {
		slog.Info("benchmark: model loaded successfully", "name", modelName)
		return nil
	}
	if resp.StatusCode == http.StatusBadRequest && strings.Contains(string(respBody), "already loaded") {
		slog.Info("benchmark: model already loaded", "name", modelName)
		return nil
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
}

// unloadAllModels unloads every loaded model from the router and waits
// until all are confirmed unloaded before returning.
func (r *Runner) unloadAllModels(routerURL string) {
	loaded := r.listLoadedModels(routerURL)
	if len(loaded) == 0 {
		return
	}

	for _, name := range loaded {
		r.unloadModel(routerURL, name)
	}

	// Wait for all models to be confirmed unloaded (up to 30s)
	for i := 0; i < 15; i++ {
		time.Sleep(2 * time.Second)
		remaining := r.listLoadedModels(routerURL)
		if len(remaining) == 0 {
			slog.Info("benchmark: all models unloaded")
			return
		}
		slog.Info("benchmark: waiting for models to unload", "remaining", len(remaining))
	}
	slog.Warn("benchmark: timeout waiting for all models to unload")
}

// listLoadedModels returns the IDs of models currently loaded in the router.
func (r *Runner) listLoadedModels(routerURL string) []string {
	resp, err := http.Get(routerURL + "/models")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var models []struct {
		ID     string `json:"id"`
		Status struct {
			Value string `json:"value"`
		} `json:"status"`
	}
	if json.NewDecoder(resp.Body).Decode(&models) != nil {
		return nil
	}

	var loaded []string
	for _, m := range models {
		if m.Status.Value == "loaded" || m.Status.Value == "loading" {
			loaded = append(loaded, m.ID)
		}
	}
	return loaded
}

// unloadModel tells the router to unload a single model.
func (r *Runner) unloadModel(routerURL, modelName string) {
	body, _ := json.Marshal(map[string]string{"model": modelName})
	resp, err := http.Post(routerURL+"/models/unload", "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Warn("benchmark: failed to unload model", "name", modelName, "error", err)
		return
	}
	resp.Body.Close()
	slog.Info("benchmark: unload requested", "name", modelName)
}

// benchPromptText is a fixed, deterministic text used for benchmarking.
const benchPromptText = `The history of computing is a story of human ingenuity and the relentless pursuit of automation. From the earliest mechanical calculators of the 17th century to the modern silicon chips that power our world, each generation has built upon the discoveries of the last. Charles Babbage conceived of the Analytical Engine in the 1830s, a mechanical general-purpose computer that, had it been built, would have contained many features of modern computers. Ada Lovelace, working with Babbage, wrote what is often considered the first computer program. The 20th century brought electronic computing into reality. Alan Turing formalized the concept of computation itself, while engineers at the University of Pennsylvania built ENIAC, one of the first electronic general-purpose computers. The invention of the transistor at Bell Labs in 1947 revolutionized electronics, leading to smaller, faster, and more reliable computers. The integrated circuit, developed independently by Jack Kilby and Robert Noyce, made it possible to place thousands and eventually billions of transistors on a single chip. This exponential growth in computing power, described by Moore's Law, has driven decades of innovation. Personal computers brought computing to the masses in the 1980s, the internet connected them in the 1990s, and smartphones made computing truly ubiquitous in the 2000s. Today, artificial intelligence and machine learning represent the latest frontier, with large language models demonstrating remarkable capabilities in understanding and generating human language. These models, trained on vast amounts of text data, can engage in conversation, write code, analyze documents, and assist with creative tasks. The computational requirements for training and running these models have driven advances in GPU computing, distributed systems, and specialized hardware accelerators. `

// buildPrompt constructs a prompt of approximately the target token count
// by repeating the benchmark text. The repetition parameter varies the
// prompt to defeat llama.cpp's prompt cache.
func buildPrompt(targetTokens int, repetition int) string {
	// Rough approximation: 1 token ≈ 4 characters for English text
	targetChars := targetTokens * 4
	var b strings.Builder
	// Prefix with repetition-specific text to defeat prompt caching
	b.WriteString(fmt.Sprintf("This is benchmark repetition number %d. Please analyze the following text carefully and provide a detailed response.\n\n", repetition))
	for b.Len() < targetChars {
		b.WriteString(benchPromptText)
	}
	text := b.String()
	if len(text) > targetChars {
		text = text[:targetChars]
	}
	return text
}

// sendCompletion sends a chat completion and returns the timings.
func (r *Runner) sendCompletion(ctx context.Context, routerURL, model string, promptTokens, genTokens int) error {
	_, err := r.sendCompletionWithTimings(ctx, routerURL, model, promptTokens, genTokens, 0)
	return err
}

// timingsResponse is the shape of llama.cpp's timings in the response.
type timingsResponse struct {
	PromptN         int     `json:"prompt_n"`
	PromptMs        float64 `json:"prompt_ms"`
	PromptPerSec    float64 `json:"prompt_per_second"`
	PredictedN      int     `json:"predicted_n"`
	PredictedMs     float64 `json:"predicted_ms"`
	PredictedPerSec float64 `json:"predicted_per_second"`
}

func (r *Runner) sendCompletionWithTimings(ctx context.Context, routerURL, model string, promptTokens, genTokens, repetition int) (*timingsResponse, error) {
	prompt := buildPrompt(promptTokens, repetition)
	reqBody, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": genTokens,
		"stream":     false,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, routerURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Parse timings from response
	var result struct {
		Timings timingsResponse `json:"timings"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if result.Timings.PredictedN == 0 {
		return nil, fmt.Errorf("no timings in response")
	}

	return &result.Timings, nil
}

// runOneTest runs a single benchmark test point.
func (r *Runner) runOneTest(ctx context.Context, routerURL, model string, promptTokens, genTokens, rep int) (*BenchmarkResult, error) {
	timings, err := r.sendCompletionWithTimings(ctx, routerURL, model, promptTokens, genTokens, rep)
	if err != nil {
		return nil, err
	}

	return &BenchmarkResult{
		PromptTokens:    timings.PromptN,
		GenTokens:       timings.PredictedN,
		Repetition:      rep,
		PromptTokPerSec: timings.PromptPerSec,
		GenTokPerSec:    timings.PredictedPerSec,
		TTFTMs:          timings.PromptMs,
		TotalMs:         timings.PromptMs + timings.PredictedMs,
	}, nil
}

// runLlamaBench executes the llama-bench binary for raw inference benchmarking.
func (r *Runner) runLlamaBench(ctx context.Context, cfg RunConfig) (*LlamaBenchResult, error) {
	benchBinary := filepath.Join(cfg.BinaryDir, "llama-bench")

	args := []string{
		"-m", cfg.ModelPath,
		"-p", fmt.Sprintf("%d", cfg.Preset.PromptTokens[0]),
		"-n", fmt.Sprintf("%d", cfg.Preset.GenTokens),
		"-r", fmt.Sprintf("%d", cfg.Preset.Repetitions),
		"-o", "json",
		"-ngl", fmt.Sprintf("%d", cfg.Run.Config.GPULayers),
		"-t", fmt.Sprintf("%d", cfg.Run.Config.Threads),
	}
	if cfg.Run.Config.FlashAttention {
		args = append(args, "-fa", "1")
	}
	if cfg.Run.Config.TensorSplit != "" {
		args = append(args, "-ts", cfg.Run.Config.TensorSplit)
	}
	if cfg.Run.Config.DirectIO {
		args = append(args, "--direct-io", "1")
	}
	if cfg.Run.Config.KVCacheQuant != "" {
		args = append(args, "-ctk", cfg.Run.Config.KVCacheQuant, "-ctv", cfg.Run.Config.KVCacheQuant)
	}

	// Set LD_LIBRARY_PATH for shared libs co-located with the binary
	cmd := exec.CommandContext(ctx, benchBinary, args...)
	cmd.Env = append(cmd.Environ(), "LD_LIBRARY_PATH="+cfg.BinaryDir)

	slog.Info("running llama-bench", "binary", benchBinary, "args", strings.Join(args, " "))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("llama-bench: %w\nstderr: %s", err, stderr.String())
	}

	return parseLlamaBenchJSON(out)
}

// parseLlamaBenchJSON parses llama-bench JSON output.
// llama-bench outputs one JSON array with objects per test.
func parseLlamaBenchJSON(data []byte) (*LlamaBenchResult, error) {
	var entries []struct {
		TestType string  `json:"test"`     // "pp" or "tg"
		AvgTS    float64 `json:"avg_ts"`
		NPrompt  int     `json:"n_prompt"`
		NGen     int     `json:"n_gen"`
		Reps     int     `json:"n_repetitions"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse llama-bench output: %w", err)
	}

	result := &LlamaBenchResult{}
	for _, e := range entries {
		switch {
		case e.NPrompt > 0 && e.NGen == 0:
			result.PromptTokPerSec = e.AvgTS
			result.PromptTokens = e.NPrompt
			result.Repetitions = e.Reps
		case e.NGen > 0 && e.NPrompt == 0:
			result.GenTokPerSec = e.AvgTS
			result.GenTokens = e.NGen
		}
	}
	if result.PromptTokPerSec == 0 && result.GenTokPerSec == 0 {
		return nil, fmt.Errorf("no results in llama-bench output")
	}
	return result, nil
}
