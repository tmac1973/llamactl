package api

import (
	"net/http"

	"github.com/tmlabonte/llamactl/internal/models"
)

// handleGPUMap renders the GPU allocation map partial.
func (s *Server) handleGPUMap(w http.ResponseWriter, r *http.Request) {
	respondHTML(w)

	metrics := s.monitor.Current()
	numGPUs := len(metrics.GPU)
	if numGPUs == 0 {
		return // render nothing when no GPUs detected
	}

	allModels := s.registry.List()
	allConfigs := make(map[string]*models.ModelConfig)
	for _, m := range allModels {
		if c, err := s.registry.GetConfig(m.ID); err == nil {
			allConfigs[m.ID] = c
		}
	}

	allocations := models.ComputeAllocations(allModels, allConfigs, numGPUs)

	// Build per-GPU data for the template
	type GPUSegment struct {
		ModelName string
		Color     string
		WidthPct  float64
	}
	type GPUBar struct {
		Index      int
		Name       string
		TotalGB    float64
		UsedGB     float64
		FreeGB     float64
		Segments   []GPUSegment
		Overcommit bool
	}

	bars := make([]GPUBar, numGPUs)
	for i, gpu := range metrics.GPU {
		totalGB := float64(gpu.VRAMTotalMB) / 1024.0
		bars[i] = GPUBar{
			Index:   gpu.Index,
			Name:    gpu.Name,
			TotalGB: totalGB,
		}

		// Collect segments for this GPU
		var usedGB float64
		for _, a := range allocations {
			for _, g := range a.GPUs {
				if g == i {
					pct := 0.0
					if totalGB > 0 {
						pct = (a.PerGPUGB / totalGB) * 100
					}
					bars[i].Segments = append(bars[i].Segments, GPUSegment{
						ModelName: a.ModelName,
						Color:     a.Color,
						WidthPct:  pct,
					})
					usedGB += a.PerGPUGB
					break
				}
			}
		}
		bars[i].UsedGB = usedGB
		bars[i].FreeGB = totalGB - usedGB
		bars[i].Overcommit = usedGB > totalGB
	}

	data := struct {
		Bars        []GPUBar
		Allocations []models.GPUAllocation
	}{
		Bars:        bars,
		Allocations: allocations,
	}
	s.renderPartial(w, "gpu_map", data)
}
