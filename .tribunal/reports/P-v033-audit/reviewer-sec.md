# Security Review — Tribunal v0.3.3 audit-driven fix release

**Reviewer:** `tribunal-reviewer-sec`
**Plan:** `P-v033-audit`
**Diff basis:** `5cc1634^..5cc1634` (commit `5cc1634`, "v0.3.3: audit-driven fix release (P-v032-audit findings)")
**Verdict:** **Request Changes**

## Summary

v0.3.3 closes most of v0.3.2's security warnings cleanly: per-attempt timeouts and transient/terminal error classification land where the docstring promised they would (F-SEC-201, F-SEC-202), pre-flight is parallelized with a bounded fan-out and a 3s per-query timeout (F-SEC-204), and `tribunal-seed` finally has a production guard (F-SEC-207). The CHANGELOG and intent line up with the code.

But the v0.3.2 question the prior sec review raised — _is the LCD authoritative for state-of-the-world decisions?_ — got _more_ surface area in v0.3.3, not less. The new `submitCommitBatch` / `submitResolveBatch` recovery loop parses the error stream emitted by `WaitForTx` to decide which entry to drop from a batch. The error stream is sourced partly from the LCD (`tx_response.raw_log` on a `code != 0` response). A hostile LCD that lies about inclusion now gets to **choose which finding gets dropped from the operator's retry batch**, which the prior `tx %s failed on-chain` failure mode did not allow. The contract is still authoritative for on-chain state (no forged reputation), but the operator's retry composition is now LCD-controlled. **F-SEC-301** below is the headline.

In addition, the regex parser that drives recovery does a `strings.TrimRight(m[2], "\"',;.)")` on the captured finding_id (sync.go:394). The contract permits these characters in `finding_id` (validate.rs only rejects pipe + control chars). So a legitimate finding ending with `.` or `)` will have its dupID corrupted, the filter loop won't match it, and recovery bails with "duplicate not in batch" — a self-DoS for plausible IDs (F-SEC-302).

Two carry-overs from the prior audit are unfixed and remain in scope:

- **F-SEC-205** (tcp→http normalization cements plaintext default) — `NormalizeRPCScheme` was moved into `internal/chain/config.go` and is now called from _every_ `LoadConfig`, but it still never warns on non-local hosts and never offers `https://` as the upgrade target. v0.3.3 expanded the silent-rewrite surface (now also fires on `LoadConfig`, not just `chain init`).
- **F-SEC-208** (txhash not validated before path-joining) — still in `fetchTx` at client.go:206. Trivial to fix; bears no relationship to the v0.3.3 diff. Carrying forward.

The `--allow-prod` chain-id heuristic in `tribunal-seed` is documented in intent.md as "a safety rail, not a security boundary" — that framing is correct, but the substring set (`devnet|testnet|test|local`) is loose enough that a mainnet chain id with `attestation` in it would slip through. Flagged at Suggestion (F-SEC-303).

**Verdict: Request Changes.** Two new Warnings (F-SEC-301, F-SEC-302), two carried-over Suggestions/Warnings (F-SEC-205 carried fwd, F-SEC-208 carried fwd), three Suggestions (F-SEC-303, F-SEC-304, F-SEC-305).

## Verification of plan tasks

### T1 — `WaitForTx` per-attempt timeout + transient classification — **IMPLEMENTED**

`internal/chain/client.go:154-194` (WaitForTx) and `client.go:202-257` (fetchTx) match the intent.md spec:

- `fetchTxAttemptTimeout = 3 * time.Second` wraps each poll (client.go:203-204). ✓
- 404 → `terminal=false, err=nil` (line 225-226). ✓ Continue polling.
- 5xx → `terminal=false, err=non-nil` (line 228-230). ✓ Transient.
- 4xx other than 404 → `terminal=true, err=non-nil` (line 232-234). ✓ Abort.
- Network-layer errors (DNS, refused, reset, ctx-deadline on the attempt ctx) → `terminal=false, err=non-nil` (line 214-217). ✓ Transient.
- Body read failure → transient (line 220-223). ✓
- JSON parse failure → transient (line 243-248). ✓
- `tx_response.code != 0` → terminal via the WaitForTx layer (line 172-174). ✓
- `ctx.Done()` → wraps `ctx.Err()` (line 187-189). ✓
- `transientStreak` is incremented across transients and included in the timeout error, which is genuinely useful operator output.

