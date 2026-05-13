---
name: tribunal-plan
description: Produce a Tribunal plan from a completed intent doc. The plan covers technical approach, module/interface contracts, risk register, verification plan, and a `tasks` decomposition with completion criteria. Use after `tribunal-intent`, before any code lands.
---

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
