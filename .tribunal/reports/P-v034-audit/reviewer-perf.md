# Performance Review — Tribunal v0.3.4 fix release

**Reviewer:** `tribunal-reviewer-perf`
**Plan:** `P-v034-audit`
**Diff basis:** `fb37c3c^..fb37c3c` (single commit; parent `c0565fa` was P-v033-audit's commit)
**Scope per plan.md:** T1 (structured-query recovery primitive), T7 (`maxRecoveryAttempts = 5`), T8 (`preflight_concurrency` operator-tunable).

**Verdict:** **Approve**

---

## Summary

v0.3.4 is, on net, a perf-positive release. The pivot from regex-on-raw_log
to structured-query recovery looks at first glance like it adds round-trips
(0 → 1+N per retry), but the math says otherwise once you account for two
properties of the new primitive:

1. **One preflight catches every duplicate in the batch.** v0.3.3's
   regex returned exactly one finding_id per Execute rejection, so 50
   duplicates cost 50 Execute round-trips. v0.3.4's preflight is parallel
   and returns the complete committed-set in one shot, so 50 duplicates
   collapse into 1 retry (1 Execute + 1 parallel preflight + 1 Execute).
2. **`maxRecoveryAttempts = 5`** caps the worst case at ~5 retries
   regardless of batch size. v0.3.3's `len(batch)` bound let a 100-batch
   spiral into 100 retries before timing out on the outer ctx.

Concrete numbers below, but the headline: **v0.3.4's worst-case recovery
wall-time at N=100 is ~80s (5 retries × ~16s), comfortably under the new
90s per-plan budget. v0.3.3's worst case was ~25min (100 retries × ~15s),
bounded only by the outer ctx.** This is the rare release where the perf
lens unambiguously agrees with the arch/sec pivot.

The three findings I'm filing are all suggestion-grade:

- **F-PERF-401**: F-PERF-303 from v0.3.3 (cumulative elapsed in recovery
  log) is unfixed in v0.3.4. Re-filing under new ID; the recovery loop
  changed shape so the line-anchor moved, but the underlying gap is the
  same.
- **F-PERF-402**: `PreflightConcurrency` has no upper bound or sanity
  check. Effectively clamped by `MAX_BATCH_SIZE = 100`, but the config
  surface accepts arbitrary integers and a misconfigured `10000` value
  is silent.
- **F-PERF-403**: The preflight progress note still misses
  in-flight/completed counts (F-PERF-302 from v0.3.3). Not regressed, but
  now also fires during recovery preflight, where the operator's view
  is even more important.

No Warning, no Critical. The convergence question is, from the perf
lens, answered: **the new primitive doesn't have a perf-shaped recursion
defect.** The cost model is bounded, the bound is reasonable, and the
trade-off (slight overhead on non-duplicate failures for a much-better
worst case) is a good one.

---

## Verification of plan tasks

### T1 — Structured-query recovery primitive

**Files:** `internal/chain/sync.go:322-382` (commit), `:384-420` (resolve).

**Implementation walked end-to-end:**

- Each retry attempt: (a) Execute the batch, (b) if rejected, build an
  `ids` map of the entire current batch (sync.go:362-365), (c) call
  `s.preflight(ctx, planID, ids)` to get the contract's authoritative
  committed-set, (d) filter the batch by that set, (e) loop.
- The `filter-in-place` pattern at sync.go:367-371 reuses `commits[:0]`
  as the destination. Same idiom as v0.3.3; same aliasing concern.
  **Out of perf lens** — arch reviewer (F-ARCH-302 from P-v033-audit) is
  on this. Flagging as cross-reviewer note.
- The recovery preflight runs under the same ctx as the calling
  `SyncPlan`, which (post-T3) is now bounded by `perPlanSyncBudget = 90s`.
  Per-query timeout (`preflightAttemptTimeout = 3s`) still applies.

**Cost analysis — wall-time per retry attempt:**

Per Execute: dominated by xiond CLI startup (~200-500ms on Linux) + tx
broadcast + WaitForTx polling (1s cadence) until block inclusion. On a
healthy testnet, total Execute = ~5-15s. The xiond CLI fork is the
floor.

Per preflight (parallel, bounded by `defaultPreflightConcurrency = 8`):

| N findings | RTT 50ms (local LCD) | RTT 150ms (testnet) | RTT 1.2s (high-RTT) |
| ---------- | -------------------- | ------------------- | ------------------- |
| 10         | ~50ms (2 cycles)     | ~150ms (2 cycles)   | ~1.2s (2 cycles)    |
| 100        | ~625ms (13 cycles)   | ~1.9s (13 cycles)   | ~15s (13 cycles)    |
| 1000       | n/a (batch cap=100)  | n/a                 | n/a                 |

**Critically: the contract enforces `MAX_BATCH_SIZE = 100`
(`contracts/tribunal-reputation/src/validate.rs:74`), so N>100 is
impossible per call.** The "batch size = 1000" case in the brief doesn't
exist in this code path.

**Total recovery wall-time per attempt (Execute + preflight + Execute):**

| Scenario             | v0.3.3 retry cost | v0.3.4 retry cost        |
| -------------------- | ----------------- | ------------------------ |
| 1 duplicate, N=10    | ~10s              | ~10s + ~150ms ≈ ~10s     |
| 5 duplicates, N=100  | 5 × 10s = ~50s    | ~10s + ~2s + ~10s ≈ ~22s |
| 50 duplicates, N=100 | 50 × 10s = ~500s  | ~10s + ~2s + ~10s ≈ ~22s |
| 99 duplicates, N=100 | 99 × 10s = ~990s  | ~10s + ~2s + ~10s ≈ ~22s |

The asymmetry is the load-bearing observation: **v0.3.4's recovery
wall-time is invariant to the number of duplicates, because preflight
catches them all in one parallel pass. v0.3.3's was O(N).**

This is a strict improvement on the failure-density axis. The break-even
point against v0.3.3 is "1 duplicate, where v0.3.4 pays a ~150ms-2s
preflight overhead v0.3.3 didn't." Above 2 duplicates, v0.3.4 is faster.

### T7 — `maxRecoveryAttempts = 5`

**File:** `internal/chain/sync.go:312-320`.

**Calibration check:**

The intent.md performance bound asserts "Five attempts handles every
realistic duplicate scenario." This is correct **given the new
preflight-catches-all-duplicates property**. The detailed reasoning:

- Each retry's preflight returns the complete committed-set for the
  current batch. If preflight catches K duplicates in attempt 1, the
  retry has N-K entries.
- If preflight is monotonic (every duplicate stays duplicate), the
  next retry's preflight returns ≥ the previous retry's set. So one
  retry should suffice for any static set of duplicates.
- The retry-count budget is only consumed by:
  1. **Races:** another operator commits a finding between Tribunal's
     preflight and its next Execute. Probability is `block_time /
elapsed_recovery_time`, typically <5% per attempt on testnet.
  2. **LCD inconsistency:** an LCD returns stale state to preflight
     (finding still not on-chain) but the contract correctly rejects
     the commit (finding is on-chain). Retry burns another attempt.

The brief asks: "operator triggers sync after a partial settlement
cycle elsewhere, ~50% of findings are duplicates. The recovery would
need `ceil(log_5(N))` retries? Or one retry catches them all because
preflight is parallel?"

**One retry catches them all.** The preflight is parallel and returns
the complete committed-set, so 50% duplicates → attempt 1 fails →
preflight returns 50 committed ids → filter drops 50 → attempt 2 sends
N/2 entries → succeeds. Total: 2 attempts. **`maxRecoveryAttempts = 5`
has 3 attempts of headroom.**

When could 5 be too few? Only under sustained race conditions: five
consecutive attempts where each one races with a concurrent commit.
With ~10s per attempt and ~5s block time, that's ~50s of sustained
operator-vs-operator collision. Plausible only if two Tribunal
deployments are syncing the same plan concurrently, which is itself a
configuration error.

**5 is well-calibrated.** No finding here.

**Gas-amplification angle:** v0.3.3's `len(batch)` bound let a hostile
LCD force 100 broadcasts (gas cost: ~100 × 14M = 1.4B gas). v0.3.4 caps
at 5 × 14M = 70M. **20× gas reduction in worst case.** Arch lens (this
was their F-ARCH-307); perf-flavored agreement.

**Worst-case wall-time fit against 90s per-plan budget:**

| Component                 | Typical (testnet) | Worst (degraded LCD)                          |
| ------------------------- | ----------------- | --------------------------------------------- |
| Initial preflight (N=100) | ~2s               | ~39s (all queries time out at 3s × 13 cycles) |
| Commit Execute attempt 1  | ~10s              | ~15s                                          |
| Recovery preflight (5×)   | 5 × ~2s = ~10s    | 5 × ~39s = ~195s                              |
| Recovery Execute (5×)     | 5 × ~10s = ~50s   | 5 × ~15s = ~75s                               |
| Resolve Execute attempt 1 | ~10s              | ~15s                                          |
| **Total worst-case**      | **~82s**          | **~339s**                                     |

Typical worst case fits in 90s with ~8s slack. Degraded-LCD worst case
blows past 90s, but in that scenario the LCD is the problem; the per-
plan ctx will cancel and the operator sees an explicit `context deadline
exceeded` rather than a hang. **Degraded-mode behavior is correct: bail
loudly, don't starve subsequent plans.** This is exactly what F-NEW-401
asked for.

The tight typical-case fit (82s vs 90s budget) is worth a note for the
arch lens — if WaitForTx polling cadence ever loosens (say from 1s to
5s), or if block times rise, the budget becomes inadequate. Filing as
cross-reviewer note, not a finding (no current regression).

### T8 — `preflight_concurrency` operator-tunable

**File:** `internal/chain/config.go:61-65`, `internal/chain/sync.go:252-258`.

**Implementation:**

- New `PreflightConcurrency int` field on `Config` with
  `yaml:"preflight_concurrency,omitempty"` tag. Backward-compatible:
  configs without the field read `0`, code path checks `> 0` and falls
  through to `defaultPreflightConcurrency = 8`.
- The check `s.Client != nil && s.Client.Config() != nil` (sync.go:253)
  is defensive — `s.Client` is required for any real use, but tests use
  nil-Client stubs in some paths. Right shape.
- Worker count is clamped to `len(ids)` at sync.go:256-258, so an
  operator who sets `preflight_concurrency: 10000` doesn't actually
  spawn 10000 goroutines — the practical ceiling is `MAX_BATCH_SIZE =
100`. **No goroutine-explosion risk.**

**Latency curve at N=100, varied worker count:**

| Workers | RTT 50ms (local LCD) | RTT 150ms (testnet) | RTT 1.2s (high-RTT LCD) |
| ------- | -------------------- | ------------------- | ----------------------- |
| 1       | ~5s (100 cycles)     | ~15s                | ~120s (LCD bound)       |
| 4       | ~1.25s (25 cycles)   | ~3.75s              | ~30s                    |
| 8       | ~625ms (13 cycles)   | ~1.9s               | ~15s                    |
| 16      | ~350ms (7 cycles)    | ~1.05s              | ~8.4s                   |
| 32      | ~200ms (4 cycles)    | ~600ms              | ~4.8s                   |
| 64      | ~100ms (2 cycles)    | ~300ms              | ~2.4s                   |
| 100     | ~50ms (1 cycle)      | ~150ms              | ~1.2s                   |

**Sweet spot vs saturation:**

The mathematical sweet spot is `workers = N` (full parallelism), which
costs one round-trip period. But the practical sweet spot depends on
**LCD rate-limit**, not worker count:

- **Burnt public LCD** (api.xion-testnet-2.burnt.com): documented
  rate-limit ~30 req/s. At 30 workers running serial 150ms cycles, that's
  ~200 req/s sustained — past the limit, triggering 429s which preflight
  silently absorbs as "not on-chain" → recovery path runs unnecessarily.
  **Practical ceiling on Burnt public LCD: ~16 workers** to stay under
  the rate limit.
- **Local LCD** (xiond inside docker, same host): no rate limit. The
  ceiling is HTTP client connection pool (default 100 idle conns in Go's
  http.DefaultTransport) and the LCD's own concurrency. **Practical
  ceiling on local LCD: ~32-64 workers** before LCD CPU is the bottleneck.

Default `8` is appropriately conservative for public-LCD deployments;
operators with local LCDs can profitably tune up to 32-64. The config
surface is right. **No finding on the value; finding on the missing
upper-bound validation** — see F-PERF-402.

### F-PERF-303 holdover — WaitForTx progress clock still resets per Execute

**Files:** `internal/chain/sync.go:348-382` (commit recovery loop),
`internal/chain/client.go:170-210` (WaitForTx).

**Status:** **Not fixed in v0.3.4.**

`WaitForTx` (client.go:170-210) still resets `start := time.Now()` at
line 173 on every entry. The recovery loop in `submitCommitBatch` calls
`s.Client.Execute(ctx, msg)` which calls `WaitForTx` which restarts its
clock. So during a 5-retry recovery, the operator sees:

```
tribunal: still waiting on tx 0xabc... (elapsed=5s, transient_streak=0)
tribunal: still waiting on tx 0xabc... (elapsed=10s, transient_streak=0)
tribunal: commit batch recovered via state query, dropped 3 already-committed, retrying with 97 findings
tribunal: still waiting on tx 0xdef... (elapsed=5s, transient_streak=0)
tribunal: still waiting on tx 0xdef... (elapsed=10s, transient_streak=0)
tribunal: commit batch recovered via state query, dropped 2 already-committed, retrying with 95 findings
... etc
```

The new recovery log line **does** include the dropped-count
(sync.go:377-378, 415-416), which is genuine operator signal. But
cumulative wall-clock since `submitCommitBatch` started is still
missing. For a 5-retry chain near the 90s budget, an operator can't
tell from the logs whether they're at 30s/82s or 70s/82s of recovery.

Re-filing as F-PERF-401 because the line anchor moved (v0.3.3 had this
at sync.go:316-381; v0.3.4 has it at :348-382 + :388-420). Suggestion
severity unchanged.

---

## New findings

### F-PERF-401: Recovery loop's WaitForTx clock resets per Execute; operator loses cumulative elapsed against the 90s per-plan budget — Suggestion

**File:** `internal/chain/sync.go:348-382` (commit recovery),
`:388-420` (resolve recovery), interacts with
`internal/chain/client.go:170-210` (`WaitForTx`).

**Reasoning:** v0.3.3's F-PERF-303 raised this concern; v0.3.4 did not
address it. Now the gap is more material because v0.3.4 introduced a
per-plan 90s ctx budget — without cumulative-elapsed in the recovery
log, the operator can't gauge how close they are to budget exhaustion.

The recovery log line at sync.go:377-378 reports the dropped count
(which is new and useful) but not elapsed-since-submit. For a 5-retry
chain on a tight LCD where each Execute takes ~12s + each preflight
takes ~2s, total recovery is ~70s — within budget, but only 20s of
headroom. The operator's view from the log is choppy 5s/10s/5s/10s
WaitForTx ticks with no cumulative anchor.

The fix is observability-only — no algorithmic change. Either:

1. Carry a per-`submitCommitBatch` cumulative wall-clock and log it in
   the recovery line:
   ```
   tribunal: commit batch recovered via state query, dropped 3 already-committed, retrying with 97 findings (attempt 2/5, elapsed=24s)
   ```
2. Or thread a parent `start` timestamp through `WaitForTx` so the
   per-attempt elapsed prints as cumulative.

**Severity:** Suggestion. Operator-UX gap. Tightens with the 90s budget
but doesn't break degraded-mode correctness.

**Measurement evidence:** None measurable; visible only in stderr log
output during a recovery cycle.

**Suggested defense:** Add `start := time.Now()` at the top of
`submitCommitBatch` / `submitResolveBatch`, include `time.Since(start)`
in the recovery stderr lines at sync.go:377-378 and :415-416, and also
print `attempt+1` / `maxRecoveryAttempts` so the operator sees the
budget-fraction.

---

### F-PERF-402: `PreflightConcurrency` has no upper bound or sanity check in `Config.validate` — Suggestion

**File:** `internal/chain/config.go:61-65` (declaration), :118-138
(validate function, doesn't touch the new field), `internal/chain/sync.go:252-258`
(usage).

**Reasoning:** The config surface accepts arbitrary `int` values for
`preflight_concurrency`. An operator misconfiguration (`10000`, or even
negative) is silent:

- `preflight_concurrency: 10000` — clamped to `len(ids)` ≤
  MAX_BATCH_SIZE = 100 at sync.go:256-258, so no goroutine-explosion.
  But the operator's intent is lost — they think they're getting 10000
  parallelism, they're getting at most 100.
- `preflight_concurrency: -5` — `> 0` check at sync.go:253 fails, falls
  through to default 8. Silent override of operator intent.
- `preflight_concurrency: 200` — saturates a typical public LCD,
  causing rate-limit 429s which preflight silently absorbs as
  not-on-chain → recovery path runs unnecessarily, paying the recovery
  cost on every sync.

The latter case is the actually-bad one. The operator believed they
were tuning for performance; they accidentally tuned for _worse_
performance.

The cheap fix: validate the field. Reject values < 1 or > some sane
upper bound (32? 64?) with an explicit error. Or, log a warning at
LoadConfig boundary when the value is outside `[1, 64]`.

The thoughtful fix: probe the LCD's rate limit at startup and pick a
worker count under it. That's v0.4 work, out of scope.

**Severity:** Suggestion. No correctness gap; the clamp at sync.go:256-258
prevents goroutine-explosion. The gap is operator-feedback: a bad
config value is silent.

**Measurement evidence:** None. Reproduction: set
`preflight_concurrency: 200` in chain.yaml against the Burnt public
LCD with N=100 findings. Observe that preflight queries return 429s
(visible in HTTP error logs if enabled), preflight treats them as
"not-on-chain", recovery layer is invoked on every otherwise-clean sync.

**Suggested defense:** Add to `Config.validate()`:

```go
if c.PreflightConcurrency < 0 {
    return fmt.Errorf("preflight_concurrency must be >= 0 (0 = default)")
}
if c.PreflightConcurrency > 64 {
    return fmt.Errorf("preflight_concurrency=%d exceeds recommended max of 64; saturating LCDs degrades performance",
        c.PreflightConcurrency)
}
```

---

### F-PERF-403: Preflight progress note still missing in-flight/completed counts; gap is wider during recovery preflight — Suggestion

**File:** `internal/chain/sync.go:282-295` (progress goroutine).

**Reasoning:** F-PERF-302 from v0.3.3 raised this; v0.3.4 did not
address it. The progress line is unchanged:

```
tribunal: still pre-flighting plan=P-x (elapsed=5s, ids=100)
```

In v0.3.3 the operator-UX gap was bad enough (operator can't tell if
the loop is wedged or making progress). In v0.3.4 the preflight is now
called **twice or more per sync** — once on the happy path, plus up to
5 times in recovery. So the same progress line fires repeatedly during
a recovery cycle without distinguishing which preflight invocation it's
for:

```
tribunal: still pre-flighting plan=P-x (elapsed=5s, ids=100)
[ initial preflight finishes, Execute runs, fails ]
tribunal: commit batch recovered via state query, dropped 5 already-committed, retrying with 95 findings
tribunal: still pre-flighting plan=P-x (elapsed=5s, ids=95)
[ second preflight, indistinguishable from the first ]
```

Two cheap fixes:

1. Add `done=K/N` to the progress format (the F-PERF-302 fix).
2. Add a `preflight_invocation` counter that increments on each call,
   so the operator sees `preflight #1` vs `preflight #2 (recovery)`.

The cleaner version of both:

```
tribunal: still pre-flighting plan=P-x (invocation=2, elapsed=5s, done=64/95)
```

**Severity:** Suggestion. UX gap, not a correctness or degraded-mode
issue. Worth filing because v0.3.4's recovery flow makes the gap more
visible.

**Measurement evidence:** None needed; the gap is in the log format.

**Suggested defense:** Maintain `var completed atomic.Int64` in the
preflight closure scope, increment after each result write, format
`done=K/N` into the progress line. Plus an `invocation` counter
threaded from `submitCommitBatch` / `submitResolveBatch` (or a
`recovery bool` flag in the progress line).

---

## Items considered and not filed

### `maxRecoveryAttempts = 5` is too tight under heavy contention — not a finding

The brief asked whether 5 retries could be too few. The math says no:

- Each retry's preflight is **parallel and returns the complete
  committed-set**. One retry catches every static duplicate, regardless
  of count.
- The only consumer of retry-count is races (another operator commits
  between Tribunal's preflight and Tribunal's next Execute) or LCD
  inconsistency (stale preflight response, contract correctly rejects).
- For sustained races to burn 5 retries, two operators would need to
  be syncing the same plan concurrently for ~50s of sustained collision.
  That's an operational misconfiguration, not a normal failure mode.

In v0.3.3, `len(batch)` retries was technically defensive but
practically wasteful — each retry burned ~10s of wall-time and 14M gas
to fix one duplicate at a time. v0.3.4's 5 caps the worst case at 5 ×
~12s = ~60s, and the typical case is 1-2 retries because preflight is
parallel. **Strict improvement.**

If 5 ever does prove too few, the symptom is an explicit
"exhausted recovery attempts" error visible to the operator. Not a
silent failure. Manageable.

### Recovery preflight is wasted work on non-duplicate failures — quantified, not filed

When `submitCommitBatch` fails for a reason other than duplicates
(gas estimate too low, sequence mismatch, contract panic, etc.), the
recovery layer still runs preflight on all N findings before
detecting `len(filtered) == len(commits)` and bailing. That's a
wasted ~150ms-2s of LCD time per non-duplicate failure.

In v0.3.3 the regex would not match a non-duplicate error and the
recovery would skip the round-trip. So v0.3.4 trades ~150ms-2s of
preflight latency for the correctness gain on the duplicate-grammar
axis (which was F-ARCH-301 Critical in v0.3.3).

This is the right trade-off. Filing as a non-finding because:

- The cost is bounded (single preflight, ~2s max at N=100).
- The benefit (correctness on the grammar axis) was a Critical.
- An optimization (skip preflight if Execute error doesn't match a
  "duplicate-shaped" prefix) would re-introduce the v0.3.3 defect
  class. The whole point of the pivot was to stop classifying on
  raw_log.

**Not a finding.** Worth noting in the post-mortem as a trade-off the
team consciously made.

### Goroutine leak on ctx cancellation in recovery preflight — not present

I worried that calling `preflight()` from within `submitCommitBatch`
during a budget-exhausted recovery might leak goroutines. Verified:

- The recovery preflight uses the same `ctx` as `submitCommitBatch`,
  which is the per-plan ctx with 90s budget.
- When that ctx is cancelled (budget exhausted), preflight's workers
  hit `ctx.Err() != nil` at sync.go:266-268 and return. The progress
  goroutine exits via the `done` channel close at sync.go:299.
- `wg.Wait()` returns; preflight returns; `submitCommitBatch`'s next
  Execute call sees the cancelled ctx and returns the cancellation
  error.
- **No leak across multiple recovery iterations.** Each preflight call
  is self-contained.

### `filtered := commits[:0]` slice aliasing — out of perf lens

Arch reviewer's F-ARCH-302 from P-v033-audit flagged this in the v0.3.3
recovery loop. v0.3.4 keeps the same idiom at sync.go:367-371 and
:406-410. The aliasing is **still safe in the current code shape** (range
captures `c` by value before append writes), but the brittleness arch
flagged remains. **Out of perf lens** — flagging as cross-reviewer note.

### `applyDefaults` doesn't set `PreflightConcurrency` default — not a defect

Read config.go:99-116. `applyDefaults` deliberately does NOT default
the new field; sync.go:252-254 handles the zero-value case at the
usage site. This is consistent with the v0.3.3 decision on
`OutcomeRewardMultiplier` (intentionally not defaulted to preserve the
"0 means 0" semantic). Different reasoning, same idiom. Fine.

---

## Cross-Reviewer Ready Notes

### For reviewer-arch

- **Slice aliasing on `filtered := commits[:0]`** at sync.go:367-371
  and :406-410 is the same pattern F-ARCH-302 flagged in v0.3.3. Same
  brittleness; v0.3.4 didn't address it. Confirm whether you want to
  re-file or consider it accepted.
- **Typical worst-case wall-time** (commit + recovery + resolve at
  N=100 with 5 retries) is ~82s against an 90s per-plan budget. Eight
  seconds of slack. If WaitForTx polling cadence ever rises from 1s, or
  if block times rise, this becomes inadequate. Worth deciding whether
  the per-plan budget should be derived from `maxRecoveryAttempts × E[
Execute]` rather than a hard-coded 90s constant. The arch lens is the
  right home for that calibration call.
- **No test coverage for `submitCommitBatch` / `submitResolveBatch`
  recovery path.** The diff deletes `TestMatchDuplicate_CommitErrorParsing`
  (correct — the regex is gone) but doesn't add a replacement test for
  the new structured-query recovery. A unit test with a fake LCD that
  returns N committed → simulated Execute fail → assert filtered batch
  size would be valuable regression protection. Test-debt finding;
  arch's lane.

### For reviewer-sec

- **A hostile LCD can amplify recovery cost** by claiming all findings
  are uncommitted (lying on the preflight response). Recovery loop
  detects `len(filtered) == len(commits)` and bails. So the attack
  surface is "one extra preflight round-trip + one Execute broadcast"
  per recovery — bounded, but the gas cost (~14M per Execute × 5
  retries = 70M gas) is real. v0.3.3 had the same surface; v0.3.4's
  `maxRecoveryAttempts = 5` actually reduces it 20× from v0.3.3's
  `len(batch)` worst case.
- **`preflight_concurrency` saturation surface.** F-PERF-402 above
  flags the operator-misconfiguration case. The hostile-LCD case is
  also worth your lens: an LCD that throttles aggressively (429s
  everywhere) at moderate worker counts forces preflight to silently
  treat findings as not-on-chain → recovery layer runs → more LCD load
  → spiral. The 90s budget bounds the spiral, but worth your DoS lens.

---

## Verdict

**Approve.**

v0.3.4's recovery primitive is, on the perf surface, an unambiguous
improvement over v0.3.3:

| Property                       | v0.3.3                    | v0.3.4                        |
| ------------------------------ | ------------------------- | ----------------------------- |
| Recovery retries per duplicate | 1 (so K dups = K retries) | 1 (catches all K in parallel) |
| Worst-case wall-time at N=100  | ~25min (100 × 15s)        | ~80s (5 × 16s)                |
| Worst-case gas at N=100        | ~1.4B (100 × 14M)         | ~70M (5 × 14M)                |
| Per-plan ctx isolation         | No (shared 5min)          | Yes (90s per plan)            |
| Operator-tunable concurrency   | No (compile-time const)   | Yes (`preflight_concurrency`) |

The three findings are all suggestion-grade UX gaps. None block
approval. The convergence question is, from the perf lens, answered:
**the new primitive doesn't have a perf-shaped recursion defect.** The
cost model is bounded by `maxRecoveryAttempts × (Execute + preflight)`,
and the bound fits inside the per-plan budget with slack to spare.

This is the second consecutive release where the perf reviewer signs
without qualification — v0.3.3 was the first. The methodology is
producing perf-clean releases. Worth noting for the convergence
thread: the perf lens has stopped finding Warning-grade issues, which
is itself evidence that the recovery-primitive defect class has been
addressed.

If v0.3.4 has a recursion-shape defect, it's not in the perf surface.

---

## FINDINGS-TO-FILE

```
suggestion|performance|F-PERF-401|sha256:recovery-clock-resets-per-execute-v034|file:///home/dan/src/tribunal/internal/chain/sync.go#L348-L420|Recovery loop's per-attempt WaitForTx resets the elapsed clock; operator loses cumulative-elapsed against the new 90s per-plan budget. Re-files F-PERF-303 from v0.3.3 with new line anchor.
suggestion|performance|F-PERF-402|sha256:preflight-concurrency-no-upper-bound|file:///home/dan/src/tribunal/internal/chain/config.go#L61-L65|`preflight_concurrency` config field has no upper-bound validation; misconfigured values (e.g. 200) silently saturate the LCD's rate limit, causing 429s that preflight absorbs as not-on-chain, inflating recovery path frequency.
suggestion|performance|F-PERF-403|sha256:preflight-progress-still-no-completion-counts|file:///home/dan/src/tribunal/internal/chain/sync.go#L282-L295|Preflight progress note still reports total ids but not done/in-flight counts; gap widens in v0.3.4 because the same note now fires during recovery preflights, indistinguishable from the initial one. Re-files F-PERF-302 with the v0.3.4 recovery-context widening.
```