The docstring (client.go:144-153) now describes actual behavior. F-SEC-202 (doc/behavior mismatch) resolved.

**Concern:** the `else` branch at client.go:176-178 zeros `transientStreak` on a `found=false, err=nil` response — meaning "tx is not yet indexed" is treated as success-shaped, resetting the streak. That's correct semantically (the LCD is responding, just hasn't indexed yet). No issue, just noting.

### T2 — `Execute` propagates `BroadcastResult` on wait error — **IMPLEMENTED**

`client.go:138-140`: error path now returns `&res, err` instead of `nil, err`. Docstring at line 101-105 loudly states the new contract. Callers in `sync.go` correctly null-check `br != nil` before reading `br.TxHash` (sync.go:198-200, 209-211). No callers were missed by grep.

### T3 — Batch atomicity recovery — **IMPLEMENTED, with new findings**

`submitCommitBatch` / `submitResolveBatch` (sync.go:316-381) post the batch, on error parse for `FindingAlreadyCommitted` / `FindingAlreadyResolved`, drop the offending entry from the local batch slice (in-place `commits[:0]` reuse), and retry. Bounded by `originalLen` iterations — termination guaranteed since each non-bailout iteration removes one entry.

Termination edge case: if `len(filtered) == len(commits)` (contract reports a duplicate **not** in our batch), the loop bails with a wrapping error (sync.go:338-340, 373-374). This is the right call — it prevents an infinite loop and surfaces the inconsistency to the operator.

Signature integrity check: each `FindingCommit` carries its own per-entry `signature` (messages.go:51). The contract verifies each entry's signature against `canonical_finding_message(plan_id, finding_id, severity, claim_hash, stake)` (commit.rs:73-91) — independent of batch composition. **Removing one entry from the batch does NOT invalidate the others' signatures.** Confirmed by inspection of `commit_finding_batch` (commit.rs:23-47): it iterates entries calling `process_finding` per-entry; no batch-level signature.

The recovery layer's correctness in the happy case is fine. The integrity holes are:

- **F-SEC-301**: error string is sourced partly from LCD's `raw_log` (terminal-on-code path). A hostile LCD that lies about inclusion can pick which entry the recovery layer drops.
- **F-SEC-302**: regex's `TrimRight` strips characters the contract allows in finding_id.

Detailed below.

### T4 — Parallel pre-flight with bounded fan-out — **IMPLEMENTED, with one concurrency observation**

`sync.go:222-298`. The pattern is sound:

- `idCh` buffered to `len(ids)`, populated up-front, closed before workers start (line 235-239). No producer/consumer race.
- `resCh` buffered to `len(ids)`, so workers never block on send (line 241).
- Worker count capped at `preflightConcurrency=8`, reduced to `len(ids)` if smaller (line 243-246).
- Each worker drains `idCh` via `for id := range idCh`, calls `Finding` with `attemptCtx` (3s per query), writes one result to `resCh`. Per-attempt ctx is `cancel()`'d eagerly (line 257-259). ✓
- Workers respect `ctx.Err()` between iterations (line 254-256). F-ARCH-202 fix lives here.
- Progress goroutine emits stderr every `waitProgressInterval=5s`. Closed via `done` channel (line 270-283).
- Parent `wg.Wait()`, then `close(resCh)`, then `close(done)`. Drain results sequentially (line 285-296). No data race on the maps (only one writer).

**Concurrency observation (Suggestion, F-SEC-304):** if ctx is cancelled mid-flight, workers that have already started a `Finding` call may complete their attempt and write a result; workers between iterations return early without writing. The parent then collects fewer results than IDs, sees missing IDs as "not committed", and `SyncPlan` rechecks `ctx.Err()` at line 125 and returns. This is **correct**, but it means the partial preflight results from a cancelled ctx are silently discarded. Fine in practice (the next sync re-checks), but no test covers it.

