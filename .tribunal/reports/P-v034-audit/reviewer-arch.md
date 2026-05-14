# Architecture Review — Tribunal v0.3.4 (convergence test)

**Reviewer:** `tribunal-reviewer-arch`
**Plan:** `P-v034-audit`
**Scope:** `fb37c3c^..fb37c3c` (`fb37c3c`, "v0.3.4: audit-driven fix release (P-v033-audit findings)")
**Verdict:** **Approve** (with two Warnings and three Suggestions for follow-up; no Critical)

## Summary

This is the convergence test. v0.3.3 was diagnosed by the adversary (F-NEW-403) as not converging on a fixed point — each fix was a narrower version of the same regex-on-error-text primitive. v0.3.4 executes the prescribed architectural pivot: the duplicate-recovery primitive is now `preflight()` itself — a structured contract-state query — shared between the success path and the recovery path. The regex helpers (`matchDuplicate`, `alreadyCommittedRE`, `alreadyResolvedRE`) and `regexp`/`strings` imports are deleted entirely. All plan tasks T1–T12 are implemented; `go build`, `go vet`, and `go test ./...` are clean against `fb37c3c`.

**The pivot worked.** The new primitive's input domain (the set of `(plan_id, finding_id)` keys the LCD returns state for) matches the contract's truth domain (the `FINDINGS` storage map keyed identically) at every type-level boundary. There is no `[^/]+` character-class narrowness because there is no character class. The recovery path now reads the same authoritative source (`FINDINGS.may_load` via the LCD smart query) that the contract's commit path checks (`FINDINGS.has`). Semantically these are the same storage lookup. F-ARCH-301 and F-SEC-301 are structurally resolved — by removing the parsing surface, not by hardening it.

What's left is calibration. Three concrete concerns:

1. **F-ARCH-401 (Warning)** — under LCD lag or partial-LCD-blip, recovery preflight can fail to see a duplicate that the contract clearly considers committed. v0.3.4 surfaces this as "no entries already on-chain" and the operator retries. Behavior is degraded-not-wrong, but it is _more_ degraded under partial LCD failure than v0.3.3's regex approach, which didn't need an LCD round-trip during recovery. This is the closest thing to a "same shape, new layer" defect I can find. I file it as Warning, not Critical, because the contract is the actual source of truth and re-running sync converges; the regression is in operator friction, not correctness.

2. **F-ARCH-402 (Warning)** — `perPlanSyncBudget = 90 * time.Second` is too tight against the worst-case recovery loop. Each recovery attempt costs ≤ 3s × (batch_size / preflight_concurrency) for preflight plus ~5s for tx broadcast+inclusion. At batch_size=100 with concurrency=8 and a slow LCD, one attempt can spend 39s in preflight alone; five attempts = ~220s, but the per-plan ctx kills the sync at 90s. The 90s budget is fine for healthy LCD operation but doesn't survive the same degraded-LCD scenario `maxRecoveryAttempts = 5` was sized for. The plan's own performance bounds (intent.md §53) acknowledge the ~75s estimate and ask reviewers to comment; the 90s budget has only 15s of margin against the optimistic case, none against the pessimistic case.

3. **F-ARCH-403 (Suggestion)** — `Config.PreflightConcurrency` isn't defaulted in `applyDefaults()`; the default lives inline in `preflight()`. Functionally equivalent (both produce `8` when the YAML omits the field), but every other defaulted field on `Config` is handled in `applyDefaults()`, so this one drifts from the pattern.

Plus two minor Suggestions on duplication and edge cases.

**Two Warnings — but neither blocks Approve.** Both Warnings are calibration concerns about the _interaction_ between the new primitive and the rest of the system; neither names a correctness defect _in_ the new primitive. The convergence question this audit was built to answer: **YES, v0.3.4 converged on the defect class P-v033 named.** No Critical in the new primitive; no recursion of the regex-narrowness defect-shape; the input domain match is type-level.

## Verification of plan tasks

