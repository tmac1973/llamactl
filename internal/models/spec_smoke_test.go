package models

import (
	"strings"
	"testing"
)

func TestSpecMTPEffectiveFlags(t *testing.T) {
	cfg := &ModelConfig{
		GPULayers:   99,
		ContextSize: 8192,
		Threads:     8,
		SpecType:    "draft-mtp",
		DraftMax:    6,
	}
	got := cfg.EffectiveFlags()
	for _, want := range []string{"--spec-type draft-mtp", "--spec-draft-n-max 6"} {
		if !strings.Contains(got, want) {
			t.Errorf("MTP flags missing %q in: %s", want, got)
		}
	}
	if strings.Contains(got, "--model-draft") {
		t.Errorf("MTP should not emit --model-draft, got: %s", got)
	}
}

func TestSpecDraftResourceFlags(t *testing.T) {
	cfg := &ModelConfig{
		GPULayers:         99,
		ContextSize:       16384,
		Threads:           8,
		SpecType:          "draft",
		DraftModelPath:    "/models/qwen-0.5b.gguf",
		DraftMax:          16,
		DraftPMin:         "0.75",
		DraftCtxSize:      4096,
		DraftGPULayers:    99,
		DraftDevice:       "CUDA1",
		DraftCPUMoE:       2,
		DraftKVCacheQuant: "q8_0",
	}
	got := cfg.EffectiveFlags()
	for _, want := range []string{
		"--spec-type draft-simple",
		"--model-draft /models/qwen-0.5b.gguf",
		"--spec-draft-n-max 16",
		"--spec-draft-p-min 0.75",
		"--ctx-size-draft 4096",
		"--gpu-layers-draft 99",
		"--device-draft CUDA1",
		"--n-cpu-moe-draft 2",
		"--cache-type-k-draft q8_0",
		"--cache-type-v-draft q8_0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Draft flags missing %q in: %s", want, got)
		}
	}
}

func TestSpecMTPPresetINI(t *testing.T) {
	cfg := &ModelConfig{
		Enabled:     true,
		GPULayers:   99,
		ContextSize: 8192,
		Threads:     8,
		SpecType:    "draft-mtp",
		DraftMax:    6,
	}
	var b strings.Builder
	writeConfigParams(&b, cfg, false)
	out := b.String()
	for _, want := range []string{"spec-type = draft-mtp", "spec-draft-n-max = 6"} {
		if !strings.Contains(out, want) {
			t.Errorf("MTP preset missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "model-draft") {
		t.Errorf("MTP preset should not emit model-draft, got:\n%s", out)
	}
}