**DoS bound:** worst-case latency now `O(ceil(N/8) × 3s)` — for N=100 findings that's ~38s, vs. the v0.3.2 worst case of N × 30s = 50min. F-SEC-204 resolved.

**Cross-poison check (from the prompt's question):** workers don't share state with each other. A hostile response for ID `X` poisons only `X`'s result struct, which the worker writes to its own channel slot. No cross-contamination. ✓

But: see F-SEC-301's second arm — a hostile LCD that claims `committed=true` for an ID that isn't actually on-chain causes that entry to be silently dropped from the commit batch (sync.go:140-142). The recovery layer does **not** catch this case (no duplicate error is returned because the contract sees a non-duplicate). The integrity hole carries forward; parallelism made it faster but no worse and no better.

### T5 — ctx check in pre-flight — **IMPLEMENTED**

`sync.go:254-256` checks `ctx.Err()` between iterations. Additionally, `SyncPlan` rechecks at line 125-127 after preflight returns. Both gates are in place.

### T6 — Dedup resolutions — **IMPLEMENTED**

`sync.go:169` introduces `seenResolve`, mirroring `seenCommit`. F-NEW-304 closed.

### T7 — `SyncAll` partial-failure aggregation — **IMPLEMENTED**

`sync.go:431-443` collects per-plan errors into a slice and returns `errors.Join(errs...)`. Out-of-band: this changes the SyncAll error contract — callers now need `errors.Is` to detect specific failures. Not a security issue, flagged for reviewer-arch.

### T8 — Progress notes — **IMPLEMENTED**

`waitProgressInterval = 5s` in both WaitForTx (client.go:180-184) and preflight (sync.go:271-283). Stderr-only, no PII / secret leakage (txhash and plan_id are public anyway).

### T9 — `NormalizeRPCScheme` runs on every `LoadConfig` — **IMPLEMENTED, expands silent-rewrite surface**

`internal/chain/config.go:21-26` (exported helper) and `config.go:81-86` (called from `LoadConfig` on read, silently). Comment at line 82-84 explicitly states "silent rewrite — log handled at the `chain init` boundary."

**Concern:** the silent rewrite means an operator running v0.3.3 against a chain.yaml they wrote _yesterday_ with `tcp://` gets `http://` substituted with **no notification**. The prior `chain init` warning was visible to the user typing the command; LoadConfig is called from every chain subcommand, so the rewrite path is wider. This is the documented behavior, but it carries forward F-SEC-205 (silent acceptance of plaintext HTTP) to a path that v0.3.2 didn't touch.

Combined with F-SEC-301: the LCD trust hole now uses cleartext HTTP by default for any operator whose chain.yaml predates v0.3.2.

### T10 — Remove `outcome_reward_multiplier` auto-default — **IMPLEMENTED**

`config.go:101-108`. Default override removed; multiplier of 0 is now respected as a legitimate contract config. Comment correctly notes that v0.3.2's auto-default silently defeated F6's contract-query behavior.

**Carryover:** F-SEC-206 (Sscanf truncates trailing garbage) is **unfixed** at `cmd/tribunal/chain.go:74`. v0.3.3 changed behavior around the multiplier but kept the Sscanf parser. A misbehaving LCD that returns `"100abc"` for the multiplier still sets it to `100` silently. Suggestion-level carryover.

### T11 — `tribunal-seed` hardening — **IMPLEMENTED**

`cmd/tribunal-seed/main.go:25-36` switches to `flag.Parse()` with named flags. `--send` is now a proper bool. `--allow-prod` defaults to false; `looksLikeTestChain` rejects mainnet-looking chain ids. `--timeout` defaults to 60s. F-SEC-207 resolved at the core level.

**Concern:** the `looksLikeTestChain` heuristic uses substring containment (`strings.Contains`, line 124-130). A mainnet chain_id containing any of `devnet|testnet|test|local` as a substring slips through. Realistic accidents (`xion-1`, `xion-mainnet`) are caught. Adversarial inputs (`xion-mainnet-attestation`, `xion-prod-testbed`) bypass. Severity: Suggestion. See F-SEC-303.

### T12 — Regex unit test — **IMPLEMENTED, with gaps**

`sync_test.go:15-77`. Five cases pin the regex against representative inputs. **Missing test cases:**

- Hostile error strings that _aren't_ from the contract — e.g., `error executing wasm: from tribunal-attacker.contract: finding P-X/F-victim already committed`. This regex matches, recovery proceeds.
- Finding IDs ending in trim-stripped chars (`F-99.`, `F-x)`, `F-y;`) — TrimRight corrupts these (see F-SEC-302).
- Multi-match input (two duplicate errors in one log) — regex takes the first match; behavior is fine but should be pinned.
- Error from a non-LCD source (e.g., xiond stderr) that contains the contract error verbatim — confirms recovery works regardless of which trust source surfaced the error.

T12 verifies the regex extracts an ID from a clean error string. It does not verify the **trust source** of that string. Recommend extending the test before any new contract version drifts the format.

### T13 — CHANGELOG — present, accurate

`CHANGELOG.md` v0.3.3 entry matches the diff. No security-sensitive omissions.

## New findings

### F-SEC-301: Recovery error-string parser is fed an LCD-tainted stream; a hostile LCD can choose which entry the operator drops from a retry batch (Warning)

`internal/chain/sync.go:316-345` + `internal/chain/client.go:171-174, 236-256`

The recovery loop in `submitCommitBatch` parses `err.Error()` with `alreadyCommittedRE`. The error chain leading there is:

1. `xiond tx wasm execute` (RPC) broadcasts the batch — typically succeeds (broadcast-mode=sync only confirms mempool).
2. `WaitForTx` polls the **LCD** (`NodeREST`) for the txhash.
3. LCD returns `tx_response.code = N (non-zero), tx_response.raw_log = "<arbitrary string>"`.
4. `fetchTx` returns `terminal=false, err=nil, ok=true, code=N, rawLog=<arbitrary>` (client.go:256).
5. `WaitForTx` (line 171-174) returns `tx <hash> failed on-chain (code=N): <rawLog>`.
6. `Execute` wraps that error and propagates to `submitCommitBatch`.
7. Recovery matches `<rawLog>` against `finding ([^/]+)/([^ ]+) already committed` and drops the captured finding_id from the local batch.

The `<rawLog>` content comes from the LCD's response body. A hostile LCD that the operator points at has full control over this string. The LCD can:

- **Fake a duplicate-rejection that didn't happen.** Operator broadcasts a clean batch of 100 findings. LCD returns `code=18, raw_log="finding P-x/F-target already committed"`. Recovery drops F-target locally, retries the batch. Meanwhile, the **first batch may have actually landed cleanly on-chain** (or may have failed for an unrelated reason — the LCD's response is the only signal we have). On retry, the contract sees 99 already-committed findings and returns 99 duplicate errors in sequence; recovery drops one per iteration, racking up N+1 wasted txs and N+1 gas charges. Worst case: N=100 batch → 101 broadcast-and-fail iterations, each costing gas.
- **Censor a specific finding.** LCD names `F-criticalfix` as the duplicate. Recovery drops it. Operator's `result.FindingsSent` for the retry is N-1; the local sync reports success. From the operator's POV, F-criticalfix was "already committed" — so the operator doesn't investigate. But it was never on-chain (the LCD lied). Permanent skip until the operator's ledger is re-synced against a _different_ LCD.
- **Force the bail-out (DoS).** LCD names a finding ID not in the batch. Recovery bails with `contract reported duplicate commit X not in batch`. The entire batch fails. Operator must resync. Costs one broadcast tx.

