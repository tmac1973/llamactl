package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSalvageModelsDir verifies that a config with a non-existent
// models_dir falls back to <DataDir>/models. This salvages registries
// poisoned by the old env-var leak, where a host bind-mount source
// got persisted as the in-container models_dir.
func TestSalvageModelsDir(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "cfg.yaml")
	os.WriteFile(cfgPath, []byte(`
data_dir: "`+tmp+`"
models_dir: "/nonexistent/host/path/fake"
`), 0o644)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ModelsDir != "" {
		t.Fatalf("ModelsDir not salvaged: got %q, want \"\"", cfg.ModelsDir)
	}
	want := filepath.Join(tmp, "models")
	if cfg.ModelsPath() != want {
		t.Fatalf("ModelsPath() = %q, want %q", cfg.ModelsPath(), want)
	}
}

// TestKeepGoodModelsDir verifies a legitimately-set models_dir survives
// the salvage check.
func TestKeepGoodModelsDir(t *testing.T) {
	tmp := t.TempDir()
	custom := filepath.Join(tmp, "custom-models")
	if err := os.Mkdir(custom, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(tmp, "cfg.yaml")
	os.WriteFile(cfgPath, []byte(`
data_dir: "`+tmp+`"
models_dir: "`+custom+`"
`), 0o644)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ModelsDir != custom {
		t.Fatalf("ModelsDir clobbered: got %q, want %q", cfg.ModelsDir, custom)
	}
}
