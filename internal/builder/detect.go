package builder

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// RunningInContainer reports whether the current process is running inside a
// Docker- or Podman-style container. Docker creates /.dockerenv at the
// container root; Podman/CRI-O create /run/.containerenv. Either marker is
// sufficient; we don't fall back to /proc/1/cgroup parsing because cgroup v2
// no longer reliably names the runtime.
func RunningInContainer() bool {
	for _, p := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// Backend represents a detected GPU compute backend.
type Backend struct {
	Name      string   `json:"name"`      // "rocm", "cuda", "vulkan", "metal", "cpu"
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
		detectMetal(),
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

	// Vulkan in containers requires careful host driver/ICD passthrough that
	// we don't manage; restrict the profile to host-mode installs.
	if RunningInContainer() {
		b.Info = "vulkan builds are only supported in host mode"
		return b
	}

	if _, err := exec.LookPath("vulkaninfo"); err != nil {
		b.Info = "vulkaninfo not found"
		return b
	}

	out, err := exec.Command("vulkaninfo", "--summary").Output()
	if err != nil {
		b.Info = "vulkaninfo failed to run"
		return b
	}

	// Parse "deviceName" lines from --summary; one per physical device.
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "deviceName") {
			continue
		}
		// Format: "deviceName        = AMD Radeon RX 7900 XTX"
		if idx := strings.Index(line, "="); idx >= 0 {
			name := strings.TrimSpace(line[idx+1:])
			if name != "" && !strings.Contains(strings.ToLower(name), "llvmpipe") {
				b.GPUs = append(b.GPUs, name)
			}
		}
	}

	if len(b.GPUs) > 0 {
		b.Available = true
		b.Info = strings.Join(b.GPUs, ", ")
	} else {
		// vulkaninfo ran but only reported software (llvmpipe) or nothing useful.
		// Mark unavailable so users don't pick it expecting GPU acceleration.
		b.Info = "vulkaninfo found but no hardware Vulkan devices"
	}
	return b
}

func detectMetal() Backend {
	b := Backend{Name: "metal"}
	if runtime.GOOS != "darwin" {
		b.Info = "Metal is macOS-only"
		return b
	}
	b.Available = true
	b.Info = "Apple Metal (always available on macOS)"
	return b
}

