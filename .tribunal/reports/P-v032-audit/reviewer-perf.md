# Performance Review — Tribunal v0.3.2 tooling fixes

**Reviewer:** `tribunal-reviewer-perf`
**Plan:** `P-v032-audit`
**Diff basis:** `HEAD~1..HEAD` (commit `f186e92`)
**Scope per plan.md:** T3 (`Execute` wait-for-inclusion) and T4 (sync pre-flight chain-state filter).

**Verdict:** **Request Changes**

---

## Summary

v0.3.2 adds two new latency sources to every on-chain settlement:

1. A REST polling loop after every `Execute` (T3). Minimum added latency is ~1s per tx; maximum is bounded only by the caller's ctx.
2. A serial pre-flight `Finding` query per unique finding before every `SyncPlan` (T4). N sequential round-trips, even on a fully-settled (no-op) plan.

The mechanism is correct and the goroutine lifecycle is clean — `time.Ticker` is stopped, no leaks. But four things are wrong for production use:

- The polling loop **bails on the first transient LCD error**, which is the exact failure mode F4 was supposed to make sync resilient to (`commit landed, resolve LCD blip kills the wait → caller thinks resolve failed → re-runs sync → pre-flight should clean up, but only if the next LCD call works`). The retry loop has no transient-error tolerance.
- The pre-flight is **strictly serial** with no parallelism, no batched query, and no fast-path for "ledger is already empty for this plan." Every `chain sync` re-run now pays N × round-trip even when there is nothing to do.
- **There is zero observability** while the wait loop is running. The operator's CLI hangs silently for up to the ctx deadline (5 minutes for `chain sync`, ∞ for `tribunal-seed --send`).
- The **doc comment lies** about the per-attempt timeout — it says "300ms" but the code uses the shared `c.http` client with a 30s `Timeout`. A single hung LCD attempt can burn 30s out of the 5-minute sync budget.

No `Approve` while these are open. F-PERF-201 and F-PERF-204 are warnings (real regressions in degraded-mode behavior); F-PERF-202, F-PERF-203, F-PERF-205 are suggestions for the v0.3.3 follow-up.

---

## Verification of plan tasks

### T3 — `Execute` polls for tx inclusion via REST (`internal/chain/client.go`)

The implementation does what the intent doc requires under happy-path conditions:

- `Execute` blocks until `fetchTx` returns `found=true` or ctx fires (`client.go:117-119`, `:129-151`).
- `fetchTx` correctly maps 404 → "keep polling" (`client.go:174-176`) and `height==""` 200 → "keep polling" (`client.go:190-195`), which lines up with intent.md's edge-case bullets.
- On-chain `code != 0` is surfaced as a `WaitForTx` error (`client.go:138-141`).
- `time.Ticker` is stopped via `defer ticker.Stop()` (`client.go:131`) — no goroutine or timer leak.
- ctx cancellation produces a wrapped `ctx.Err()` (`client.go:144-146`) — intent.md edge case met.

What is **not** met:

- intent.md failure mode says "`/cosmos/tx/v1beta1/txs/{hash}` always 404 → polls until ctx done." That works. But intent.md never specifies behavior on a **transient non-404** error (503, connection refused, JSON parse fail). The implementation **returns immediately** on any such error (`client.go:135-137`), which contradicts the spirit of F4 — sync was supposed to become resilient to LCD flakiness, and instead the wait loop is now a single point of failure that propagates a transient blip as a fatal Execute error. See F-PERF-201.
- The doc comment claims `default per-attempt timeout is short (300ms)` (`client.go:125-126`). No such 300ms timeout exists anywhere; per-attempt is bounded only by the shared `c.http.Timeout = 30 * time.Second` (`client.go:47`). Misleading; see F-PERF-202.

### T4 — Sync pre-flight chain-state filter (`internal/chain/sync.go`)

Implementation is straightforward:

- `checkIDs` is collected from `findings`, `resolutions`, and `queued` (`sync.go:93-108`).
- Each id is queried serially via `s.Client.Finding(ctx, planID, id)` (`sync.go:109-125`).
- Query errors are tolerated and downgraded to "treat as not committed" (`sync.go:111-116`), per intent.md.
- `committedOnChain` / `resolvedOnChain` are correctly applied during commit-build (`sync.go:138-140, 160-162`) and resolve-build (`sync.go:172-174`).

What is **not** met:

- Performance bounds in intent.md say "for typical batch sizes (<10) this is negligible. For large batches this would dominate sync cost — flag if you think the threshold matters." I think the threshold matters. See F-PERF-203.
- intent.md Concrete Scenario #3 ("idempotent no-op") says `chain sync` on a fully-settled plan should be a no-op. The new code makes it a no-op _in terms of Execute calls_ but **N synchronous LCD reads on every invocation**. On a stale ledger with N=100 historical findings, every `chain sync` is now 100 sequential LCD round-trips with no batching and no cache. See F-PERF-203.

