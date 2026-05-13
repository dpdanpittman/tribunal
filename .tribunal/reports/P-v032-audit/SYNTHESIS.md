# Synthesis — P-v032-audit (Tribunal reviewing Tribunal)

**Date:** 2026-05-13
**Diff:** `HEAD~1..HEAD` (`f186e92`, "v0.3.2: devnet-driven tooling fixes")
**Verdict:** **Escalate / Request Changes** — v0.3.2 should not have shipped as-is; v0.3.3 should land before further work.

## Why this audit matters

This is the first time Tribunal has reviewed its own release. The release was already validated end-to-end against a live xion-devnet (`devnet-e2e-2026-05-13.md`). The point of the audit was to test whether the methodology surfaces real defects the manual e2e didn't catch, on a non-trivial change to the chain client + sync + deploy script.

It did.

## Headline findings

The audit produced **30 findings** total: 3 Critical, 12 Warnings, 15 Suggestions. The strongest hits, in priority order for v0.3.3:

1. **F-NEW-301 (Critical, adversary)** — **batch atomicity vs. pre-flight false-negative.** `commit_finding_batch` uses Rust's `?` operator; one already-committed finding short-circuits the whole batch. A single LCD blip that false-negatives one pre-flight query in an N-finding plan reverts all N commits and burns ~10M+ gas for a 100-finding plan. Every lens reviewer wrote some variant of "contract will reject duplicates" as the recovery argument — true per-message, **false per-batch**. The trio's shared corpus on retry semantics is uniformly per-request idempotency; CosmWasm batched-tx atomicity breaks that mental model in exactly the failure mode F5's pre-flight is supposed to absorb.
2. **F-NEW-302 (Critical, adversary)** — `Execute` returns a non-nil `BroadcastResult` with txhash on `WaitForTx` error, but every caller discards it. Operator sees "tx failed" while the tx is actually pending or successful on-chain. Loses the audit trail and the ability to recover.
3. **F-ARCH-201 (Critical, reviewer-arch) / F-SEC-201 / F-PERF-201** — `WaitForTx` aborts on any non-404 HTTP error instead of polling through transient blips. This **defeats F4's stated goal**: the wait loop exists to make sync resilient to broadcast-to-inclusion gaps, but its own bail-on-error logic makes a flaky LCD or network blip indistinguishable from terminal failure. Cross-validated by all three lens reviewers AND the adversary.

The methodology earned its name on this run. The lens trio caught the surface defects (WaitForTx error handling, plaintext HTTP, pre-flight cost, observability silence); the adversary caught the architectural-correctness defect the trio's shared corpus made invisible (batched-tx atomicity).

## Trio verdict

| Lens         | Verdict         | Findings (C/W/S) |
| ------------ | --------------- | ---------------- |
| Architecture | Request Changes | 1 / 5 / 5        |
| Security     | Request Changes | 0 / 4 / 4        |
| Performance  | Request Changes | 0 / 3 / 3        |

All three independently flagged the WaitForTx transient-error abort (different framings — arch as boundary, sec as availability, perf as resilience). The convergence is the methodology working: a single defect surfaced from three trained lenses is a stronger signal than any single reviewer's verdict.

## Adversary verdict

**Escalate.** 5 new findings, 2 Critical, 3 Warning, 0 Suggestion. The adversary did not file Suggestions — that's correct. The adversary's value is in cross-corpus blind spots, not in style or doc-comment polish.

## Verification pyramid

All 4 applicable layers passed (build, fmt, vet, test, 4.5s total). `staticcheck` and `golangci-lint` disabled in `tribunal.yaml`. Pyramid green does not mean the code is correct — the adversary's F-NEW-301 Critical is exactly the kind of correctness defect no toolchain layer catches.

## Settlement

All 30 findings filed to the local ledger signed by their filing agent's keypair. All 30 resolutions written by `pm-alpha` as `true_positive` (every finding deemed valid; v0.3.3 will incorporate the fixes). Settled on-chain via a single `commit_finding_batch` + a single `resolve_finding_batch`, total 3.4s. Final leaderboard:

| Rank | Agent           | Balance | TP  | FP  |
| ---- | --------------- | ------- | --- | --- |
| 1    | reviewer-arch   | 176     | 11  | 0   |
| 2    | adversary-alpha | 172     | 6   | 0   |
| 3    | reviewer-sec    | 148     | 8   | 0   |
| 4    | reviewer-perf   | 136     | 6   | 0   |
| 5    | pm-alpha        | 100     | 0   | 0   |

