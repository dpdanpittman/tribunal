package clawpatch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dpdanpittman/tribunal/internal/agent"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

// Lens names the three Tribunal review lenses. Clawpatch findings are
// bucketed into one of these via LensBucket so the existing adversary
// stage (which reads per-lens reports off disk) keeps working unchanged.
type Lens string

const (
	LensArch Lens = "arch"
	LensSec  Lens = "sec"
	LensPerf Lens = "perf"
)

// AllLenses returns the three lens labels in the canonical order Tribunal
// uses (arch / sec / perf).
func AllLenses() []Lens { return []Lens{LensArch, LensSec, LensPerf} }

// LensBucket maps a clawpatch category into Tribunal's three lens labels.
// The mapping is intentionally coarse — a finding that's both a security
// AND a performance concern lands in sec (the higher-priority lens).
//
// Phase 2 will replace this with lens-aware prompts upstream, but for the
// Phase 1 spike a categorical bucket is enough to give the adversary stage
// three readable inputs.
func LensBucket(category string) Lens {
	switch strings.ToLower(category) {
	case "security":
		return LensSec
	case "performance", "concurrency":
		return LensPerf
	default:
		// bug, api-contract, data-loss, test-gap, docs-gap, build-release,
		// maintainability — and anything new clawpatch invents — falls under
		// architecture. arch is the "everything else" bucket.
		return LensArch
	}
}

// SeverityMap converts clawpatch's 4-tier severity (critical/high/medium/low)
// into Tribunal's 3-tier ledger.Severity (critical/warning/suggestion).
//
// The choice to collapse "high" and "medium" into warning is conservative:
// clawpatch's "high" typically means exploitable-but-not-critical, and
// Tribunal's warning already blocks merges, so the merge-block semantics
// survive the translation. "low" maps to suggestion (advisory only).
func SeverityMap(severity string) (ledger.Severity, error) {
	switch strings.ToLower(severity) {
	case "critical":
		return ledger.SeverityCritical, nil
	case "high", "medium":
		return ledger.SeverityWarning, nil
	case "low":
		return ledger.SeveritySuggestion, nil
	default:
		return "", fmt.Errorf("clawpatch: unknown severity %q", severity)
	}
}

// CategoryMap converts a clawpatch category string into Tribunal's
// ledger.Category enum. Categories outside Tribunal's known set become
// the closest semantic match; we don't invent new ledger categories from
// clawpatch values because the adversary, classifier, and reputation
// math reason about that enum.
func CategoryMap(category string) ledger.Category {
	switch strings.ToLower(category) {
	case "security":
		// Clawpatch "security" usually maps to either adversarial-input or
		// hidden-assumption depending on the finding. We choose
		// adversarial_input as the default because it's the most common
		// shape for security findings in clawpatch's output.
		return ledger.CategoryAdversarialInput
	case "performance", "concurrency":
		return ledger.CategoryTemporalStateMismatch
	case "api-contract":
		return ledger.CategoryRefinementMismatch
	case "data-loss":
		return ledger.CategoryEdgeCase
	case "test-gap":
		return ledger.CategoryCoverageGap
	case "docs-gap":
		return ledger.CategoryAmbiguity
	case "build-release":
		return ledger.CategoryCompositionFailure
	case "maintainability":
		return ledger.CategoryOverSpecification
	case "bug":
		// "bug" is too broad to map cleanly; default to edge_case which is
		// the closest "we found something that shouldn't happen" category.
		return ledger.CategoryEdgeCase
	default:
		return ledger.CategoryEdgeCase
	}
}

