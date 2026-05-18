# Adversary Attack Report — Tribunal v0.3.4 (multi-model panel, sonnet variant)

**Adversary:** `tribunal-adversary` (claude-sonnet-4-6)
**Plan:** `P-multi-adversary` (methodology experiment)
**Scope:** `fb37c3c^..fb37c3c` ("v0.3.4: audit-driven fix release")
**Trio reports:** reviewer-arch (Approve, 2 Warnings), reviewer-sec (Request Changes, 1 Critical), reviewer-perf (Approve)

```
VERDICT: INDETERMINATE (F-SEC-401 is a genuine Critical that the trio correctly identifies, but the convergence question has a more nuanced answer than either the arch or sec reviewers capture — the recursion IS partially broken, but a deeper structural property neither reviewer names enables the remaining defect)
```

---

## Summary

The trio did substantive work. reviewer-sec's F-SEC-401 is a real Critical and I concur fully. reviewer-arch's convergence verdict ("converged") is too generous — it concedes the LCD-availability dependency without accounting for the deeper trust-architecture consequence sec names. reviewer-perf's math is sound.

What the trio collectively missed: three things. First, `submitResolveBatch`'s recovery uses `preflight`'s `resolved` map, but the contract can reject a resolution batch for `FindingNotCommitted` — an error class that is NOT a "duplicate resolution" and is NOT recoverable via the `resolved` map. This creates a silent retry loop that exhausts `maxRecoveryAttempts` and surfaces a misleading error. Second, the batch atomicity model means a single bad entry in a mixed batch (e.g., one already-resolved finding alongside nine legitimate ones) causes the contract to abort the ENTIRE tx at the bad entry, not just skip it — but the recovery layer filters by the `resolved` map, which may correctly identify the already-resolved entry AND leave the contract-rejected ordering problem unaddressed for non-duplicate errors. Third, the "batch fully filtered → `(nil, 0, nil)` → `FindingsSent=0`, no error" return path is the SAME exit path for both F-SEC-401's hostile-LCD suppression AND legitimate idempotent re-sync. The two cases are structurally indistinguishable to the caller. None of the trio named this `SyncResult` ambiguity as a correctness concern; reviewer-sec noted it as an architectural suggestion in cross-reviewer notes but didn't file a finding.

My convergence verdict for the P-v034 audit's central question: **PIVOTED BUT NOT CONVERGED**, agreeing with reviewer-sec. The specific framing: the regex-narrowness recursion is dead (input-domain-vs-truth-domain defect class resolved at the type level). A NEW defect class has emerged: the recovery layer uses structured-query data from an untrusted source to make authorization decisions. This is not the same shape — it's a trust-boundary defect, not an input-domain-narrowness defect. The methodology's recursion on the regex class is broken. A new recursion on the LCD-as-oracle class has started with v0.3.4.

---

## Trio Finding Triage

| ID                 | Trio severity | My call                                | Rationale                                                                                                                                                                                                                                                                                      |
| ------------------ | ------------- | -------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| F-ARCH-401         | Warning       | **Concur, but understated**            | The LCD-availability-in-recovery concern is real, but the arch reviewer frames it as "more LCD-sensitive than v0.3.3" without naming the authorization consequence: a hostile-available LCD can force full-batch suppression. The finding is real; the framing understates the severity class. |
| F-ARCH-402         | Warning       | **Concur**                             | 90s budget vs 220s worst-case is a calibration defect. Math checks out.                                                                                                                                                                                                                        |
| F-ARCH-403         | Suggestion    | **Concur**                             | `PreflightConcurrency` default location is a style gap. Correct at the call site regardless.                                                                                                                                                                                                   |
| F-ARCH-404         | Suggestion    | **Concur**                             | Docstring overclaims; cheap fix.                                                                                                                                                                                                                                                               |
| F-ARCH-405         | Suggestion    | **Concur**                             | Duplication is brittle; sec also names it.                                                                                                                                                                                                                                                     |
| F-SEC-401          | Critical      | **Concur, independently verify below** | Full-batch silent suppression via hostile LCD is correct; the attack chain I trace is the same as sec's. The exit path `(nil, 0, nil)` is the load-bearing hole.                                                                                                                               |
| F-SEC-402          | Warning       | **Concur**                             | Zero-field validation of `FindingResp` is a necessary condition for trivial F-SEC-401 exploitation. Additive to F-SEC-401, not a replacement.                                                                                                                                                  |
| F-SEC-403          | Suggestion    | **Concur**                             | No upper-bound in `validate()` for `PreflightConcurrency`. Cheap fix.                                                                                                                                                                                                                          |
| F-SEC-404          | Suggestion    | **Concur**                             | Duplication is structurally brittle.                                                                                                                                                                                                                                                           |
| F-SEC-205-carryfwd | Warning       | **Concur, compounding with F-SEC-401** | Silent tcp→http rewrite remains; MITM now executes F-SEC-401 without controlling the LCD server. The compounding makes this worse than the standalone v0.3.3 finding.                                                                                                                          |
| F-PERF-401         | Suggestion    | **Concur**                             | Clock reset in WaitForTx is a real observability gap against the 90s budget.                                                                                                                                                                                                                   |
| F-PERF-402         | Suggestion    | **Concur**                             | Missing upper-bound validation.                                                                                                                                                                                                                                                                |
| F-PERF-403         | Suggestion    | **Concur**                             | Progress signal indistinguishable across preflight invocations.                                                                                                                                                                                                                                |

