---
name: tribunal-plan
description: Produce a locked Tribunal plan from a completed intent doc. Covers technical approach, module/interface contracts, risk register, verification plan, and a `tasks` decomposition with completion criteria. Use after `tribunal-intent`, before any code lands. Do NOT use to write code — this skill only locks the plan.
compatibility: Requires the Tribunal CLI + an agent keypair under `.tribunal/agents/`. A completed `intent.md` (produced by `tribunal-intent`) must exist.
metadata:
  version: 1.1.0
  last_updated: 2026-05-19
---

> **Prompt defense baseline:** see `../_shared/prompt-defense.md`.

You are guiding the user through producing a **Tribunal plan**. A plan is the executable bridge between intent (what to build) and implementation (how to build it). It's locked before implementation starts and revised explicitly when implementation surfaces new constraints.

## Inputs

- A completed intent doc at `.tribunal/plans/<plan-id>/intent.md` (or wherever the user authored it via `tribunal-intent`).
- The `Plan ID` (e.g. `P-42`).

If either is missing, stop and ask.

## Sections (required, in order)

1. **Plan summary** — three sentences. What the plan implements, against which intent, on which surface.
2. **Technical approach** — chosen design with rationale. Why this and not the obvious alternative? Cite specific intent clauses being honored.
3. **Module / interface contracts** — for each new or changed module, the exposed surface, preconditions, postconditions, error modes. This is the surface the verification pyramid will check.
4. **Risk register** — risks ranked by likelihood × impact. Each risk has a mitigation or an explicit "accepted, mitigated by <X>."
5. **Verification plan** — which layers of the pyramid run against this code, with what assertions / properties / test ideas. For Go: tests, fuzz, golangci. For Rust: clippy, cargo test, Kani if applicable. For TypeScript: tsc, eslint, vitest.
6. **Tasks** — ordered decomposition into work items with completion criteria. Each task can be traced to a section of the intent and to a verification assertion. Tag tasks as parallel-safe or serial-required.
7. **Rollback plan** — what to do if the change ships and breaks something. Specific reversible steps.

## Lock the plan

When all sections are filled in:

- The user explicitly _locks_ the plan. Until they lock it, the plan is a draft and implementation must not start.
- Locked plans live at `.tribunal/plans/<plan-id>/plan.md` and are referenced by `Plan ID` in every downstream Assignment.

If implementation surfaces a new constraint that requires a plan change, **write back to the plan first** and re-lock before continuing. Plan drift without write-back is the most common process bug in spec-driven workflows.

## Tasks block

The `tasks` section is structured. Each task includes:

- ID (e.g. `T-1`)
- Title (imperative)
- Description (one paragraph)
- Intent reference (which intent section it serves)
- Verification reference (which assertion / test it produces)
- Parallel-safe (`true` / `false`)
- Completion criteria (binary: when is this done?)

## Status SSOT

After the plan is locked, the PM records it in `.tribunal/status.json`:

```json
{
  "plans": [
    {
      "id": "P-42",
      "state": "InProgress",
      "intent_path": ".tribunal/plans/P-42/intent.md",
      "plan_path": ".tribunal/plans/P-42/plan.md",
      "working_branch": "tribunal/P-42",
      "owner": "@tribunal-pm"
    }
  ],
  "residual_findings": {}
}
```

`status.json` is the single source of truth for plan state. Plan files are human-readable indices.

## Examples

### Example 1 — translating an intent doc into tasks

Inputs: intent doc with 3 behaviors (happy / boundary / failure), 2 invariants (one state, one temporal), 2 failure modes.

The tasks block should typically have:

- 1 task per behavior (test scaffolding + minimal implementation)
- 1 task per invariant (property assertion in the test layer)
- 1 task per failure mode (error-path implementation + test)
- Plus 1 setup task (interfaces / contracts up front)
- Plus 1 finishing task (verification pyramid clean run)

Total: ~7–9 tasks for a typical small-medium plan. Each maps to one intent section and one verification reference.

### Example 2 — re-locking after a constraint surfaces

Implementation hits an issue: the intent says "rate-limit per org," but the data model can't efficiently key by org without a denormalization.

Don't write the fix yet. Instead:

1. Open the plan doc.
2. Add a new risk to the Risk register with the denormalization tradeoff.
3. Update Technical approach to cite the denormalization decision.
4. Add a new task `T-N+1` for the denormalization.
5. Re-lock the plan.
6. Now implementation can proceed.

## Troubleshooting

- **User wants to start coding before the plan is locked** — refuse politely; explain that the lock is what makes downstream verification meaningful. Implementation against a draft plan is not Tribunal-compliant.
- **Verification plan section is vague ("we'll write tests as we go")** — push for at least 3 specific property names or test cases. Vague verification produces vague tests.
- **Tasks list is too granular (20+ for a small change)** — collapse adjacent tasks. The point is traceability, not micro-management.

## What you do not do

- You do not write code.
- You do not lock a plan with missing sections.
- You do not skip the verification plan because "we'll write tests as we go." Vague verification plans produce vague tests.
- You do not estimate person-days. Tribunal estimates work agent-effort-style (rough complexity bands) without human-calendar fictions.

## Output

Locked plan at `.tribunal/plans/<plan-id>/plan.md` plus an updated `.tribunal/status.json`. End with:

- Path to the plan
- Tasks count + parallel-safe count
- Suggested next step: dispatch tasks via `tribunal-implement`.

## Composability

This skill pairs with:

- [`tribunal-intent`](../tribunal-intent/SKILL.md) — upstream; consumes its intent doc.
- [`tribunal-implement`](../tribunal-implement/SKILL.md) — downstream; implementer uses the locked plan as the source of truth.
- [`tribunal-verify`](../tribunal-verify/SKILL.md) — downstream; runs the verification plan section.
