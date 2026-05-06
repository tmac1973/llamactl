package models

import (
	"fmt"
	"math"
)

const vramOverheadGB = 0.2 // fixed overhead for compute buffers, scratch space, etc.

// BytesToGB converts bytes to gigabytes.
func BytesToGB(b int64) float64 {
	return float64(b) / (1024 * 1024 * 1024)
}

// EstimateVRAM returns a rough VRAM estimate in GB based on file size alone.
// Used as a fallback when GGUF metadata isn't available.
func EstimateVRAM(sizeBytes int64) float64 {
	return float64(sizeBytes)*1.1/(1024*1024*1024) + vramOverheadGB
}

// EstimateVRAMDetailed returns an estimated VRAM usage in GB accounting for
// model weights, KV cache at a given context size, and overhead.
//
// Parameters:
//   - sizeBytes: model file size (approximates weight memory)
//   - nLayers, nKVHead, nHead, nEmbd: architecture params from GGUF
//   - contextSize: token context window (0 = model default, treated as 2048)
//   - kvCacheQuant: "", "q8_0", "q4_0"
func EstimateVRAMDetailed(sizeBytes int64, nLayers, nKVHead, nHead, nEmbd, contextSize int, kvCacheQuant string) float64 {
	// Weight memory: file size is a good proxy for quantized weights in VRAM.
	weightsGB := BytesToGB(sizeBytes)

	// KV cache estimate
	kvGB := EstimateKVCacheGB(nLayers, nKVHead, nHead, nEmbd, contextSize, kvCacheQuant)

	return weightsGB + kvGB + vramOverheadGB
}

// EstimateKVCacheGB returns the estimated KV cache size in GB.
//
// Formula: 2 (K+V) × n_layers × n_kv_head × head_dim × ctx × bytes_per_element
func EstimateKVCacheGB(nLayers, nKVHead, nHead, nEmbd, contextSize int, kvCacheQuant string) float64 {
	if nLayers == 0 || nEmbd == 0 {
		return 0
	}

	// If KV heads not specified, fall back to full attention (n_kv_head = n_head)
	kvHeads := nKVHead
	if kvHeads == 0 {
		kvHeads = nHead
	}
	if kvHeads == 0 {
		return 0
	}

	// Head dimension
	headDim := nEmbd
	if nHead > 0 {
		headDim = nEmbd / nHead
	}

	// Default context if not set
	ctx := contextSize
	if ctx == 0 {
		ctx = 2048
	}

	// Bytes per element based on KV cache quantization
	var bytesPerElem float64
	switch kvCacheQuant {
	case "q4_0":
		bytesPerElem = 0.5625 // 4.5 bits = 0.5625 bytes (4 bits + 0.5 bit block scale)
	case "q8_0":
		bytesPerElem = 1.0625 // 8.5 bits (8 bits + 0.5 bit block scale)
	default:
		bytesPerElem = 2.0 // f16
	}

	// 2 (K + V) × layers × kv_heads × head_dim × context × bytes
	totalBytes := 2.0 * float64(nLayers) * float64(kvHeads) * float64(headDim) * float64(ctx) * bytesPerElem

	return totalBytes / (1024 * 1024 * 1024)
}

// VRAMEstimateForConfig returns the total estimated VRAM for a model with
// the given configuration. This is the primary function used by the UI.
func VRAMEstimateForConfig(m *Model, cfg *ModelConfig) float64 {
	if m.NLayers == 0 || m.NEmbd == 0 {
		// No GGUF metadata — fall back to rough estimate
		return EstimateVRAM(m.SizeBytes)
	}
	// ContextSize 0 means "Model Default" in the UI; llama-server resolves
	// that to the model's trained context length at load time, so estimate
	// against that, not the bare 2048 fallback in EstimateKVCacheGB.
	ctx := cfg.ContextSize
	if ctx == 0 {
		ctx = m.ContextLength
	}
	return EstimateVRAMDetailed(
		m.SizeBytes,
		m.NLayers, m.NKVHead, m.NHead, m.NEmbd,
		ctx,
		cfg.KVCacheQuant,
	)
}

// VRAMFitLabel returns a human-readable label for how a model fits
// relative to the available VRAM. perGPU is the size of one GPU in GB,
// numGPUs is how many are available. For tensor parallelism, estimatedGB
// is automatically divided by numberProcessors.
// Returns: "fits" (single GPU), "2 GPU", "3 GPU", etc., or "too_large".
func VRAMFitLabel(estimatedGB float64, perGPU float64, numGPUs int, numberProcessors int) string {
	if perGPU <= 0 || numGPUs <= 0 {
		return ""
	}

	// For tensor parallelism, divide estimated VRAM by number of processors
	// since tensors are split across all GPUs
	if numberProcessors > 0 && numberProcessors < numGPUs {
		numGPUs = numberProcessors
	}
	totalVRAM := perGPU * float64(numGPUs)
	needed := int(math.Ceil(estimatedGB / perGPU))
	if needed <= 0 {
		needed = 1
	}

	if estimatedGB > totalVRAM {
		return "too_large"
	}
	if needed == 1 {
		return "fits"
	}
	return fmt.Sprintf("%d GPU", needed)
}

// FormatVRAM formats a VRAM estimate as a human-readable string.
func FormatVRAM(gb float64) string {
	if gb < 1 {
		return formatFloat(gb*1024, 0) + " MB"
	}
	return formatFloat(gb, 1) + " GB"
}

func formatFloat(f float64, decimals int) string {
	p := math.Pow(10, float64(decimals))
	return trimTrailingZeros(math.Round(f*p) / p)
}

func trimTrailingZeros(f float64) string {
	s := math.Floor(f)
	if f == s {
		return fmt.Sprintf("%.0f", f)
	}
	return fmt.Sprintf("%.1f", f)
}