### T1 — Structured-query recovery primitive (replaces regex) — IMPLEMENTED, CORRECT

`internal/chain/sync.go:348-382` (commit) and `:388-420` (resolve) replace v0.3.3's regex recovery with `preflight()` calls. The recovery is structurally identical between commit and resolve:

```go
// On Execute rejection:
ids := map[string]struct{}{}
for _, c := range commits {
    ids[c.FindingID] = struct{}{}
}
committed, _ := s.preflight(ctx, planID, ids)
filtered := commits[:0]
for _, c := range commits {
    if !committed[c.FindingID] {
        filtered = append(filtered, c)
    }
}
if len(filtered) == len(commits) {
    return br, 0, fmt.Errorf("commit batch rejected and no entries already on-chain: %w", err)
}
```

**The defect-shape question.** The contract's commit gate (`contracts/tribunal-reputation/src/execute/commit.rs:55`) checks `FINDINGS.has(deps.storage, (f.plan_id.as_str(), f.finding_id.as_str()))`. The query handler (`contracts/tribunal-reputation/src/query/finding.rs:7`) does `FINDINGS.may_load(deps.storage, (plan_id.as_str(), finding_id.as_str()))`. These are the _same storage lookup_ on the same key. The structured-query primitive consumes the second; the commit-side rejection is driven by the first. Therefore: at the contract level, the two are perfectly aligned. There is no analog to "regex character class narrower than `validate_id_field`" because the primitive doesn't parse anything — it asks the contract.

The trust posture also improves: v0.3.3's regex consumed LCD-sourced `raw_log` text, so a hostile LCD could inject arbitrary finding_ids into the recovery decision (F-SEC-301). v0.3.4 consumes only structured `FindingResp.finding` envelopes; a hostile LCD can drop entries from the result but can't fabricate them, because `resp.Finding == nil` means "not committed" and the contract would override that mistake on the retried submission.

**The composition with the success path.** Both `SyncPlan`'s up-front preflight (line 133) and the recovery preflight call the exact same function with the same parameters. They share semantics by construction. The plan §25 invariant ("both must classify on-chain state identically") is satisfied trivially because there is only one classifier.

**One subtlety worth recording.** `preflight()` swallows per-query errors into "not committed" (line 272-275). This is correct for the success path (a false-negative just adds an unnecessary commit attempt the contract will then reject). On the recovery path, the same false-negative shape becomes "we don't know this entry is committed → don't filter it → retry includes it → contract rejects again." See F-ARCH-401.

### T2 — Regex helpers deleted + `regexp`/`strings` imports removed — IMPLEMENTED, CORRECT

`git show fb37c3c:internal/chain/sync.go` no longer references `regexp` or `strings`. The `matchDuplicate`, `alreadyCommittedRE`, `alreadyResolvedRE` symbols are gone. `TestMatchDuplicate_CommitErrorParsing` is removed from `sync_test.go` because its subject doesn't exist. This is exactly the deletion P-v033-audit prescribed; no fossil code remains.

### T3 — Per-plan ctx isolation in `SyncAll` — IMPLEMENTED, CORRECT

`internal/chain/sync.go:457-471`:

```go
for _, planID := range planOrder {
    planCtx, planCancel := context.WithTimeout(ctx, perPlanSyncBudget)
    res, err := s.SyncPlan(planCtx, planID, planFindings[planID], planResolutions[planID])
    planCancel()
    // ...
}
```

The derivation from the outer `ctx` is correct: cancelling the outer ctx propagates to every `planCtx`. No leak because `planCancel()` is called explicitly after `SyncPlan` returns (not deferred — defers in a loop would stack until function exit and leak `planCancel` closures). The plan's invariant — "if the caller's ctx is shorter than `perPlanSyncBudget`, this WithTimeout resolves to the caller's deadline" — is satisfied by Go's `context.WithTimeout` semantics: the child's deadline is `min(parent.deadline, parent.deadline + duration)`. Confirmed against `context` source.

