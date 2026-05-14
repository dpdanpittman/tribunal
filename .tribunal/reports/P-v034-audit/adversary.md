# Adversary Attack Report — Tribunal v0.3.4 Fix Release (Convergence Test)

**Adversary:** `tribunal-adversary` (single-model, default panel)
**Plan:** `P-v034-audit`
**Targets:** trio reports at `.tribunal/reports/P-v034-audit/reviewer-{arch,sec,perf}.md` (SPLIT verdict: arch Approve, sec Request Changes, perf Approve)
**Diff basis:** `fb37c3c^..fb37c3c` (`fb37c3c`, "v0.3.4: audit-driven fix release (P-v033-audit findings)")
**Verdict:** **Concur-with-sec, ESCALATE** — the convergence outcome is **#2 (pivoted but not converged)**, and the recursion shape sec found is older + wider than sec stated. Three new findings, one Critical, plus a meta-finding on the methodology's second-order convergence behavior.

---

## Summary

This audit was designed as the empirical convergence test. The trio split: arch (Approve) and perf (Approve) said converged; sec (Request Changes, Critical F-SEC-401) said pivoted-but-not-converged. **Sec is right. Arch and perf are looking at the right artifact through too narrow a lens.**

The pivot the v0.3.3 adversary prescribed (F-NEW-403) DID land cleanly: the regex helpers are deleted, the recovery primitive shares semantics with the success-path preflight by construction, and the regex-grammar narrowness recursion **cannot recur** because there is nothing to parse. arch is correct about that narrow claim. The problem: that narrow claim was never the load-bearing claim. F-NEW-403 named the proximate recursion (regex-vs-grammar); it did not name the deeper recursion (any client-side primitive that reads an oracle for "what is on-chain" must verify what it reads). The pivot patched the proximate recursion but ratified the deeper one in a new form.

Sec's F-SEC-401 is correctly Critical and correctly framed as "strictly worse than v0.3.3's F-SEC-301." But sec's writeup understates the surface in three load-bearing ways:

1. **The silent-suppression vulnerability is present on the SUCCESS path, not just the recovery path.** Sec described the recovery attack (operator submits → contract rejects → hostile LCD lies on recovery preflight → batch suppressed). The success-path attack is simpler and strictly cheaper: hostile LCD lies on the INITIAL preflight, `commits` ends up empty at `sync.go:201`, `submitCommitBatch` is never invoked, `SyncPlan` returns clean. **The attacker doesn't need to make Execute fail at all.** This success-path silent-suppression has been latent since v0.3.2 and no audit has surfaced it. F-NEW-501 below.

2. **The `(nil, 0, nil)` return on `len(commits) == 0` (line 350-352) is a sentinel that's structurally indistinguishable from both the legitimate "everything settled in a prior sync" case AND the F-SEC-401 attack's clean-exit case.** The operator-visible `SyncResult{FindingsSent: 0, CommitTxHash: ""}` cannot be classified after the fact as either "nothing to do" or "suppression victim." There is no field on `SyncResult` that distinguishes them. F-NEW-502 below.

3. **The new recovery log lost per-finding information that v0.3.3's regex provided.** v0.3.3's recovery emitted "recovered from duplicate plan/F-X, retrying with N findings" (per-finding ID). v0.3.4 emits "dropped 3 already-committed, retrying with N findings" (count only). This is a DEFENSIVE-feature regression that the trio missed because they were looking at what was added, not what was lost in the deletion. F-NEW-503 below.

Plus a fourth finding that arch hinted at but didn't escalate: there is **zero unit-test coverage** for the new structured-query recovery primitive — the load-bearing pivot of the entire release ships untested. F-NEW-504.

And a meta-finding only the adversary can produce: **the methodology IS converging on the specific recursion F-NEW-403 named, but is NOT converging on the broader "trust-source" defect class.** That's outcome #2 (pivoted but not converged) under the intent.md classification. The implication for v0.3.5+ is structurally important: convergence-by-pivot worked, but each pivot only retires the SHAPE the prior adversary named, not the underlying ARCHITECTURAL category. F-NEW-505 below — filed as architectural recommendation, not strict finding.

After triage: trio bundle stands; sec's F-SEC-401 is the load-bearing finding and is correctly Critical. My new findings: one Critical (the success-path version of F-SEC-401), two Warnings, one Suggestion (test debt), one architectural recommendation. **Verdict: ESCALATE — block v0.3.5 design on F-NEW-501 (silent-suppression on success path) and F-NEW-505 (deeper convergence question).**

---

## Resolving the arch/sec disagreement: which convergence outcome?

This audit's primary deliverable. The intent doc names three outcomes:

1. **Converged** — no new Critical; the recursion is broken.
2. **Pivoted but not converged** — a Critical in a DIFFERENT defect class.
3. **Still iterating** — a Critical in the SAME recursion shape.

arch claims outcome #1. Sec claims outcome #2. They disagree because they are using different definitions of "same defect class."

### Arch's definition of "same defect class"

arch's framing: the v0.3.3 defect was a **regex character class narrower than the contract's identifier grammar**. arch correctly observes there is no regex in v0.3.4, no character class to be narrow, no parsing surface at all. The literal v0.3.3 defect cannot recur. Arch's verdict is correct under this definition.

### Sec's definition of "same defect class"

sec's framing: the v0.3.3 defect was a **client primitive that trusts the LCD as truth-source for on-chain state**. sec correctly observes that the new structured-query primitive ALSO trusts the LCD as truth-source for on-chain state, with the trust-boundary in the SAME place (the parse of an LCD-returned envelope). Sec's verdict is correct under this definition.

### Both are right, but only one definition is load-bearing

The methodology's value proposition (per the intent.md "Purpose" and F-NEW-403's framing) is **not** "find regex bugs and replace them with non-regex bugs." The methodology's value proposition is **"iterating on the methodology's own findings produces a fixed point rather than a moving target."** Under that load-bearing definition, the relevant question is not "did the prior defect shape recur?" but **"did this iteration produce a defect that the next iteration will close — or will the next iteration produce a defect of the same shape too?"**

F-SEC-401 is **almost certainly the kind of defect that the next iteration will produce a structural fix for** (response validation, cross-source verification, Merkle-proof reads). That is, F-SEC-401 is a defect that the methodology CAN converge on. So under the load-bearing definition, the methodology is converging — each iteration produces a more architectural defect than the prior, and each iteration's fix is more architectural than the prior.

But — and this is the part that makes sec's verdict the correct one — **the new defect SHARES the architectural shape that produced the prior defect**. The trust-boundary placement is identical: "the LCD is the source of truth for what's on-chain." The defect form changed (parsing-narrowness → trust-naivety); the architectural decision producing it did not. **A primitive that asks an untrusted source whether something is on-chain has a defect surface bounded by the untrusted source's ability to lie. Patching the parsing while keeping the question structure produces a defect whose surface is the lie.**

Under intent.md's three-outcome classification, this is unambiguously **#2: Pivoted but not converged**. The intent doc itself says:

