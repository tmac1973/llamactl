package config

import (
	"os"
	"path/filepath"
	"runtime"
)

const appName = "llama-toolchest"

// DefaultDataDir returns the platform-appropriate data directory for a host
// install. Containers override this via the YAML's data_dir field.
func DefaultDataDir() string {
	switch runtime.GOOS {
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", appName)
		}
	case "windows":
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			return filepath.Join(dir, appName)
		}
	default:
		if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
			return filepath.Join(dir, appName)
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "share", appName)
		}
	}
	return "/data"
}

// DefaultConfigPath returns the platform-appropriate config file path.
// Honors LLAMA_TOOLCHEST_CONFIG if set.
func DefaultConfigPath() string {
	if path := os.Getenv("LLAMA_TOOLCHEST_CONFIG"); path != "" {
		return path
	}
	switch runtime.GOOS {
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", appName, "llama-toolchest.yaml")
		}
	case "windows":
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			return filepath.Join(dir, appName, "llama-toolchest.yaml")
		}
	default:
		if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
			return filepath.Join(dir, appName, "llama-toolchest.yaml")
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".config", appName, "llama-toolchest.yaml")
		}
	}
	return "/data/config/llama-toolchest.yaml"
}
