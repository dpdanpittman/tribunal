# Architecture Review — Tribunal v0.3.2

**Reviewer:** `tribunal-reviewer-arch`
**Plan:** `P-v032-audit`
**Scope:** `HEAD~1..HEAD` (`f186e92`, "v0.3.2: devnet-driven tooling fixes (F1-F6)")
**Verdict:** **Request Changes**

## Summary

v0.3.2 is a pure tooling-fix release that lands its six advertised changes (F1–F6). Every diff hunk traces cleanly to a plan task (T1–T9); no out-of-scope refactor snuck in. `go build`, `go vet`, and `go test ./...` are all clean against `f186e92`.

That said, the architectural cost of the rushed devnet fixes shows up at four boundaries that the e2e run could not have surfaced on its own:

1. **`Config.OutcomeRewardMultiplier` is silently overridden on load.** T6 ("auto-populate from contract") is undermined by the pre-existing `applyDefaults()` rule "0 → 2" inside `LoadConfig`. A contract instantiated with `multiplier=0`, or a `chain init` that failed the query and wrote `0`, both end up loaded as `2`. The advisory field lies in exactly the case T6 was meant to fix.
2. **`WaitForTx` aborts on any non-404 HTTP error**, contradicting its docstring claim that it polls "until ctx done." A transient LCD blip will fail an Execute whose tx already landed; the surrounding `BroadcastResult` is dropped by every caller (sync.go, chain.go, tribunal-seed). Sync recovers on next run thanks to T4's pre-flight; one-shot commands (`chain register`, `chain rotate`) do not.
3. **Pre-flight loop in `SyncPlan` silently swallows context cancellation.** Any `Finding()` query error — including `ctx.Err()` — is reduced to `continue`, leaving `committedOnChain[id]` false. A user who Ctrl-C's mid-sync gets a sync that will then attempt to re-submit already-committed findings on the next run with a fresh ctx, only to be saved by the contract's duplicate guard at the cost of a failed broadcast.
4. **`normalizeRPCScheme` lives in `cmd/tribunal` not `internal/chain`.** Boundary violation: input normalization for `Config.NodeRPC` belongs next to `Config`, and should be applied in `LoadConfig`, not only in `chain init`. A user who edits `chain.yaml` directly will silently re-introduce the `tcp://` bug F3 was meant to fix.

None of these are correctness-breaking against the documented happy path. None would have surfaced in the devnet e2e (which only exercised the success path and the immediate-retry path). All four are exactly the class of "fix is too narrow" that lens-parallel review is supposed to catch.

## Verification of plan tasks

### T3 — `Execute` polls for tx inclusion via REST — IMPLEMENTED, INCOMPLETE

`internal/chain/client.go:85-121` adds the `WaitForTx` call into `Execute` after the broadcast result is parsed. `WaitForTx` (`client.go:129-151`) and `fetchTx` (`client.go:156-197`) handle the 404/200-with-empty-height/success cases correctly. The doc-comment promise that ctx bounds the round-trip is honored on the success path.

**Drift from documented contract:** the docstring on `WaitForTx` (`client.go:123-128`) says "the function gives up only when ctx is done." The implementation gives up on the first non-404 HTTP error returned by `fetchTx`. A 502 from a load balancer, a TLS handshake reset, an LCD restart between blocks — any of these immediately propagate to the caller. See F-ARCH-201.

### T4 — Sync pre-flight filter against on-chain state — IMPLEMENTED, FRAGILE

`internal/chain/sync.go:86-125` builds the `checkIDs` set from `findings`, `resolutions`, and `queued`, then queries the contract per ID. The filter wiring at lines 138-140 (commits), 160-162 (queue commits), and 172-174 (resolutions) is correct.

**Issue:** the `for id := range checkIDs` loop swallows every `Finding()` error with `continue`, including `ctx.Err()`. The plan's stated tolerance ("pre-flight query that errors out is tolerated") was meant to absorb individual REST flakes, not silent ctx cancellation. See F-ARCH-202.