There are no goroutines that escape `SyncPlan` and outlive its ctx. `preflight()`'s worker pool calls `wg.Wait()` on line 297 before returning. So `planCancel()` after return is purely a cleanup of context resources, not a goroutine cancellation signal — no in-flight queries to interrupt.

See F-ARCH-402 for the calibration concern about the 90s constant.

### T7 — `maxRecoveryAttempts = 5` constant — IMPLEMENTED, CALIBRATION CONCERN

`internal/chain/sync.go:320` declares the constant; lines 349 and 389 use it as the loop bound. The constant docstring (lines 312-319) explicitly addresses the v0.3.3 vulnerability — `len(batch)` bound let a hostile LCD amplify gas by forcing many recovery attempts on a large batch.

The calibration argument: "Five attempts handles every realistic partial-failure scenario: each retry drops at least one entry." That's only true under _healthy_ LCD — every recovery preflight successfully identifies the duplicates. Under degraded LCD, a single recovery attempt may identify only k < total-duplicates entries; after 5 attempts you've dropped ≤ 5 distinct entries even if there were 20 actual duplicates. The remainder surfaces as "exhausted recovery attempts" and the operator retries the whole sync. Not silently wrong, but the cap is more aggressive than the docstring claims.

When 5 is too few: large batches (50-100 findings) with many duplicates AND degraded LCD where each preflight call only successfully identifies 1-2 duplicates per attempt.

When 5 is too many: it isn't. The cap exists to bound gas exposure; if the LCD is healthy, recovery usually converges in 1-2 attempts and the extra cap is unused.

Calibration verdict: 5 is reasonable. The docstring should narrow the claim from "every realistic scenario" to "every healthy-LCD scenario; under degraded LCD, operator retry is the recovery mechanism." Suggestion (F-ARCH-404).

### T4 — CLI renders partial `SyncAll` results before erroring — IMPLEMENTED, CORRECT

`cmd/tribunal/chain.go:221-227`: print loop now runs unconditionally before the error check. The `results` slice from `SyncAll` only contains non-nil entries (line 470 of sync.go appends only on `err == nil`), so `printSyncResult(r)` cannot dereference a nil. The F-ARCH-303 fix is clean.

### T5/T6 — `looksLikeTestChain` token-aware — IMPLEMENTED, CORRECT (duplicated, see F-ARCH-405)

`internal/chain/client.go:60-78` and `cmd/tribunal-seed/main.go:127-145` are byte-identical implementations. The token-aware logic:

1. Split on `-`.
2. If any token is `mainnet`/`main`/`prod`/`production` → not a test chain.
3. Else if any token is `devnet`/`testnet`/`test`/`dev`/`local` → test chain.

This correctly handles the F-SEC-303 hostile case `xion-mainnet-test-fork` (mainnet wins, returns false). The 11-case test pins the contract. Duplication is filed as Suggestion (plan §29 explicitly invites reviewers to do this).

### T8 — `PreflightConcurrency` field in `Config` — IMPLEMENTED, BACKWARD-COMPATIBLE

`internal/chain/config.go:65`:

```go
PreflightConcurrency int `yaml:"preflight_concurrency,omitempty"`
```

Backward compat with v0.3.1–v0.3.3-era YAML files: YAML parser sets unspecified int fields to zero; `preflight()` checks `> 0` (sync.go:253-255) and falls back to `defaultPreflightConcurrency = 8`. I tested this mentally and also confirmed in code: an existing chain.yaml without the field unmarshals to `cfg.PreflightConcurrency = 0`, and `s.Client.Config().PreflightConcurrency > 0` is false, so workers = 8. Confirmed.

See F-ARCH-403 for the style suggestion about defaulting in `applyDefaults()`.

### T9, T10 — Test churn — IMPLEMENTED

New `TestLooksLikeTestChain_TokenAware` pins 11 cases. Old `TestMatchDuplicate_CommitErrorParsing` is removed. Tests pass.

### T11, T12 — Doc updates — IMPLEMENTED

