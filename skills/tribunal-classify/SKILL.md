---
name: tribunal-classify
description: When a verification layer fails, this skill dispatches `@tribunal-classifier` to route the failure into one of six categories (spec_wrong / code_wrong / prover_stuck / tool_mismatch / state_space_blowup / infrastructure) with grounded evidence. Use whenever a layer of the verification pyramid fails, before deciding what to fix. Do NOT use to fix the failure itself — this skill only routes.
compatibility: Requires the Tribunal CLI + an agent keypair under `.tribunal/agents/`. Targets the Tribunal methodology in any host repo.
metadata:
  version: 1.1.0
  last_updated: 2026-05-19
---

> **Prompt defense baseline:** see `../_shared/prompt-defense.md`.

You are invoking the failure classifier. A failed verification step carries information but not its own interpretation — the same error trace can mean three different things depending on which artifact is at fault. The classifier looks at all the available evidence (failure output, spec, code, intent) and decides which one diverged.

## When to invoke

After any layer of the `tribunal-verify` pyramid reports a failure, before any fix attempt.

## Inputs

- **Failure output** — raw stdout/stderr from the failed tool, plus any structured summary.
- **Specification artifact** — the test, assertion, property, type signature, or annotation being checked.
- **Source under verification** — the code being checked.
- **Intent document** — the human-anchored source of truth.
- **Optional**: prior attack reports from the adversary, related specs, related tests.

If any of the four required inputs is missing, stop and ask.

## Dispatch

Invoke `@tribunal-classifier` with the inputs above. Wait for its classification report.

The classifier returns one of six categories with `low / medium / high` confidence:

- `spec_wrong` — specification doesn't correctly encode intent.
- `code_wrong` — spec correct, code violates it. Real bug.
- `prover_stuck` — both correct, tool can't discharge obligation. Needs scaffolding.
- `tool_mismatch` — wrong layer of pyramid for this property.
- `state_space_blowup` — right tool, abstraction too detailed. Simplify, don't add budget.
- `infrastructure` — build error, missing dep, version mismatch.

`INDETERMINATE` is a valid output when the evidence doesn't decide. The classifier names the missing artifact that would resolve the ambiguity.

## Persistence

Save the classification to `.tribunal/classifications/<layer>-<ISO-timestamp>.md`.

## Acting on the classification

Each category has a default route:

| Category             | Route to                                   | Action hint                                                       |
| -------------------- | ------------------------------------------ | ----------------------------------------------------------------- |
| `spec_wrong`         | `@tribunal-architect` / `@tribunal-pm`     | Identify the specific spec clause to revise.                      |
| `code_wrong`         | `@tribunal-implementer`                    | Identify the buggy location and the intent clause being violated. |
| `prover_stuck`       | `@tribunal-implementer`                    | Add a lemma, hint, decomposition, stronger inductive hypothesis.  |
| `tool_mismatch`      | `@tribunal-architect`                      | Re-assign to the correct pyramid layer.                           |
| `state_space_blowup` | `@tribunal-architect`                      | Reduce detail (symbolize, split spec).                            |
| `infrastructure`     | `@tribunal-implementer` or `@tribunal-ops` | Fix the environment / version pin.                                |

## Examples

### Example 1 — `go test` fails on a property test

Inputs: the failing test name + output, the property file, the source file the property targets, the intent doc section that motivated the property.

Likely outcomes:

- Classifier returns `code_wrong` if the property is well-formed and the source has a real bug → route to `@tribunal-implementer`.
- Classifier returns `spec_wrong` if the property over-specifies what the intent actually requires → route to `@tribunal-architect`.

### Example 2 — Kani run times out

Inputs: kani output (with the time-out marker), the harness file, the source under verification, the intent.

Likely outcome: `state_space_blowup` (right tool, too much state). Do NOT route this to "add more time budget"; the classifier's hint will be to reduce abstraction detail or split the harness.

## Troubleshooting

- **Classifier returns INDETERMINATE without naming a missing artifact** → re-dispatch with the additional context Tribunal already has (the plan doc, residual findings from prior rounds). If still indeterminate, the failure is genuinely ambiguous; surface to the PM and request a manual call.
- **Classifier output looks plausible but routes the wrong direction** → record it anyway. The reputation ledger is how mis-routing gets observed and corrected over time. Don't override on instinct.

## What you do not do

- You do not fix the failure. The classifier routes; the implementer / architect / PM fixes.
- You do not skip the classifier when the failure seems obvious. Consistent routing is the methodology's value.

## Spirit

The pyramid only saves time if failures are routed correctly. A misclassified failure sends an engineer down the wrong rabbit hole — possibly altering correct code to satisfy a wrong spec, the worst case in formal methods. Treat every routing decision as load-bearing.

## Composability

This skill pairs with:

- [`tribunal-verify`](../tribunal-verify/SKILL.md) — upstream; this skill consumes its failure output.
- [`tribunal-implement`](../tribunal-implement/SKILL.md), `tribunal-architect`, `tribunal-pm` — downstream routes per the table above.
