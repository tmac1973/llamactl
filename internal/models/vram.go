package models

// EstimateVRAM returns estimated VRAM in GB for a model file.
// Uses file_size * 1.1 as a rough estimate (overhead for KV cache and buffers).
func EstimateVRAM(sizeBytes int64) float64 {
	return float64(sizeBytes) * 1.1 / (1024 * 1024 * 1024)
}

// VRAMFitCategory returns a fit category based on estimated VRAM.
// "single" = fits on one 32GB GPU, "dual" = fits across two (64GB), "too_large"
func VRAMFitCategory(estimatedGB float64) string {
	switch {
	case estimatedGB <= 32:
		return "single"
	case estimatedGB <= 64:
		return "dual"
	default:
		return "too_large"
	}
}