The Sync struct docstring (lines 22-39 of sync.go) is rewritten to describe the structured-query model. CHANGELOG.md v0.3.4 entry exists and is comprehensive. Both clean.

## New findings (v0.3.4)

### F-ARCH-401 — Recovery preflight is more LCD-sensitive than v0.3.3's regex was [Warning]

- **File:** `internal/chain/sync.go:360-376` (commit) and `:399-413` (resolve)
- **Plan anchor:** intent.md §59 ("Persistent LCD blip"), plan task T1
- **Scenario:** operator submits a 30-finding batch. 20 entries were committed yesterday but the operator's local ledger doesn't know that (the success-path preflight at line 133 caught a transient LCD blip and saw 0-of-30 committed). The batch goes to the contract, which rejects on the first duplicate it processes. Recovery preflight runs. If the LCD is still partially blipping (say, 50% query success), recovery sees only 10 of the 20 duplicates. Filter drops 10; retry contains 20 (10 still-duplicates + 10 fresh). Contract rejects again. Recovery sees a few more. After 5 attempts, sync exhausts and surfaces "exhausted recovery attempts."

In v0.3.3, the regex approach didn't need an LCD round-trip during recovery — it parsed the rejected tx's `raw_log` and extracted the duplicate finding_id directly. So under partial-LCD-blip, v0.3.3 could recover in fewer attempts than v0.3.4 can.

