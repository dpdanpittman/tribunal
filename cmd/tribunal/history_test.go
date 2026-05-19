package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dpdanpittman/tribunal/internal/converge"
	"github.com/dpdanpittman/tribunal/internal/dispatch"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

// TestBuildTimeline_EmptyPlan exercises the absent-state path: no
// convergence rounds, no signed-ledger entries. Expect a populated
// summary with zero counters and stable empty slices (not nil).
func TestBuildTimeline_EmptyPlan(t *testing.T) {
	tl := buildTimeline("P-empty", nil, nil, nil)
	if tl.PlanID != "P-empty" {
		t.Errorf("PlanID = %q, want P-empty", tl.PlanID)
	}
	if tl.Rounds == nil || len(tl.Rounds) != 0 {
		t.Errorf("Rounds should be empty slice, not nil; got len=%d", len(tl.Rounds))
	}
	if tl.SignedFindings == nil || len(tl.SignedFindings) != 0 {
		t.Errorf("SignedFindings should be empty slice, not nil")
	}
	if tl.Resolutions == nil {
		t.Errorf("Resolutions should be empty slice, not nil")
	}
	if tl.Summary.RoundCount != 0 || tl.Summary.SignedCount != 0 {
		t.Errorf("summary counters non-zero on empty plan: %+v", tl.Summary)
	}
}

// TestBuildTimeline_MultiRound covers the load-bearing path: two rounds,
// one with carry-forward, plus signed-ledger entries with a resolution.
// Verifies novel/carry-forward bookkeeping, final-verdict derivation,
// and resolution-driven open-count.
func TestBuildTimeline_MultiRound(t *testing.T) {
	now := time.Now().UTC()
	rounds := []converge.RoundResult{
		{
			Round:       1,
			StartedAt:   now,
			CompletedAt: now.Add(5 * time.Minute),
			Duration:    5 * time.Minute,
			Panel: converge.PanelComposition{
				Round:        1,
				Members:      []dispatch.PanelMember{{Label: "adversary-opus"}},
				RotationAxis: "focus",
			},
			Findings: []converge.RoundFinding{
				{ClaimHash: "hash-A", Severity: "critical"},
				{ClaimHash: "hash-B", Severity: "warning"},
			},
			OverallVerdict: "BREAKS",
		},
		{
			Round:       2,
			StartedAt:   now.Add(6 * time.Minute),
			CompletedAt: now.Add(11 * time.Minute),
			Duration:    5 * time.Minute,
			Panel: converge.PanelComposition{
				Round:        2,
				Members:      []dispatch.PanelMember{{Label: "adversary-sonnet"}},
				RotationAxis: "focus",
			},
			Findings: []converge.RoundFinding{
				{ClaimHash: "hash-A", Severity: "critical"}, // carry-forward
				{ClaimHash: "hash-C", Severity: "suggestion"},
			},
			OverallVerdict: "SURVIVES",
			Stopped:        true,
			StopCriterion:  "consecutive-clean",
			StopReason:     "two consecutive rounds without new critical findings",
		},
	}
	findings := []*ledger.Finding{
		{FindingID: "F-001", PlanID: "P-test", Round: 1, AgentLabel: "adversary-opus", Severity: ledger.SeverityCritical, ClaimHash: "hash-A", Timestamp: now},
		{FindingID: "F-002", PlanID: "P-test", Round: 1, AgentLabel: "adversary-opus", Severity: ledger.SeverityWarning, ClaimHash: "hash-B", Timestamp: now},
	}
	resolutions := []*ledger.Resolution{
		{FindingID: "F-001", PlanID: "P-test", Outcome: ledger.OutcomeTruePositive, ResolverLabel: "pm", Timestamp: now.Add(20 * time.Minute)},
	}

	tl := buildTimeline("P-test", rounds, findings, resolutions)

	if got, want := len(tl.Rounds), 2; got != want {
		t.Fatalf("Rounds count = %d, want %d", got, want)
	}
	r1 := tl.Rounds[0]
	if r1.NovelFindings != 2 || r1.CarriedForward != 0 {
		t.Errorf("round 1 novel/carry = %d/%d, want 2/0", r1.NovelFindings, r1.CarriedForward)
	}
	r2 := tl.Rounds[1]
	if r2.NovelFindings != 1 || r2.CarriedForward != 1 {
		t.Errorf("round 2 novel/carry = %d/%d, want 1/1", r2.NovelFindings, r2.CarriedForward)
	}
	if tl.Summary.UniqueClaims != 3 {
		t.Errorf("UniqueClaims = %d, want 3", tl.Summary.UniqueClaims)
	}
	if tl.Summary.CarriedForward != 1 {
		t.Errorf("CarriedForward = %d, want 1", tl.Summary.CarriedForward)
	}
	if tl.Summary.FinalVerdict != "SURVIVES" {
		t.Errorf("FinalVerdict = %q, want SURVIVES", tl.Summary.FinalVerdict)
	}
	if tl.Summary.StoppedAtRound != 2 || tl.Summary.StopCriterion != "consecutive-clean" {
		t.Errorf("stop bookkeeping wrong: %+v", tl.Summary)
	}
	if tl.Summary.SignedCount != 2 || tl.Summary.ResolutionCount != 1 || tl.Summary.OpenFindings != 1 {
		t.Errorf("signed/resolution/open mismatch: %+v", tl.Summary)
	}
}