Crucially: the contract is still authoritative for what's _actually_ on-chain — the attacker cannot forge reputation. But the attacker controls:

(a) **Which findings the operator's retry omits.** The retry batch is composed by `commits[:0]` filter against the LCD-supplied dupID. Each retry includes a fresh `Execute` call (real ed25519 signature over batch contents).
(b) **Operator's local view of "what landed."** `result.FindingsSent` and `result.CommitTxHash` reflect the last-attempt result. If recovery whittled the batch to empty (every entry "rejected" by the LCD), `submitCommitBatch` returns `(nil, 0, nil)` — `len(commits)==0` branch at line 319-322. The operator sees `findings=0` and exits sync clean. The operator's ledger now permanently believes those findings are settled.
(c) **Operator gas burn.** Each retry is a real, signed, broadcast transaction. With v0.3.3's gas-prices default of `0.025uxion` and a 100-finding batch at ~14M gas per attempt, an attacker forcing 100 retries on a victim's mainnet sync wastes ~1.4B gas worth of fees from the operator's account.

The intent.md correctly says "LCD endpoint is untrusted infrastructure" and "Reviewers should confirm no path trusts LCD as authoritative." This path **does trust the LCD** — specifically, it trusts the LCD's raw_log content to identify which finding to drop. v0.3.2's `WaitForTx` already trusted the LCD for the success-or-failure decision; v0.3.3 extended that trust to control over retry composition. That's an expansion of the LCD's role in the TCB.