> Pivoted but not converged. A Critical in a different defect class (e.g. composition between recovery + preflight, or a new failure surface introduced by maxRecoveryAttempts). The methodology converged on the regex class but found another class.

F-SEC-401 is in a different defect class (trust-boundary, not parsing-narrowness). It IS a Critical. The intent doc's text matches sec's framing exactly. **Sec is correct.**

### Why arch's verdict isn't load-bearing

arch's verdict — "yes, converged on the regex-narrowness class" — is true and uninteresting. The methodology's purpose isn't to retire individual defect classes one at a time; it's to demonstrate that adversarial iteration shrinks the surface area where the NEXT defect can land. v0.3.4 retired one class (parsing-narrowness) but ratified another (LCD-as-oracle). The aggregate surface didn't shrink in the way the methodology promises.

This is a subtle but important distinction. arch is reporting on a SPECIFIC convergence question (did the prescribed fix work for its named target?). The methodology demands a STRONGER convergence claim (does each iteration shrink the total surface?). Under the stronger claim, v0.3.4 is outcome #2.

### What it means for v0.3.5

Outcome #2 isn't catastrophic. It means: the prescribed fix worked for what it was prescribed to fix, AND it exposed a deeper defect that the prescribed fix didn't address. The methodology is doing what it was designed to do — surface defects more architecturally as iteration proceeds. v0.3.4 should ship after F-SEC-401 (or F-NEW-501) is closed, NOT before. The recursion isn't broken; it's shifted to a layer where the fix has a clearer structural answer (validate LCD responses, OR cross-source via Tendermint RPC, OR write a sentinel for silent-suppression detection — pick one or several).

