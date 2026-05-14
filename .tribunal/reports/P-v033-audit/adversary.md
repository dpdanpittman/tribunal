# Adversary Attack Report — Tribunal v0.3.3 Fix Release

**Adversary:** `tribunal-adversary` (single-model, default panel)
**Plan:** `P-v033-audit`
**Targets:** the three trio reports at `.tribunal/reports/P-v033-audit/reviewer-{arch,sec,perf}.md`
**Diff basis:** `5cc1634^..5cc1634` (`5cc1634`, "v0.3.3: audit-driven fix release (P-v032-audit findings)")
**Verdict:** **Escalate** — the trio's bundle is necessary but not sufficient. Three new findings, plus a meta-observation about the methodology's convergence behavior that none of the lens reviewers can see from their vantage.

---

## Summary

The trio did its job at higher fidelity than on P-v032-audit. reviewer-arch surfaced the v0.3.3-equivalent of v0.3.2's F-NEW-301 (F-ARCH-301: recovery regex doesn't model the contract's full identifier charset) and called it out as Critical with the exact same framing my prior dogfood used. reviewer-sec independently surfaced the LCD-injection vector (F-SEC-301) and the TrimRight character corruption (F-SEC-302), both of which compose tightly with arch's regex finding. reviewer-perf approved cleanly and is right to do so — no perf regressions; the three suggestions are operator-UX polish.

What the trio shares as a corpus blind spot in this release:

1. **Shared ctx across SyncAll's per-plan iterations.** T7 (errors.Join aggregation) lets SyncAll continue past per-plan failures, but every plan in a single invocation shares the same 5-minute outer ctx from `cmd/tribunal/chain.go:201`. arch noted the recovery-loop worst-case wall-time approaches the ctx budget (line 380-381) but framed it as a single-plan concern. With T7 letting one operator invocation settle N plans, **a slow plan A's recovery cycle now starves plans B–E of ctx budget**. The trio celebrated T7's "doesn't abort on first failure" property without tracing through "doesn't have time to attempt subsequent plans." F-NEW-401 below.

2. **Recovery regex doesn't recognize `BatchMixedPlanID`.** The trio focused on the two error patterns the regex was designed for (`already committed`, `already resolved`). The contract's `commit_finding_batch` (`commit.rs:31-40`) emits a third atomic-batch-killing error: `BatchMixedPlanID { batch_plan_id, found_plan_id, finding_id }` if any `FindingCommit.plan_id` doesn't match the wrapping batch's `plan_id`. The recovery regex has no case for it. A queue-file corruption, hand-edit, or future refactor that lets a finding's per-entry plan_id drift from the batch plan_id will tip the whole batch into a non-recoverable failure. F-NEW-402 below.