**Issue:** the loop is N serial round-trips for N unique IDs. Plan acknowledges "for large batches this would dominate sync cost — flag if you think the threshold matters." The threshold matters. See F-ARCH-203.

### T5 — `chain init` normalizes `tcp://` → `http://` — IMPLEMENTED, MISPLACED

`cmd/tribunal/chain.go:19-27` and `cmd/tribunal/chain.go:61-66` do the rewrite + stderr notice. Behavior on `http://` and `https://` inputs is correct (no rewrite, no warning).

**Architectural issue:** the function lives in `package main` (`cmd/tribunal`) instead of `internal/chain` next to `Config`. The deploy script's tail (`scripts/deploy-contract.sh:169-170`) still prints `node_rpc: "tcp://..."` for paste-into-yaml use; if a user follows the script's documented "Paste this into ~/.tribunal/chain.yaml" path, `LoadConfig` will happily accept the `tcp://` URL and every subsequent chain command will fail with the exact error F3 was meant to eliminate. See F-ARCH-204.

### T6 — `chain init` queries contract for `outcome_reward_multiplier` — IMPLEMENTED, OVERWRITTEN ON LOAD

`cmd/tribunal/chain.go:68-91` does the basic save → query → re-save dance the intent.md invariants describe. The 10s timeout, non-fatal warning path, and `fmt.Sscanf` parse are all handled.

**Correctness issue:** `internal/chain/config.go:84-86` (pre-existing) treats `OutcomeRewardMultiplier == 0` as "unset" and rewrites it to `2`. Combined with T6:

- Contract instantiated with `multiplier=0` → query returns `"0"` → cfg field set to 0 → saved as `0` → loaded as `2`. **Wrong.**
- Chain unreachable at init time → file saved with `0` (per docstring) → loaded as `2`. **Wrong, and inconsistent with the stderr warning that told the user it's 0.**
- Contract instantiated with `multiplier=2` → loaded as `2`. Right by accident.
- Contract with `multiplier>=1` and `!= 0` → loaded correctly.

The intent.md invariant — "outcome_reward_multiplier in chain.yaml is documented as advisory / preview-only" — is consistent with the field lying, but the user's mental model of `chain init` is "make the local config match the contract," and the load-time default actively defeats that. See F-ARCH-205.

### T7 — `cmd/tribunal-seed` harness — IMPLEMENTED, BRITTLE

`cmd/tribunal-seed/main.go:1-110` exists and seeds + optionally sends. Plan marks this as test-support only.

**Issue:** argv parsing at lines 23-25 unconditionally treats `os.Args[1]` as the plan ID. Invoke as `tribunal-seed --send` and you seed plan id literal `"--send"`. The actual `--send` detection at lines 82-86 then ALSO sees `--send` in argv, so the seed succeeds _and_ the broadcast fires with planID `"--send"` — which the contract may or may not reject depending on validate_id_field. See F-ARCH-206.

**Issue:** the on-chain `Execute` call at line 105 uses `context.Background()` — no timeout. The new `WaitForTx` docstring (`client.go:127-128`) explicitly tells headless callers to wrap in `context.WithTimeout(parent, 30*time.Second)`. The seed harness is exactly the headless caller the docstring is warning about. See F-ARCH-207.

### T8 — CHANGELOG entry — IMPLEMENTED

`CHANGELOG.md:10-26` covers all six fixes with mechanism, evidence, and cross-refs to the F1–F6 numbering in the test-run report. Accurate.

### T9 — Test-run report — IMPLEMENTED

`.tribunal/reports/devnet-e2e-2026-05-13.md:1-105` covers the e2e math, the six findings, and v0.3.1 audit-fix verification. Accurate.

## New findings

### F-ARCH-201: `WaitForTx` aborts on transient HTTP errors (Critical)

**File:** `internal/chain/client.go:129-151`

**Claim:** `WaitForTx` is documented as polling "until the given hash is found or ctx is cancelled," but the implementation returns immediately on any error from `fetchTx` other than 404. Specifically, `fetchTx` returns errors for: network failures (`c.http.Do` failed), non-200/non-404 HTTP status, response body read failures, JSON parse failures. Any of these abort the wait loop.

