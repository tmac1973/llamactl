package models

import (
	"fmt"
	"strings"
)

// GPUOption represents one selectable GPU assignment choice for the dropdown.
type GPUOption struct {
	Value     string // "all", "0", "0-1", "2-3", "custom"
	Label     string // "All GPUs", "GPU 0", "GPUs 0-1"
	GPUs      []int  // which GPU indices (e.g., [0,1])
	Disabled  bool   // model doesn't fit
	Recommend bool   // suggested option
}

// GPUAssignOptions generates all GPU assignment options for a given number of GPUs.
// Returns single GPUs, contiguous pairs, triples (for 4+ GPUs), "all", and "custom".
func GPUAssignOptions(numGPUs int) []GPUOption {
	if numGPUs <= 0 {
		return nil
	}

	var opts []GPUOption

	// "All GPUs" is always first
	allGPUs := make([]int, numGPUs)
	for i := range allGPUs {
		allGPUs[i] = i
	}
	opts = append(opts, GPUOption{
		Value: "all",
		Label: fmt.Sprintf("All GPUs (%d)", numGPUs),
		GPUs:  allGPUs,
	})

	// Single GPUs
	for i := 0; i < numGPUs; i++ {
		opts = append(opts, GPUOption{
			Value: fmt.Sprintf("%d", i),
			Label: fmt.Sprintf("GPU %d", i),
			GPUs:  []int{i},
		})
	}

	// Contiguous pairs
	if numGPUs >= 2 {
		for i := 0; i <= numGPUs-2; i++ {
			opts = append(opts, GPUOption{
				Value: fmt.Sprintf("%d-%d", i, i+1),
				Label: fmt.Sprintf("GPUs %d-%d", i, i+1),
				GPUs:  []int{i, i + 1},
			})
		}
	}

	// Contiguous triples
	if numGPUs >= 4 {
		for i := 0; i <= numGPUs-3; i++ {
			opts = append(opts, GPUOption{
				Value: fmt.Sprintf("%d-%d", i, i+2),
				Label: fmt.Sprintf("GPUs %d-%d", i, i+2),
				GPUs:  []int{i, i + 1, i + 2},
			})
		}
	}

	// "Custom" is always last
	opts = append(opts, GPUOption{
		Value: "custom",
		Label: "Custom (manual tensor split)",
	})

	return opts
}

// ResolveGPUAssign converts a GPU assignment string into tensor-split, split-mode,
// and main-gpu values for llama-server.
//
//	"all"    → ("", "", 0)         — default behavior
//	"0"      → ("1,0,0,0", "layer", 0)
//	"0-1"    → ("1,1,0,0", "layer", 0)
//	"2-3"    → ("0,0,1,1", "layer", 2)
//	"custom" → ("", "", 0)        — caller preserves raw TensorSplit
func ResolveGPUAssign(assign string, numGPUs int) (tensorSplit, splitMode string, mainGPU int) {
	if assign == "" || assign == "all" || assign == "custom" || numGPUs <= 0 {
		return "", "", 0
	}

	// Parse the assignment to get GPU indices
	gpus := parseGPURange(assign)
	if len(gpus) == 0 {
		return "", "", 0
	}

	// Build tensor-split string: 1 for active GPUs, 0 for inactive
	parts := make([]string, numGPUs)
	for i := range parts {
		parts[i] = "0"
	}
	for _, g := range gpus {
		if g >= 0 && g < numGPUs {
			parts[g] = "1"
		}
	}

	return strings.Join(parts, ","), "layer", gpus[0]
}

// parseGPURange parses GPU assignment values like "0", "0-1", "2-3" into indices.
func parseGPURange(assign string) []int {
	// Try single GPU: "0", "1", etc.
	if len(assign) == 1 && assign[0] >= '0' && assign[0] <= '9' {
		return []int{int(assign[0] - '0')}
	}

	// Try range: "0-1", "2-3", etc.
	var start, end int
	if n, _ := fmt.Sscanf(assign, "%d-%d", &start, &end); n == 2 && end >= start {
		gpus := make([]int, 0, end-start+1)
		for i := start; i <= end; i++ {
			gpus = append(gpus, i)
		}
		return gpus
	}

	return nil
}

