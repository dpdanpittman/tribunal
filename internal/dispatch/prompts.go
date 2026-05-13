package dispatch

import "strings"

// BuildSystemPrompt assembles the adversary system prompt for one panel
// member. The base is the verbatim adversary agent definition; the focus
// modifier shapes attention.
func BuildSystemPrompt(adversaryBody, focus string) string {
	base := strings.TrimSpace(adversaryBody)
	mod, ok := focusModifiers[strings.ToLower(focus)]
	if !ok {
		mod = focusModifiers["general"]
	}
	if mod == "" {
		return base
	}
	return base + "\n\n## Focus modifier (this member)\n\n" + mod
}

// focusModifiers are appended to the adversary's base prompt to shape
// attention. They overlap intentionally — a `spec` focus and an `impl`
// focus on the same diff produce different findings, and that variance is
// what the panel buys.
var focusModifiers = map[string]string{
	"general": "",
	"spec": `Concentrate this round's attacks on whether the **specification (intent doc + plan) correctly encodes the user's goal**.
- Look for under-specification (a behavior the spec allows but intent forbids).
- Look for over-specification (the spec demands something the intent doesn't require).
- Look for triviality (the spec is vacuously satisfied).
- Treat the reviewer reports as advisory; the spec itself is your target.`,
	"impl": `Concentrate this round's attacks on whether the **implementation correctly refines the specification**.
- Walk every diff hunk against the spec it claims to implement.
- Look for refinement mismatches — the code goes off-spec but the trio missed it.
- Look for boundary-input behavior the spec covers but the code doesn't.
- Treat the spec as ground truth; the diff is your target.`,
	"temporal": `Concentrate this round's attacks on **temporal properties**: orderings, causality, idempotency, retry semantics, state-transition invariants.
- State-only invariants encoded for temporal properties are a key category to surface.
- Look for "must have happened before" relationships that are unenforced.
- Look for invariants that hold *at* each state but not *across* sequences.`,
	"security": `Concentrate this round's attacks on **security and trust boundaries**.
- Auth bypasses, missing authorization, time-of-check-time-of-use, race conditions in access control.
- Untrusted input flowing into trusted domains without validation.
- Secrets leaving their assumed compartment (logs, error messages, trace data).`,
	"perf": `Concentrate this round's attacks on **performance and reliability**.
- Hot-path complexity introduced beyond what the plan calls for.
- Unbounded memory or goroutine growth.
- Missing timeouts on remote operations.
- Degraded-mode behavior the plan declared but the implementation doesn't actually deliver.`,
}

// BuildUserPrompt assembles the user-message portion of the attack
// prompt from the named artifacts. The result is a single string; the
// orchestrator never sends multipart messages.
//
// Order matters: intent first (highest authority), plan next, diff after,
// reviewer reports last so the model has the relevant context structure
// in mind by the time it reads the consensus to attack.
func BuildUserPrompt(intent, plan, diff string, reviewerReports map[string]string) string {
	var b strings.Builder
	b.WriteString("Attack the proposed approval. Find what the trio missed.\n\n")
	b.WriteString("=== INTENT ===\n")
	b.WriteString(intent)
	b.WriteString("\n=== END INTENT ===\n\n")

	b.WriteString("=== PLAN ===\n")
	b.WriteString(plan)
	b.WriteString("\n=== END PLAN ===\n\n")

	b.WriteString("=== DIFF UNDER REVIEW ===\n")
	b.WriteString(diff)
	b.WriteString("\n=== END DIFF ===\n\n")

	for label, body := range reviewerReports {
		b.WriteString("=== REVIEWER REPORT: ")
		b.WriteString(label)
		b.WriteString(" ===\n")
		b.WriteString(body)
		b.WriteString("\n=== END REVIEWER REPORT ===\n\n")
	}
	b.WriteString("Report per your system prompt. Verdict line first. Be conservative on severity; do not invent attacks.\n")
	return b.String()
}
