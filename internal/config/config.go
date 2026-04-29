package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ListenAddr  string `yaml:"listen_addr"`  // default ":3000"
	DataDir     string `yaml:"data_dir"`     // default "/data"
	LlamaPort   int    `yaml:"llama_port"`   // default 8080
	ExternalURL string `yaml:"external_url"` // e.g. "http://myserver:3000" for links
	HFToken     string `yaml:"hf_token"`     // optional HuggingFace token
	APIKey      string `yaml:"api_key"`      // optional API key for /v1/* proxy
	LogLevel    string `yaml:"log_level"`    // default "info"
	ActiveBuild string `yaml:"active_build"` // which llama.cpp build to use
	ModelsMax   int    `yaml:"models_max"`   // max loaded models, 0 = unlimited
	AutoStart   bool   `yaml:"auto_start"`   // start the llama-server on container startup
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
			return cfg, nil // use defaults
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