---

## New findings

### F-PERF-201: `WaitForTx` aborts sync on the first transient LCD error — Warning

**File:** `internal/chain/client.go:129-151`, `internal/chain/sync.go:188`

**Reasoning:** F4's stated purpose (commit message, devnet report) is to make sequential `Execute` calls safe — specifically so that the sync's commit→resolve pipeline stops dying on `account sequence mismatch`. The wait loop achieves that on the happy path. But:

```go
ok, code, log, err := c.fetchTx(ctx, txhash)
if err != nil {
    return err   // <-- single LCD blip kills the whole sync
}
```

`fetchTx` returns a non-nil `err` for:

- network error (`c.http.Do` failure — `client.go:165-167`),
- non-OK / non-404 HTTP status (`client.go:177-178`) — e.g. 502 from an LB, 503 from a reload-restarting LCD,
- JSON parse failure on a partial body (`client.go:187-188`).

All three are exactly the transient class of failure an idempotent retry path is supposed to absorb. Worse, the new sync pipeline is **commit Execute → WaitForTx → resolve Execute → WaitForTx**. If the resolve-side WaitForTx aborts on a transient 503 _after the commit landed_, the operator's next `chain sync` will at least see the pre-flight filter correctly skip the committed half — but only if **that** sync's LCD calls all succeed. So a single LCD blip can put settlement into a state that requires multiple manual retries to escape.

**Severity:** Warning. Not critical because eventually the operator can retry past the blip; critical-grade would require an unrecoverable failure path.

**Measurement evidence:** Scenario reproducible by injecting `httptest.NewServer` with a handler that returns `502 Bad Gateway` once then `200 OK`; the loop dies on the first attempt rather than re-trying. The diff includes no test that exercises a transient-error path through `WaitForTx`.

**Suggested defense:** Treat network errors / 5xx / JSON-parse failures the same way 404 is treated — `found=false, err=nil` — and let the ctx be the only thing that terminates the loop. Add a separate "consecutive failures" counter for observability (see F-PERF-204) so a permanently-broken LCD doesn't silently hide for the full 5-minute budget. If a hard-fail is desired, do it after N consecutive errors with backoff.

---

### F-PERF-202: `WaitForTx` doc comment lies about per-attempt timeout — Suggestion

**File:** `internal/chain/client.go:123-128`

**Reasoning:** The doc comment claims:

> The default per-attempt timeout is short (300ms) and the poll cadence is 1s

There is no 300ms timeout anywhere in the code path. `fetchTx` uses the shared `c.http` (`client.go:165`), whose `Timeout` is set to **30 seconds** at construction (`client.go:47`). The poll cadence of 1s is real (`client.go:130`).

This matters for the degraded-mode contract. With a stuck LCD socket and the 5-minute ctx that `chain sync` uses (`cmd/tribunal/chain.go:212`), the loop can spend a single 30-second HTTP attempt with no progress, then return the network error per F-PERF-201. A reader of the doc would expect ~300ms to fail-fast and the next tick to retry — that's not what happens.

**Severity:** Suggestion. Pure doc drift; no operational consequence beyond hiding F-PERF-201's impact.

**Suggested defense:** Either implement what the doc says (per-attempt context with a 300ms-2s deadline derived from the parent ctx), or correct the doc to describe the actual 30s shared client.

---

### F-PERF-203: Sync pre-flight is strictly serial; N LCD round-trips on every `sync`, even no-ops — Warning

**File:** `internal/chain/sync.go:109-125`

**Reasoning:** The pre-flight loop does N sequential blocking `s.Client.Finding(ctx, planID, id)` calls. There is:

- no concurrency (trivially parallelizable with an `errgroup.WithContext` + bounded worker pool),
- no batched contract query (the contract exposes per-id `Finding` only; out of scope to fix, but should be flagged to arch),
- no fast-path that skips the loop when **both** the ledger half (`findings`+`resolutions`+`queued` for this plan) is empty,
- no fast-path that skips the loop when this plan has never been synced before (no local cache).

Cost calculus on a typical xion devnet (~150ms LCD round-trip):

| N findings                                   | Pre-flight latency added per `chain sync` |
| -------------------------------------------- | ----------------------------------------- |
| 5                                            | ~750ms                                    |
| 10                                           | ~1.5s                                     |
| 100 (the MAX_BATCH_SIZE ceiling from v0.3.1) | ~15s                                      |

