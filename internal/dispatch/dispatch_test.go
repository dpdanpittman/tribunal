package dispatch

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mockProvider lets tests assert exactly how the dispatcher routes inputs
// and aggregates outputs without making real HTTP calls. The call counter
// is atomic because Dispatch fans out concurrently.
type mockProvider struct {
	name        string
	staticReply *Report
	staticErr   error
	delay       time.Duration
	calls       atomic.Int64
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) Attack(ctx context.Context, member PanelMember, sys, user string) (*Report, error) {
	m.calls.Add(1)
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.staticErr != nil {
		return nil, m.staticErr
	}
	cp := *m.staticReply
	cp.Member = member
	return &cp, nil
}

func TestDispatchRoutesByProviderName(t *testing.T) {
	reg := NewRegistry()
	pa := &mockProvider{name: "claude", staticReply: &Report{Verdict: VerdictSurvives, Findings: []ParsedFinding{{Category: "edge_case", Severity: "warning"}}}}
	pb := &mockProvider{name: "openai", staticReply: &Report{Verdict: VerdictBreaks, Findings: []ParsedFinding{{Category: "shared_blind_spot", Severity: "critical"}}}}
	reg.Register(pa)
	reg.Register(pb)

	panel := Panel{
		Name: "test",
		Members: []PanelMember{
			{Label: "claude-spec", Provider: "claude", Model: "claude-opus-4-7", Temperature: 0, Focus: "spec"},
			{Label: "claude-impl", Provider: "claude", Model: "claude-opus-4-7", Temperature: 0.7, Focus: "impl"},
			{Label: "gpt-spec", Provider: "openai", Model: "gpt-5", Temperature: 0, Focus: "spec"},
		},
	}
	reports, err := Dispatch(context.Background(), reg, panel, "sys", "user")
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 3 {
		t.Fatalf("got %d reports, want 3", len(reports))
	}
	if pa.calls.Load() != 2 {
		t.Errorf("claude called %d times, want 2", pa.calls.Load())
	}
	if pb.calls.Load() != 1 {
		t.Errorf("openai called %d times, want 1", pb.calls.Load())
	}
	// Members carried through.
	if reports[0].Member.Label != "claude-spec" {
		t.Errorf("report 0 label = %q", reports[0].Member.Label)
	}
}

func TestDispatchCapturesErrors(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockProvider{name: "claude", staticErr: errString("boom")})
	panel := Panel{Members: []PanelMember{{Label: "x", Provider: "claude", Model: "m"}}}
	reports, err := Dispatch(context.Background(), reg, panel, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if reports[0].Error == "" || !strings.Contains(reports[0].Error, "boom") {
		t.Fatalf("expected error captured in report, got %+v", reports[0])
	}
	if reports[0].Verdict != VerdictIndeterminate {
		t.Errorf("expected INDETERMINATE on error, got %s", reports[0].Verdict)
	}
}

