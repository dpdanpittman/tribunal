# Adversary Attack Report — Tribunal v0.3.2 Tooling Audit

**Adversary:** `tribunal-adversary` (single-model)
**Plan:** `P-v032-audit`
**Targets:** the three trio Request-Changes verdicts at `.tribunal/reports/P-v032-audit/reviewer-{arch,sec,perf}.md`
**Diff basis:** `HEAD~1..HEAD` (`f186e92`, "v0.3.2: devnet-driven tooling fixes (F1-F6)")
**Verdict:** **Escalate** — the trio's findings stand, but five additional defects materially expand the blast radius. Three of them are correctness-affecting under realistic operator workflows.

---

## Summary

The trio did its job: every Critical / Warning it filed is real, well-grounded, and pointing at the right defect. Verdict alignment is high.

But the trio's three lens-trained reports converge on the same six "obvious" angles — transient-error abort in `WaitForTx`, the docstring 300ms lie, N-serial pre-flight, scheme-normalization placement, multiplier-default-overrides-T6, seed-harness footguns — to the exclusion of subtler temporal-state and composition defects. In particular: **no reviewer modeled what happens when the batch atomicity property (which they all implicitly relied on) is combined with the new pre-flight's failure mode**. The result is a sync that, under one mis-classified LCD response, reverts a 100-finding batch and burns 14M+ gas. None of the trio said this out loud.

Five new findings below. Two Critical, one Warning, two Suggestions. After triage, my verdict is **Escalate** — the trio's already-filed bundle is necessary but not sufficient.

---

## Trio finding triage

| ID         | Trio Severity | My call                  | Rationale (one line)                                                                                                                                                             |
| ---------- | ------------- | ------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| F-ARCH-201 | Critical      | **Concur**               | WaitForTx aborts on non-404; reproduced by reading the `if err != nil { return err }` at `client.go:135-137`.                                                                    |
| F-SEC-201  | Warning       | **Escalate**             | Same defect as F-ARCH-201; severity should rise to Critical given F-NEW-301 (batch atomicity) below — a single blip reverts 100 commits.                                         |
| F-PERF-201 | Warning       | **Escalate**             | Same root cause; perf framing understates the correctness impact when combined with batch atomicity.                                                                             |
| F-ARCH-202 | Warning       | **Concur**               | `ctx.Err()` swallowed via `continue` at `sync.go:111-116`; idempotency invariant breaks on cancel.                                                                               |
| F-ARCH-203 | Warning       | **Concur**               | N serial round-trips confirmed at `sync.go:109-125`.                                                                                                                             |
| F-SEC-204  | Warning       | **Concur**               | Same defect; DoS angle is real.                                                                                                                                                  |
| F-PERF-203 | Warning       | **Concur**               | Same defect; no-op scenario inflated to ~15s at N=100.                                                                                                                           |
| F-ARCH-204 | Warning       | **Concur**               | normalizeRPCScheme placement is wrong; deploy script also emits raw `tcp://` (F-ARCH-211).                                                                                       |
| F-ARCH-205 | Warning       | **Concur**               | `applyDefaults` rewrites 0→2; T6 silently lies for `multiplier=0` contracts and for unreachable-chain init path.                                                                 |
| F-ARCH-206 | Warning       | **Concur**               | Seed harness argv bug; `tribunal-seed --send` mis-seeds and broadcasts plan `--send`.                                                                                            |
| F-SEC-207  | Suggestion    | **Concur, but escalate** | F-SEC-207 captures the prod-guard miss; combined with F-NEW-302 below (Execute returns BroadcastResult even on wait error, masked from operator), severity is closer to Warning. |
| F-SEC-203  | Warning       | **Concur**               | LCD-trust framing; the contract is the authority but local state desync is real.                                                                                                 |
| F-SEC-205  | Suggestion    | **Concur**               | tcp→http cementing plaintext; doc/script change cluster.                                                                                                                         |
| F-SEC-206  | Suggestion    | **Concur**               | Sscanf("%d") silently truncates; switch to strconv.ParseUint.                                                                                                                    |
| F-SEC-208  | Suggestion    | **Concur**               | Path-traversal hardening on txhash shape; cheap, future-proof.                                                                                                                   |
| F-ARCH-207 | Suggestion    | **Concur**               | Seed `context.Background()`; trio-perf F-PERF-205 same.                                                                                                                          |
| F-ARCH-208 | Suggestion    | **Concur**               | `chain.New(cfg)` without validate; defensive.                                                                                                                                    |
| F-ARCH-209 | Suggestion    | **Concur**               | Save not calling validate; pre-existing, worth flagging.                                                                                                                         |
| F-ARCH-210 | Suggestion    | **Concur**               | 300ms timeout docstring lie; covered also by F-SEC-202, F-PERF-202.                                                                                                              |
| F-ARCH-211 | Suggestion    | **Concur**               | Deploy script emits `tcp://` in paste-ready yaml; should mirror node_rest rewrite.                                                                                               |
| F-PERF-204 | Warning       | **Concur**               | Zero observability during wait; cheap fix.                                                                                                                                       |
| F-PERF-206 | Suggestion    | **Concur**               | 1s ticker floor; cheap fix.                                                                                                                                                      |

