# Convergence

> _"The unit of trust is not a single review pass — it is **adversarial pressure that no longer changes the artifact**."_

This document extends the methodology with a second-order property: **convergence**. A single-pass review tells you what's wrong right now. A converging review tells you when you're done. Tribunal's release process should be the second thing.

## What single-pass review covers

The methodology shipped in v0.1 — v0.3 covers a single review cycle:

```
intent → plan → implement → trio (parallel lens) → adversary → verify → settle
```

That cycle produces a signed verdict (`Approve` / `Request Changes` / `Escalate`) and an on-chain reputation impact. It's exactly the right primitive for a PR review.

But it has no opinion about whether to ship. A "Request Changes" verdict tells the PM that fixes are needed; the PM then ships fixes; somebody runs the next review pass; the verdict might be clean now, or there might be a new defect. The decision to release lives outside the methodology, in the operator's head.

## What convergence covers

A converging review runs the single-pass loop _repeatedly_ until the artifact reaches a **fixed point under adversarial pressure**: a state where introducing fresh adversarial review no longer produces material findings.

The release ships at convergence — not before, not on a schedule, not because the trio approved one round.

```
round 0: trio + adversary → findings₀ → implementer fixes → diff₁
round 1: trio + adversary → findings₁ → implementer fixes → diff₂
round 2: trio + adversary → findings₂ (✓ clean) → release
```

The signal that the release is ready isn't "the trio approved." It's "another round of adversaries with rotated composition couldn't find anything more."

## Why this matters

Tribunal observed three audit cycles on its own code (P-v031-audit → v0.3.1, P-v032-audit → v0.3.2, P-v033-audit → v0.3.3). Each cycle's fix introduced a structurally-similar defect that the next cycle caught. The adversary in P-v033-audit named the pattern: each fix is a narrower version of the same primitive, and the methodology asymptotically approaches the contract's true error grammar without ever reaching it.

That's a real failure mode for a methodology. The fix isn't a tighter primitive — it's a **release-gating loop that doesn't let the next version ship until adversarial review stops finding things**. A single-pass methodology converges to whatever the adversary catches on round 0; a converging methodology converges to whatever the adversary catches after N rounds with rotated composition.

## The three possible outcomes

Treating the loop as a function, the convergence question has three answers:

