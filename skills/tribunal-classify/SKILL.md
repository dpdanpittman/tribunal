---
name: tribunal-classify
description: When a verification layer fails, this skill dispatches `@tribunal-classifier` to route the failure into one of six categories (spec_wrong / code_wrong / prover_stuck / tool_mismatch / state_space_blowup / infrastructure) with grounded evidence. Use whenever a layer of the pyramid fails, before deciding what to fix.
---

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

## What you do not do

- You do not fix the failure. The classifier routes; the implementer / architect / PM fixes.
- You do not skip the classifier when the failure seems obvious. Consistent routing is the methodology's value.

## Spirit

The pyramid only saves time if failures are routed correctly. A misclassified failure sends an engineer down the wrong rabbit hole — possibly altering correct code to satisfy a wrong spec, the worst case in formal methods. Treat every routing decision as load-bearing.
