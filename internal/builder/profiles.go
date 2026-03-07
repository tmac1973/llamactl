package builder

// BuildProfile defines cmake flags for a build configuration.
type BuildProfile struct {
	Name       string            `json:"name"`
	Backend    string            `json:"backend"` // "rocm", "vulkan", "cpu"
	CMakeFlags map[string]string `json:"cmake_flags"`
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
				"GGML_HIP":         "ON",
				"AMDGPU_TARGETS":   gpuTargets,
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
