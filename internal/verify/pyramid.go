package verify

import (
	"context"
	"fmt"
	"time"
)

// Run executes the configured verification pyramid against the project at
// projectRoot. The caller may pass a context with a deadline; the
// orchestrator does not enforce one of its own.
func Run(ctx context.Context, projectRoot string, cfg Config) (*PyramidReport, error) {
	report := &PyramidReport{
		ProjectRoot:    projectRoot,
		Started:        time.Now(),
		HaltOnFailure:  cfg.HaltOnFailureValue(),
		LayersExcluded: cfg.ExcludeLayers,
	}

	switch cfg.Stack {
	case "", "go":
		report.Layers = runGoStack(ctx, projectRoot, cfg)
	case "rust":
		// v0.2 stub — Rust stack lands when a CosmWasm contract sample is
		// added under contracts/. For now, report not_applicable.
		report.Layers = []LayerResult{{
			Layer:  "rust-stack",
			Tool:   "cargo",
			Status: StatusNotApplicable,
			Note:   "Rust stack not yet wired (v0.2 stub)",
		}}
	case "ts":
		report.Layers = []LayerResult{{
			Layer:  "ts-stack",
			Tool:   "node",
			Status: StatusNotApplicable,
			Note:   "TypeScript stack not yet wired (v0.2 stub)",
		}}
	default:
		return nil, fmt.Errorf("verify: unknown stack %q (supported: go, rust, ts)", cfg.Stack)
	}

	report.Completed = time.Now()
	report.OverallPassed = !anyFailed(report.Layers)
	report.SuggestedAction = suggestedAction(report.Layers)
	return report, nil
}

func anyFailed(layers []LayerResult) bool {
	for _, l := range layers {
		if l.Status == StatusFailed {
			return true
		}
	}
	return false
}

func suggestedAction(layers []LayerResult) string {
	for _, l := range layers {
		if l.Status != StatusFailed {
			continue
		}
		switch l.Layer {
		case "go-build":
			return "Layer go-build failed — fix compilation errors before re-running the pyramid."
		case "go-fmt":
			return "Layer go-fmt failed — run `gofmt -s -w .` to apply formatting."
		case "go-vet":
			return "Layer go-vet failed — address the reported issues; cheapest correctness check."
		case "staticcheck", "golangci-lint":
			return fmt.Sprintf("Layer %s failed — address the reported issues or downgrade the tool in tribunal.yaml.", l.Layer)
		case "go-test":
			return "Layer go-test failed — route the failure to `tribunal-classifier` to decide spec/code/prover/tool/state/infra."
		case "go-fuzz":
			return "Layer go-fuzz failed — a fuzz seed crashed; route the counterexample to `tribunal-classifier`."
		}
		return fmt.Sprintf("Layer %s failed — see report for details.", l.Layer)
	}
	return "All applicable layers passed. Proceed to merge."
}
