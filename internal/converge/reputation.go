package converge

import "context"

// ReputationSink is the controller's hook for recording implementer
// outcomes into Tribunal's reputation ledger. Decouples the converge
// package from the agent registry + ledger persistence — the production
// implementation lives in cmd/tribunal (it needs the agent registry +
// keypair signing); the controller calls it after each implementer
// invocation when the sink is non-nil.
//
// v0.4.5 ships this as a local-ledger feedback layer: outcomes land in
// `.tribunal/ledger.jsonl` and flow on-chain via the existing
// `tribunal chain sync` path. Implementers that ship working patches
// accumulate reputation across plans; implementers that ship patches
// that fail verify lose stake. The on-chain leaderboard at
// tribunal.mabus.ai/leaderboard surfaces both classes.
type ReputationSink interface {
	// RecordImplementerOutcome is called by the controller after each
	// invokeImplementer call. The sink decides what to record (Finding,
	// Resolution, both, or neither) based on the outcome state.
	//
	// Errors are non-fatal — the controller continues the loop. The
	// outcome is recorded on the round via PatchError when the sink
	// returns an error.
	RecordImplementerOutcome(ctx context.Context, outcome ImplementerOutcome) error
}

// ImplementerOutcome is the structured view the controller hands the
// sink. Captures the input the implementer addressed, the output it
// produced, and (when M3 is enabled) the verify gate's verdict.
type ImplementerOutcome struct {
	// PlanID + Round scope the outcome.
	PlanID string
	Round  int

	// ImplementerLabel is the keypair label the implementer reports
	// via Implementer.Label(). Used by the sink to look up / register
	// the on-disk keypair.
	ImplementerLabel string

	// PatchHash is a stable sha256 hash of the patch text (or empty
	// for refused/no-patch outcomes). Becomes the claim_hash on the
	// synthetic Finding the sink writes.
	PatchHash string

	// Severities lists the severities of the findings the patch was
	// authored to address. Sink uses this to pick the synthetic
	// finding's severity (typically the highest).
	Severities []string

	// Refused: implementer declined to author a patch. No Finding
	// should be filed in this case — refusal is operator signal, not
	// a reputation event.
	Refused bool

	// Applied: AutoApply ran `git apply` on the patch (M2 / M3).
	Applied bool

	// VerifyRan + VerifyPassed: M3 verify-gate outcome. When VerifyRan
	// is true, the sink files an auto-Resolution with
	// outcome=TruePositive (VerifyPassed) or FalsePositive (!VerifyPassed).
	// When VerifyRan is false (M2 mode), only a Finding is filed and
	// settlement awaits manual operator action.
	VerifyRan     bool
	VerifyPassed  bool
	VerifySummary string

	// PatchError is populated when ApplyPatch failed (e.g., git apply
	// --check refused the patch). A non-empty PatchError without
	// Applied=true is treated the same as a failed verify — sink files
	// FalsePositive.
	PatchError string
}

// NeedsResolution reports whether the outcome has a definitive verdict
// the sink should record as a Resolution. True when verify ran OR the
// patch failed to apply (both terminal signals).
func (o *ImplementerOutcome) NeedsResolution() bool {
	if o.VerifyRan {
		return true
	}
	if o.PatchError != "" && !o.Applied {
		// Patch failed to apply at all — implementer's claim was wrong
		// before verify even got a chance to run.
		return true
	}
	return false
}

// IsTruePositive returns true when the outcome should settle as a
// reputation gain for the implementer. Currently: verify ran AND
// passed. Future M3.5 extension: PM/QA manual marking of un-verified
// patches.
func (o *ImplementerOutcome) IsTruePositive() bool {
	return o.VerifyRan && o.VerifyPassed
}