3. **The methodology is not converging on a fixed point.** This is the meta-finding only an adversary positioned to compare across audit cycles can see. v0.3.2's audit found F-NEW-301 (recovery layer absent → batch atomicity revert). v0.3.3 fixed it with a regex-based recovery layer. v0.3.3's audit found F-ARCH-301 (recovery regex's char-class is narrower than the contract permits → recovery layer dead-ends). v0.3.4 will fix the regex. v0.3.4 is highly likely to introduce yet-another defect of the same structural shape: **the wrong primitive (string-parse over LCD output) is being patched, not replaced.** Each iteration buys correctness against the previous specific defect but doesn't shrink the surface where the next defect can land. F-NEW-403 below — filed as architectural recommendation, not a strict finding.

The remaining attack surface I probed (account-sequence between recovery retries, finding_id regex meta-character injection, cross-plan finding_id collisions, queue-vs-ledger ordering at sync, `transient_streak` reset on alternating-error LCD, SyncPlan's discard of `br` on submitCommitBatch error) either lands inside trio-filed findings or is non-exploitable. Three areas where the trio's coverage is actually airtight: goroutine lifecycle in the new preflight pool (perf walked it carefully), signature integrity in retried batches (sec confirmed per-entry signatures don't depend on batch composition), and the `outcome_reward_multiplier=0` round-trip (arch flagged the test gap; the actual behavior is correct).

After triage: trio's findings stand. Three new findings, one Warning + one Warning + one Architectural-recommendation. Verdict: **Escalate** — block v0.3.4 design on F-NEW-401 and F-NEW-403's framing.

---

## Trio finding triage

| ID                 | Trio severity | My call                                   | Rationale (one line)                                                                                                                                                                |
| ------------------ | ------------- | ----------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| F-ARCH-301         | Critical      | **Concur**                                | Reproduced via inline regex test against `validate.rs` identifier rules; `/` in plan_id and space in finding_id both fail recovery the exact way arch describes.                    |
| F-ARCH-302         | Warning       | **Concur**                                | Slice-aliasing filter is correct today, brittle tomorrow. Standard Go aliasing-correctness footgun.                                                                                 |
| F-ARCH-303         | Warning       | **Concur**                                | T7's CLI wiring drops the `results` slice on error. Trio is right; the fix is the four-line reordering arch suggests.                                                               |
| F-ARCH-304         | Suggestion    | **Concur**                                | `internal/` mooting public-surface concern is correct. Naming consistency only.                                                                                                     |
| F-ARCH-305         | Suggestion    | **Concur**                                | Test-debt finding. No round-trip regression test for the 0-multiplier case.                                                                                                         |
| F-ARCH-306         | Warning       | **Concur**                                | Test gap is the proximate cause of F-ARCH-301 landing un-caught. Trio's right to file both.                                                                                         |
| F-ARCH-307         | Warning       | **Concur, escalate to Critical-adjacent** | Gas-amplification via hostile LCD + len(batch)-bound recovery is a real attack. The recovery loop's `originalLen+1` cap is structural, not economic. Sec angle below.               |
| F-SEC-301          | Warning       | **Concur**                                | LCD-tainted recovery composition is the headline integrity finding for v0.3.3. The contract still authoritative for state; operator local view + gas spend are attacker-influenced. |
| F-SEC-302          | Warning       | **Concur**                                | TrimRight corrupting plausible IDs is real. One-line delete fix; trio's right that this is silent-failure shape.                                                                    |
| F-SEC-303          | Suggestion    | **Concur**                                | `looksLikeTestChain` substring-containment is a known shape of footgun. The chain id `xion-attestation-1` example is plausible.                                                     |
| F-SEC-304          | Suggestion    | **Concur**                                | ctx-cancel partial-result invariant is correctly handled but untested. Test debt.                                                                                                   |
| F-SEC-305          | Suggestion    | **Concur**                                | Broadcast-time error path is the same shape as wait-time; cross-source verify defense applies to both.                                                                              |
| F-SEC-205-carryfwd | Warning       | **Concur**                                | Expanded silent-rewrite surface in LoadConfig is a real escalation; the warning is appropriate.                                                                                     |
| F-SEC-206-carryfwd | Suggestion    | **Concur**                                | Sscanf truncation pre-dates v0.3.3; not amplified by this release.                                                                                                                  |
| F-SEC-208-carryfwd | Suggestion    | **Concur**                                | One-line hardening; cheap.                                                                                                                                                          |
| F-PERF-301         | Suggestion    | **Concur**                                | 8-worker cap is right default; operator knob is the missing affordance.                                                                                                             |
| F-PERF-302         | Suggestion    | **Concur**                                | Progress note missing in-flight count is a real operator-UX gap.                                                                                                                    |
| F-PERF-303         | Suggestion    | **Concur**                                | Cumulative-elapsed signal across recovery retries; small fix.                                                                                                                       |

**Verdict on trio:** every finding lands. F-ARCH-307's gas-amplification angle composes with F-SEC-301's LCD-injection angle into a more dangerous shape than either alone — the perf reviewer is right to defer to sec, and sec's defense (cross-source via contract query) closes both. No trio finding is mis-graded; one (F-ARCH-307) could plausibly escalate to Critical depending on the threat model, but Warning is defensible given the LCD is operator-controlled in practice.

---

## New findings the trio missed

### F-NEW-401: SyncAll's per-plan iterations share a single 5-minute ctx; one slow plan's recovery cycle starves all subsequent plans (Warning, composition_failure)

**Files:** `cmd/tribunal/chain.go:201`, `internal/chain/sync.go:425-443`, `internal/chain/sync.go:316-381` (recovery loop), `internal/chain/sync.go:192-212` (SyncPlan caller).

**Scenario:** Operator runs `tribunal chain sync` (no `--plan` flag) against a ledger with five plans A, B, C, D, E. `cmd/tribunal/chain.go:201` creates the ctx with `5*time.Minute`. `SyncAll` iterates the plans, calling `SyncPlan(ctx, ...)` on each. **The ctx is shared across all five iterations.**

Plan A has 100 findings, three of which the LCD is slow-rolling on, plus one real duplicate from a prior partial sync. The recovery loop in `submitCommitBatch` enters its second iteration. Each retry includes `Execute → WaitForTx`, and `WaitForTx`'s outer loop is bounded only by the shared ctx — there's no per-Execute timeout. The recovery loop's worst-case wall-time is `len(commits) × (broadcast + wait)`. With a 5s block time and 1s poll cadence, a single recovery cycle eating two minutes of wait is realistic.

If plan A burns 4:55 of the 5min ctx, `SyncAll` proceeds to plan B with 5 seconds left. Plan B's pre-flight (3s per query, 8 workers, ~38s worst case for 100 findings) cannot complete. `preflight` returns partial results; the post-preflight `ctx.Err()` check at `sync.go:125-127` returns `pre-flight cancelled: context deadline exceeded`. Plans C, D, E hit the same wall.

**Result:** the T7 fix that was supposed to let one bad plan no longer block subsequent plans is satisfied at the data-path layer but defeated at the timing layer. From the operator's POV, "plan A took too long, plans B–E silently skipped" looks identical to v0.3.2's "plan A failed, plans B–E aborted."

**Why the trio missed this:**

- **reviewer-arch** noted the recovery loop's worst-case wall-time saturates the 5min ctx (lines 380-381 of reviewer-arch.md, under "Cross-Reviewer Ready Notes for reviewer-perf") and tagged it as an arch concern about whether the outer ctx should be sized for `2 * MAX_BATCH_SIZE * E[wait]`. But the framing was single-plan: "the recovery loop saturates the budget." Nobody traced through to **N-plan saturation** under T7.
- **reviewer-sec** flagged F-SEC-301's gas-amplification as a per-batch concern. Same single-plan framing.
- **reviewer-perf** validated the cost calculus per plan but never multi-plan: the perf cost calculus table (line 103-108) caps at N=100, not "5 plans of N=100 each."

The shared blind spot: T7's composition of "continue past failure" + shared ctx + len(batch)-bounded recovery is a three-component interaction that no lens reviewer's checklist exercises. The intent.md invariant ("one bad plan no longer aborts every subsequent plan from being settled") is satisfied in the logical sense — the loop does continue — but **the temporal-state invariant "plans B–E get reasonable time to attempt"** is not articulated and is not held.

**Plan anchor:** intent.md "Behaviors under review" #7 ("`SyncAll` partial-failure aggregation"): "Per-plan errors collected via errors.Join instead of aborting on first failure." Plan.md task T7 doesn't specify the temporal invariant. The trio audited the data-path; the timing-path is the gap.

**Severity:** Warning. Pre-existing pattern (the 5min ctx was the same in v0.3.2), but v0.3.3's T7 changes the operator's expectation: "now multi-plan sync should work even on a flaky chain." With one slow plan, it doesn't. The interaction is composition-defective.

**Suggested defense:** Either (a) give each `SyncPlan` call its own derived ctx with a per-plan budget — `planCtx, cancel := context.WithTimeout(ctx, 60*time.Second)` — so a slow plan doesn't starve siblings, OR (b) move the 5min budget cap from the CLI to a per-plan basis inside `SyncAll`, OR (c) emit a loud warning to stderr if a plan's `SyncPlan` consumes >N% of the outer budget. The default ctx budget should scale with `len(planOrder)`.

---

### F-NEW-402: Recovery regex doesn't recognize `BatchMixedPlanID` errors; a per-entry plan_id drift kills the batch unrecoverably (Warning, edge_case)

**Files:** `internal/chain/sync.go:300-305` (regex constants), `contracts/tribunal-reputation/src/execute/commit.rs:34-40`, `contracts/tribunal-reputation/src/error.rs` (`BatchMixedPlanID` variant).

**Scenario:** The contract's `commit_finding_batch` validates that every per-entry `FindingCommit.plan_id` matches the wrapping batch's `plan_id`:

```rust
for f in findings {
    if f.plan_id != plan_id {
        return Err(ContractError::BatchMixedPlanID {
            batch_plan_id: plan_id.clone(),
            found_plan_id: f.plan_id,
            finding_id: f.finding_id,
        });
    }
    process_finding(deps.branch(), env.clone(), f)?;
}
```

When this fires, the resulting LCD error message is `"finding {finding_id} (plan_id={found}) does not match batch plan_id={expected}"` or similar — definitively NOT matching `alreadyCommittedRE = "finding ([^/]+)/([^ ]+) already committed"`. The recovery layer's `matchDuplicate` returns `ok=false`. `submitCommitBatch` falls into the give-up branch at `sync.go:330`: `return br, 0, err`. F-NEW-301 returns.

How does the drift happen in practice? Three realistic paths:

1. **Queue file hand-edit or corruption.** The queue's `q.Msg.CommitFinding.PlanID` is an independent field from the queue's per-entry `PlanID` key (which is what `Queue.Drain(planID)` filters on). A hand-edit to fix one and not the other produces a batch where the wrapper says `planID=P-X` but one entry says `plan_id=P-Y`.
2. **Future refactor.** A future "draft mode" feature that lets an operator preview commits across plans before settling could easily cross-pollinate. The boundary that today is "filter by `f.PlanID == planID`" at `sync.go:133` is one careless edit away from a bug.
3. **CommitRealtime → queue → SyncPlan drift.** If the agent's plan_id is computed at commit-time but corrected later in the ledger (e.g., a plan was renamed), the queue still holds the old plan_id. SyncPlan drains by new plan_id, gets nothing. But SyncPlan also drains by old plan_id (if that's still in the ledger), gets the queued message — and its `CommitFinding.PlanID` is the OLD plan_id, while the batch's `planID` is the OLD plan_id too. Matches. Fine.
4. **Different scenario:** ledger.findings carries `f.PlanID = "P-X"` but at sync time the `FindingCommit` is built with `BuildFindingCommit(f, kp)` which copies `f.PlanID` into the commit's plan_id field. So they stay in sync at build time. But if a future test or fixture constructs a `FindingCommit` directly without going through `BuildFindingCommit`, drift is possible.

**Why the trio missed this:** The trio focused on the two recovery patterns the regex explicitly handles. Nobody read `commit.rs:31-40` to enumerate **every** atomic-batch-killing error the contract can emit and check that the recovery layer handles each. The audit corpus is "regex matches the documented patterns" not "regex matches every error that can revert a batch."

**Plan anchor:** intent.md "Failure modes" lists "Recovery regex drift if the contract changes its error string format → TestMatchDuplicate_CommitErrorParsing should fail loudly." This addresses drift in the regex's matching of `already committed`. It does NOT address the case where a different contract error fires. The plan's failure-mode enumeration is incomplete.

**Severity:** Warning. Triggering this requires corruption or a future refactor, not normal operation. But the recovery layer's contract is "absorb batch-atomic failures" — and BatchMixedPlanID is exactly that kind of failure. The current handling is silent fall-through to give-up.

**Suggested defense:** Either (a) enumerate every batch-atomic error in the contract's `error.rs` and add a regex / classifier for each, with explicit "unrecoverable" markers for ones recovery genuinely can't handle (BatchMixedPlanID is unrecoverable — there's no entry to drop, the whole batch is malformed); OR (b) add a pre-broadcast invariant check in `submitCommitBatch` that every `c.PlanID == planID` before sending, so the client catches its own malformed batch before the contract has to. Option (b) is cheap and tight; recommended.

---

### F-NEW-403: The methodology is not converging on a fixed point; each audit cycle's "fix" introduces a structurally-similar defect (Architectural recommendation, not a strict finding)

**Files:** comparison across `.tribunal/reports/P-v032-audit/` and `.tribunal/reports/P-v033-audit/`.

**Scenario:** This is the meta-finding only available to an adversary positioned to compare across audit cycles. The lens reviewers can't see it because their assignment is bounded to a single diff.

**P-v032-audit:** Trio approved v0.3.2 missing F-NEW-301 (batch atomicity + pre-flight false-negative → 100-commit revert from one LCD blip). Adversary surfaced it. Fix: add a post-broadcast recovery layer that parses contract errors and drops duplicates.

**P-v033-audit:** Trio caught F-ARCH-301 (recovery regex's char-class doesn't match contract's `validate_id_field` — `/` and space break recovery). This is **the v0.3.3 equivalent of F-NEW-301**. Same shape: a client-side primitive (recovery loop / regex parser) was added with an unstated assumption that's narrower than the contract's actual semantics. The contract is the authority; the client-side primitive was supposed to absorb edge cases; the absorber's edge-handling is narrower than the contract's edge-allowance.

**Pattern:** each iteration patches the _visible_ defect by adding a more complex client-side primitive. Each new primitive inherits the same modeling-error blind spot: **what does the contract actually permit, vs. what does the client-side code implicitly assume?** The pattern recurs because:

- The fix focuses on the immediate failure mode (a regex that recognizes `already committed` / `already resolved`).
- The fix doesn't address the **fragility of the primitive** (parsing untrusted error strings to drive recovery is brittle by construction).
- The next audit cycle finds a deeper variant of the same shape (the regex's char-class is wrong; or in F-NEW-402 above, a different error string altogether isn't handled).
- The next fix patches that variant. The cycle repeats.

**v0.3.4 prediction:** Likely the fix for F-ARCH-301 will (a) tighten the regex to handle `/` and space, (b) add test cases derived from `validate_id_field`'s charset, (c) leave the architectural primitive unchanged. The next audit cycle will find F-NEW-402's `BatchMixedPlanID` gap, or a different contract error the regex doesn't handle, or a TrimRight-class corruption on a new char class (`F-SEC-302` is exactly this), or a hostile LCD injection (F-SEC-301), or a not-yet-known-variant of the same shape. The methodology converges if and only if the v0.3.4 fix **replaces the primitive** (parse → structured contract query) rather than **patches** it.

**Why the trio missed this:** the lens reviewers' assignments are scoped to a single diff. None of them is positioned to ask "is the methodology converging?" — that's a meta-property of the audit ledger, not of the code under review. The adversary is the only role that gets to read prior audits and compare shape.

**Plan anchor:** intent.md "Purpose": "The methodology only earns its name if iterating on its own findings doesn't create new ones." The purpose statement explicitly invites this analysis. v0.3.3 vs v0.3.2 shows the methodology IS creating new findings of the same shape on each iteration. Whether the iterations are bounded or diverging depends entirely on the v0.3.4 architectural decision.

**Severity:** Not filed as a strict finding (no specific code-level defect). Filed as an architectural recommendation: **v0.3.4's fix should replace the parse-error-string primitive with a structured query primitive**. Suggested implementation: when `Execute` returns an error containing `tx ... failed on-chain (code=N): ...`, before parsing the raw_log for recovery hints, the recovery layer should re-query the contract directly (`Finding(planID, findingID)`) for each entry in the batch and identify duplicates via on-chain state, not via LCD-emitted error strings. This makes the contract the authority (closing F-SEC-301), eliminates the regex-vs-charset modeling error entirely (closing F-ARCH-301), and handles BatchMixedPlanID by allowing the client to detect drift pre-broadcast (closing F-NEW-402). One primitive replaces three.

**Suggested defense:** Architecture-level. The methodology converges on a fixed point if the next fix release retires the regex-based recovery in favor of contract-query-based recovery. If v0.3.4 ships another regex fix, expect F-NEW-501 on the next cycle.

---

## Cross-corpus blind spots

Two patterns showed up as shared blind spots across all three v0.3.3 reviewers (separately from the v0.3.2 blind-spots already noted in the prior adversary report):

### CB-1 (carry-forward): "The contract will reject duplicates" — now refined to "the recovery layer will absorb duplicate-rejections"

In P-v032-audit, the trio's shared blind spot was assuming the contract's per-call duplicate guard composes safely with batch broadcasts. v0.3.3 added the recovery layer to mediate this. The v0.3.3 trio's new shared blind spot is **assuming the recovery layer handles every batch-atomic failure mode** — but it only handles the two named "already committed" / "already resolved" cases. F-NEW-402 (BatchMixedPlanID) and F-ARCH-301 (charset narrower than validate_id_field's) are the same shape: the recovery layer's coverage is a strict subset of the contract's failure modes.

The training-corpus pattern: when reviewers see an "absorbing layer" between a strict primitive (the contract) and a permissive primitive (operator inputs), they tend to trust the absorbing layer's intent without auditing its **completeness**. Coverage gaps are invisible to per-lens checklists because the checklist asks "is the absorbing layer present?" not "does the absorbing layer match the strict primitive's full surface area?"

### CB-2: Single-invocation timing reasoning for a multi-plan, multi-iteration system

In P-v032-audit's blind spots I noted "single-plan reasoning for a multi-plan system" (CB-3 from prior). v0.3.3 retains this in a more subtle form: every lens reviewer reasoned about per-`Execute` timing and per-`SyncPlan` timing, but none reasoned about **temporal interactions across SyncAll's plan-iteration**. The 5-minute ctx is a per-CLI-invocation budget, not a per-plan budget. T7's "continue past failure" changes the operator's mental model from "one plan per invocation" to "all plans per invocation" — but the ctx model didn't update. F-NEW-401 above is the manifestation.

The training-corpus pattern: lens reviewers focus on the function under review. SyncAll is in scope but is treated as a thin orchestration wrapper. The fact that SyncAll changes the **operational semantics** of the ctx budget — from "one operation" to "N operations sequentially" — was not visible to any single-function audit.

---

## Things I attacked and did NOT find

In addition to confirming the trio's coverage on goroutine lifecycle, signature integrity in retried batches, and 0-multiplier round-trip, I probed and confirmed safe:

1. **Account-sequence mismatch between recovery retries.** The recovery loop only fires after `WaitForTx` observes an on-chain code != 0, which means the first batch has been observed. xiond auto-fetches the operator's sequence at each broadcast. Subsequent retries use sequence N+1 correctly. No race.
2. **Finding_id regex meta-character injection.** `c.FindingID != dupID` is a string compare, not a regex compile. An attacker who controls a finding_id of `.*` cannot make recovery accidentally match other findings.
3. **Cross-plan finding_id collision via dedup.** `seenCommit` / `seenResolve` are scoped inside `SyncPlan`, which only sees one planID at a time. No cross-plan key collision is possible.
4. **Queue-vs-ledger sync ordering.** The ledger entries are processed first (line 132), then queue entries (line 153). Same `seenCommit` map — queue duplicates of ledger-present findings are silently dropped. This is correct for normal operation (the ledger's freshly-built signature wins over a queued older signature after a key rotation) and acceptable for the no-rotation case.
5. **`transient_streak` reset on alternating LCD errors.** A hostile LCD that alternates `503` → `200 empty` → `503` → `200 empty` would reset the streak on every `200 empty`. Operator-facing log would show `transient_streak=1` instead of the actual transient burst rate. Misleading but not security-affecting; the outer ctx still bounds the wait. Filed as adjacent to F-PERF-302 — already a Suggestion-grade trio finding.
6. **SyncPlan discards `br` from `submitCommitBatch` on error.** At `sync.go:194-195`, `if err != nil { return nil, fmt.Errorf(...) }` — the `br` from submitCommitBatch is dropped. This is the same shape as F-NEW-302 from prior cycle (Execute's `br` discarded on error), now propagated one layer up through `submitCommitBatch`'s `(br, sent, err)` triple. The trio noted Execute's contract is honored only inside the recovery loop (arch line 51: "the recovery layer is the one new caller, and it correctly threads br through the bail paths"); they did NOT notice that **SyncPlan's caller of submitCommitBatch drops br exactly the same way the v0.3.2 callers dropped Execute's br**. This isn't a fresh finding because F-ARCH-303 (arch) implicitly covers the broader pattern of "internal change doesn't propagate to caller," but worth a Suggestion-grade cross-reference: the F-NEW-302 fix from v0.3.2 needs another layer of propagation now that `submitCommitBatch` is the new caller-of-Execute and SyncPlan is the new caller-of-submitCommitBatch.

---

## Verdict

**Escalate.**

The trio's bundle (1 Critical + 5 Warnings + 7 Suggestions across all three lenses, plus 3 carryovers) is well-grounded and lands the v0.3.3-equivalent of the v0.3.2 dogfood pattern: arch caught a structural Critical on the recovery layer; sec caught the trust-posture amplification; perf approved cleanly. The trio is doing its job at higher fidelity than on v0.3.2 — F-ARCH-301 is the kind of finding that v0.3.2's trio missed (and my prior adversary caught as F-NEW-301).

**My three new findings:**

- **F-NEW-401** (Warning): SyncAll's shared ctx defeats T7's "continue past failure" intent when one plan's recovery loop slow-rolls. Block on this — the operator-visible behavior of multi-plan sync is broken under the same workload that drove T7 to exist.
- **F-NEW-402** (Warning): Recovery regex doesn't handle `BatchMixedPlanID` — a third batch-atomic error the contract can emit. Defer to the v0.3.4 architectural decision (F-NEW-403); if recovery stays regex-based, add a BatchMixedPlanID case + pre-broadcast invariant check. If recovery moves to query-based, this finding closes for free.
- **F-NEW-403** (Architectural recommendation): the methodology is not converging on a fixed point under the current v0.3.4 trajectory. Each iteration patches a specific defect of the same shape (client-side primitive narrower than contract's actual semantics). The fix is to **replace the parse-error-string primitive with a contract-query primitive**, not patch it again. This is the v0.3.4 design call, not a v0.3.3 defect — but it's the call that determines whether the next audit cycle adds findings of the same shape or actually closes the loop.

**The most important thing the trio missed:** F-NEW-401 — SyncAll's shared ctx is the unit of timing, but T7 made SyncAll the unit of multi-plan settlement. The two don't compose. arch hinted at it in cross-reviewer notes (the recovery loop saturates the 5min budget) but didn't take the next step to multi-plan starvation.

**What made the trio's coverage airtight:**

- Goroutine lifecycle on the new preflight pool — perf walked every termination path including ctx-cancel-mid-flight. No leak, no race, no missing `wg.Wait`.
- Signature integrity in retried batches — sec confirmed that dropping an entry from the batch doesn't invalidate the rest's signatures (each is bound to per-entry canonical bytes).
- `outcome_reward_multiplier=0` correctness — arch caught the missing regression test (F-ARCH-305) but the actual fix is correct.

The methodology is buying real correctness — but it's not yet self-converging. v0.3.4's design call is whether to keep buying correctness one-defect-at-a-time (regex patches) or to amortize by replacing the primitive. Recommend the latter.

---

## FINDINGS-TO-FILE

```
warning|composition|F-NEW-401|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v033-audit/adversary.md#f-new-401|SyncAll's per-plan iterations share a single 5-minute ctx so a slow plan's recovery cycle starves subsequent plans of budget, defeating T7's continue-past-failure intent
warning|edge-case|F-NEW-402|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v033-audit/adversary.md#f-new-402|Recovery regex does not recognize BatchMixedPlanID errors so a per-entry plan_id drift between FindingCommit and batch wrapper renders the batch unrecoverable
suggestion|architecture-meta|F-NEW-403|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v033-audit/adversary.md#f-new-403|Methodology is not converging on a fixed point; each audit cycle's regex-patch fix introduces a structurally-similar defect, recommend replacing parse-error-string primitive with contract-query primitive in v0.3.4
```