**Why it matters:** the tx was already broadcast to mempool (broadcast-mode sync confirmed acceptance before `WaitForTx` ran). The tx will most likely land. The caller, however, sees `Execute` return an error and treats it as a failed submission:

- `internal/chain/sync.go:188-191` discards `br` on error → next sync run re-builds the commit, pre-flight saves us via T4, but we've burned an extra REST round trip and produced a misleading error log.
- `cmd/tribunal/chain.go:167-171` (`chain register`) → user sees "execute failed" but the agent IS registered. Re-running `chain register` will fail because the agent already exists (no pre-flight here).
- `cmd/tribunal/chain.go:439-444` (`chain rotate`) → same. Rotation may have landed but the operator thinks it didn't.
- `cmd/tribunal-seed/main.go:105-108` → log.Fatal kills the process; the resolution is on-chain but the harness reports failure.

**Plan anchor:** intent.md "Failure modes" → "Pre-flight query during sync errors out → entry treated as not-on-chain, sync proceeds." The same tolerance principle is not applied to `WaitForTx` — but should be, because the same root cause (flaky REST) drives both.

**Severity:** Critical. Misleading error semantics on the primary tx-submission path. The "happy path holds, sad path tells lies" pattern is exactly what e2e tests don't catch.

**Suggested defense:** treat non-404 errors as transient inside the for-loop. Distinguish only ctx.Done() as a real terminal condition. Optionally log per-error to stderr after N retries so the operator has signal that something is wrong. Hand off to reviewer-sec to confirm no security concern with infinite retry against a hostile LCD.

---

### F-ARCH-202: `SyncPlan` pre-flight loop swallows `ctx.Err()` (Warning)

**File:** `internal/chain/sync.go:109-117`

**Claim:** the pre-flight loop reads:

```go
for id := range checkIDs {
    resp, err := s.Client.Finding(ctx, planID, id)
    if err != nil {
        continue
    }
    ...
}
```

If `ctx` is cancelled mid-loop (operator Ctrl-C, parent timeout), every subsequent `Finding()` call returns immediately with `ctx.Err()` and the loop quietly burns through `checkIDs` doing nothing. After the loop, `committedOnChain` is empty (or partially populated). The build-commits step then constructs commit messages for findings that ARE on-chain. The Execute then either fails (good) or, depending on contract version, broadcasts a tx that fails on-chain (bad: gas burned).

**Plan anchor:** intent.md "Failure modes" — the tolerance was meant for individual REST errors, not for cancellation. plan.md acceptance criteria invariant: "for any sequence of `chain sync` calls against a fully-settled plan, every subsequent call is a no-op." This invariant breaks the moment ctx cancels.

**Severity:** Warning. Operator-driven cancellation should not flip the system into a state where re-running produces erroneous tx attempts. The contract's duplicate guard is the safety net, but the architectural intent ("sync is idempotent") is broken in the cancelled-mid-flight case.

**Suggested defense:** check `ctx.Err()` after each `Finding()` call. If non-nil, return the error and abandon the sync (the caller will retry on a fresh ctx with a clean pre-flight). Or use `errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)` to bail early.

---

### F-ARCH-203: Pre-flight is N serial REST round-trips (Warning)

**File:** `internal/chain/sync.go:109-125`

**Claim:** for a plan with `N` unique finding IDs across `findings`, `resolutions`, and the queue, the pre-flight does `N` sequential calls to `s.Client.Finding(ctx, planID, id)`. Each is one HTTP round-trip to the LCD. At a 100ms RTT, 100 findings = 10 seconds of latency before the batch tx is even built. The plan acknowledges this: "for large batches this would dominate sync cost — flag if you think the threshold matters."

