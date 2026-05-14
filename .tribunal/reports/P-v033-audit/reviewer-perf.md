# Performance Review — Tribunal v0.3.3 fix release

**Reviewer:** `tribunal-reviewer-perf`
**Plan:** `P-v033-audit`
**Diff basis:** `5cc1634^..5cc1634` (single commit; parent `6b71e78` was the P-v032-audit dogfood commit)
**Scope per plan.md:** T4 (parallel pre-flight) and T8 (observability).

**Verdict:** **Approve**

---

## Summary

v0.3.3 lands every perf-shaped finding from the P-v032-audit pass with the
right shape:

- **F-PERF-201** (`WaitForTx` aborts on transient LCD error) — fixed.
  `fetchTx` now returns a `terminal` flag; only 4xx-not-404 and on-chain
  `code != 0` are terminal. 5xx, network errors, body-read failures, and
  partial-body JSON parse failures all surface as transient with
  `terminal=false`, and the loop continues. `internal/chain/client.go:154-194`.
- **F-PERF-202** (doc lies about 300ms per-attempt timeout) — fixed.
  Per-attempt deadline now real and accurate: `fetchTxAttemptTimeout =
3 * time.Second` at `client.go:79`, applied via `context.WithTimeout` at
  `client.go:203`. Doc comment at `:144-153` now describes actual behavior.
- **F-PERF-203** (N serial pre-flight queries) — fixed. `preflight` fans
  out to 8 workers via a fixed-size goroutine pool at `sync.go:222-298`.
- **F-PERF-204** (zero observability during multi-minute waits) — fixed.
  Both `WaitForTx` (`client.go:180-184`) and `preflight` (`sync.go:269-283`)
  emit one stderr line every 5s while the loop is alive.
- **F-PERF-205** (`tribunal-seed` uses `context.Background`) — fixed.
  `cmd/tribunal-seed/main.go` now wraps the harness ctx in
  `context.WithTimeout` with a configurable flag (out-of-scope for the
  perf lens; reviewer-arch confirms shape).
- **F-PERF-206** (1s first-poll floor) — explicitly **not addressed**.
  The intent.md performance bounds still accept the floor as nominal
  ("1–2s on successful Execute"), so this is a non-finding for v0.3.3.

The mechanism is correct, the goroutine pool is well-bounded, and the
degraded-mode posture is dramatically better than v0.3.2. Three things
I'd still call out — all suggestion-grade, none blocking:

1. The 8-worker constant is a private compile-time const with no
   per-deployment knob. For a Tribunal instance pointed at an LCD with
   high p50 RTT, the cap is a hard ceiling on pre-flight throughput.
   Default is fine; tunability is the gap.
2. The pre-flight progress note prints `ids=N` (the total) but not the
   in-flight / completed counts. An operator staring at the line every
   5s can't tell if the loop is stuck or making forward progress.
