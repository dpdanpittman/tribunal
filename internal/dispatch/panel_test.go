package dispatch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultDispatchConfigShape(t *testing.T) {
	cfg := DefaultDispatchConfig()
	if len(cfg.DefaultPanel) != 3 {
		t.Errorf("default panel = %d members, want 3", len(cfg.DefaultPanel))
	}
	if len(cfg.HighStakesPanel) != 4 {
		t.Errorf("high-stakes panel = %d members, want 4", len(cfg.HighStakesPanel))
	}
	// Default panel is all Claude.
	for i, m := range cfg.DefaultPanel {
		if m.Provider != "claude" {
			t.Errorf("default[%d].Provider = %q, want claude", i, m.Provider)
		}
	}
	// High-stakes spans 4 distinct vendor families.
	families := map[string]bool{}
	for _, m := range cfg.HighStakesPanel {
		families[BucketByVendorFamily(m)] = true
	}
	if len(families) != 4 {
		t.Errorf("high-stakes spans %d families, want 4", len(families))
	}
}

func TestLoadConfigMissingFileUsesDefaults(t *testing.T) {
	cfg, err := LoadConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.DefaultPanel) == 0 || len(cfg.HighStakesPanel) == 0 {
		t.Fatalf("expected defaults populated: %+v", cfg)
	}
}

func TestLoadConfigOverridesDefaultPanel(t *testing.T) {
	dir := t.TempDir()
	yaml := `
adversary:
  default_panel:
    - { label: only-one, provider: claude, model: claude-opus-4-7, temperature: 0, focus: spec }
`
	if err := os.WriteFile(filepath.Join(dir, "tribunal.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.DefaultPanel) != 1 || cfg.DefaultPanel[0].Label != "only-one" {
		t.Fatalf("override not applied: %+v", cfg.DefaultPanel)
	}
	// High-stakes still default.
	if len(cfg.HighStakesPanel) != 4 {
		t.Errorf("high-stakes panel should remain default when not overridden: %d", len(cfg.HighStakesPanel))
	}
}

func TestSelectKnownAndUnknownPanels(t *testing.T) {
	cfg := DefaultDispatchConfig()
	p, err := cfg.Select("default")
	if err != nil || p.Name != "default" {
		t.Errorf("default lookup failed: %v %v", p, err)
	}
	p, err = cfg.Select("high-stakes")
	if err != nil || p.Name != "high-stakes" {
		t.Errorf("high-stakes lookup failed: %v %v", p, err)
	}
	if _, err := cfg.Select("ultra-high-stakes"); err == nil {
		t.Errorf("expected error for unknown panel")
	}
}