// GPUAllocation describes one model's footprint per GPU.
type GPUAllocation struct {
	ModelID   string
	ModelName string
	GPUs      []int
	PerGPUGB  float64
	TotalGB   float64
	Color     string
}

// allocationColors are the colors assigned to models in the GPU map.
var allocationColors = []string{
	"#4e79a7", "#f28e2b", "#e15759", "#76b7b2",
	"#59a14f", "#edc948", "#b07aa1", "#ff9da7",
	"#9c755f", "#bab0ac",
}

// ComputeAllocations builds the GPU allocation list from enabled models.
func ComputeAllocations(modelList []*Model, configs map[string]*ModelConfig, numGPUs int) []GPUAllocation {
	if numGPUs <= 0 {
		return nil
	}

	var allocs []GPUAllocation
	colorIdx := 0

	for _, m := range modelList {
		cfg, ok := configs[m.ID]
		if !ok || !cfg.Enabled {
			continue
		}

		totalGB := VRAMEstimateForConfig(m, cfg)
		gpus := resolveModelGPUs(cfg, numGPUs)
		perGPU := totalGB
		if len(gpus) > 0 {
			perGPU = totalGB / float64(len(gpus))
		}

		allocs = append(allocs, GPUAllocation{
			ModelID:   m.ID,
			ModelName: shortModelName(m.ModelID),
			GPUs:      gpus,
			PerGPUGB:  perGPU,
			TotalGB:   totalGB,
			Color:     allocationColors[colorIdx%len(allocationColors)],
		})
		colorIdx++
	}

	return allocs
}

// resolveModelGPUs determines which GPUs a model is assigned to.
func resolveModelGPUs(cfg *ModelConfig, numGPUs int) []int {
	if cfg.GPUAssign != "" && cfg.GPUAssign != "all" && cfg.GPUAssign != "custom" {
		if gpus := parseGPURange(cfg.GPUAssign); len(gpus) > 0 {
			return gpus
		}
	}

	// "all", "custom" with tensor-split, or default: assume all GPUs
	all := make([]int, numGPUs)
	for i := range all {
		all[i] = i
	}
	return all
}

// shortModelName extracts a short display name from a model ID like "org/repo-GGUF".
func shortModelName(modelID string) string {
	// Strip org prefix
	if idx := strings.LastIndex(modelID, "/"); idx >= 0 {
		modelID = modelID[idx+1:]
	}
	// Strip common suffixes
	modelID = strings.TrimSuffix(modelID, "-GGUF")
	modelID = strings.TrimSuffix(modelID, "-gguf")
	return modelID
}

// GPUAssignLabel returns a short human-readable label for a GPU assignment.
func GPUAssignLabel(assign string) string {
	switch {
	case assign == "" || assign == "all":
		return "all gpus"
	case assign == "custom":
		return "custom"
	default:
		return "gpu:" + assign
	}
}

// MarkRecommended marks the best GPU option: fewest GPUs that fit,
// preferring least-allocated GPUs.
func MarkRecommended(options []GPUOption, modelVRAMGB float64, perGPUGB float64, existing []GPUAllocation) {
	if len(options) == 0 || perGPUGB <= 0 {
		return
	}

	// Calculate current usage per GPU
	gpuUsed := make(map[int]float64)
	for _, a := range existing {
		for _, g := range a.GPUs {
			gpuUsed[g] += a.PerGPUGB
		}
	}

	bestIdx := -1
	bestScore := -1.0

	for i := range options {
		opt := &options[i]
		if opt.Value == "custom" {
			continue
		}

		gpuCount := len(opt.GPUs)
		if gpuCount == 0 {
			continue
		}

		perGPUNeed := modelVRAMGB / float64(gpuCount)

		// Check if model fits on these GPUs
		fits := true
		totalFree := 0.0
		for _, g := range opt.GPUs {
			free := perGPUGB - gpuUsed[g]
			if free < perGPUNeed {
				fits = false
			}
			totalFree += free
		}

		if !fits {
			opt.Disabled = true
			continue
		}

		// Score: prefer fewer GPUs, then more free space
		score := totalFree - float64(gpuCount)*100 // heavily penalize more GPUs
		if bestIdx == -1 || score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	if bestIdx >= 0 {
		options[bestIdx].Recommend = true
	}
}