Now combine with T3: a full sync of a 100-finding plan now costs **15s pre-flight + 1-6s WaitForTx (commit) + 1-6s WaitForTx (resolve)** = ~17-27s minimum. That fits in the 5-minute ctx, but it crowds it, and a slow-block chain (10s) or a flaky LCD pushes it over. More importantly, the **idempotent no-op case** (`chain sync` on an already-settled plan, intent.md scenario #3) now takes 15s for N=100 instead of being instantaneous. Operators will run `chain sync` from cron / CI for "did anything land yet"-style polling; that workload is now an N-multiplied LCD pressure source.

The plan **explicitly invited** this finding ("flag if you think the threshold matters"). I'm flagging it.

**Severity:** Warning. Not critical because correctness holds; sync still settles. Warning because the latency is super-linear in ledger growth and the operator-facing surface (`chain sync`) is a primary command.

**Measurement evidence:** No benchmark in the diff. Reproduction: seed a ledger with 100 findings on a single plan, run `chain sync` twice, observe wallclock of the second run (a no-op against an already-settled plan) is dominated by the pre-flight loop. The diff adds zero tests for this path.

**Suggested defense (any one):**

1. Parallelize with `errgroup.SetLimit(8)` and a worker pool. Drops 15s → ~2s.
2. Add a local `.tribunal/sync-state.json` cache of last-seen committed/resolved ids. Pre-flight only queries ids not in the cache. Drops the no-op case to ~0.
3. Add a `BatchFindings(plan_id, [ids...])` query on the contract side. Out of scope for v0.3.2 but worth filing for v0.3.3.
4. Short-circuit: if `findings`+`resolutions`+`queued` for this plan are all empty after the seen-dedupe, skip the pre-flight entirely.

---

### F-PERF-204: No observability for a stalled `Execute` / `WaitForTx` / pre-flight loop — Warning

**File:** `internal/chain/client.go:129-151`, `internal/chain/sync.go:109-125`, `cmd/tribunal/chain.go:212` (caller ctx is 5min)

**Reasoning:** Every new path added in v0.3.2 is silent. There is no logging anywhere in `WaitForTx`, `fetchTx`, or the pre-flight loop. No counter, no progress print, no stderr breadcrumb. The only thing the operator sees is:

```
$ tribunal chain sync
... [hangs for up to 5 minutes] ...
Error: commit batch (plan=P-x, n=12): wait for inclusion: tx 0xabc... not included before context done: context deadline exceeded
```

That is the worst possible failure mode for a settlement command: long latency, no signal of progress, no way to tell whether the chain is slow or the LCD is wrong or the tx will eventually land. Compare against the existing `chain register` (`cmd/tribunal/chain.go:171`) which at least prints `✓ registered ... (txhash: ...)` once the tx lands — but only **after** the new silent wait.

Intent.md's stated failure mode is:

> `/cosmos/tx/v1beta1/txs/{hash}` always 404 (e.g. wrong NodeREST) → WaitForTx polls until ctx done, then returns wrapped ctx error. Caller sees timeout, not silent hang.

The caller sees the **wrapped error**, yes — 5 minutes later. The wait itself is a silent hang. This is the production-vs-working delta that the perf-review lens exists to catch.

**Severity:** Warning. Concrete operator-facing degradation; cheap fix.

**Suggested defense:**

- In `WaitForTx`: on the first miss and every 10s thereafter, write a one-liner to stderr (`tribunal: waiting for tx 0xabc... (Ns elapsed)`).
- In the sync pre-flight: a single line at the start (`tribunal: pre-flight N finding(s) for plan P-x`) and a one-shot error log for each downgraded query failure (currently the `// continue` silently swallows them).
- Future hook: structured logging via `slog` would let operators set log level, but that's a v0.4 ask.

---

### F-PERF-205: `cmd/tribunal-seed/main.go` uses `context.Background()` against the new wait loop — Suggestion

**File:** `cmd/tribunal-seed/main.go:105`

**Reasoning:** The new seed harness calls `cli.Execute(context.Background(), exec)`. With the v0.3.2 change, `Execute` now blocks inside `WaitForTx` and the only termination condition is ctx done. A misconfigured `node_rest` or an LCD that never indexes the tx will hang the harness forever — there is no fallback timeout.

The doc comment on `WaitForTx` explicitly anticipates this (`client.go:127-128`):

> For headless callers without an explicit ctx, wrap the call in context.WithTimeout(parent, 30\*time.Second) or similar.

The seed harness is exactly such a headless caller and does not follow the advice.

**Severity:** Suggestion. Test-support binary, not a release artifact. Still worth fixing to avoid wedging future e2e CI runs.

**Suggested defense:** Wrap the Execute call in `context.WithTimeout(context.Background(), 2*time.Minute)` (or whatever matches the documented expectation on slow devnets).

---

### F-PERF-206: First-poll latency floor is ~1s even for fast-indexed txs — Suggestion

**File:** `internal/chain/client.go:129-150`

**Reasoning:** The wait loop calls `fetchTx` once immediately, but on the typical case (tx is in mempool, not yet indexed) it falls through to `select { case <-ticker.C: }` and blocks 1s before the next attempt. There is no fast-poll-then-back-off behavior. On an active 1-2s-block chain like xion devnet, the tx is often indexed within 200-500ms of broadcast; the operator pays a hard 1s floor per `Execute` regardless.

For a full `chain sync` of N=100 with two batches: +2s minimum from this floor alone, on top of F-PERF-203's pre-flight cost.

**Severity:** Suggestion. The intent.md performance bounds explicitly accept "1–6s of latency per Execute on a 5s-block chain (typical)" so 1s isn't a contract violation; it just leaves easy latency on the table.

**Suggested defense:** Start the ticker at e.g. 200ms and back off to 1s after the first miss, or use a simple exponential backoff (200ms, 400ms, 800ms, 1s, 1s, ...). Trivial change, drops happy-path Execute latency by ~800ms.

---

## Cross-Reviewer Ready Notes

### For reviewer-arch

- **The `Finding` query contract is the bottleneck behind F-PERF-203.** Per-id only, no batched alternative. Worth flagging to PM as a v0.3.3 candidate: add `findings_by_plan { plan_id, after, limit }` paginated query so the pre-flight can do O(1) round-trips instead of O(N). This is an arch-level call, not perf-shaped.
- **`Sync.SyncPlan` is now doing three different things** (queue drain, pre-flight query, build+submit). The pre-flight is plausibly its own function — clearer separation, easier to test, easier to swap for a cached/batched implementation later.

### For reviewer-sec

- The "silently downgrade pre-flight query errors to not-on-chain" path (`sync.go:111-116`) is intentionally permissive for resilience, but a hostile LCD could deliberately return error or partial responses to **force** Tribunal to re-submit already-landed txs. The contract's duplicate guard catches this (failure surfaces as `tx broadcast failed (code=...)`, no money lost) — but it's worth confirming with reviewer-sec that the LCD's error path can't be weaponized to grief sync into burning gas on rejected duplicates.
- F-PERF-201's "abort on transient error" interacts with the threat model: a malicious LCD can deliberately blip 502 to force the operator to keep retrying. Combined with the no-observability point (F-PERF-204), the operator may not realize they're being denied service.

---

## Verdict

**Request Changes.**

The mechanism is correct under nominal devnet conditions and the goroutine lifecycle is clean. But three of the new failure modes (F-PERF-201 LCD blip kills sync, F-PERF-203 N-serial-queries-on-no-op, F-PERF-204 silent multi-minute hang) all live in the **degraded-mode** quadrant, which is the lens this reviewer is here to catch.

These don't need contract changes or v0.4-scope work. The fixes are: tolerate transient errors in the wait loop, parallelize or short-circuit the pre-flight, and add minimum stderr breadcrumbs. Pre-merge for v0.3.2 if the release is meant to be the "real-chain-tested" release. Acceptable as v0.3.3 follow-ups if the v0.3.2 release notes call out the known degraded-mode behavior explicitly.

---

## FINDINGS-TO-FILE

```
warning|performance|F-PERF-201|sha256:waitfortx-transient-abort|file:///home/dan/src/tribunal/internal/chain/client.go#L129-L151|WaitForTx aborts the wait loop on the first transient LCD error, defeating F4's resilience goal
suggestion|performance|F-PERF-202|sha256:waitfortx-doc-lie|file:///home/dan/src/tribunal/internal/chain/client.go#L123-L128|WaitForTx doc comment claims a 300ms per-attempt timeout that does not exist; actual per-attempt cap is the shared 30s http.Client.Timeout
warning|performance|F-PERF-203|sha256:sync-preflight-serial|file:///home/dan/src/tribunal/internal/chain/sync.go#L109-L125|Sync pre-flight does N serial LCD round-trips with no parallelism, no batching, and no fast-path for empty/no-op plans
warning|performance|F-PERF-204|sha256:wait-noobservability|file:///home/dan/src/tribunal/internal/chain/client.go#L129-L151|Wait loop and sync pre-flight are completely silent; operator sees a multi-minute hang with no progress signal
suggestion|performance|F-PERF-205|sha256:seed-no-timeout|file:///home/dan/src/tribunal/cmd/tribunal-seed/main.go#L105|tribunal-seed --send uses context.Background() against the new wait loop and can hang forever on a misconfigured LCD
suggestion|performance|F-PERF-206|sha256:waitfortx-1s-floor|file:///home/dan/src/tribunal/internal/chain/client.go#L129-L150|WaitForTx has a hard 1s floor before the first retry; happy-path Execute pays ~1s of avoidable latency on a fast-block chain
```