The convergence-controller question (F-NEW-403's deeper framing) becomes: **after how many iterations does the surface stabilize?** v0.3.4 shows it's at least 2 (regex → LCD-trust). Whether v0.3.5 surfaces a third recursion depends on which defense path is chosen and whether THAT defense itself ratifies a deeper trust assumption. I prescribe specific paths in F-NEW-505 below.

---

## Trio finding triage

| ID           | Trio severity | My call                                             | Rationale (one line)                                                                                                                                                                                                                                                                                |
| ------------ | ------------- | --------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| F-ARCH-401   | Warning       | **Escalate to Critical**                            | This is the recovery-path LCD-availability dependency. sec's F-SEC-401 names the active-hostile case; arch's F-ARCH-401 names the unavailable case. They're two scenarios on the same defect axis. Sec's framing is correct; arch under-graded by treating availability as separate from integrity. |
| F-ARCH-402   | Warning       | **Concur, but the outer ctx is the bigger problem** | per-plan 90s vs outer 5min: with 4+ plans, plan N gets `min(90s, 300-89*N)` and starves anyway. The fix isn't just widening 90s; it's deriving the outer ctx from `N × per-plan + slack`. Filed as F-NEW-506 (cross-reference).                                                                     |
| F-ARCH-403   | Suggestion    | **Concur**                                          | `PreflightConcurrency` default in `preflight()` not `applyDefaults()`. Style consistency.                                                                                                                                                                                                           |
| F-ARCH-404   | Suggestion    | **Concur**                                          | Docstring "every realistic" overstates. Tighten to "healthy-LCD."                                                                                                                                                                                                                                   |
| F-ARCH-405   | Suggestion    | **Concur**                                          | Duplicated `looksLikeTestChain`.                                                                                                                                                                                                                                                                    |
| F-SEC-401    | Critical      | **Concur — this is the load-bearing finding**       | Sec correctly identifies the LCD-as-oracle trust hole and correctly grades Critical. My F-NEW-501 below adds: same defect exists on success path, strictly cheaper for the attacker.                                                                                                                |
| F-SEC-402    | Warning       | **Concur**                                          | Input-validation gap that makes F-SEC-401 a single-line attack.                                                                                                                                                                                                                                     |
| F-SEC-403    | Suggestion    | **Concur**                                          | `preflight_concurrency` no upper bound. Defense-in-depth.                                                                                                                                                                                                                                           |
| F-SEC-404    | Suggestion    | **Concur**                                          | `looksLikeTestChain` duplication (same as F-ARCH-405).                                                                                                                                                                                                                                              |
| F-SEC-205-cf | Warning       | **Concur, escalate**                                | `tcp://` → `http://` silent rewrite plus F-SEC-401 = MITM full attack chain. Sec already noted this composition.                                                                                                                                                                                    |
| F-SEC-208-cf | Suggestion    | **Concur**                                          | Carryforward; cheap fix unchanged.                                                                                                                                                                                                                                                                  |
| F-SEC-206-cf | Suggestion    | **Concur**                                          | Carryforward; cheap fix unchanged.                                                                                                                                                                                                                                                                  |
| F-SEC-304-cf | Suggestion    | **Concur, surface area now doubled**                | sec correctly noted v0.3.4 added a second preflight call site without testing the partial-cancel invariant.                                                                                                                                                                                         |
| F-SEC-305-cf | Suggestion    | **Concur, coupled to F-SEC-401**                    | RPC-supplied raw_log is the trigger for the recovery path that F-SEC-401 exploits.                                                                                                                                                                                                                  |
| F-PERF-401   | Suggestion    | **Concur**                                          | Cumulative-elapsed in recovery log. UX gap.                                                                                                                                                                                                                                                         |
| F-PERF-402   | Suggestion    | **Concur, sec also caught (F-SEC-403)**             | `preflight_concurrency` no upper bound. Same finding.                                                                                                                                                                                                                                               |
| F-PERF-403   | Suggestion    | **Concur**                                          | Preflight progress note missing done/in-flight.                                                                                                                                                                                                                                                     |

**Verdict on trio:** sec's bundle is correct. arch's bundle understates by missing F-SEC-401's framing. perf's bundle is appropriate for the perf lens but doesn't see the trust hole.

The single most important triage call: **F-SEC-401 is correctly Critical. arch's F-ARCH-401 (degraded-LCD recovery friction) is in the same defect family but graded down because arch treated availability and integrity as separable, then graded the lesser one. They are not separable.**

---

## New findings the trio missed

### F-NEW-501: Silent-suppression vulnerability exists on the SUCCESS path, not just the recovery path; attacker doesn't need to trigger Execute failure (Critical, shared_blind_spot)

**Files:** `internal/chain/sync.go:133` (the preflight call in SyncPlan), `:149-151` (the success-path filter), `:170-172` (queue-side same pattern), `:187-188` (resolutions same pattern), `:201-211` (the `if len(commits) > 0` gate that decides whether submitCommitBatch is invoked at all).

**Scenario:**

Operator runs `tribunal chain sync` against a plan with 50 uncommitted findings against a hostile (or MITM'd) LCD endpoint.

1. SyncPlan builds `checkIDs = {F_1, F_2, ..., F_50}` from the ledger (sync.go:117-132).
2. SyncPlan calls `s.preflight(ctx, planID, checkIDs)` (sync.go:133). The preflight worker pool issues 50 `Finding(planID, F_i)` queries via the LCD smart-query path.
3. **Hostile LCD responds to each query with `{"data":{"finding":{}}}`.** Go's `json.Unmarshal` on FindingResp produces `FindingResp{Finding: &FindingState{}}` (non-nil pointer to zero-valued struct).
4. preflight worker (sync.go:272-276): `err != nil` is false; `resp == nil` is false; `resp.Finding == nil` is false → branch falls through to `resCh <- result{id: id, committed: true, resolved: resp.Finding.Resolution != nil}`. Since the zero-valued FindingState has nil Resolution, `resolved=false`. So `committed[F_i] = true` for every i.
5. Back in SyncPlan: the build-commits loop (sync.go:141-161) sees `committedOnChain[F_i] = true` for every finding, so EVERY iteration hits the `continue` at line 150. **`commits` is empty when the loop finishes.**
6. The queue-side loop (sync.go:162-174) has the same filter, same outcome.
7. The resolutions loop (sync.go:179-199) — if the LCD also forges `resolved=true` (via a non-nil Resolution field in the forged response), `resCommits` is empty too. Or even if resolutions are legitimately committed-but-unresolved, the resolution side reaches `resolvedOnChain[F_i] = false` (zero-valued Resolution is nil) and proceeds — but **resolutions for findings the contract NEVER COMMITTED will fail at the contract with FindingNotCommitted**, hitting recovery which... is bounded by maxRecoveryAttempts.
8. At sync.go:201: `if len(commits) > 0` is FALSE. **`submitCommitBatch` is never invoked.** No Execute call. No tx broadcast. No gas burn.
9. SyncPlan returns `&SyncResult{PlanID, FindingsSent: 0, CommitTxHash: "", ...}` with **`err = nil`**.
10. CLI prints `plan=X findings=0 resolutions=? queue_drained=N commit_tx= resolve_tx=...` and exits 0.

**Operator's view of the world:** "Plan synced; nothing to do; 0 findings sent because they were all already on-chain." This is observationally identical to the legitimate idempotent re-sync case where a prior sync settled everything. **The operator cannot distinguish "settled in a prior run" from "suppressed by hostile LCD" without an out-of-band chain query.**

**Re-running sync repeats the attack.** The LCD keeps lying; preflight keeps marking everything committed; `commits` is always empty; no findings ever land. The plan's findings are NEVER settled until the operator points at a different LCD.

**Comparison to F-SEC-401:**

| Attack property                  | F-SEC-401 (recovery path)                                              | F-NEW-501 (success path)                          |
| -------------------------------- | ---------------------------------------------------------------------- | ------------------------------------------------- |
| Requires Execute to fail         | Yes (attacker needs hostile WaitForTx response)                        | **No**                                            |
| Requires recovery loop to invoke | Yes (one Execute + one preflight)                                      | **No, single preflight only**                     |
| Number of LCD lies required      | 1 wait-for-tx lie + 50 query lies                                      | **50 query lies**                                 |
| Operator gas cost                | ~14M gas (one Execute)                                                 | **0 (no broadcast)**                              |
| Stderr noise                     | Visible "recovered via state query, dropped 50 already-committed" line | **None — silent success**                         |
| Operator's visible signal        | One Execute attempt observable + "recovered..." log                    | **None — looks like a normal idempotent re-sync** |

**F-NEW-501 is strictly worse than F-SEC-401 on every axis.** F-SEC-401 makes noise (a visible Execute attempt + a recovery log line). F-NEW-501 makes no noise at all. The trio focused on the recovery path because that's the new code in v0.3.4. The success path has been silently exploitable since v0.3.2 because the same trust-boundary primitive (`preflight()`) underpins both.

**Why the trio missed this:**

- **arch** explicitly framed the new primitive's value as "input domain match is type-level" (reviewer-arch.md:24) but did not interrogate what the input domain ACCEPTS. The match between LCD response and contract's storage map is type-level. The match between LCD response and contract's TRUTH is not — the LCD can lie. arch saw only the type-level shape of the success path; the success path's trust posture wasn't part of arch's framing.
- **sec** caught the trust-posture issue at the recovery path because v0.3.4 made the recovery path the new attack surface. Sec correctly named the defect class ("LCD as oracle") but framed the suppression vector as recovery-path-specific. The reasoning in reviewer-sec.md:14 says "submitCommitBatch returns (nil, 0, nil)" as the suppression mechanism. That's the recovery-path version. The success-path version doesn't even reach submitCommitBatch.
- **perf** correctly noted "a hostile LCD can amplify recovery cost by claiming all findings are uncommitted" (reviewer-perf.md:538) — the OPPOSITE attack from F-SEC-401/F-NEW-501. perf didn't trace the version where the LCD claims all findings ARE committed.

The shared blind spot: **the lens reviewers learned that v0.3.4's new code is the recovery primitive. They focused there. The pre-existing success-path primitive — IDENTICAL trust posture, IDENTICAL vulnerability — is older code that the lens checklists didn't re-audit.** Adversarial review's value here is precisely this: it reads the whole call graph, not just the diff.

**Plan anchor:** intent.md §27 ("the structured-query primitive's input domain (the set of `(plan_id, finding_id)` keys the LCD returns state for) matches the contract's truth domain (the `FINDINGS` storage map keyed identically) at every type-level boundary") — this is the SHARED ASSUMPTION the trio operated under. It is true at the type level. It is FALSE at the trust level. The same primitive consumes the same LCD response on both paths; the trust hole is identical.

**Severity: Critical.** Strictly worse than F-SEC-401 (zero broadcast cost, zero stderr noise, zero distinguishability from legitimate idempotent re-sync). The contract is still authoritative (no reputation forgery, no signature bypass) so this is Tribunal-integrity-level not contract-level Critical, same ladder rung as F-SEC-401.

**Suggested defense:** The fix for F-NEW-501 closes F-SEC-401 by construction (same primitive). Best paths, sorted by cost:

(a) **Strict response validation** (sec's Defense (a) on F-SEC-401, applied to `preflight()`): in the preflight worker, after `s.Client.Finding` returns, validate `resp.Finding.PlanID == planID`, `resp.Finding.FindingID == id`, `resp.Finding.AgentPubkey != ""`, `resp.Finding.CommittedAt != ""`, `resp.Finding.ClaimHash != ""`. Reject malformed responses as `committed=false`. **Closes both F-NEW-501 and F-SEC-401's trivial forgery.** ~15 lines.

(b) **Cross-source via local ledger** (sec's Defense (b)): on success path, before believing the LCD says F is committed, cross-check the LCD-supplied `claim_hash` and `agent_pubkey` against what the operator's local `findings[]` says they SHOULD be. If LCD says F is committed with a different claim_hash than what the agent signed, **the LCD is lying or there's been a chain-side overwrite (impossible)**; in either case, treat as suspicious and DON'T filter. ~10 lines, gives true cross-source verification.

(c) **Loud sentinel on full-batch suppression** (sec's Defense (d), applied to success path too): if the preflight result is "every checkID is committed and resolved" AND the local ledger has findings to settle, emit `tribunal: WARNING — every finding for plan %s reports as already-settled on first preflight; manual verification recommended` to stderr. Won't break the legitimate idempotent re-sync case (no warning if local ledger is empty), but flags the hostile case. ~5 lines.

(d) **ABCI-proof reads** (sec's Defense (c)): replace LCD smart-query with `/abci_query` against contract storage with IAVL proof verified against latest block header. Removes LCD from TCB. v0.4 work.

**My recommendation: (a) + (c) for v0.3.5 minimum.** Without these, v0.3.4 ships exploitable. (a) raises attacker cost from "one byte of JSON" to "construct a plausible FindingState envelope per query"; (c) raises attacker detection from "invisible" to "stderr warning." (b) is the v0.3.5 architectural fix. (d) is v0.4 roadmap.

---

### F-NEW-502: `submitCommitBatch` returns `(nil, 0, nil)` for both legitimate `len(commits) == 0` and recovery-filter-to-empty; SyncResult has no field that distinguishes them (Warning, refinement_mismatch)

**Files:** `internal/chain/sync.go:350-352` (the `len(commits) == 0` early-return), `:367-372` (the recovery filter), `:201-211` (the gate that decides whether submitCommitBatch is invoked at all), and the `SyncResult` struct at `:47-55`.

**Scenario:**

`submitCommitBatch`'s `(nil, 0, nil)` return triggers in three observationally-identical-but-semantically-distinct cases:

1. **Legitimate: nothing to commit.** `commits` arrives empty (e.g., success-path preflight filtered everything because they really WERE committed). The `len(commits) == 0` check at line 350 fires on the FIRST loop iteration. Operator's correct conclusion: "all settled in prior sync."
2. **Recovery filtered everything (legitimate).** Operator submitted a batch where every entry was a real duplicate the contract has already seen. Execute fails. Recovery preflight legitimately returns "all committed." `filtered = commits[:0]` ends empty. `commits = filtered` makes len 0. Next loop iteration hits the `len(commits) == 0` early-return at line 350. Operator's correct conclusion: "the batch I submitted was already settled."
3. **Hostile-LCD suppression (F-SEC-401 / F-NEW-501).** As described in F-NEW-501. `submitCommitBatch` returns `(nil, 0, nil)` via the same code path.

**Operator-visible SyncResult fields after each case:**

```
case 1: {FindingsSent: 0, CommitTxHash: ""}
case 2: {FindingsSent: 0, CommitTxHash: ""}
case 3: {FindingsSent: 0, CommitTxHash: ""}
```

Identical. The operator (or a downstream PM, or the CLI's JSON output consumed by other tooling) cannot distinguish. **The sentinel is overloaded across three semantically-different cases.**

**Why this is a separate finding from F-NEW-501:**

F-NEW-501 names the attack. F-NEW-502 names the structural reason F-NEW-501 is unobservable. Even with response validation (F-NEW-501's defense (a)), if an attacker constructs a well-formed FindingResp (e.g., one stolen from a real chain query), the suppression still goes through the same `(nil, 0, nil)` exit and is still unobservable in the SyncResult. **F-NEW-502 is the observability defect; closing it lets defenses-in-depth detect a suppression even when validation is bypassed.**

**Plan anchor:** intent.md §47 ("`Concrete scenarios` #1 Happy-path: no recovery needed... No recovery layer invoked") establishes that legitimate `len(commits) == 0` is a real case. The spec doesn't address recovery-filtered-to-empty as a separate case. Both return through the same path with no structural distinction.

**Severity: Warning.** This is a structural-observability defect, not an exploitable defect per se. Combined with F-NEW-501 it removes the operator's ability to notice an attack; alone it's a code-smell.

**Suggested defense:**

- Add a field to `SyncResult`: either `RecoveryFilteredCount int` (0 means happy path, >0 means recovery filtered N entries) and/or `PreflightSuppressedCount int` (0 means no entries dropped at preflight, >0 means N entries were filtered at preflight).
- In `submitCommitBatch`, distinguish the two empty-batch paths: the line-350 early-return for the FIRST iteration is the legitimate "nothing to do" case, but the same return path after `commits = filtered` makes len-0 is the recovery-emptied case. Either track which path hit (a `recoveryFiltered bool` flag in the loop), OR have the post-recovery empty case return a different result (e.g., `return nil, 0, fmt.Errorf("commit batch fully filtered by recovery (n=%d) — verify LCD trust", originalLen)` as a loud failure, which is sec's Defense (d)).
- The cleanest defense is: **never silently return `(nil, 0, nil)` from a code path that had ANY findings as input.** The line-350 return is fine when invoked with empty commits. Anything else should be a structured signal.

---

### F-NEW-503: Removal of regex deleted per-finding observability in the recovery log; operator now knows the count of dropped findings but not which ones (Warning, refinement_mismatch)

**Files:** `internal/chain/sync.go:377-378` (the new recovery log) compared to v0.3.3's regex recovery log at the same site.

**Scenario:**

v0.3.3's recovery log emitted:

```
tribunal: commit batch recovered from duplicate P-X/F-12, retrying with 49 findings
```

— per-finding ID, by parsing the contract's error string with the regex. The operator could read this and learn "F-12 is the duplicate."

v0.3.4's recovery log emits:

```
tribunal: commit batch recovered via state query, dropped 3 already-committed, retrying with 47 findings
```

— a count only. The operator learns "3 entries were dropped" but does NOT learn which 3.

**When this matters:**

1. **Debugging a settlement mystery.** An operator notices their settlement count is off by N findings. They want to figure out which N. v0.3.3's log let them grep stderr for the IDs. v0.3.4's log doesn't.
2. **Auditing a hostile-LCD recovery.** Even if F-NEW-501 / F-SEC-401 is closed (with response validation), an attacker who CAN forge a valid FindingResp drops findings the operator never authorized. The recovery log is the operator's only signal of which findings the LCD claimed are already-on-chain. v0.3.4's count-only log makes this audit impossible.
3. **Verifying recovery semantics.** Was the right finding dropped? v0.3.4 makes you re-query each finding against another LCD to check.

**Why the trio missed this:**

The trio analyzed what was ADDED in v0.3.4 (the structured-query primitive). They did not analyze what was LOST in deleting the regex. The regex was a SECURITY surface (F-SEC-301) AND a SIGNAL surface (per-finding ID extraction). The deletion correctly closed the security surface but accidentally also closed the signal surface, with no replacement. The structured-query primitive HAS the per-finding-ID information (in the `committed` map's keys), it just doesn't log it.

**Plan anchor:** intent.md §27 prescribes the structured-query pivot but says nothing about preserving operator-visible per-finding observability across the transition.

**Severity: Warning.** Operator-UX regression, not correctness. But it makes F-NEW-501/F-SEC-401's recovery-path version harder to audit even after defense, which is why it matters for the overall security posture.

**Suggested defense:**

Modify lines 377-378 to enumerate the dropped IDs in the log:

```go
droppedIDs := make([]string, 0, len(commits)-len(filtered))
for _, c := range commits {
    if committed[c.FindingID] {
        droppedIDs = append(droppedIDs, c.FindingID)
    }
}
fmt.Fprintf(os.Stderr,
    "tribunal: commit batch recovered via state query, dropped %d already-committed [%s], retrying with %d findings\n",
    len(droppedIDs), strings.Join(droppedIDs, ","), len(filtered))
```

(Caveat: this requires re-introducing `strings` import. Or use `fmt.Sprint` over the slice. The reintroduction of `strings` is fine — the v0.3.4 removal was about removing the parsing surface, not the package.)

The same fix applies to the resolve-side log at lines 415-416.

---

### F-NEW-504: Zero unit-test coverage for the new structured-query recovery primitive — the load-bearing pivot of v0.3.4 ships untested (Suggestion, refinement_mismatch)

**Files:** `internal/chain/sync_test.go` (the file that should but doesn't have these tests).

**Scenario:**

`grep "^func Test" /home/dan/src/tribunal/internal/chain/sync_test.go` yields:

- `TestLooksLikeTestChain_TokenAware` — covers T5/T6.
- `TestClient_Reputation_ParsesEnvelope` — covers query envelope parsing.
- `TestClient_Status_ParsesHeight` — covers status parsing.
- `TestQueue_EnqueueAndDrain` — covers queue mechanics.
- `TestSync_BuildsCommitsFromLedger` — covers the success-path commit building (line 167).

**There is no test for `submitCommitBatch` or `submitResolveBatch`.** The recovery primitive — T1, the load-bearing pivot, the answer to F-NEW-403 — has no unit-test coverage. The removed regex test (T10 deletion) was the only test that touched the recovery path; v0.3.4 removed it and didn't add a replacement.

**Why this matters specifically for v0.3.4:**

- F-NEW-403 named the convergence question. v0.3.4 is the answer. The answer's correctness is asserted by `go test ./...` passing. But the answer's correctness on the critical path (recovery) is asserted by no test at all.
- A unit test with a fake `chain.Client` that:
  1. Returns the same error from Execute every call (simulating duplicate rejection),
  2. Returns `committed=true` for a controlled subset on Finding queries,
  3. Asserts the filtered batch size shrinks by exactly that subset per iteration,
  4. Asserts the loop terminates at `maxRecoveryAttempts`,
  5. Asserts `(nil, 0, nil)` on legitimate filter-to-empty,
  6. Asserts the loud error on filter-doesn't-shrink case.

  ... would be ~80 lines and would catch any future regression in the recovery primitive AND would catch the silent-suppression vulnerability (F-NEW-501) in the test that explicitly returns "all committed."

**Why the trio missed this:**

- arch noted "Tests pass" (reviewer-arch.md:121) without auditing what's covered.
- sec's verification at reviewer-sec.md:55 says "Verified via `grep`" but verified absence of regex symbols, not absence of test coverage.
- perf explicitly called this out in cross-reviewer notes (reviewer-perf.md:524-530): "No test coverage for submitCommitBatch / submitResolveBatch recovery path... arch's lane." It was filed to arch as a test-debt finding; arch didn't file it as a finding.

This is the kind of finding that falls in the gap between two reviewers. perf saw it and named it; arch was supposed to receive it; the file-back step didn't happen. **The trio's coverage IS airtight if you read the union of their reports — but each lens reviewer files only what's in their lane, and "test debt on the recovery primitive" is in nobody's clear lane.** The adversarial role's job is precisely to catch findings that fall in inter-lens gaps.

**Plan anchor:** plan.md acceptance criteria includes "v0.3.4 lands without regressing the existing test suite (`go test ./...` clean)." This passes. But it does NOT include "the new recovery primitive has unit-test coverage." That gap is in the plan.

**Severity: Suggestion (escalating to Warning given the load-bearing role).** This is test debt — no defect by itself. But for the LOAD-BEARING pivot of the release, no test = no confidence the structural fix works under adversarial inputs. The trio's "Approve" on v0.3.4 is based on `go test ./...` passing, which doesn't test the part of the release the verdict is about.

**Suggested defense:**

Add a test file `internal/chain/sync_recovery_test.go` with at minimum:

```go
func TestSubmitCommitBatch_RecoveryFiltersDuplicates(t *testing.T) {
    // Fake Client: Execute fails once, Finding returns committed=true for F-1
    // Assert: batch shrinks from [F-1, F-2] to [F-2], retry succeeds.
}

func TestSubmitCommitBatch_RecoveryExhausts(t *testing.T) {
    // Fake Client: Execute fails every call, Finding returns committed=false for all
    // Assert: returns "exhausted recovery attempts" after maxRecoveryAttempts.
}

func TestSubmitCommitBatch_RecoveryNoDuplicatesSurfacesError(t *testing.T) {
    // Fake Client: Execute fails with "gas too low", Finding returns committed=false for all
    // Assert: returns the gas-too-low error wrapped, NOT a recovery error.
}

func TestSubmitCommitBatch_FullSuppression_HostileFinding(t *testing.T) {
    // Fake Client: Execute fails, Finding returns committed=true for ALL.
    // Assert: returns (nil, 0, nil) — and this assertion should be the
    //         CANARY that protects against F-NEW-501. When F-NEW-501 is
    //         fixed (sentinel on full suppression), this test is updated
    //         to assert the loud-failure path.
}

func TestSubmitResolveBatch_RecoveryFiltersResolved(t *testing.T) { /* mirror */ }
```

This is ~80-120 lines. Trivial cost; load-bearing test debt.

---

### F-NEW-505: The methodology IS converging on the specific recursion F-NEW-403 named but has NOT converged on the broader "untrusted-source as truth-oracle" defect family (Architectural recommendation, not a strict finding)

**Files:** comparison across `.tribunal/reports/P-v032-audit/adversary.md`, `.tribunal/reports/P-v033-audit/adversary.md`, and this report.

**Scenario:**

This is the meta-finding only the adversary positioned to compare across audit cycles can produce. Both arch and sec are scoped to a single diff and cannot file this.

**The pattern:**

- **P-v032-audit (v0.3.2):** F-NEW-301 (Critical, adversary-filed). Batch atomicity + preflight false-negative = 100-commit revert from one LCD blip. The defect: preflight's LCD-error tolerance + batch atomicity composed badly.
- **P-v033-audit (v0.3.3):** F-ARCH-301 (Critical, trio-filed) + F-SEC-301 (Warning, trio-filed) + F-NEW-403 (architectural, adversary-filed). Recovery regex narrower than contract grammar AND LCD-tainted raw_log injection. The defect: a client-side primitive trusts LCD text as a source-of-truth for which entry to drop on retry.
- **P-v034-audit (v0.3.4):** F-SEC-401 (Critical, trio-filed) + F-NEW-501 (Critical, adversary-filed). Structured-query recovery trusts LCD response as source-of-truth for what's on-chain. The defect: a client-side primitive trusts LCD JSON as a source-of-truth for which entries to filter.

**The persistent shape across three cycles:** in every cycle, there is a client-side primitive that consults the LCD for "what is the contract's view of state X" and uses the LCD's answer to drive a settlement decision. v0.3.2: preflight tells us which findings to omit from the batch. v0.3.3: recovery regex tells us which finding the contract rejected. v0.3.4: structured-query tells us which findings to filter on retry AND on success-path.

**Each fix retired the SPECIFIC primitive named:**

- v0.3.2 → v0.3.3: added recovery layer. Closed F-NEW-301 (specific composition). Opened F-ARCH-301 (recovery regex narrowness).
- v0.3.3 → v0.3.4: replaced regex with structured query. Closed F-ARCH-301 (specific narrowness). Opened F-SEC-401 (structured query trust hole) AND F-NEW-501 (same on success path).

**The methodology IS converging on the SHAPES it explicitly names.** v0.3.4's "no more regex" is a real convergence on the regex shape. **But the methodology is NOT converging on the broader defect family** — "client trusts LCD as truth-oracle for chain state" — because each iteration's prescribed fix has been TARGETED at the specific shape, not the family.

This is a stronger statement than F-NEW-403's. F-NEW-403 predicted "each iteration buys correctness against the previous specific defect but doesn't shrink the surface where the next defect can land." v0.3.4 PROVES this: the regex defect is closed (specific surface shrunk), but the next defect landed in EXACTLY the predicted place (same family, deeper shape).

**This is convergent at the iteration level — each iteration's fix is more architectural than the prior** (regex patch → primitive replacement → primitive trust hardening, prescribed for v0.3.5). **But it's not yet a fixed point** because the family of "client trusts LCD" hasn't been closed.

**The v0.3.5 prediction:**

If v0.3.5 ships ONLY response validation (F-SEC-401 / F-NEW-501's Defense (a)), the family stays open: a sophisticated attacker constructs a well-formed FindingResp (e.g., one stolen from a previous legitimate response, or one synthesized from public on-chain data) and the validation passes. The defect resurfaces. v0.3.6 audit finds it.

If v0.3.5 ships cross-source verification (Defense (b)), the family narrows but doesn't fully close: the cross-source compare is to the operator's LOCAL ledger, which is itself an untrusted-source-by-different-rules (operator could have a stale ledger; a malicious operator could lie about the ledger). The defect resurfaces at a different layer.

If v0.3.5 ships Tendermint RPC ABCI-proof reads (Defense (c)), the family closes: the LCD is removed from the TCB, and the proof verification anchors trust in the validator-set's BFT consensus signatures, which is the only assumption the operator already makes anyway. **Defense (c) is the architectural fix that retires the family.**

**The convergence-controller question (F-NEW-403's deeper framing):**

The methodology converges on a defect family in three patterns:

1. **Specific-target convergence (v0.3.3 → v0.3.4):** each iteration retires a specific shape. Converges asymptotically; surface eventually shrinks to zero but each iteration adds a smaller residual. This is what the methodology has been doing.

2. **Family-target convergence (v0.3.5 → v0.3.6, hypothetical):** an iteration retires a whole family by changing the underlying architectural decision (Defense (c) above). The family closes; the next audit looks at a DIFFERENT family. This is what the methodology needs to demonstrate next.

3. **Divergent (worst case):** each iteration retires the prior shape but opens a wider one in the same family. v0.3.4 had a hint of this — F-NEW-501's no-broadcast-needed shape is wider than F-SEC-301's per-finding shape. If v0.3.5's fix opens an even wider shape, the methodology is divergent on this family.

**The prescription for v0.3.5:**

- **Ship F-NEW-501 / F-SEC-401's Defense (a) + (c) immediately.** Validation closes the trivial-forgery vector; the suppression-sentinel makes the harder attack observable. These are table stakes.
- **Roadmap v0.4 around Defense (b) AND (c) together.** Local-ledger cross-source as a transitional safety net; ABCI-proof reads as the architectural fix. ADR the trust-model shift.
- **The audit ledger of "what the methodology learns" needs an entry:** "specific-target fixes are necessary but not sufficient; once a family is named, the v0.X iteration after the naming must address the FAMILY, not the specific instance within it." This is what F-NEW-405 prescribes.

**Severity: Architectural recommendation.** Not a strict finding (no specific code-level defect). The strict findings are F-NEW-501 through F-NEW-504 above. F-NEW-505 is the meta-pattern that explains why F-NEW-501 was inevitable given the v0.3.4 fix's scope.

---

### F-NEW-506: The CLI's outer 5-minute ctx is unchanged from v0.3.3 but is now too tight relative to the per-plan 90s budget for ledgers with 4+ plans (Warning, composition_failure)

**Files:** `cmd/tribunal/chain.go:201` (the 5-minute outer ctx, unchanged from v0.3.3), `internal/chain/sync.go:67` (`perPlanSyncBudget = 90s`, new in v0.3.4), `internal/chain/sync.go:463` (the per-plan WithTimeout).

**Scenario:**

`context.WithTimeout(parent, dur)` returns a ctx with deadline = `min(parent.deadline, now + dur)`. The CLI's `context.WithTimeout(context.Background(), 5*time.Minute)` sets the outer deadline at T0 + 5min. Each plan's `WithTimeout(ctx, 90s)` sets per-plan deadline = `min(T0 + 5min, T_plan_start + 90s)`.

For N plans where each takes the full 90s:

- Plan 1: starts T0, deadline = min(T0+300s, T0+90s) = T0+90s. Gets 90s.
- Plan 2: starts T0+90s, deadline = min(T0+300s, T0+180s) = T0+180s. Gets 90s.
- Plan 3: starts T0+180s, deadline = min(T0+300s, T0+270s) = T0+270s. Gets 90s.
- Plan 4: starts T0+270s, deadline = min(T0+300s, T0+360s) = **T0+300s. Gets only 30s.**
- Plan 5+: starts T0+300s, deadline already passed. **Gets 0s.**

So for ledgers with ≥4 plans, the per-plan fairness invariant F-NEW-401 was supposed to establish DOESN'T HOLD. Plan 4 gets truncated; plan 5 gets nothing. From the operator's POV, this looks identical to v0.3.2's "first failure kills the rest."

**Why the trio missed this:**

- arch noted the 90s budget might be too tight (F-ARCH-402) but framed it as a per-plan concern.
- arch's cross-reviewer note flagged "the recovery loop's worst-case wall-time saturates the budget" as a CONSTANT issue — didn't trace the per-plan-vs-outer composition.
- sec noted the 90s budget concern indirectly (reviewer-sec.md:62) but as an observability concern.
- perf walked the worst-case math at N=100 single plan (reviewer-perf.md:165-178) and noted "typical worst case fits in 90s with ~8s slack." Did not extend to N-plan composition.

The composition gap: F-NEW-401's fix was supposed to give each plan its own 90s. The fix gives each plan UP TO 90s, with the outer ctx as the binding constraint after 3+ plans. The CLI's 5-minute outer ctx (unchanged from v0.3.2) and the per-plan 90s budget (introduced in v0.3.4) were set against different assumptions.

**Plan anchor:** intent.md §59 (Concrete scenarios) describes single-plan scenarios; no scenario tests N≥4 plans. The plan's verification didn't exercise this composition.

**Severity: Warning.** Pre-existing latent bug (the CLI's 5min cap has been there since v0.3.2). v0.3.4's per-plan budget makes it BITE: previously, a single ctx covered all plans (no per-plan fairness possible by design). Now there IS a per-plan budget, but it only takes effect for the first 3 plans.

**Suggested defense:**

Either:

(a) **Derive the outer ctx from N × per-plan + slack.** In `cmd/tribunal/chain.go`, before SyncAll, count the plans in the ledger and use `context.WithTimeout(ctx, time.Duration(len(plans)) * perPlanSyncBudget + 60*time.Second)`. The outer ctx scales with the workload.

(b) **Remove the outer cap entirely for SyncAll.** SyncAll's per-plan WithTimeout is now the binding constraint; the outer ctx can be `context.Background()`. SIGINT still works for operator-initiated cancellation. Cleanest.

(c) **Document the limitation.** If neither (a) nor (b) is acceptable, the CHANGELOG should say "v0.3.4's per-plan budget interacts with the CLI's 5-minute outer cap: ledgers with >3 slow plans will see truncated budgets for plans 4+." This is unsatisfying but honest.

**My recommendation: (b) for v0.3.5.** The per-plan budget IS the right binding constraint; the outer cap was a pre-v0.3.4 artifact.

---

## Cross-corpus blind spots

Three patterns across the v0.3.4 trio (separate from the v0.3.2 and v0.3.3 blind spots noted in prior reports):

### CB-1: "The pivot worked at the type level, so trust is fine"

The lens reviewers (especially arch) celebrated the type-level match between the structured query's input domain and the contract's storage map. Type-level matching is a legitimate convergence claim for the REGEX-narrowness defect class. It is NOT a convergence claim for the broader trust-source defect family.

Specifically, arch's reviewer-arch.md:24 says "the input domain match is type-level." This is true. It is also unrelated to whether the LCD can lie about which keys exist in that storage map. The type-level claim and the trust-level claim are orthogonal; the trio collapsed them.

The training-corpus pattern: type-level correctness IS sufficient correctness for systems where the data source is the same process or has structural integrity guarantees (Merkle-checked, signed, etc.). It is NOT sufficient for systems where the data source is over an untrusted network. The LCD is an untrusted network source. The reviewers wrote in the type-checked-call-graph idiom they were trained on.

### CB-2: "We learned the lesson at the wrong granularity"

Sec correctly identified that v0.3.4's structured-query primitive trusts the LCD. Sec correctly named the defect class. Sec did NOT generalize the lesson to: **every read from the LCD has the same trust posture**. The success-path preflight (F-NEW-501), the WaitForTx polling (F-SEC-305-carryforward), the `Reputation()` query for leaderboard display, the `ContractConfig()` query at startup — all consume LCD JSON responses with no cross-source verification.

The trio learns "the recovery preflight needs hardening." The generalized lesson — "ANY LCD-driven decision needs cross-source verification" — is not derived.

The training-corpus pattern: lens reviewers focus on the diff. v0.3.4's diff is the recovery primitive. The lesson scope matches the diff scope. The lesson SHOULD scope to the architectural pattern.

### CB-3: "What was deleted doesn't matter"

This is the most subtle. The trio analyzed what v0.3.4 ADDED (structured-query, per-plan ctx, maxRecoveryAttempts, preflight_concurrency). The trio did NOT analyze what v0.3.4 LOST in the deletion (the regex's per-finding-ID extraction, which was both a security hole AND a signal surface — see F-NEW-503).

When a deletion is correct for security (regex was attacker-controllable), the deletion is correct. But the deletion can also remove DEFENSIVE behaviors that weren't appreciated as defensive — in this case, the per-finding log. Removing a vulnerable feature also removes whatever non-vulnerable benefits the feature had.

The training-corpus pattern: lens reviewers diff-grep for what's added. The deletions get a one-line "yes, the dangerous thing is gone." The fact that the dangerous thing was ALSO doing something useful (logging) is not surfaced because the lens didn't ask.

---

## Things I attacked and did NOT find

Beyond what the trio caught and what's above, I probed and confirmed safe or already-covered:

1. **TOCTOU between success preflight and Execute.** If another operator commits a finding between our preflight (T0) and our Execute (T1), the contract rejects on duplicate, recovery preflight at T2 sees it as committed, filter drops, retry succeeds. Bounded by maxRecoveryAttempts. No new defect.

2. **`maxRecoveryAttempts = 5` interaction with operator-retry.** A hostile-LCD attack that triggers F-SEC-401 / F-NEW-501 doesn't amplify gas at the within-invocation level (the cap holds) but does require operator-retry to "recover," and each operator-retry is bounded by 5 attempts. So the attack is bounded by `operator_persistence × 5`. The cap is the right magnitude; the attack budget is the operator's tolerance.

3. **`filtered := commits[:0]` slice aliasing.** F-ARCH-302 from P-v033-audit flagged this. v0.3.4 keeps the same pattern. The aliasing is safe because range captures `c` by value before append writes; len(filtered) ≤ i+1 at all times. Confirmed safe (re-verified by trace through the loop body). Trio perf flagged as cross-reviewer note; not refiling.

4. **Recovery loop exit when `len(filtered) == len(commits)` followed by next iteration.** When recovery filters nothing and bails (line 373-376), it returns IMMEDIATELY. No risk of looping with original batch.

5. **Composition between F-NEW-401 (per-plan ctx) and F-NEW-302 (lost txhash) v0.3.2 carryforward.** Per-plan ctx cancels; Execute returns `(br, err)` with non-nil br. `submitCommitBatch` does `if err == nil { return br }` (line 357) and falls through to recovery on err. The non-nil br on the err path is discarded at line 357-358 (the `if err == nil` only returns on success, the err path falls through). On the err path the txhash is NOT propagated. **This IS a refinement of F-NEW-302** but at the submitCommitBatch layer rather than Execute. Filed as adjacent to F-SEC-305-carryforward; not a new finding because sec already covered the broader pattern.

6. **`PreflightConcurrency` interactions with `maxRecoveryAttempts`.** An operator who sets `preflight_concurrency: 1` (serial) reduces parallelism. At N=100 findings, preflight takes ~300s (100 × 3s per-query worst case). The per-plan 90s budget kills the sync. Operator self-DoS. F-PERF-402 / F-SEC-403 already cover the upper bound; lower bound (1) is a different but related foot-gun. Filed as adjacent to F-PERF-402; not a new finding.

7. **`looksLikeTestChain` against the empty string.** Empty string → strings.Split returns `[""]` → no token matches either set → returns false → treated as PROD. Sec already covered with 11-case test. Confirmed.

8. **What if the LCD returns a finding with `resolution != nil` for an UN-resolved finding?** The preflight worker (sync.go:276) sets `resolved=true` based on `resp.Finding.Resolution != nil`. A hostile LCD lying with `{"data":{"finding":{"resolution":{}}}}` marks the finding as resolved. Effect: success-path resolutions filter (line 187) skips THE finding's resolution → operator's resolution is never settled. Mirror of F-NEW-501 for the resolution side. Filed under F-NEW-501's umbrella (same defect, same defense).

9. **Cross-plan finding_id collision via dedup.** `seenCommit` / `seenResolve` are scoped per-call to SyncPlan. Same plan_id per call. No cross-plan collision possible. Confirmed safe.

10. **Account-sequence between recovery retries.** Each Execute call inside the recovery loop is a fresh `xiond tx wasm execute` invocation. xiond fetches the operator's sequence at each broadcast. The recovery loop's earlier Execute landed (broadcast-mode sync confirms mempool), so the next Execute uses sequence N+1. No sequence-collision race. Confirmed safe (re-verified from v0.3.3 audit).

---

## Verdict

**ESCALATE — concur with sec, against arch and perf.**

The convergence outcome under intent.md's three-way classification is unambiguously **#2: Pivoted but not converged**.

The trio's verdicts (Approve / Request Changes / Approve) split because arch and perf operated under a narrow definition of "same defect class" while sec operated under the load-bearing definition. Under arch's narrow definition (regex-narrowness), v0.3.4 converged. Under the load-bearing definition (does each iteration shrink the total defect surface in the named family?), v0.3.4 did NOT converge. The intent.md text itself supports sec's framing: "Pivoted but not converged. A Critical in a different defect class."

The trio's bundle is necessary but not sufficient. The new findings:

- **F-NEW-501 (Critical):** the silent-suppression vulnerability sec identified on the RECOVERY path also exists on the SUCCESS path, has been latent since v0.3.2, is strictly cheaper for the attacker (zero broadcasts, zero stderr noise), and is the load-bearing finding the trio missed.
- **F-NEW-502 (Warning):** `(nil, 0, nil)` is overloaded across three semantically-distinct cases; SyncResult has no way to distinguish them.
- **F-NEW-503 (Warning):** v0.3.4's deletion of the regex removed defensive per-finding observability in the recovery log.
- **F-NEW-504 (Suggestion):** the new recovery primitive ships with zero unit-test coverage; the load-bearing pivot is asserted only by the integration smoke test.
- **F-NEW-505 (Architectural recommendation):** the methodology is converging on specific shapes but not on defect families; v0.3.5 must address the LCD-as-truth-oracle family architecturally (ABCI-proof reads in v0.4 roadmap) rather than patching another instance.
- **F-NEW-506 (Warning):** the CLI's outer 5-minute ctx is too tight for the per-plan 90s budget when N ≥ 4 plans — pre-existing constraint that v0.3.4's per-plan budget exposes.

**Blocking findings for v0.3.5:**

1. F-SEC-401 (sec, Critical) AND F-NEW-501 (adversary, Critical) — both close with Defense (a) + (c). Ship.
2. F-NEW-502 (Warning) — adds a sentinel that makes hostile-suppression detectable even after Defense (a). Ship.
3. F-NEW-503 (Warning) — re-introduce per-finding observability in the recovery log. Ship.
4. F-NEW-504 (Suggestion) — write the unit tests for submitCommitBatch / submitResolveBatch. Ship.
5. F-NEW-506 (Warning) — fix the outer ctx (option (b): remove the 5min cap; the per-plan budget is the binding constraint). Ship.

**Defer-able:**

- F-NEW-505 (architectural) — ADR the trust-model shift to ABCI-proof reads. v0.4 work.
- F-ARCH-402 (per-plan budget tight) — re-litigate after F-NEW-506 is fixed; the per-plan budget tightness becomes a different question once the outer ctx is removed.
- F-PERF-401/402/403 (UX gaps) — independent cleanups.

**The most important thing the trio missed:** **F-NEW-501.** The success-path silent suppression has been exploitable since v0.3.2 across THREE audit cycles without surfacing. The trio's coverage has been thorough but each cycle has examined only the NEW code in that diff. The success-path preflight is older code; it survived three audits because the diff didn't touch it. **The lesson: the diff-bounded scope of lens reviewers is exactly the gap adversarial review is designed to fill, and that gap can hide critical-grade vulnerabilities for multiple iterations.**

**What made the trio's coverage airtight (this cycle):** the token-aware `looksLikeTestChain` heuristic (sec exhaustively walked 29 adversarial chain IDs and confirmed no bypass), the recovery loop's termination guarantees (arch traced the bound under all paths), and the goroutine lifecycle of the parallel preflight (perf walked every termination path). On those three surfaces, the trio's coverage is genuinely airtight.

The methodology, on aggregate, is doing what it was designed to do. The defect surface is moving from specific to architectural. Each cycle's findings are MORE architecturally significant than the prior cycle's. The trio's role is to be exhaustive within the diff; the adversary's role is to climb the call graph and check assumptions. Both roles produced their findings this cycle. The split verdict between trio reviewers is itself a signal that the architecture is at a transition point — between the regex-narrowness defect family (closed) and the trust-source defect family (open).

v0.3.4 ships after F-NEW-501 / F-SEC-401 is closed. If the close is Defense (a) + (c), v0.3.5 will surface a more sophisticated forgery attack — still in the same family. If the close is Defense (c) (ABCI proofs), v0.3.5 audit will find the next family entirely. Methodology trajectory: clear.

---

## FINDINGS-TO-FILE

```
critical|trust-boundary|F-NEW-501|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v034-audit/adversary.md#f-new-501|Silent-suppression via hostile LCD exists on the success-path preflight not just recovery; attacker doesn't need to trigger Execute failure and the attack is observationally identical to a normal idempotent re-sync
warning|refinement-mismatch|F-NEW-502|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v034-audit/adversary.md#f-new-502|submitCommitBatch returns (nil, 0, nil) for legitimate empty-batch AND recovery-filter-to-empty AND hostile-suppression cases; SyncResult has no field to distinguish them
warning|refinement-mismatch|F-NEW-503|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v034-audit/adversary.md#f-new-503|Deletion of recovery regex removed per-finding-ID observability from the recovery log; v0.3.4 emits a count where v0.3.3 emitted specific finding IDs
suggestion|test-debt|F-NEW-504|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v034-audit/adversary.md#f-new-504|Zero unit-test coverage for the new structured-query recovery primitive — the load-bearing pivot of v0.3.4 ships untested; perf flagged in cross-reviewer notes but it fell in the inter-lens gap
suggestion|architecture-meta|F-NEW-505|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v034-audit/adversary.md#f-new-505|Methodology is converging on specific defect shapes but not on defect families; v0.3.5 must address the LCD-as-truth-oracle family architecturally (ABCI-proof reads) rather than patching another instance within it
warning|composition|F-NEW-506|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-v034-audit/adversary.md#f-new-506|CLI's outer 5-minute ctx is unchanged from v0.3.2 but too tight for v0.3.4's per-plan 90s budget when N >= 4 plans; per-plan fairness invariant breaks at plan 4
```
