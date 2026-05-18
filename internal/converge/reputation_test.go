package converge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dpdanpittman/tribunal/internal/dispatch"
)

// stubReputationSink captures every outcome it receives. Lets the
// converge tests assert exactly what the controller hands the sink at
// each exit path.
type stubReputationSink struct {
	got []ImplementerOutcome
	err error
}

func (s *stubReputationSink) RecordImplementerOutcome(_ context.Context, o ImplementerOutcome) error {
	s.got = append(s.got, o)
	return s.err
}

// TestReputation_M2NoVerifyEmitsFindingOnly — implementer authors a
// patch but no verify ran (M2 mode); the sink sees an outcome with
// VerifyRan=false and NeedsResolution()=false.
func TestReputation_M2NoVerifyEmitsFindingOnly(t *testing.T) {
	stage := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "BREAKS", Findings: []RoundFinding{
				{ClaimHash: "h1", Severity: "critical", Member: "claude-opus-spec"},
			}, Verdicts: map[string]string{}},
		},
	}
	impl := &stubImplementer{queue: []PatchOutput{{
		Patch:     "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@\n-a\n+b\n",
		Reasoning: "bump",
	}}}
	sink := &stubReputationSink{}
	c, target := newTestController(t, stage)
	c.Implementer = impl
	c.AutoApply = false
	c.Reputation = sink

	if _, err := c.Run(context.Background(), target); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(sink.got) != 1 {
		t.Fatalf("sink got %d outcomes, want 1", len(sink.got))
	}
	o := sink.got[0]
	if o.VerifyRan {
		t.Fatalf("M2 outcome should have VerifyRan=false")
	}
	if o.NeedsResolution() {
		t.Fatalf("M2 outcome should NOT need a resolution (no verify; awaiting operator)")
	}
	if o.IsTruePositive() {
		t.Fatalf("M2 outcome IsTruePositive=true is invalid (verify didn't run)")
	}
	if o.Round != 1 || o.PlanID != target.PlanID {
		t.Fatalf("outcome scope drift: %+v", o)
	}
	if len(o.Severities) == 0 || o.Severities[0] != "critical" {
		t.Fatalf("severities not captured: %+v", o.Severities)
	}
}

// TestReputation_M3VerifyPassEmitsTruePositive — verify ran and passed;
// the outcome's IsTruePositive() returns true. Sink sees one outcome
// per round; loop continues to next clean rounds and converges.
func TestReputation_M3VerifyPassEmitsTruePositive(t *testing.T) {
	stage := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "BREAKS", Findings: []RoundFinding{
				{ClaimHash: "h1", Severity: "critical", Member: "x"},
			}, Verdicts: map[string]string{}},
			{OverallVerdict: "SURVIVES", Verdicts: map[string]string{}},
			{OverallVerdict: "SURVIVES", Verdicts: map[string]string{}},
		},
	}
	impl := &stubImplementer{queue: []PatchOutput{{Patch: validPatch()}}}
	gate := &stubVerifyGate{queue: []VerifyResult{{Passed: true, Summary: "ok"}}}
	sink := &stubReputationSink{}

	c, target := newM3Controller(t, stage, impl, gate)
	c.Reputation = sink

	res, err := c.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != StatusConverged {
		t.Fatalf("status=%s want converged", res.Status)
	}
	// Only round 1 invoked the implementer; rounds 2-3 were clean.
	if len(sink.got) != 1 {
		t.Fatalf("sink got %d outcomes, want 1", len(sink.got))
	}
	o := sink.got[0]
	if !o.IsTruePositive() {
		t.Fatalf("expected TruePositive (verify passed): %+v", o)
	}
	if !o.NeedsResolution() {
		t.Fatalf("verify-ran outcome must need a resolution")
	}
}

// TestReputation_M3VerifyFailEmitsFalsePositive — verify ran and failed;
// outcome IsTruePositive() = false, NeedsResolution() = true.
func TestReputation_M3VerifyFailEmitsFalsePositive(t *testing.T) {
	stage := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "BREAKS", Findings: []RoundFinding{
				{ClaimHash: "h1", Severity: "critical", Member: "x"},
			}, Verdicts: map[string]string{}},
		},
	}
	impl := &stubImplementer{queue: []PatchOutput{{Patch: validPatch()}}}
	gate := &stubVerifyGate{queue: []VerifyResult{{Passed: false, Summary: "broken"}}}
	sink := &stubReputationSink{}

	c, target := newM3Controller(t, stage, impl, gate)
	c.Reputation = sink

	if _, err := c.Run(context.Background(), target); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(sink.got) != 1 {
		t.Fatalf("sink got %d outcomes", len(sink.got))
	}
	o := sink.got[0]
	if o.IsTruePositive() {
		t.Fatalf("FalsePositive expected (verify failed): %+v", o)
	}
	if !o.NeedsResolution() {
		t.Fatalf("failed verify must need a resolution")
	}
	if !strings.Contains(o.VerifySummary, "broken") {
		t.Fatalf("verify summary not forwarded: %q", o.VerifySummary)
	}
}

// TestReputation_RefusalEmitsNothing — implementer Refused; no outcome
// recorded (refusal is operator signal, not a reputation event).
func TestReputation_RefusalEmitsNothing(t *testing.T) {
	stage := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "BREAKS", Findings: []RoundFinding{
				{ClaimHash: "h1", Severity: "critical", Member: "x"},
			}, Verdicts: map[string]string{}},
		},
	}
	impl := &stubImplementer{queue: []PatchOutput{{Refused: true, Reasoning: "needs ADR"}}}
	sink := &stubReputationSink{}

	c, target := newTestController(t, stage)
	c.Implementer = impl
	c.Reputation = sink

	if _, err := c.Run(context.Background(), target); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(sink.got) != 0 {
		t.Fatalf("refusal should emit nothing, got %+v", sink.got)
	}
}

// TestReputation_SinkErrorRecordedNotFatal — when the sink errors, the
// controller still completes the loop; the error lands on the round's
// PatchError so the operator sees it.
func TestReputation_SinkErrorRecordedNotFatal(t *testing.T) {
	stage := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "BREAKS", Findings: []RoundFinding{
				{ClaimHash: "h1", Severity: "critical", Member: "x"},
			}, Verdicts: map[string]string{}},
		},
	}
	impl := &stubImplementer{queue: []PatchOutput{{Patch: "diff --git a/x b/x"}}}
	sink := &stubReputationSink{err: errors.New("disk full")}

	c, target := newTestController(t, stage)
	c.Implementer = impl
	c.Reputation = sink

	res, err := c.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != StatusNeedsFixes {
		t.Fatalf("status=%s want needs_fixes", res.Status)
	}
	if len(res.Rounds) != 1 {
		t.Fatalf("rounds=%d want 1", len(res.Rounds))
	}
	pe := res.Rounds[0].PatchError
	if !strings.Contains(pe, "reputation sink") || !strings.Contains(pe, "disk full") {
		t.Fatalf("round PatchError should record sink failure: %q", pe)
	}
}

// Silence unused-import warnings when this file's helpers aren't all
// exercised by every test run (rare but possible during partial -run).
var (
	_ = time.Second
	_ = dispatch.DefaultDispatchConfig
)
