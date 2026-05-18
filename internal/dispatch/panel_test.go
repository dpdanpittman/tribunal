package dispatch

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultDispatchConfigShape pins the v0.4.0 intra-Claude diversity
// panel: default = three distinct Claude tiers (opus + sonnet + haiku);
// high-stakes = same trio + one cross-family slot. P-multi-adversary's H2
// confirmation (intra-family disagreement is real) drove this reshape.
func TestDefaultDispatchConfigShape(t *testing.T) {
	cfg := DefaultDispatchConfig()
	if len(cfg.DefaultPanel) != 3 {
		t.Errorf("default panel = %d members, want 3", len(cfg.DefaultPanel))
	}
	if len(cfg.HighStakesPanel) != 4 {
		t.Errorf("high-stakes panel = %d members, want 4 (intra-Claude trio + 1 cross-family slot)", len(cfg.HighStakesPanel))
	}
	// Default panel is all Claude.
	for i, m := range cfg.DefaultPanel {
		if m.Provider != "claude" {
			t.Errorf("default[%d].Provider = %q, want claude", i, m.Provider)
		}
	}
	// v0.4.0: default panel must span three distinct Claude model tiers
	// (opus, sonnet, haiku) — that's the load-bearing diversity primitive.
	tiers := map[string]bool{}
	for _, m := range cfg.DefaultPanel {
		tiers[BucketByModelTier(m)] = true
	}
	wantTiers := map[string]bool{"opus": true, "sonnet": true, "haiku": true}
	if len(tiers) != 3 {
		t.Errorf("default panel spans %d model tiers, want 3: %v", len(tiers), tiers)
	}
	for tier := range wantTiers {
		if !tiers[tier] {
			t.Errorf("default panel missing model tier %q (have %v)", tier, tiers)
		}
	}
	// v0.4.0: high-stakes panel must include the intra-Claude trio as its
	// load-bearing layer plus at least one non-Claude slot for the cross-
	// family TIER-2 signal.
	highTiers := map[string]bool{}
	families := map[string]bool{}
	for _, m := range cfg.HighStakesPanel {
		highTiers[BucketByModelTier(m)] = true
		families[BucketByVendorFamily(m)] = true
	}
	for tier := range wantTiers {
		if !highTiers[tier] {
			t.Errorf("high-stakes panel missing intra-Claude tier %q (have %v)", tier, highTiers)
		}
	}
	if len(families) < 2 {
		t.Errorf("high-stakes panel must span ≥2 vendor families (have %v)", families)
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