- **Why this isn't Critical:** the contract is still authoritative; the operator's retry will eventually converge. The error is loud and unambiguous. No silent corruption.
- **Why this is Warning, not Suggestion:** the new primitive's recovery dynamics depend on a network round-trip that v0.3.3's didn't. The plan explicitly asks (intent.md §32) "Are there contract-side error conditions OTHER than 'already committed/resolved' that v0.3.4 doesn't recover from?" The answer here is subtler: v0.3.4 recovers from the same error conditions v0.3.3 did, but its recovery now requires the LCD to be healthy _during recovery_ in addition to during the success path's preflight. This is the closest thing to a defect-class re-emergence in v0.3.4 — not regex-narrowness, but LCD-dependence is now load-bearing on two paths instead of one.
- **Suggested defense:** the recovery primitive could log per-finding query failures so operators can see the LCD's contribution to a recovery failure. OR: derive the recovery preflight's per-query timeout (3s currently) from the per-plan budget so a slow LCD doesn't burn the budget. OR: cache the success-path preflight's per-id query errors and use them to skip duplicate LCD queries in the recovery preflight (if we got 503 for F-A on the success path 5s ago, don't query F-A again on the recovery path — the contract is the better signal).

### F-ARCH-402 — `perPlanSyncBudget = 90s` is too tight against the worst-case recovery loop [Warning]

- **File:** `internal/chain/sync.go:67` (the constant) and `:463` (the use)
- **Plan anchor:** intent.md §53 ("Reviewers should comment if this fits within the per-plan budget of 90s")
- **Scenario:** healthy operation needs ~10s per plan (preflight ~2s + Execute ~5s + dedup). The recovery path multiplies that: a single recovery attempt with 100-finding batch and concurrency=8 on a 3s-timeout LCD where every query hits the timeout is `ceil(100/8) × 3s = 39s` in preflight alone. Add 5s Execute. One attempt ≈ 44s. Five attempts ≈ 220s. The per-plan ctx kills the sync at 90s.
- **Why this matters:** F-NEW-401 (v0.3.3 adversary) said one slow plan's recovery shouldn't starve other plans. v0.3.4 fixes that — but it now has the opposite problem: the per-plan budget can starve the plan's _own_ recovery loop. The Warning isn't about correctness (the surface is "ctx cancelled" → partial results printed → operator retries), it's about the calibration between two related constants: `maxRecoveryAttempts = 5` is sized for ~5 LCD round-trips × N preflight queries each, but `perPlanSyncBudget = 90s` is sized for one such cycle. They're set against different assumptions.
- **Suggested defense:** either widen `perPlanSyncBudget` to 5 × (`preflightAttemptTimeout` × ceil(MAX_BATCH_SIZE / `defaultPreflightConcurrency`) + ~10s broadcast/wait) ≈ 250s, OR tighten `preflightAttemptTimeout` and `defaultPreflightConcurrency` for the recovery path specifically. Adding a constant pair `recoveryPreflightAttemptTimeout` + `recoveryPreflightConcurrency` would make the math line up. The minimum-change fix: bump `perPlanSyncBudget` to 5 minutes (300s) and add a comment that pins the calculation.

### F-ARCH-403 — `PreflightConcurrency` default lives in `preflight()`, not `applyDefaults()` [Suggestion]

- **File:** `internal/chain/config.go:99-116` (`applyDefaults`) vs `internal/chain/sync.go:252-255` (inline default)
- **Plan anchor:** plan task T8, intent.md §24 (backward-compatible Config evolution)
- **Scenario:** every other defaulted Config field (`XiondBinary`, `KeyringBackend`, `GasAdjustment`) is handled in `Config.applyDefaults()`. `PreflightConcurrency`'s default of 8 is handled inline in `preflight()` instead, leaving `cfg.PreflightConcurrency = 0` after LoadConfig finishes. Functionally equivalent; structurally inconsistent. A future second consumer of the field (say, a separate "list operator status" command) would have to remember to repeat the inline default.
- **Suggested defense:** move the default into `applyDefaults()`:
  ```go
  if c.PreflightConcurrency == 0 {
      c.PreflightConcurrency = 8
  }
  ```
  Then `preflight()` can read `s.Client.Config().PreflightConcurrency` directly without the inline fallback, and any future consumer gets the populated value for free.

### F-ARCH-404 — `maxRecoveryAttempts` docstring overpromises [Suggestion]

- **File:** `internal/chain/sync.go:312-319`
- **Plan anchor:** plan task T7, intent.md §35 ("Does `maxRecoveryAttempts = 5` interact with the structured query in a way that creates a new gas-amplification or starvation pattern?")
- **Scenario:** the docstring says "Five attempts handles every realistic partial-failure scenario." Under degraded LCD (partial query failure during recovery), five attempts can fail to identify all duplicates if each attempt only sees k < total-duplicates entries. The Warning is in F-ARCH-401; this Suggestion is just a docstring fix.
- **Suggested defense:** rewrite as "Five attempts handles every healthy-LCD partial-failure scenario; under degraded LCD, recovery may exhaust with duplicates remaining, and operator-retry is the convergence mechanism (each retry re-runs the success-path preflight and progresses)."

### F-ARCH-405 — `looksLikeTestChain` duplicated byte-identically across two files [Suggestion]

- **File:** `internal/chain/client.go:60-78` and `cmd/tribunal-seed/main.go:127-145`
- **Plan anchor:** plan §29 ("Two `looksLikeTestChain` implementations now have synchronized token-aware behavior but are NOT deduplicated. Reviewers may file Suggestions on the duplication if they consider it brittle.")
- **Scenario:** the two implementations are byte-identical _today_. Any future hostile-pattern addition (e.g., adding `staging` as a non-test marker) requires touching both files. The pattern is brittle by inspection; the test only covers one of the implementations.
- **Suggested defense:** export `LooksLikeTestChain` from `internal/chain/client.go` (or a small `internal/chain/chainid` subpackage) and have `cmd/tribunal-seed/main.go` import it. Drop the duplicate. Single source of truth for the heuristic.

## Cross-Reviewer Ready Notes

For **reviewer-sec**:

- F-ARCH-401's "hostile LCD during recovery" is the new mirror of the F-SEC-301 finding that v0.3.4 fixed at the regex layer. v0.3.4 trusts the LCD _less_ in terms of injection (no `raw_log` text consumed) but _more_ in terms of availability (recovery depends on the LCD being up). A hostile-but-available LCD has narrower attack surface; a non-existent or pointed-DoS LCD now has a new failure mode (recovery exhaustion). Worth confirming the security model treats availability and integrity separately.
- The `PreflightConcurrency` field in `Config` (T8) ships with no upper bound. An operator (or someone with write access to chain.yaml) can set it to 10000. The intent.md flagged this for reviewer-sec attention; I confirm the surface exists (no bound in `applyDefaults`, no validation in `validate`). Up to sec to decide if a 10000-concurrency parallel-LCD-storm from a misconfigured client is a meaningful threat.

For **reviewer-perf**:

- The math in F-ARCH-402 is mine, but it's a performance/calibration concern at its core. If reviewer-perf has tighter numbers on `WaitForTx` latency against `xion-testnet-2`, the 90s vs 300s budget question may collapse to "fine for healthy operation, document the degraded-mode failure surface." OR if perf has data showing recovery preflight never approaches 39s in practice, the Warning becomes a Suggestion.
- The cost growth for recovery (1 + N parallel queries per attempt, up to 5 attempts) was flagged in intent.md §52. Worth recording an empirical wallclock measurement against `xion-testnet-2` to settle whether the 5/90s calibration is right.

## Convergence verdict

This audit was built to answer: did the architectural pivot to a structured-query primitive break the recursion P-v033-audit identified?

**Answer: yes.** The defect class P-v033 named — regex-input-domain narrower than contract-identifier-grammar — cannot recur in v0.3.4 because there is no regex and no parsing. The two storage lookups (`FINDINGS.has` on commit, `FINDINGS.may_load` on query) operate on identical keys; the primitive's input domain IS the contract's truth domain at the type level.

The two Warnings I filed (F-ARCH-401, F-ARCH-402) are _not_ the same defect shape. They're calibration concerns about how the new primitive interacts with degraded LCD and with the per-plan budget. F-ARCH-401 is the closest thing to "same shape one layer deeper" — the new primitive depends on LCD availability where the old one didn't — but the dependency is on _availability_, not on _parsing correctness_, and the failure mode is loud (surfaced error, operator retry) rather than silent (wrong finding_id dropped).

The convergence outcome per intent.md §65-69: **"Converged."** No Critical findings in the new primitive's input space. The recursion is broken. v0.4 may proceed.

## Verdict

**Approve** with two Warnings (F-ARCH-401, F-ARCH-402) and three Suggestions (F-ARCH-403, F-ARCH-404, F-ARCH-405) recorded for v0.3.5 or v0.4 grooming. Both Warnings are calibration concerns, not correctness defects; neither blocks the release. The v0.3.4 release should ship as-is, and the trio should explicitly flag for the adversary that the convergence question has been answered in the affirmative.

## FINDINGS-TO-FILE

```
Warning|architecture|F-ARCH-401|sha256:tbd|internal/chain/sync.go:360-376|Recovery preflight depends on LCD availability during recovery — degraded LCD can produce "exhausted recovery attempts" where v0.3.3's regex would have recovered without the round-trip.
Warning|architecture|F-ARCH-402|sha256:tbd|internal/chain/sync.go:67|perPlanSyncBudget=90s is too tight against the worst-case 5-attempt recovery loop with degraded LCD (≈220s); budget should match the cap.
Suggestion|architecture|F-ARCH-403|sha256:tbd|internal/chain/config.go:99|PreflightConcurrency default lives inline in preflight() instead of applyDefaults() — drifts from the pattern other defaulted fields follow.
Suggestion|architecture|F-ARCH-404|sha256:tbd|internal/chain/sync.go:312-319|maxRecoveryAttempts docstring claims "every realistic partial-failure scenario"; under degraded LCD this is too strong, narrow it to healthy-LCD case.
Suggestion|architecture|F-ARCH-405|sha256:tbd|internal/chain/client.go:60-78|looksLikeTestChain duplicated byte-identically with cmd/tribunal-seed/main.go:127-145; export from chain and dedup.
```
