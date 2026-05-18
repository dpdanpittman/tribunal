// Package converge implements Tribunal's release-gating convergence
// controller. The single-pass methodology produces a verdict per cycle;
// convergence drives that cycle repeatedly until adversarial pressure
// stops finding things — the release ships at that fixed point, not
// before. See docs/convergence.md + docs/adr/0001-convergence-controller.md.
//
// v0.4.1 ships milestone M1 (output-only). The controller runs the
// adversary stage per round with a rotated panel composition, records
// findings to a round ledger, evaluates configured stopping criteria,
// and emits a structured summary. It does NOT author fixes — between
// rounds (when findings exist), the operator applies the implementer
// role manually and re-invokes. The round ledger preserves state across
// invocations so rotation can be computed against history.
package converge

import (
	"time"

	"github.com/dpdanpittman/tribunal/internal/dispatch"
)

// ConvergenceTarget is the input to Controller.Run.
type ConvergenceTarget struct {
	// PlanID identifies the plan under convergence; matches the on-chain
	// plan_id used at settlement and the .tribunal/plans/<id>/ directory.
	PlanID string

	// DiffSpec is the initial round's diff: git range, file path, or
	// "staged". Subsequent rounds re-read from the working tree.
	DiffSpec string

	// ProjectRoot is the absolute path the controller operates against
	// (CLI passes os.Getwd() result).
	ProjectRoot string
}

// PanelComposition is the snapshot of one round's adversary panel. The
// round ledger records this so future rotations can compute distance.
type PanelComposition struct {
	// Round is 1-indexed. Round 0 is reserved for "no prior round".
	Round int `json:"round"`

	// Members is the ordered set of panel members dispatched this round.
	Members []dispatch.PanelMember `json:"members"`

	// RotationAxis names the axis the rotator varied for this round
	// (e.g., "focus", "model_tier", "composite:focus,vendor_family").
	RotationAxis string `json:"rotation_axis,omitempty"`
}

// RoundResult captures one round's outcome: panel composition, parsed
// findings, verdicts, costs, stopping evaluation. Persisted per round at
// .tribunal/convergence/<plan-id>/round-<N>.json.
type RoundResult struct {
	Round       int              `json:"round"`
	StartedAt   time.Time        `json:"started_at"`
	CompletedAt time.Time        `json:"completed_at"`
	Panel       PanelComposition `json:"panel"`

	// Findings are the structured findings the adversary panel produced.
	// Severity strings: "critical" | "warning" | "suggestion" | "".
	Findings []RoundFinding `json:"findings"`

	// Verdicts is per-member-label → verdict ("BREAKS" | "SURVIVES" |
	// "INDETERMINATE"). Lets stopping criteria see who said what without
	// re-reading the full Synthesis.
	Verdicts map[string]string `json:"verdicts"`

	// OverallVerdict is the panel-level verdict computed by
	// dispatch.Synthesize.
	OverallVerdict string `json:"overall_verdict"`

	// TokenCost is the total token spend for this round, summed across
	// members. Best-effort; provider-specific. Zero when unknown.
	TokenCost int `json:"token_cost,omitempty"`

	// Stopped records whether a stopping criterion fired AFTER this round.
	Stopped       bool   `json:"stopped"`
	StopReason    string `json:"stop_reason,omitempty"`
	StopCriterion string `json:"stop_criterion,omitempty"`

	// Duration is the round's wallclock cost.
	Duration time.Duration `json:"duration_ns"`

	// Patch fields are populated by the M2 implementer flow when the
	// controller's Implementer is non-nil and the round produced
	// unresolved Critical/Warning findings.
	PatchAuthored bool     `json:"patch_authored,omitempty"`
	PatchPath     string   `json:"patch_path,omitempty"`
	PatchReadme   string   `json:"patch_readme,omitempty"`
	PatchRefused  bool     `json:"patch_refused,omitempty"`
	PatchApplied  bool     `json:"patch_applied,omitempty"`
	PatchFiles    []string `json:"patch_files,omitempty"`
	PatchTokens   int      `json:"patch_tokens,omitempty"`
	PatchError    string   `json:"patch_error,omitempty"`
}

// RoundFinding is the controller's view of one finding — a normalized
// subset of dispatch.ParsedFinding (and ledger.Finding when on-chain
// history is consulted). Carries enough state for stopping criteria to
// classify "novel" vs "carry-forward".
type RoundFinding struct {
	// ClaimHash is the stable identifier for finding-deduplication
	// across rounds. Matches the on-chain claim_hash format.
	ClaimHash string `json:"claim_hash"`
	Category  string `json:"category"`
	Severity  string `json:"severity"` // critical | warning | suggestion
	Member    string `json:"member"`   // member label that surfaced it
	Scenario  string `json:"scenario,omitempty"`

	// CarryForward is true when an earlier round in this plan's history
	// already surfaced the same claim_hash. Computed by the controller
	// at round-merge time; the adversary stage doesn't fill it.
	CarryForward bool `json:"carry_forward,omitempty"`
}

// ConvergenceStatus is the terminal status reported by Controller.Run.
type ConvergenceStatus string

const (
	// StatusConverged — a stopping criterion fired affirmatively.
	StatusConverged ConvergenceStatus = "converged"

	// StatusBudgetExhausted — the loop ran out of rounds, tokens, or
	// wallclock before convergence.
	StatusBudgetExhausted ConvergenceStatus = "budget_exhausted"

	// StatusNeedsFixes — the round produced findings the operator must
	// address before the next invocation. M1's natural pause point.
	StatusNeedsFixes ConvergenceStatus = "needs_fixes"

	// StatusErrored — the controller hit a non-recoverable error before
	// producing a meaningful round.
	StatusErrored ConvergenceStatus = "errored"
)

// ConvergenceResult is the structured summary Controller.Run returns.
type ConvergenceResult struct {
	PlanID      string            `json:"plan_id"`
	Status      ConvergenceStatus `json:"status"`
	Reason      string            `json:"reason,omitempty"`
	StartedAt   time.Time         `json:"started_at"`
	CompletedAt time.Time         `json:"completed_at"`
	Rounds      []RoundResult     `json:"rounds"`

	// TotalTokenCost is the sum across rounds.
	TotalTokenCost int `json:"total_token_cost,omitempty"`

	// TotalDuration is the sum across rounds.
	TotalDuration time.Duration `json:"total_duration_ns"`
}
