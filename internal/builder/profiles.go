package builder

// BuildProfile defines cmake flags for a build configuration.
type BuildProfile struct {
	Name       string            `json:"name"`
	Backend    string            `json:"backend"` // "rocm", "vulkan", "cpu"
	CMakeFlags map[string]string `json:"cmake_flags"`
}

// BuildOption describes a toggleable cmake flag for a profile.
type BuildOption struct {
	Flag        string `json:"flag"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Default     bool   `json:"default"`
}

// ProfileOptions returns the toggleable build options for a given profile.
func ProfileOptions(profile string) []BuildOption {
	common := []BuildOption{
		{
			Flag:        "GGML_NATIVE",
			Label:       "Native CPU Optimizations",
			Description: "Compile with -march=native for best performance on this machine. Disable if building for a different CPU.",
			Default:     true,
		},
	}

	switch profile {
	case "cuda":
		return append(common, []BuildOption{
			{
				Flag:        "GGML_CUDA_F16",
				Label:       "CUDA FP16",
				Description: "Use FP16 for CUDA operations. Faster on RTX cards with minor accuracy tradeoff.",
				Default:     false,
			},
			{
				Flag:        "GGML_CUDA_FORCE_MMQ",
				Label:       "Force Custom Matrix Multiply",
				Description: "Use llama.cpp's custom matrix multiply kernels instead of cuBLAS. Can be faster on some GPUs.",
				Default:     false,
			},
			{
				Flag:        "GGML_CUDA_ENABLE_UNIFIED_MEMORY",
				Label:       "Unified Memory (VRAM Overflow)",
				Description: "Allow models larger than VRAM to overflow into system RAM. Slower but lets you run bigger models.",
				Default:     false,
			},
		}...)
	case "rocm":
		return append(common, []BuildOption{
			{
				Flag:        "LLAMA_HIP_UMA",
				Label:       "HIP Unified Memory Access",
				Description: "Enable unified memory for ROCm. Enable for APUs only — hurts performance on discrete GPUs.",
				Default:     false,
			},
			{
				Flag:        "GGML_CUDA_ENABLE_UNIFIED_MEMORY",
				Label:       "Unified Memory (VRAM Overflow)",
				Description: "Allow models larger than VRAM to overflow into system RAM. Slower but lets you run bigger models.",
				Default:     false,
			},
		}...)
	case "vulkan":
		return common
	case "cpu":
		return common
	default:
		return common
	}
}

// DefaultProfiles returns built-in profiles for each backend.
// The ROCm profile auto-detects GPU targets from rocminfo.
func DefaultProfiles() []BuildProfile {
	// Detect GPU targets for ROCm
	gpuTargets := "gfx1100" // fallback
	for _, b := range DetectBackends() {
		if b.Name == "rocm" && len(b.GPUs) > 0 {
			gpuTargets = uniqueJoin(b.GPUs, ";")
			break
		}
	}

	return []BuildProfile{
		{
			Name:    "rocm",
			Backend: "rocm",
			CMakeFlags: map[string]string{
				"GGML_HIP":        "ON",
				"AMDGPU_TARGETS":  gpuTargets,
				"CMAKE_BUILD_TYPE": "Release",
			},
		},
		{
			Name:    "cuda",
			Backend: "cuda",
			CMakeFlags: map[string]string{
				"GGML_CUDA":        "ON",
				"CMAKE_BUILD_TYPE": "Release",
			},
		},
		{
			Name:    "vulkan",
			Backend: "vulkan",
			CMakeFlags: map[string]string{
				"GGML_VULKAN":      "ON",
				"CMAKE_BUILD_TYPE": "Release",
			},
		},
		{
			Name:    "cpu",
			Backend: "cpu",
			CMakeFlags: map[string]string{
				"CMAKE_BUILD_TYPE": "Release",
			},
		},
	}
}

// FindProfile returns the profile matching the given name.
func FindProfile(name string) (BuildProfile, bool) {
	for _, p := range DefaultProfiles() {
		if p.Name == name {
			return p, true
		}
	}
	return BuildProfile{}, false
}

// uniqueJoin deduplicates strings and joins with sep.
func uniqueJoin(items []string, sep string) string {
	seen := map[string]bool{}
	var out []string
	for _, s := range items {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	result := ""
	for i, s := range out {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}
