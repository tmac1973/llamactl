package builder

import (
	"os/exec"
	"strings"
)

// Backend represents a detected GPU compute backend.
type Backend struct {
	Name      string   `json:"name"`      // "rocm", "vulkan", "cpu"
	Available bool     `json:"available"`
	GPUs      []string `json:"gpus"`      // e.g. ["gfx1201", "gfx1201"]
	Info      string   `json:"info"`      // human-readable summary
}

// DetectBackends probes the system for available GPU compute backends.
func DetectBackends() []Backend {
	backends := []Backend{
		detectROCm(),
		detectVulkan(),
		{Name: "cpu", Available: true, Info: "CPU fallback (always available)"},
	}
	return backends
}

func detectROCm() Backend {
	b := Backend{Name: "rocm"}

	out, err := exec.Command("rocminfo").Output()
	if err != nil {
		b.Info = "rocminfo not found or failed"
		return b
	}

	// Parse GPU agent names from rocminfo output.
	// Only match short gfx IDs (e.g. "gfx1100"), skip triple-format like "amdgcn-amd-amdhsa--gfx1100".
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Name:") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(line, "Name:"))
		if strings.HasPrefix(name, "gfx") && !strings.Contains(name, "-") {
			b.GPUs = append(b.GPUs, name)
		}
	}

	if len(b.GPUs) > 0 {
		b.Available = true
		b.Info = strings.Join(b.GPUs, ", ")
	} else {
		b.Info = "rocminfo found but no GPU agents detected"
	}
	return b
}

func detectVulkan() Backend {
	b := Backend{Name: "vulkan"}

	err := exec.Command("vulkaninfo", "--summary").Run()
	if err != nil {
		b.Info = "vulkaninfo not found or failed"
		return b
	}

	b.Available = true
	b.Info = "Vulkan runtime detected"
	return b
}