reviewer-arch wins on finding count (11). adversary-alpha is a close second on reputation despite filing only 5 findings — the Critical-and-Warning concentration paid more per finding than the trio's mix of severities. That's the incentive layer working as designed: adversary's role is to file fewer, higher-severity findings that the trio's lens-bound search misses.

## v0.3.3 scope (action items)

Triaged from this audit. Critical and Warning go in scope; Suggestions defer unless they cluster.

### Critical (must-fix)

- **F-NEW-301** — sync's pre-flight tolerance must be replaced. Either: (a) make pre-flight errors fatal (caller retries explicitly), or (b) split the batch into per-message txs when a pre-flight error is observed, or (c) introduce a `commit_finding_batch_idempotent` contract path that gracefully skips known-duplicates. Pick one before any large-batch sync runs against mainnet.
- **F-NEW-302** — propagate the `BroadcastResult` even on `WaitForTx` error; let callers decide how to handle a tx that broadcast but didn't land. Change `Execute`'s return signature or document the "non-nil on error" contract loudly.
- **F-ARCH-201 / F-SEC-201 / F-PERF-201** — `WaitForTx` must distinguish transient HTTP errors from terminal ones. 5xx, connection refused, parse failure on partial bodies → continue polling. ctx done → terminate. 4xx other than 404 → caller decides. The current `return false, 0, "", err` on any non-404 is the bug.

### Warning (should-fix)

- **F-ARCH-202** — pre-flight loop swallows ctx.Err() via `continue`. Check `ctx.Err()` after each query failure and bail if cancelled.
- **F-ARCH-203 / F-PERF-203** — pre-flight is N serial REST round-trips. Parallelize with bounded fan-out, or batch via a new `findings_in_plan` contract query.
- **F-ARCH-204** — `normalizeRPCScheme` lives in `cmd/` but should be in `internal/chain/config.go` so `LoadConfig` also normalizes on read (covers configs written before v0.3.2).
- **F-ARCH-205** — `applyDefaults` overrides `outcome_reward_multiplier=0` to `2`, defeating F6 when the contract genuinely has multiplier 0. Defaults must not override values explicitly set by `chain init`.
- **F-ARCH-206** — `tribunal-seed` argv parsing treats `--send` as plan ID. Use a real flag parser.
- **F-SEC-203 / F-SEC-204** — hostile / slow LCD can fake inclusion or starve sync. Add per-attempt timeout (the one the docstring already lies about) and bound pre-flight by deadline-from-ctx.
- **F-PERF-204** — wait loop and pre-flight are observability-silent. Emit progress to stderr on a ≥2s threshold.
- **F-NEW-303** — `SyncAll` aborts on first plan failure and discards partial results. Collect per-plan errors and continue.
- **F-NEW-304** — resolutions not deduped (commits are). Same `seen` pattern; ledger can have duplicate resolutions across runs.
- **F-NEW-305** — poison-pill finding causes perpetual batch revert. Add a skip-on-N-failed-attempts affordance.

### Suggestions (deferred unless clustering reveals shape)

11 suggestions across the four reports. Most are doc-comment polish, minor parse robustness, and TLS posture choices. None individually blocks v0.3.3.

## Methodology meta-observations

- The trio's reports independently used some shared phrasing about the WaitForTx defect. This is the cross-corpus signal we built the adversary stage to surface — and the adversary did, in F-NEW-301. The methodology is correctly calibrated.
- The lens-parallel dispatch took ~5 minutes wall-clock; the adversary ~7 minutes; the verification pyramid <5s. Total wall time for a full review: under 15 minutes. Tractable for any non-trivial PR.
- Filing 30 findings to the ledger + on-chain settlement took 3.4s end-to-end. The pre-flight filter correctly identified the empty starting state; the F4 wait loop correctly serialized the commit + resolve batches. Both v0.3.2 fixes worked exactly as designed under load — even as the audit found correctness defects in them.
- The fact that v0.3.2 ships fixes that the same release's audit immediately escalates is a feature, not a contradiction: it means v0.3.2 was the right scope (six observable bugs from the e2e), and v0.3.3 has the right scope (the deeper composition + corpus-blind-spot defects only adversarial review surfaces). Each release is correct relative to the question it was asked.

## Recommendation

Cut v0.3.3 with the eight Critical / Warning items above. Defer the suggestions to v0.3.4 unless one becomes load-bearing during v0.3.3 implementation. Run this same dogfooding audit against v0.3.3 before ship.
