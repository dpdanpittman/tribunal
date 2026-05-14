# Security Review — Tribunal v0.3.4 audit-driven fix release

**Reviewer:** `tribunal-reviewer-sec`
**Plan:** `P-v034-audit`
**Diff basis:** `fb37c3c^..fb37c3c` (commit `fb37c3c`, "v0.3.4: audit-driven fix release (P-v033-audit findings)")
**Verdict:** **Request Changes**

## Summary

v0.3.4 closes the regex-vs-grammar recursion cleanly. The old primitive — parse `raw_log` with a finding-id regex that didn't match the contract's identifier grammar — is gone. `regexp` and `strings` are dropped from `internal/chain/sync.go`. The structural pivot the P-v033 adversary prescribed (F-NEW-403) landed where it was supposed to land. The `looksLikeTestChain` token-aware rewrite shut every false-positive I could throw at it (29 adversarial chain ids, all classified the way the heuristic intends — see "Adversarial chain-ID walkthrough" below). `maxRecoveryAttempts = 5` caps the v0.3.3 gas-amplification surface. `preflight_concurrency` is operator-tunable with sensible default. CI is green: `go build`, `go vet`, `go test`, `gofmt` all clean.

But the methodology's deeper question — _did the pivot eliminate the trust hole, or relocate it?_ — gets a hard answer: **it relocated it, with the suppression vector amplified.**

