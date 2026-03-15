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
		detectCUDA(),
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

func detectCUDA() Backend {
	b := Backend{Name: "cuda"}

	out, err := exec.Command("nvidia-smi",
		"--query-gpu=name",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		b.Info = "nvidia-smi not found or failed"
		return b
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name := strings.TrimSpace(line)
		if name != "" {
			b.GPUs = append(b.GPUs, name)
		}
	}

	if len(b.GPUs) > 0 {
		b.Available = true
		b.Info = strings.Join(b.GPUs, ", ")
	} else {
		b.Info = "nvidia-smi found but no GPUs detected"
	}
	return b
}

func detectVulkan() Backend {
	b := Backend{Name: "vulkan"}

	// Check if Vulkan development libraries are installed (sufficient for building).
	// We check for the Vulkan header rather than vulkaninfo, since the runtime
	// ICD/loader version may not match the host driver (e.g. container has Vulkan 1.3
	// loader but host has 1.4 driver). The actual GPU compatibility is validated
	// when llama.cpp runs, not at detection time.
	if _, err := exec.Command("pkg-config", "--exists", "vulkan").CombinedOutput(); err == nil {
		b.Available = true
		b.Info = "Vulkan SDK available"
	} else if _, err := exec.Command("test", "-f", "/usr/include/vulkan/vulkan.h").CombinedOutput(); err == nil {
		b.Available = true
		b.Info = "Vulkan SDK available"
	} else {
		b.Info = "Vulkan development libraries not installed"
		return b
	}

	// Try to get GPU info from vulkaninfo (best-effort, may fail due to loader/driver mismatch)
	if out, err := exec.Command("vulkaninfo", "--summary").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "deviceName") {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					name := strings.TrimSpace(parts[1])
					if name != "" {
						b.GPUs = append(b.GPUs, name)
					}
				}
			}
		}
		if len(b.GPUs) > 0 {
			b.Info = strings.Join(b.GPUs, ", ")
		}
	}

	return b
}
