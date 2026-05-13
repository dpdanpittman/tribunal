# Security Review — Tribunal v0.3.2 tooling fixes

**Reviewer:** `tribunal-reviewer-sec`
**Plan:** `P-v032-audit`
**Diff basis:** `HEAD~1..HEAD` (commit `f186e92`, "v0.3.2: devnet-driven tooling fixes (F1-F6)")
**Verdict:** **Request Changes**

## Summary

v0.3.2 is a focused tooling-fix release: the deploy script defaults to the optimizer, `chain init` rewrites `tcp://` to `http://` and auto-populates `outcome_reward_multiplier`, `Execute` now waits for tx inclusion before returning, and `sync` pre-filters findings already on chain. No contract changes; the v0.3.0/v0.3.1 contract surface remains in force.

The contract is still the authoritative state — no critical on-chain-state bug is possible from the new code (the contract rejects duplicates, validates input, etc.). However, the new client-side code paths take a meaningful step toward **trusting the LCD/REST endpoint** as authoritative for local decisions: `WaitForTx` returns success on the LCD's word, sync's pre-flight skips findings on the LCD's word, and `chain init` adopts an `outcome_reward_multiplier` from the LCD's word. A compromised, MITM'd, or merely buggy LCD can now (a) cause sync to permanently skip legitimate commits, (b) cause `Execute` to claim success before the tx exists, and (c) silently mis-set a client-side config field. The TLS posture taken by F3 (`tcp://` → `http://`, never to `https://`) makes the plaintext-HTTP case the default for the documented onboarding flow. None of this lets an attacker forge on-chain reputation, but it lets an attacker DoS / censor an operator's settlement pipeline.

Additionally, the new `cmd/tribunal-seed` harness is unguarded: a one-line typo can sign and broadcast a fake "true positive" against a production chain.

Two Warnings, four Suggestions. No Critical.

## Verification of plan tasks

### T3 — `Execute` polls for tx inclusion via REST — **IMPLEMENTED, with concerns**

`internal/chain/client.go:85-121` wraps `runXiond → WaitForTx`. `WaitForTx` (line 129-151) loops `fetchTx` (line 156-197) until found or ctx done. Behavior matches intent.md:

- 404 → keep polling (line 174-175). ✓
- 200 with `tx_response.height == ""` → keep polling (line 193-194). ✓
- 200 with non-zero `code` → return error with `raw_log` (line 138-141). ✓
- ctx done → wrap with ctx.Err (line 144-146). ✓

But see F-SEC-201, F-SEC-202, F-SEC-203 below for things this implementation gets wrong on the hostile-input side.

### T4 — Sync pre-flight filter against on-chain state — **IMPLEMENTED**

`internal/chain/sync.go:86-125` queries `Client.Finding(planID, id)` for each unique finding/resolution id in the plan. Findings with non-nil `resp.Finding` get added to `committedOnChain`; those with `resp.Finding.Resolution != nil` also get added to `resolvedOnChain`. The build-commits loop (line 138-140) and build-resolutions loop (line 172-174) skip accordingly. A pre-flight query that errors out is tolerated via `continue` (line 111-116) — entry treated as "not on chain", contract is final authority. Matches intent.md edge-case spec.

Performance / DoS impact: see F-SEC-204.

### T5 — `chain init` normalizes `tcp://` → `http://` — **IMPLEMENTED**

`cmd/tribunal/chain.go:22-27` strips `tcp://` and prepends `http://`. `cmd/tribunal/chain.go:61-66` emits stderr warning + rewrites cfg before save. `https://` is preserved (no rewrite). Matches intent.md.

Trust-posture concern: see F-SEC-205.

### T6 — `chain init` queries contract for `outcome_reward_multiplier` — **IMPLEMENTED**

`cmd/tribunal/chain.go:72-91`. Saves twice (basic save → query → re-save), per intent.md invariant. On query failure, stderr warning, exit 0 — non-fatal. Field is documented as advisory / preview-only in intent.md and `config.go:44-46`.

Sscanf parsing edge case: see F-SEC-206.

## New findings

### F-SEC-201: `WaitForTx` aborts on first transient error instead of polling through it (Warning)

`internal/chain/client.go:133-137`

```go
for {
    ok, code, log, err := c.fetchTx(ctx, txhash)
    if err != nil {
        return err  // <-- any error kills the wait loop
    }
```

Only HTTP 404 and HTTP 200-with-empty-height are treated as "not yet indexed". A transient 502/503 from a load balancer, a TCP-reset blip, or a `json.Unmarshal` failure on a momentarily malformed response (e.g., an LCD that returns an HTML 500 page during indexing lag) — all cause immediate `return err` to the caller. From `sync.go:188-194` and `sync.go:198-204`, the caller is the batch settlement code, and any error here aborts the whole plan sync with `commit batch (...): wait for inclusion: ...`.

