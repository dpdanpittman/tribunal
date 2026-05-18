package converge

import (
	"context"
	"strings"

	"github.com/dpdanpittman/tribunal/internal/verify"
)

// VerifyGate runs a "does the project still build + pass tests" check
// after the implementer applies a patch. The controller uses this to
// decide whether to continue the loop (M3 auto-continue) or to revert
// and pause for the operator. Tests inject stubs; the production
// implementation routes through `internal/verify.Run`.
type VerifyGate interface {
	Verify(ctx context.Context, projectRoot string) (*VerifyResult, error)
}

// VerifyResult is the gate's verdict.
type VerifyResult struct {
	// Passed is true when every applicable layer of the verification
	// pyramid returned StatusPassed (or StatusSkipped / StatusNotApplicable
	// for layers the operator excluded).
	Passed bool

	// Summary is a short human-readable view: "go-build ok, go-test ok"
	// on pass, or the first failed layer's suggested action on fail.
	// Persisted to the round ledger.
	Summary string
}

// PyramidVerifyGate is the production VerifyGate. Adapts verify.Run
// from the existing internal/verify package — same layers that
// `tribunal verify` runs by default. v0.4.3 doesn't extend the pyramid
// itself; it just consults it post-patch.
type PyramidVerifyGate struct {
	// Stack is the verify-config stack name ("go" | "rust" | "ts").
	// Empty defaults to verify.LoadConfig's choice (which reads
	// tribunal.yaml or falls back to the Go stack).
	Stack string
}

func (g *PyramidVerifyGate) Verify(ctx context.Context, projectRoot string) (*VerifyResult, error) {
	cfg, err := verify.LoadConfig(projectRoot)
	if err != nil {
		return nil, err
	}
	if g.Stack != "" {
		cfg.Stack = g.Stack
	}
	report, err := verify.Run(ctx, projectRoot, cfg)
	if err != nil {
		return nil, err
	}
	return &VerifyResult{
		Passed:  report.OverallPassed,
		Summary: summarizeVerifyReport(report),
	}, nil
}

// summarizeVerifyReport produces a one-line view for the round ledger.
// On pass: comma-separated `<layer> ok`. On fail: `<layer> FAILED — <suggested action>`.
func summarizeVerifyReport(r *verify.PyramidReport) string {
	if r == nil {
		return ""
	}
	if r.OverallPassed {
		parts := make([]string, 0, len(r.Layers))
		for _, l := range r.Layers {
			if l.Status == verify.StatusPassed {
				parts = append(parts, l.Layer+" ok")
			}
		}
		return strings.Join(parts, ", ")
	}
	// On failure, report the first failed layer + suggested action.
	for _, l := range r.Layers {
		if l.Status == verify.StatusFailed {
			return l.Layer + " FAILED — " + r.SuggestedAction
		}
	}
	return r.SuggestedAction
}