---

## New Findings — What the Trio Missed

### F-SONNET-001 — `submitResolveBatch` recovery uses `resolved` map to filter a batch that the contract rejects for `FindingNotCommitted` [Serious]

**Category:** composition_failure

**File:** `internal/chain/sync.go:388-419` (`submitResolveBatch`), `contracts/tribunal-reputation/src/execute/resolve.rs:84-89` (`FindingNotCommitted`), `contracts/tribunal-reputation/src/error.rs:29`.

**Scenario:** An operator runs `tribunal chain sync` against a plan where the commit batch succeeded in a prior session (findings are on-chain), but the local commit ledger entry was lost or corrupted so the success-path preflight (sync.go:133) saw them as "not committed" (preflight false-negative). `SyncPlan` rebuilds the commit batch, `submitCommitBatch` runs it, the contract correctly rejects with `FindingAlreadyCommitted`, recovery runs and correctly filters those entries. Eventually the commit batch completes (or was already empty after preflight). Now `submitResolveBatch` runs for the same plan.

Separately: suppose finding F-X was added to the resolution batch (`resCommits`) but F-X was never committed to the chain — perhaps because a queue corruption let a `ResolutionCommit` record land in the ledger without the corresponding `Finding`. The contract rejects `resolve_finding_batch` at F-X with `FindingNotCommitted` (resolve.rs:84-89). `submitResolveBatch` enters recovery (sync.go:399): it queries `preflight(ctx, planID, ids)` to get the `resolved` map.

But `FindingNotCommitted` is not the same as `FindingAlreadyResolved`. The `resolved` map from `preflight` is keyed on `resp.Finding.Resolution != nil` (sync.go:276). A finding that doesn't exist on-chain has `resp.Finding == nil` → `committed=false, resolved=false`. F-X: `resolved[F-X] = false`. Therefore the filter at line 407-409 does NOT drop F-X from the retry batch. `len(filtered) == len(resCommits)` is true at line 412. The recovery layer infers "no duplicates explain the rejection" and bails with `"resolve batch rejected and no entries already resolved: %w"`.