1. **Converges.** The methodology is a fixed-point. Each release stabilizes within finite rounds. Time-to-converge becomes the cost metric for a release. _This is the desired regime._
2. **Oscillates.** Fix A introduces defect B; fix B re-introduces defect A. A coverage check that prevents revisiting prior states is needed. The on-chain finding history (`committed_at` per finding) makes this detectable.
3. **Diverges.** Every fix introduces a smaller defect forever (Zeno's adversary). The MAX_ROUNDS escape valve catches it; the deeper diagnosis is severity-floor filtering — stop fighting over Suggestions and let the cycle complete on Critical+Warning closure.

## The load-bearing detail: panel rotation

A converging loop only converges if **the panel composition meaningfully changes between rounds**.

If round 2's adversary panel is identical to round 1's, it'll either re-find the same defects (no progress) or pass when it shouldn't (false convergence — the original blind spot persists). The convergence theorem holds only when each round introduces independent adversarial lenses.

Tribunal's existing diversity-bucket configuration in `tribunal.yaml` is exactly the right primitive for this. The convergence controller rotates across the configured axes per round:

- `vendor_family` — Claude / GPT / Gemini / local
- `temperature_band` — 0 / 0.7 / 1.0
- `focus` — spec / impl / temporal / security / performance
- `model_tier` — opus / sonnet / haiku
- `composite:axis1,axis2,...` — combinations

Round N's panel is selected to maximize distance from rounds 0..N-1 along the configured axis. The audit ledger records the panel composition per round so future audits can verify diversity was real and not nominal.

## Stopping criteria

The controller supports several stopping criteria; the operator picks one (or several, AND'd) per plan:

- `consecutive-clean(n)` — N back-to-back rounds with zero Critical and zero unresolved Warning. Strictest.
- `no-novel-findings` — the next round only re-discovers findings already in the ledger for this plan (carry-forwards). Indicates the methodology has explored the artifact's surface.
- `adversary-explicit-pass` — the adversary explicitly returns `Verdict: Pass` with a concrete justification, citing the panel composition diversity.
- `severity-floor(suggestion)` — stop fighting over Suggestions; converge on closure of Critical + Warning only.
- `max-rounds(N)` — escape valve; the controller bails after N rounds regardless of convergence state. Always wired alongside the others.

The operator can configure per-plan in the Assignment header (e.g., `Stopping: consecutive-clean(2), max-rounds(5)`).

## Role separation in the loop

A single review pass involves four roles (PM, reviewer-arch/sec/perf, adversary, implementer). The convergence loop requires that the **implementer role is genuinely separate from the reviewer/adversary roles**. Otherwise the same agent's same blind spot writes the fix that the same agent's same blind spot then signs off on — which is not convergence, it's circular validation.

The contract's `Role` enum already supports this separation; the convergence loop enforces it by requiring distinct keypairs for the implementer role. The implementer's reputation is settled by whether their fixes survive the NEXT round of adversarial review — a high-quality implementer accumulates reputation for shipping fixes that don't cause regressions; a noisy implementer drops weight on each regression.

## Reputation feedback inside a convergence cycle

Across cycles, reputation accumulates per agent. _Inside_ a cycle, the loop adds an intermediate signal: per-round.

If an adversary in round 3 files a finding that round 4 marks as `stale` (already fixed) or `false_positive`, that adversary's reputation drops within the same convergence cycle. This is the feedback layer that prevents an adversary from gaming the loop by re-filing the same finding under variant claim hashes. The loop teaches the panel which adversaries are noisy IN THE CONTEXT OF THIS RELEASE, not just across releases.

## What this implies for the contract

The contract surface today supports register, rotate, commit_finding_batch, resolve_finding_batch, and several queries. The convergence controller needs at most one additional capability — a structured query returning the post-state of a batch — and Tribunal's own audit (F-NEW-403) named this as the next-most-needed primitive anyway.

No other contract changes are required to support convergence. The controller is entirely a Go-side feature.

## The F-NEW-403 lesson

P-v033-audit's adversary named a general design principle observed across three audit cycles:

> _"Each fix is a more precise version of the same primitive (parse-the-LCD's-error-string). Each version is narrower than the contract's true error grammar. The methodology will keep finding gaps as long as we stay on this primitive."_

The lesson, generalized: **when convergence stalls, look for the primitive the implementer keeps refining**. The fix is usually not a tighter version of that primitive — it's a different primitive that doesn't have the same input-space-vs-implementation-surface mismatch.

In v0.3.X's case: stop parsing the contract's error text; query the contract's state instead. That replaces a primitive whose domain is "all possible error strings the contract might emit" (open-ended, evolves with contract changes) with a primitive whose domain is "the structured response of a query" (closed, stable across contract versions).

A converging methodology surfaces this kind of architectural lesson naturally — by showing the implementer the same shape of defect three times.

## Implementation plan

Convergence is **v0.4 scope**. Tracked separately at `docs/adr/0001-convergence-controller.md`. The implementation breaks into three milestones:

1. **`tribunal converge --plan X --diff Y`** — drives the loop, output only, no auto-apply. Operator manually applies the implementer's suggested fix between rounds.
2. **Implementer interface** — pluggable agent that authors fixes between rounds based on the prior round's findings. Initial impl: Claude provider, given findings + diff + intent, produces a patch.
3. **Auto-apply mode** — `--auto` flag; the controller applies implementer patches and re-runs the loop without human intervention. Bounded by `--max-rounds`, `--max-tokens`, `--max-wallclock`. The dangerous regime; ships last with extensive testing.

Until then, the methodology operates in single-pass mode and convergence is achieved by manually iterating.

## Related

- [`methodology.md`](./methodology.md) — the single-pass methodology this extends.
- [`incentive-mechanism.md`](./incentive-mechanism.md) — how reputation feedback works (the static layer; convergence adds the dynamic layer).
- [`on-chain-protocol.md`](./on-chain-protocol.md) — the contract surface.
- [`adr/0001-convergence-controller.md`](./adr/0001-convergence-controller.md) — implementation ADR.
- `.tribunal/reports/P-v033-audit/SYNTHESIS.md` — the empirical observation that motivated this document.