// TestWriteHistoryJSON_Round-trips verifies the json output parses back
// into Timeline cleanly — the schema is stable.
func TestWriteHistoryJSON_RoundTrip(t *testing.T) {
	tl := buildTimeline("P-rt", nil, nil, nil)
	var buf bytes.Buffer
	if err := writeHistoryJSON(&buf, tl); err != nil {
		t.Fatalf("writeHistoryJSON: %v", err)
	}
	var got Timeline
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, buf.String())
	}
	if got.PlanID != "P-rt" {
		t.Errorf("PlanID round-trip failed: %q", got.PlanID)
	}
}

// TestWriteText_SmokeOutput ensures the text writer emits the expected
// section headers without panicking. We don't assert exact layout — the
// human-readable shape is allowed to drift; the json shape is the
// contract.
func TestWriteText_SmokeOutput(t *testing.T) {
	now := time.Now().UTC()
	rounds := []converge.RoundResult{
		{
			Round:       1,
			StartedAt:   now,
			CompletedAt: now.Add(time.Minute),
			Duration:    time.Minute,
			Panel: converge.PanelComposition{
				Members: []dispatch.PanelMember{{Label: "adversary-opus"}},
			},
			Findings: []converge.RoundFinding{
				{ClaimHash: "h1", Severity: "warning"},
			},
			OverallVerdict: "SURVIVES",
		},
	}
	tl := buildTimeline("P-text", rounds, nil, nil)
	var buf bytes.Buffer
	if err := writeText(&buf, tl); err != nil {
		t.Fatalf("writeText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Plan:", "Rounds:", "Round 1", "Verdict:"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q in:\n%s", want, out)
		}
	}
}