**Suggested defense:**

(a) **Cross-source the duplicate check.** Before dropping an entry on the LCD's word, re-query the contract directly (via the LCD's smart-query path or, better, via Tendermint RPC's `/abci_query`) for `Finding{plan_id, finding_id}`. If the contract confirms the finding is on-chain with the same canonical bytes the operator signed, drop. If not, surface to operator as "LCD claims duplicate but contract disagrees" and abort. This makes the contract the authority again, with the LCD relegated to "fast hint."

(b) **Cap retries hard.** v0.3.3 bounds retries by `originalLen` (sync.go:317-318). For a 100-finding batch that's 101 retries. Lower the cap to something operator-tunable like 5, and surface a loud failure on exceeding it — recovery should be rare, not a hot path.

(c) **Track retries-per-batch in the SyncResult.** Currently `submitCommitBatch` swallows the retry count. Surface it in `SyncResult` so an operator looking at `sync` output can see "this batch took 12 retries" — a signal of an unhealthy LCD even when the final result looks clean.

(d) Document the LCD as part of the TCB. The intent.md says it; the code comments do not.

**Severity rationale: Warning, not Critical.** No on-chain integrity violation. No reputation forgery. The contract still validates per-finding signatures. The harm is operator-level: censorship of legitimate settlement, wasted gas, misleading sync results. Critical would require the attacker to forge on-chain state, which they cannot.

### F-SEC-302: Recovery regex's `TrimRight` strips characters the contract permits in finding_id (Warning)

`internal/chain/sync.go:386-396`

```go
func matchDuplicate(errMsg string, re *regexp.Regexp) (string, bool) {
    m := re.FindStringSubmatch(errMsg)
    if len(m) != 3 {
        return "", false
    }
    fid := strings.TrimRight(m[2], "\"',;.)")
    return fid, fid != ""
}
```

