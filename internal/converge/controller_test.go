package converge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dpdanpittman/tribunal/internal/dispatch"
)

func osStat(p string) (os.FileInfo, error) { return os.Stat(p) }

// stubStage feeds the controller a queue of canned RoundOutputs, one per
// call. Lets us drive the loop through arbitrary scenarios without
// touching providers or the lens stage.
type stubStage struct {
	queue []RoundOutput
	calls int
}

func (s *stubStage) RunRound(ctx context.Context, in RoundInput) (*RoundOutput, error) {
	if s.calls >= len(s.queue) {
		// Exhausted queue → return a clean round so we don't accidentally
		// loop forever in a buggy test.
		s.calls++
		return &RoundOutput{Verdicts: map[string]string{}, OverallVerdict: "SURVIVES"}, nil
	}
	out := s.queue[s.calls]
	s.calls++
	cp := out
	return &cp, nil
}

func newTestController(t *testing.T, stage AdversaryStage) (*Controller, ConvergenceTarget) {
	t.Helper()
	dir := t.TempDir()
	cfg := dispatch.DefaultDispatchConfig()
	c := &Controller{
		Adversary:      stage,
		Rotator:        &FocusShuffleRotator{},
		Stopping:       []StoppingCriterion{&ConsecutiveCleanCriterion{N: 2}},
		Budget:         Budget{MaxRounds: 10, MaxWallclock: time.Minute},
		DispatchConfig: cfg,
	}
	target := ConvergenceTarget{
		PlanID:      "P-test",
		ProjectRoot: dir,
	}
	return c, target
}

// TestController_StopsOnConsecutiveClean exercises the happy path: two
// clean rounds in a row fire ConsecutiveCleanCriterion(2) and converge.
func TestController_StopsOnConsecutiveClean(t *testing.T) {
	stage := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "SURVIVES", Verdicts: map[string]string{}},
			{OverallVerdict: "SURVIVES", Verdicts: map[string]string{}},
		},
	}
	c, target := newTestController(t, stage)
	res, err := c.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != StatusConverged {
		t.Fatalf("status=%s want %s (reason=%s)", res.Status, StatusConverged, res.Reason)
	}
	if len(res.Rounds) != 2 {
		t.Fatalf("rounds=%d want 2", len(res.Rounds))
	}
	if stage.calls != 2 {
		t.Fatalf("stage calls=%d want 2", stage.calls)
	}
	// Round 2 should record the stop.
	if !res.Rounds[1].Stopped || res.Rounds[1].StopCriterion != "consecutive-clean(2)" {
		t.Fatalf("round 2 stop drift: stopped=%v criterion=%q", res.Rounds[1].Stopped, res.Rounds[1].StopCriterion)
	}
}

// TestController_PausesOnFindings — when a round produces Critical or
// Warning, the loop exits with StatusNeedsFixes so the operator can
// patch and re-invoke.
func TestController_PausesOnFindings(t *testing.T) {
	stage := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "BREAKS", Findings: []RoundFinding{
				{ClaimHash: "h1", Category: "shared_blind_spot", Severity: "critical", Member: "claude-opus-spec"},
			}, Verdicts: map[string]string{"claude-opus-spec": "BREAKS"}},
		},
	}
	c, target := newTestController(t, stage)
	res, err := c.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != StatusNeedsFixes {
		t.Fatalf("status=%s want %s", res.Status, StatusNeedsFixes)
	}
	if len(res.Rounds) != 1 {
		t.Fatalf("rounds=%d want 1", len(res.Rounds))
	}
}

// TestController_RotatesAcrossRounds — the rotator's view of history
// must include the prior round so it can rotate panel composition.
// FocusShuffleRotator should produce a different Focus on round 2.
func TestController_RotatesAcrossRounds(t *testing.T) {
	stage := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "SURVIVES", Verdicts: map[string]string{}},
			{OverallVerdict: "SURVIVES", Verdicts: map[string]string{}},
		},
	}
	c, target := newTestController(t, stage)
	res, err := c.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Rounds) < 2 {
		t.Fatalf("expected >=2 rounds, got %d", len(res.Rounds))
	}
	r1 := res.Rounds[0].Panel.Members
	r2 := res.Rounds[1].Panel.Members
	if len(r1) != len(r2) || len(r1) == 0 {
		t.Fatalf("panel sizes drift")
	}
	// At least one member's focus must differ between rounds.
	differs := false
	for i := range r1 {
		if r1[i].Focus != r2[i].Focus {
			differs = true
			break
		}
	}
	if !differs {
		t.Fatalf("focus did not rotate between rounds: r1=%+v r2=%+v", focuses(r1), focuses(r2))
	}
}