In practice this means: an operator running `tribunal chain sync` against a fronted LCD (any production-grade infra with HAProxy / nginx / Caddy in front) will hit a brittle failure during routine 503s and have to re-run sync. The contract's idempotency guard plus the new pre-flight filter (F5) covers correctness on re-run — but availability-wise this is a foot-gun, and the docstring at line 123-128 promises "the function gives up only when ctx is done", which is false: it gives up on the first non-404 non-200 response.

**Suggested defense:** classify errors. Treat HTTP 5xx, JSON parse error, and `net` errors (connection reset, DNS failure) as transient — log to stderr, continue polling. Only abort on persistent failure modes (e.g., 400, 401, 403). Update the docstring to reflect actual behavior.

### F-SEC-202: Docstring claims a per-attempt timeout that doesn't exist (Warning)

`internal/chain/client.go:123-128`

```go
// WaitForTx polls the REST tx endpoint until the given hash is found or
// ctx is cancelled. Returns an error if the tx is found but failed
// (on-chain code != 0). The default per-attempt timeout is short
// (300ms) and the poll cadence is 1s; ...
```

There is no 300ms per-attempt timeout anywhere in the code. `fetchTx` uses `c.http` which is constructed at `client.go:46-48` with `Timeout: 30 * time.Second` — that is the entire-request timeout, no per-attempt override. A slow-rolling / slowloris LCD can hold each poll for ~30 seconds. With the typical caller ctx of 5 minutes (`cmd/tribunal/chain.go:212`), an attacker who holds connections gets ~10 polling attempts instead of the ~300 the docstring implies.

Severity escalated because (a) the docstring will mislead the next reviewer / contributor making changes here, and (b) the attack vector — a malicious or compromised LCD operator — is plausible given F-SEC-205 (plaintext HTTP by default).

**Suggested defense:** either implement the 300ms per-attempt timeout (wrap each `fetchTx` with `context.WithTimeout(ctx, 300*time.Millisecond)`), or rewrite the docstring to describe the actual 30s ceiling. The former is preferable: it makes the wait loop more responsive on a 5s-block chain and bounds slowloris.

### F-SEC-203: `WaitForTx` returns success on the LCD's word; a hostile LCD can fake inclusion or fake non-inclusion (Warning)

`internal/chain/client.go:138-143, 156-196`

`WaitForTx` returns `nil` as soon as `fetchTx` reports `ok=true, code=0`. That decision is built from an HTTP 200 response with `tx_response.height != ""` and `tx_response.code == 0`. The LCD is **fully trusted**:

- A malicious LCD can return `{"tx_response":{"code":0,"height":"1","raw_log":""}}` immediately for _any_ txhash, including one that was never broadcast. `Execute` returns success → `sync.go` records `result.CommitTxHash = res.TxHash` and proceeds to resolve. Resolve then fails (contract sees no commit). Worse: the next sync's pre-flight (`sync.go:109-125`) calls the same LCD; if the LCD also fakes `Finding(...)` to return a record, the commit is permanently skipped from the local side.
- Symmetric attack: a malicious LCD can return 404 forever, forcing every legit commit to time out at the ctx deadline. Pure denial-of-service.

The contract is still the authoritative ledger — on-chain state cannot be forged this way. But an operator's local view of "what I've committed" desyncs from "what the chain says". For a system whose entire point is auditable reputation, that's bad.

The same trust hole existed pre-v0.3.2 for the read-only query path (`Reputation`, `Finding`, etc.). What's new in v0.3.2 is that the **write path now depends on a trusted LCD for state-of-the-world decisions**, not just display.

**Suggested defense:** (a) document explicitly that the LCD is part of the TCB and that operators must run a node they control or use a TLS-verified mirror; (b) consider cross-checking inclusion via the Tendermint RPC's `/tx?hash=...` endpoint as a second source for high-stakes operations; (c) at minimum, require `https://` for `NodeREST` when chain id doesn't look like a test chain (mirror of the existing keyring-backend warning at `client.go:38-43`).

### F-SEC-204: Pre-flight sync queries are serial and unbounded; a slow LCD eats the whole sync window (Warning)

`internal/chain/sync.go:109-125`

The pre-flight loop fires one `Client.Finding(ctx, planID, id)` per unique finding in the plan, serially, no per-query timeout. Each call goes through `Client.Query → http.Client.Do` with the same 30s ceiling. With N findings:

- Worst-case latency: N × 30s.
- A 5-minute caller ctx (`cmd/tribunal/chain.go:212`) is exhausted by 10 findings × 30s = 300s = 5 min. Sync ctx then dies before any commit is even attempted.

