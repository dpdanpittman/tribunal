# Synthesis — P-v033-audit (Tribunal reviewing v0.3.3)

**Date:** 2026-05-13 (evening)
**Diff:** `5cc1634^..5cc1634` (`v0.3.3: audit-driven fix release (P-v032-audit findings)`)
**Verdict:** **Escalate / Request Changes** — v0.3.3 ships, but v0.3.4 must follow because the methodology's adversary surfaced a structural recursion.

## Why this audit matters

This is Tribunal reviewing its own response to a prior Tribunal audit. v0.3.3 was a fix release for the 30 findings P-v032-audit produced. The question this audit answers is **does iterating with the methodology actually converge?** Two cycles in, the answer is: not yet. The adversary identified the recursion shape and named the architectural fix that would break it.

## Headline findings

The audit produced **21 findings**: 1 Critical, 9 Warnings, 11 Suggestions. The priorities for v0.3.4:

1. **F-ARCH-301 (Critical, reviewer-arch)** — the duplicate-recovery regex `finding ([^/]+)/([^ ]+) already committed` doesn't match the contract's own `validate_id_field` rules. The contract permits `/` inside `plan_id` and space inside `finding_id`. v0.3.3's recovery layer fails — with a confusing "duplicate not in batch" or by falling all the way back to v0.3.2's give-up path — for any identifier convention outside the narrow hyphen+dot+alphanum range the test happened to cover. Same defect shape as F-NEW-301 from P-v032-audit, one layer deeper.
2. **F-SEC-302 (Warning, reviewer-sec) — same defect, different framing.** `matchDuplicate`'s `TrimRight(m[2], "\"',;.)")` corrupts legitimate finding IDs that _contain_ those chars (contract permits dots, parens, quotes too). Convergence with F-ARCH-301 from a parallel lens is itself a methodology signal: two reviewers, two framings, one defect.
3. **F-NEW-403 (Architectural recommendation, adversary)** — _the methodology is not converging on a fixed point_. The adversary's verdict on the audit's own meta-question, with a concrete prescription: **v0.3.4 should replace the parse-error-string primitive with a contract-query primitive.** That breaks the recursion because the new primitive doesn't depend on text format at all — it asks the contract authoritatively which entries are committed/resolved and acts on the structured answer. Without this pivot, expect F-NEW-501 next cycle (regex narrower than _some_ identifier shape that's allowed; we'll just be in a different corner of the input space).
4. **F-NEW-401 (Warning, adversary)** — `SyncAll`'s per-plan iterations share one 5-minute ctx from `cmd/tribunal/chain.go:201`. T7's "continue past per-plan failure" works in the data path but fails at the timing layer — one slow plan's recovery cycle (worst case ~5min) starves all subsequent plans of budget. Trio reviewers each anchored on a single plan; nobody traced multi-plan starvation. Pure cross-lens composition defect.
5. **F-NEW-402 (Warning, adversary)** — recovery regex doesn't recognize `BatchMixedPlanID` errors (`contracts/tribunal-reputation/src/error.rs` defines a THIRD batch-atomic error variant; the trio audited the two named patterns and missed it). A per-entry `plan_id` drift between FindingCommit and batch wrapper renders the batch unrecoverable.

## Trio verdicts

| Lens         | Verdict         | Findings (C/W/S) |
| ------------ | --------------- | ---------------- |
| Architecture | Request Changes | 1 / 4 / 2        |
| Security     | Request Changes | 0 / 3 / 5        |
| Performance  | **Approve**     | 0 / 0 / 3        |

The perf approval is a real signal — v0.3.3's parallelization + observability work landed clean. The remaining defects are correctness, not latency.

## Adversary verdict

**Escalate.** Two Warnings the trio missed (F-NEW-401 composition, F-NEW-402 third-error-variant) and the architectural meta-finding F-NEW-403 that names the convergence problem. The adversary correctly anchored its value on what cross-corpus framing the trio shared: every lens reviewer audited the regex IN ISOLATION; none enumerated the full surface of contract errors that can revert a batch, and none traced how SyncAll's data-layer fix interacts with its timing-layer constraints.

## Convergence: the meta-finding

Two audit cycles, three releases:

| Cycle        | Defect found                                                           | Fix shipped                   | Defect of fix                                          |
| ------------ | ---------------------------------------------------------------------- | ----------------------------- | ------------------------------------------------------ |
| P-v031 audit | Wire-format mismatches                                                 | v0.3.1 ships fixes            | (clean)                                                |
| P-v032 audit | F-NEW-301: no recovery layer for batched-tx atomicity                  | v0.3.2 adds pre-flight + sync | F-NEW-301 still possible via false-negative pre-flight |
| P-v033 audit | F-ARCH-301: regex recovery narrower than contract identifier semantics | v0.3.3 adds regex recovery    | F-ARCH-301 (this audit) — same shape, one layer deeper |