The new recovery primitive (`preflight()` against the LCD) trusts the LCD's smart-query response as authoritative state-of-the-world. A hostile LCD that forges `{ "data": { "finding": {...} } }` for every entry in a failed batch causes the recovery layer to mark the entire batch as "already committed" and exit `submitCommitBatch` with `(nil, 0, nil)` — the operator's `SyncResult` reports `FindingsSent=0`, no error, no tx hash. From the operator's local view, the batch was already on-chain. **v0.3.3's hostile-LCD attack (F-SEC-301) cost the attacker control over ONE finding per retry, bounded by len(batch) iterations; v0.3.4 lets the same attacker suppress the ENTIRE batch in a single recovery cycle.** Same defect shape (LCD trusted as oracle for what's on-chain), strictly worse blast radius. Filing as **F-SEC-401 (Critical)**. The contract is still authoritative for actual on-chain state — no reputation forgery, no signature bypass — but the operator's local view of "what landed" is now fully LCD-controlled.

Secondary findings:

- **F-SEC-402 (Warning)** — `Query()` accepts ANY non-empty JSON object as a valid `FindingState` because `preflight()` only checks `resp.Finding != nil`. There's no validation that the returned `plan_id`/`finding_id`/`committed_at` match the query, that signatures roundtrip, or that the response is consistent with the request. Forging is trivial — a single `{"data":{"finding":{}}}` response is enough to set `committed[id] = true`. This is the input-validation gap that makes F-SEC-401 a single-line attack instead of a multi-step forgery.
- **F-SEC-403 (Suggestion)** — `preflight_concurrency` has no sanity cap. Operator setting `10000` is bounded by `len(ids)` in practice (capped at contract `MAX_BATCH_SIZE=100` for batched commits/resolves), so the foot-gun isn't catastrophic, but a typo or copy-paste of `concurrency: 10000` on a hot path that doesn't hit the batch cap (or a future call site) becomes a noisy self-DoS. One-line fix in `Config.validate()`.
- **F-SEC-404 (Suggestion)** — `looksLikeTestChain` is duplicated across `internal/chain/client.go:62-78` and `cmd/tribunal-seed/main.go:129-145`. The implementations are byte-identical today, but a future hardening (e.g., word-boundary regex for fuzzier inputs, or extra prod tokens like `livenet`/`canary`) in one but not the other reintroduces the v0.3.3 substring-vs-token drift. Plan.md acknowledges this duplication is in scope for reviewer Suggestions; consolidating to a shared helper costs five lines.

Carryforward from prior audits (see section below):

- **F-SEC-301-carryfwd-v034** — _superseded by F-SEC-401._ The regex-tainted-text version is closed (regex is deleted), but the structured-query version is the new shape; not a carryforward in the literal sense, but called out for trace.
- **F-SEC-205-carryfwd-v034** — silent `tcp://` → `http://` rewrite on every `LoadConfig` still ships. v0.3.4 didn't touch it. Severity unchanged from v0.3.3 (Warning).
- **F-SEC-208-carryfwd-v034** — `url.JoinPath(NodeREST, ..., txhash)` still doesn't validate `txhash` shape. Suggestion, unchanged.
- **F-SEC-206-carryfwd-v034** — `Sscanf` on `outcome_reward_multiplier` still truncates trailing garbage. Suggestion, unchanged.
- **F-SEC-304-carryfwd-v034** — preflight ctx-cancel partial-result invariant still untested. Suggestion, unchanged. v0.3.4 added a SECOND preflight call site (the recovery path), doubling the surface area for any future bug that depends on this invariant.
- **F-SEC-305-carryfwd-v034** — `Execute` broadcast-time error path still injectable via hostile RPC. Now coupled to F-SEC-401 — the broadcast error makes recovery happen, the LCD makes recovery suppress.

**Verdict: Request Changes.** One Critical (F-SEC-401), one Warning (F-SEC-402), two Suggestions (F-SEC-403, F-SEC-404), six carryforwards.

The convergence question (per intent.md): **PIVOTED BUT NOT CONVERGED.** v0.3.4 broke the regex-narrowness recursion (F-ARCH-301, F-SEC-302 are genuinely dead). But the trust-source recursion — LCD as oracle for state-of-the-world — has not been broken; it's been refactored. The next iteration cannot keep using the LCD smart-query as the recovery primitive's truth source without either (a) cross-source verification (Tendermint RPC `/abci_query` against the contract's storage root with a Merkle proof) or (b) reconciling the recovery decision with on-chain effects (re-broadcast the same batch and trust the contract's actual rejection cascade rather than a pre-emptive filter based on what the LCD says is committed). Without one of those, v0.3.5 will surface the same shape: a hostile LCD response is the input domain, and the contract's actual state is the truth, and the primitive trusts the LCD.

## Verification of plan tasks

### T1 — Structured-query recovery primitive — **IMPLEMENTED, with new Critical**

`internal/chain/sync.go:348-420` (commit + resolve recovery loops).

The shape is what intent.md described:

1. `Execute` rejection → assemble `ids = {FindingID for each commit in batch}` (line 362-365).
2. `preflight(ctx, planID, ids)` → returns `committed` map keyed by `FindingID`.
3. Filter `commits` to entries where `!committed[id]` (line 367-372).
4. If `len(filtered) == len(commits)` (no entries dropped), bail with `commit batch rejected and no entries already on-chain: %w` (line 373-376).
5. Otherwise log "dropped %d already-committed", reassign `commits = filtered`, retry (line 377-379).
6. Bounded by `maxRecoveryAttempts = 5` (line 349).

Architecture and termination story are clean. The regex helpers, `regexp` import, and `strings` import are gone. CHANGELOG and package doc both correctly describe the change.

**But:** the `committed` map (line 366) is populated entirely from LCD responses. The hostile-LCD attack v0.3.3's F-SEC-301 surfaced got worse, not better. See F-SEC-401 below.

### T2 — Regex helpers deleted + imports removed — **IMPLEMENTED**

Verified via `grep -rn "regexp\|alreadyCommittedRE\|alreadyResolvedRE\|matchDuplicate" /home/dan/src/tribunal/internal/chain/` — no hits. `regexp` and `strings` no longer imported in `sync.go`. `internal/chain/client.go` still imports `strings` (used for `strings.HasPrefix`/`strings.Fields`/`strings.ToLower`) which is unrelated. Clean.

### T3 — Per-plan ctx isolation in `SyncAll` — **IMPLEMENTED**

`internal/chain/sync.go:457-470`. Each plan runs under `context.WithTimeout(ctx, perPlanSyncBudget)` where `perPlanSyncBudget = 90 * time.Second`. The outer ctx still binds — if the caller passes a 30s ctx, that wins. `planCancel()` is invoked right after the `SyncPlan` call (line 465), so no goroutine leaks across plans.

**Concern (Suggestion territory, filing as note):** 90s is below the worst-case bound the intent.md math suggested. With `maxRecoveryAttempts=5`, `preflightConcurrency=8`, batch=100 findings, `preflightAttemptTimeout=3s`: each recovery cycle costs `1 broadcast tx + ceil(100/8) * 3s = 1 tx + ~38s` worst case for the preflight alone. Five recovery cycles = `5 tx + ~190s` for the preflight rounds alone. Add in tx wait time (LCD polling up to ctx limit) and you're well past 90s before maxRecoveryAttempts is exhausted. So in pathological cases the per-plan ctx times out BEFORE the recovery cap fires, and the error operator sees is `wait for inclusion: context deadline exceeded` rather than `commit batch exhausted recovery attempts`. Not a security defect — just an observability one. Filed via Cross-Reviewer notes for reviewer-perf.

### T4 — CLI renders partial results before erroring — **IMPLEMENTED**

`cmd/tribunal/chain.go:214-230`. Diff is straightforward — the `if err != nil { return err }` moved below the result-printing loop. F-ARCH-303 closed.

No security implications.

### T5 — `looksLikeTestChain` token-aware (chain.Client) — **IMPLEMENTED**

`internal/chain/client.go:62-78`. Token-aware as specified. See "Adversarial chain-ID walkthrough" below — I exercised 29 adversarial patterns and the heuristic behaves correctly for all of them under its stated semantics.

### T6 — `looksLikeTestChain` token-aware (tribunal-seed) — **IMPLEMENTED**

`cmd/tribunal-seed/main.go:129-145`. Byte-identical to the chain.Client version. F-SEC-404 filed as a Suggestion on the duplication.

### T7 — `maxRecoveryAttempts = 5` — **IMPLEMENTED**

`internal/chain/sync.go:320` and used at line 349 / 389. Constant, not operator-tunable (correct — making it tunable would let an operator set it high enough to re-enable the v0.3.3 gas-amplification surface). The bound is a strict `<`, so attempts are `0..4` — five iterations max.

**Sanity check on the cap:** the comment claims "Five attempts handles every realistic partial-failure scenario." In the structured-query world, each non-success iteration drops _at least one entry_ from the batch (otherwise the bail-out at line 373 fires). So in the worst non-hostile case, five iterations can absorb up to ~five duplicate findings spread across the batch. That's the right calibration for a single plan-close sync against a non-adversarial LCD. A hostile LCD that forges N duplicates per cycle (rather than 1) collapses the recovery into a single iteration; the cap is moot in that case (see F-SEC-401).

### T8 — `preflight_concurrency` field in `Config` — **IMPLEMENTED, with Suggestion**

`internal/chain/config.go:61-65`. Field added with the `omitempty` yaml tag so configs predating v0.3.4 load with zero (then default to 8 via line 252-255 of sync.go). Backward compat verified.

**No sanity cap.** An operator setting `preflight_concurrency: 10000` is bounded by `len(ids)` in normal sync paths (line 256-258 in sync.go) which can't exceed `MAX_BATCH_SIZE=100` per the contract's batch validation. So today the foot-gun is bounded by that downstream cap. But:

- If a future call site uses `preflight()` against an id-set larger than `MAX_BATCH_SIZE` (e.g., a full-plan reconciliation pass that doesn't go through `submitCommitBatch`), the operator's setting fans out unchecked.
- `validate()` already has format checks for `chain_id`, `node_rpc`, etc. Adding `if c.PreflightConcurrency > 256 { return errors.New("preflight_concurrency capped at 256") }` is a one-liner that prevents the foot-gun from ever needing to be re-litigated.

Filed as F-SEC-403 (Suggestion).

### T9 — New `TestLooksLikeTestChain_TokenAware` — **IMPLEMENTED**

`internal/chain/sync_test.go:19-45`. 11 cases. Covers:

- Standard testnet/devnet/mainnet (3 cases).
- Hostile substring-vs-token (2 cases: `xion-mainnet-test-fork`, `xion-test-mainnet-fork`).
- Prod marker, local + devnet, attestation/untested substring-vs-token (3 cases).
- production marker, empty (2 cases).

Solid coverage of the F-SEC-303 attack surface. See my adversarial walkthrough below for cases the test doesn't cover; none invalidate the heuristic's correctness, but a couple are surprising under the stated rules (e.g., `prodnet-1` returns `false` for "not test" because `prodnet` isn't in either set — it's treated as production, which is correct intent but surprising semantics).

### T10 — `TestMatchDuplicate_CommitErrorParsing` removed — **IMPLEMENTED**

Verified via `grep TestMatchDuplicate` — no hits.

### T11 — `sync.go` package doc updated — **IMPLEMENTED**

`internal/chain/sync.go:25-39`. Package doc describes the structured-query primitive and cross-references P-v033 F-ARCH-301 and F-SEC-301. Accurate.

### T12 — CHANGELOG v0.3.4 entry — present, accurate

Diff hunks line up with documented behavior. No security-sensitive omissions.

## Adversarial chain-ID walkthrough

Specifically called out in plan.md: "What about chain IDs an attacker controls naming for — e.g. `xion-test-mainnet-bypass`?" I ran 29 adversarial patterns through the actual code (compiled inline, see `/tmp/lltc.go` from this review session). Results:

| chain_id                                          | result | expected | notes                                                                       |
| ------------------------------------------------- | ------ | -------- | --------------------------------------------------------------------------- |
| `xion-test-mainnet-bypass`                        | false  | false    | mainnet token wins                                                          |
| `xion-mainnet-test-fork`                          | false  | false    | mainnet token wins                                                          |
| `xion-test-mainnet-fork`                          | false  | false    | mainnet token wins regardless of position                                   |
| `xion-1`                                          | false  | false    | no tokens; treated as PROD (fail-closed, correct)                           |
| `cosmoshub-4`                                     | false  | false    | no tokens; PROD                                                             |
| `juno-1`                                          | false  | false    | no tokens; PROD                                                             |
| `osmosis-1`                                       | false  | false    | no tokens; PROD                                                             |
| `sandbox-1`                                       | false  | false    | `sandbox` not in test set — treated as PROD                                 |
| `staging-1`                                       | false  | false    | `staging` not in test set — treated as PROD                                 |
| `qa-1`                                            | false  | false    | `qa` not in test set — treated as PROD                                      |
| `experimental-1`                                  | false  | false    | not in either set — PROD                                                    |
| `preview-1`                                       | false  | false    | PROD                                                                        |
| `playground-1`                                    | false  | false    | PROD                                                                        |
| `foobar-dev`                                      | true   | true     | `dev` is a discrete token                                                   |
| `foobar-developer`                                | false  | false    | `developer` is not `dev` — substring suppression works                      |
| `foo-DEV-prod`                                    | false  | false    | PROD token wins                                                             |
| `DEV-foo`                                         | true   | true     | case-insensitive; dev token wins                                            |
| `chain_with_underscore`                           | false  | false    | no dashes → single token "chain_with_underscore" not in either set          |
| `mainnet`                                         | false  | false    | single token = mainnet                                                      |
| `prodnet`                                         | false  | false    | `prodnet` ≠ `prod` ≠ in either set → fails through to PROD (correct intent) |
| `prodnet-1`                                       | false  | false    | same                                                                        |
| `testnet.evil.com`                                | false  | false    | splits on `-` only → single token `testnet.evil.com` not in test set        |
| `testnet/x`                                       | false  | false    | single token `testnet/x` not in test set                                    |
| `-testnet-`                                       | true   | true     | splits to `["", "testnet", ""]` → matches                                   |
| `testnet`                                         | true   | true     | bare match                                                                  |
| `MAINNET`                                         | false  | false    | lowercased, mainnet wins                                                    |
| `MaInNeT-1`                                       | false  | false    | case-insensitive                                                            |
| `my-test-chain-with-mainnet-substring-mainnetism` | false  | false    | mainnet token wins                                                          |
| `mainnet-shadow` / `shadow-mainnet`               | false  | false    | mainnet wins                                                                |

**Two findings from this:**

1. The heuristic is fail-CLOSED in `tribunal-seed` (`!looksLikeTestChain && !allowProd → refuse to send`). This is the safer bias: unknown chain ids are treated as PROD, requiring the operator to explicitly `--allow-prod`. Confirmed: `xion-1`, `juno-1`, `staging-1`, `sandbox-1`, `qa-1` all refuse without `--allow-prod`. Good.

2. The heuristic is fail-OPEN in `chain.Client.New` (`KeyringBackend=="test" && !looksLikeTestChain → emit warning`). Same heuristic, inverse semantics. So `testnet.evil.com` (with a dot, not a dash) → `false` → triggers the keyring warning. That's correct semantically — an attacker constructing a chain id with `.` instead of `-` to bypass the heuristic still gets warned because the heuristic refuses to call the chain "test." Net effect: attackers can't make the warning silent via clever naming.

**No security-relevant bypass found.** The heuristic is solid against the adversarial space I could construct. F-SEC-303 is genuinely closed.

There is a minor observability nit: `prodnet-1` returns `false` (treated as PROD) because `prodnet` isn't in either set. The intent is correct — we don't know what `prodnet` is, so treat as PROD. But the symmetric "we don't know what `testnet-ish-but-not-testnet` is, so treat as PROD" might surprise operators who deliberately pick non-standard suffixes. Suggestion-level if anything — not filing.

## New findings

### F-SEC-401: Structured-query recovery trusts LCD response as oracle for on-chain state; hostile LCD silently suppresses entire batch (Critical)

`internal/chain/sync.go:360-379` (commit recovery), `internal/chain/sync.go:399-417` (resolve recovery), `internal/chain/client.go:275-316` (`Query`), `internal/chain/query.go:152-162` (`Finding`).

**Attack scenario.**

Operator runs `tribunal chain sync` against a hostile (or MITM'd) LCD endpoint. Plan has 50 uncommitted findings.

1. Preflight (`sync.go:133`) queries each of 50 findings. Hostile LCD returns `{"data": null}` or absent `finding` field → `committedOnChain[F] = false` for all. Preflight succeeds, batch is built fully.
2. `submitCommitBatch` calls `Execute` (sync.go:355). xiond broadcasts the tx; broadcast-mode=sync returns success (code=0, txhash=X).
3. `WaitForTx` polls the hostile LCD for txhash X. LCD returns `{"tx_response":{"code":11,"raw_log":"out of gas"}}` → `WaitForTx` returns `tx X failed on-chain (code=11): out of gas` (client.go:188-189). The tx may have actually landed cleanly or not landed at all — operator's only signal is the LCD's response.
4. `Execute` propagates the error. `submitCommitBatch` enters recovery (sync.go:360).
5. Recovery loops over 50 IDs, calls `s.preflight(ctx, planID, ids)`. Hostile LCD responds to EACH of the 50 `Finding(planID, F_i)` queries with `{"data": {"finding": {}}}`. `Query()` (client.go:312) parses the envelope, extracts `data`, returns it. `Finding()` (query.go:158) unmarshals into `FindingResp{Finding: &FindingState{}}`. Since `resp.Finding != nil`, `preflight()` (sync.go:272-276) marks `committed[F_i] = true` for all 50.
6. Filter (sync.go:368): `committed[c.FindingID]` is true for every entry → `filtered` is empty after loop.
7. `len(filtered) == len(commits)`? `len(filtered) = 0`, `len(commits) = 50`. **They differ.** So the bail-out at line 373 does NOT fire. Stderr emits: `tribunal: commit batch recovered via state query, dropped 50 already-committed, retrying with 0 findings`.
8. `commits = filtered` → `commits` is now `[]`. Loop iterates: `len(commits) == 0` (line 350) → `return nil, 0, nil`.
9. `submitCommitBatch` returned `(nil, 0, nil)`. `SyncPlan` records `result.FindingsSent = 0`, no `CommitTxHash`, no error.
10. `SyncAll` sees the plan succeeded. CLI prints `findings=0` for the plan and exits 0.

**Operator's view of the world after this attack:** the plan has been settled "successfully" with 0 findings sent on-chain. There's no error. There's no tx hash. Re-running `tribunal chain sync` repeats the attack: preflight against the same hostile LCD still says all findings are committed, so commits are filtered out entirely BEFORE reaching `submitCommitBatch`, and the sync exits clean with `FindingsSent=0`. **The findings are NEVER settled until the operator points at a different LCD.**

Compared to v0.3.3's F-SEC-301:

| Aspect                                 | v0.3.3 (F-SEC-301)                                                                                                                                                | v0.3.4 (F-SEC-401)                                                                                                                                                   |
| -------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Attacker controls...                   | Which ONE finding the operator drops per retry (via raw_log regex match)                                                                                          | Which ALL findings the operator drops per recovery cycle (via per-id smart-query responses)                                                                          |
| Suppression cost to attacker           | Must craft a valid contract-error-shaped raw_log per retry                                                                                                        | Must respond `{"data":{"finding":{}}}` to each `Finding` query — trivial                                                                                             |
| Operator gas burn                      | High (one broadcast per dropped entry, bounded by `len(batch)`)                                                                                                   | Low (one broadcast, then recovery suppresses without further broadcasts)                                                                                             |
| Operator's view if all entries dropped | `submitCommitBatch` returns `contract reported duplicate X not in batch` or exhausts retries — visible error                                                      | `submitCommitBatch` returns `(nil, 0, nil)` — `FindingsSent=0`, no error, no tx hash                                                                                 |
| Persistent vs one-shot                 | Re-running sync after attack: preflight is parallel, attacker can also forge there, but the regex recovery would surface different IDs on retry — distinguishable | Re-running sync: preflight forges committed=true again → commits never built → identical clean exit with `FindingsSent=0`. Operator has no signal anything is wrong. |

**v0.3.4 is strictly worse than v0.3.3 on the censorship axis.** The gas-burn axis is better (no more amplification), but censorship is the integrity concern, and the trust hole has been EXPANDED, not closed. The pivot did not break the recursion on the LCD-as-truth defect shape; it moved the consequence from "drop one entry, log a recovery, surface an exhaustion error" to "drop everything, log success, exit clean."

**Why the contract is still safe (so this is Critical and not Catastrophic):**

The contract's on-chain state is untouched. No reputation is forged. Per-entry signatures still validate against `canonical_finding_message` (commit.rs:73-91). What this attack denies is _settlement_ — the operator's local ledger believes the findings are settled, but they aren't. The harm is:

- **Audit suppression.** A hostile LCD operator (or MITM) can permanently prevent an operator's findings from being settled, with no error signal.
- **Reputation arrest.** If an adversary's findings are in a plan being settled, suppressing the entire batch denies the adversary's stake-return and reward. Same for PM resolutions — suppressing the resolve batch leaves stakes locked.
- **Trust erosion in the "I settled it" guarantee.** Tribunal's whole value proposition is "the audit is on-chain." If a hostile LCD can convince the operator that the audit was settled when it wasn't, Tribunal's claim that on-chain settlement is verifiable depends entirely on the operator catching the discrepancy via an out-of-band query (which they wouldn't think to do).

**Severity rationale: Critical, not Warning.**

v0.3.3's F-SEC-301 was filed at Warning because the attacker controlled ONE finding per retry, and the operator saw `findings=N-1` instead of `findings=N` — an observable discrepancy if the operator counts. v0.3.4 lets the attacker hide the discrepancy entirely: `findings=0` looks identical to "everything was already on-chain" (the legitimate idempotent re-sync case). The attack is observationally indistinguishable from the happy path. Critical is the right ladder rung for a defect that (a) silently destroys an operator-visible invariant, (b) is one-step exploitable by a single trust-boundary actor, and (c) has no detection mechanism short of external on-chain audit.

This is not a CosmWasm-level integrity violation — the contract is fine. It IS a Tribunal-level integrity violation, because Tribunal's job is to translate "this audit happened" into "the chain knows it," and v0.3.4 lets a hostile LCD lie about the second half.

**Suggested defense (multiple options, sorted by cost):**

(a) **Strict preflight response validation (cheapest).** In `preflight()`, after the LCD returns a `FindingResp`, verify the response's `plan_id` and `finding_id` match the query, that `agent_pubkey` is non-empty, that `committed_at` parses as a valid timestamp, that `claim_hash` is non-empty. Reject malformed responses as `committed=false`. This costs a few lines and makes the trivial `{"data":{"finding":{}}}` forgery fail. A determined attacker can still forge a fully-shaped response (the LCD doesn't sign anything), but the bar goes from "any non-empty JSON" to "construct a plausible-looking finding state."

(b) **Verify against operator's local signature.** When the recovery layer marks an entry "already committed," cross-check the LCD-supplied `claim_hash` and `agent_pubkey` against what the operator's local `commits[]` slice has. If they don't match, the LCD is lying. This is the "cross-source the duplicate" defense the v0.3.3 sec review suggested, just adapted to the structured-query path. Costs ~10 lines of code and gives true cross-source verification for the censorship attack. Strongly recommended.

(c) **Authenticated chain queries.** Replace the LCD smart-query with Tendermint RPC `/abci_query` against the contract's storage root, with the IAVL Merkle proof verified against the latest block header. The block header has the validator set's signatures (BFT consensus). This is the gold standard and removes the LCD from the TCB entirely. Costs more code and a new dependency on Tendermint RPC proof verification, but it's the architectural fix that closes this defect class permanently. Worth scoping for v0.4.

(d) **Loud failure when recovery filters the entire batch.** If `len(filtered) == 0` and `len(commits) > 0`, treat as suspicious and refuse to silently return `(nil, 0, nil)`. Instead, surface an explicit error: `commit batch fully suppressed by recovery — verify LCD trust`. This is a one-line defense that closes the silent-suppression vector even if (a)-(c) aren't shipped. Operator still sees an error and can investigate. Combined with (a) or (b), this is a robust v0.3.5 minimum.

**My recommendation for v0.3.5:** (a) + (d) ship as table stakes. (b) ships as the v0.3.5 architectural fix. (c) goes on the v0.4 roadmap with an ADR.

### F-SEC-402: `preflight()` accepts any non-empty FindingResp as proof-of-commitment with no field validation (Warning)

`internal/chain/sync.go:272-276` + `internal/chain/query.go:152-162`.

```go
// query.go:152-162
func (c *Client) Finding(ctx context.Context, planID, findingID string) (*FindingResp, error) {
    raw, err := c.Query(ctx, &QueryMsg{Finding: &QueryFinding{PlanID: planID, FindingID: findingID}})
    if err != nil {
        return nil, err
    }
    var resp FindingResp
    if err := json.Unmarshal(raw, &resp); err != nil {
        return nil, fmt.Errorf("parse finding: %w", err)
    }
    return &resp, nil
}

// sync.go:272-276 — inside preflight worker
resp, err := s.Client.Finding(attemptCtx, planID, id)
cancel()
if err != nil || resp == nil || resp.Finding == nil {
    resCh <- result{id: id}
    continue
}
resCh <- result{id: id, committed: true, resolved: resp.Finding.Resolution != nil}
```

The check is `resp.Finding != nil`. Nothing else. A hostile LCD response of `{"data":{"finding":{}}}` (literally one byte beyond what's required to construct a non-nil pointer) sets `committed = true`. This is the input-validation gap that makes F-SEC-401 trivial.

The `FindingState` struct has 8 fields (plan_id, finding_id, agent_pubkey, severity, claim_hash, stake, committed_at, resolution). None are validated. A legitimate contract response (from `query/finding.rs:6-9`) populates all of them via `FINDINGS.may_load`, but the client doesn't enforce that contract.

**Compounding with F-SEC-301-carryfwd:** the v0.3.3 review noted that `Execute` returns `&res, err` with an LCD-tainted `raw_log` on the broadcast-time failure path. The recovery layer in v0.3.4 doesn't parse that raw_log anymore (good), but it ALSO doesn't validate the LCD's smart-query response (bad). The trust posture got narrower on one axis (no more text parsing) but stayed wide-open on another (structured responses are accepted at face value).

**Suggested defense:** add a `validateFindingResponse(planID, findingID string, resp *FindingResp) error` helper that checks:

- `resp.Finding.PlanID == planID`
- `resp.Finding.FindingID == findingID`
- `resp.Finding.AgentPubkey != ""`
- `resp.Finding.CommittedAt != ""` (and ideally parses as a valid timestamp)
- `resp.Finding.ClaimHash != ""`
- `resp.Finding.Severity` is one of {`critical`, `warning`, `suggestion`}

Call from `preflight()` worker. On validation failure, treat as `committed=false` and emit a stderr warning so operators see the LCD is misbehaving. ~15 lines.

**Severity rationale: Warning.** The defect is necessary-but-not-sufficient for F-SEC-401's Critical. The fix is cheap and removes the trivial-forgery vector. Filed separately because closing F-SEC-402 doesn't close F-SEC-401 (a sophisticated attacker can construct a well-formed response), but F-SEC-401's full fix should not be predicated on F-SEC-402's narrow patch — they're additive.

### F-SEC-403: `preflight_concurrency` has no sanity cap; future call sites without a downstream batch-size limit are unbounded (Suggestion)

`internal/chain/config.go:60-65` + `internal/chain/sync.go:252-258`.

```go
// config.go
PreflightConcurrency int `yaml:"preflight_concurrency,omitempty"`
```

`Config.validate()` (config.go:118-138) does not check `PreflightConcurrency`. An operator setting `preflight_concurrency: 10000` in chain.yaml will:

- Today: be bounded by `if len(ids) < workers { workers = len(ids) }` in sync.go:256-258 → workers capped at `len(ids)` per call. `len(ids)` is the union of findings + resolutions + queue entries for a plan. Plans with 10000 findings would spawn 10000 goroutines hitting the LCD simultaneously.
- Plans with <100 findings (the contract's MAX_BATCH_SIZE) → bounded to <100 goroutines. Self-DoS surface is small.
- Plans with >100 findings → all goroutines fire, but the eventual `submitCommitBatch` will reject with `BatchTooLarge` (contract `MAX_BATCH_SIZE=100`). So the operator has _already_ broken something before the concurrency hits. Not a new attack surface.

**The Suggestion:** add `validate()` check for `PreflightConcurrency`. A reasonable cap is 256 (32x the default) — well above any conceivable legitimate use case and far below LCD connection-limit territory.

```go
// in config.go validate()
if c.PreflightConcurrency > 256 {
    return fmt.Errorf("preflight_concurrency must be <= 256 (got %d)", c.PreflightConcurrency)
}
```

**Severity rationale: Suggestion.** Today's attack surface is bounded by contract MAX_BATCH_SIZE. Filing because (a) defense in depth, (b) a future call site that doesn't have a downstream cap (e.g., a `tribunal chain audit` command that reconciles a 10000-finding plan against the LCD) would surface this with no warning, (c) the fix is one line in `validate()`.

### F-SEC-404: `looksLikeTestChain` is duplicated across `chain.Client` and `tribunal-seed` with no shared implementation (Suggestion)

`internal/chain/client.go:62-78` + `cmd/tribunal-seed/main.go:129-145`.

The two implementations are byte-identical today. Plan.md explicitly notes this is in scope for Suggestion-level findings: "Reviewers may file Suggestions on the duplication if they consider it brittle."

The duplication is brittle for two reasons:

1. **Drift risk.** v0.3.3 had two implementations of the substring check; v0.3.4 has two implementations of the token-aware check. If v0.3.5 adds `livenet`/`canary`/`bootstrap` to the test-marker set in one but not the other, the `tribunal-seed` guard and the `chain.Client` warning will disagree about the same chain id. Either:
   - `chain.Client` sees test-like, suppresses warning — but `tribunal-seed` refuses unless `--allow-prod` → user gets confused
   - `chain.Client` sees not-test, emits keyring warning — but `tribunal-seed` sends without `--allow-prod` → user gets the false-positive of a warning that the seed binary contradicts.
2. **Inverse semantics increase the chance of correctness bugs.** `chain.Client` uses the function to decide "should I warn?" (returns warning when NOT a test chain). `tribunal-seed` uses it to decide "should I refuse?" (refuses when NOT a test chain). A future maintainer reading one in isolation might invert the boolean in the other.

**Suggested defense:** consolidate into `internal/chain/heuristics.go` (or co-locate in `chain.Client`) and import from `tribunal-seed`. The function has no `Client` dependency — it's a pure string predicate — so the consolidation is mechanical.

**Severity rationale: Suggestion.** Today's behavior is correct. The risk is future drift, which is structural-not-immediate.

## Carried-over findings from prior audits

### F-SEC-301-carryfwd-v034: Hostile-LCD recovery suppression — _superseded by F-SEC-401_ (not separately re-filed)

The literal v0.3.3 F-SEC-301 (LCD-tainted raw*log regex) is \_closed* by v0.3.4's deletion of the regex layer. The defect shape, however, persists in the structured-query primitive. Filed forward as F-SEC-401 with the structurally-equivalent attack model. Not double-filing.

### F-SEC-205-carryfwd-v034: `tcp://` → `http://` silent rewrite on every LoadConfig (Warning)

`internal/chain/config.go:89-91`. Unchanged from v0.3.3.

The rewrite still happens on every `LoadConfig`, with the silent comment at line 87-88: "Silent rewrite — log handled at the `chain init` boundary." Operators upgrading from v0.3.1 to v0.3.4 against an old chain.yaml still get plaintext HTTP substituted with no notification.

**Compounded by F-SEC-401:** if the LCD is reached over plaintext HTTP, an in-path MITM has full control over the smart-query responses and can execute the F-SEC-401 censorship attack with no need to compromise the LCD itself. Specifically:

1. MITM intercepts the operator's HTTPS-was-supposed-to-be-HTTPS-but-now-HTTP smart-query.
2. Returns forged `{"data":{"finding":{}}}` for every `Finding` query.
3. Returns forged `{"tx_response":{"code":11,"raw_log":"..."}}` for the WaitForTx poll.
4. The combined attack: F-SEC-401 censorship now requires only network-layer control, not control of the LCD endpoint itself.

**Severity rationale:** unchanged from v0.3.3 (Warning). v0.3.4 didn't change the silent-rewrite path. Filing as carryforward.

**Suggested defense:** one-line stderr warning in `LoadConfig` on rewrite. Bonus: refuse to rewrite to `http://` against a chain id that returns `false` from `looksLikeTestChain` unless explicit `--allow-plaintext` is set (or a config field `allow_plaintext_rpc: true`).

### F-SEC-208-carryfwd-v034: `txhash` not validated before path-joining (Suggestion)

`internal/chain/client.go:222`. Unchanged from v0.3.2 and v0.3.3.

Recommendation stands: validate `txhash` against `^[0-9A-Fa-f]{64}$` before path-joining. Two lines.

### F-SEC-206-carryfwd-v034: `Sscanf` truncates trailing garbage on outcome_reward_multiplier (Suggestion)

`cmd/tribunal/chain.go` (the parse site noted in v0.3.3 review). Unchanged from v0.3.2.

Recommendation stands: switch to `strconv.ParseUint`.

### F-SEC-304-carryfwd-v034: Preflight ctx-cancel partial-result invariant has no test (Suggestion, surface area DOUBLED)

`internal/chain/sync.go:266-268` (the ctx-check inside worker).

v0.3.3 review filed this because preflight was called from one site (`SyncPlan`). **v0.3.4 added a SECOND call site** (`submitCommitBatch` line 366, `submitResolveBatch` line 405) without adding any test for the ctx-cancel invariant in EITHER path. The bug surface is now strictly larger.

**Specific worry for v0.3.4:** if ctx cancels mid-recovery, workers may finish their attempts and write `committed=true` for some IDs while others are missing. The recovery layer then filters based on partial data — entries with no result get `committed[id] = false` (zero-value) and stay in the batch; entries with finished results get correctly dropped if committed, kept if not. So the partial-cancellation behavior is at least consistent with "missing data = not committed" (safe direction).

BUT: the recovery layer then does `commits = filtered` and the for-loop continues. The next iteration calls `Execute` against the partial batch with a CANCELLED ctx. `xiond` will refuse, recovery enters again, preflight is called against a cancelled ctx (which returns immediately with no results since workers exit on `ctx.Err() != nil`), `committed` is empty, `filtered == commits`, bail-out fires at line 373 with the underlying ctx-cancellation error. OK — terminates correctly.

**Worth testing.** A test that cancels ctx between Execute and preflight, and asserts that recovery surfaces the cancellation rather than silently completing, would pin this invariant. Doubly worth testing now that there are two call sites.

### F-SEC-305-carryfwd-v034: Execute broadcast-time error path injectable via hostile RPC (Suggestion, coupled to F-SEC-401)

`internal/chain/client.go:148-150`.

```go
if res.Code != 0 {
    return &res, fmt.Errorf("tx broadcast failed (code=%d): %s", res.Code, res.RawLog)
}
```

`res.RawLog` here comes from the RPC node's broadcast response (distinct from the LCD's wait-for-inclusion response). v0.3.3 review noted this as a parallel injection vector. v0.3.4's recovery layer no longer parses the error text, so the RPC-supplied raw_log can't be used to pick which entry the operator drops. BUT: a hostile RPC node can still trigger the F-SEC-401 attack chain by causing `Execute` to fail, which forces the recovery path, which then trusts the LCD. So if RPC and LCD are co-compromised (likely — they're often the same vendor's infra), F-SEC-401 is reachable via either trust source.

**Severity rationale:** unchanged from v0.3.3 (Suggestion). The fix (validate RPC-supplied raw_log shape, or use Tendermint RPC's structured response rather than raw_log) is upstream of F-SEC-401's root cause; closing F-SEC-401 (cross-source verification) closes this too.

## Cross-Reviewer Ready Notes

- **For reviewer-arch:** F-SEC-401 is fundamentally an architectural defect — the `chain.Client` abstraction should expose a "verified-on-chain-state" affordance distinct from a "what-the-LCD-says-the-state-is" affordance. The current `Finding()` method conflates them. A `FindingVerified(ctx, planID, findingID)` that does ABCI-proof verification would be the architectural fix. v0.4 roadmap.
- **For reviewer-arch:** the recovery loop's `(nil, 0, nil)` silent-success return path (sync.go:350-352, when `len(commits) == 0` after filtering) deserves a structural review. The intent is "everything was already on-chain, no tx needed" — legitimate in the happy idempotent re-sync case. But it's ALSO the F-SEC-401 attack's clean-exit path. The two cases need to be structurally distinguishable. Perhaps `SyncResult.PreflightedAway` separate from `SyncResult.RecoveryFiltered`?
- **For reviewer-perf:** F-SEC-401's defense (a) — strict response validation — adds ~10 syscalls per preflight worker (string equality checks). Negligible at 8-worker scale. Defense (b) — cross-check against local `commits[]` — is also a hashmap lookup, negligible. Defense (c) — ABCI proof verification — is meaningful CPU + an additional RPC roundtrip per finding. Worth quantifying when scoping v0.4.
- **For reviewer-perf:** the per-plan ctx budget of 90s vs worst-case recovery wallclock of ~190s+ (see T3 verification above) needs explicit math. If the recovery primitive can outlast the ctx, the cap is operative only in the happy-path; in pathological cases the operator sees `context deadline exceeded`, not `exhausted recovery attempts`. Worth a perf-level note.
- **For PM (`pm-alpha`):** F-SEC-401 + F-SEC-402 form one coherent v0.3.5 workstream: validate LCD responses defensively + add cross-source verification for the recovery decision. F-SEC-403 + F-SEC-404 are independent cleanups; can ship anytime. Carryforwards F-SEC-205, F-SEC-208, F-SEC-206 are independent of the v0.3.4 diff and can be cleared with one or two lines each.
- **For adversary:** the convergence question's answer is **pivoted but not converged** (per intent.md's classification). The recursion is partially broken — the regex-grammar narrowness defect is dead — but the LCD-as-truth defect shape has been refactored, not eliminated. The fact that the v0.3.4 attack is STRICTLY WORSE than the v0.3.3 attack (full-batch suppression vs single-entry suppression, no error vs visible error) is the kind of signal that says the pivot prescribed by P-v033 was structurally correct but didn't go deep enough. v0.3.5 needs either response-validation (cheap, partial close) or cross-source proof (expensive, full close). The adversary's prior call to "stop parsing LCD-supplied text" was right; the missing word was "AND stop trusting LCD-supplied data."

## Verdict

**Request Changes.** One Critical (F-SEC-401), one Warning (F-SEC-402), two Suggestions (F-SEC-403, F-SEC-404), plus carryforwards.

The v0.3.4 fixes solve the regex-grammar narrowness recursion. They do not solve the LCD-as-oracle integrity hole, and they made the suppression vector strictly worse than v0.3.3 did. The methodology earned credit for breaking the regex-vs-grammar recursion but lost credit for accepting a fix that opens a wider hole in the same trust dimension. v0.3.5 must close F-SEC-401 with either strict response validation (Defense (a) + (d) above) or cross-source verification (Defense (b)) before v0.4 ships.

CI: `go build ./...` clean, `go vet ./...` clean, `gofmt -l .` clean, `go test ./...` all green. No CI gate blocker beyond the findings above.

## FINDINGS-TO-FILE

```
Critical|trust-boundary|F-SEC-401|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v034-audit/reviewer-sec.md#f-sec-401|Structured-query recovery trusts LCD as oracle for on-chain state; hostile LCD silently suppresses entire batch with FindingsSent=0 clean exit
Warning|input-validation|F-SEC-402|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v034-audit/reviewer-sec.md#f-sec-402|preflight accepts any non-empty FindingResp as proof-of-commitment with no field-level validation against the query
Suggestion|unsafe-default|F-SEC-403|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v034-audit/reviewer-sec.md#f-sec-403|preflight_concurrency has no sanity cap; future call sites without batch-size limit can be configured to saturate LCDs
Suggestion|defense-in-depth|F-SEC-404|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v034-audit/reviewer-sec.md#f-sec-404|looksLikeTestChain duplicated across chain.Client and tribunal-seed; future drift between the two implementations is a structural risk
Warning|tls-posture|F-SEC-205-carryfwd-v034|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v034-audit/reviewer-sec.md#f-sec-205-carryfwd-v034|tcp to http silent rewrite still fires on every LoadConfig; compounds with F-SEC-401 under MITM
Suggestion|input-validation|F-SEC-208-carryfwd-v034|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v034-audit/reviewer-sec.md#f-sec-208-carryfwd-v034|txhash still not validated against hex-64 shape before path-joining
Suggestion|input-validation|F-SEC-206-carryfwd-v034|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v034-audit/reviewer-sec.md#f-sec-206-carryfwd-v034|Sscanf parsing of outcome_reward_multiplier still truncates trailing garbage silently
Suggestion|race-condition|F-SEC-304-carryfwd-v034|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v034-audit/reviewer-sec.md#f-sec-304-carryfwd-v034|Preflight ctx-cancel partial-result invariant still untested; surface area doubled in v0.3.4 by adding recovery-path call site
Suggestion|trust-boundary|F-SEC-305-carryfwd-v034|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v034-audit/reviewer-sec.md#f-sec-305-carryfwd-v034|Execute broadcast-time error path still injectable via hostile RPC; coupled to F-SEC-401 as trigger for recovery path
```