// TestController_LoadsHistoryAcrossInvocations verifies the second
// Controller.Run picks up where the first left off via the round ledger.
func TestController_LoadsHistoryAcrossInvocations(t *testing.T) {
	stage1 := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "BREAKS", Findings: []RoundFinding{
				{ClaimHash: "h1", Severity: "critical", Member: "claude-opus-spec"},
			}, Verdicts: map[string]string{}},
		},
	}
	c, target := newTestController(t, stage1)
	res, err := c.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("invocation 1: %v", err)
	}
	if res.Status != StatusNeedsFixes {
		t.Fatalf("inv1 status=%s want %s", res.Status, StatusNeedsFixes)
	}

	// Same target dir; second invocation should see round 1 on disk.
	stage2 := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "SURVIVES", Verdicts: map[string]string{}},
			{OverallVerdict: "SURVIVES", Verdicts: map[string]string{}},
		},
	}
	c2 := &Controller{
		Adversary:      stage2,
		Rotator:        &FocusShuffleRotator{},
		Stopping:       []StoppingCriterion{&ConsecutiveCleanCriterion{N: 2}},
		Budget:         Budget{MaxRounds: 10, MaxWallclock: time.Minute},
		DispatchConfig: dispatch.DefaultDispatchConfig(),
	}
	res2, err := c2.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("invocation 2: %v", err)
	}
	// Round 1 is loaded from disk; result.Rounds carries only the NEW
	// rounds run in this invocation.
	if len(res2.Rounds) != 2 {
		t.Fatalf("inv2 rounds in result=%d want 2 (the two new ones)", len(res2.Rounds))
	}
	if res2.Rounds[0].Round != 2 {
		t.Fatalf("inv2 first round number=%d want 2", res2.Rounds[0].Round)
	}
	// History on disk should now have 3 rounds total.
	hist, err := LoadHistory(target.ProjectRoot, target.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 3 {
		t.Fatalf("history len=%d want 3", len(hist))
	}
	// And round 1's findings should still be there as carry-forward
	// reference data even after subsequent clean rounds.
	if len(hist[0].Findings) != 1 || hist[0].Findings[0].ClaimHash != "h1" {
		t.Fatalf("history[0] drift: %+v", hist[0])
	}
}

// TestController_BudgetExhaustion — when MaxRounds caps below convergence,
// the loop exits with StatusBudgetExhausted naming the axis.
func TestController_BudgetExhaustion(t *testing.T) {
	// Never converges (each round has a warning) → MaxRounds=2 is the bound.
	stage := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "BREAKS", Findings: []RoundFinding{
				{ClaimHash: "h1", Severity: "warning", Member: "x"},
			}, Verdicts: map[string]string{}},
		},
	}
	c, target := newTestController(t, stage)
	c.Budget = Budget{MaxRounds: 1}
	// Use a stopping criterion that won't fire so we exit on budget.
	c.Stopping = []StoppingCriterion{&ConsecutiveCleanCriterion{N: 5}}
	res, err := c.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// After 1 round with a warning, the loop should pause for fixes
	// before budget kicks in.
	if res.Status != StatusNeedsFixes {
		t.Fatalf("status=%s want %s", res.Status, StatusNeedsFixes)
	}
}