3. The recovery loop in `submitCommitBatch` / `submitResolveBatch`
   re-enters `Execute` → `WaitForTx` each iteration, and each entry
   gets its own fresh `start` / `lastProgress` clock. So total operator
   silence per recovery cycle is bounded by `2 * waitProgressInterval`
   (worst case: tx broadcasts in 4s and Execute returns, then the next
   Execute's WaitForTx burns 5s before its first progress note). Not
   spam, but the operator's view of "elapsed" resets per attempt
   without a cumulative wall-time signal.

No `Approve` blockers. All three findings are suggestions.

---

## Verification of plan tasks

### T4 — Parallel pre-flight with bounded fan-out + per-query timeout

**File:** `internal/chain/sync.go:222-298`.

Implementation walked end-to-end:

- 8 workers via `preflightConcurrency` (`sync.go:58`). Workers spun up
  exactly once, not per id (no goroutine-per-id explosion). `:250-267`.
- Worker count clamped to `len(ids)` for small batches (`:243-246`) —
  no idle goroutines on N=1.
- `idCh` is fully buffered (`make(chan string, len(ids))`) and pre-loaded
  before `close(idCh)` (`:235-239`). Producers never block; workers drain
  via `for id := range idCh`. Loop exits cleanly when the channel is
  drained.
- Per-query timeout via `context.WithTimeout(ctx, preflightAttemptTimeout)`
  with explicit `cancel()` after each attempt (`:257-259`). The cancel
  is inside the loop, not deferred — no per-id timer accumulation on
  the worker goroutine.
- `ctx.Err()` checked at the **top** of each iteration (`:254-256`),
  so a cancelled outer ctx breaks the worker out of its loop without
  starting a new attempt. Workers return cleanly via `defer wg.Done()`.
- `resCh` is fully buffered (`make(chan result, len(ids))`) so workers
  never block on send even if the consumer hasn't started yet (`:241`).

Goroutine lifecycle — clean termination paths verified:

| scenario                       | termination path                                                                                                                                                                                               |
| ------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| all ids drained successfully   | workers exit `for id := range idCh`, `wg.Done`, `wg.Wait` returns, `close(done)`, progress goroutine sees `<-done` and returns.                                                                                |
| outer ctx cancelled mid-flight | each worker hits `ctx.Err() != nil` at top of next iteration, returns. Unprocessed ids stay in `idCh`; since the channel is buffered nothing blocks. `wg.Wait` returns, progress goroutine exits. **No leak.** |
| Finding query panics           | not recovered — workers don't have a recover; that's an arch concern (would crash the process). Out of scope for perf.                                                                                         |

The `committed` / `resolved` maps are written from the **main** goroutine
after `close(resCh)` (`sync.go:289-296`). No concurrent map writes, no
race. Confirmed by `go vet ./...` (clean) and inspection.

Cost calculus on a typical xion devnet (~150ms LCD round-trip):

| N findings           | v0.3.2 serial | v0.3.3 parallel (8 workers) |
| -------------------- | ------------- | --------------------------- |
| 5                    | 750ms         | 150ms                       |
| 8                    | 1.2s          | 150ms                       |
| 16                   | 2.4s          | 300ms                       |
| 100 (MAX_BATCH_SIZE) | 15s           | ~1.9s                       |

For a "no-op re-sync" with N=100 already-settled findings (intent.md
scenario #3), v0.3.3 collapses ~15s of wall time into ~2s. The F-PERF-203
defect from v0.3.2 is fully retired.

### T8 — Progress notes in `WaitForTx` + pre-flight

**Files:** `internal/chain/client.go:180-184`, `internal/chain/sync.go:269-283`.

`WaitForTx` progress note:

- Fires once every `waitProgressInterval` (5s) of elapsed wait
  (`client.go:180-184`).
- Includes `txhash`, `elapsed`, and `transient_streak`. The
  `transient_streak` is genuinely useful operator signal — "we're
  4s deep and 4 consecutive HTTP attempts have failed" tells the
  operator the LCD is sick, not the chain.
- `transientStreak` is correctly **reset on the not-found-but-no-error
  path** (`client.go:176-178`, the `else` branch when `ok=false &&
err=nil`). This was the specific concern raised in the task brief —
  the streak is bounded by `ctxBudget / pollCadence` ≈ 300 for a 5min
  sync, far from int overflow. Confirmed reset on success.

Pre-flight progress note:

- Fires once every `waitProgressInterval` (5s) of elapsed wait
  (`sync.go:269-283`).
- Includes `planID`, `elapsed`, `len(ids)`. **Missing: in-flight /
  completed counts.** See F-PERF-301.
- The progress goroutine is correctly closed via the `done` channel
  (`:270, :287`). No leak.

There is one minor non-finding here: the `lastProgress` field in
`WaitForTx` resets after each print, so the cadence is "every 5s of
new elapsed wait", not "at 5s, 10s, 15s, 20s ...". They're mostly
the same in practice (tick is 1s, drift is <1s), but the doc could
be sharper. Pre-tax suggestion only; not filing.

---

## New findings

### F-PERF-301: Pre-flight worker cap is a private constant with no operator override — Suggestion

**File:** `internal/chain/sync.go:55-58`.

**Reasoning:** `preflightConcurrency = 8` is a compile-time constant.
For the typical xion testnet path (LCD round-trip ~150ms, MAX_BATCH_SIZE
= 100), 8 is well-calibrated: pre-flight completes in ~2s, the LCD isn't
saturated, and operator wall-time is good. But:

- For an LCD with high p50 RTT (e.g. a remote endpoint behind a CDN,
  measured at 800ms-1.2s in some xion deployments), N=100 with 8 workers
  is `(100/8) * 1.2s ≈ 15s` — back to the v0.3.2 wall-time regime.
- For an LCD that the operator is running locally (xiond inside docker
  on the same host, sub-10ms RTT), 8 workers is over-conservative;
  16–32 would land pre-flight in <100ms even for N=100.
- The constant is private (lowercase, no exported alias), so even
  power-users can't tune it without recompiling.

Compare to `fetchTxAttemptTimeout` and `preflightAttemptTimeout` —
also constants, but their values (3s) are documented and justified in
the comment above each. The pre-flight cap should at minimum be
**exported** so the package's godoc surfaces the value, and ideally
**configurable** via `chain.yaml` (`preflight_concurrency: 8` would
be a natural fit).

**Severity:** Suggestion. Default is fine; this is about giving the
operator a knob when the default doesn't fit.

**Measurement evidence:** None in the diff. Reproduction:
`time tribunal chain sync --plan P-x` on a plan with N=100 findings
against an LCD whose `/cosmwasm/wasm/v1/contract/.../smart/...` p50 is
≥800ms. Expected wall-time: `ceil(100/8) * 800ms ≈ 10s`.

**Suggested defense:** Promote the constant to `Sync.PreflightConcurrency`
or add `preflight_concurrency` to `Config` with a default of 8. Document
the trade-off in the godoc.

---

### F-PERF-302: Pre-flight progress note misses in-flight / completed counts — Suggestion

**File:** `internal/chain/sync.go:269-283`.

**Reasoning:** The progress line is:

```
tribunal: still pre-flighting plan=P-x (elapsed=5s, ids=100)
```

This tells the operator the loop is alive, but **not** how far in it
is. An operator seeing the same line at 5s, 10s, 15s, 20s ... has no
signal as to whether one worker is wedged on a slow LCD attempt or the
whole pool is making forward progress.

The cheap fix: maintain an atomic counter incremented by each worker
after a successful (or tolerated-failure) `Finding` call. Add the
count to the progress format:

```
tribunal: still pre-flighting plan=P-x (elapsed=5s, done=72/100)
```

That's the difference between "the loop is alive" (current state) and
"the loop is making progress" (the F-PERF-204 fix's actual operator
intent).

By contrast, the `WaitForTx` progress note **does** provide forward-
progress signal via `transient_streak` (so the operator can see "5s
elapsed, 5 transient HTTP failures" = LCD is broken; vs "5s elapsed,
0 transient failures" = chain is just slow).

**Severity:** Suggestion. Cosmetic-but-real operator UX gap.

**Measurement evidence:** None needed; the gap is in the log format,
not in measurable runtime behavior.

**Suggested defense:** Add a `var completed int64` (or
`atomic.Int64`) to the preflight closure scope. Workers increment after
each result write. Progress goroutine prints
`atomic.LoadInt64(&completed)` in the format.

---

### F-PERF-303: Recovery loop resets WaitForTx progress clock per attempt; operator loses cumulative elapsed signal — Suggestion

**File:** `internal/chain/sync.go:316-347` (commit), `:352-381` (resolve),
interacts with `internal/chain/client.go:154-194` (`WaitForTx`).

**Reasoning:** When `submitCommitBatch` recovers from a
`FindingAlreadyCommitted` rejection and retries, it calls
`s.Client.Execute(ctx, msg)` again. `Execute` calls `WaitForTx`, and
`WaitForTx` resets its own `start` and `lastProgress` clocks
(`client.go:157-158`). The operator sees:

```
tribunal: still waiting on tx 0xabc... (elapsed=5s, transient_streak=0)
tribunal: still waiting on tx 0xabc... (elapsed=10s, transient_streak=0)
[ recovery layer drops F-99, retries ]
tribunal: commit batch recovered from duplicate P-1/F-99, retrying with 99 findings
tribunal: still waiting on tx 0xdef... (elapsed=5s, transient_streak=0)
```

The "elapsed=5s" in the second wait is per-Execute, not cumulative-since-
sync-started. For a worst-case recovery chain of 100 retries
(theoretical max from `for attempt := 0; attempt <= originalLen`), the
operator could see "elapsed=5s" 100 times in a row without any signal
that 100 retries have happened in a row. The recovery log lines DO print,
so the operator can count them — but the wait-loop signal is misleading.

This won't happen in practice — a 100-retry recovery chain would imply
99 dropped findings out of 100, which is a corpus drift or operator
misconfiguration, not a normal sync. But for the **bounded-but-not-tight**
case (3-5 duplicates in a 100-finding batch), the operator's progress
view is choppy: "5s … recovered … 5s … recovered … 5s …" instead of
"15s elapsed, recovered 2".

The fix is observability-only — no algorithmic change. Either:

1. Carry a per-`submitCommitBatch` cumulative-wall-clock and log it
   in the recovery line:
   ```
   tribunal: commit batch recovered from duplicate P-1/F-99 (attempt 2, total_elapsed=12s, retrying with 98 findings)
   ```
2. Or add a `WaitForTx`-shaped helper that threads a parent `start`
   timestamp so the per-attempt elapsed prints as cumulative.

**Severity:** Suggestion. Operator-UX gap, not a correctness or
degraded-mode issue. The F-NEW-301 fix's correctness is unaffected.

**Measurement evidence:** None measurable; visible only in stderr log
output during a recovery cycle.

**Suggested defense:** Add `start := time.Now()` at the top of
`submitCommitBatch` / `submitResolveBatch`, and include
`time.Since(start)` in the recovery stderr lines at `sync.go:342-343`
and `:376-377`.

---

## Items considered and not filed

### `transientStreak` overflow — not a concern

The task brief asked whether `transientStreak` (`client.go:159`) is
bounded. Audit:

- It's an `int` (platform-native, 64-bit on linux/amd64).
- Incremented once per LCD attempt that returns a transient error.
- Reset to 0 in the `else` branch when `ok=false && err=nil`
  (`client.go:176-178`).
- Reset to 0 implicitly when the function returns on success
  (`:175`) or on terminal error (`:165`).

Maximum streak depth = `ctxBudget / pollCadence` = `5min / 1s` ≈ 300
for `chain sync`, or `2min / 1s` ≈ 120 for `tribunal-seed`. The
counter can never exceed `ctxBudget / 1s` because each iteration
includes a `<-ticker.C` 1s wait. Far below `MaxInt64`. **No overflow
risk.**

### Goroutine leak on ctx cancellation — not present

The task brief asked whether the worker pool could leak goroutines on
ctx cancellation. Audit:

- Workers iterate `for id := range idCh`. The channel is fully
  pre-loaded and `close(idCh)` runs before any worker starts
  (`sync.go:235-239`). So workers always exit eventually when they
  drain.
- On ctx cancellation, the `if ctx.Err() != nil { return }` check at
  `:254-256` causes workers to return early. Unprocessed ids stay in
  the channel — that's fine, the channel is buffered and will be GC'd.
- `wg.Wait()` (`:285`) waits for all workers to return.
- The progress goroutine waits on `<-done` (`:275-277`); `done` is
  closed after `wg.Wait` (`:287`). So the progress goroutine exits
  exactly once.
- No goroutine references survive function return. **Clean.**

### 3s per-attempt timeout vs large response payloads — not a concern for current contract surface

The task brief flagged whether 3s is tight for "big leaderboards". The
contract's query surface used in this code path is:

- `Finding(plan_id, finding_id)` — returns one finding's data (signature,
  metadata, optional resolution). Worst-case payload is a few KB.
- `fetchTx(txhash)` — returns one tx response (logs + result). For a
  CommitFindingBatch of N=100, the tx response is dominated by N
  signatures + indexed events; even a worst-case ~20KB body comfortably
  transfers in <500ms on a healthy LCD.

3s per attempt is well-calibrated. The contract does not currently
expose a "list all findings for plan" query (that's the v0.4 ask
reviewer-perf raised in the v0.3.2 cross-reviewer notes). If such a
query is added in v0.4 with N=10000 leaderboard responses, the 3s
budget should be re-examined. For v0.3.3's surface, it's fine.

### 8-worker cap on huge batches — not a concern given MAX_BATCH_SIZE=100

The task brief flagged whether the cap should scale with batch size.
Constraint: the contract's `MAX_BATCH_SIZE = 100`
(`contracts/tribunal-reputation/src/validate.rs:74`). So pre-flight is
always operating on at most 100 ids per plan. With 8 workers at 150ms
RTT, that's ~13 sequential dispatch cycles ≈ 2s — well within the
5-minute ctx budget. **The cap is right for the current contract surface.**

If MAX_BATCH_SIZE ever grows, F-PERF-301 (operator-tunable concurrency)
becomes more important. Filed as suggestion.

---

## Cross-Reviewer Ready Notes

### For reviewer-arch

- **`Sync.preflight` is now a private method on the Sync struct.** It's
  the right factoring (the v0.3.2 reviewer-arch note about "pre-flight
  is plausibly its own function" was acted on). Worth confirming the
  arch lens is happy with the signature — `(committed, resolved map[string]bool)`
  return is map-of-bool instead of map-of-struct, which is the
  marginally-less-efficient idiom but matches v0.3.2 for diff clarity.
- **`preflightConcurrency = 8`** is a magic number. F-PERF-301 asks
  arch to weigh in on whether to expose it on `Config` or `Sync` —
  perf says "yes please" but the call lives in arch's lane.
- **Recovery loop bound** at `sync.go:318` (`for attempt := 0; attempt
<= originalLen`) is `originalLen + 1` iterations max. Each iteration
  with a duplicate hit drops at least one finding (or bails with
  "duplicate not in batch"), so termination is guaranteed in
  `len(commits)` retries. Worst case wall-time = `len(commits) *
(broadcast+wait)` ≈ `100 * 3s = 300s` ≈ 5 minutes. That exactly
  saturates the default sync ctx. Arch should consider whether the
  outer ctx budget should be sized for `2 * MAX_BATCH_SIZE * E[wait]`
  to leave headroom for a worst-case recovery chain.

### For reviewer-sec

- **`fetchTx` transient/terminal split** is now the security-relevant
  surface: a hostile LCD can no longer kill sync with a one-shot 502.
  But a hostile LCD that returns 502 **forever** burns the full ctx
  budget while transient_streak counts up. The progress log makes this
  visible to the operator, but doesn't programmatically bail out. Worth
  reviewer-sec asking whether "infinite transients" should fail-fast at
  some threshold (e.g. 30 consecutive transients = LCD is broken,
  bail). Perf says no — let the operator's ctx be the budget. Sec lens
  may disagree.
- **Pre-flight error tolerance** (workers downgrade Finding errors to
  `not-on-chain`) interacts with the recovery layer (`submitCommitBatch`
  re-submits would-be-duplicates). The combined posture is more robust
  than v0.3.2, but a hostile LCD can deliberately force the recovery
  path to run, burning gas. The contract's duplicate guard catches it
  before any state change lands, but the operator pays for the failed
  broadcast (~0.01 XION). Worth reviewer-sec costing-out.

---

## Verdict

**Approve.**

Every Warning-grade perf finding from P-v032-audit is resolved with
the right shape (per-attempt timeouts in place, parallel pre-flight
bounded and well-terminated, progress signal where there was silence).
The three new suggestions are operator-UX polish, not degraded-mode
regressions:

- F-PERF-301: expose the worker cap as a config knob.
- F-PERF-302: include in-flight count in pre-flight progress.
- F-PERF-303: thread cumulative elapsed through the recovery log.

No Critical or Warning open. Approve clean.

This is the first release where the perf reviewer can sign without
qualification — v0.3.0 had no chain wait, v0.3.1 added inadequate
classification, v0.3.2 was silent-and-serial, v0.3.3 is bounded-and-
observable. Worth noting for the methodology-meta thread: the perf
lens added genuine value in v0.3.2 and the fix release closed every
finding without introducing perf regressions. That's the "doesn't
create new ones" property intent.md asks for.

---

## FINDINGS-TO-FILE

```
suggestion|performance|F-PERF-301|sha256:preflight-concurrency-not-tunable|file:///home/dan/src/tribunal/internal/chain/sync.go#L55-L58|Pre-flight worker cap is a private compile-time constant with no per-deployment override; high-RTT or local-LCD operators have no knob
suggestion|performance|F-PERF-302|sha256:preflight-progress-missing-completion|file:///home/dan/src/tribunal/internal/chain/sync.go#L269-L283|Pre-flight progress note reports total ids but not in-flight/completed counts, so operator can't tell if the loop is wedged or making progress
suggestion|performance|F-PERF-303|sha256:recovery-loop-resets-progress-clock|file:///home/dan/src/tribunal/internal/chain/sync.go#L316-L381|Recovery loop's per-attempt WaitForTx resets the progress clock; operator loses cumulative elapsed signal across a multi-retry recovery cycle
```
