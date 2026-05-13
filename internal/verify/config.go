package verify

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the subset of `tribunal.yaml` that the verify package reads.
// Project authors declare which verification stack applies and which
// layers should run.
type Config struct {
	// Stack is one of "go", "rust", "ts", "custom". Defaults to "go" if
	// unset (the v0.1 reference stack).
	Stack string `yaml:"stack"`

	// Go holds Go-flavored options. Only consulted when Stack == "go".
	Go GoConfig `yaml:"go"`

	// HaltOnFailure causes the pyramid orchestrator to stop at the first
	// failing layer. Defaults to true.
	HaltOnFailure *bool `yaml:"halt_on_failure"`

	// ExcludeLayers lists layer names that should be skipped regardless of
	// availability. Useful when an optional tool is intentionally not in
	// scope for a project.
	ExcludeLayers []string `yaml:"exclude_layers"`
}

// GoConfig holds Go-stack tuning.
type GoConfig struct {
	// Race enables `-race` on `go test`. Defaults to true.
	Race *bool `yaml:"race"`
	// TestCount is the value passed to `go test -count=N`. Defaults to 1.
	TestCount int `yaml:"test_count"`
	// Staticcheck enables the `staticcheck` layer. Defaults to false
	// because the tool may not be installed; flip to true when the project
	// commits to it.
	Staticcheck bool `yaml:"staticcheck"`
	// GolangciLint enables the `golangci-lint` layer. Same default rationale.
	GolangciLint bool `yaml:"golangci_lint"`
	// FuzzTime, when non-empty, runs `go test -fuzz=<pattern> -fuzztime=<d>`
	// against the package set. Empty string means skip the fuzz layer.
	FuzzPattern string `yaml:"fuzz_pattern"`
	FuzzTime    string `yaml:"fuzz_time"`
}

// DefaultConfig returns Tribunal's documented defaults: Go stack, halt on
// failure, race detector on, single test run, optional tools off.
func DefaultConfig() Config {
	t := true
	return Config{
		Stack: "go",
		Go: GoConfig{
			Race:      &t,
			TestCount: 1,
		},
		HaltOnFailure: &t,
	}
}

// LoadConfig reads `tribunal.yaml` from the given project root and merges
// it on top of DefaultConfig(). A missing file is not an error — the
// defaults apply.
func LoadConfig(projectRoot string) (Config, error) {
	cfg := DefaultConfig()
	path := filepath.Join(projectRoot, "tribunal.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	// We unmarshal into a wrapper so users can put the verify config under
	// a top-level `verify:` key. We accept both top-level and nested.
	var wrapped struct {
		Verify *Config `yaml:"verify"`
	}
	if err := yaml.Unmarshal(raw, &wrapped); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if wrapped.Verify != nil {
		mergeConfig(&cfg, *wrapped.Verify)
		return cfg, nil
	}
	// Fallback: try unwrapped at top level.
	var direct Config
	if err := yaml.Unmarshal(raw, &direct); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	mergeConfig(&cfg, direct)
	return cfg, nil
}

// mergeConfig overlays src onto dst. Zero values in src don't override
// non-zero values in dst (so partial configs work as expected).
func mergeConfig(dst *Config, src Config) {
	if src.Stack != "" {
		dst.Stack = src.Stack
	}
	if src.HaltOnFailure != nil {
		dst.HaltOnFailure = src.HaltOnFailure
	}
	if len(src.ExcludeLayers) > 0 {
		dst.ExcludeLayers = src.ExcludeLayers
	}
	if src.Go.Race != nil {
		dst.Go.Race = src.Go.Race
	}
	if src.Go.TestCount != 0 {
		dst.Go.TestCount = src.Go.TestCount
	}
	// Booleans we treat as set when explicitly true; we don't introspect
	// "explicitly false" because yaml.v3 returns zero. If a project really
	// wants to disable, they can omit the key (zero value already off).
	if src.Go.Staticcheck {
		dst.Go.Staticcheck = true
	}
	if src.Go.GolangciLint {
		dst.Go.GolangciLint = true
	}
	if src.Go.FuzzPattern != "" {
		dst.Go.FuzzPattern = src.Go.FuzzPattern
	}
	if src.Go.FuzzTime != "" {
		dst.Go.FuzzTime = src.Go.FuzzTime
	}
}

// HaltOnFailureValue returns the effective halt-on-failure setting.
func (c *Config) HaltOnFailureValue() bool {
	if c.HaltOnFailure == nil {
		return true
	}
	return *c.HaltOnFailure
}

// RaceValue returns the effective race-detector setting.
func (g *GoConfig) RaceValue() bool {
	if g.Race == nil {
		return true
	}
	return *g.Race
}

// TestCountValue returns the effective test count.
func (g *GoConfig) TestCountValue() int {
	if g.TestCount <= 0 {
		return 1
	}
	return g.TestCount
}

// IsExcluded reports whether the given layer name appears in
// ExcludeLayers.
func (c *Config) IsExcluded(layer string) bool {
	for _, e := range c.ExcludeLayers {
		if e == layer {
			return true
		}
	}
	return false
}
