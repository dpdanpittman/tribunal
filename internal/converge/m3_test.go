package converge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dpdanpittman/tribunal/internal/dispatch"
)

// stubVerifyGate returns a queued sequence of VerifyResults. Each
// Verify call pops one; queue exhaustion errors so tests catch
// over-invocation.
type stubVerifyGate struct {
	queue []VerifyResult
	calls int
	err   error
}

func (g *stubVerifyGate) Verify(ctx context.Context, projectRoot string) (*VerifyResult, error) {
	g.calls++
	if g.err != nil {
		return nil, g.err
	}
	if g.calls > len(g.queue) {
		return nil, errors.New("stubVerifyGate: queue exhausted")
	}
	v := g.queue[g.calls-1]
	return &v, nil
}

// newM3Controller builds a Controller in a temp git repo so the M3
// path (ApplyPatch + RevertWorkingTree) has a real working tree to
// manipulate. The patches in the queue must apply against this seed.
func newM3Controller(t *testing.T, stage AdversaryStage, impl Implementer, gate VerifyGate) (*Controller, ConvergenceTarget) {
	t.Helper()
	dir := initGitRepo(t)
	// Seed a file the patch can modify.
	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOrDie(t, dir, "add", "x.txt")
	gitOrDie(t, dir, "commit", "-q", "-m", "seed")

	c := &Controller{
		Adversary:      stage,
		Rotator:        &FocusShuffleRotator{},
		Stopping:       []StoppingCriterion{&ConsecutiveCleanCriterion{N: 2}},
		Budget:         Budget{MaxRounds: 5, MaxWallclock: time.Minute},
		DispatchConfig: dispatch.DefaultDispatchConfig(),
		Implementer:    impl,
		AutoApply:      true,
		AutoContinue:   true,
		VerifyGate:     gate,
	}
	target := ConvergenceTarget{PlanID: "P-m3", ProjectRoot: dir}
	return c, target
}

// validPatch returns a unified diff that changes x.txt v1 → v2.
func validPatch() string {
	return "diff --git a/x.txt b/x.txt\nindex 0000000..1111111 100644\n--- a/x.txt\n+++ b/x.txt\n@@ -1 +1 @@\n-v1\n+v2\n"
}

// TestM3_VerifyPassContinuesLoop — patch applies + verify passes →
// loop continues to next round; convergence happens via
// consecutive-clean(2) over the subsequent clean rounds.
func TestM3_VerifyPassContinuesLoop(t *testing.T) {
	stage := &stubStage{
		queue: []RoundOutput{
			// Round 1: BREAKS, implementer patches, verify passes, continue.
			{OverallVerdict: "BREAKS", Findings: []RoundFinding{
				{ClaimHash: "h1", Severity: "critical", Member: "claude-opus-spec"},
			}, Verdicts: map[string]string{}},
			// Round 2: SURVIVES.
			{OverallVerdict: "SURVIVES", Verdicts: map[string]string{}},
			// Round 3: SURVIVES → consecutive-clean(2) fires.
			{OverallVerdict: "SURVIVES", Verdicts: map[string]string{}},
		},
	}
	impl := &stubImplementer{queue: []PatchOutput{{Patch: validPatch(), Reasoning: "bump to v2"}}}
	gate := &stubVerifyGate{queue: []VerifyResult{{Passed: true, Summary: "go-build ok, go-test ok"}}}

	c, target := newM3Controller(t, stage, impl, gate)
	res, err := c.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != StatusConverged {
		t.Fatalf("status=%s want %s reason=%s", res.Status, StatusConverged, res.Reason)
	}
	if len(res.Rounds) != 3 {
		t.Fatalf("rounds=%d want 3", len(res.Rounds))
	}
	r1 := res.Rounds[0]
	if !r1.PatchApplied || !r1.VerifyRan || !r1.VerifyPassed {
		t.Fatalf("round 1 M3 state drift: applied=%v verifyRan=%v verifyPassed=%v",
			r1.PatchApplied, r1.VerifyRan, r1.VerifyPassed)
	}
	if r1.Reverted {
		t.Fatalf("round 1 should NOT have reverted on verify pass")
	}
	// Patch should still be in the tree (v2).
	body, _ := os.ReadFile(filepath.Join(target.ProjectRoot, "x.txt"))
	if strings.TrimSpace(string(body)) != "v2" {
		t.Fatalf("post-loop file contents: %q want v2", string(body))
	}
}

