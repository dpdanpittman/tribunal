package verify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProject(t *testing.T, mainSrc, testSrc string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/proj\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(mainSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if testSrc != "" {
		if err := os.WriteFile(filepath.Join(dir, "main_test.go"), []byte(testSrc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if c.Stack != "go" {
		t.Fatalf("Stack = %q, want go", c.Stack)
	}
	if !c.HaltOnFailureValue() {
		t.Fatal("halt-on-failure should default true")
	}
	if !c.Go.RaceValue() {
		t.Fatal("race should default true")
	}
	if c.Go.TestCountValue() != 1 {
		t.Fatalf("test count default = %d, want 1", c.Go.TestCountValue())
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Stack != "go" {
		t.Fatalf("expected default 'go' stack on missing file, got %q", c.Stack)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	dir := t.TempDir()
	yaml := `
verify:
  stack: go
  halt_on_failure: false
  exclude_layers: [staticcheck]
  go:
    staticcheck: true
    golangci_lint: true
    race: false
    test_count: 3
    fuzz_pattern: FuzzFoo
    fuzz_time: 10s
`
	if err := os.WriteFile(filepath.Join(dir, "tribunal.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.HaltOnFailureValue() {
		t.Error("halt-on-failure should be false after override")
	}
	if !c.IsExcluded("staticcheck") {
		t.Error("staticcheck should be excluded")
	}
	if c.Go.RaceValue() {
		t.Error("race should be false after override")
	}
	if c.Go.TestCountValue() != 3 {
		t.Errorf("test count = %d, want 3", c.Go.TestCountValue())
	}
	if c.Go.FuzzPattern != "FuzzFoo" || c.Go.FuzzTime != "10s" {
		t.Errorf("fuzz fields not parsed: %+v", c.Go)
	}
}

const passingMain = `package main

func Add(a, b int) int { return a + b }

func main() { _ = Add(1, 2) }
`

const passingTest = `package main

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("Add wrong")
	}
}
`

func TestPyramidPasses(t *testing.T) {
	dir := writeProject(t, passingMain, passingTest)
	cfg := DefaultConfig()
	// Skip staticcheck / golangci by default (already off).
	report, err := Run(context.Background(), dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OverallPassed {
		for _, l := range report.Layers {
			t.Logf("layer %s status=%s note=%s\nstdout=%s\nstderr=%s", l.Layer, l.Status, l.Note, l.Stdout, l.Stderr)
		}
		t.Fatal("expected pyramid to pass")
	}
	passed, failed, _, _ := report.Counts()
	if passed == 0 || failed != 0 {
		t.Errorf("counts: passed=%d failed=%d", passed, failed)
	}
}

const failingFmt = `package main

func main() {
println("hi") // intentional non-canonical indentation
}
`

func TestPyramidCatchesFmt(t *testing.T) {
	dir := writeProject(t, failingFmt, "")
	cfg := DefaultConfig()
	report, err := Run(context.Background(), dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if report.OverallPassed {
		t.Fatal("expected pyramid to fail on bad fmt")
	}
	// Look for the failing layer name.
	var found bool
	for _, l := range report.Layers {
		if l.Layer == "go-fmt" && l.Status == StatusFailed {
			found = true
			if !strings.Contains(l.Note, "gofmt") && !strings.Contains(l.Stdout, "main.go") {
				t.Errorf("expected note or stdout to reference gofmt/main.go; got note=%q stdout=%q", l.Note, l.Stdout)
			}
		}
	}
	if !found {
		t.Fatal("expected go-fmt layer to fail")
	}
}

const failingTest = `package main

import "testing"

func TestBroken(t *testing.T) {
	t.Fatal("intentional")
}
`

func TestPyramidHaltsOnFailure(t *testing.T) {
	dir := writeProject(t, passingMain, failingTest)
	cfg := DefaultConfig()
	report, err := Run(context.Background(), dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if report.OverallPassed {
		t.Fatal("expected pyramid to fail")
	}
	// Halt-on-failure means after the failing layer (go-test), no later
	// layers should appear in the result.
	failingIdx := -1
	for i, l := range report.Layers {
		if l.Status == StatusFailed {
			failingIdx = i
			break
		}
	}
	if failingIdx == -1 {
		t.Fatal("expected a failing layer")
	}
	// All subsequent layers should be skipped (excluded) or absent.
	// runGoStack returns early on first failure, so the failing layer
	// should be the last entry.
	if failingIdx != len(report.Layers)-1 {
		t.Errorf("halt-on-failure should stop after first failure; got %d more layers", len(report.Layers)-1-failingIdx)
	}
}
