package config

import (
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
		if os.IsNotExist(err) {
			applyEnvOverrides(cfg)
			return cfg, nil // use defaults
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	applyEnvOverrides(cfg)
	return cfg, nil
}

// applyEnvOverrides lets specific env vars fill in fields the YAML left
// blank. Intentionally narrow — most config lives in YAML; the env path
// exists for container deployments where setup.sh-managed env vars feed
// in a host bind-mount path that the YAML doesn't know about.
func applyEnvOverrides(cfg *Config) {
	if cfg.ModelsDir == "" {
		if v := os.Getenv("LLAMA_TOOLCHEST_MODELS_DIR"); v != "" {
			cfg.ModelsDir = v
		}
	}
}
