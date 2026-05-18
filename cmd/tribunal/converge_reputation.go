package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dpdanpittman/tribunal/internal/agent"
	"github.com/dpdanpittman/tribunal/internal/converge"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

// ledgerReputationSink is the production converge.ReputationSink. Given
// the agent.Registry and the project root, it auto-registers two
// keypairs the first time it's invoked — the implementer's own keypair
// (role=Implementer) and a system convergence-verifier (role=QA, the
// only role besides PM that can sign Resolutions). It then writes a
// synthetic Finding for every authored patch and an auto-Resolution
// whenever the verify gate has produced a verdict.
//
// The entries land in `.tribunal/ledger.jsonl` like any other Tribunal
// finding/resolution. They flow on-chain via `tribunal chain sync` if
// the operator has registered the implementer + verifier agents on-chain
// via `tribunal chain register <label>`. v0.4.5 doesn't auto-register
// on-chain — the local feedback loop ships first; on-chain settlement
// is an explicit operator step (same as the rest of the chain layer).
type ledgerReputationSink struct {
	ProjectRoot string
	Registry    *agent.Registry
}

func (s *ledgerReputationSink) RecordImplementerOutcome(ctx context.Context, o converge.ImplementerOutcome) error {
	if o.Refused {
		return nil
	}
	if o.PatchHash == "" && o.PatchError == "" {
		// Nothing actionable to record.
		return nil
	}

	// Resolve / auto-register the implementer keypair.
	implKP, _, err := s.resolveOrAdd(o.ImplementerLabel, "convergence-implementer", agent.RoleImplementer)
	if err != nil {
		return fmt.Errorf("implementer keypair: %w", err)
	}

	// Build the synthetic Finding the implementer "files" as a claim.
	severity := pickReputationSeverity(o.Severities)
	claimHash := o.PatchHash
	if claimHash == "" {
		// Patch never landed; use a stable hash over the error message
		// so duplicate sync invocations don't fork the entry.
		claimHash = "sha256:apply-failed:" + shortHashOfString(o.PatchError)
	}
	findingID := fmt.Sprintf("IMPL-%s-%04d", o.PlanID, o.Round)
	claimURI := filepath.Join(".tribunal", "convergence", o.PlanID, fmt.Sprintf("round-%04d-patch.diff", o.Round))

	f := ledger.NewFinding(findingID, o.PlanID, o.Round, implKP, o.ImplementerLabel,
		severity, ledger.CategoryRefinementMismatch, claimHash, claimURI)
	if err := f.Sign(implKP); err != nil {
		return fmt.Errorf("sign implementer finding: %w", err)
	}
	lg := ledger.New(filepath.Join(s.ProjectRoot, ".tribunal", "ledger.jsonl"))
	if err := lg.AppendFinding(f); err != nil {
		return fmt.Errorf("append implementer finding: %w", err)
	}

	// Auto-resolve when the outcome is terminal (verify ran OR patch
	// failed to apply at all). Inconclusive M2 outcomes (no verify,
	// successful apply) await manual operator resolution.
	if !o.NeedsResolution() {
		return nil
	}
	verifierKP, _, err := s.resolveOrAdd("convergence-verifier", "convergence-verifier", agent.RoleQA)
	if err != nil {
		return fmt.Errorf("verifier keypair: %w", err)
	}
	outcome := ledger.OutcomeFalsePositive
	if o.IsTruePositive() {
		outcome = ledger.OutcomeTruePositive
	}
	evidenceHash := "sha256:" + shortHashOfString(o.VerifySummary+"|"+o.PatchError)
	evidenceURI := claimURI
	if !o.IsTruePositive() {
		// On failure, the patch readme captures the reasoning + the
		// verify summary captures the why-it-broke. Both useful evidence.
		evidenceURI = filepath.Join(".tribunal", "convergence", o.PlanID, fmt.Sprintf("round-%04d-patch.md", o.Round))
	}
	r := ledger.NewResolution(findingID, o.PlanID, outcome, verifierKP, "convergence-verifier",
		evidenceHash, evidenceURI)
	if err := r.Sign(verifierKP); err != nil {
		return fmt.Errorf("sign auto-resolution: %w", err)
	}
	if err := lg.AppendResolution(r); err != nil {
		return fmt.Errorf("append auto-resolution: %w", err)
	}
	return nil
}

// resolveOrAdd looks up an agent by exact label, falling back to a
// second probe label, and auto-creates with the given role when neither
// exists. Returns the keypair + the label actually used.
func (s *ledgerReputationSink) resolveOrAdd(label, fallbackLabel string, role agent.Role) (*agent.Keypair, string, error) {
	for _, candidate := range []string{label, fallbackLabel} {
		if candidate == "" {
			continue
		}
		existing, err := s.Registry.Get(candidate)
		if err == nil {
			kp, err := s.Registry.LoadKeypair(existing.Label)
			if err != nil {
				return nil, "", err
			}
			return kp, existing.Label, nil
		}
	}
	chosen := label
	if chosen == "" {
		chosen = fallbackLabel
	}
	a, err := s.Registry.Add(chosen, "n/a", role)
	if err != nil {
		return nil, "", err
	}
	kp, err := s.Registry.LoadKeypair(a.Label)
	if err != nil {
		return nil, "", err
	}
	return kp, a.Label, nil
}

// pickReputationSeverity picks a severity for the synthetic implementer
// finding. Highest severity among the findings the patch was authored
// to address; defaults to warning when the input is empty.
//
// Rationale: the implementer's reputation should track the value of the
// work, not just the count. A critical-finding fix that ships clean
// should accumulate more reputation than a suggestion fix.
func pickReputationSeverity(severities []string) ledger.Severity {
	best := ledger.SeverityWarning
	bestRank := severityRankLocal(string(best))
	for _, s := range severities {
		r := severityRankLocal(s)
		if r > bestRank {
			bestRank = r
			best = ledger.Severity(strings.ToLower(s))
		}
	}
	if !best.IsValid() {
		return ledger.SeverityWarning
	}
	return best
}

// severityRankLocal mirrors the rank table elsewhere in this package
// (see converge.go) without exporting the helper. Higher = more severe.
func severityRankLocal(s string) int {
	switch strings.ToLower(s) {
	case "critical":
		return 3
	case "warning", "serious":
		return 2
	case "suggestion", "cosmetic":
		return 1
	}
	return 0
}

// shortHashOfString — a stable short identifier used in claim_hash /
// evidence_hash construction. Keeps the strings ≤ MaxHashLen for the
// contract's validate_id_field check without pulling in sha256
// directly here (controller.go already imports it; this side keeps
// dependencies minimal).
func shortHashOfString(s string) string {
	// Truncated FNV-style mix is enough for stable id derivation; not
	// cryptographic. The on-chain validator only checks length + char
	// class, so anything stable + ASCII works.
	var h uint64 = 1469598103934665603
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return fmt.Sprintf("%016x", h)
}
