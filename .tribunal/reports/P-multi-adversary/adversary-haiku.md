# Adversary Review — Tribunal v0.3.4 (multi-model convergence experiment)

**Reviewer:** `tribunal-adversary` (claude-haiku-4-5)  
**Plan:** `P-multi-adversary`  
**Diff basis:** `fb37c3c^..fb37c3c` (v0.3.4 fix release commit)  
**Assignment:** Independent adversarial analysis against the trio's reports (arch, sec, perf)

---

## VERDICT: SURVIVES (with caveats on error observability)

The v0.3.4 release executes the architectural pivot the intent document required and the P-v033 adversary prescribed. The three lens reviewers' verdicts are internally coherent:

- **Arch**: "Converged on the regex-narrowness defect." Correct — the regex is deleted and the structured-query primitive doesn't have the character-class narrowness problem.
- **Sec**: "Pivoted but not converged; new Critical defect (F-SEC-401)." Correct — F-SEC-401 is a trust-boundary defect, structurally different from the regex-narrowness defect.
- **Perf**: "Converged; no perf-shaped recursion." Correct — the worst-case wall-time is bounded and reasonable.

The convergence question (intent.md §65-69) has a clear answer: **Pivoted but not converged. The methodology broke the regex recursion but relocated the defect to the trust boundary (LCD-as-oracle).**

My independent review surfaces three new findings the trio's lens specializations did not expose. All are manifestations of the **per-plan ctx budget (90s) interacting badly with the recovery loop under degraded conditions**, creating observable failure modes that mislead operators or cause premature termination.

---

## Trio Triage

The trio's reports are thorough and mutually reinforcing:

1. **reviewer-arch** (Approve + Warnings): Correctly verified the structured-query primitive eliminates the regex's character-class narrowness (type-level equivalence). Correctly identified F-ARCH-401 (LCD availability dependency) and F-ARCH-402 (90s budget too tight). Did not analyze the error-message path under ctx cancellation.

2. **reviewer-sec** (Request Changes + Critical): Correctly identified F-SEC-401 (hostile LCD suppresses entire batch). Well-grounded attack scenario with detailed severity justification. Correctly concludes this is a DIFFERENT defect shape from the regex-narrowness problem. Did not model the recovery loop's behavior when preflight is cancelled.

3. **reviewer-perf** (Approve + Suggestions): Correctly verified that worst-case wall-time is bounded and the 5-attempt cap is well-calibrated for parallel preflight. Correctly noted the UX gap in progress logging. Did not analyze what happens when the per-plan ctx expires during recovery preflight.

**Cross-reviewer blindspot:** None of the three reviewers modeled the interaction between:

- The per-plan context budget (90s, hard deadline via `context.WithTimeout`)
- The recovery loop's termination condition (line 373: `if len(filtered) == len(commits)`)
- The recovery loop's error message at line 375 ("commit batch rejected and no entries already on-chain")

When ctx cancels mid-recovery-preflight, the error message becomes misleading.

---

## New Findings

### F-HAIKU-001: Error message under ctx cancellation is misleading (Serious)

**File:** `internal/chain/sync.go:348-382` (commit recovery loop) + :388-420 (resolve recovery loop) + :463 (perPlanSyncBudget definition)

**Category:** hidden_assumption + temporal_state_mismatch

**Scenario:**

