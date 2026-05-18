# ADR-0001: Convergence Controller

**Status:** Implemented — M1 in v0.4.1 (2026-05-17); M2 / M3 still scheduled.
**Date:** 2026-05-13 (proposed) → 2026-05-17 (M1 shipped)
**Driver:** P-v033-audit, F-NEW-403 — observed three audit cycles producing structurally-similar defects without converging.

## Context

Tribunal's single-pass review (v0.1 — v0.3) produces a signed verdict per cycle. It has no opinion about when to ship. After three observed audit cycles on Tribunal's own code, the adversary in P-v033-audit demonstrated empirically that single-pass review **does not converge to a stable release** — each fix introduces a structurally-similar defect that the next cycle catches.

The release process needs a second-order primitive: a controller that drives the single-pass loop _repeatedly_ until convergence under rotated adversarial pressure.

The methodology doc for the convergence property lives at `docs/convergence.md`. This ADR captures the implementation direction for v0.4.

## Decision

Add an `internal/converge` package and a `tribunal converge` CLI verb that drives a multi-round audit loop. Implementation phased across three milestones; the first two ship in v0.4, the third later.

### Package shape

```
internal/converge/
  controller.go     // the loop driver
  round.go          // RoundResult, RoundLedger
  rotator.go        // PanelRotator interface + default impls
  stopping.go       // StoppingCriterion interface + default impls
  budget.go         // BudgetTracker (rounds, tokens, wallclock)
  implementer.go    // Implementer interface (M2)
```

### Public API

```go
// Controller orchestrates a convergence loop.
type Controller struct {
    Sync        *chain.Sync     // for reading on-chain finding history
    Reviewer    ReviewerSet     // dispatches the trio per round
    Adversary   AdversaryStage  // dispatches the adversary panel per round
    Rotator     PanelRotator    // selects panel composition per round
    Stopping    []StoppingCriterion
    Budget      BudgetTracker
    Implementer Implementer    // M1: nil → output-only; M2: real
}

// Run drives the loop until a stopping criterion fires or the budget is exhausted.
func (c *Controller) Run(ctx context.Context, plan ConvergenceTarget) (*ConvergenceResult, error)

// PanelRotator selects the adversary panel for round N given the history.
type PanelRotator interface {
    NextPanel(round int, history []PanelComposition, config *DispatchConfig) (PanelComposition, error)
}

// StoppingCriterion decides whether the loop should stop after a round.
type StoppingCriterion interface {
    ShouldStop(rounds []RoundResult) (stop bool, reason string)
}

// Implementer authors a fix between rounds.
type Implementer interface {
    Patch(ctx context.Context, in PatchInput) (PatchOutput, error)
}
```

### Milestones

**M1 — output-only loop (shipped v0.4.1 at 2026-05-17)**

The controller runs the trio + adversary per round, records the round's findings, evaluates stopping criteria, emits a `ConvergenceResult` summary. **It does not author fixes.** Between rounds, the operator applies the implementer's role manually and re-runs `tribunal converge` (which picks up where it left off via the on-chain history).

Flags:

- `--plan <id>` — the plan to converge on
- `--diff <range>` — what to review (initial round; subsequent rounds re-read from the working tree)
- `--max-rounds <N>` — escape valve, default 5
- `--max-tokens <N>` — token budget cap, default 200_000
- `--max-wallclock <duration>` — wallclock cap, default 30m
- `--severity-floor <level>` — stop fighting over Suggestions only (`critical|warning|suggestion`, default `warning`)
- `--rotation <strategy>` — panel rotation scheme (default `composite:vendor_family,focus`)
- `--stop-on <criterion>` — comma-separated; default `consecutive-clean(2)`

**M2 — Implementer interface (v0.4.1+)**

Define `Implementer` and provide a Claude implementation: given findings + diff + intent, returns a patch. The controller can be invoked with `--implementer=claude-opus-4-7` to delegate fix authoring to an LLM. Patches are presented for human approval by default; `--auto-apply` skips approval.

**M3 — Auto-apply (later)**