**Verdict on trio:** every finding lands. Two of them (F-ARCH-201 / F-SEC-201 / F-PERF-201) understate severity given the new findings below.

---

## New findings the trio missed

### F-NEW-301: Batch atomicity + pre-flight false-negative = 100-commit revert from a single LCD blip (Critical, composition_failure)

**Files:** `internal/chain/sync.go:109-125, 186-194`; `contracts/tribunal-reputation/src/execute/commit.rs:30-46`.

**Scenario:** Operator runs `chain sync` against plan `P-X` with 100 findings, 30 of which are already on-chain from a prior partial sync (e.g., last run was Ctrl-C'd mid-flight, hitting F-ARCH-202). Pre-flight at `sync.go:109-125` fires 100 `Finding(ctx, planID, id)` calls. The LCD is healthy for 95 of them, but returns a transient 503 for **5 of the 30 already-committed findings** (typical fronting LB hiccup, see F-SEC-201). Per `sync.go:111-116`, those 5 errors are swallowed with `continue`. The result: `committedOnChain` correctly contains the 25 successful pre-flight hits but **missing the 5 blipped ones**. The build-commits loop at `sync.go:138-140` correctly filters out 25, builds commits for `70 + 5 = 75` findings.

The 75-commit batch goes out. The contract's `commit_finding_batch` at `commit.rs:30-46` validates batch size (75 ≤ 100 ✓), then iterates: `for f in findings { process_finding(deps.branch(), env.clone(), f)?; }`. The `?` operator short-circuits on the first error. When the contract gets to one of the 5 already-committed findings, `process_finding` returns `ContractError::FindingAlreadyCommitted` and **the entire 75-item batch reverts**. Gas burned: ~75 × 140k = ~10.5M gas at minimum (validation + per-item processing up to the failing item). All 70 legitimately-new commits are rejected because of 5 already-committed false-negatives.

**Why the trio missed this:** each lens reviewer noted the pre-flight's per-id error tolerance (F-ARCH-202, F-SEC-204, F-PERF-201, F-PERF-203) but framed it as "per-id failure → re-submit → contract rejects → wasted broadcast." Nobody traced through the **batch atomicity** consequence: the `?`-short-circuit in `commit_finding_batch` means the wasted broadcast is not 1 commit, it's the **entire current batch up to MAX_BATCH_SIZE = 100**. The arch reviewer touched batch atomicity tangentially (F-ARCH-203) but missed that it composes with F-ARCH-201/202 to amplify a single LCD hiccup into a 100-commit settlement failure.

**Plan anchor:** intent.md "Failure modes" says "Pre-flight query during sync errors out → entry treated as not-on-chain, sync proceeds, contract rejects duplicate if any, sync surfaces the contract error. Acceptable degradation." This invariant is wrong: the contract's rejection of even one duplicate kills the whole batch, not just the duplicate. The plan's "acceptable degradation" framing assumes per-item rejection. The contract does not work that way.

**Severity:** Critical. The pre-existing v0.3.1 "duplicate guard saves us" model fails entirely under v0.3.2's batched-with-pre-flight settlement. An operator running a single sync against a 100-finding plan after even one LCD blip can lose the entire batch's settlement effort.

**Suggested defense:** (a) treat pre-flight errors as a HARD stop, not `continue` — abort sync with "pre-flight failed for finding X; retry when LCD is healthy"; OR (b) make the contract's batch loop non-atomic for `FindingAlreadyCommitted` specifically (silently skip duplicates inside the batch, return a per-item result), which requires a contract migration; OR (c) before sending the batch, re-query each id in the assembled commit list to confirm it's not on-chain, with stricter error handling — but that doubles pre-flight cost.

---

### F-NEW-302: `Execute` returns non-nil `BroadcastResult` on `WaitForTx` error; callers discard it and lose the landed txhash (Critical, refinement_mismatch)

**Files:** `internal/chain/client.go:117-120`; `internal/chain/sync.go:188-194, 196-204`; `cmd/tribunal/chain.go:167-171, 439-444`; `cmd/tribunal-seed/main.go:105-109`.

**Scenario:** The contract for `Execute` at `client.go:85-121` returns `(*BroadcastResult, error)` with the following property when the broadcast succeeded but `WaitForTx` failed (lines 117-119):

```go
if err := c.WaitForTx(ctx, res.TxHash); err != nil {
    return &res, fmt.Errorf("wait for inclusion: %w", err)
}
```

`res` is non-nil and contains a real `TxHash` that was accepted by mempool and is very likely to land. **Every single caller in the v0.3.2 diff discards `res` when `err != nil`:**

- `sync.go:188-191`: `br, err := s.Client.Execute(ctx, msg); if err != nil { return nil, fmt.Errorf(...) }` — `br` is the named variable but is never referenced on the error path.
- `sync.go:198-201`: same pattern for resolve.
- `cmd/tribunal/chain.go:167-171` (`chain register`): `res, err := client.Execute(ctx, msg); if err != nil { return err }` — `res.TxHash` is lost.
- `cmd/tribunal/chain.go:439-444` (`chain rotate`): same.
- `cmd/tribunal-seed/main.go:105-108`: `log.Fatalf("execute: %v", err)` — process dies, txhash is lost.

The operator's perceived reality: "register failed." Actual on-chain reality: "register succeeded; txhash 0xABCD lives forever in the chain history; no local record." Re-running `tribunal chain register adversary-X` now FAILS for a different reason — `AgentAlreadyRegistered` — leaving the operator with a confusing "the second-run error contradicts the first-run error." `chain rotate` has the same shape; `tribunal-seed --send` is worse because it deposits a real-world reputation effect.

**Why the trio missed this:** F-ARCH-201 named the issue ("`chain rotate` → same. Rotation may have landed but the operator thinks it didn't") but treated it as a symptom of "WaitForTx aborts too eagerly." The actual defect is more subtle: even when the `WaitForTx` fix lands (per F-ARCH-201's suggested fix), there's still a class of correctness failure where ctx is genuinely cancelled mid-wait and `res` is the only record of what the operator just authorized. Discarding it on error is a refinement mismatch against the `Execute` signature, which deliberately returns `(*BroadcastResult, error)` instead of `(error,)` so callers can recover the txhash.

**Plan anchor:** intent.md Behavior #2 ("`Execute` waits for tx inclusion") edge cases enumerate "ctx cancelled → return wrap of ctx.Err()" but say nothing about whether the caller is responsible for handling the returned `*BroadcastResult`. The plan's reviewer-arch focus mentions "is `WaitForTx` correctly bounded by ctx" but not "do callers correctly handle the (res, err) tuple shape."

**Severity:** Critical for `chain register` and `chain rotate` — the local registry has no awareness of the on-chain agent's existence, but the on-chain state is permanent. The operator's local state desyncs from chain. For `chain sync`, severity is lower because the next sync's pre-flight recovers (modulo F-NEW-301).

**Suggested defense:** On `Execute` error, every caller should log `res.TxHash` (if non-empty) and the error to stderr so the operator can manually verify on-chain. The cleanest fix: change `Execute`'s signature to never return a non-nil `BroadcastResult` with a non-nil error — make wait failures rewind the local "successful broadcast" perception by emitting the txhash into the error message itself. Or expose `WaitForTx` as a separate caller-driven step so `Execute` returns immediately after broadcast and the caller polls explicitly.

---

### F-NEW-303: `SyncAll` aborts on first plan failure; subsequent plans are silently un-synced (Warning, composition_failure)

**File:** `internal/chain/sync.go:236-244`; consumer at `cmd/tribunal/chain.go:228-234`.

**Scenario:** Operator runs `tribunal chain sync` (no `--plan` flag) against a ledger with plans A, B, C, D, E. Plan A is small and healthy. Plan B has a poisoned finding (e.g., agent retired on-chain between commit and re-sync, or hits F-NEW-301's batch revert). Plan C, D, E are healthy.

`SyncAll` iterates `planOrder` and calls `SyncPlan` per plan. At `sync.go:240-241`:

```go
if err != nil {
    return out, err
}
```

The function returns immediately, including A's successful result but **never calling SyncPlan for C, D, E**. The caller at `cmd/tribunal/chain.go:228-234`:

```go
results, err := sync.SyncAll(ctx, lg)
if err != nil {
    return err
}
for _, r := range results {
    printSyncResult(r)
}
return nil
```

**Returns `err` and skips the `for _, r := range results` loop entirely**. The operator sees only the error from plan B and has no idea that:

1. Plan A actually settled (its result is in the discarded `results` slice).
2. Plans C, D, E were never attempted.

The operator's recovery requires manually identifying which plans need re-sync, which the CLI provides no tooling for. The settlement of healthy plans gets blocked indefinitely by a single bad plan.

**Why the trio missed this:** the trio focused on per-plan correctness (F-ARCH-203, F-SEC-204) and the per-Execute wait loop (F-ARCH-201). The "what happens to SyncAll's contract when one plan fails" composition was not exercised. The plan.md scenarios and intent.md edge cases are all single-plan.

**Plan anchor:** plan.md task T4 describes "pre-flight chain-state filter" per-plan but says nothing about the orchestration boundary. intent.md non-goal #2 ("we are NOT auditing the dispatch / verify / ledger packages") could be read to exclude SyncAll, but SyncAll lives in `internal/chain/sync.go` which is in scope per the diff.

**Severity:** Warning. Pre-existing behavior, not introduced by v0.3.2 — but v0.3.2's pre-flight makes the first-failure case more common (F-NEW-301), so the latent SyncAll bug now bites under realistic LCD-flake conditions.

**Suggested defense:** In `SyncAll`, change the error handling to either (a) collect per-plan errors into a `[]error` and return both `out` and a multi-error so the caller can print every successful plan + every failed plan, OR (b) print progress per plan to stderr as it goes so the operator sees real-time partial progress. Update `cmd/tribunal/chain.go:228-234` to print partial results even on error.

---

### F-NEW-304: Resolutions are not deduplicated in `SyncPlan`; duplicate ledger entries cause `FindingAlreadyResolved` batch revert (Warning, edge_case)

**File:** `internal/chain/sync.go:167-184`; contract path `contracts/tribunal-reputation/src/execute/resolve.rs:90-95`.

**Scenario:** The local ledger contains two resolution entries for the same (plan_id, finding_id). This happens if a PM agent runs `tribunal pm resolve` twice (e.g., revised the outcome), or if a script appends to ledger.jsonl without dedup. The ledger is append-only.

Note the asymmetry in `SyncPlan`:

- **Commits** are deduplicated via `seen := map[string]struct{}{}` at `sync.go:129-137`.
- **Resolutions** are NOT deduplicated. The loop at `sync.go:167-184` reads every resolution for the plan, filters only by `resolvedOnChain[r.FindingID]`, and builds `ResolutionCommit` for each.

If both duplicates fall through pre-flight (`resolvedOnChain[id]=false` because the chain hasn't seen either yet), both `ResolutionCommit` entries land in the batch. The contract's `resolve_finding_batch` iterates and calls `process_resolution`; the first one sets `state.resolution = Some(...)`; the second hits `if state.resolution.is_some() { return Err(FindingAlreadyResolved) }`. Atomic batch reverts. All resolutions in the batch fail because of one duplicate.

**Why the trio missed this:** the trio noticed the pre-flight + filter pattern and assumed symmetry between commits and resolutions. They did not read the loops carefully enough to spot that `seen` only exists for commits.

**Plan anchor:** plan.md acceptance criteria invariant "Each task lands without regressing the existing test suite" — the existing test suite doesn't exercise duplicate ledger entries. intent.md Concrete Scenario #2 ("partial-failure retry") implicitly assumes one resolution per finding.

**Severity:** Warning. Requires duplicate ledger writes, which is unusual but not impossible (multi-process writes, append-without-check, script bugs). Combined with F-NEW-301's batch atomicity, one duplicate kills the entire resolve batch.

**Suggested defense:** Apply the same `seen` deduplication to the resolutions loop:

```go
resSeen := map[string]struct{}{}
for _, r := range resolutions {
    if r.PlanID != planID { continue }
    if _, dup := resSeen[r.FindingID]; dup { continue }
    resSeen[r.FindingID] = struct{}{}
    if resolvedOnChain[r.FindingID] { continue }
    // ... build
}
```

Optionally: prefer LAST-WINS or FIRST-WINS semantics and document.

---

### F-NEW-305: Poison-pill finding causes perpetual batch failure with no operator-facing diagnostic (Warning, edge_case)

**File:** `internal/chain/sync.go:186-194`; contract path `contracts/tribunal-reputation/src/execute/commit.rs:33-42`.

**Scenario:** The local ledger contains a finding with a SUBTLE defect that pre-flight cannot detect:

- `agent_pubkey` references an agent that was retired on-chain between the local sign and the sync attempt (contract: `ContractError::AgentRetired`).
- `stake` exceeds the agent's on-chain balance (contract: `ContractError::InsufficientStake`).
- Signature was produced against a stale canonical-message version (contract: `ContractError::InvalidSignature` — unlikely with the v0.3.1 canonical fix but possible if the agent's keyring rotated).

None of these are surfaced by the pre-flight `Finding` query (which returns `nil` for a not-yet-committed finding regardless of why). The pre-flight thinks the finding is fine to submit. The contract rejects it. Since `commit_finding_batch` uses `?`, the entire batch reverts.

**The operator's recovery loop:** re-run `chain sync` → same batch built → same poison pill → same revert. Sync NEVER settles the plan. There is no skip-the-bad-one CLI affordance and no way to mark a ledger entry as "do not re-submit."

**Why the trio missed this:** the trio's batch-atomicity awareness was limited (F-ARCH-203 mentions MAX_BATCH_SIZE but not atomicity); the "perpetual failure on a single bad entry" failure mode was not modeled. The trio also didn't probe what kinds of legitimate ledger entries the pre-flight cannot pre-validate.

**Plan anchor:** intent.md does not specify recovery semantics for "ledger entry that pre-flight passes but contract rejects." The contract's documented error set in `error.rs` includes many such cases.

**Severity:** Warning. Requires a specific operator workflow (agent retirement, balance underflow, key rotation) but each is realistic. The defect is not introduced by v0.3.2 — it's amplified by the pre-flight + batch architecture that v0.3.2 doubles down on.

**Suggested defense:** Provide a CLI affordance to mark a ledger entry as "do not re-submit" without deleting it (preserves audit trail). Or, more invasively, change the contract's batch loops to collect per-item errors and surface a partial-success result so the bad item can be identified and skipped. Or add a `chain sync --skip-finding F-X` flag that filters the local build before sending.

---

## Cross-corpus blind spots

Three patterns showed up as shared blind spots across all three reviewers:

### CB-1: "The contract will reject duplicates" used as a recovery argument

All three reviewers wrote some variant of "the contract's idempotency guard catches this." That's true for **per-call** semantics — submitting one duplicate gets rejected. It is **NOT true for batched calls** because the batch atomicity property turns one duplicate into a 100-commit revert (F-NEW-301). The reviewers' shared mental model of "contract is the authority, will save us" was a single-message model; the contract operates with batch atomicity that breaks the recovery narrative.

This is a training-corpus pattern: REST APIs that reject duplicates almost always reject per-request. The Cosmos-SDK / CosmWasm batched-tx model is different. The reviewers wrote in the REST/HTTP idiom they were trained on.

### CB-2: `(result, error)` Go convention assumed mutually exclusive

All three reviewers assumed that `err != nil` means "no useful result." That's the canonical Go idiom — but `Execute` deliberately violates it (F-NEW-302). When the broadcast succeeded and only the wait failed, the txhash is real and recoverable. None of the three reviewers flagged any of the four call sites that discard the txhash.

Same training-corpus root: Go convention is `(zero, err)` on error. Reviewers expect that convention. They don't read the implementation closely enough to spot deliberate deviations.

### CB-3: Single-plan reasoning for a multi-plan system

All three reviewers exercised `SyncPlan` in isolation. None exercised `SyncAll`. The intent and plan documents both define behavior per-plan, which encouraged this. But the operator-facing command is `tribunal chain sync` (no plan flag), which invokes `SyncAll`. The composition-failure (F-NEW-303) was invisible to all three because the unit of attention was the single plan.

Same training-corpus root: the lens reviewers focus on the function under review and rarely climb up to the caller's caller. The PM-anchored intent doc didn't help by also being single-plan-focused.

---

## Things I attacked and did NOT find

I attempted attacks in these areas and either confirmed the trio handled them or confirmed they're not in scope / not exploitable:

1. **UTF-8 / multi-byte identifier injection:** `validateIDField` on both sides uses Unicode-aware `IsControl`/`is_control`. Byte-length checks (`len`/`value.len()`) are consistent. No mismatch.
2. **`tribunal-seed --send` with mainnet config:** trio-sec F-SEC-207 fully covered. The plan-id `--send` quirk is also covered by trio-arch F-ARCH-206. The combination is the worst case (sign + broadcast garbage TP against mainnet) but already filed.
3. **Bash injection via `--reward-multiplier "x; rm -rf"`:** the value lands in JSON-quoted form for the instantiate (line 131) and unquoted in the yaml-tail (line 178). The yaml-tail is a stdout print, not a file write, so injection is bounded to the operator's pasting it. Acceptable for a developer tool.
4. **F-PERF-203 parallelism vs DoS:** trio-perf's suggested 8-way pool is the right answer, but I confirmed no security risk in parallelizing — each call is read-only LCD GET.
5. **xiond JSON output forgery:** `BroadcastResult` parsing trusts xiond. The `XiondBinary` env supports `docker exec` prefixes, so a malicious wrapper is conceivable. But this is outside Tribunal's TCB; the operator chose to delegate signing to that binary.
6. **Cross-plan finding_id collision:** the contract's `FINDINGS` Map is keyed `(plan_id, finding_id)`. The pre-flight queries are per-(plan, id). No cross-plan leakage.
7. **`fetchTx` returning `code != 0` from a successful tx:** the contract's `commit_finding` returns `Response::new()` only on success; a non-zero code at the cosmos-tx level (not the contract level) would indicate a cosmos-sdk error. Surfacing this as a wait error is correct.
8. **`outcome_reward_multiplier` Uint128 vs uint64 truncation:** the local Go cfg field is `uint64`. The contract stores `Uint128`. `fmt.Sscanf("%d", &n)` on a >2^64 value would error (no parse). Then cfg field stays 0 and `applyDefaults` rewrites to 2 (F-ARCH-205 trap, but exotic case). Acceptable.
9. **Signal handling for Ctrl-C during WaitForTx:** no signal handler installed in cmd/tribunal. SIGINT kills the process. Acceptable UX for a CLI.
10. **`url.JoinPath` with empty `NodeREST`:** produces relative path, request fails fast. Non-exploitable.

---

## Verdict

**Escalate.**

The trio's bundle (1 Critical + 7 Warnings + ~10 Suggestions) is well-grounded and correctly diagnoses what they looked at. My five new findings (2 Critical, 1 Warning, 0 Suggestions, plus 2 trio-finding severity escalations) push the v0.3.2 release into "do not ship until at least the Criticals are fixed" territory.

**Specifically blocking:**

- **F-NEW-301** (batch atomicity + pre-flight false-negative): a single LCD blip can revert a 100-commit settlement. This is correctness-level, not "operator UX hiccup" level.
- **F-NEW-302** (Execute discards `BroadcastResult` on wait error): `chain register` and `chain rotate` can silently succeed on-chain while reporting failure locally, with no recovery path. Pre-existing, but the F4 wait loop is what makes it bite.
- **F-ARCH-201 / F-SEC-201 / F-PERF-201** (WaitForTx transient abort): trio Critical / Warnings; should be treated as Critical given F-NEW-301's amplification.

**Defer-able:**

- F-NEW-303 (SyncAll abort): pre-existing, not amplified by v0.3.2 specifically. Important for usability but doesn't gate a release.
- F-NEW-304 (resolution dedup): requires a duplicate-ledger workflow, which is unusual.
- F-NEW-305 (poison-pill): requires specific failure modes, but worth tracking as a v0.3.3 architectural ask.

The methodology is doing its job: trio-parallel surfaced the obvious six, adversary surfaced the three less-obvious composition defects, ledger-of-findings reflects both. v0.3.2 is salvageable in one focused v0.3.3 with the Criticals addressed.

---

## FINDINGS-TO-FILE

```
critical|composition|F-NEW-301|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v032-audit/adversary.md#f-new-301|Batch atomicity plus pre-flight false-negative causes single LCD blip to revert 100-commit settlement batch
critical|refinement-mismatch|F-NEW-302|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v032-audit/adversary.md#f-new-302|Execute returns non-nil BroadcastResult on WaitForTx error but every caller discards it losing the on-chain txhash
warning|composition|F-NEW-303|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v032-audit/adversary.md#f-new-303|SyncAll aborts on first plan failure and discards partial results so subsequent plans are silently un-synced
warning|edge-case|F-NEW-304|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v032-audit/adversary.md#f-new-304|SyncPlan deduplicates commits but not resolutions causing FindingAlreadyResolved batch revert on duplicate ledger entries
warning|edge-case|F-NEW-305|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v032-audit/adversary.md#f-new-305|Poison-pill finding causes perpetual batch revert with no operator-facing skip affordance
```