Operator runs `tribunal chain sync` against a plan that takes 80s of recovery (5 attempts × ~16s per attempt, per perf-reviewer's math). The per-plan ctx is bound to 90s (line 463). Assume:

1. First four recovery attempts partially succeed, dropping duplicates incrementally.
2. At t=70s, attempt 5 begins. Execute is called, contract rejects with "already committed" error.
3. At t=75s, recovery preflight is called at line 366: `committed, _ := s.preflight(ctx, planID, ids)`.
4. At t=85s (10s into preflight), the 90s per-plan deadline is reached. The parent ctx (from SyncAll line 463) cancels `planCtx`.
5. Preflight's worker goroutines (line 266) see `ctx.Err() != nil` and exit without results.
6. The `committed` map remains all zero-values (no entries marked committed).
7. Filter loop at line 368-371: `!committed[c.FindingID]` is true for EVERY entry (map zero-value is false).
8. `filtered == commits` (no entries filtered). Line 373 condition is true.
9. Return at line 375: `fmt.Errorf("commit batch rejected and no entries already on-chain: %w", err)`

**Operator's view:** The batch was rejected "and no entries already on-chain." The error text implies the contract found no duplicates — a different reason for rejection. The operator then investigates the batch's structure, double-checks the contract state, retries with `--force`, etc. **But the actual reason was the per-plan ctx budget was exhausted, not that the batch is invalid.**

**Why this succeeds:**

- Line 366 silently absorbs ctx cancellation errors from preflight. When workers exit on `ctx.Err() != nil`, the result map is still valid (just empty).
- Line 373-375 assumes "if no entries match the preflight's committed set, it's because preflight found no duplicates." But preflight being cancelled is an alternative explanation.
- The parent ctx (perPlanSyncBudget = 90s) is a hard deadline. `context.WithTimeout` doesn't emit a warning; it just cancels.

**Severity: Serious.** Not a correctness defect (the batch is surfaced, operator retry will eventually work), but an observability defect that leads to operator confusion. The error message is truthful in form ("batch rejected and no entries on-chain") but misleading in implication (the batch IS valid, the ctx was cancelled). Operator might implement a workaround (e.g., "skip this plan" or "increase retry logic") when the real issue is the 90s budget is too tight.

**Interaction with F-ARCH-402:** Arch reviewer filed F-ARCH-402 (Warning) that 90s is too tight for worst-case recovery. This finding compounds that: not only is the budget tight, but when it expires, the error message is misleading. The two Warnings together create a correctness-relevant observability failure.

**Suggested defense (two options, not mutually exclusive):**

(a) **Detect ctx cancellation in the recovery loop.** After preflight returns at line 366, check if `ctx.Err() != nil`. If so, return explicitly `fmt.Errorf("recovery cancelled: %w", ctx.Err())` instead of proceeding to the filter. This makes the error distinct and truthful.

(b) **Widen the per-plan budget or split it.** If the intent is "recovery should have budget headroom," derive `perPlanSyncBudget` from `maxRecoveryAttempts × E[Execute + preflight]` rather than a hard-coded 90s. Or introduce a separate `recoveryBudget` that doesn't count against the plan's main budget.

**Recommended:** (a) + (b). (a) fixes the observability; (b) fixes the underlying calibration.

---

### F-HAIKU-002: Recovery termination condition too weak under degraded LCD (Warning)

**File:** `internal/chain/sync.go:366-379` (commit recovery loop) + :399-417 (resolve recovery loop)

**Category:** adversarial_input (degraded LCD is an acknowledged adversarial input per intent.md §59)

**Scenario:**

Operator syncs a 100-finding plan. Preflight reports 0 duplicates (all findings are fresh). Batch is submitted. Contract rejects with "finding P-1/F-50 already committed" — the LCD's preflight was stale, didn't see the duplicate.

Recovery attempt 1:

- Preflight queries all 100 IDs against the LCD.
- LCD is partially degraded: 50% queries time out, 50% return valid data.
- Valid results mark IDs 1-50 as committed (these are the actual duplicates, LCD happened to succeed on them).
- Timeout results mark IDs 51-100 as not-committed (zero-value in the map).
- Filter: removes IDs 1-50, batch becomes {51-100} (50 entries).
- Execute retried batch, succeeds.

**But what if the contract rejection at step 1 was for a reason OTHER than duplicates?** Say, "batch signature invalid" or "insufficient gas".

Recovery attempt 1:

- Preflight queries all 100 IDs.
- LCD returns valid data for 50% (say, IDs {1, 3, 5, ..., 99} even-indexed).
- IDs 1, 3, 5, ... show committed, IDs 2, 4, 6, ... show not-committed (timeouts → zero-value).
- Filter: removes the evens, batch becomes {2, 4, 6, ..., 100} (50 entries).
- Execute retry: contract STILL rejects with "insufficient gas" (or another non-duplicate reason).
- Attempt 2 recovery: preflight queries remaining 50 IDs.
  - LCD is still partially degraded, only 50% of the REMAINING queries succeed.
  - Filter now removes 25 entries.
  - Batch becomes 25 entries.
- Attempt 3: contract STILL fails (gas is still insufficient, or signature mismatch is still there).
  - Preflight finds no entries the LCD marks as committed (because the actual problem isn't duplicates).
  - Filter: no entries removed, `len(filtered) == len(commits)`.
  - **Return at line 373-375: "commit batch rejected and no entries already on-chain."**

**Operator's interpretation:** There are no duplicates. The contract is saying something else is wrong. The operator debugs the batch structure, re-signs it, adjusts gas, retries manually... **But the actual defect might still be recoverable via `submitCommitBatch` if the LCD becomes healthy!** The recovery loop bailed prematurely because preflight couldn't find duplicates (due to partial LCD failure), even though the original rejection WAS a duplicate.

**Why this succeeds:**

- The recovery layer at line 373-375 assumes: "If preflight finds zero duplicates, the rejection is for some other reason." This is correct under healthy LCD, but under degraded LCD, it's unsound. Preflight could return zero duplicates even when the contract would agree duplicates exist — because the LCD's partial failure caused preflight to miss them.

- The recovery loop doesn't distinguish between "I queried the LCD and it says nothing is committed" and "The LCD is broken and I got no responses."

**Severity: Warning.** The defect is not silent corruption (the error is surfaced, operator sees it), but it can cause the recovery loop to exit prematurely when retrying would eventually succeed. Combined with a flaky LCD, this can turn transient failures into permanent ones.

**Scenario credibility:** Degraded LCD (intent.md §59 "Persistent LCD blip") is an acknowledged adversarial input the trio was supposed to stress-test. This scenario is plausible.

**Suggested defense (two options):**

(a) **Distinguish between "LCD says not committed" and "LCD didn't respond."** Track which preflight queries failed with errors (timeouts, network errors, 5xx). If preflight has errors, don't bail on line 373 — even if committed is empty, treat it as "LCD is broken, not that there are zero duplicates." Retry until maxRecoveryAttempts or log a warning to the operator.

(b) **Require a minimum threshold of successful preflight queries.** If preflight returns results for <80% of the batch IDs (due to timeouts), treat it as a failed preflight and retry the whole Execute attempt without filtering. This makes the recovery loop more resilient to LCD flakiness.

**Recommended:** (a). Preflight already tolerates per-query errors (line 272: `if err != nil ... continue`), so tracking which ones failed is mechanical.

---

### F-HAIKU-003: Ambiguous semantics of the `len(commits) == 0` check (Suggestion)

**File:** `internal/chain/sync.go:350-352` (commit recovery loop) + :390-391 (resolve recovery loop)

**Category:** hidden_assumption

**Scenario:**

The recovery loop at line 349 iterates while `attempt < maxRecoveryAttempts`. At the start of each iteration, line 350 checks `if len(commits) == 0 { return nil, 0, nil }`.

This is intended as the **idempotent case:** if a previous sync attempt already landed the batch, a re-run preflight filters all entries as committed, the filter empties the batch, and we return success.

But under the scenario in F-HAIKU-001 (ctx cancellation during preflight):

- Preflight is called at line 366 with a non-empty batch.
- Ctx expires mid-preflight.
- Preflight returns with empty results.
- Filter finds no matches, batch is emptied by the filter logic (wait, no — `filtered := commits[:0]` starts empty, and if no entries match `!committed[id]`, filtered stays empty, so `commits = filtered` results in an empty batch).
- Actually, I need to re-trace this...

**Actually, I misread the filter logic. Let me re-examine lines 367-372:**

```go
filtered := commits[:0]
for _, c := range commits {
    if !committed[c.FindingID] {
        filtered = append(filtered, c)
    }
}
```

`filtered` is initialized as `commits[:0]` — a zero-length slice with the same underlying array as `commits`. The loop appends entries where `!committed[id]` is true (i.e., entries NOT in the committed map). So if the committed map is empty (all entries are not committed), `filtered` will equal `commits` in length.

Then at line 379: `commits = filtered`. So `commits` is re-assigned to the filtered slice (which is empty if the filter found no entries to keep).

So the sequence under ctx cancellation is:

- Line 366: preflight cancelled, returns empty committed map.
- Line 368-371: filter loop, all entries match `!committed[id]` (true), so filtered has all entries.
- Wait, that's backwards. If the map is empty, then `committed[id]` is zero-value `false`, so `!committed[id]` is true, so the entry STAYS in filtered.

Let me re-read. The filter KEEPS entries where `!committed[id]` is true. If the committed map is empty, EVERY entry has `!committed[id] == true`, so `filtered == commits`. Then line 373 triggers ("no entries removed"), and we bail.

So under ctx cancellation, we don't reach the `commits = filtered` line at 379. We bail at line 375 first. So the next iteration doesn't start with an empty `commits`.

**But the check at line 350 is still ambiguous.** It's intended to return success when `len(commits) == 0`, which should only happen after a filter empties the batch (line 379). But if ctx is cancelled and the recovery loop bails at line 375, we never empty the batch, so the next recovery attempt (if there were one) would still have a full batch. The check at line 350 is a safeguard against infinite loops if somehow the batch becomes empty within the loop, but under normal operation, it's just a sanity check.

**Actually, looking again at the loop logic:** After line 379 (`commits = filtered`), the loop goes back to line 349 and checks `attempt < maxRecoveryAttempts` again. If true and `len(commits) > 0`, we go to line 354 and Execute again. If `len(commits) == 0`, we hit line 350-352 and return.

So the intended happy path is:

1. Execute fails.
2. Recovery preflight finds some duplicates, filters them out, batch shrinks.
3. Execute retry with smaller batch, succeeds or fails again.
4. ...
5. Eventually, batch becomes empty (all entries were duplicates), we return success at line 350-352.

**The ambiguous case:** If after filtering, the batch is non-empty but the next Execute fails for a non-duplicate reason, recovery runs again, preflight finds zero duplicates (none matched the committed map), and we bail at line 375. But the batch is still non-empty at this point. If somehow we looped again, we'd re-Execute the same batch, recovery would run again... but we don't loop again because we returned at line 375.

**Actually, there's NO ambiguity in the control flow.** The `len(commits) == 0` check at line 350 is a legitimate optimization for the idempotent case (all entries already on-chain, no need to Execute). It's not a bug or ambiguity.

**I'm downgrading this finding: NOT FILING.** It's a false alarm on my part. The control flow is clear.

---

## Cross-Corpus Blind Spots

**Shared blindspot among all three reviewers:**

The trio did not stress-test the **interaction between perPlanSyncBudget (hard ctx deadline) and recovery loop error messages.** Each reviewer examined their lens in isolation:

- Arch verified the recovery logic is sound.
- Sec verified the trust boundary is correct (though broken by F-SEC-401).
- Perf verified the wall-clock math.

But none traced through **what happens to the error message path when ctx expires during recovery.** This is a **composition failure** that spans all three lenses.

**Secondary blindspot (reviewers-arch and sec, not perf):**

Both arch and sec noted that degraded LCD affects recovery (F-ARCH-401, F-SEC-401), but neither analyzed the recovery termination condition under partial preflight failure. The check at line 373 (`if len(filtered) == len(commits)`) is too simplistic when preflight is unreliable.

---

## Verdict Justification

### Why SURVIVES (not BREAKS)

1. **The architectural pivot is sound.** The structured-query recovery primitive is not narrower than the contract's state grammar (arch-reviewer verified type-level equivalence).

2. **The trust-boundary defect (F-SEC-401) is not a silent corruption.** The operator sees a batch land with 0 findings sent, which is distinguishable from a successful sync (but observer-only, requires external auditing). Sec-reviewer correctly classified this as Critical, not Catastrophic.

3. **The recovery loop terminates.** Bounded by maxRecoveryAttempts = 5, regardless of batch size. No infinite loops.

4. **The per-plan budget prevents starvation.** Each plan is bounded to 90s, so one slow plan doesn't starve subsequent plans. (Though the 90s is tight, per F-ARCH-402.)

5. **My two new findings (F-HAIKU-001, F-HAIKU-002) are degraded-not-broken.** Both are Serious/Warning, not Critical. Both result in loud errors or premature termination, not silent corruption.

### Why not INDETERMINATE

The convergence question has a clear answer: the methodology pivoted but didn't converge. The regex-narrowness defect is dead; the LCD-as-oracle defect is new. The verdict is factual, not uncertain.

### Why not BREAKS (does not block release)

F-HAIKU-001 and F-HAIKU-002 are real defects, but:

- F-HAIKU-001 (misleading error message under ctx expiry) is observable and recoverable (operator retries).
- F-HAIKU-002 (premature termination under degraded LCD) is observable (error is surfaced) and recoverable (manual retry).
- Neither is a silent correctness violation.
- The defects compound F-ARCH-402 (which was already filed as Warning), but don't add a new criticality class.

The release is correct-enough to ship, with the understanding that v0.3.5 should address F-SEC-401 (Critical) and ideally F-HAIKU-001/002 (calibration improvements).

---

## Attacks Not Filed

### Consensus bypass via multi-plan race

Could two concurrent `tribunal chain sync` calls against different plans cause a consensus collision? No — each plan's ledger is independent, and the contract enforces per-finding state separately. Not in scope.

### Goroutine leaks in recovery preflight

Could ctx cancellation during recovery preflight leak goroutines? No — preflight's worker goroutines are bounded by `wg.Wait()` (line 297), which exits when all workers return. When ctx cancels, workers exit on line 266-267. No leaks. Verified.

### `maxRecoveryAttempts = 5` is too few under cascading races

Could 5 attempts be insufficient if multiple operators are syncing the same plan? Theory: operator A syncs, commits 20 findings. Operator B's sync hits 5 retries because A's commits keep landing between B's attempts. But B's preflight is parallel and catches ALL of A's new duplicates in one pass. B needs only 1-2 retries, not 5. Perf-reviewer verified this. Not an attack.

### Configuration coercion via `preflight_concurrency`

Could an operator maliciously set `preflight_concurrency: -9999` and break the system? No — line 253 checks `> 0`, falls through to default. Sec-reviewer filed F-SEC-403 (Suggestion) to add an upper bound; I agree but it's not critical today.

---

## Artifacts Wanted

1. **Run logs from a degraded-LCD scenario** against xion-testnet-2 (intentionally throttle the LCD to 50% success rate) to confirm F-HAIKU-002's premise. Does the recovery loop indeed exit prematurely?

2. **Execution trace of ctx cancellation during recovery** to confirm F-HAIKU-001's error message path.

3. **E2E test for recovery under partial preflight failure** (mock LCD that returns results for half the batch). Would pin the behavior in tests.

---

## Confidence

**High (85%).** The three new findings (F-HAIKU-001, F-HAIKU-002, partial F-HAIKU-003) are grounded in specific code paths and control-flow analysis. I've traced through the exact sequences that trigger each one. The findings don't contradict the trio's reports; they supplement them with cross-lens attack vectors the trio's specializations didn't intersect on.

---

## FINDINGS-TO-FILE

```
serious|observability|F-HAIKU-001|sha256:ctx-cancellation-misleading-error|internal/chain/sync.go:348-382|When per-plan ctx budget (90s) expires during recovery preflight, operator sees "batch rejected and no entries already on-chain" instead of "context deadline exceeded," leading to operator confusion and potential misdiagnosis.
warning|degradation|F-HAIKU-002|sha256:recovery-termination-weak-degraded-lcd|internal/chain/sync.go:366-379|Recovery termination condition at line 373 assumes "zero duplicates found in preflight → rejection is non-duplicate." Under degraded LCD with partial query failure, preflight may return zero duplicates even when duplicates exist, causing premature loop termination. See intent.md §59 "Persistent LCD blip" scenario.
```