The most dangerous regime. Controller runs the loop end-to-end without human intervention: dispatch, review, patch, apply, re-dispatch, until convergence or budget. Bounded by all the budget flags from M1. Only ships after M2 has been validated empirically on at least three real convergence cycles.

### Panel rotation strategies

Built-in rotators:

- `vendor-rotation` — cycle through `vendor_family` axes per round (Claude → GPT → Gemini → local → Claude…).
- `temperature-diversification` — vary temperature ranges; round 0 uses [0, 0.3], round 1 uses [0.5, 0.8], etc.
- `focus-shuffle` — rotate the assigned `focus` axis on each panelist; member 0 does spec round 0, impl round 1, temporal round 2.
- `random-axes` — pick a different diversity bucket per round.
- `composite` — combinations.

The default for v0.4.0 is `composite:vendor_family,focus`, which gives meaningful inter-round diversity in any environment that has more than one provider configured. Single-provider deployments degrade gracefully to `focus-shuffle` only.

### Stopping criteria

Built-in stopping criteria:

- `consecutive-clean(n)` — N back-to-back rounds with zero Critical + zero unresolved Warning.
- `no-novel-findings` — every finding in this round is a carry-forward (same `claim_hash` as a finding already in the on-chain ledger for this plan).
- `adversary-explicit-pass` — the adversary returns a `Pass` verdict.
- `severity-floor(level)` — stop when no findings at or above the given severity remain unresolved.
- `max-rounds(n)` — hard cap; always wired alongside the others.

Multiple criteria AND together by default. The operator can configure `--stop-on consecutive-clean(2),no-novel-findings` to require both.

### Role enforcement

The controller refuses to run if the configured implementer's keypair label appears in the configured reviewer or adversary panel. The contract's existing `Role` enum is the source of truth here — the controller queries it on startup and validates.

### Budget enforcement

The `BudgetTracker` is consulted before each round and after each LLM call. Exhausting any budget gracefully terminates the loop with the most recent `ConvergenceResult` preserved and an explicit `BudgetExhausted` reason.

### Convergence ledger

Each round writes a `RoundLedger` entry to `.tribunal/convergence/<plan-id>/round-N.json` capturing: panel composition, findings, verdicts, costs, stopping evaluation. This is the audit trail for the convergence cycle itself — separate from the per-finding `.tribunal/ledger.jsonl`.

On-chain settlement happens per-round as usual via `tribunal chain sync` — convergence is a Go-side controller; the on-chain protocol is unchanged.

## Consequences

**Positive:**

- Releases ship at convergence, not at a schedule. The methodology gets a release-gating signal.
- The "is the methodology converging?" question becomes empirical, not opinion. Either a release converges within budget or it doesn't.
- Reputation feedback inside a cycle catches noisy adversaries before they accumulate cross-cycle reputation harm.

**Negative:**

- Token cost grows with convergence depth. A release that needs five rounds costs ~5x a release that converges on the first.
- The implementer role (M2+) is a new failure surface. An implementer that hallucinates fixes will burn token budget without producing useful patches.
- The controller introduces a configuration burden (rotation strategy, stopping criteria, budget caps) that didn't exist in the single-pass methodology.

**Neutral:**

- The contract surface doesn't change. F-NEW-403 separately motivates a structured-query primitive for the recovery layer; that's a v0.3.4 contract-side change, independent of this ADR.

## Alternatives considered

- **No convergence; rely on cross-release audits.** This is the v0.3.X regime. Empirically observed to not converge. Rejected.
- **Single-iteration with multi-vendor panel.** Adds vendor diversity but only on one pass; doesn't catch the "fix introduces structurally-similar defect" failure mode. Useful but insufficient.
- **Hard-coded loop in the CLI without configuration.** Tempting for v0.4.0, but rotation strategy and stopping criteria are exactly the operator-tunable surfaces; hard-coding them eliminates the loop's value.

## Related

- `docs/convergence.md` — the methodology-level treatment.
- `.tribunal/reports/P-v033-audit/adversary.md` — F-NEW-403, the empirical motivation.
- `docs/methodology.md` — the single-pass methodology this extends.
