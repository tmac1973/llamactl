package models

import (
	"strings"
	"testing"
)

// TestGeneratePresetINIUniqueSectionsPerQuant guards against the section
// collision that caused bug 2 — when multiple quants of the same repo
// shared a directory-derived section name, llama.cpp's INI parser
// silently merged them and 13/23 enabled models became invisible to the
// router. Each model must now own its own section.
func TestGeneratePresetINIUniqueSectionsPerQuant(t *testing.T) {
	mods := []*Model{
		{ID: "u--R-GGUF--R-Q4_0", ModelID: "u/R-GGUF", Filename: "R-Q4_0.gguf", Quant: "Q4_0", FilePath: "/data/models/u--R-GGUF/R-Q4_0.gguf"},
		{ID: "u--R-GGUF--R-Q8_0", ModelID: "u/R-GGUF", Filename: "R-Q8_0.gguf", Quant: "Q8_0", FilePath: "/data/models/u--R-GGUF/R-Q8_0.gguf"},
	}
	cfgs := map[string]*ModelConfig{
		"u--R-GGUF--R-Q4_0": {Enabled: true},
		"u--R-GGUF--R-Q8_0": {Enabled: true},
	}
	out := GeneratePresetINI("/data/models", mods, cfgs)

	if !strings.Contains(out, "[u--R-GGUF--R-Q4_0]") {
		t.Errorf("Q4_0 section missing from preset:\n%s", out)
	}
	if !strings.Contains(out, "[u--R-GGUF--R-Q8_0]") {
		t.Errorf("Q8_0 section missing from preset:\n%s", out)
	}
	// Two distinct quants in the same dir must NOT share a section header.
	if strings.Count(out, "[u--R-GGUF]\n") != 0 {
		t.Errorf("dirname-only section header would collide; got:\n%s", out)
	}
}

// TestGeneratePresetINIAliasesIncludeDirnameOnlyWhenUnique pins down the
// alias rule. When a dirname is shared, listing it as an alias on multiple
// sections makes the router refuse the second one — so we only include
// the dirname alias when it points to exactly one model.
func TestGeneratePresetINIAliasesIncludeDirnameOnlyWhenUnique(t *testing.T) {
	mods := []*Model{
		{ID: "u--R-GGUF--R-Q4_0", ModelID: "u/R-GGUF", Quant: "Q4_0", FilePath: "/data/models/u--R-GGUF/R-Q4_0.gguf"},
		{ID: "u--R-GGUF--R-Q8_0", ModelID: "u/R-GGUF", Quant: "Q8_0", FilePath: "/data/models/u--R-GGUF/R-Q8_0.gguf"},
		{ID: "u--S-GGUF--S-Q8_0", ModelID: "u/S-GGUF", Quant: "Q8_0", FilePath: "/data/models/u--S-GGUF/S-Q8_0.gguf"},
	}
	cfgs := map[string]*ModelConfig{
		"u--R-GGUF--R-Q4_0": {Enabled: true},
		"u--R-GGUF--R-Q8_0": {Enabled: true},
		"u--S-GGUF--S-Q8_0": {Enabled: true},
	}
	out := GeneratePresetINI("/data/models", mods, cfgs)

	// The R section's alias line must NOT contain the colliding "u--R-GGUF" dirname.
	for _, sectionID := range []string{"u--R-GGUF--R-Q4_0", "u--R-GGUF--R-Q8_0"} {
		body := sectionBody(out, sectionID)
		if strings.Contains(body, ",u--R-GGUF,") || strings.Contains(body, ",u--R-GGUF\n") {
			t.Errorf("section %s should not list the colliding dirname alias; body:\n%s", sectionID, body)
		}
	}

	// The S section's alias line SHOULD contain its unique dirname.
	body := sectionBody(out, "u--S-GGUF--S-Q8_0")
	if !strings.Contains(body, "u--S-GGUF") {
		t.Errorf("S section should list its unique dirname alias; body:\n%s", body)
	}
}

// sectionBody returns the lines of the given INI section, up to (but not
// including) the next section header.
func sectionBody(ini, section string) string {
	header := "[" + section + "]"
	idx := strings.Index(ini, header)
	if idx < 0 {
		return ""
	}
	rest := ini[idx+len(header):]
	if next := strings.Index(rest, "\n["); next >= 0 {
		return rest[:next]
	}
	return rest
}

// TestRegistryFindByAny verifies that registry ID, public name, and
// user-defined aliases all resolve to the same model — the contract that
// /api/models/{id}/* and /v1/* both depend on for bug 3.
func TestRegistryFindByAny(t *testing.T) {
	r := &Registry{
		dataDir:   "/tmp",
		modelsDir: "/tmp/models",
		data: registryData{
			Models: map[string]*Model{
				"u--R-GGUF--R-Q8_0": {
					ID: "u--R-GGUF--R-Q8_0", ModelID: "u/R-GGUF", Quant: "Q8_0",
				},
			},
			Configs: map[string]*ModelConfig{
				"u--R-GGUF--R-Q8_0": {Enabled: true, Aliases: []string{"my-fav"}},
			},
		},
	}

	// PublicName is "u-R.Q8_0" given ModelID "u/R-GGUF" + Quant "Q8_0".
	for _, name := range []string{"u--R-GGUF--R-Q8_0", "u-R.Q8_0", "my-fav"} {
		m, _ := r.FindByAny(name)
		if m == nil {
			t.Errorf("FindByAny(%q) returned nil; expected the R Q8_0 model", name)
			continue
		}
		if m.ID != "u--R-GGUF--R-Q8_0" {
			t.Errorf("FindByAny(%q) resolved to %s; expected u--R-GGUF--R-Q8_0", name, m.ID)
		}
	}

	if m, _ := r.FindByAny("nope"); m != nil {
		t.Errorf("FindByAny(\"nope\") should return nil; got %v", m)
	}
}