The contract's `validate_id_field` (contracts/tribunal-reputation/src/validate.rs:28-56) only rejects bytes that are `|` or `is_control()`. Every printable ASCII character including `.`, `"`, `'`, `,`, `;`, `)` is a legal `finding_id` byte. CosmWasm canonical_finding_message safely round-trips all of them — the only forbidden char is pipe.

Failure case: operator commits `F-NEW-301.` (trailing period; not contrived — version-bumping conventions like `F-NEW-301.1` are plausible). The contract emits `finding P-x/F-NEW-301. already committed`. Regex captures `[^ ]+` as `F-NEW-301.`. TrimRight strips the dot → `F-NEW-301`. The filter loop at sync.go:332-337 looks for `c.FindingID == "F-NEW-301"`, doesn't find it (the actual ID is `F-NEW-301.`), `len(filtered) == len(commits)`, bail-out fires (line 338-340): `"contract reported duplicate commit \"F-NEW-301\" not in batch"`. Recovery fails for that batch.

Compounding: the bail-out error message _quotes_ the corrupted ID, so the operator sees `F-NEW-301` and looks for it in the ledger — but the actual ID in the ledger is `F-NEW-301.`. Confusing debugging.

Verified via inline test (see /tmp/rxtest.go run during this review):

- Input: `finding P-1/. already committed` (legitimate id `.`) → matches but TrimRight nukes the whole ID → `fid != ""` is false → returns false. Recovery fails to even identify the duplicate.

The TrimRight was added "in case xiond error text wraps the ID in quotes/punctuation." But (a) the actual contract error format from `error.rs:25` is `"finding {plan_id}/{finding_id} already committed"` with no surrounding punctuation — TrimRight has nothing to do; (b) the regex `[^ ]+` will greedily capture quotes/punctuation if xiond _does_ wrap them. The fix as written corrupts legitimate IDs to mitigate a hypothetical wrapping problem that doesn't appear in the test cases or the contract source.

**Suggested defense:** delete the `TrimRight` call. If a real wrapping case shows up in CI, write a test that pins the exact xiond output and tighten the regex to exclude the wrap chars _only_ via the regex's character class, not via a destructive post-process. Alternatively: emit the error wrapper from the contract with explicit delimiters (e.g. `finding {plan_id}/<<{finding_id}>> already committed`) so the regex can use anchors that don't depend on per-byte stripping.

**Severity rationale: Warning, not Suggestion.** This is silent recovery failure for plausible finding IDs. The bug manifests as "your sync fails inexplicably and the error message points at a different ID than the one that's actually broken." Combined with F-SEC-301's hostile-injection path, an attacker could craft a forged dupID _specifically chosen to trigger this corruption_ to deny recovery entirely.

### F-SEC-303: `--allow-prod` heuristic uses substring containment; mainnet IDs containing benign substrings bypass the guard (Suggestion)

`cmd/tribunal-seed/main.go:122-130` + mirror at `internal/chain/client.go:56-62`

```go
func looksLikeTestChain(chainID string) bool {
    id := strings.ToLower(chainID)
    return strings.Contains(id, "devnet") ||
        strings.Contains(id, "testnet") ||
        strings.Contains(id, "test") ||
        strings.Contains(id, "local")
}
```

Cases that **bypass** the guard (substring matches):

- `xion-attestation-1` → contains `test`. ✓ marked as test chain.
- `xion-mainnet-protested` → contains `test`. ✓ marked as test chain.
- `xion-local-validators-mainnet` → contains `local`. ✓ marked as test chain.
- `xion-mainnet-1-contested-fork` → contains `test`.

Cases that correctly catch:

- `xion-testnet-2` → ✓.
- `xion-devnet-1` → ✓.
- `xion-mainnet-1` → ✗ correctly rejected.

The intent.md explicitly notes this is "a safety rail, not a security boundary." So the severity is bounded to Suggestion. But the substring `test` is far too generic — `attest`, `latest`, `contest`, `protest`, `manifesto`, `testify`, `testimony` all match. Real chain ids in the Cosmos ecosystem with `test` as a substring would be misclassified.

**Suggested defense:** use word-boundary regex or explicit suffix/prefix check. Something like `regexp.MustCompile(`(^|-)(dev|test|local)(net)?(-|$)`)`. Catches `xion-testnet-2`, `xion-devnet-1`, `xion-local-validators` (suffix); rejects `xion-attestation-1`, `xion-mainnet-contested-fork`. Same heuristic should be applied to the client.go:56-62 keyring warning so the two helpers stay in lock-step.

### F-SEC-304: Preflight workers can leak partial results on ctx cancel, with no test covering the path (Suggestion)

`internal/chain/sync.go:222-298`

If `ctx` is cancelled while workers are running, workers between-iterations return early (line 254-256), workers mid-call complete their attempt (3s max via `attemptCtx`) and write a result. The parent's result-drain loop (line 289-296) only sees the results that landed. Missing IDs are treated as "not committed" in the maps returned (zero-value `false`).

`SyncPlan` then rechecks `ctx.Err()` at line 125-127 and returns. So the **observed behavior is correct**: a cancelled sync doesn't accidentally treat findings as on-chain when they aren't.

But: there is no test asserting this. If a future refactor changes the post-cancellation invariant (e.g., adds a fallback that uses partial pre-flight results), the bug would land silently. The race between "worker returns early without writing" and "worker writes and then ctx cancels" is timing-dependent and not exercised by any existing test.

**Suggested defense:** add a unit test that creates a `chain.Client` stub returning slow `Finding` responses, cancels the parent ctx mid-flight, and asserts (a) `SyncPlan` returns `ctx.Canceled`-wrapped error, (b) no on-chain mutation occurred. Doesn't need a real LCD; a fake `KeyResolver` + a fake `Client` interface suffices.

### F-SEC-305: `Execute` returns `&res, err` on wait-error, but the contract error `tx broadcast failed (code=N): raw_log` at client.go:132-134 also flows through this path with an LCD-tainted `raw_log` (Suggestion)

`internal/chain/client.go:128-134`

```go
var res BroadcastResult
if err := json.Unmarshal(out, &res); err != nil {
    return nil, fmt.Errorf("parse xiond output: %w (output=%q)", err, string(out))
}
if res.Code != 0 {
    return &res, fmt.Errorf("tx broadcast failed (code=%d): %s", res.Code, res.RawLog)
}
```

This is the **broadcast-time** error path (mempool rejection, not wait-for-inclusion). The `res.RawLog` here comes from xiond's parsed output of the RPC broadcast response — sourced from the _Tendermint RPC node_, not the LCD. Distinct trust source from F-SEC-301.

But the recovery layer in `submitCommitBatch` doesn't distinguish trust sources — it just matches the regex against `err.Error()`. So if the RPC node is also hostile (or compromised separately), it can inject the same `finding P-x/F-victim already committed` string at the broadcast stage. Same attack as F-SEC-301, different trust source. Same defense applies (cross-source the duplicate via contract query before dropping).

Bundled with F-SEC-301 — fixing that one fixes this one. Filing separately for completeness because the trust source is distinct (RPC vs. LCD), and an operator might trust one but not the other.

## Carried-over findings from P-v032-audit

### F-SEC-205 (carried forward) — `tcp://` → `http://` rewrite still defaults plaintext, now silent on every LoadConfig (Warning, was Suggestion)

