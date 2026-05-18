package dispatch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the dispatch-specific subset of tribunal.yaml, keyed under the
// top-level `adversary:` mapping.
type Config struct {
	// DefaultPanel is the panel dispatched when an Assignment doesn't ask
	// for high-stakes review. v0.4.0 pivot: three distinct Claude model
	// tiers (opus + sonnet + haiku) instead of three opus/sonnet variants.
	// The intra-Claude diversity panel is the empirical winner from
	// P-multi-adversary (H2 confirmed: intra-family disagreement is real;
	// F-OPUS-004 the most novel finding came from this configuration).
	DefaultPanel []PanelMember `yaml:"default_panel"`

	// HighStakesPanel adds a cross-family slot on top of the intra-Claude
	// trio. v0.4.0 reshape: intra-Claude is the load-bearing primitive
	// (the cheap empirical winner); the cross-family slot is the TIER-2
	// optimization for genuine multi-vendor signal. P-multi-adversary H1
	// REFUTED with a methodology caveat — high-stakes still earns its keep
	// for environments that have keys for >1 vendor.
	HighStakesPanel []PanelMember `yaml:"high_stakes_panel"`
}

// LoadConfig reads tribunal.yaml from projectRoot. A missing file is
// treated as the documented defaults (three Claude variants for the
// default panel; cross-vendor for high-stakes — both populated regardless
// of whether keys are configured, so the loader is deterministic).
func LoadConfig(projectRoot string) (Config, error) {
	cfg := DefaultDispatchConfig()
	path := filepath.Join(projectRoot, "tribunal.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	var wrapped struct {
		Adversary *Config `yaml:"adversary"`
	}
	if err := yaml.Unmarshal(raw, &wrapped); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if wrapped.Adversary == nil {
		return cfg, nil
	}
	if len(wrapped.Adversary.DefaultPanel) > 0 {
		cfg.DefaultPanel = wrapped.Adversary.DefaultPanel
	}
	if len(wrapped.Adversary.HighStakesPanel) > 0 {
		cfg.HighStakesPanel = wrapped.Adversary.HighStakesPanel
	}
	return cfg, nil
}

// DefaultDispatchConfig returns the methodology's recommended panels:
//
//   - Default: three Claude model tiers (opus + sonnet + haiku), each
//     with a distinct focus axis. v0.4.0 pivot — the previous default
//     was three opus/sonnet variants with identical model tiers; the
//     P-multi-adversary experiment showed that intra-tier disagreement
//     (opus vs sonnet vs haiku) is the cheap empirical winner.
//
//   - High-stakes: the intra-Claude trio plus one cross-family slot.
//     v0.4.0 reshape — the previous high-stakes panel was 4 distinct
//     vendors with no Claude redundancy. P-multi-adversary H1 was
//     refuted (provisionally); the cross-family slot stays for the
//     opt-in TIER-2 signal but intra-Claude is the load-bearing layer.
//
// These are *recommendations*. The orchestrator dispatches only members
// whose providers are registered, so missing API keys quietly demote a
// member to INDETERMINATE rather than crash the panel.
func DefaultDispatchConfig() Config {
	intraClaude := []PanelMember{
		{Label: "claude-opus-spec", Provider: "claude", Model: "claude-opus-4-7", Temperature: 0, Focus: "spec"},
		{Label: "claude-sonnet-impl", Provider: "claude", Model: "claude-sonnet-4-6", Temperature: 0.7, Focus: "impl"},
		{Label: "claude-haiku-temporal", Provider: "claude", Model: "claude-haiku-4-5-20251001", Temperature: 0, Focus: "temporal"},
	}
	return Config{
		DefaultPanel: intraClaude,
		HighStakesPanel: append(append([]PanelMember{}, intraClaude...),
			PanelMember{Label: "local-qwen-security", Provider: "local", Model: "qwen3:32b", Temperature: 0, Focus: "security"},
		),
	}
}

// Select returns the requested panel by name. Known names: "default",
// "high-stakes". Returns an error for unknown names.
func (c Config) Select(name string) (Panel, error) {
	switch name {
	case "", "default":
		return Panel{Name: "default", Members: c.DefaultPanel}, nil
	case "high-stakes", "high_stakes":
		return Panel{Name: "high-stakes", Members: c.HighStakesPanel}, nil
	default:
		return Panel{}, fmt.Errorf("dispatch: unknown panel %q (known: default, high-stakes)", name)
	}
}
