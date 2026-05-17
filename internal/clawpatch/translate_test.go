package clawpatch_test

import (
	"strings"
	"testing"

	"github.com/dpdanpittman/tribunal/internal/agent"
	"github.com/dpdanpittman/tribunal/internal/clawpatch"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

func TestLensBucket(t *testing.T) {
	tests := []struct {
		category string
		want     clawpatch.Lens
	}{
		{"security", clawpatch.LensSec},
		{"SECURITY", clawpatch.LensSec}, // case-insensitive
		{"performance", clawpatch.LensPerf},
		{"concurrency", clawpatch.LensPerf},
		{"bug", clawpatch.LensArch},
		{"api-contract", clawpatch.LensArch},
		{"data-loss", clawpatch.LensArch},
		{"test-gap", clawpatch.LensArch},
		{"docs-gap", clawpatch.LensArch},
		{"build-release", clawpatch.LensArch},
		{"maintainability", clawpatch.LensArch},
		{"unknown-category-future", clawpatch.LensArch}, // defaults to arch
		{"", clawpatch.LensArch},
	}
	for _, tt := range tests {
		t.Run(tt.category, func(t *testing.T) {
			got := clawpatch.LensBucket(tt.category)
			if got != tt.want {
				t.Errorf("LensBucket(%q) = %q; want %q", tt.category, got, tt.want)
			}
		})
	}
}

func TestSeverityMap(t *testing.T) {
	tests := []struct {
		in      string
		want    ledger.Severity
		wantErr bool
	}{
		{"critical", ledger.SeverityCritical, false},
		{"CRITICAL", ledger.SeverityCritical, false},
		{"high", ledger.SeverityWarning, false},
		{"medium", ledger.SeverityWarning, false},
		{"low", ledger.SeveritySuggestion, false},
		{"info", "", true},
		{"", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := clawpatch.SeverityMap(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("SeverityMap(%q) error = %v; wantErr=%v", tt.in, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("SeverityMap(%q) = %q; want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCategoryMap(t *testing.T) {
	// We don't assert every mapping (the table can shift), only that the
	// returned value is a valid ledger.Category.
	categories := []string{
		"security", "performance", "concurrency", "bug", "api-contract",
		"data-loss", "test-gap", "docs-gap", "build-release", "maintainability",
		"unknown-future",
	}
	for _, c := range categories {
		got := clawpatch.CategoryMap(c)
		if string(got) == "" {
			t.Errorf("CategoryMap(%q) returned empty", c)
		}
	}
}

func TestAgentLabelAndRole(t *testing.T) {
	cases := []struct {
		lens     clawpatch.Lens
		wantLbl  string
		wantRole agent.Role
	}{
		{clawpatch.LensArch, "clawpatch-arch", agent.RoleReviewerArch},
		{clawpatch.LensSec, "clawpatch-sec", agent.RoleReviewerSec},
		{clawpatch.LensPerf, "clawpatch-perf", agent.RoleReviewerPerf},
	}
	for _, tt := range cases {
		t.Run(string(tt.lens), func(t *testing.T) {
			if got := clawpatch.AgentLabel(tt.lens); got != tt.wantLbl {
				t.Errorf("AgentLabel(%q) = %q; want %q", tt.lens, got, tt.wantLbl)
			}
			if got := clawpatch.AgentRole(tt.lens); got != tt.wantRole {
				t.Errorf("AgentRole(%q) = %q; want %q", tt.lens, got, tt.wantRole)
			}
		})
	}
}

func TestToTribunalFinding_ShapeAndSignature(t *testing.T) {
	// Build a synthetic clawpatch finding and a one-off keypair, then
	// translate. Assert the resulting ledger.Finding is signed and shaped
	// correctly.
	kp, err := agent.NewKeypair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	cp := clawpatch.Finding{
		FindingID:      "fnd-abc123",
		FeatureID:      "feat-1",
		Title:          "SQL injection via unescaped slug",
		Category:       "security",
		Severity:       "critical",
		Confidence:     "high",
		Reasoning:      "User-controlled slug is concatenated into a SQL string at db.go:42.",
		Recommendation: "Use prepared statements.",
		CreatedAt:      "2026-05-17T20:00:00Z",
		UpdatedAt:      "2026-05-17T20:00:00Z",
	}
	fnd, err := clawpatch.ToTribunalFinding(cp, "/tmp/repo", "P-test", 1, kp, clawpatch.AgentLabel(clawpatch.LensSec))
	if err != nil {
		t.Fatalf("ToTribunalFinding: %v", err)
	}
	if fnd.Severity != ledger.SeverityCritical {
		t.Errorf("severity = %q; want critical", fnd.Severity)
	}
	if fnd.Signature == "" {
		t.Error("expected signature to be set")
	}
	if !strings.Contains(fnd.FindingID, "clawpatch-sec") {
		t.Errorf("FindingID = %q; expected substring 'clawpatch-sec'", fnd.FindingID)
	}
	if fnd.PlanID != "P-test" {
		t.Errorf("PlanID = %q; want P-test", fnd.PlanID)
	}
	if err := fnd.Verify(); err != nil {
		t.Errorf("signature did not verify: %v", err)
	}
	if fnd.Stake <= 0 {
		t.Errorf("Stake = %d; want >0 for a Critical finding", fnd.Stake)
	}
}

func TestFormatLensReport_EmptyLens(t *testing.T) {
	out := clawpatch.FormatLensReport(clawpatch.LensSec, "P-x", nil)
	if !strings.Contains(out, "No sec-lens findings") {
		t.Errorf("expected empty-lens placeholder; got: %s", out)
	}
}

func TestFormatLensReport_WithFindings(t *testing.T) {
	cp := []clawpatch.Finding{
		{
			FindingID:  "fnd-1",
			Title:      "Missing auth on /admin",
			Category:   "security",
			Severity:   "high",
			Confidence: "high",
			Evidence: []clawpatch.Evidence{
				{Path: "api/admin.go", StartLine: ptr(42), EndLine: ptr(50)},
			},
			Reasoning:      "Admin route does not check the JWT.",
			Recommendation: "Add authMiddleware.",
		},
	}
	out := clawpatch.FormatLensReport(clawpatch.LensSec, "P-x", cp)
	for _, s := range []string{
		"Missing auth on /admin",
		"api/admin.go:42-50",
		"Add authMiddleware",
		"high (clawpatch) → warning (tribunal)",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("expected substring %q in report; full body:\n%s", s, out)
		}
	}
}

func ptr[T any](v T) *T { return &v }
