package ledger

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/dpdanpittman/tribunal/internal/agent"
)

func mustKeypair(t *testing.T, seed byte) *agent.Keypair {
	t.Helper()
	kp, err := agent.NewKeypairFromSeed(bytes.Repeat([]byte{seed}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return kp
}

func tempLedger(t *testing.T) *Ledger {
	t.Helper()
	dir := t.TempDir()
	return New(filepath.Join(dir, "ledger.jsonl"))
}

func mustSignFinding(t *testing.T, f *Finding, kp *agent.Keypair) *Finding {
	t.Helper()
	if err := f.Sign(kp); err != nil {
		t.Fatal(err)
	}
	return f
}

func mustSignResolution(t *testing.T, r *Resolution, kp *agent.Keypair) *Resolution {
	t.Helper()
	if err := r.Sign(kp); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFindingSignVerifyRoundtrip(t *testing.T) {
	kp := mustKeypair(t, 0x01)
	f := NewFinding("F-1", "P-1", 1, kp, "agent-1", SeverityCritical, CategoryEdgeCase, "sha256:abc", ".tribunal/findings/F-1.md")
	mustSignFinding(t, f, kp)

	if err := f.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// Tampering breaks verification.
	f.Severity = SeverityWarning
	if err := f.Verify(); err == nil {
		t.Fatal("expected verify failure after tampering")
	}
}

func TestFindingSignRejectsWrongKeypair(t *testing.T) {
	k1 := mustKeypair(t, 0x01)
	k2 := mustKeypair(t, 0x02)
	f := NewFinding("F-1", "P-1", 1, k1, "agent-1", SeverityCritical, CategoryEdgeCase, "h", "u")
	if err := f.Sign(k2); err == nil {
		t.Fatal("expected sign with wrong keypair to fail")
	}
}

func TestLedgerAppendReadRoundtrip(t *testing.T) {
	l := tempLedger(t)
	kp := mustKeypair(t, 0x05)

	for i := 0; i < 3; i++ {
		f := NewFinding(
			"F-"+itoa(i), "P-1", 1, kp, "agent",
			SeverityCritical, CategoryEdgeCase, "h", "u",
		)
		mustSignFinding(t, f, kp)
		if err := l.AppendFinding(f); err != nil {
			t.Fatal(err)
		}
	}
	findings, resolutions, err := l.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 3 || len(resolutions) != 0 {
		t.Fatalf("got %d findings + %d resolutions", len(findings), len(resolutions))
	}
}

func TestLedgerVerifyAllDetectsCorruption(t *testing.T) {
	l := tempLedger(t)
	kp := mustKeypair(t, 0x09)
	f := NewFinding("F-1", "P-1", 1, kp, "agent", SeverityCritical, CategoryEdgeCase, "h", "u")
	mustSignFinding(t, f, kp)
	if err := l.AppendFinding(f); err != nil {
		t.Fatal(err)
	}
	// Hand-craft a finding with a forged signature and bypass the writer's
	// signature check by writing directly. This simulates ledger tampering.
	bad := *f
	bad.FindingID = "F-2"
	if err := l.appendJSON(&bad); err != nil {
		t.Fatal(err)
	}
	if err := l.VerifyAll(); err == nil {
		t.Fatal("expected VerifyAll to flag corruption")
	}
}

func TestReputationBasicMath(t *testing.T) {
	kp1 := mustKeypair(t, 0xAA)
	kp2 := mustKeypair(t, 0xBB)
	pmKp := mustKeypair(t, 0xCC)

	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	mkF := func(id string, agentKp *agent.Keypair, label string, sev Severity, ageDays int) *Finding {
		f := &Finding{
			Kind:        KindFinding,
			FindingID:   id,
			PlanID:      "P",
			Round:       1,
			AgentPubkey: agentKp.PublicKeyString(),
			AgentLabel:  label,
			Severity:    sev,
			Category:    CategoryEdgeCase,
			ClaimHash:   "h",
			ClaimURI:    "u",
			Stake:       sev.DefaultStake(),
			Timestamp:   now.AddDate(0, 0, -ageDays),
		}
		if err := f.Sign(agentKp); err != nil {
			t.Fatal(err)
		}
		return f
	}
	mkR := func(id string, outcome Outcome) *Resolution {
		r := NewResolution(id, "P", outcome, pmKp, "pm", "eh", "eu")
		mustSignResolution(t, r, pmKp)
		return r
	}

	// Agent 1: two recent TPs (heavy positive), one old FP (light negative).
	// Agent 2: one recent FP, no TPs (negative).
	findings := []*Finding{
		mkF("F-1", kp1, "alice", SeverityCritical, 1),  // TP, age 1d
		mkF("F-2", kp1, "alice", SeverityWarning, 5),   // TP
		mkF("F-3", kp1, "alice", SeverityCritical, 60), // FP, age 60d (heavily decayed)
		mkF("F-4", kp2, "bob", SeverityCritical, 2),    // FP
	}
	resolutions := []*Resolution{
		mkR("F-1", OutcomeTruePositive),
		mkR("F-2", OutcomeTruePositive),
		mkR("F-3", OutcomeFalsePositive),
		mkR("F-4", OutcomeFalsePositive),
	}

	cfg := DefaultReputationConfig()
	reps := ComputeReputation(findings, resolutions, cfg, now)
	if len(reps) != 2 {
		t.Fatalf("expected 2 reputations, got %d: %+v", len(reps), reps)
	}
	byLabel := map[string]*Reputation{}
	for _, r := range reps {
		byLabel[r.AgentLabel] = r
	}
	alice := byLabel["alice"]
	bob := byLabel["bob"]
	if alice == nil || bob == nil {
		t.Fatalf("expected both alice and bob: %+v", reps)
	}
	if alice.Score <= 0 {
		t.Errorf("alice should have positive score (2 TP - 1 decayed FP), got %.2f", alice.Score)
	}
	if bob.Score >= 0 {
		t.Errorf("bob should have negative score (1 FP), got %.2f", bob.Score)
	}
	if alice.TPCount != 2 || alice.FPCount != 1 {
		t.Errorf("alice counts wrong: tp=%d fp=%d", alice.TPCount, alice.FPCount)
	}
	if bob.TPCount != 0 || bob.FPCount != 1 {
		t.Errorf("bob counts wrong: tp=%d fp=%d", bob.TPCount, bob.FPCount)
	}
}

func TestReputationWindowExcludesOldFindings(t *testing.T) {
	kp := mustKeypair(t, 0x12)
	pmKp := mustKeypair(t, 0x13)
	now := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)

	f := &Finding{
		Kind:        KindFinding,
		FindingID:   "F-old",
		PlanID:      "P",
		Round:       1,
		AgentPubkey: kp.PublicKeyString(),
		AgentLabel:  "old-agent",
		Severity:    SeverityCritical,
		Category:    CategoryEdgeCase,
		ClaimHash:   "h",
		ClaimURI:    "u",
		Stake:       8,
		Timestamp:   now.AddDate(-1, 0, 0), // 1 year old
	}
	mustSignFinding(t, f, kp)
	r := NewResolution("F-old", "P", OutcomeTruePositive, pmKp, "pm", "eh", "eu")
	mustSignResolution(t, r, pmKp)

	reps := ComputeReputation([]*Finding{f}, []*Resolution{r}, DefaultReputationConfig(), now)
	if len(reps) != 0 {
		t.Fatalf("expected 0 reputations (old finding outside window), got %d", len(reps))
	}
}

func TestGateDecisions(t *testing.T) {
	c := DefaultGateConfig()
	cases := []struct {
		score float64
		want  GateDecision
	}{
		{-50, GateRotateOut},
		{-5, GateRequireCorroboration},
		{0, GateNormal},
		{30, GateNormal},
		{50, GateAutoElevate},
		{100, GateAutoElevate},
	}
	for _, tc := range cases {
		if got := c.Decide(tc.score); got != tc.want {
			t.Errorf("Decide(%v) = %v, want %v", tc.score, got, tc.want)
		}
	}
}

func TestGateDecideForAgentDefaultsToCorroboration(t *testing.T) {
	c := DefaultGateConfig()
	got := c.DecideForAgent(nil, "ed25519:unknown")
	if got != GateRequireCorroboration {
		t.Fatalf("unknown agent should require corroboration, got %v", got)
	}
}

func TestModelFamilyMapping(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-7":  "anthropic",
		"gpt-5":            "openai",
		"o3-mini":          "openai",
		"gemini-2.5-pro":   "google",
		"qwen3-32b":        "local",
		"llama-3.1-70b":    "local",
		"some-other-model": "other",
	}
	for input, want := range cases {
		if got := ModelFamily(input); got != want {
			t.Errorf("ModelFamily(%q) = %q, want %q", input, got, want)
		}
	}
}

// small itoa to avoid pulling in strconv in test wiring
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		b[n] = '-'
	}
	return string(b[n:])
}