// ToTribunalFinding turns a clawpatch Finding into a signed ledger.Finding
// ready to be appended. Signing happens here (Tribunal-on-ingest, per
// ADR-0002 decision); the caller does not need to call Sign separately.
//
// claimRoot is the project root used to build the relative ClaimURI.
// planID and round identify the review run. agentLabel is the auto-created
// Tribunal agent (e.g. "clawpatch-sec") that owns the keypair.
func ToTribunalFinding(cp Finding, claimRoot, planID string, round int, kp *agent.Keypair, agentLabel string) (*ledger.Finding, error) {
	sev, err := SeverityMap(cp.Severity)
	if err != nil {
		return nil, err
	}
	cat := CategoryMap(cp.Category)

	// claim_hash deterministically hashes the clawpatch finding ID so two
	// runs that surface the same finding produce identical Tribunal
	// claims (and the ledger deduplicates by hash).
	sum := sha256.Sum256([]byte("clawpatch:" + cp.FindingID))
	claimHash := "sha256:" + hex.EncodeToString(sum[:])

	// findingID encodes plan + lens + clawpatch-id so the human can scan
	// the ledger and tell at a glance which lens emitted what.
	lens := LensBucket(cp.Category)
	findingID := fmt.Sprintf("F-%s-clawpatch-%s-%s", planID, lens, sanitizeID(cp.FindingID))
	claimURI := filepath.Join(".tribunal", "findings", findingID+".md")

	fnd := ledger.NewFinding(findingID, planID, round, kp, agentLabel, sev, cat, claimHash, claimURI)
	if err := fnd.Sign(kp); err != nil {
		return nil, fmt.Errorf("sign clawpatch finding %s: %w", cp.FindingID, err)
	}
	return fnd, nil
}

// AgentLabel returns the Tribunal agent label that owns clawpatch-sourced
// findings for the given lens. Used to auto-register keypairs.
func AgentLabel(lens Lens) string {
	return "clawpatch-" + string(lens)
}

// AgentRole returns the agent role to register for a lens-tagged agent.
// We use the existing lens-specific reviewer roles so reputation math
// treats clawpatch findings the same as skill-trio findings.
func AgentRole(lens Lens) agent.Role {
	switch lens {
	case LensSec:
		return agent.RoleReviewerSec
	case LensPerf:
		return agent.RoleReviewerPerf
	default:
		return agent.RoleReviewerArch
	}
}

// FormatLensReport produces the markdown body Tribunal's adversary stage
// expects under .tribunal/reports/<plan>/<plan>-<lens>-clawpatch.md.
// The format mirrors what the skill-trio reviewers emit so the adversary
// stage doesn't need to know whether the lens reports came from skill
// agents or clawpatch.
func FormatLensReport(lens Lens, planID string, findings []Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Tribunal lens report — %s (via clawpatch)\n\n", lens)
	fmt.Fprintf(&b, "Plan: %s\n\n", planID)
	if len(findings) == 0 {
		fmt.Fprintf(&b, "No %s-lens findings produced by clawpatch.\n", lens)
		return b.String()
	}
	for _, f := range findings {
		sev, _ := SeverityMap(f.Severity)
		fmt.Fprintf(&b, "## %s\n", f.Title)
		fmt.Fprintf(&b, "- **Category**: %s (clawpatch) → %s (tribunal)\n", f.Category, CategoryMap(f.Category))
		fmt.Fprintf(&b, "- **Severity**: %s (clawpatch) → %s (tribunal)\n", f.Severity, sev)
		fmt.Fprintf(&b, "- **Confidence**: %s\n", f.Confidence)
		fmt.Fprintf(&b, "- **Clawpatch ID**: %s\n", f.FindingID)
		if len(f.Evidence) > 0 {
			b.WriteString("- **Evidence**:\n")
			for _, e := range f.Evidence {
				loc := e.Path
				if e.StartLine != nil {
					loc = fmt.Sprintf("%s:%d", e.Path, *e.StartLine)
					if e.EndLine != nil && *e.EndLine != *e.StartLine {
						loc = fmt.Sprintf("%s-%d", loc, *e.EndLine)
					}
				}
				fmt.Fprintf(&b, "  - `%s`", loc)
				if e.Symbol != nil && *e.Symbol != "" {
					fmt.Fprintf(&b, " (`%s`)", *e.Symbol)
				}
				b.WriteString("\n")
			}
		}
		if f.Reasoning != "" {
			fmt.Fprintf(&b, "\n%s\n\n", f.Reasoning)
		}
		if f.Recommendation != "" {
			fmt.Fprintf(&b, "**Recommendation**: %s\n\n", f.Recommendation)
		}
		b.WriteString("---\n\n")
	}
	return b.String()
}

// sanitizeID makes a clawpatch finding ID safe for use in a filesystem
// path component. Clawpatch IDs are usually already path-safe but defense
// in depth costs nothing here.
func sanitizeID(s string) string {
	r := strings.NewReplacer(
		"/", "-",
		"\\", "-",
		":", "-",
		" ", "-",
		"\t", "-",
		"\n", "-",
	)
	return r.Replace(s)
}