Per intent.md "Performance bounds": "Pre-flight chain queries in sync add one query per unique finding per sync call. For typical batch sizes (<10 per plan) this is negligible." That's only true with a healthy LCD. The plan flags: "flag if you think the threshold matters." On a hostile or simply slow LCD, it matters at N≥10.

The fault tolerance is correct (per-query error → `continue`), but tolerance to _latency_ is missing. A malicious LCD doesn't need to return errors — it just needs to slow-respond.

**Suggested defense:** (a) parallelize the pre-flight loop with a bounded worker pool (5–10 concurrent), (b) add a per-query `context.WithTimeout(ctx, 5*time.Second)`, (c) optionally consider a single batched query endpoint on the contract side for v0.4 — but that's a contract change, out of scope.

### F-SEC-205: `tcp://` → `http://` normalization fixes the bug but cements plaintext as the default (Suggestion)

`cmd/tribunal/chain.go:22-27, 61-66`; `scripts/deploy-contract.sh:9, 55, 171`; `docs/on-chain-protocol.md:260`

The bug being fixed is real: Go's `net/http` rejects `tcp://`. The chosen fix rewrites `tcp://` to `http://` (never `https://`). This is correct for `tcp://localhost:26657` (devnet) but means an operator who copy-pastes the documented onboarding flow ends up with **plaintext HTTP** in `chain.yaml`, even against a remote chain.

The `scripts/deploy-contract.sh:171` sed expression does the same: `sed 's|^tcp://|http://|; s|:26657$|:1317|'`. The CHANGELOG entry and example commands all show `tcp://` form. No warning is emitted to the operator that they've just configured a plaintext endpoint for a chain that may or may not have TLS available.

Threat model: a remote operator running `tribunal chain sync` over `http://remote-lcd:1317` has every settlement request observable in cleartext, and every response forgeable by anyone on the network path. Combined with F-SEC-203, an in-path attacker can fake inclusion / non-inclusion at will.

**Suggested defense:** (a) in `normalizeRPCScheme`, after rewriting `tcp://` → `http://`, additionally check whether the host looks remote (not `localhost`, not `127.0.0.1`, not a private RFC1918 range); if remote, emit a second stderr warning recommending `https://`. (b) update the docs and `init-testnet.sh` to use `https://` examples for non-devnet flows.

### F-SEC-206: `Sscanf("%d")` on multiplier silently truncates trailing garbage (Suggestion)

`cmd/tribunal/chain.go:84-86`

```go
var n uint64
if _, parseErr := fmt.Sscanf(resp.OutcomeRewardMultiplier, "%d", &n); parseErr == nil {
    cfg.OutcomeRewardMultiplier = n
```

Reproducible behavior: `fmt.Sscanf("100abc", "%d", &n)` returns `n=100, nread=1, err=nil`. A misbehaving LCD or a JSON injection on the contract response can quietly set the operator's local multiplier to a truncated value. Symmetrically, `"0xff"` parses to `0` with nil err (Sscanf treats `0xff` as `0` + leftover `xff`).

Impact is bounded: intent.md and `config.go:44-46` both state `outcome_reward_multiplier` is advisory / preview-only; no contract behavior depends on it. So this is a Suggestion, not a Warning. But the silent truncation means the operator's `tribunal chain query` displays will be wrong, and any future code that grows to depend on this value inherits the bug.

**Suggested defense:** use `strconv.ParseUint(resp.OutcomeRewardMultiplier, 10, 64)` instead. It rejects trailing garbage explicitly. Log a stderr warning on parse failure rather than silently keeping `0`.

### F-SEC-207: `cmd/tribunal-seed` has no production guard; one typo signs a fake TP against any configured chain (Suggestion)

`cmd/tribunal-seed/main.go:18-110`

The seed harness:

- Hardcodes labels `adversary-alpha` and `pm-alpha` (line 19-20).
- Loads the actual `~/.tribunal/chain.yaml` via `chain.LoadConfig("")` (line 91).
- On `--send`, broadcasts a `ResolveFindingBatch` containing a TP outcome for `F-e2e-001` against whatever chain that config points to (line 105).
- Uses `context.Background()` (line 105) — no timeout. Combined with F-SEC-201/F-SEC-203, this can hang indefinitely.
- Argument parsing is loose: `os.Args[1]` is taken as `planID` (line 24) AND iterated for `--send` flag (line 82). Running `tribunal-seed --send` sets `planID = "--send"` and writes a finding with that plan id.

Risk scenario: an operator's CI runner or dev box has `~/.tribunal/chain.yaml` pointing at mainnet (e.g., they ran `chain init` against prod earlier today). They `go run ./cmd/tribunal-seed --send`. The harness loads mainnet config, signs a "the-bug-was-real" TP for fake plan `--send` and fake finding `F-e2e-001`, and broadcasts it. The contract is unlikely to accept it (no matching commit exists), but the operator just spent gas and put a noisy failed tx on-chain.