// TestLoadPlanLedger_FiltersByPlanID covers the on-disk integration:
// write a ledger with mixed plan_ids, ensure loadPlanLedger returns
// only the requested plan's entries.
func TestLoadPlanLedger_FiltersByPlanID(t *testing.T) {
	dir := t.TempDir()
	ledgerPath := filepath.Join(dir, ".tribunal", "ledger.jsonl")
	if err := os.MkdirAll(filepath.Dir(ledgerPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Write two findings with different plan_ids as raw json lines.
	// We're not testing signature verification here — the read path
	// doesn't require valid signatures, only well-formed entries.
	lines := []string{
		`{"kind":"finding","finding_id":"F-A","plan_id":"P-keep","round":1,"agent_pubkey":"","agent_label":"adv","severity":"warning","category":"shared_blind_spot","claim_hash":"h1","claim_uri":"","stake":1,"timestamp":"2026-05-18T00:00:00Z","signature":""}`,
		`{"kind":"finding","finding_id":"F-B","plan_id":"P-drop","round":1,"agent_pubkey":"","agent_label":"adv","severity":"warning","category":"shared_blind_spot","claim_hash":"h2","claim_uri":"","stake":1,"timestamp":"2026-05-18T00:01:00Z","signature":""}`,
	}
	if err := os.WriteFile(ledgerPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, _, err := loadPlanLedger(dir, "P-keep")
	if err != nil {
		t.Fatalf("loadPlanLedger: %v", err)
	}
	if len(findings) != 1 || findings[0].FindingID != "F-A" {
		t.Errorf("expected only F-A, got %+v", findings)
	}
}

// TestLoadPlanLedger_MissingLedger ensures an absent ledger returns
// empty results, not an error.
func TestLoadPlanLedger_MissingLedger(t *testing.T) {
	dir := t.TempDir()
	findings, resolutions, err := loadPlanLedger(dir, "P-whatever")
	if err != nil {
		t.Fatalf("loadPlanLedger on missing ledger: %v", err)
	}
	if findings != nil || resolutions != nil {
		t.Errorf("expected nil slices on missing ledger, got %d findings / %d resolutions",
			len(findings), len(resolutions))
	}
}

// TestLoadTrajectoryLedger_FiltersByTrajectoryID (v0.5.6) covers the
// mirror of TestLoadPlanLedger_FiltersByPlanID for the new trajectory-
// scoped query. A ledger with mixed plan-scoped + trajectory-scoped
// entries should return only the requested trajectory's items via
// loadTrajectoryLedger, and only the plan's items via loadPlanLedger.
func TestLoadTrajectoryLedger_FiltersByTrajectoryID(t *testing.T) {
	dir := t.TempDir()
	ledgerPath := filepath.Join(dir, ".tribunal", "ledger.jsonl")
	if err := os.MkdirAll(filepath.Dir(ledgerPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Three entries: one plan-scoped, one trajectory-scoped "session-essence",
	// one trajectory-scoped "tribunal-self-audits". As raw JSON lines so the
	// test doesn't depend on signing infra.
	lines := []string{
		`{"kind":"finding","finding_id":"F-PLAN","plan_id":"P-keep","round":1,"agent_pubkey":"","agent_label":"adv","severity":"warning","category":"shared_blind_spot","claim_hash":"h1","claim_uri":"","stake":1,"timestamp":"2026-05-18T00:00:00Z","signature":""}`,
		`{"kind":"finding","finding_id":"F-SE","plan_id":"","trajectory_id":"session-essence","round":0,"agent_pubkey":"","agent_label":"adv","severity":"critical","category":"temporal_state_mismatch","claim_hash":"h2","claim_uri":"","stake":1,"timestamp":"2026-05-18T00:01:00Z","signature":""}`,
		`{"kind":"finding","finding_id":"F-SELF","plan_id":"","trajectory_id":"tribunal-self-audits","round":0,"agent_pubkey":"","agent_label":"adv","severity":"warning","category":"temporal_state_mismatch","claim_hash":"h3","claim_uri":"","stake":1,"timestamp":"2026-05-18T00:02:00Z","signature":""}`,
	}
	if err := os.WriteFile(ledgerPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// loadTrajectoryLedger("session-essence") should return only F-SE.
	findings, _, err := loadTrajectoryLedger(dir, "session-essence")
	if err != nil {
		t.Fatalf("loadTrajectoryLedger: %v", err)
	}
	if len(findings) != 1 || findings[0].FindingID != "F-SE" {
		t.Errorf("expected only F-SE for trajectory=session-essence, got %+v", findings)
	}

	// loadPlanLedger("P-keep") should return only F-PLAN — trajectory
	// entries don't contaminate plan-scoped queries.
	planFindings, _, err := loadPlanLedger(dir, "P-keep")
	if err != nil {
		t.Fatalf("loadPlanLedger: %v", err)
	}
	if len(planFindings) != 1 || planFindings[0].FindingID != "F-PLAN" {
		t.Errorf("expected only F-PLAN for plan=P-keep, got %+v", planFindings)
	}

	// loadTrajectoryLedger("nonexistent") should return zero entries
	// (no error).
	noFindings, _, err := loadTrajectoryLedger(dir, "does-not-exist")
	if err != nil {
		t.Fatalf("loadTrajectoryLedger nonexistent: %v", err)
	}
	if len(noFindings) != 0 {
		t.Errorf("expected zero findings for nonexistent trajectory, got %d", len(noFindings))
	}
}

// TestFinding_ExactlyOneOfPlanOrTrajectory (v0.5.6) pins the validation
// at the signing layer: a Finding with both plan_id and trajectory_id
// set, or neither, must fail to sign.
func TestFinding_ExactlyOneOfPlanOrTrajectory(t *testing.T) {
	// We don't need to actually sign — just call SigningPayload and
	// confirm the validation error.
	both := &ledger.Finding{
		Kind:         ledger.KindFinding,
		FindingID:    "F-bad",
		PlanID:       "P-001",
		TrajectoryID: "traj-001",
		Severity:     ledger.SeverityWarning,
	}
	if _, err := both.SigningPayload(); err == nil {
		t.Error("expected error when both plan_id and trajectory_id are set, got nil")
	}

	neither := &ledger.Finding{
		Kind:      ledger.KindFinding,
		FindingID: "F-bad",
		Severity:  ledger.SeverityWarning,
	}
	if _, err := neither.SigningPayload(); err == nil {
		t.Error("expected error when neither plan_id nor trajectory_id is set, got nil")
	}

	planOnly := &ledger.Finding{
		Kind:      ledger.KindFinding,
		FindingID: "F-good-plan",
		PlanID:    "P-001",
		Severity:  ledger.SeverityWarning,
	}
	if _, err := planOnly.SigningPayload(); err != nil {
		t.Errorf("plan-only finding should sign OK, got: %v", err)
	}

	trajectoryOnly := &ledger.Finding{
		Kind:         ledger.KindFinding,
		FindingID:    "F-good-traj",
		TrajectoryID: "traj-001",
		Severity:     ledger.SeverityWarning,
	}
	if _, err := trajectoryOnly.SigningPayload(); err != nil {
		t.Errorf("trajectory-only finding should sign OK, got: %v", err)
	}
}