`internal/chain/config.go:81-86` + the existing `NormalizeRPCScheme` (line 21-26).

v0.3.3 expanded the rewrite path from `chain init` only to **every config load**. The original audit flagged this as a Suggestion because the surface was narrow. With the surface now wider, an operator running v0.3.3 against any chain.yaml written by v0.3.1 or earlier gets silent http-fication of their RPC endpoint on _every_ invocation, with no visible warning.

Combined with F-SEC-301 (LCD trust), an in-path attacker can:

1. MITM the plaintext HTTP RPC and LCD responses.
2. Forge `tx_response.code != 0, raw_log = "finding P-x/F-victim already committed"` for any broadcast.
3. Trigger F-SEC-301's censorship attack on any operator who runs v0.3.3 against an upgraded-but-not-reconfigured chain.yaml.

Escalating from Suggestion to Warning because the v0.3.3 changes materially widened the exposed surface.

**Suggested defense:** in `LoadConfig`'s normalize call, emit a one-line stderr WARNING on any rewrite, identical to the `chain init` warning. The intent.md says "log handled at the chain init boundary" — but operators upgrading from v0.3.1 never see that boundary again. Bonus: refuse to use `http://` (post-normalization) against a chain id that doesn't `looksLikeTestChain`, requiring an explicit `--allow-plaintext` flag for prod.

### F-SEC-208 (carried forward) — `url.JoinPath(NodeREST, ..., txhash)` still doesn't validate txhash shape (Suggestion)

`internal/chain/client.go:206`. Unchanged from v0.3.2. Recommendation stands: validate `txhash` against `^[0-9A-Fa-f]{64}$` before path-joining. The risk surface didn't grow in v0.3.3 (txhash still sourced from local xiond's output), but the fix is one line and removes a latent traversal door if `XiondBinary` is ever a docker-exec'd remote container.

### F-SEC-206 (carried forward) — Sscanf truncates trailing garbage on outcome_reward_multiplier parse (Suggestion)

`cmd/tribunal/chain.go:74`. Unchanged from v0.3.2. v0.3.3 fixed the _default-override_ hole (F-ARCH-205); the _parse-truncation_ hole is independent and still open. Switch to `strconv.ParseUint(s, 10, 64)`.

## Cross-Reviewer Ready Notes

