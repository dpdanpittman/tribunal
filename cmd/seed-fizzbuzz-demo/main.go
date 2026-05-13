// Command seed-fizzbuzz-demo populates examples/go-fizzbuzz-verified/.tribunal/
// with deterministic signed ledger entries so a fresh `git clone` shows
// realistic Tribunal output when the user runs `tribunal ledger summary`
// inside the example directory.
//
// The seed is *deterministic* — every keypair is derived from a fixed
// 32-byte seed so successive runs produce identical bytes. Commit the
// output to the repo.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/dpdanpittman/tribunal/internal/agent"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

const exampleDir = "examples/go-fizzbuzz-verified"

// seededAgent is a (label, model, role, seed) tuple that gets a stable
// keypair. The seed bytes are 32 copies of a single byte for readability.
type seededAgent struct {
	Label   string
	Model   string
	Role    agent.Role
	SeedTag byte
}

func keypair(s seededAgent) *agent.Keypair {
	kp, err := agent.NewKeypairFromSeed(bytes.Repeat([]byte{s.SeedTag}, 32))
	if err != nil {
		log.Fatalf("seed %s: %v", s.Label, err)
	}
	return kp
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func main() {
	tribunalDir := filepath.Join(exampleDir, ".tribunal")
	ledgerPath := filepath.Join(tribunalDir, "ledger.jsonl")

	// Start fresh so the seed is reproducible.
	if err := os.RemoveAll(ledgerPath); err != nil && !os.IsNotExist(err) {
		log.Fatal(err)
	}
	for _, sub := range []string{"findings", "resolutions", "reports/P-fizzbuzz"} {
		if err := os.MkdirAll(filepath.Join(tribunalDir, sub), 0o755); err != nil {
			log.Fatal(err)
		}
	}

	// Agents — deterministic keys.
	pm := seededAgent{"claude-pm", "claude-opus-4-7", agent.RoleProjectManager, 0x01}
	rArch := seededAgent{"claude-reviewer-arch", "claude-opus-4-7", agent.RoleReviewerArch, 0x02}
	rSec := seededAgent{"claude-reviewer-sec", "claude-opus-4-7", agent.RoleReviewerSec, 0x03}
	rPerf := seededAgent{"claude-reviewer-perf", "claude-opus-4-7", agent.RoleReviewerPerf, 0x04}
	adv := seededAgent{"claude-adversary", "claude-opus-4-7", agent.RoleAdversary, 0x05}
	qa := seededAgent{"claude-qa", "claude-opus-4-7", agent.RoleQA, 0x06}

	kpPM := keypair(pm)
	kpArch := keypair(rArch)
	kpSec := keypair(rSec)
	kpPerf := keypair(rPerf)
	kpAdv := keypair(adv)
	kpQA := keypair(qa)

	l := ledger.New(ledgerPath)

	// Two timestamps spread across a day so reputation math has a window.
	t0 := time.Date(2026, 5, 12, 14, 0, 0, 0, time.UTC)

	type findInput struct {
		id       string
		kp       *agent.Keypair
		label    string
		role     agent.Role
		severity ledger.Severity
		category ledger.Category
		claim    string
		ageMin   int
	}

	finds := []findInput{
		{"F-001", kpArch, rArch.Label, rArch.Role, ledger.SeverityWarning, ledger.CategoryCoverageGap,
			"intent.md doesn't list `math.MinInt` as a boundary; FizzBuzz(math.MinInt) would panic via the < 0 path but it's not in §2 Behaviors.", 30},
		{"F-002", kpSec, rSec.Label, rSec.Role, ledger.SeveritySuggestion, ledger.CategoryHiddenAssumption,
			"Panic-on-negative is intentional but undocumented at the boundary — main.go doesn't recover, so a caller passing -1 from CLI args will crash the process without a useful error message.", 45},
		{"F-003", kpPerf, rPerf.Label, rPerf.Role, ledger.SeveritySuggestion, ledger.CategoryAdversarialInput,
			"Fuzz harness skips negative inputs but doesn't exercise large positive inputs near math.MaxInt. Acceptable for v1 but worth a follow-up.", 60},
		{"F-004", kpAdv, adv.Label, adv.Role, ledger.SeverityCritical, ledger.CategorySharedBlindSpot,
			"None of the lens reviewers caught the fact that intent.md §3.2 lists FOUR invariants but TestFizzBuzzInvariants only checks three (the `not divisible` case is implicit, not explicit, and could pass even if FizzBuzz returned `\"0\"` for n=7).", 75},
	}

	// Write findings to the ledger + markdown.
	for _, fi := range finds {
		claimURI := filepath.Join(".tribunal", "findings", fi.id+".md")
		f := &ledger.Finding{
			Kind:        ledger.KindFinding,
			FindingID:   fi.id,
			PlanID:      "P-fizzbuzz",
			Round:       1,
			AgentPubkey: fi.kp.PublicKeyString(),
			AgentLabel:  fi.label,
			Severity:    fi.severity,
			Category:    fi.category,
			ClaimHash:   sha256hex(fi.claim),
			ClaimURI:    claimURI,
			Stake:       fi.severity.DefaultStake(),
			Timestamp:   t0.Add(-time.Duration(fi.ageMin) * time.Minute),
		}
		if err := f.Sign(fi.kp); err != nil {
			log.Fatalf("sign %s: %v", fi.id, err)
		}
		if err := l.AppendFinding(f); err != nil {
			log.Fatalf("append %s: %v", fi.id, err)
		}
		writeFindingMarkdown(tribunalDir, fi.id, fi.label, fi.severity, fi.category, fi.claim)
	}

	// Resolutions: PM marks F-001 and F-004 as TP (legit findings), F-002 stale (it's actually already noted in §4), F-003 indeterminate (defer to follow-up).
	type resInput struct {
		id       string
		outcome  ledger.Outcome
		evidence string
	}
	resolutions := []resInput{
		{"F-001", ledger.OutcomeTruePositive, "intent.md was updated to include math.MinInt as a boundary case under §2.5"},
		{"F-002", ledger.OutcomeStaleDuplicate, "Equivalent point already captured in intent.md §4 NegativeInput failure mode"},
		{"F-003", ledger.OutcomeIndeterminate, "Deferred to follow-up plan P-fizzbuzz-perf; not blocking acceptance"},
		{"F-004", ledger.OutcomeTruePositive, "main_test.go now has explicit assertions for the 'otherwise' case via TestFizzBuzzInvariants"},
	}
	for i, ri := range resolutions {
		var kp *agent.Keypair
		var label string
		if i%2 == 0 {
			kp = kpPM
			label = pm.Label
		} else {
			kp = kpQA
			label = qa.Label
		}
		evidenceURI := filepath.Join(".tribunal", "resolutions", ri.id+".md")
		r := ledger.NewResolution(ri.id, "P-fizzbuzz", ri.outcome, kp, label, sha256hex(ri.evidence), evidenceURI)
		// Compute reward.
		stake := 0
		for _, fi := range finds {
			if fi.id == ri.id {
				stake = fi.severity.DefaultStake()
				break
			}
		}
		r.Reward = ledger.DefaultReward(ri.outcome, stake)
		r.Timestamp = t0.Add(-time.Duration(10) * time.Minute)
		if err := r.Sign(kp); err != nil {
			log.Fatalf("sign res %s: %v", ri.id, err)
		}
		if err := l.AppendResolution(r); err != nil {
			log.Fatalf("append res %s: %v", ri.id, err)
		}
		writeResolutionMarkdown(tribunalDir, ri.id, label, ri.outcome, ri.evidence)
	}

	// status.json — minimal snapshot.
	status := map[string]any{
		"plans": []map[string]any{{
			"id":             "P-fizzbuzz",
			"state":          "Done",
			"intent_path":    "intent.md",
			"plan_path":      "(walked through inline)",
			"working_branch": "main",
			"owner":          "@tribunal-pm",
		}},
		"residual_findings": map[string]any{},
	}
	statusBytes, _ := json.MarshalIndent(status, "", "  ")
	if err := os.WriteFile(filepath.Join(tribunalDir, "status.json"), append(statusBytes, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}

	// Re-verify everything we just wrote.
	if err := l.VerifyAll(); err != nil {
		log.Fatalf("final verify: %v", err)
	}

	// Summary.
	findings, resolutionsRead, err := l.All()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("✓ Seeded %s\n", ledgerPath)
	fmt.Printf("  Findings:    %d\n", len(findings))
	fmt.Printf("  Resolutions: %d\n", len(resolutionsRead))
	fmt.Printf("\nRun `tribunal ledger summary` inside %s to see per-agent reputation.\n", exampleDir)
}

func writeFindingMarkdown(tribunalDir, id, label string, sev ledger.Severity, cat ledger.Category, claim string) {
	body := fmt.Sprintf(`# Finding %s

**Author:** %s
**Severity:** %s
**Category:** %s

## Claim

%s

## Suggested defense

(See ledger entry + report in .tribunal/reports/P-fizzbuzz/ for context.)
`, id, label, sev, cat, claim)
	path := filepath.Join(tribunalDir, "findings", id+".md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		log.Fatal(err)
	}
}

func writeResolutionMarkdown(tribunalDir, id, label string, outcome ledger.Outcome, evidence string) {
	body := fmt.Sprintf(`# Resolution for %s

**Resolver:** %s
**Outcome:** %s

## Evidence

%s
`, id, label, outcome, evidence)
	path := filepath.Join(tribunalDir, "resolutions", id+".md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		log.Fatal(err)
	}
}