**The pattern:** each fix is a more precise version of the same primitive (parse-the-LCD's-error-string). Each version is narrower than the contract's true error surface. Each subsequent audit finds the gap. The adversary's claim is that this isn't a convergence at all — it's an asymptotic approach to the contract's error grammar, and the methodology will keep finding gaps as long as we stay on this primitive.

**The fix that breaks the recursion** is the one F-NEW-403 names: stop parsing the contract's response text. Instead, on batch failure, query the contract for the post-state and reconcile from authoritative structured data. That replaces "guess which entry got rejected based on its name in the error string" with "ask the contract which entries are now committed and act on the diff."

## Verification pyramid

All four applicable Go-stack layers passed:

- build, fmt, vet, test → green
- New test `TestMatchDuplicate_CommitErrorParsing` passes (but is itself insufficient — F-ARCH-306 — because it doesn't cover the identifier characters the contract permits).

Pyramid green is still necessary-but-not-sufficient. The audit's Critical is exactly the kind of correctness defect no Go toolchain layer catches.

## Settlement

All 21 findings filed to the local ledger signed by their filing agent's keypair. All 21 resolutions written by `pm-alpha` as `true_positive`. Settled on `xion-testnet-2` via single `commit_finding_batch` + single `resolve_finding_batch`, total ~14s wall time.

| Tx      | Hash                                                               |
| ------- | ------------------------------------------------------------------ |
| commit  | `5126E66EAC45722EB008A5A497275701364DC721B0A818C6E23ACCD81B971DB8` |
| resolve | `F2C0758CC2640DFD5BE4E7BECBF3C57E8C16FCE2AA38AE51B489489AAE0F73F9` |

Cumulative reputation across both audits (visible at [tribunal.mabus.ai/leaderboard](https://tribunal.mabus.ai/leaderboard)):

| Rank | Agent           | Balance | TP  | FP  |
| ---- | --------------- | ------- | --- | --- |
| 1    | reviewer-arch   | 232     | 18  | 0   |
| 2    | reviewer-sec    | 192     | 16  | 0   |
| 3    | adversary-alpha | 176     | 8   | 0   |
| 4    | reviewer-perf   | 148     | 9   | 0   |
| 5    | pm-alpha        | 100     | 0   | 0   |

reviewer-arch leads on volume (18 TP across two audits, single Critical). adversary-alpha sits third on count but has the highest per-finding stake-concentration in Criticals/Warnings — that's the incentive layer rewarding adversaries for cross-corpus signal, not for diligence.

## v0.3.4 scope (action items)

The release must do **both** of:

### Must-fix Critical

- **F-ARCH-301 + F-SEC-302** — fix the recovery layer's identifier handling. **DO NOT just widen the regex** — that's the convergence trap. Follow F-NEW-403's prescription: replace `parse contract error string for finding_id` with `query the contract for post-batch state, diff against the submitted batch, retry the missing entries`. This is a small contract-side change too if needed (contract may need a multi-finding-state query), but the v0.3.4 implementation should land entirely on the Go side.

### Strong Warnings worth landing same release

- **F-NEW-401** — `SyncAll` per-plan ctx isolation. Each plan needs its own derived ctx from a per-plan budget OR the caller's outer ctx; one plan's recovery shouldn't starve the rest.
- **F-NEW-402** — handle `BatchMixedPlanID` in recovery. Likely subsumed by the F-NEW-403 fix if we move to structured contract queries.
- **F-ARCH-303** — CLI caller of `SyncAll` should render the partial results slice on error, not discard.
- **F-ARCH-306** — expand `TestMatchDuplicate_CommitErrorParsing` to slashy/spacey/dotty IDs.
- **F-ARCH-307** — bound recovery loop attempts more aggressively to prevent gas amplification under hostile LCD.
- **F-SEC-301** — by structuring-error-parsing-out via F-NEW-403, the LCD trust posture improves: hostile LCD can no longer inject a fake `finding_id` via `raw_log` because we're not reading `raw_log` anymore.

### Suggestions worth grouping

- F-SEC-303 (`looksLikeTestChain` substring bypass — e.g. `xion-mainnet-test-fork` slips through)
- F-PERF-301 (preflight concurrency knob)
- F-PERF-302/303 (observability completeness)
- F-ARCH-302 (slice-aliasing in-place filter brittleness — may evaporate once F-NEW-403 rewrites this code anyway)
- F-SEC-205/206/208-carryfwd (still alive from v0.3.2)

### Methodology meta-action

- **`docs/convergence.md`** — write up the multi-cycle convergence pattern observed, the asymptotic-recursion failure mode, and the design principle of replacing text-parsing primitives with structured-state-query primitives. This becomes the methodology's first articulated lesson learned _from itself_.

## Methodology meta-observations

- Two cycles in, every Critical has come from the adversary stage, not the lens trio. The trio's role is high-volume defect coverage; the adversary's role is the cross-corpus reframing that ALWAYS lives in their joint blind spot. That's a load-bearing methodology observation — without the adversary, all three Criticals from the two cycles would have slipped through.
- Convergence is the right question, and the methodology isn't currently converging. The adversary's F-NEW-403 is what makes it possible to converge — a different primitive that doesn't expand the same input space forever. Whether v0.3.4 lands clean depends on whether the implementer takes the architectural advice or just widens the regex (which would buy maybe one more cycle).
- The lens trio's coverage of mechanical correctness (signature integrity, goroutine lifecycle, ctx propagation, multiplier defaulting) is airtight at this point. Future audits should expect to find correctness defects at composition + cross-cutting concerns + new-feature-level surfaces, not in the v0.3.1-era wire-format layer.
- The on-chain reputation accumulating across cycles is functioning as designed: high-volume reviewers (arch) rise on findings count, while high-severity-concentration agents (adversary) rise per-finding. Both rankings are legitimate signals for different consumer roles (PMs weighting which agent to elevate vs. which agent to require corroboration on).

## Recommendation

Cut v0.3.4 with the Critical-fix using the F-NEW-403-named primitive (contract-query, not regex hardening). Run P-v034-audit before any further work. If THAT audit comes back clean — or with only Suggestions — the methodology converged on this defect class and v0.4 (multi-org, slashing appeals, cross-chain, fungible operator rewards) can start. If P-v034-audit surfaces another Critical in the same convergence shape, the contract surface itself needs revisiting before the tooling can settle.
