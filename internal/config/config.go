package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ListenAddr  string `yaml:"listen_addr"`  // default ":3000"
	DataDir     string `yaml:"data_dir"`     // default "/data"
	ModelsDir   string `yaml:"models_dir"`   // optional override for the models dir; empty = <DataDir>/models
	LlamaPort   int    `yaml:"llama_port"`   // default 8080
	ExternalURL string `yaml:"external_url"` // e.g. "http://myserver:3000" for links
	HFToken     string `yaml:"hf_token"`     // optional HuggingFace token
	APIKey      string `yaml:"api_key"`      // optional API key for /v1/* proxy
	LogLevel    string `yaml:"log_level"`    // default "info"
	ActiveBuild string `yaml:"active_build"` // which llama.cpp build to use
	ModelsMax   int    `yaml:"models_max"`   // max loaded models, 0 = unlimited
	AutoStart   bool   `yaml:"auto_start"`   // start the llama-server on container startup
}

// ModelsPath returns the directory where GGUF models live. ModelsDir wins
// when set; otherwise it's <DataDir>/models. Pulling it into a single
// helper means downstream code doesn't have to repeat the fallback logic.
func (c *Config) ModelsPath() string {
	if c.ModelsDir != "" {
		return c.ModelsDir
	}
	return filepath.Join(c.DataDir, "models")
}

func Load(path string) (*Config, error) {
	cfg := &Config{
		ListenAddr:  ":3000",
		DataDir:     DefaultDataDir(),
		LlamaPort:   8080,
		ExternalURL: "http://localhost:3000",
		LogLevel:    "info",
		ModelsMax:   1,
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		// no file → use defaults, fall through to validation
	} else if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	salvageModelsDir(cfg)
	return cfg, nil
}

// salvageModelsDir blanks ModelsDir if it points to a path that doesn't exist
// or isn't a directory. This catches a previously-shipped bug where the host
// bind-mount source LLAMA_TOOLCHEST_MODELS_DIR leaked from .env into the
// container's environment and was misinterpreted as the in-container path,
// causing downloads to land in the container's writable layer (lost on
// rebuild). With the env-override path removed, runtime models_dir comes
// only from YAML — but registries written under the broken behavior may
// still hold a host-shaped models_dir value, so we drop it here at load
// time and fall back to <DataDir>/models, which is the bind-mount target.
func salvageModelsDir(cfg *Config) {
	if cfg.ModelsDir == "" {
		return
	}
	info, err := os.Stat(cfg.ModelsDir)
	if err == nil && info.IsDir() {
		return
	}
	reason := "does not exist"
	if err == nil {
		reason = "is not a directory"
	} else if !os.IsNotExist(err) {
		reason = fmt.Sprintf("stat failed: %v", err)
	}
	fallback := filepath.Join(cfg.DataDir, "models")
	slog.Warn("configured models_dir is unusable; falling back to default",
		"models_dir", cfg.ModelsDir, "reason", reason, "fallback", fallback,
		"hint", "save the Settings page to clear the bad value from the config file")
	cfg.ModelsDir = ""
}
