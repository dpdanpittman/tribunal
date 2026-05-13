package verify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

// goLayer represents one runnable layer of the Go verification stack.
type goLayer struct {
	name    string
	tool    string
	enabled bool
	run     func(ctx context.Context, projectRoot string) (status LayerStatus, exit int, stdout, stderr, note string)
}

// goLayers returns the ordered Go pyramid for the given config.
// The order is cheapest-first.
func goLayers(cfg Config) []goLayer {
	g := cfg.Go
	layers := []goLayer{
		{
			name: "go-build", tool: "go", enabled: true,
			run: makeCmdRunner("go", "build", "./..."),
		},
		{
			name: "go-fmt", tool: "gofmt", enabled: true,
			run: runGofmt,
		},
		{
			name: "go-vet", tool: "go", enabled: true,
			run: makeCmdRunner("go", "vet", "./..."),
		},
		{
			name: "staticcheck", tool: "staticcheck", enabled: g.Staticcheck,
			run: makeCmdRunner("staticcheck", "./..."),
		},
		{
			name: "golangci-lint", tool: "golangci-lint", enabled: g.GolangciLint,
			run: makeCmdRunner("golangci-lint", "run"),
		},
		{
			name: "go-test", tool: "go", enabled: true,
			run: runGoTest(g),
		},
	}
	if g.FuzzPattern != "" && g.FuzzTime != "" {
		layers = append(layers, goLayer{
			name: "go-fuzz", tool: "go", enabled: true,
			run: makeCmdRunner("go", "test", "-fuzz="+g.FuzzPattern, "-fuzztime="+g.FuzzTime, "./..."),
		})
	}
	return layers
}

// makeCmdRunner builds a runner that invokes argv in projectRoot and maps
// the exit code to a LayerStatus.
func makeCmdRunner(argv ...string) func(ctx context.Context, projectRoot string) (LayerStatus, int, string, string, string) {
	return func(ctx context.Context, projectRoot string) (LayerStatus, int, string, string, string) {
		if _, err := exec.LookPath(argv[0]); err != nil {
			return StatusNotApplicable, 0, "", "", fmt.Sprintf("%s not found in PATH", argv[0])
		}
		stdout, stderr, code, runErr := runCommand(ctx, projectRoot, argv...)
		if runErr != nil && !errors.As(runErr, new(*exec.ExitError)) {
			return StatusFailed, code, stdout, stderr, "command error: " + runErr.Error()
		}
		if code == 0 {
			return StatusPassed, 0, stdout, stderr, ""
		}
		return StatusFailed, code, stdout, stderr, ""
	}
}

// runGofmt runs `gofmt -s -d .` and treats any non-empty stdout (a diff)
// as a failure.
func runGofmt(ctx context.Context, projectRoot string) (LayerStatus, int, string, string, string) {
	if _, err := exec.LookPath("gofmt"); err != nil {
		return StatusNotApplicable, 0, "", "", "gofmt not found in PATH"
	}
	stdout, stderr, code, runErr := runCommand(ctx, projectRoot, "gofmt", "-s", "-d", ".")
	if runErr != nil && !errors.As(runErr, new(*exec.ExitError)) {
		return StatusFailed, code, stdout, stderr, "command error: " + runErr.Error()
	}
	if code != 0 {
		return StatusFailed, code, stdout, stderr, ""
	}
	if len(bytes.TrimSpace([]byte(stdout))) > 0 {
		return StatusFailed, 0, stdout, stderr, "gofmt produced a diff (run `gofmt -s -w .`)"
	}
	return StatusPassed, 0, stdout, stderr, ""
}

// runGoTest builds the go test runner from config.
func runGoTest(g GoConfig) func(ctx context.Context, projectRoot string) (LayerStatus, int, string, string, string) {
	args := []string{"test"}
	if g.RaceValue() {
		args = append(args, "-race")
	}
	args = append(args, "-count="+strconv.Itoa(g.TestCountValue()))
	args = append(args, "./...")
	return makeCmdRunner(append([]string{"go"}, args...)...)
}

// runCommand executes argv in dir and captures stdout/stderr.
func runCommand(ctx context.Context, dir string, argv ...string) (stdout, stderr string, exit int, err error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err = cmd.Run()
	exit = cmd.ProcessState.ExitCode()
	return truncate(out.String(), 8000), truncate(errb.String(), 8000), exit, err
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... (truncated)"
}

// runGoStack runs all enabled Go layers in order, honoring halt-on-failure
// and exclusions. Returns the ordered LayerResults.
func runGoStack(ctx context.Context, projectRoot string, cfg Config) []LayerResult {
	results := make([]LayerResult, 0, 8)
	halt := cfg.HaltOnFailureValue()

	for _, l := range goLayers(cfg) {
		if !l.enabled {
			results = append(results, LayerResult{
				Layer:  l.name,
				Tool:   l.tool,
				Status: StatusSkipped,
				Note:   "layer disabled in tribunal.yaml",
			})
			continue
		}
		if cfg.IsExcluded(l.name) {
			results = append(results, LayerResult{
				Layer:  l.name,
				Tool:   l.tool,
				Status: StatusSkipped,
				Note:   "layer in exclude_layers",
			})
			continue
		}
		start := time.Now()
		status, code, stdout, stderr, note := l.run(ctx, projectRoot)
		results = append(results, LayerResult{
			Layer:    l.name,
			Tool:     l.tool,
			Status:   status,
			Duration: time.Since(start),
			Command:  toCommand(l),
			Stdout:   stdout,
			Stderr:   stderr,
			Note:     note,
			ExitCode: code,
		})
		if halt && status == StatusFailed {
			return results
		}
	}
	return results
}

// toCommand reconstructs a representative argv for the layer; purely
// informational, only used in reports.
func toCommand(l goLayer) []string {
	// We don't introspect the closure; instead we map by layer name to a
	// canonical argv. Keep these strings stable for stable report output.
	switch l.name {
	case "go-build":
		return []string{"go", "build", "./..."}
	case "go-fmt":
		return []string{"gofmt", "-s", "-d", "."}
	case "go-vet":
		return []string{"go", "vet", "./..."}
	case "staticcheck":
		return []string{"staticcheck", "./..."}
	case "golangci-lint":
		return []string{"golangci-lint", "run"}
	case "go-test":
		return []string{"go", "test", "-race", "-count=1", "./..."}
	case "go-fuzz":
		return []string{"go", "test", "-fuzz=...", "-fuzztime=...", "./..."}
	default:
		return nil
	}
}