- **For reviewer-arch:** F-SEC-301 (LCD-tainted recovery composition) is fundamentally an architecture question — the `chain.Client` abstraction should have a "verify-via-contract-not-via-error-string" affordance. The recovery layer is currently a string parser; it could be a query+verify loop. Worth a v0.4 design discussion.
- **For reviewer-arch:** the SyncAll error-aggregation change (T7, errors.Join) changes the SyncAll error contract. Callers that previously checked `err == ErrSpecific` now need `errors.Is`. Not a security issue; flagging for cohesion.
- **For reviewer-perf:** F-SEC-301's "100-finding batch can cost 101 broadcasts" is also a perf concern — bounded by `originalLen` is correct for safety but is operator-funded DoS on a hostile LCD. Tightening the cap (e.g. to `min(originalLen, 10)`) bounds both lenses.
- **For reviewer-perf:** F-SEC-302's TrimRight bug is also a perf concern in that it triggers extra retry rounds for plausibly-named findings; deleting TrimRight is a one-line perf+security combo fix.
- **For PM (`pm-alpha`):** F-SEC-301, F-SEC-302, F-SEC-305 form a single coherent "harden the recovery layer against LCD/RPC injection" workstream for v0.3.4. Bundle them.
- **For PM:** F-SEC-205 carry-forward escalation is unusual — v0.3.3 didn't make it worse on purpose, but the wider rewrite surface mechanically did. Resolve as part of v0.3.4 if a v0.4 TLS-mandatory mode is too aggressive for the release window.

## Verdict

**Request Changes.** Two new Warnings (F-SEC-301, F-SEC-302) on the recovery layer's trust posture and ID-handling. The carryover F-SEC-205 escalates from Suggestion to Warning given the wider rewrite surface in `LoadConfig`. Three Suggestions (F-SEC-303, F-SEC-304, F-SEC-305) plus two carryovers (F-SEC-206, F-SEC-208).

The v0.3.3 fixes solve the v0.3.2 _availability_ concerns about the LCD (per-attempt timeouts, transient classification, parallel preflight) cleanly. They do not address the v0.3.2 _integrity_ concern (LCD-as-authoritative); v0.3.3 actually expanded that concern by handing the LCD a new vote on retry composition. The methodology earns its name if v0.3.4 closes the integrity loop without introducing a fresh hole — F-SEC-301 is the test.

CI: `go build ./...` clean, `go vet ./...` clean, `go test ./...` all green. No CI gate blocker beyond the findings above.

## FINDINGS-TO-FILE

```
Warning|trust-boundary|F-SEC-301|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v033-audit/reviewer-sec.md#f-sec-301|Recovery error-string parser is fed LCD-tainted stream; hostile LCD chooses which entry the operator drops from retry batch
Warning|input-validation|F-SEC-302|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v033-audit/reviewer-sec.md#f-sec-302|matchDuplicate TrimRight strips characters the contract permits in finding_id, corrupting recovery for plausible IDs
Warning|tls-posture|F-SEC-205-carryfwd|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v033-audit/reviewer-sec.md#f-sec-205-carried-forward|tcp to http normalization now silent on every LoadConfig escalates plaintext-default surface from v0.3.2
Suggestion|input-validation|F-SEC-303|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v033-audit/reviewer-sec.md#f-sec-303|looksLikeTestChain substring containment lets mainnet ids containing test as substring bypass the prod guard
Suggestion|race-condition|F-SEC-304|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v033-audit/reviewer-sec.md#f-sec-304|Preflight ctx-cancel partial-result path is correct but has no test covering invariants
Suggestion|trust-boundary|F-SEC-305|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v033-audit/reviewer-sec.md#f-sec-305|Execute broadcast-time error path also injectable via hostile RPC node feeding regex-matchable raw_log
Suggestion|input-validation|F-SEC-208-carryfwd|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v033-audit/reviewer-sec.md#f-sec-208-carried-forward|url.JoinPath txhash still not validated against hex-64 shape; carried from v0.3.2
Suggestion|input-validation|F-SEC-206-carryfwd|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v033-audit/reviewer-sec.md#f-sec-206-carried-forward|Sscanf parsing of outcome_reward_multiplier still truncates trailing garbage silently; carried from v0.3.2
```