**Why it matters:** the contract has a documented `MAX_BATCH_SIZE = 100`. A reasonable user with 100 findings per plan (the contract's published limit) waits an extra ~10s per sync. With many plans synced via `SyncAll`, latency compounds. The whole point of plan-close batched sync is to do ONE round-trip per plan; the pre-flight added `N+2`.

**Plan anchor:** plan.md "Reviewer assignment" hands this to me explicitly: "if you think the threshold matters". It does.

**Severity:** Warning, not Critical, because `MAX_BATCH_SIZE = 100` caps the blast radius. But it's worth fixing before any real prod load.

**Suggested defense:** add a batched `findings_by_plan` query to the contract (already easy via `state::FINDINGS.prefix(plan_id)`), or add a `findings({plan_id, finding_ids})` LCD-friendly query that returns the subset in one round-trip. Until then, parallelize the pre-flight with a small worker pool (bounded by `min(N, 8)` concurrent requests). Hand to reviewer-perf for cost confirmation.

---

### F-ARCH-204: `normalizeRPCScheme` lives in `cmd/`, not `internal/chain/` (Warning)

**File:** `cmd/tribunal/chain.go:19-27`

**Claim:** the function is a Config-normalization concern but lives in `package main`. Three architectural consequences:

1. It's not exported, so no other `cmd/` binary (e.g., `tribunal-seed`) can apply the same normalization if it ever needs to.
2. `internal/chain/config.go::LoadConfig` does not apply the normalization, so a `chain.yaml` with `node_rpc: "tcp://..."` on disk (e.g., from a user who pasted the deploy-contract.sh tail, lines 169-170, directly into yaml) loads cleanly and every chain command fails downstream with `unsupported protocol scheme "tcp"`. This is the exact bug F3 was meant to eliminate.
3. There is no unit test. Plan T5 is asserted only by the e2e; a future refactor that loses the `strings.HasPrefix` check would not break any test.

**Plan anchor:** plan.md reviewer-arch focus: "abstraction cost (is `normalizeRPCScheme` worth being its own function)". The answer is yes IF it's tested and applied at the right boundary. Currently it is neither.

**Severity:** Warning. The fix is layering, not correctness — the normalization works where it's called. But the "deploy-contract.sh tail still prints `tcp://`" path means an operator who follows the documented "Paste this into ~/.tribunal/chain.yaml" instruction (line 167) regresses to the F3 bug.

**Suggested defense:** move `normalizeRPCScheme` to `internal/chain/config.go`, export it, call it from both `LoadConfig` (post-unmarshal, pre-validate) and from `chain init`. Add unit tests covering `tcp://`, `http://`, `https://`, and edge cases (`tcp://` followed by `:` ambiguity, IPv6 literals). Update `scripts/deploy-contract.sh:170` to emit `http://` in the paste-ready yaml block.

---

### F-ARCH-205: `applyDefaults` overrides `outcome_reward_multiplier=0` to `2`, defeating T6 (Warning)

**File:** `internal/chain/config.go:84-86`

**Claim:** `applyDefaults()` treats `OutcomeRewardMultiplier == 0` as "unset" and rewrites it to `2`. This default predates v0.3.2. T6 was added in v0.3.2 to make `chain init` write the contract's actual value to the file. The two interact:

- If the contract is instantiated with `multiplier = 0`, T6 saves `0` to the file, then `LoadConfig` rewrites it to `2`. Subsequent `chain query config`-style preview will report `2`. **The user is silently lied to** about a value `chain init` just went and fetched.
- If the chain is unreachable at init time, the warning to stderr tells the user "chain.yaml has outcome_reward_multiplier=0" (literal text from `chain.go:81`). Then `LoadConfig` rewrites to `2`. **The error message lies about the on-disk state**, or more precisely, the on-disk state and the in-memory state after load differ on the value the user was just told.

**Plan anchor:** intent.md invariant: "every change is additive to behavior". T6 is additive, but the interaction with the pre-existing default is corrosive. plan.md acceptance criteria: "Each behavior in intent.md is exercised by the e2e test-run report" — but the e2e only ran against a contract with `multiplier=2`, so this case never showed up.

**Severity:** Warning. The field is documented as "advisory / preview-only" so no on-chain math depends on it. Still: it's a lying advisory in exactly the case T6 was supposed to make truthful.

**Suggested defense:** introduce a sentinel (e.g. `*uint64` with nil meaning "unset"), or split `applyDefaults`'s reward-multiplier branch off so that an explicitly-saved `0` survives load. Document that `0` is a valid contract value. Add a unit test asserting that a config saved with `0` loads with `0`.

---

### F-ARCH-206: `tribunal-seed` argv parsing treats `--send` as plan ID (Warning)

**File:** `cmd/tribunal-seed/main.go:23-25, 82-86`

**Claim:** the harness reads `os.Args[1]` unconditionally as the plan ID:

```go
if len(os.Args) > 1 {
    planID = os.Args[1]
}
```

Then it does a separate pass to detect `--send`:

```go
for _, a := range os.Args[1:] {
    if a == "--send" {
        send = true
    }
}
```

Invocation: `tribunal-seed --send`. Result: `planID = "--send"`. The finding is signed and appended with plan ID `"--send"`. Then `--send` is detected → broadcast fires with `PlanID: "--send"`. The contract validates plan_id (see `internal/chain/validate.go` — invalid chars including pipe rejected); `"--send"` may pass validation depending on the exact regex.

**Plan anchor:** T7 is "test-support only" — fair. But a throwaway harness that **succeeds in seeding garbage plan IDs into the local ledger** poisons the ledger for any future e2e that uses the same `.tribunal/` directory. The ledger is jsonl-append-only; cleanup is manual.

**Severity:** Warning. Plan T7 doesn't promise robust argv parsing, but the failure mode is "harness silently mis-seeds the ledger" which is the wrong default for test-support code.

**Suggested defense:** parse the optional plan id positionally only if it does not start with `--`. Or use `flag` package. Or just delete the plan-id-from-argv path; the default `"P-e2e-001"` is fine.

---

### F-ARCH-207: `tribunal-seed` uses `context.Background()` for on-chain Execute (Suggestion)

**File:** `cmd/tribunal-seed/main.go:105`

**Claim:** `cli.Execute(context.Background(), exec)` is called with no timeout. The new `WaitForTx` docstring (`internal/chain/client.go:127-128`) explicitly warns: "For headless callers without an explicit ctx, wrap the call in context.WithTimeout(parent, 30\*time.Second)." `tribunal-seed --send` is the canonical headless caller.

**Why it matters:** combined with F-ARCH-201, a single transient REST hiccup terminates immediately. Without F-ARCH-201, an unreachable LCD makes `tribunal-seed --send` hang forever. Both outcomes are bad.

**Severity:** Suggestion. Test-support code, not user-facing.

**Suggested defense:** `ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second); defer cancel()` and pass that.

---

### F-ARCH-208: `chain init` calls `chain.New(cfg)` without validating cfg first (Suggestion)

**File:** `cmd/tribunal/chain.go:76-91`

**Claim:** the new T6 code runs:

```go
client := chain.New(cfg)
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
if resp, err := client.ContractConfig(ctx); err != nil {
    ...
```

`chain.New(cfg)` is called with the in-memory cfg, which has NOT been through `validate()` (Save doesn't call validate; LoadConfig does). If, e.g., `--node-rest` is empty, `ContractConfig` will produce a confusing URL error rather than a clear "node_rest is required" check.

**Severity:** Suggestion. Cobra defaults cover the common path; this is a foot-gun for users who override flags with empty strings or for future code that reuses this pattern.

**Suggested defense:** invoke `cfg.applyDefaults()` and `cfg.validate()` (would need to export validate, or extract a `Validate()`) before the first `Save` and the `chain.New` call. Fail fast with a clear message instead of attempting a doomed REST query.

---

### F-ARCH-209: `Save` does not call `validate`, so any caller can persist a broken config (Suggestion)

**File:** `internal/chain/config.go:113-129`

**Claim:** pre-existing behavior, now exposed in v0.3.2: `Save(path)` writes whatever's in `c` to disk, no validation. The new `chain init` code calls `Save` twice; the second call could write a partially-populated cfg if a parse error occurs between the two. The first Save's content is already on disk by then.

**Severity:** Suggestion. Pre-existing, not introduced by v0.3.2, but the diff's "Save twice" pattern aggravates the symmetry. Worth flagging because the v0.3.1 audit (F-ARCH-005) noted the same kind of contract-vs-Go invariant gap.

**Suggested defense:** call `validate()` at the top of `Save()`. If a caller has a legitimate reason to persist an in-progress config (`chain init`'s basic-save before the query), make that explicit (`SaveUnsafe()` or skip the unwritten field).

---

### F-ARCH-210: `WaitForTx` per-attempt timeout claimed in docstring but not enforced (Suggestion)

**File:** `internal/chain/client.go:124-128`

**Claim:** the docstring says "The default per-attempt timeout is short (300ms)." There is no 300ms timeout anywhere in the implementation. `fetchTx` uses `c.http`, which has `Timeout: 30 * time.Second` from `New()`. A slow LCD can therefore tie up each attempt for up to 30 seconds, blocking the 1s ticker entirely.

**Severity:** Suggestion. The docstring is wrong; the behavior may be acceptable but isn't what's documented. Worth pinning down.

**Suggested defense:** either implement the 300ms per-attempt timeout via `context.WithTimeout(ctx, 300*time.Millisecond)` around the `fetchTx` call, or correct the docstring to match reality. Pick one; the asymmetry hides a real design choice.

---

### F-ARCH-211: `deploy-contract.sh` still emits `tcp://` in the paste-ready yaml block (Suggestion)

**File:** `scripts/deploy-contract.sh:166-179`

**Claim:** the closing block prints `node_rpc: "$NODE"` where `$NODE` is `tcp://localhost:26657` per the documented usage. The script's documented downstream flow includes "Paste this into ~/.tribunal/chain.yaml" — which produces a broken config that downstream chain commands reject. The script does derive `node_rest` by rewriting `tcp://` → `http://` and `:26657` → `:1317` (line 171), but does not apply the same rewrite to `node_rpc`. This is inconsistent on its face and directly contradicts the F3/T5 fix's intent: the user can EITHER paste correctly via `tribunal chain init` OR get a config that has been auto-corrected.

**Severity:** Suggestion. The escape hatch (`chain init` does the rewrite) exists, but the script's own output advertises a broken config.

**Suggested defense:** rewrite `node_rpc` to `http://` in the printed yaml the same way `node_rest` is rewritten. Tie the change to F-ARCH-204 (move `normalizeRPCScheme` to the shared layer and apply consistently).

---

## Cross-Reviewer Ready Notes

- **For reviewer-sec:**
  - **F-ARCH-201 implication:** if `WaitForTx` is changed to retry on all non-404 errors (recommended fix), the polling loop is bounded only by ctx. A malicious LCD that responds with 500s indefinitely could keep the operator's process pinned. Currently the implementation aborts on first 500 — security-positive but reliability-negative. The tradeoff needs your call.
  - **F-ARCH-204:** `normalizeRPCScheme` strips `tcp://` and prepends `http://` without checking that the remainder is a valid host:port. Worth confirming there's no injection vector through `cfg.NodeRPC` later (it's used in `xiond --node` and in `c.http.Do` against `/status`). Probably fine because `cfg.NodeRPC` is operator-controlled, but worth a look.
  - **F-ARCH-208:** missing `validate()` before `chain.New(cfg)` in `chain init` means a malformed cfg can reach `chain.New`'s keyring-test warning logic — low risk but worth confirming nothing else in `New` panics on empty fields.
  - **No new keyring or signing surface introduced** by v0.3.2; F-ARCH-003 from v0.3.1 (xiond not on PATH) remains deferred and is your call whether to re-raise.

- **For reviewer-perf:**
  - **F-ARCH-203** is squarely your lane: confirm 8-way (or any-way) parallelism is the right knob; flag if you'd prefer a contract-side `findings_by_plan` query.
  - **F-ARCH-210:** the 30s default HTTP timeout vs. the 1s ticker — confirm whether a poll attempt actually blocks the ticker (`select` semantics on a stopped channel) or whether overlapping attempts could pile up. Reading the code, the loop is strictly sequential: `fetchTx` → check → `select{ctx,ticker}` → repeat. So no goroutine leak, but the effective max poll cadence is `1s + max(fetchTx_duration)`.
  - **`WaitForTx` adds 1-6s of latency per Execute** (intent.md). Compounded by F-ARCH-203's serial pre-flight, a single `chain sync` against 100 findings takes `(N × 100ms pre-flight) + (1-6s commit wait) + (1-6s resolve wait)`. Confirm acceptable for batch settlement.

## Verdict

**Request Changes.**

Three Warnings (F-ARCH-202, F-ARCH-203, F-ARCH-204, F-ARCH-205, F-ARCH-206) and one Critical (F-ARCH-201) are unresolved. The Critical is on the primary Execute path; the Warnings cluster around T4/T5/T6's boundary placement. The Suggestions (F-ARCH-207..F-ARCH-211) can defer.

Specifically blocking on F-ARCH-201: the failure mode (silent loss of broadcast result on transient LCD blip in `chain register` / `chain rotate` paths that have no pre-flight) is exactly the kind of subtle wrongness that ships and bites under network load. The fix is small (treat non-404 as continue, bail only on ctx.Done()); the win is real.

## FINDINGS-TO-FILE

```
critical|architecture|F-ARCH-201|<claim_hash_pending>|file:///home/dan/src/tribunal/internal/chain/client.go#L129-L151|WaitForTx aborts on transient HTTP errors instead of polling until ctx done
warning|architecture|F-ARCH-202|<claim_hash_pending>|file:///home/dan/src/tribunal/internal/chain/sync.go#L109-L117|SyncPlan pre-flight swallows ctx.Err() via continue, breaking idempotency under cancellation
warning|architecture|F-ARCH-203|<claim_hash_pending>|file:///home/dan/src/tribunal/internal/chain/sync.go#L109-L125|Pre-flight is N serial REST round-trips; dominates latency at MAX_BATCH_SIZE
warning|architecture|F-ARCH-204|<claim_hash_pending>|file:///home/dan/src/tribunal/cmd/tribunal/chain.go#L19-L27|normalizeRPCScheme lives in cmd/ not internal/chain/, so LoadConfig does not normalize on read
warning|architecture|F-ARCH-205|<claim_hash_pending>|file:///home/dan/src/tribunal/internal/chain/config.go#L84-L86|applyDefaults overrides outcome_reward_multiplier=0 to 2, defeating T6 when contract has multiplier 0
warning|architecture|F-ARCH-206|<claim_hash_pending>|file:///home/dan/src/tribunal/cmd/tribunal-seed/main.go#L23-L25|tribunal-seed argv parsing treats --send as plan ID, poisoning the ledger
suggestion|architecture|F-ARCH-207|<claim_hash_pending>|file:///home/dan/src/tribunal/cmd/tribunal-seed/main.go#L105|tribunal-seed uses context.Background() for on-chain Execute against new wait-for-tx contract
suggestion|architecture|F-ARCH-208|<claim_hash_pending>|file:///home/dan/src/tribunal/cmd/tribunal/chain.go#L76-L91|chain init calls chain.New(cfg) without validating cfg first
suggestion|architecture|F-ARCH-209|<claim_hash_pending>|file:///home/dan/src/tribunal/internal/chain/config.go#L113-L129|Save does not call validate, so callers can persist a broken config
suggestion|architecture|F-ARCH-210|<claim_hash_pending>|file:///home/dan/src/tribunal/internal/chain/client.go#L124-L128|WaitForTx docstring claims 300ms per-attempt timeout that is not enforced
suggestion|architecture|F-ARCH-211|<claim_hash_pending>|file:///home/dan/src/tribunal/scripts/deploy-contract.sh#L166-L179|deploy-contract.sh still emits tcp:// in the paste-ready yaml block, regressing F3 when operator follows documented copy-paste flow
```