// TestM3_VerifyFailRevertsAndExits — patch applies but verify fails;
// controller calls RevertWorkingTree and exits StatusNeedsFixes.
func TestM3_VerifyFailRevertsAndExits(t *testing.T) {
	stage := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "BREAKS", Findings: []RoundFinding{
				{ClaimHash: "h1", Severity: "critical", Member: "x"},
			}, Verdicts: map[string]string{}},
		},
	}
	impl := &stubImplementer{queue: []PatchOutput{{Patch: validPatch(), Reasoning: "bump"}}}
	gate := &stubVerifyGate{queue: []VerifyResult{{Passed: false, Summary: "go-test FAILED — TestFoo broken"}}}

	c, target := newM3Controller(t, stage, impl, gate)
	res, err := c.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != StatusNeedsFixes {
		t.Fatalf("status=%s want %s", res.Status, StatusNeedsFixes)
	}
	r := res.Rounds[0]
	if !r.PatchApplied || !r.VerifyRan || r.VerifyPassed {
		t.Fatalf("round M3 state drift: applied=%v verifyRan=%v verifyPassed=%v",
			r.PatchApplied, r.VerifyRan, r.VerifyPassed)
	}
	if !r.Reverted {
		t.Fatalf("round should be reverted on verify fail")
	}
	if !strings.Contains(r.VerifySummary, "TestFoo broken") {
		t.Fatalf("verify summary lost: %q", r.VerifySummary)
	}
	// Reason should mention the revert + summary.
	if !strings.Contains(res.Reason, "reverted") || !strings.Contains(res.Reason, "TestFoo broken") {
		t.Fatalf("reason missing revert/summary: %q", res.Reason)
	}
	// File should be back at v1.
	body, _ := os.ReadFile(filepath.Join(target.ProjectRoot, "x.txt"))
	if strings.TrimSpace(string(body)) != "v1" {
		t.Fatalf("post-revert file contents: %q want v1", string(body))
	}
}

// TestM3_VerifyGateErrorReverts — gate itself errors (not just a
// failing verdict); controller treats it as a failure (revert + exit).
func TestM3_VerifyGateErrorReverts(t *testing.T) {
	stage := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "BREAKS", Findings: []RoundFinding{
				{ClaimHash: "h1", Severity: "critical", Member: "x"},
			}, Verdicts: map[string]string{}},
		},
	}
	impl := &stubImplementer{queue: []PatchOutput{{Patch: validPatch()}}}
	gate := &stubVerifyGate{err: errors.New("gate broke")}

	c, target := newM3Controller(t, stage, impl, gate)
	res, err := c.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != StatusNeedsFixes {
		t.Fatalf("status=%s want %s", res.Status, StatusNeedsFixes)
	}
	r := res.Rounds[0]
	if !r.Reverted {
		t.Fatalf("gate error should still trigger revert")
	}
	if !strings.Contains(r.VerifySummary, "gate broke") {
		t.Fatalf("verify summary should capture gate error: %q", r.VerifySummary)
	}
	body, _ := os.ReadFile(filepath.Join(target.ProjectRoot, "x.txt"))
	if strings.TrimSpace(string(body)) != "v1" {
		t.Fatalf("post-revert file contents: %q want v1", string(body))
	}
}

// TestM3_RequiresAutoApply — if AutoContinue is set but AutoApply is
// not, the controller falls through to the M2 path (no verify, no
// continue). The CLI also blocks this combo upstream; the controller
// itself stays permissive so library callers can wire things however
// they want, but only triggers the M3 path when both are true.
func TestM3_RequiresAutoApply(t *testing.T) {
	stage := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "BREAKS", Findings: []RoundFinding{
				{ClaimHash: "h1", Severity: "critical", Member: "x"},
			}, Verdicts: map[string]string{}},
		},
	}
	impl := &stubImplementer{queue: []PatchOutput{{Patch: validPatch()}}}
	gate := &stubVerifyGate{}

	c, target := newM3Controller(t, stage, impl, gate)
	c.AutoApply = false // contradiction with AutoContinue=true
	res, err := c.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if gate.calls != 0 {
		t.Fatalf("verify gate must NOT be called when AutoApply=false (called %d)", gate.calls)
	}
	if res.Status != StatusNeedsFixes {
		t.Fatalf("status=%s want %s", res.Status, StatusNeedsFixes)
	}
}

// TestRevertWorkingTree — sanity check the helper does both
// reset --hard and clean -fd.
func TestRevertWorkingTree(t *testing.T) {
	dir := initGitRepo(t)
	// Seed two files; one we'll modify, one we'll add post-commit.
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOrDie(t, dir, "add", "tracked.txt")
	gitOrDie(t, dir, "commit", "-q", "-m", "seed")
	// Modify tracked + add untracked (simulating an implementer patch).
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := RevertWorkingTree(ctx, dir); err != nil {
		t.Fatalf("revert: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "tracked.txt"))
	if string(body) != "orig" {
		t.Fatalf("tracked file not restored: %q", string(body))
	}
	if _, err := os.Stat(filepath.Join(dir, "untracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked file not removed (err=%v)", err)
	}
}