This is the correct error surface — but the behavior is only correct if the rejection is for one entry. If the resolution batch has NINE legitimate resolutions and ONE `FindingNotCommitted` entry, the contract rejects the ENTIRE atomic batch at F-X (resolve.rs:30-46, the loop aborts on first error). Recovery runs, preflight correctly shows all nine as not-yet-resolved, the filter drops none (`len(filtered) == 9 == len(resCommits)` — wait, 9 != 10 only if we drop zero). Let me be precise: `len(resCommits) = 10`, `len(filtered) = 10` because none of the ten entries have `resolved=true` in the preflight result (F-X doesn't exist, the other nine are genuinely unresolved). The bail-out fires: `"resolve batch rejected and no entries already resolved"`. The nine legitimate resolutions are abandoned.

**Why it succeeds:** The comment at sync.go:399-400 reads: "Recovery via contract-state query. resolved map tells us which findings already have on-chain resolutions." This is accurate — the map tells us about _resolved_ findings. But the contract can reject a resolve batch for a reason (`FindingNotCommitted`) that is NOT captured in the `resolved` map. The recovery layer's implicit assumption is "any rejection that isn't already-resolved must be fatal for the whole batch" — this assumption is wrong when `FindingNotCommitted` is the actual cause and the remaining entries could succeed without the bad entry.

The intent.md §34 asks: "Are there contract-side error conditions OTHER than 'already committed/resolved' that v0.3.4 doesn't recover from — and if so, does the new recovery layer's 'no entries on-chain → surface the error' path handle those gracefully?" The answer for `FindingNotCommitted` in the resolve path is: the `no entries on-chain` path fires correctly for the trivial case (one bad entry, N=1), but for a mixed batch (1 bad + K good), it surfaces an error without recovering the K good resolutions.

**Contract evidence:** `contracts/tribunal-reputation/src/execute/resolve.rs:84-89`:

```rust
let mut state = FINDINGS.may_load(deps.storage, key)?.ok_or_else(|| {
    ContractError::FindingNotCommitted {
        plan_id: r.plan_id.clone(),
        finding_id: r.finding_id.clone(),
    }
})?;
```

This fires BEFORE the `FindingAlreadyResolved` check at line 90-94, so for any batch containing a finding that was never committed, the contract aborts the entire batch at that entry, and the `resolved` map from `preflight` has no signal to offer the recovery layer.

**Severity: Serious.** Not Critical because the error IS surfaced — the batch fails loudly, the operator can investigate. But K legitimate resolutions are blocked by 1 data-integrity defect in the ledger, and the error message (`"resolve batch rejected and no entries already resolved"`) is misleading — it implies no entries were already resolved, but doesn't signal `FindingNotCommitted` as the actual cause. The operator's investigation path is: look at the error, assume it's an LCD issue, retry, hit the same error. Without correlating against the on-chain committed state for each finding_id in the resolution batch, the operator has no automatic signal about which finding was never committed.

**Suggested defense:** After the recovery `preflight` runs and `len(filtered) == len(resCommits)` (no already-resolved entries explain the rejection), execute a secondary check: for each entry in `resCommits`, check `committed[r.FindingID]` from the same `preflight` result. If `committed[r.FindingID] == false` for any entry, log a specific warning: `"tribunal: WARNING — resolution batch contains finding %s/%s not committed on-chain; remove from ledger and retry"`. This costs no additional LCD round-trip (the `preflight` already returned both `committed` and `resolved` maps), makes the `FindingNotCommitted` cause visible, and guides operator remediation without requiring protocol changes.

---

### F-SONNET-002 — `len(commits)==0` in recovery loop returns `(nil, 0, nil)` — semantically ambiguous, masks partial-batch landing [Serious]

**Category:** temporal_state_mismatch

**File:** `internal/chain/sync.go:349-352` and `:389-391`, `internal/chain/sync.go:201-209` (caller).

**Scenario:** In `submitCommitBatch`, on the SECOND or later attempt of the recovery loop, `commits` has been filtered to the uncommitted subset. Suppose after filtering in attempt 0 we have `commits = [F-3, F-7, F-9]` (the committed ones were dropped). Attempt 1 calls `Execute`. The Execute SUCCEEDS (err == nil), returns at line 356-358 with `(br, 3, nil)`. This is correct and clean.

But there is a second path to `len(commits) == 0`. After filtering in attempt 0, the recovery loop could filter ALL entries if the hostile LCD (via F-SEC-401) marks every entry as committed. Then `commits = []` on the next loop iteration, and line 350-352 fires: `return nil, 0, nil`. This is the silent-suppression path named by F-SEC-401.

The adversarial scenario is not new — it's F-SEC-401 restated. What IS new is the correctness implication under a LEGITIMATE scenario: suppose in attempt 0, the Execute fails, recovery preflight runs, finds entries F-1 and F-2 as committed, filters them out of a 3-entry batch leaving only F-3. Attempt 1 executes with `commits = [F-3]`. F-3 SUCCEEDS. The function returns `(br, 1, nil)`. Fine.

But now suppose in attempt 0, the Execute fails and recovery preflight somehow finds ALL THREE entries as committed (perhaps a delayed LCD state catch-up — all three WERE committed by the prior broadcast, which the LCD had not yet indexed at the time of the initial preflight). The filter at attempt 0 produces `commits = []`. The loop iterates: `len(commits) == 0` → `return nil, 0, nil`.

`SyncPlan` (line 201-209) receives `(nil, 0, nil)`. It sets `result.FindingsSent = 0` and `result.CommitTxHash = ""`. **The prior broadcast that actually committed all three findings is not reflected in the SyncResult.** The operator sees `findings=0` in the output even though three findings landed on-chain. The tx hash is lost.

The docstring at line 350-352 says "Everything in the batch was already on-chain. No tx needed." This is correct for the initial-batch-already-committed case at the START of the loop (attempt 0, before any Execute). It is INCORRECT for the RECOVERY case where a prior Execute MAY have landed — the comment assumes `len(commits)==0` after filtering means the findings were already on-chain from a PREVIOUS sync session, not from the current Execute that just broadcast.

**Why it succeeds:** The loop variable `attempt` is never checked before entering the `len(commits)==0` short-circuit. There's no path through the loop that records the BroadcastResult from a prior-iteration Execute before filtering reduces `commits` to zero. If attempt 0's Execute broadcast successfully but returned an error on WaitForTx, and recovery preflight then confirms all entries committed (because the tx DID land), the next loop iteration discards the tx hash.

**Plan anchor:** intent.md §22-23: "for any sequence of batch retries within `maxRecoveryAttempts`, the loop terminates because each iteration that doesn't succeed either reduces the batch size by ≥1 (recovery via state-query) or fails for a non-duplicate reason (immediate surface)." This invariant is about TERMINATION, not about whether the BroadcastResult from the partially-landing execution is preserved. The invariant is technically satisfied; the observability defect is outside its scope.

**Severity: Serious.** Under non-hostile LCD conditions, the practical probability is low — WaitForTx timeouts that cause the recovery path to fire after a successful broadcast are transient (the tx lands, the next preflight sees it committed, `len(commits)==0`, the function returns with an empty tx hash). The operator re-runs sync, the next preflight filters everything, and the final SyncResult is `findings=0` with no tx hash — indistinguishable from "nothing to do." The tx hash from the successful broadcast is permanently lost from the SyncResult. This is bad for auditability (the claim "F-1 was committed by tx ABC" is only recoverable by external chain query), and it degrades under the F-SEC-401 scenario to a full censorship vector. Not Critical because the data IS on-chain; the observability loss doesn't change contract state.

**Suggested defense:** Track the last successful `BroadcastResult` across loop iterations. Before entering the `len(commits)==0` short-circuit, if `lastBR != nil` and `attempt > 0`, return `(lastBR, originalLen - len(commits), nil)` instead of `(nil, 0, nil)`. This preserves the tx hash when the prior Execute's broadcast landed but recovery filtered to zero. Two additional lines in the loop:

```go
var lastBR *BroadcastResult
for attempt := 0; attempt < maxRecoveryAttempts; attempt++ {
    if len(commits) == 0 {
        return lastBR, 0, nil // preserve lastBR if a prior broadcast landed
    }
    br, err := s.Client.Execute(ctx, msg)
    if err == nil {
        return br, len(commits), nil
    }
    lastBR = br // may be non-nil even on error (broadcast succeeded, WaitForTx failed)
    // ... recovery ...
}
```

---

### F-SONNET-003 — Recovery loops in both `submitCommitBatch` and `submitResolveBatch` use `commits[:0]` filter-in-place against a slice the SAME loop controls; a future allocation in the recovery path could silently re-expand the slice [Cosmetic, with serious-if-triggered shape]

**Category:** hidden_assumption

**File:** `internal/chain/sync.go:367-371` (commit recovery filter), `:406-410` (resolve recovery filter).

**Scenario:** The filter-in-place idiom:

```go
filtered := commits[:0]
for _, c := range commits {
    if !committed[c.FindingID] {
        filtered = append(filtered, c)
    }
}
commits = filtered
```

`commits[:0]` creates a slice with `len=0` but shares the underlying array with `commits`. The `range commits` iterator captures `commits` by value at the top of the range — this is safe in Go (the range iterator holds a copy of the slice header). The append writes into the shared backing array. This is correct and standard.

The hidden assumption: no other reference holds the `commits` slice's backing array during this loop, and no goroutine concurrently reads the array. Both are true in the current code: `submitCommitBatch` is not concurrent, and `preflight()` was already called synchronously and returned before the filter runs.

But: `preflight()` may have its own retained references into `ids`, which is built from `commits` at line 362-365:

```go
ids := map[string]struct{}{}
for _, c := range commits {
    ids[c.FindingID] = struct{}{}
}
```

This copies the `FindingID` strings (strings are immutable in Go), so `ids` has no reference to the backing array of `commits`. Safe.

The actual concern: F-ARCH-302 from P-v033-audit (slice aliasing) was concurred on by the v0.3.3 adversary but deferred. v0.3.4 does NOT fix it. The reviewer-perf cross-reviewer note (line 512-514 of reviewer-perf.md) flags it as not yet addressed. The trio has now concurred twice that this is a known brittleness and twice deferred it.

Why does this matter as an adversary finding rather than just a second concurrence? Because v0.3.4 DOUBLED the call sites (commit recovery at :367 AND resolve recovery at :406), so the blast radius of a future bug triggered by a refactor that introduces a second reference to the backing array has doubled. And a concrete trigger exists: the recovery path at sync.go:366-366 calls `preflight()` which builds a goroutine pool and a `resCh` channel. If a future version of `preflight()` captures a reference to the `ids` map's values (which are zero-value structs, so no aliasing today) or if a caller passes the `commits` slice directly into `preflight()` instead of building `ids` separately, the aliasing would become observable.

**Evidence:** The filter-in-place pattern now appears in TWO places in `submitCommitBatch`/`submitResolveBatch` (introduced by v0.3.4's structured-query recovery), both inside retry loops. Prior to v0.3.4, this pattern existed only in the same functions but was simpler (one-entry-at-a-time drop). The expansion doubles the maintenance surface for the known brittleness.

**Severity: Cosmetic.** The current code is correct. The brittleness is noted twice in the audit trail. Filing because (a) it's now in two loop-internal call sites instead of one, (b) neither reviewer filed a v0.3.4-specific finding on it — they either flagged it as out-of-their-lens or cross-reviewer-noted it without a finding number. The adversary role requires registering that this is a DEFERRED but still-open finding with doubled surface area.

**Suggested defense:** Replace the in-place filter with an explicit new slice allocation:

```go
filtered := make([]FindingCommit, 0, len(commits))
for _, c := range commits {
    if !committed[c.FindingID] {
        filtered = append(filtered, c)
    }
}
commits = filtered
```

Three lines instead of two; no backing-array aliasing; safe against any future refactor.

---

## Cross-Corpus Blind Spots — What All Three Reviewers Missed

### 1. The `FindingNotCommitted` failure mode for `submitResolveBatch` [F-SONNET-001 above]

None of the three reviewers traced through what happens when the resolve batch contains a finding that was never committed. The arch reviewer analyzed `submitCommitBatch` and `submitResolveBatch` as symmetric structures; the sec reviewer focused on the hostile-LCD injection vector; the perf reviewer focused on throughput. The contract's `FindingNotCommitted` error on the resolve path (resolve.rs:84-89) is a distinct code path that the `resolved` map from `preflight` cannot signal.

### 2. The BroadcastResult drop when recovery filters to empty [F-SONNET-002 above]

The `(nil, 0, nil)` return at sync.go:350-352 is used for TWO distinct scenarios: (a) the initial batch was already fully on-chain before this call, and (b) the recovery path confirmed all entries committed after a potentially-successful broadcast. No reviewer traced the observability consequence of scenario (b) — the tx hash from a successful broadcast being lost. The arch reviewer's analysis of T1 (sync.go:348-382) confirmed the code is correct for the early-filter path; the sec reviewer's F-SEC-401 traces the attack through step 8 ("return nil, 0, nil") without noting that the SAME exit path is also reached legitimately when a broadcast-then-WaitForTx-timeout lands successfully and recovery discovers it.

### 3. The intent.md §59 "Persistent LCD blip" scenario is incompletely handled on the RESOLVE path

Intent.md §59: "Persistent LCD blip. LCD returns 503 to every preflight query during recovery. The recovery layer can't distinguish 'no entries on-chain' from 'LCD broken'." The arch reviewer (F-ARCH-401) and the sec reviewer (F-SEC-401 scenario) both analyze the COMMIT path. Neither traces through what happens on the RESOLVE path.

On the resolve path, an LCD blip during recovery makes `preflight()` return `resolved = {}` (empty — all queries failed, result default is false). The filter drops nothing. `len(filtered) == len(resCommits)`. The bail-out fires: `"resolve batch rejected and no entries already resolved: %w"`. This is the correct behavior. BUT: the error message buries the actual cause (503s from the LCD) inside the wrapped error chain. The operator sees "resolve batch rejected and no entries already resolved" and must dig into the error chain to find the 503. Under the COMMIT path, arch's F-ARCH-401 suggests per-finding query failure logging; neither reviewer extended this suggestion to the RESOLVE path's recovery bail-out message.

### 4. No test for the structured-query recovery path

The diff removes `TestMatchDuplicate_CommitErrorParsing` (correct — the regex is gone) and adds `TestLooksLikeTestChain_TokenAware`. No new test covers the recovery path that v0.3.4 introduced. There is no test that:

- Sets up a fake LCD that marks N findings as committed after a fake Execute failure
- Asserts that `submitCommitBatch` filters and retries correctly
- Asserts that `submitCommitBatch` surfaces "exhausted recovery attempts" after 5 failed attempts

Reviewer-perf (cross-reviewer note, line 528) flags this explicitly: "No test coverage for `submitCommitBatch` / `submitResolveBatch` recovery path." reviewer-arch doesn't file a finding. Neither files a numbered finding. The adversary notes this as a genuine gap: the v0.3.4 recovery path, which is the entire point of this release, has no unit test coverage. The `fakeXiondServer` / `fakeLCDServer` infrastructure in `sync_test.go` exists (line 67-70 of the test file) and could exercise the recovery path with a fake LCD handler; no test exercises it.

This is not a separate finding from the arch-observed gap, but it is worth naming because the test infrastructure to address it already exists in the test file.

---

## Convergence Verdict

The intent.md §65-69 asks for one of three outcomes. My verdict:

**PIVOTED BUT NOT CONVERGED** — same as reviewer-sec, but for a richer reason.

The regex-narrowness recursion (v0.3.3's defect class: input-domain-vs-truth-domain on identifier parsing) is broken. v0.3.4 cannot have that defect shape because there is no parsing. The adversary who prescribed the pivot was right; the pivot worked for that defect class.

However: the LCD-as-oracle trust defect that F-SEC-401 names is NOT the same recursion. It is a NEW defect class — trust-boundary confusion between the LCD's assertion of state and the contract's actual state. The v0.3.4 recovery layer trusts the LCD's `FindingResp` as authoritative for whether a finding is committed, without verifying the LCD's response against any separate source of truth.

The meta-observation: the adversary's recommendation in P-v033 was "replace regex-on-LCD-error-text with structured-query-on-LCD-state." Both the old and new approaches trust the LCD. The adversary's prescription was correct for the PARSING defect class but did not address the TRUST-SOURCE defect class. v0.3.4 faithfully implemented the prescribed pivot and in doing so reduced the parsing attack surface to zero while expanding the data-integrity attack surface (from single-entry influence to full-batch suppression).

Whether this constitutes "still iterating" vs "pivoted but not converged" depends on whether the LCD-as-oracle defect was present and exploitable in v0.3.3. It was (F-SEC-301, filed at Warning, was exactly the LCD-injection-into-recovery defect). v0.3.4 refactored the injection surface, not the trust relationship. So this is NOT a NEW defect class introduced by the pivot — it is a RESHAPED defect class that was present in v0.3.3 (F-SEC-301 Warning) and is now WORSE in v0.3.4 (F-SEC-401 Critical). The recursion on the parsing class is broken. The recursion on the trust-source class is ACCELERATING — each fix to the parsing layer expands the trust-source exposure because the primitive now queries the LCD for more data (complete committed-set vs single regex-extracted ID).

**Implication for v0.3.5:** the fix must address the trust-source recursion, not the parsing layer. sec's defense options (a)+(d) are minimum-viable; defense (b) — cross-check against the local `commits[]` slice's `claim_hash` — is the right structural fix. Defense (c) (ABCI Merkle proof) is the architectural closure.

---

## META

### Categories attacked:

- `composition_failure` — F-SONNET-001 (resolve recovery mismatches `FindingNotCommitted` error class)
- `temporal_state_mismatch` — F-SONNET-002 (BroadcastResult drop on recovery-filter-to-zero)
- `hidden_assumption` — F-SONNET-003 (slice aliasing doubled, deferred by prior audits)
- `shared_blind_spot` — Sections under Cross-Corpus: FindingNotCommitted on resolve path; BroadcastResult observability; test gap for the entire recovery path

### Categories where I concurred with the trio and did not add:

- `trust-boundary` — F-SEC-401 is correct and complete; I verified the attack chain independently and concur without modification
- `adversarial_input` — reviewer-sec's 29-case chain-ID walkthrough is thorough; I found no gaps in the looksLikeTestChain heuristic
- `refinement_mismatch` — all plan tasks T1-T12 are implemented correctly per plan and intent

### Categories I did not attack because I found nothing:

- `contradiction` — the diff, plan, intent, and tests are internally consistent; no self-contradiction found
- `edge_case` on `preflight()` concurrency — the `workers = min(len(ids), configuredWorkers)` cap is correct; the zero-ids early return is correct; goroutine lifecycle is clean (wg.Wait before close(resCh), close(done) after)

### Artifacts I would have wanted but didn't have:

- The actual LCD response schema from the Burnt XION testnet to verify `FindingResp` wire format (particularly whether an empty finding truly responds `{"data":{"finding":null}}` vs `{"data":null}` — both are handled by the `resp.Finding == nil` check, but confirming field ordering matters for the F-SEC-402 minimal-forgery claim)
- Prior P-v034-audit adversary report (intentionally excluded by experiment design; I note this so the synthesis can verify whether my findings duplicate that report's content)

### Confidence in verdict: **medium-high**

The Critical (F-SEC-401 concurrence) is high-confidence — I independently walked the code path and the attack chain holds. F-SONNET-001 is medium-high confidence — it requires the `FindingNotCommitted` error to trigger during a batch resolve, which requires a data-integrity defect in the ledger (missing commit record), which is a non-routine condition. F-SONNET-002 is medium confidence — the scenario requires a broadcast-succeeds-but-WaitForTx-fails case followed by a recovery preflight that sees all entries committed, which is plausible but requires specific timing. The convergence verdict is high-confidence.

---

## FINDINGS-TO-FILE

```
serious|composition_failure|F-SONNET-001|sha256:resolve-recovery-findingnotcommitted|internal/chain/sync.go:388-419|submitResolveBatch recovery uses the resolved map to detect "already resolved" duplicates but cannot detect FindingNotCommitted rejections; a ledger entry for a finding never committed on-chain causes the entire resolution batch to fail with a misleading error even when other entries in the batch are legitimate.
serious|temporal_state_mismatch|F-SONNET-002|sha256:broadcast-result-drop-recovery-filter|internal/chain/sync.go:349-352|The len(commits)==0 short-circuit in the recovery loop returns (nil, 0, nil) even after a prior loop iteration's Execute may have broadcast successfully; the tx hash from that broadcast is lost, producing FindingsSent=0 with no CommitTxHash in the SyncResult for findings that did land on-chain.
cosmetic|hidden_assumption|F-SONNET-003|sha256:slice-alias-doubled-surface|internal/chain/sync.go:367-371|The commits[:0] filter-in-place pattern flagged as brittle in F-ARCH-302 (P-v033) now appears at two loop-internal call sites in v0.3.4 (commit recovery and resolve recovery); the deferred brittleness has doubled its maintenance surface without resolution.
```
