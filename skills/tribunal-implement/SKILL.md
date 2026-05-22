---
name: tribunal-implement
description: Cross-role coding behavior baseline for Tribunal implementation work — think before coding, simplicity first, surgical changes, goal-driven execution. Required reading before any implementer / architect / QA / ops role writes code. Use when picking up an Assignment, writing patches, or shipping a fix. Does not override branch policy, review gates, or assignment authority — those are owned by `tribunal-pm` and the methodology.
compatibility: Requires the Tribunal CLI + an agent keypair under `.tribunal/agents/`. Targets the Tribunal methodology in any host repo.
metadata:
  version: 1.1.0
  last_updated: 2026-05-19
---

> **Prompt defense baseline:** see `../_shared/prompt-defense.md`.

# Tribunal Coding Behavior

Lightweight, host-agnostic principles that reduce common agent failure modes. Complements the rest of the methodology; does not override stage gates or role routing.

## Priority order

If two rules conflict, resolve in this order:

1. Explicit user instruction in the current turn.
2. Project `AGENTS.md` / `CLAUDE.md` (if present).
3. This skill (`tribunal-implement`) and `tribunal-review`.
4. Role-specific agent prompts.

## 1. Think before coding

Don't silently choose an interpretation when ambiguity exists.

- State material assumptions before implementing.
- If multiple plausible interpretations exist, present them and ask.
- Surface tradeoffs that affect scope, risk, or maintainability.
- If critical context is missing, pause and clarify.

Self-check: can a reviewer see what assumptions I made? Will the user catch a wrong assumption before large edits land?

## 2. Simplicity first

Implement the minimum that satisfies the request and acceptance criteria.

- Don't add features, flags, or configurability that weren't requested.
- Don't introduce new abstractions for single-use logic.
- Prefer straightforward local fixes over framework-level reshaping.
- Don't add speculative error handling for impossible paths unless project policy demands it.

Default rule: if 200 lines can be 50 with the same behavior and clarity, prefer 50.

## 3. Surgical changes

Every changed line should be traceable to a task.

- Touch only files needed for the requested outcome.
- Don't opportunistically refactor adjacent code.
- Match existing style and patterns unless the request explicitly changes them.
- Remove only artifacts your own change makes unused.
- Report unrelated issues separately, don't piggyback fixes.

Traceability test: each diff hunk maps to a task, an acceptance criterion, or a required fix-up.

## 4. Goal-driven execution

Convert vague requests into verifiable outcomes and iterate until verified.

- Define concrete success criteria before major edits.
- For multi-step tasks, use brief `Step → verify` checkpoints.
- Prefer evidence-backed completion claims (tests, command output, reproducible checks).
- If verification fails, diagnose and fix before declaring completion.

Micro template:

```
1. <Step>
   Verify: <specific check>
2. <Step>
   Verify: <specific check>
3. <Step>
   Verify: <specific check>
```

## Editable-edits discipline

- Read each file from disk before editing it. Stale context is the #1 cause of patch failures.
- If a patch fails to apply, **re-read** before retrying. Don't retry against the same stale anchor.
- For multi-file changes, verify each path before applying.

## Verification before completion

Before claiming `Done`:

- Run the verification layers applicable to the change (at minimum: type-check, lint, tests).
- Quote the actual output in the completion report.
- Don't claim a property holds without evidence.

## Examples

### Example 1 — picking up an Assignment

User says: "Take T-3 from plan P-42 and implement it."

1. Read `intent.md` and `plan.md` from `.tribunal/plans/P-42/` first.
2. Locate T-3 in the plan's tasks block. Note its intent reference + verification reference.
3. State the assumption you'll work under (if any).
4. Implement the minimum that satisfies T-3's completion criteria.
5. Run the verification layers cited in the plan's verification plan.
6. Report `Done` with quoted command output.

### Example 2 — patch fails to apply

`tribunal-implement` says re-read before retrying. Specifically:

```
edit fails → STOP
re-read the file from disk
re-locate the anchor
issue a fresh edit
```

Do not retry against the same stale context. The stale-context retry loop is the most common failure mode in agent coding.

## Troubleshooting

- **Conflicting instructions between this skill and a role-specific prompt** → use the Priority order at the top of this skill. Role-specific prompts come last.
- **"Done" claim rejected by reviewer** → check whether each verification layer's output was actually quoted in your completion report, not just summarized. Reviewers reject paraphrased verification.
- **Patch keeps drifting** — the file is being modified between your reads. Lock with a single Read → Edit → Verify cycle and don't interleave other writes.

## What this skill does not cover

- Branch policy and worktree mandates — that's `tribunal-pm` and the methodology.
- Review gate definitions — that's `tribunal-review`.
- Assignment authority — only PM dispatches subagents.

Use this skill as the _how I write code_ baseline. Use the others as the _what shape the work takes_ skeleton.

## Composability

This skill pairs with:

- [`tribunal-plan`](../tribunal-plan/SKILL.md) — upstream; this skill consumes the locked plan.
- [`tribunal-review`](../tribunal-review/SKILL.md) — downstream; reviews the patches this skill produces.
- [`tribunal-verify`](../tribunal-verify/SKILL.md) — runs alongside; verification layers cited here.