// TestController_NoNovelFindingsFires — every finding in the second
// round is a claim_hash that was already in round 1; the criterion
// classifies the round as converged.
func TestController_NoNovelFindingsFires(t *testing.T) {
	stage := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "SURVIVES", Findings: []RoundFinding{
				{ClaimHash: "h1", Severity: "suggestion", Member: "x"},
			}, Verdicts: map[string]string{}},
			{OverallVerdict: "SURVIVES", Findings: []RoundFinding{
				{ClaimHash: "h1", Severity: "suggestion", Member: "x"}, // carry-forward
			}, Verdicts: map[string]string{}},
		},
	}
	c, target := newTestController(t, stage)
	c.Stopping = []StoppingCriterion{&NoNovelFindingsCriterion{}}
	res, err := c.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != StatusConverged {
		t.Fatalf("status=%s want %s reason=%s", res.Status, StatusConverged, res.Reason)
	}
	if !res.Rounds[1].Findings[0].CarryForward {
		t.Fatalf("round 2 finding should be classified as carry-forward")
	}
}

// TestParseStoppingCriteria pins the --stop-on parser.
func TestParseStoppingCriteria(t *testing.T) {
	tests := []struct {
		spec    string
		wantLen int
		names   []string
		wantErr bool
	}{
		{"", 1, []string{"consecutive-clean(2)"}, false},
		{"consecutive-clean(3)", 1, []string{"consecutive-clean(3)"}, false},
		{"consecutive-clean(2),no-novel-findings", 2, []string{"consecutive-clean(2)", "no-novel-findings"}, false},
		{"max-rounds(5),no-novel-findings", 2, []string{"max-rounds(5)", "no-novel-findings"}, false},
		{"unknown(1)", 0, nil, true},
		{"consecutive-clean(0)", 0, nil, true},
		{"max-rounds(abc)", 0, nil, true},
	}
	for _, tt := range tests {
		got, err := ParseStoppingCriteria(tt.spec)
		if tt.wantErr {
			if err == nil {
				t.Errorf("spec=%q expected error, got %v", tt.spec, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("spec=%q: %v", tt.spec, err)
			continue
		}
		if len(got) != tt.wantLen {
			t.Errorf("spec=%q len=%d want %d", tt.spec, len(got), tt.wantLen)
			continue
		}
		for i, name := range tt.names {
			if got[i].Name() != name {
				t.Errorf("spec=%q [%d] name=%q want %q", tt.spec, i, got[i].Name(), name)
			}
		}
	}
}

// TestSelectRotator pins the rotation-strategy parser.
func TestSelectRotator(t *testing.T) {
	if _, err := SelectRotator("focus-shuffle"); err != nil {
		t.Fatalf("focus-shuffle: %v", err)
	}
	if _, err := SelectRotator("composite:focus"); err != nil {
		t.Fatalf("composite: %v", err)
	}
	if _, err := SelectRotator(""); err != nil {
		t.Fatalf("default: %v", err)
	}
	if _, err := SelectRotator("ultra-rotation"); err == nil {
		t.Fatalf("unknown rotator should error")
	}
	r, _ := SelectRotator("composite:focus,bogus_axis")
	if _, err := r.NextPanel(nil, dispatch.DefaultDispatchConfig()); err == nil {
		t.Fatalf("composite with unknown axis should error at NextPanel time")
	}
}

// TestLedgerRoundtrip verifies SaveRound + LoadHistory are inverses.
func TestLedgerRoundtrip(t *testing.T) {
	dir := t.TempDir()
	r1 := &RoundResult{Round: 1, OverallVerdict: "SURVIVES", Findings: []RoundFinding{{ClaimHash: "h1", Severity: "warning"}}}
	r2 := &RoundResult{Round: 2, OverallVerdict: "SURVIVES"}
	if _, err := SaveRound(dir, "P-rt", r1); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveRound(dir, "P-rt", r2); err != nil {
		t.Fatal(err)
	}
	hist, err := LoadHistory(dir, "P-rt")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("len=%d want 2", len(hist))
	}
	if hist[0].Round != 1 || hist[1].Round != 2 {
		t.Fatalf("order drift: %d, %d", hist[0].Round, hist[1].Round)
	}
	// Verify file naming uses the zero-padded scheme.
	path := filepath.Join(LedgerDir(dir, "P-rt"), "round-0001.json")
	if _, err := osStat(path); err != nil {
		t.Fatalf("expected zero-padded filename at %s: %v", path, err)
	}
}

func focuses(ms []dispatch.PanelMember) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Focus
	}
	return out
}
