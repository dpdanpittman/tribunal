package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dpdanpittman/tribunal/internal/agent"
	"github.com/dpdanpittman/tribunal/internal/dispatch"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

// stubProvider returns a configured Report regardless of inputs. Used to
// drive the orchestration without making real HTTP calls.
type stubProvider struct {
	name string
	body string
}

func (p *stubProvider) Name() string { return p.name }

func (p *stubProvider) Attack(_ context.Context, member dispatch.PanelMember, _, _ string) (*dispatch.Report, error) {
	verdict, reason, findings := dispatch.ParseReport(p.body)
	return &dispatch.Report{
		Member:   member,
		Verdict:  verdict,
		Reason:   reason,
		Findings: findings,
		RawText:  p.body,
	}, nil
}

func TestReviewRunWritesReportsAndLedger(t *testing.T) {
	root := t.TempDir()
	tribunalDir := filepath.Join(root, ".tribunal")
	planDir := filepath.Join(tribunalDir, "plans", "P-42")
	reportsDir := filepath.Join(tribunalDir, "reports", "P-42")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(reportsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "intent.md"), []byte("# Intent\nMust handle FizzBuzz."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "plan.md"), []byte("# Plan\nSingle pure function."), 0o644); err != nil {
		t.Fatal(err)
	}
	// Three reviewer reports.
	for _, role := range []string{"arch", "sec", "perf"} {
		path := filepath.Join(reportsDir, "P-42-reviewer-"+role+".md")
		if err := os.WriteFile(path, []byte("# QC reviewer "+role+" report\nApprove."), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Write a tribunal.yaml that pins the panel to one stub-provider member.
	yaml := `
adversary:
  default_panel:
    - { label: stub-adv, provider: stub, model: stub-1, temperature: 0, focus: spec }
`
	if err := os.WriteFile(filepath.Join(root, "tribunal.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	// Dispatch registry with the stub provider returning a BREAKS report.
	provider := &stubProvider{
		name: "stub",
		body: `VERDICT: BREAKS

### Finding 1 — shared_blind_spot
- Category: shared_blind_spot
- Severity: critical
- Scenario: All three reviewers approved without exercising the empty-input path.
- Suggested defense: Add an explicit empty-input test.
`,
	}
	reg := dispatch.NewRegistry()
	reg.Register(provider)

	// Use a temp agent registry so the test doesn't touch the user's ~/.tribunal.
	agentReg := agent.NewRegistry(filepath.Join(root, "agents-home"))

	in, err := FindInputs(root, "P-42", "")
	if err != nil {
		t.Fatal(err)
	}
	if in.IntentPath == "" {
		t.Fatal("intent path not resolved")
	}
	if len(in.ReviewerReports) != 3 {
		t.Errorf("expected 3 reviewer reports, got %d", len(in.ReviewerReports))
	}

	result, err := Run(context.Background(), Options{
		ProjectRoot:   root,
		PlanID:        "P-42",
		PanelName:     "default",
		BucketSpec:    "composite:vendor_family,focus",
		AdversaryBody: "You are the Tribunal adversary.",
		WriteToLedger: true,
		AutoRegister:  true,
	}, in, reg, agentReg)
	if err != nil {
		t.Fatal(err)
	}
	if result.OverallVerdict != dispatch.VerdictBreaks {
		t.Errorf("expected BREAKS, got %s", result.OverallVerdict)
	}
	if len(result.LedgerFindings) != 1 {
		t.Fatalf("expected 1 ledger finding, got %d (skipped: %v)", len(result.LedgerFindings), result.Skipped)
	}
	if result.LedgerFindings[0].Severity != ledger.SeverityCritical {
		t.Errorf("severity = %q, want critical", result.LedgerFindings[0].Severity)
	}

	// Per-member report file should exist.
	expectReport := filepath.Join(reportsDir, "P-42-adversary-stub-adv.md")
	if _, err := os.Stat(expectReport); err != nil {
		t.Errorf("expected per-member report at %s: %v", expectReport, err)
	}
	// Synthesis JSON should exist.
	expectSyn := filepath.Join(reportsDir, "P-42-adversary-synthesis.json")
	if _, err := os.Stat(expectSyn); err != nil {
		t.Errorf("expected synthesis at %s: %v", expectSyn, err)
	}
	// Ledger should have one entry verifiable.
	l := ledger.New(filepath.Join(tribunalDir, "ledger.jsonl"))
	if err := l.VerifyAll(); err != nil {
		t.Errorf("ledger verify failed: %v", err)
	}
	findings, _, err := l.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 ledger entry, got %d", len(findings))
	}
	// Finding-text URI should resolve.
	findingTextPath := filepath.Join(root, findings[0].ClaimURI)
	body, err := os.ReadFile(findingTextPath)
	if err != nil {
		t.Errorf("finding markdown not written: %v", err)
	}
	if !strings.Contains(string(body), "shared_blind_spot") {
		t.Errorf("finding markdown missing category: %s", body)
	}
}

func TestFindInputsTolerantOfMissingArtifacts(t *testing.T) {
	root := t.TempDir()
	in, err := FindInputs(root, "P-nothing", "")
	if err != nil {
		t.Fatal(err)
	}
	if in.IntentPath != "" || in.PlanPath != "" {
		t.Errorf("expected empty paths on missing artifacts: %+v", in)
	}
	if len(in.ReviewerReports) != 0 {
		t.Errorf("expected no reviewer reports on missing artifacts")
	}
}