The file header docs the harness as "tiny throwaway" but the file is checked into the release commit, not gated behind a build tag.

**Suggested defense:** (a) refuse to run unless an env var like `TRIBUNAL_SEED_OK=1` is set; (b) refuse to run if `cfg.ChainID` doesn't look like a test chain (reuse `looksLikeTestChain`); (c) require an explicit `--plan <id>` flag instead of positional arg; (d) wrap the Execute call in `context.WithTimeout(ctx, 60*time.Second)`. Or gate the whole thing with `//go:build seed` so it can't be accidentally built.

### F-SEC-208: `url.JoinPath(NodeREST, ..., txhash)` does not validate `txhash` shape (Suggestion)

`internal/chain/client.go:157`

`url.JoinPath` collapses `..` segments. Tested with `hash="../../../etc/passwd"`, output is `http://host/cosmos/etc/passwd`. The `txhash` value here comes from `res.TxHash` (parsed out of xiond's JSON output at `client.go:108`), so the trust source is the local xiond binary — generally fine. But if xiond is ever swapped for a docker exec into a container (a documented supported configuration via `XiondBinary` env, see `client.go:282-287`), or if a future change accepts a user-provided txhash for status checks, the path-traversal door is open.

**Suggested defense:** validate `txhash` against `^[0-9A-Fa-f]{64}$` before path-joining. Cheap, future-proof.

## Cross-Reviewer Ready Notes

- **For reviewer-arch:** F-SEC-203 (LCD trust) is fundamentally an architecture question — should `chain.Client` cross-check Tendermint RPC against LCD REST for state-of-the-world decisions? Worth flagging in v0.4 abstraction work. The `normalizeRPCScheme` function is a single-call helper that probably should live next to `Config.applyDefaults()` in `config.go` rather than at the top of `cmd/tribunal/chain.go`.
- **For reviewer-perf:** F-SEC-204 (serial pre-flight) is also a latency concern at large N. The parallelization suggestion serves both lenses. F-SEC-202 (missing per-attempt timeout) also has a perf flavor: the responsiveness of `WaitForTx` on flaky networks is bounded by the http.Client.Timeout, not the docstring's 300ms.
- **For PM (`pm-alpha`):** F-SEC-201, F-SEC-202, F-SEC-203, F-SEC-204 are inter-related — they form a single coherent "harden the LCD trust boundary" workstream for v0.3.3 / v0.4. Bundle them.

## Verdict

**Request Changes.** Two Warnings (F-SEC-201, F-SEC-203, F-SEC-204; F-SEC-202 is a doc/severity-mismatch warning) on the new write-path code's trust posture and resilience to a hostile LCD. The Warnings don't block on-chain correctness — the contract still validates — but they materially weaken the operator's ability to reason about settlement success. Combined with the seed-harness foot-gun (F-SEC-207), v0.3.2 should not ship to mainnet operators without at least: (a) per-query timeouts in pre-flight, (b) error classification in `WaitForTx`, (c) a `looksLikeTestChain` guard on the seed harness.

The Suggestions (F-SEC-205, F-SEC-206, F-SEC-208) are defense-in-depth — track for v0.3.3 but they don't gate the verdict.

## FINDINGS-TO-FILE

```
Warning|availability|F-SEC-201|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v032-audit/reviewer-sec.md#f-sec-201|WaitForTx aborts on first transient error instead of polling through it
Warning|documentation|F-SEC-202|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v032-audit/reviewer-sec.md#f-sec-202|Docstring claims a 300ms per-attempt timeout that does not exist in code
Warning|trust-boundary|F-SEC-203|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v032-audit/reviewer-sec.md#f-sec-203|WaitForTx trusts LCD verdict; hostile LCD can fake inclusion or starve sync
Warning|dos|F-SEC-204|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v032-audit/reviewer-sec.md#f-sec-204|Serial unbounded pre-flight queries let a slow LCD exhaust the sync ctx
Suggestion|tls-posture|F-SEC-205|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v032-audit/reviewer-sec.md#f-sec-205|tcp to http normalization cements plaintext HTTP as default for documented onboarding
Suggestion|input-validation|F-SEC-206|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v032-audit/reviewer-sec.md#f-sec-206|Sscanf parsing of outcome_reward_multiplier silently truncates trailing garbage
Suggestion|unsafe-default|F-SEC-207|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v032-audit/reviewer-sec.md#f-sec-207|cmd/tribunal-seed has no production guard; typo can sign fake TP on mainnet
Suggestion|input-validation|F-SEC-208|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v032-audit/reviewer-sec.md#f-sec-208|url.JoinPath with txhash collapses dotdot segments; validate hash shape
```
