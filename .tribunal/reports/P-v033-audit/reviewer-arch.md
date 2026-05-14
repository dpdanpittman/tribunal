# Architecture Review — Tribunal v0.3.3

**Reviewer:** `tribunal-reviewer-arch`
**Plan:** `P-v033-audit`
**Scope:** `5cc1634^..5cc1634` (`5cc1634`, "v0.3.3: audit-driven fix release (P-v032-audit findings)")
**Verdict:** **Request Changes**

## Summary

v0.3.3 lands every fix the v0.3.2 audit demanded. Diff hunks all trace to plan tasks T1–T13; no unplanned refactor snuck in. `go build`, `go vet`, and `go test ./...` are clean against `5cc1634`. The classification rework on `WaitForTx` is exactly what F-ARCH-201 asked for and the new `BroadcastResult`-on-error contract is documented in the docstring loud enough that future callers won't drop it.

The audit-of-the-audit-fixes finds two architectural defects that v0.3.2 didn't have because v0.3.2 didn't have a recovery layer:

1. **The duplicate-recovery regex parser is the new untrusted-input boundary, and its character classes don't match the contract's own identifier rules.** `validate_id_field` (`contracts/tribunal-reputation/src/validate.rs:28-56`) permits `/` and spaces inside `plan_id` and `finding_id` — only `|` and control chars are rejected. The recovery regex `finding ([^/]+)/([^ ]+) already committed` (`internal/chain/sync.go:302`) breaks on both:
   - Plan ID containing `/` → wrong finding_id captured → "duplicate not in batch" bail → sync fails permanently against that plan even though the contract's duplicate guard succeeded. This is exactly F-NEW-301's failure mode, just behind a different door.
   - Finding ID containing space → regex doesn't match at all → recovery layer falls through to the give-up path. F-NEW-301 returns under load.
     The new `TestMatchDuplicate_CommitErrorParsing` pins normal-case parsing but never exercises either failure mode the contract's identifier rules actually permit.

2. **`SyncAll` partial-failure aggregation is wired internally but not at the CLI boundary.** The intent.md invariant — "one bad plan no longer aborts every subsequent plan" — is honored by `SyncAll` (`internal/chain/sync.go:431-443`), but the only caller, `cmd/tribunal/chain.go:217-220`, does `if err != nil { return err }` and discards the `results` slice. Operator visible behavior is unchanged from v0.3.2: one error, no progress info on the 9 successful plans.

The remaining new findings are Warnings about test coverage gaps (the recovery layer has zero end-to-end coverage; preflight cancellation has zero coverage), the slice-aliasing filter idiom in `submitCommitBatch` / `submitResolveBatch`, and a Suggestion that `NormalizeRPCScheme`'s "exported" status is misnamed (it's still `internal/`-confined, so the public-surface concern in the plan is moot).

Three Critical-or-Warning findings unresolved → Request Changes.

## Verification of plan tasks

### T1 — `WaitForTx` error classification + per-attempt timeout — IMPLEMENTED, CORRECT

`internal/chain/client.go:154-194` replaces the v0.3.2 "any-error-aborts" loop with a four-bucket classifier surfaced via `fetchTx`'s new `terminal bool` return:

- **404 / 200-with-empty-height** → not-yet-indexed, continue polling, `transientStreak` reset.
- **5xx, network-layer, body-read failure, JSON parse failure on partial body** → transient, `terminal=false`, loop continues, `transientStreak++`.
- **4xx other than 404** → terminal, propagate.
- **200 with `code != 0`** → terminal on-chain failure, propagate with code + raw_log.
- **ctx.Done()** → wrap and return.

