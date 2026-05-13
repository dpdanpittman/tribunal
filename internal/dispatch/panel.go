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
	// for high-stakes review. The methodology recommends three Claude
	// variants with different focus and temperature.
	DefaultPanel []PanelMember `yaml:"default_panel"`

	// HighStakesPanel is the cross-vendor panel dispatched when an
	// Assignment declares `Adversary mode: high-stakes`. Recommended:
	// Claude + OpenAI + Gemini + local for genuine vendor diversity.
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
//   - Default: three Claude variants with different focus + temperature.
//   - High-stakes: cross-vendor with one Claude + one OpenAI + one Gemini + one local.
//
// These are *recommendations*. The orchestrator dispatches only members
// whose providers are registered, so missing API keys quietly demote a
// member to INDETERMINATE rather than crash the panel.
func DefaultDispatchConfig() Config {
	return Config{
		DefaultPanel: []PanelMember{
			{Label: "claude-spec", Provider: "claude", Model: "claude-opus-4-7", Temperature: 0, Focus: "spec"},
			{Label: "claude-impl", Provider: "claude", Model: "claude-opus-4-7", Temperature: 0.7, Focus: "impl"},
			{Label: "claude-temporal", Provider: "claude", Model: "claude-sonnet-4-6", Temperature: 0, Focus: "temporal"},
		},
		HighStakesPanel: []PanelMember{
			{Label: "claude-spec", Provider: "claude", Model: "claude-opus-4-7", Temperature: 0, Focus: "spec"},
			{Label: "gpt-spec", Provider: "openai", Model: "gpt-5", Temperature: 0, Focus: "spec"},
			{Label: "gemini-spec", Provider: "google", Model: "gemini-2.5-pro", Temperature: 0, Focus: "spec"},
			{Label: "local-spec", Provider: "local", Model: "qwen-3-32b", Temperature: 0, Focus: "spec"},
		},
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
