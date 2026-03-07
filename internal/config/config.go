package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ListenAddr string `yaml:"listen_addr"` // default ":3000"
	DataDir    string `yaml:"data_dir"`    // default "/data"
	LlamaPort  int    `yaml:"llama_port"`  // default 8080
	HFToken    string `yaml:"hf_token"`    // optional HuggingFace token
	APIKey     string `yaml:"api_key"`     // optional API key for /v1/* proxy
	LogLevel   string `yaml:"log_level"`   // default "info"
}

func Load(path string) (*Config, error) {
	cfg := &Config{
		ListenAddr: ":3000",
		DataDir:    "/data",
		LlamaPort:  8080,
		LogLevel:   "info",
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