func TestDispatchUnknownProviderMarksIndeterminate(t *testing.T) {
	reg := NewRegistry()
	panel := Panel{Members: []PanelMember{{Label: "x", Provider: "ghost", Model: "m"}}}
	reports, err := Dispatch(context.Background(), reg, panel, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if reports[0].Verdict != VerdictIndeterminate {
		t.Errorf("expected INDETERMINATE for unknown provider")
	}
}

func TestSynthesizeSharedVsUnique(t *testing.T) {
	reports := []*Report{
		{
			Member:  PanelMember{Label: "a", Provider: "claude", Model: "claude-opus-4-7", Focus: "spec"},
			Verdict: VerdictBreaks,
			Findings: []ParsedFinding{
				{Category: "edge_case", Severity: "critical"},
				{Category: "shared_blind_spot", Severity: "warning"},
			},
		},
		{
			Member:  PanelMember{Label: "b", Provider: "claude", Model: "claude-opus-4-7", Focus: "impl"},
			Verdict: VerdictBreaks,
			Findings: []ParsedFinding{
				{Category: "edge_case", Severity: "warning"},
			},
		},
		{
			Member:   PanelMember{Label: "c", Provider: "claude", Model: "claude-sonnet-4-6", Focus: "temporal"},
			Verdict:  VerdictSurvives,
			Findings: nil,
		},
	}
	syn := Synthesize(reports, BucketComposite(BucketByVendorFamily, BucketByFocus))

	// edge_case appeared in a + b → shared.
	if len(syn.Shared) != 1 || syn.Shared[0].Category != "edge_case" {
		t.Fatalf("expected 1 shared finding (edge_case), got %+v", syn.Shared)
	}
	if syn.Shared[0].Severity != "critical" {
		t.Errorf("shared severity should be max (critical), got %q", syn.Shared[0].Severity)
	}
	// shared_blind_spot appeared only in a → unique.
	if len(syn.Unique) != 1 || syn.Unique[0].Category != "shared_blind_spot" {
		t.Fatalf("expected 1 unique finding, got %+v", syn.Unique)
	}
	if syn.Unique[0].Member != "a" {
		t.Errorf("unique finding member = %q", syn.Unique[0].Member)
	}
	if syn.Unique[0].Bucket != "anthropic+spec" {
		t.Errorf("unique finding bucket = %q, want anthropic+spec", syn.Unique[0].Bucket)
	}
	// Overall verdict: a + b BREAKS, a has critical → overall BREAKS.
	if syn.Overall != VerdictBreaks {
		t.Errorf("overall verdict = %q, want BREAKS", syn.Overall)
	}
}

func TestSynthesizeSurvivesWhenAllSurvive(t *testing.T) {
	reports := []*Report{
		{Member: PanelMember{Label: "a", Provider: "claude", Model: "m"}, Verdict: VerdictSurvives},
		{Member: PanelMember{Label: "b", Provider: "claude", Model: "m"}, Verdict: VerdictSurvives},
	}
	syn := Synthesize(reports, BucketByVendorFamily)
	if syn.Overall != VerdictSurvives {
		t.Errorf("expected SURVIVES, got %s", syn.Overall)
	}
}

func TestSelectBucketKnownAndUnknown(t *testing.T) {
	if _, err := SelectBucket("vendor_family"); err != nil {
		t.Fatal(err)
	}
	if _, err := SelectBucket("temperature_band"); err != nil {
		t.Fatal(err)
	}
	if _, err := SelectBucket("composite:vendor_family,focus"); err != nil {
		t.Fatal(err)
	}
	if _, err := SelectBucket("nonsense_axis"); err == nil {
		t.Fatal("expected error for unknown axis")
	}
}

func TestBucketByTemperatureBand(t *testing.T) {
	cases := []struct {
		t    float64
		want string
	}{
		{0, "deterministic"},
		{0.2, "deterministic"},
		{0.3, "balanced"},
		{0.6, "balanced"},
		{0.7, "creative"},
		{1.0, "creative"},
	}
	for _, c := range cases {
		got := BucketByTemperatureBand(PanelMember{Temperature: c.t})
		if got != c.want {
			t.Errorf("temp=%.2f bucket=%q want %q", c.t, got, c.want)
		}
	}
}

func TestParseReportExtractsVerdictAndFindings(t *testing.T) {
	raw := `VERDICT: BREAKS

The trio missed a critical edge case.

### Finding 1 — edge_case

- Category: edge_case
- Severity: critical
- Scenario: When n = math.MinInt the FizzBuzz path panics with a non-actionable message.
- Defense: Add an explicit boundary check for math.MinInt in main.go.

### Finding 2 — shared_blind_spot

- Category: shared_blind_spot
- Severity: warning
- Scenario: All three reviewers approved without exercising the empty-input path.

META: confidence=high, categories=[edge_case, shared_blind_spot]
`
	verdict, reason, findings := ParseReport(raw)
	if verdict != VerdictBreaks {
		t.Errorf("verdict = %q", verdict)
	}
	if reason != "" {
		t.Errorf("unexpected reason = %q", reason)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(findings))
	}
	if findings[0].Category != "edge_case" || findings[0].Severity != "critical" {
		t.Errorf("finding 0 = %+v", findings[0])
	}
	if findings[1].Category != "shared_blind_spot" || findings[1].Severity != "warning" {
		t.Errorf("finding 1 = %+v", findings[1])
	}
}

func TestParseReportMissingVerdictIsIndeterminate(t *testing.T) {
	raw := "nothing interesting here\nmaybe later\n"
	v, reason, _ := ParseReport(raw)
	if v != VerdictIndeterminate {
		t.Errorf("verdict = %q", v)
	}
	if reason == "" {
		t.Errorf("expected non-empty reason for INDETERMINATE")
	}
}

func TestValidatePanel(t *testing.T) {
	if err := ValidatePanel(Panel{}); err == nil {
		t.Error("expected error for empty panel")
	}
	if err := ValidatePanel(Panel{Members: []PanelMember{{Provider: "claude", Model: ""}}}); err == nil {
		t.Error("expected error for missing model")
	}
	if err := ValidatePanel(Panel{Members: []PanelMember{
		{Label: "x", Provider: "claude", Model: "m"},
		{Label: "x", Provider: "claude", Model: "m"},
	}}); err == nil {
		t.Error("expected error for duplicate label")
	}
	if err := ValidatePanel(Panel{Members: []PanelMember{
		{Label: "a", Provider: "claude", Model: "m"},
		{Label: "b", Provider: "claude", Model: "m"},
	}}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// helper: a string-typed error.
type errString string

func (e errString) Error() string { return string(e) }