Per-attempt timeout (`fetchTxAttemptTimeout = 3 * time.Second`) is enforced via `context.WithTimeout(ctx, …)` around the HTTP round-trip (`internal/chain/client.go:203-204`). F-ARCH-210 from the prior audit (docstring claimed 300ms, code didn't enforce it) is gone — the new docstring matches the new constant.

The `transientStreak` counter showing up in the terminal error message ("elapsed=300s, transient_streak=N") is a nice operator-facing observability win that the plan didn't strictly require.

**Drift from documented contract:** none I can spot. The docstring matches the implementation on each of the five branches.

### T2 — Propagate `BroadcastResult` on wait error — IMPLEMENTED, CORRECT

`internal/chain/client.go:138-140` returns `(&res, err)` instead of `(nil, err)` on `WaitForTx` failure, and the txhash is interpolated into the wrapped error message so even a caller that doesn't unpack the struct still sees the hash in the log.

The docstring at `client.go:101-105` is **emphatic** about the new contract:

> Callers must check res != nil and the txhash even on error so they can resume polling or surface the on-chain status to the operator.

The recovery layer (`submitCommitBatch`, `submitResolveBatch`) is the one new caller, and it correctly threads `br` through the bail paths (`sync.go:330, 340, 365, 374`). All other call sites (`cmd/tribunal/chain.go` for `register`, `rotate`, `query`, `sync`; `cmd/tribunal-seed/main.go`) still discard `br` on error. **That's acceptable** for register/rotate (the operator can re-derive intent by re-running) but **the docstring contract is now part of the public-ish surface of `Execute`** — anyone writing a third-party caller has to know to check `res` on error. Worth a Suggestion to surface the txhash in the error message for every Execute call site, not just the wait error path.

### T3 — Batch atomicity recovery — IMPLEMENTED, DEFECTIVE UNDER REAL IDENTIFIER RULES

`internal/chain/sync.go:316-381` implements both `submitCommitBatch` and `submitResolveBatch`. Termination is guaranteed by the `for attempt := 0; attempt <= originalLen; attempt++` bound combined with the "filtered length must shrink or bail" check (`sync.go:338-341`, `sync.go:373-376`). I traced 100-element batches with adversarial fakes — every path returns within `originalLen + 1` iterations.

**Defect:** the duplicate-extraction regex doesn't agree with the contract's `validate_id_field` constraints on `/` and space. See F-ARCH-301 below — this is the new Critical.

**Defect:** the slice-aliasing filter idiom `filtered := commits[:0]` reuses the backing array of `commits`. It's correct in the current loop (range copies `c` before append writes back), but it's brittle: any future refactor that changes the loop shape, or any caller that retains a reference to the original `commits` slice, gets silent corruption. See F-ARCH-302.

### T4 — Parallel pre-flight with bounded fan-out + per-query timeout — IMPLEMENTED, CORRECT

`internal/chain/sync.go:222-298`. 8-worker fan-out, fixed-size buffered channels sized to `len(ids)`, per-query `context.WithTimeout(ctx, preflightAttemptTimeout)`, progress goroutine that exits on `close(done)`.

**Goroutine lifecycle audit:**

- Workers exit when `idCh` is closed (drained) OR when `ctx.Err() != nil` (cancellation).
- On ctx-cancel: workers may stop reading `idCh` before fully drained. Doesn't leak — the channel is GC'd after `preflight` returns.
- `resCh` is sized to `len(ids)`. Workers can never block on send.
- The progress goroutine exits on `close(done)`. `close(done)` is called _after_ `wg.Wait()`. Workers signal completion via wg only.
- `wg.Wait()` blocks `preflight` until all workers exit. After that, `close(resCh)` then `close(done)` then drain. Order is correct.

**Drift:** the post-preflight check at `sync.go:125-127` returns the cancellation error. Good — but the per-id result channel may be partially populated, meaning `committedOnChain[id]` is `false` for ids the workers never reached. If SyncPlan ever ignores the cancellation return and proceeds, those ids would be re-broadcast. Right now it doesn't ignore. Fine, but worth noting that the invariant "if ctx is alive after preflight, all ids were checked" depends on the cancellation check, not on the preflight return value itself.

### T5 — ctx check in pre-flight — IMPLEMENTED, CORRECT

The check lives in two places:

- Inside each worker before each LCD call (`sync.go:254-256`) so cancellation bails fast.
- After `preflight` returns (`sync.go:125-127`) so SyncPlan refuses to proceed with a partial result map.

F-ARCH-202 from the prior audit is resolved. Good.

### T6 — Dedup resolutions — IMPLEMENTED, CORRECT

`sync.go:167-180` introduces `seenResolve` parallel to `seenCommit`. F-NEW-304 closed cleanly.

The diff also renamed the commit-side `seen` to `seenCommit` (`sync.go:131-160`). That rename is in scope per plan T6 but isn't called out in the diff hunks. Architecturally fine — just noting for traceability.

### T7 — `SyncAll` partial-failure aggregation via `errors.Join` — IMPLEMENTED INTERNALLY, NOT WIRED AT CLI

`internal/chain/sync.go:431-443` does what intent.md says: per-plan errors collected, `errors.Join`-ed, returned alongside the successful results.

**Defect:** the only caller, `cmd/tribunal/chain.go:217-220`:

```go
results, err := sync.SyncAll(ctx, lg)
if err != nil {
    return err
}
for _, r := range results {
    printSyncResult(r)
}
```

discards `results` whenever `err != nil`. The intent invariant ("one bad plan no longer aborts every subsequent plan from being settled") is satisfied _inside SyncAll_, but the operator's experience is identical to v0.3.2: one error, no visible progress on the plans that did settle. This is exactly the kind of "half-wired refactor" the architectural lens is supposed to catch — the function signature now carries new info, but the caller ignores it. See F-ARCH-303.

### T8 — Progress notes — IMPLEMENTED, CORRECT

`WaitForTx`: `client.go:180-184` emits every 5s of elapsed wait via `lastProgress` bookkeeping inside the poll loop.
Pre-flight: `sync.go:269-283` runs a separate goroutine with a `5s` ticker that prints `tribunal: still pre-flighting plan=… elapsed=…`. Exits cleanly on `close(done)` after `wg.Wait()`.

Cadence is consistent across both paths (`waitProgressInterval = 5 * time.Second`). F-PERF-204 closed.

### T9 — `NormalizeRPCScheme` on every `LoadConfig` — IMPLEMENTED, CORRECT

`internal/chain/config.go:18-29` defines `NormalizeRPCScheme` (exported), called from:

- `LoadConfig` (`config.go:82-85`) — silent normalization on read.
- `cmd/tribunal/chain.go:50` — stderr-loud normalization at `chain init`.

The old `cmd/tribunal/chain.go::normalizeRPCScheme` is gone. Boundary placement is now correct: it's a Config concern in the Config package.

**On the "public surface enlargement" concern the parent flagged:** `internal/chain` is an `internal/` package. Even though the symbol is uppercase, Go's `internal/` rule blocks any importer outside `github.com/dpdanpittman/tribunal/`. There is no actual public surface enlargement — the export is module-scoped. The plan's stated "Any reviewer concern about exporting it should be filed" can be dismissed on this point. See F-ARCH-304 (Suggestion: comment the export reasoning).

### T10 — Remove `outcome_reward_multiplier` auto-default — IMPLEMENTED, CORRECT

`internal/chain/config.go:104-110` replaces the `if c.OutcomeRewardMultiplier == 0 { c.OutcomeRewardMultiplier = 2 }` block with a comment block explaining why 0 is a legitimate value. F-ARCH-205 closed.

**Drift:** there's no test asserting "a config with `outcome_reward_multiplier: 0` round-trips as 0." The fix is correct, but a regression test pinning the new behavior would prevent a well-meaning future PR from putting the default back. See F-ARCH-305 (Suggestion).

### T11 — `tribunal-seed` hardening — IMPLEMENTED, CORRECT

`cmd/tribunal-seed/main.go:25-37` switches to the `flag` package; flags are named, defaults are explicit, `--allow-prod` opts out of the production-chain guard. `looksLikeTestChain` mirrors the same heuristic the chain client warning uses (consistent boundary). F-ARCH-206 and F-ARCH-207 from the prior audit closed.

The `--allow-prod` guard is a safety rail, not a security boundary (intent.md says as much). Architecturally fine.

### T12 — Regex unit test — IMPLEMENTED, INCOMPLETE COVERAGE

`internal/chain/sync_test.go:21-78`. Five cases:

1. Plain `already committed`.
2. `already committed` with a dotted finding_id (`F-sec.201`).
3. `already resolved`.
4. Unrelated error — no match.
5. Wrong regex against committed message — no match.

**What's missing:** the contract's identifier rules permit `/` and space (see `contracts/tribunal-reputation/src/validate.rs:42-54`). The regex breaks on both. The test never tries either. See F-ARCH-301 + F-ARCH-306 (the test gap is a Warning because it's _why_ the bug landed un-caught).

### T13 — CHANGELOG v0.3.3 entry — IMPLEMENTED, ACCURATE

`CHANGELOG.md` covers every fix with cross-refs to the F-ARCH/SEC/PERF/NEW IDs from the prior audit. No drift.

## New findings

### F-ARCH-301: Recovery regex character classes don't match the contract's `validate_id_field` rules (Critical)

**File:** `internal/chain/sync.go:300-305`

**Claim:** the recovery layer's regexes are:

```go
var alreadyCommittedRE = regexp.MustCompile(`finding ([^/]+)/([^ ]+) already committed`)
var alreadyResolvedRE  = regexp.MustCompile(`finding ([^/]+)/([^ ]+) already resolved`)
```

The contract's `validate_id_field` (`contracts/tribunal-reputation/src/validate.rs:28-56`) permits any character in `plan_id` and `finding_id` except `|` and `unicode::is_control()`. Specifically, **`/` and ASCII space are permitted** in both fields.

Concrete failure cases verified by `go run /tmp/regex_test.go`:

| Error message                               | Captured plan | Captured finding | Truth                                            |
| ------------------------------------------- | ------------- | ---------------- | ------------------------------------------------ |
| `finding P-arch/sec/F-99 already committed` | `P-arch`      | `sec/F-99`       | actual plan was `P-arch/sec`, finding was `F-99` |
| `finding P-1/F 99 already committed`        | _(no match)_  | _(no match)_     | actual finding was `F 99`                        |

**Downstream behavior:**

- **Plan ID with `/`** → the regex captures `sec/F-99` as the finding_id. The recovery loop filters `commits` for entries with `FindingID == "sec/F-99"` — none match. `len(filtered) == len(commits)` triggers the "duplicate not in batch" bail (`sync.go:338-341`). **Sync fails permanently** for any plan whose `plan_id` contains `/`, even though the contract's duplicate guard succeeded. This is exactly F-NEW-301's failure mode resurfacing.
- **Finding ID with space** → `[^ ]+` stops at the space, the literal ` already committed` matcher then fails because the input is ` 99 already committed`. The full regex returns no match. `matchDuplicate` returns `ok=false`. Recovery treats the error as a non-duplicate failure and **gives up the whole batch** at `sync.go:330`. F-NEW-301 returns.

**Why it matters:** plan/finding IDs are operator-chosen. The contract documents these character rules, and the codebase deliberately permits them — `validate_id_field` was added in v0.3 specifically to enumerate what's legal. Any caller using a `/`-bearing plan id (a natural choice for namespaced plans like `P-arch/sec-201`) hits the first case immediately. The trio is currently the most likely caller, and the codebase's own claim IDs and category labels in `FINDINGS-TO-FILE` blocks are space-bearing in some places — drift is one careless `plan_id` away.

**Plan anchor:** intent.md "Failure modes" → "Recovery regex drift if the contract changes its error string format → TestMatchDuplicate_CommitErrorParsing should fail loudly." The contract format is unchanged; what drifted is **the regex's assumption about identifier characters**, which was already wrong at write time. The test never noticed because every case used hyphen-only IDs.

**Severity:** Critical. This is the v0.3.3-equivalent of v0.3.2's F-NEW-301: the recovery layer ITSELF has a hole that the trio approving the fix didn't probe. The contract's duplicate guard correctly rejects the duplicate, but the client-side parser can't identify which entry to drop, so the recovery loop dies the same way the original batch-atomic-revert killed v0.3.2.

**Suggested defense:** parse `raw_log` as a structured envelope when available — xiond emits the contract's `ContractError` Display string but also a JSON-ish event log; prefer the latter. As a fallback, encode the finding_id end via a delimiter the contract enforces (e.g., have the contract emit `finding {plan_id} :: {finding_id} already committed` with a separator no identifier can contain). At minimum, change the regex to anchor the trailing text: `finding (.+) already (committed|resolved)$` and then split the captured "X/Y" on the **last** `/`, since plan_id can contain `/` but finding_id can't be the entire trailing — actually no, finding_id CAN contain `/` too. The cleanest fix is contract-side: emit the IDs as quoted strings or JSON, then parse the quoted region.

---

### F-ARCH-302: `submitCommitBatch` / `submitResolveBatch` use slice-aliasing filter; correctness depends on for-range copy semantics (Warning)

**File:** `internal/chain/sync.go:332-337`, `sync.go:367-372`

**Claim:** the filter idiom

```go
filtered := commits[:0]
for _, c := range commits {
    if c.FindingID != dupID {
        filtered = append(filtered, c)
    }
}
```

reuses the backing array of `commits`. It's correct as written because the for-range copies each element into `c` _before_ the append writes back into the same backing slot — but the safety is non-obvious and depends on a Go semantic that has bitten people before (e.g., for-range over slices of structs vs. slices of pointers; modifying the slice during iteration).

**Why it matters:** the recovery loop is the new boundary that v0.3.2's trio approval didn't have to consider. Anyone refactoring it (e.g., to add a "retry budget" or to filter on more than just `FindingID`) is likely to break the aliasing invariant without realizing it. The function name "submitCommitBatch" doesn't telegraph "in-place filter inside" — the abstraction's internals are tightly coupled to a slice idiom that future maintenance will likely forget.

**Plan anchor:** plan.md reviewer-arch focus: "recovery loop termination guarantees" — termination is fine, but the recovery loop's _internal correctness_ hinges on a sharp edge. The plan does not declare this idiom.

**Severity:** Warning. Not a correctness bug _today_. Will be one tomorrow.

**Suggested defense:** allocate a fresh slice (`filtered := make([]FindingCommit, 0, len(commits)-1)`) or use `slices.DeleteFunc` (`std lib`, Go 1.21+). The allocation cost is irrelevant against the cost of a chain round-trip. Or — better — accumulate the surviving entries by building an index set and copying once at the end.

---

### F-ARCH-303: `SyncAll`'s partial-failure aggregation is unobservable from the CLI (Warning)

**File:** `cmd/tribunal/chain.go:217-220` (caller); `internal/chain/sync.go:431-443` (producer)

**Claim:** the v0.3.3 fix to F-NEW-303 moves the per-plan error handling inside `SyncAll`: instead of `return out, err`, it accumulates via `errors.Join` and returns `(out, errors.Join(errs...))`. The intent.md invariant is "one bad plan no longer aborts every subsequent plan from being settled."

But the only caller is:

```go
results, err := sync.SyncAll(ctx, lg)
if err != nil {
    return err
}
for _, r := range results {
    printSyncResult(r)
}
```

When `err != nil`, the `results` slice is discarded. The operator sees the error, but **not** the summary of the plans that did settle. Behavior visible to the user is identical to v0.3.2.

**Why it matters:** the fix landed correctly in the data path but the boundary at `cmd/tribunal/chain.go` didn't get updated. This is the architectural class of "internal refactor without caller update." Other reviewers might also note: the test suite has zero coverage of partial-failure aggregation, so even a future caller fix wouldn't be guarded.

**Plan anchor:** intent.md "Failure modes" → "Per-plan errors collected via errors.Join instead of aborting on first failure." The plan-task language ("collected") is satisfied; the **operator-observable invariant** is not.

**Severity:** Warning. Soft-bricks the v0.3.3 value-add of T7 unless the CLI is also updated.

**Suggested defense:** in `cmd/tribunal/chain.go:217-225`, change to:

```go
results, syncErr := sync.SyncAll(ctx, lg)
for _, r := range results {
    printSyncResult(r)
}
if syncErr != nil {
    return syncErr
}
return nil
```

Print the successful plans first, then return the aggregated error. Operator sees both signals.

---

### F-ARCH-304: `NormalizeRPCScheme` "exported" status is moot under `internal/` rule (Suggestion)

**File:** `internal/chain/config.go:18-29`

**Claim:** the plan calls out "public surface enlargement" for moving `normalizeRPCScheme` (lowercase, `cmd/`) → `NormalizeRPCScheme` (uppercase, `internal/chain/`). The Go `internal/` rule means no external module can import this symbol regardless of casing. The export is module-internal only.

**Why it matters:** this is a no-op as far as actual public surface goes. The plan's "Any reviewer concern about exporting it should be filed" concern is mooted by the package path. Worth a one-line comment on the function explaining why it's exported (so callers in `cmd/` and tests can reach it) but that there's no API stability commitment because `internal/` blocks external importers.

**Severity:** Suggestion. Naming convention.

**Suggested defense:** add a doc-comment line: `// Lives in internal/chain/, so the export is module-scoped — no external API stability commitment.`

---

### F-ARCH-305: No regression test pinning the new `outcome_reward_multiplier=0` round-trip (Suggestion)

**File:** `internal/chain/config.go:101-110` (new behavior); no test file covers the round-trip.

**Claim:** T10 removed the `0 → 2` rewrite. A future PR that puts the rewrite back (e.g., a contributor who sees "if `OutcomeRewardMultiplier == 0`, default to 2" as a "fix" for an unrelated reason) would not break any test. The fix is correct, but it has no guard.

**Severity:** Suggestion. Test debt.

**Suggested defense:** add a unit test `TestLoadConfig_PreservesZeroOutcomeMultiplier` that writes a config with `outcome_reward_multiplier: 0` to a tempfile, loads it, and asserts the loaded value is 0.

---

### F-ARCH-306: Regex test misses the actual identifier rules the contract permits (Warning)

**File:** `internal/chain/sync_test.go:21-78`

**Claim:** `TestMatchDuplicate_CommitErrorParsing` covers five cases: plain, dotted, resolved, unrelated, wrong-regex. Every case uses hyphen + dot identifiers. The contract's `validate_id_field` (`contracts/tribunal-reputation/src/validate.rs:42-54`) permits any non-pipe, non-control char — including `/`, space, single-quote, paren. None of those appear in the test.

This is the proximate cause of F-ARCH-301 landing un-caught: the regex was written against an implicit identifier model that doesn't match the contract, and the test was written against the same implicit model.

**Why it matters:** even if F-ARCH-301 is fixed via a regex change, the _test gap_ will let the next regex drift land unnoticed unless the test corpus is broadened to cover the full identifier character class the contract accepts.

**Plan anchor:** intent.md "Failure modes" → "Reviewers must confirm the test pins the format that the contract actually emits." The format isn't only the surrounding string template — it's also the character classes inside the captures. The test confirms the former and not the latter.

**Severity:** Warning. The test exists and pins the happy path correctly; what it doesn't cover is what F-ARCH-301 demonstrates is broken.

**Suggested defense:** add cases derived directly from `validate_id_field`'s rules:

- `plan_id` containing `/`
- `finding_id` containing space
- `plan_id` and `finding_id` both at the 64-char max length
- Mixed unicode (the rule allows non-control non-pipe Unicode chars)
  Ideally these cases live next to the regex constants so a future regex change must touch the test cases too.

---

### F-ARCH-307: Recovery loop interacts with `WaitForTx`'s transient tolerance to enable gas-amplification under hostile LCD (Warning)

**File:** `internal/chain/sync.go:316-381`

**Claim:** `WaitForTx` no longer terminates on transient errors. The recovery loop iterates up to `originalLen + 1` times. Each iteration calls `s.Client.Execute(ctx, msg)`, which **broadcasts a real tx**. If a hostile LCD returns a parseable `already committed` error referencing a different in-batch finding on each retry, the operator burns one full batch-broadcast worth of gas per iteration (~14M gas at 100 findings, per the contract's `MAX_BATCH_SIZE` doc).

Worst case: a 100-element batch facing an adversarial LCD eats ~100 broadcasts × ~14M gas = ~1.4B gas before the recovery loop's `len(commits) == 0` exit catches it. The outer ctx (5 minutes from `cmd/tribunal/chain.go:201`) bounds wall time, not gas spend.

**Why it matters:** the recovery loop's termination guarantee is purely structural (len-bounded). It has no economic guarantee. The trio approving v0.3.2 didn't have to consider this because there was no recovery loop; v0.3.3's introduction of the loop opens an attacker-controlled gas-amplification vector via the LCD.

**Plan anchor:** intent.md "Trust boundaries" → "LCD endpoint is untrusted infrastructure." Per-attempt timeouts protect wall time. The recovery loop's gas cost was not analyzed.

**Severity:** Warning. The LCD is operator-controlled in practice (operators run their own LCD or trust a public one), so the _threat_ is operator-misconfiguration or compromised LCD, not unknown-adversary internet. Still: an attacker who compromises the LCD turns a 14M-gas batch into a 1.4B-gas wallet drain.

Hand to **reviewer-sec** for primary lens — the attack model is sec-territory. Filing here because the architectural decision ("recovery loop trusts the LCD's error log") is the cause.

**Suggested defense:** cap recovery iterations to a small constant (e.g. 5) instead of `len(commits)`. Real-world batched-tx atomicity failures happen because of ONE duplicate, occasionally two. Five is enough for any non-malicious case; beyond that, bail and let the operator re-run. Or: track the cumulative gas spend across the recovery loop and bail when it exceeds an operator-configured cap.

---

## Cross-Reviewer Ready Notes

- **For reviewer-sec:**
  - **F-ARCH-301 has a security angle:** the regex is parsing untrusted LCD output. A malicious LCD/xiond can craft a fake `raw_log` that says `finding P-X/F-attack already committed` to trick the recovery layer into dropping the wrong (legitimate) entry from the batch. The contract's actual duplicate guard never fired; the operator just lost a legitimate finding via client-side parser manipulation. Severity here is moderated by "the LCD is operator-trusted in practice" but you should consider whether the trust model is documented loudly enough.
  - **F-ARCH-307 is your lane primarily.** Recovery loop + hostile LCD + per-tx gas burn = real gas-amplification attack. The architectural cap is `len(commits)`. Sec lens should call out whether that bound is tight enough against a compromised-LCD threat model.
  - **`tribunal-seed`'s `--allow-prod` heuristic** is the same `looksLikeTestChain` used elsewhere. If you find the heuristic too permissive (e.g., a real chain id like `agoric-test-1` would be treated as test), the seed harness inherits that hole. Worth a check.
  - **No new signing surface introduced.** Recovery loop retries re-broadcast the same signed batch messages with the duplicate removed; the signatures inside each `FindingCommit` are bound to the (plan_id, finding_id, …) canonical message, not to the batch composition, so dropping an entry doesn't invalidate the rest. Verified at `internal/chain/messages.go:142` and `messages.go:177`.

- **For reviewer-perf:**
  - **F-ARCH-307's gas-amplification angle is perf-adjacent.** Even on a benign LCD, the recovery loop with a `len(commits)`-sized retry bound means worst-case wall time = N × (broadcast + WaitForTx). With WaitForTx's 1s poll cadence and typical 5s block time, a 100-element pathological batch is ~10 minutes of sync. Outer ctx is 5 minutes (`cmd/tribunal/chain.go:201`). Confirm whether the bound is right.
  - **Pre-flight 8-worker cap** at `MAX_BATCH_SIZE=100` means ~13 sequential rounds of 8 concurrent queries, each capped at 3s. Worst case ~39s. Confirm whether the worker count is the right knob or whether the contract should expose a `findings_by_plan` batch query.
  - **Progress note cadence (5s) doubles up under cancellation** because the progress goroutine in `preflight` doesn't check `ctx.Done()` — it only exits on `close(done)`. So a slow LCD + ctx cancel sees up to one extra progress note before the workers exit. Minor; only flagging because it's the kind of thing perf review tracks.

## Verdict

**Request Changes.**

One Critical (F-ARCH-301) and three Warnings (F-ARCH-302, F-ARCH-303, F-ARCH-306, F-ARCH-307) unresolved. The Critical is on the new recovery layer — the v0.3.3-equivalent of v0.3.2's F-NEW-301. The Warnings cluster around test coverage gaps and an incomplete CLI wiring of T7.

Specifically blocking on F-ARCH-301: a sync against any plan with a `/` in its plan_id or a space in any finding_id will hit the recovery-layer dead-end and either bail with "duplicate not in batch" (slashy plan) or fall back to the v0.3.2 give-up path (spacey finding). Operators using namespaced plan ids — a natural convention — are one un-tested input away from rediscovering F-NEW-301.

The fix is small. Anchor the regex to end-of-line, capture the whole `plan_id/finding_id` tuple, and split on the **structurally correct** delimiter — which means changing the contract's error format to use a separator no identifier can contain (recommended), or accepting that recovery can only handle the subset of identifiers that round-trip through the regex (and validating that subset client-side at commit time).

The trio approved v0.3.2 and missed a Critical (F-NEW-301). v0.3.3 fixes that Critical and introduces an architecturally analogous one. I'm submitting this as Request Changes precisely so the next iteration doesn't ship the same shape of bug a third time.

## FINDINGS-TO-FILE

```
critical|architecture|F-ARCH-301|<claim_hash_pending>|file:///home/dan/src/tribunal/internal/chain/sync.go#L300-L305|Recovery regex character classes break on identifiers containing / or space that the contract's validate_id_field permits, re-opening F-NEW-301 under realistic plan/finding id conventions
warning|architecture|F-ARCH-302|<claim_hash_pending>|file:///home/dan/src/tribunal/internal/chain/sync.go#L332-L337|submitCommitBatch and submitResolveBatch use a slice-aliasing in-place filter idiom whose correctness depends on a sharp Go semantic that future maintenance will likely break
warning|architecture|F-ARCH-303|<claim_hash_pending>|file:///home/dan/src/tribunal/cmd/tribunal/chain.go#L217-L220|SyncAll's partial-failure aggregation is correctly produced but the CLI caller discards the results slice on error, leaving the operator-visible behavior identical to v0.3.2
suggestion|architecture|F-ARCH-304|<claim_hash_pending>|file:///home/dan/src/tribunal/internal/chain/config.go#L18-L29|NormalizeRPCScheme is exported from an internal/ package; the public-surface concern in the plan is moot but the doc-comment should explain why
suggestion|architecture|F-ARCH-305|<claim_hash_pending>|file:///home/dan/src/tribunal/internal/chain/config.go#L101-L110|outcome_reward_multiplier=0 round-trip has no regression test; a future PR could silently re-introduce the auto-default
warning|architecture|F-ARCH-306|<claim_hash_pending>|file:///home/dan/src/tribunal/internal/chain/sync_test.go#L21-L78|TestMatchDuplicate_CommitErrorParsing covers only hyphen+dot identifiers and never exercises the / or space characters the contract permits, which is why F-ARCH-301 landed un-caught
warning|architecture|F-ARCH-307|<claim_hash_pending>|file:///home/dan/src/tribunal/internal/chain/sync.go#L316-L381|Recovery loop bounded only by len(batch) enables gas amplification against a hostile LCD; bound is structural, not economic
```
