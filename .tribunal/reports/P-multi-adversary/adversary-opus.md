# Adversary Report — Tribunal v0.3.4 (multi-adversary panel: opus variant)

**Adversary:** `tribunal-adversary` (claude-opus-4-7)
**Plan:** `P-multi-adversary` (methodology experiment; not gated on a release)
**Diff under review:** `fb37c3c^..fb37c3c` (v0.3.4 fix release)
**Verdict:** **BREAKS** (one Critical that escalates F-SEC-401's blast radius; two new Serious findings the trio missed; one Critical re-classification on the convergence question)

---

## Summary

The trio's verdicts split cleanly along lens lines: arch says Approve / converged, perf says Approve / converged, sec says Request Changes / pivoted-but-not-converged with F-SEC-401 Critical (hostile-LCD recovery suppresses entire batch). The arch and perf reviewers' reasoning is internally consistent inside their lenses but they each treated F-SEC-401 as out-of-scope; only the sec reviewer interrogated the LCD trust model.

I think the sec reviewer is **directionally right** but **mis-categorizes** the defect shape. F-SEC-401 is not "F-NEW-403's recursion one layer deeper" — F-NEW-403 was an _input-domain-vs-truth-grammar_ mismatch (regex narrower than contract identifiers), which is structurally absent in v0.3.4. F-SEC-401 is a **trust-source-vs-truth-source mismatch** (LCD trusted as oracle for state the contract owns). These are different defect classes. The recursion P-v033-audit named is genuinely closed; what v0.3.4 exposes is an _orthogonal, pre-existing_ defect that v0.3.4 amplified rather than introduced.

That distinction matters for the convergence experiment. The methodology DID converge on the regex-grammar narrowness class. It did NOT yet reach zero-Critical equilibrium because each release has surfaced a Critical in a _new_ class (v0.3.2: contract-call narrowness; v0.3.3: regex-grammar narrowness; v0.3.4: LCD-trust source mismatch). That's progress, not divergence — but it's also not the "converged" outcome the arch lens claims.

My own attack surface beyond the trio:

1. **F-OPUS-001 (Critical, shared_blind_spot)** — F-SEC-401's blast radius is _larger than the sec reviewer named_. The hostile-LCD suppression vector activates on the **success-path preflight** (`sync.go:133`), not just on recovery. A hostile LCD that returns `{"data":{"finding":{}}}` for every `Finding` query causes `submitCommitBatch` to never even be called — line 201's `if len(commits) > 0` short-circuits to false. This attack pre-existed v0.3.4 (the success-path preflight has been there since v0.3.3 at least) and v0.3.4 inherited it; the sec reviewer's F-SEC-401 narrative focuses on the recovery path, missing that the simpler attack is one preflight pass earlier. F-SEC-401 is real but understates the surface.

2. **F-OPUS-002 (Serious, hidden_assumption)** — The CLI's outer ctx is `5 * time.Minute` (`cmd/tribunal/chain.go:201`) and `perPlanSyncBudget = 90 * time.Second`. With ≥4 plans in the ledger, the per-plan budget is no longer the binding constraint — the outer 5m ctx is. Plans 4+ get truncated time. The arch reviewer analyzed the per-plan budget against the worst-case recovery loop but did not analyze the OUTER ctx vs the per-plan budget; the relationship between them isn't derived from any consistent calculation. An operator with 20 plans expects ~30 minutes of patience, gets 5.

3. **F-OPUS-003 (Serious, refinement_mismatch)** — No client-side batch chunking against `MAX_BATCH_SIZE = 100` (`contracts/tribunal-reputation/src/validate.rs:74`). SyncPlan builds `commits` from every finding in the plan, no chunking step. A plan with >100 findings hits the contract's `BatchTooLarge` error on every sync, and the new recovery layer can't help (preflight returns no committed findings for fresh entries, so `len(filtered) == len(commits)` triggers immediate bail-out). The arch reviewer's calibration argument ("each retry drops at least one entry") implicitly assumes the batch is ≤100; the contract's cap is named in the perf review but not connected to the absence of chunking. Pre-existing latent issue, but the v0.3.4 recovery loop's _failure mode_ for >100 batches is exactly the "exhausted recovery attempts" path the cap was built to bound — and now operators will see it on a configuration error rather than on a network attack.

4. **F-OPUS-004 (Serious, adversarial_input)** — `looksLikeTestChain` is Unicode-bypass-able. A chain id like `MAİNNET-fork` (Turkish capital I with dot, codepoint U+0130) lowercases under Go's `strings.ToLower` to `mai̇nnet-fork`, which does NOT match the literal `mainnet` token. Combined with `test` somewhere in the id (e.g., `MAİNNET-test-fork`), the heuristic returns `true` (classified as test) — `tribunal-seed --send` would proceed without `--allow-prod` against a chain whose id looks unambiguously like mainnet to a human reading the YAML. The sec reviewer's 29-pattern table is solid for ASCII inputs but doesn't include non-ASCII confusables. Severity is Serious because chain ids are typically operator-controlled — but in a multi-tenant environment (publicly-listed chains where the chain operator chose the id), a hostile chain operator could exploit this to fool a tribunal-seed user.

5. **F-OPUS-005 (Serious, shared_blind_spot)** — The recovery loop's exhaustion error (`sync.go:381`, `:419`) reports `"commit batch exhausted recovery attempts (cap=5)"` with **no information about which findings were dropped, which remain, or what the underlying contract error was**. The per-attempt logs at lines 377-378 / 415-416 print dropped-counts but don't accumulate into the terminal error. After exhaustion, the operator can't tell:
   - Whether ANY findings landed (`FindingsSent` is hard-coded to 0 in the exhaustion branch, which is wrong if iter 1's broadcast actually committed some entries and only the recovery loop spun on a different failure)
   - Which finding IDs are still uncommitted
   - What the contract was actually complaining about (the wrapped `err` from `s.Client.Execute` is discarded in the exhaustion branch — note `return nil, 0, fmt.Errorf(...)` does NOT wrap err)

   The arch reviewer's F-ARCH-404 narrows the docstring; that doesn't fix the observability hole. Severity Serious: under sustained recovery exhaustion, the operator has zero forensic data to act on.

6. **F-OPUS-006 (Suggestion, edge_case)** — `submitCommitBatch` returns `FindingsSent=0` on the "everything was already on-chain" silent-success path (`sync.go:351-352`). This is OBSERVATIONALLY INDISTINGUISHABLE from the F-SEC-401 attack outcome. Even with strict response validation (sec's defense (a)), the legitimate idempotent re-sync case and the attack case produce identical CLI output: `plan=X findings=0 ... commit_tx= resolve_tx=`. The sec reviewer's defense (d) proposed a loud failure when `len(filtered) == 0 && len(commits) > 0` — but that's the RECOVERY path, not the success-path-everything-was-on-chain path which short-circuits earlier in `SyncPlan` (line 201). Defense (d) doesn't close the observability hole for the simpler attack I'm raising as F-OPUS-001.

---

## Trio triage

| Trio finding                                                              | My assessment                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| ------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| F-ARCH-401 (Warning) — recovery preflight LCD-sensitivity                 | **Confirmed but undersold.** The arch reviewer frames this as "operator friction under partial-LCD-blip," which is correct for non-hostile LCDs. But the same code path is the attack surface for F-SEC-401 (and my F-OPUS-001). The defect isn't "LCD-availability dependence"; it's "LCD-trust dependence under any failure mode." Re-categorizing as Serious.                                                                                                                                                                                               |
| F-ARCH-402 (Warning) — 90s per-plan budget too tight against degraded LCD | **Confirmed, severity calibrated.** The math is right; the budget doesn't survive worst-case recovery. Worth noting that the perf reviewer's typical-case math says 82s vs 90s budget (8s slack) — that's optimistic. The CLI's 5min outer ctx is the binding constraint for ≥4 plans (see my F-OPUS-002).                                                                                                                                                                                                                                                     |
| F-ARCH-403 / F-ARCH-404 / F-ARCH-405 (Suggestions)                        | All correct and fine to defer. F-ARCH-405's duplication concern compounds with my F-OPUS-004 — the Unicode bypass exists in BOTH copies of the heuristic; if dedup happens, fix the bypass in one place.                                                                                                                                                                                                                                                                                                                                                       |
| F-SEC-401 (Critical) — hostile LCD suppression via recovery preflight     | **Confirmed, but the attack surface is wider than named.** See F-OPUS-001: the success-path preflight has the same vulnerability and pre-existed v0.3.4. The defense recommendation (cross-source verification) is structurally right but understates that response validation alone (defense (a)) leaves the silent-success observable indistinguishable from idempotent re-sync (see F-OPUS-006). v0.3.5 needs both defense (a) AND defense (d) at minimum, AND the same defense applied to the success-path preflight call site, not just the recovery one. |
| F-SEC-402 (Warning) — no field validation on FindingResp                  | **Confirmed.** Defense is cheap; should ship.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| F-SEC-403 / F-SEC-404 (Suggestions)                                       | Confirmed.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| F-SEC-205-carryfwd-v034 — silent `tcp://` → `http://` rewrite             | **Confirmed, compound impact noted.** Worth raising the severity: combined with F-SEC-401/F-OPUS-001, plaintext HTTP makes the hostile-LCD attack reachable via passive MITM with no LCD compromise.                                                                                                                                                                                                                                                                                                                                                           |
| F-PERF-401/402/403                                                        | Confirmed Suggestions. The math in the perf report is sound.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |

**Cross-lens contradiction:** the arch verdict is "Approve / Converged"; the sec verdict is "Request Changes / Pivoted but not converged"; the perf verdict is "Approve / Converged." Arch and perf are framing the LCD-trust hole as out-of-lens, which technically respects role boundaries but means the trio's _consensus_ approval is misleading. If you read only the arch + perf reports, v0.3.4 looks ready to ship; the sec lens is doing all the load-bearing work on the actual blocking defect. **This is itself a methodology signal: the convergence question can only be answered correctly by averaging across lens reports, not by any single lens's verdict.**

---

## New findings

### F-OPUS-001 — Hostile-LCD success-path suppression is older and broader than F-SEC-401 names — Critical [shared_blind_spot]

**Files:** `internal/chain/sync.go:117-133` (success-path preflight call site), `:149` (commit-skip), `:170` (queue-commit-skip), `:187` (resolve-skip), `:201` (`if len(commits) > 0` short-circuit).

**Category:** `shared_blind_spot` — none of the three reviewers traced the attack model back to the success-path preflight. The sec reviewer's F-SEC-401 narrative starts at "operator submits a 50-finding batch... preflight returns clean," then describes the recovery path attack. But the cleaner attack is one step earlier: the hostile LCD lies on the SUCCESS-PATH preflight, and the recovery layer never even runs.

**Scenario:**

1. Operator runs `tribunal chain sync` against a plan with 50 ledger findings.
2. Hostile LCD intercepts the smart-query path at `internal/chain/sync.go:133` → `preflight(ctx, planID, ids)`. For each of the 50 `Finding(planID, F_i)` queries, returns `{"data":{"finding":{}}}` (or anything where `resp.Finding != nil`).
3. `preflight()` records `committed[F_i] = true` for every entry (`sync.go:276`).
4. SyncPlan's commit-build loop at line 141-161 hits `if committedOnChain[f.FindingID] { continue }` for every finding. **`commits` ends up empty.**
5. Same logic at line 162-174 for queued entries; same logic at line 179-199 for resolutions (via the `resolvedOnChain` map).
6. Line 201: `if len(commits) > 0 { ... }` — false, so `submitCommitBatch` is NEVER called. **The recovery primitive's "no entries on-chain → bail loudly" guard never fires because the recovery layer doesn't run.** Same for resolves at line 212.
7. SyncPlan returns `result` with `FindingsSent=0`, `ResolutionsSent=0`, no error, no tx hash.
8. CLI prints `plan=P findings=0 resolutions=0 ... commit_tx= resolve_tx=`. Exits 0.

**Why it succeeds:**

- The LCD response validation in `preflight()` is `if err != nil || resp == nil || resp.Finding == nil` (`sync.go:272`). Any non-empty JSON object passes. F-SEC-402 names this gap but only in the recovery context.
- F-SEC-401's described mitigation paths — strict response validation (defense (a)), local cross-check of claim*hash (defense (b)), ABCI proof verification (defense (c)) — are correct for closing my F-OPUS-001 \_too*, but only if applied at the **success-path call site** (`sync.go:133`), not only at the recovery call sites (`sync.go:366`, `:405`). The sec reviewer's defense narrative implicitly assumes the fix lives near where they filed it; the call graph has two consumers of `preflight()` and only one is referenced in defense narrative.
- This vulnerability pre-dates v0.3.4. `git show 5cc1634:internal/chain/sync.go` (v0.3.3) has the identical success-path preflight + skip-if-committed pattern. v0.3.4 INHERITED this; v0.3.4's contribution to the attack surface is making the SECOND-tier attack (recovery suppression) available even if the success-path attack misses. So F-SEC-401 + F-OPUS-001 together describe the full surface; v0.3.4 didn't introduce the deeper hole, but v0.3.4's framing as "audit-driven fix release for trust-boundary defects" suggests it should have closed both.

**Quoted text:**

`sync.go:149`: `if committedOnChain[f.FindingID] { continue }` — silently drops findings the LCD claimed are committed.
`sync.go:201`: `if len(commits) > 0 { br, sent, err := s.submitCommitBatch(...) }` — no else branch, no warning if commits is empty when the ledger had N>0 findings for the plan.

Intent.md §47 explicitly says: "LCD endpoint is untrusted. The structured-query recovery layer trusts it less than the regex did (no raw_log injection surface), but the trust posture is not zero. Reviewers should explicitly consider: what can a hostile LCD do via the structured query's response?" The trio considered the recovery-path consumption but not the success-path consumption.

**Severity rationale:** **Critical.** Same blast radius as F-SEC-401 (full-batch silent suppression, no error signal, observationally indistinguishable from idempotent re-sync), one step earlier in the call graph, and pre-dates v0.3.4. The fact that v0.3.4 didn't close it means the "audit-driven fix release" is incomplete.

**Suggested defense:** Apply F-SEC-402's response validation at BOTH success-path and recovery-path preflight call sites. Specifically: validate `resp.Finding.PlanID == planID`, `resp.Finding.FindingID == id`, `resp.Finding.AgentPubkey != ""`, and ideally cross-check `resp.Finding.ClaimHash` against the local commit's `ClaimHash` (which is what the sec reviewer's defense (b) does for the recovery path — extend it to the success path). Additionally: if `committedOnChain` ends up flagging ALL entries (full-batch suppression), emit a stderr warning analogous to defense (d) but at the success-path boundary.

---

### F-OPUS-002 — CLI 5-minute outer ctx misaligned with 90s per-plan budget; ≥4 plans guarantees truncation — Serious [hidden_assumption]

**Files:** `cmd/tribunal/chain.go:201` (5-minute outer ctx), `internal/chain/sync.go:67` (90s per-plan budget), `:463` (per-plan ctx derivation).

**Category:** `hidden_assumption` — the per-plan budget assumes the outer ctx allows AT LEAST `N_plans × 90s` of wallclock. The CLI hard-codes `5 * time.Minute = 300s`, which supports at most 3 plans at full per-plan budget. The arch reviewer's F-ARCH-402 analyzed the 90s budget against worst-case _per-plan_ recovery wallclock; nobody analyzed it against the multi-plan total.

**Scenario:**

1. Operator has a ledger with 10 plans, each with 50 findings, none yet on-chain.
2. CLI `tribunal chain sync` (no `--plan`) → `context.WithTimeout(context.Background(), 5*time.Minute)`.
3. `SyncAll(ctx, lg)` iterates plans in order. Each `SyncPlan` runs under `context.WithTimeout(ctx, 90s)`.
4. Per Go's `context.WithTimeout` semantics, the child ctx deadline is `min(parent.deadline, now + 90s)`. So plan 1 gets ~90s, plan 2 gets ~90s, plan 3 gets ~90s — at the 270s mark, the outer ctx has 30s left, so plan 4's `WithTimeout(ctx, 90s)` resolves to 30s, plan 5 gets 0s.
5. Plans 4-10 see `context deadline exceeded` errors. SyncAll's `errors.Join` aggregates them.
6. CLI prints partial results (plans 1-3 successful, per the v0.3.4 F-ARCH-303 fix) then the joined error.

**Why this matters for v0.3.4:** the 90s budget was sized for the worst-case recovery loop. The 5min outer ctx was set in an earlier version when there was no per-plan budget — when SyncAll was straight-line and didn't have explicit budget math. Now there are two constants set against different assumptions and they don't agree. With realistic ledgers (>3 plans), the per-plan budget is uniformly UNATTAINABLE for later plans.

The arch reviewer's F-ARCH-402 says the 90s budget should bump to 300s to match the recovery worst case. If they do, the 5min outer ctx supports ONE plan with full budget. The outer ctx and per-plan budget need to be related: outer ≥ N_plans × per-plan, with explicit slack.

**Quoted text:**

`cmd/tribunal/chain.go:201`: `ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)` — no comment explaining why 5m or how it relates to per-plan budget.
`internal/chain/sync.go:67`: `const perPlanSyncBudget = 90 * time.Second` — comment explains the budget for ONE plan against degraded LCD but doesn't say how many plans the system supports.

Plan §17 says "for any sequence of batch retries within `maxRecoveryAttempts`, the loop terminates because each iteration that doesn't succeed either reduces the batch size by ≥1 ... or fails for a non-duplicate reason." Termination is per-plan; the multi-plan budget isn't named.

**Severity rationale:** **Serious.** The system silently truncates work in the common case (multi-plan sync). The error message is informative (`context deadline exceeded` for the truncated plans), but the failure isn't visible to the calibration story the v0.3.4 design rests on. Not Critical because the data isn't lost (next sync run picks up where this one left off, modulo the queue-drain issue I flag below) — just operator-time-wasted.

**Suggested defense:** Either (a) make the CLI outer ctx derive from the per-plan budget: `time.Duration(N_plans) * perPlanSyncBudget + headroom`, computed AFTER reading the ledger; or (b) drop the outer ctx entirely and rely on per-plan ctx for all bounding; or (c) document the relationship explicitly with a calculation and a config knob. Minimum: bump the outer to `30 * time.Minute` and comment the calculation.

---

### F-OPUS-003 — No client-side batch chunking against contract's MAX_BATCH_SIZE=100 — Serious [refinement_mismatch]

**Files:** `internal/chain/sync.go:139-161` (commit build, no chunking), `:179-199` (resolve build, no chunking), `:201` and `:212` (single-batch submit calls). Contract: `contracts/tribunal-reputation/src/validate.rs:74` (`MAX_BATCH_SIZE: usize = 100`), `:80` (`BatchTooLarge` error).

**Category:** `refinement_mismatch` — the plan's invariant ("for any sequence of batch retries... each iteration that doesn't succeed either reduces the batch size by ≥1 or fails for a non-duplicate reason") implicitly assumes batches submitted to the contract are valid in size. The contract's batch-size cap is named in the perf review but the absence of client-side chunking is not connected to it.

**Scenario:**

1. Operator has a plan with 150 findings (a busy plan-close in a larger ledger).
2. SyncPlan builds `commits` from all 150 entries; no chunking.
3. `submitCommitBatch` calls `Execute` with a 150-element batch.
4. Contract's `validate_batch_size(150)` returns `BatchTooLarge { actual: 150, max: 100 }`.
5. `Execute` returns the wrapped error to the recovery loop.
6. Recovery preflight runs on all 150 findings — none are committed (this is the first sync). Returns empty `committed` map.
7. `len(filtered) == 150 == len(commits)` → bail-out at `sync.go:373` with `commit batch rejected and no entries already on-chain: batch contains 150 items; max allowed is 100`.

The bail-out fires loudly (good — F-ARCH-401's loud-failure property holds). But the underlying issue is that **Tribunal can't sync any plan with >100 findings**, ever, until someone adds chunking. v0.3.4's recovery loop doesn't help here — it can't, because the recovery primitive is designed to filter out duplicates, not chunk new entries.

**Quoted text:**

`internal/chain/sync.go:201`: `if len(commits) > 0 { br, sent, err := s.submitCommitBatch(ctx, planID, commits) }` — one batch, no slicing.
`contracts/tribunal-reputation/src/validate.rs:74-87`: `pub const MAX_BATCH_SIZE: usize = 100;` and `validate_batch_size` rejects with `BatchTooLarge` on >100.

The perf reviewer cites `MAX_BATCH_SIZE = 100` correctly at line 92 of their report and uses it as the upper bound on preflight cost: "the contract enforces `MAX_BATCH_SIZE = 100` ... so N>100 is impossible per call." That's misleading — `MAX_BATCH_SIZE` is a contract-side rejection, not a client-side validation. The Go client CAN and DOES build batches larger than 100; it just fails on submission.

**Severity rationale:** **Serious.** This is a real correctness gap — a plan with >100 findings cannot be settled. Not Critical because: (a) it fails LOUDLY (operator sees `BatchTooLarge` in the wrapped error), (b) operators with <100 findings/plan aren't affected, (c) the workaround is to split the plan or wait for chunking. But it's also not a Suggestion: the failure surface is real, it's not a degraded mode, the recovery loop literally cannot help, and the contract's batch cap was a deliberate gas-budget decision that the client never internalized.

**Suggested defense:** Add a chunking layer in SyncPlan: split `commits` into N slices of ≤100 entries, call `submitCommitBatch` per chunk, aggregate `FindingsSent` and tx hashes. Same for resolutions. Approximately 15 lines. The `SyncResult.CommitTxHash` field becomes a slice (or single-hash semantics get a comma-separated representation, but slice is cleaner).

---

### F-OPUS-004 — `looksLikeTestChain` bypassable via Unicode case-mapping (Turkish I and similar confusables) — Serious [adversarial_input]

**Files:** `internal/chain/client.go:62-78`, `cmd/tribunal-seed/main.go:129-145`.

**Category:** `adversarial_input` — the sec reviewer ran 29 ASCII-only adversarial chain ids and found no bypass. The Unicode confusable surface is not exercised.

**Scenario:**

A chain id containing `İ` (U+0130, Latin capital I with dot above, used in Turkish) lowercases under Go's `strings.ToLower` to a TWO-CODEPOINT sequence: `i` + `̇` (U+0307, combining dot above). The token comparison `case "mainnet", "main", "prod", "production"` operates on byte-equal strings; `mai̇nnet` (8 bytes / 7 codepoints) is NOT byte-equal to `mainnet` (7 bytes / 7 codepoints), so the production-marker check FAILS.

Concrete attack:

```
chain_id: xion-MAİNNET-test-fork
```

- `strings.ToLower(...)` → `xion-mai̇nnet-test-fork`
- `strings.Split("-")` → `["xion", "mai̇nnet", "test", "fork"]`
- First loop: no token matches `mainnet`/`main`/`prod`/`production`. Pass through.
- Second loop: `test` matches → return `true`.

Result: a chain id that visually reads as MAINNET to a human inspecting `~/.tribunal/chain.yaml` is classified as a TEST chain by the heuristic.

Consequences:

- `tribunal-seed --send` against this chain id succeeds WITHOUT `--allow-prod` (because looksLikeTestChain returns true, so the refusal at `main.go:100` doesn't fire).
- `chain.Client.New` doesn't emit the `keyring_backend=test` warning (because the chain looks like a test chain to the heuristic).

Verification: I traced through `strings.ToLower` semantics. Go's unicode package handles `İ → i̇` correctly per Unicode case-folding rules; the resulting NFC normalization is NOT applied automatically, so the byte representation has the combining mark.

Less exotic confusables that also bypass:

- `ＭAINNET` (U+FF2D fullwidth M) — lowercases to itself (fullwidth-M is its own lowercase), doesn't match `mainnet`.
- `mainnеt` (Cyrillic small е, U+0435) — already lowercase, doesn't match `mainnet`.

**Quoted text:**

`internal/chain/client.go:63-64`: `id := strings.ToLower(chainID); parts := strings.Split(id, "-")` — `strings.ToLower` is Unicode-aware; the subsequent `case` comparisons are byte-equal.

Plan reviewer assignment to `reviewer-sec`: "the token-aware heuristic against more adversarial chain-ID patterns than the 11 in the new test" (`plan.md:40`). The sec reviewer's 29-pattern table covered substring confusion, case insensitivity, mixed positions, and dotted/slashed separators — but explicitly only ASCII inputs.

**Severity rationale:** **Serious.** Chain ids are typically operator-controlled, so an attacker has to convince the operator to use a malicious chain id — which is a real attack vector in (a) multi-tenant chain hosting where the chain operator publishes the id, (b) copy-paste from a malicious Discord/Twitter post or compromised docs, (c) phishing-style swaps in a doc the operator follows verbatim. Not Critical because the operator has to type the chain id; the heuristic isn't fooled by attacker-provided runtime input. Severity Serious because the heuristic's WHOLE PURPOSE is to be a safety guard against operator mistakes, and a Unicode confusable defeats it silently.

**Suggested defense:** After lowercasing, validate the chain id against `[a-z0-9-]+` (or `[a-z0-9._-]+` if there are real production chain ids using `.`/`_`). If it contains non-ASCII, refuse to call it a test chain (fail-CLOSED — return false, which causes `tribunal-seed` to require `--allow-prod` and `chain.Client.New` to emit the warning). One line: `if !regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(id) { return false }`. Note this re-introduces a `regexp` import that v0.3.4 deliberately removed — alternative is a manual byte-range check.

---

### F-OPUS-005 — Recovery exhaustion error sheds underlying contract error and per-finding state — Serious [shared_blind_spot]

**Files:** `internal/chain/sync.go:381` (commit exhaustion), `:419` (resolve exhaustion).

**Category:** `shared_blind_spot` — the arch reviewer's F-ARCH-404 narrows the docstring; the perf reviewer's F-PERF-401 suggests adding cumulative elapsed to the recovery log lines. Neither names the actual forensic loss: at exhaustion, the FINAL error has no `errors.Wrap` of the underlying contract error and no list of which finding IDs were filtered vs. retained.

**Scenario:**

1. SyncPlan submits a 50-finding batch.
2. Iter 1: contract rejects with `FindingAlreadyCommitted {plan_id, finding_id: F-007}`. Recovery preflight finds F-007 + 4 others (genuine prior commits). Filter drops 5; retry with 45.
3. Iter 2: contract rejects with `InsufficientStake`. Recovery preflight: same 5 still committed. Filter: same 45 entries. `len(filtered) == len(commits)` → bail with `"commit batch rejected and no entries already on-chain: tx broadcast failed (code=5): insufficient stake balance: agent has 100, finding requires 200"`. **Returns from line 375**, not from the exhaustion path at line 381.

OK actually that branch DOES propagate the underlying err via `%w`. Let me re-check the EXHAUSTION path:

```go
return nil, 0, fmt.Errorf("commit batch exhausted recovery attempts (cap=%d)", maxRecoveryAttempts)
```

Line 381. This fires after 5 iterations that EACH SUCCESSFULLY DROPPED entries (so the bail-out at line 373-376 didn't trigger). Five consecutive iterations where each iteration sees 1+ new duplicates. The `err` from the most recent `s.Client.Execute` is NOT wrapped in this return. **The operator sees "exhausted recovery attempts" with no clue what the contract was saying.**

Iteration log lines (`tribunal: commit batch recovered via state query, dropped K already-committed, retrying with M findings`) DO go to stderr, but they're not aggregated in the error. If the operator is running `tribunal chain sync` from CI / a script that only captures the stdout/return-code, the per-attempt context is lost. Even the FINAL contract error (which would be the most diagnostic) is discarded.

Additionally: `FindingsSent` is hard-coded to 0 in the exhaustion path. If iter 1's broadcast actually committed some entries (the contract's commit_finding_batch is atomic — but suppose iter 1's batch had 50 entries, contract committed 5 entries' worth of work BEFORE hitting `FindingAlreadyCommitted` on entry 6, which rolled back all 50 — wait that's atomic, OK), the final `FindingsSent=0` is technically right for the exhaustion case. But if the recovery loop's iter 2-5 DID land actual commits (which they shouldn't — exhaustion means none succeeded), the count is wrong. Not a bug in the current code shape (the loop only returns success at line 357 with `return br, len(commits), nil`), but fragile to future changes.

**Why it succeeds (per quoted text):**

`sync.go:381`: `return nil, 0, fmt.Errorf("commit batch exhausted recovery attempts (cap=%d)", maxRecoveryAttempts)` — no `%w`, no err parameter.
`sync.go:419`: same shape for resolve.

Plan §35 specifically asks: "Does `maxRecoveryAttempts = 5` interact with the structured query in a way that creates a new gas-amplification or starvation pattern?" The starvation case is exhaustion; the audit asks for adversarial-input testing of that path. Neither the trio nor I can find a starvation that drives all 5 iterations without one bailing out — but if such a pattern exists (say, the LCD reports one committed finding per iteration that's actually DIFFERENT from the prior iteration's claim, causing infinite-feeling progression), the error message gives the operator nothing.

**Severity rationale:** **Serious.** Observability gap at the precise moment the operator most needs information. Not Critical because the underlying state is recoverable (the operator can re-run sync and the success-path preflight will tell them what's on-chain). But operationally, this is the difference between "I know what to fix" and "I have to dig through stderr scrollback to reconstruct what happened."

**Suggested defense:** Track `lastExecuteErr error` and the cumulative dropped-list across iterations, and include both in the exhaustion error:

```go
return nil, 0, fmt.Errorf("commit batch exhausted recovery attempts (cap=%d, last_contract_err=%w, total_dropped=%d, remaining=%d)",
    maxRecoveryAttempts, lastExecuteErr, totalDropped, len(commits))
```

Plus return a list of remaining-uncommitted finding IDs in the SyncResult or via a typed error.

---

### F-OPUS-006 — Silent-success (`FindingsSent=0`) is observationally indistinguishable from F-SEC-401/F-OPUS-001 attack outcome — Suggestion [composition_failure]

**Files:** `internal/chain/sync.go:350-352` (recovery "all already on-chain"), `:201` (success-path empty-commits short-circuit), `cmd/tribunal/chain.go:235-237` (`printSyncResult`).

**Category:** `composition_failure` — F-SEC-401/F-OPUS-001 attack outcome and idempotent re-sync (legitimate happy case) compose into identical CLI output. The sec reviewer's defense (d) addresses the recovery-path silent suppression; it doesn't address the success-path one. Even with defense (d) and defense (a) shipped, the success-path attack (F-OPUS-001) still presents as `findings=0 ... commit_tx= resolve_tx=`, which is exactly what an idempotent re-sync looks like.

**Scenario:**

Two scenarios produce identical CLI output:

- (LEGITIMATE) Operator ran sync yesterday; all 50 findings committed. Today: idempotent re-sync. Preflight returns all 50 committed. Commits/resolutions filtered to empty. SyncPlan returns clean. CLI: `plan=P findings=0 resolutions=0 ... commit_tx= resolve_tx=`.
- (ATTACK) Hostile LCD returns `{finding:{}}` for all 50 queries. Preflight returns committed = full set. Commits/resolutions filtered to empty. SyncPlan returns clean. CLI: `plan=P findings=0 resolutions=0 ... commit_tx= resolve_tx=`.

The operator can distinguish these only by an out-of-band query against a DIFFERENT LCD (or by checking the on-chain state via a Tendermint RPC `/abci_query` with proof). Without that, the attacker's lie is indistinguishable from the legitimate success case.

**Why it succeeds (per quoted text):**

`cmd/tribunal/chain.go:236-237`: `printSyncResult` prints `findings=%d resolutions=%d` — these are POST-FILTER counts. The PRE-FILTER count (how many ledger entries existed) is not exposed in the output.

`sync.go:108`: `result.QueueDrainedCount = len(drained)` — queue-drained count IS in `SyncResult`, so the schema can carry a pre-filter count. The schema just doesn't have one for findings/resolutions yet.

**Severity rationale:** **Suggestion** (not Serious) because:

- Detection is possible (operator can compare ledger-count vs `findings` count).
- Mitigation is incremental — even partial defenses against F-SEC-401 mitigate this too.
- This is a composition issue at the CLI output level, not a primitive-level defect.

But the FIX is cheap and closes the silent-suppression detection gap independent of whether F-SEC-401 is closed:

**Suggested defense:** In `SyncResult`, add `FindingsPreflighted int` (count of findings in the ledger for this plan) and `FindingsSkipped int` (count filtered as already-on-chain). Print both in `printSyncResult`. The legitimate idempotent case shows `preflighted=50 skipped=50 findings=0`; the attack case shows the same — UNLESS the operator looks at the local ledger count and sees the discrepancy with what the LCD claims. Combine with a flag like `--strict` that exits non-zero if `findings=0 && preflighted > 0 && commit_tx==""` to make the silent-suppression detectable in CI.

---

## Cross-corpus blind spots

Things that would be visible to a reviewer who is NOT trained on this codebase's conventions:

1. **The `Sync.Queue` field is wired into the data path (drained on every SyncPlan call) but `CommitRealtime` has no callers in the binary** (grep `\.CommitRealtime\b` across `cmd/` and `internal/` returns nothing). The Queue is effectively dead code today — `s.Queue != nil` checks in SyncPlan are protecting against a code path that doesn't fire. This isn't a v0.3.4 issue, but the trio universally accepted the queue plumbing as load-bearing. A fresh reviewer would notice the disconnect. **Not a finding** (no actual correctness issue), but a methodology note: the codebase has cargo-cult plumbing the trio doesn't interrogate.

2. **The contract's error grammar (`error.rs`) includes errors that v0.3.4's recovery layer correctly DOESN'T try to handle**: `AgentNotRegistered`, `AgentRetired`, `InvalidSignature`, `InvalidSeverity`, `InvalidRole`, `InsufficientStake`, `InvalidIdentifier`, `BatchMixedPlanID`, `EmptyBatch`, `InvalidInitialBalance`, `InvalidPubkeyLength`, `InvalidRotationSource`, `InvalidRotationTarget`. The recovery layer's bail-out fires for all of these (preflight returns 0 already-committed entries → `len(filtered) == len(commits)` → bail with the wrapped error). The intent.md §32 explicitly asks this question; the arch reviewer addresses it briefly in the F-ARCH-401 analysis ("recovery requires the LCD to be healthy") but doesn't enumerate the contract error grammar against the recovery's behavior. **My enumeration**: every non-duplicate contract error path produces a loud, wrapped error. The recovery layer is well-behaved for non-duplicate failures. Not a finding; just confirming the audit's intent question with the actual error grammar.

3. **The `BatchMixedPlanID` error specifically catches a class of bugs the client can't currently produce** — SyncPlan builds commits filtered to `if f.PlanID != planID { continue }`. So all commits in a batch share planID. Defense in depth on the contract side; client-side this can't fire. OK.

4. **`canonical_finding_message` includes the stake in the signed payload** — so an operator can't sign a finding for stake=100 and submit it with stake=200 (the signature wouldn't verify). v0.3.4 doesn't touch this; just noting it's a property the recovery layer doesn't undermine.

5. **The `Severity::parse` on the contract side** (commit.rs:70) returns `InvalidSeverity` if the wire-format severity doesn't parse. The Go client's `BuildFindingCommit` doesn't validate severity values against the contract's allowed set (`critical`, `warning`, `suggestion`) — it just passes `string(f.Severity)` through. If a future change to `ledger.Severity` adds a new value, the contract rejects and the recovery layer bails with the contract error. Defense-in-depth opportunity; not a v0.3.4 issue.

6. **The `commits[:0]` aliasing in recovery (sync.go:367, :406)** — F-ARCH-302 from P-v033 flagged this. The perf reviewer notes it persists in v0.3.4 and is "still safe in the current code shape." I verified the same: in the inner `for _, c := range commits` loop, the range variable `c` captures by value before any `append(filtered, c)` writes to the aliased backing array, so per-iteration the read precedes the write. SAFE today. Latent if anyone refactors the loop to iterate by index instead of range, or if `commits` is passed by pointer and re-aliased elsewhere. Trio is correct to not re-file as a Critical or Warning. Suggestion-level at most.

---

## Convergence verdict (the experiment's primary question)

Intent.md §65-69 asks for one of three outcomes:

- **Converged.** No new Critical; recursion is broken.
- **Pivoted but not converged.** A Critical in a DIFFERENT defect class.
- **Still iterating.** A Critical in the SAME recursion shape.

My answer: **Pivoted but not converged — in a different sense than the sec reviewer named.**

The sec reviewer says F-SEC-401 is the same defect shape as F-NEW-403 ("LCD trusted as oracle for what's on-chain"). I disagree. F-NEW-403 was a _grammar-narrowness_ defect (regex character class smaller than contract identifier grammar); F-SEC-401 is a _trust-source_ defect (LCD accepted as authoritative for state the contract owns). These have different fix shapes:

- Grammar-narrowness fix: stop parsing structured text against a too-strict pattern. Done in v0.3.4.
- Trust-source fix: cross-source verification or authenticated queries. Not done in v0.3.4.

The recursion P-v033-audit named IS broken. The methodology executed the pivot correctly. **But the audit ALSO surfaced a different defect class that was lurking at the layer below the pivot** — and that defect (LCD trust) is older than v0.3.4, was inherited rather than introduced, and gets WORSE (larger blast radius) under v0.3.4's recovery primitive.

So the framing I'd urge the convergence experiment to adopt:

- v0.3.4's specific recursion is closed. **The pivot worked.**
- v0.3.4 still surfaces a Critical (F-SEC-401 + my F-OPUS-001), but it's a Critical in a structurally-distinct defect class.
- Each iteration's findings should be classified by defect class. The methodology has converged on one class (grammar narrowness) and is now exposed to another (LCD trust). Working on v0.3.5 to close the new class is NOT recursion — it's normal multi-pass auditing where each pass deepens the threat model.
- **The methodology is NOT diverging.** The methodology IS reaching deeper into the trust model with each release. If v0.3.5 closes LCD-trust and v0.3.6 surfaces a NEW class (e.g., xiond binary integrity), that's still progress; the convergence metric to watch is "finding count over time within a defect class" not "any Critical across all classes."

That nuance is what I think the trio missed. The sec reviewer's verdict of "pivoted but not converged" is right on outcome but the reasoning conflates two defect classes. The arch reviewer's "converged" is also right on the SPECIFIC question (regex-grammar narrowness is dead) but understates that a fresh Critical exists.

---

## Final verdict

**BREAKS.**

Three drivers:

1. **F-OPUS-001 (Critical)** — F-SEC-401's blast radius is larger than the trio characterized; the success-path preflight is the simpler and pre-existing attack vector that v0.3.4 didn't close. Any v0.3.5 fix MUST apply at both call sites or it's just patching half the surface.
2. **F-OPUS-003 (Serious)** — the absence of client-side batch chunking is a real correctness gap for plans with >100 findings, and v0.3.4's recovery loop can't paper over it.
3. **F-OPUS-002 (Serious)** — the CLI's outer ctx silently truncates multi-plan syncs; the per-plan budget invariant is unattainable in the common case.

F-OPUS-004 (Unicode chain-id bypass) and F-OPUS-005 (exhaustion error sheds context) are Serious calibration gaps the trio's testing didn't exercise.

**Convergence answer:** pivoted-but-not-converged on outcome; but the defect class that remains open (LCD trust) is structurally different from the one P-v033 named, and that distinction matters for how v0.4 prioritizes fixes.

---

## META

- **Categories attacked:** `shared_blind_spot` (F-OPUS-001, F-OPUS-005), `hidden_assumption` (F-OPUS-002), `refinement_mismatch` (F-OPUS-003), `adversarial_input` (F-OPUS-004), `composition_failure` (F-OPUS-006).
- **Categories NOT attacked and why:**
  - `temporal_state_mismatch`: v0.3.4 doesn't introduce temporal properties; the convergence question is the main "temporal" angle and I addressed it above as a methodology question rather than a finding.
  - `contradiction`: I looked for diff-vs-plan, diff-vs-intent, diff-vs-tests contradictions. The diff is internally consistent and matches the plan; nothing surfaced.
  - `edge_case`: I covered specific edge cases as part of the other categories rather than filing a generic edge-case finding.
- **Artifacts I wanted but didn't have:**
  - An empirical xion-testnet-2 trace of a real recovery loop firing (the perf reviewer's table is from analysis, not measurement).
  - The v0.3.3 audit's adversary report — explicitly held back for the experiment, but I could've found a sharper convergence answer with it. Working without it, I had to reason about defect-class boundaries from scratch.
- **Confidence in verdict:** **High** for F-OPUS-001 (verified attack chain end-to-end against the actual code), F-OPUS-003 (verified by inspection — no chunking exists), F-OPUS-004 (verified Unicode case-mapping semantics). **Medium** for F-OPUS-002 (the calibration math is sound but I haven't tested CLI behavior with 10 plans empirically), F-OPUS-005 (forensic observability gap, opinion-shaded by what I think operators need). **High** on the convergence verdict.

---

## FINDINGS-TO-FILE

```
critical|trust-boundary|F-OPUS-001|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-multi-adversary/adversary-opus.md#f-opus-001|Success-path preflight (sync.go:133) accepts hostile LCD response as proof-of-commitment, suppressing entire batch before submitCommitBatch ever runs — wider and older attack surface than F-SEC-401's recovery-path narrative captures.
serious|architecture|F-OPUS-002|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-multi-adversary/adversary-opus.md#f-opus-002|CLI 5-minute outer ctx misaligned with 90s per-plan budget; ≥4 plans guarantees later plans get truncated time, undermining v0.3.4's per-plan isolation guarantee.
serious|correctness|F-OPUS-003|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-multi-adversary/adversary-opus.md#f-opus-003|No client-side chunking against contract MAX_BATCH_SIZE=100; plans with >100 findings cannot be synced, recovery loop cannot help, all entries fail with BatchTooLarge.
serious|trust-boundary|F-OPUS-004|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-multi-adversary/adversary-opus.md#f-opus-004|looksLikeTestChain bypassable via Unicode case-mapping; chain id "MAİNNET-test-fork" lowercases to a non-byte-equal "mai̇nnet" token, classified as test chain, defeats tribunal-seed mainnet guard.
serious|observability|F-OPUS-005|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-multi-adversary/adversary-opus.md#f-opus-005|Recovery exhaustion error (sync.go:381, :419) sheds underlying contract error and per-finding state; operator has zero forensic data at the exact moment they need it most.
suggestion|observability|F-OPUS-006|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-multi-adversary/adversary-opus.md#f-opus-006|Silent-success FindingsSent=0 path observationally indistinguishable from F-SEC-401/F-OPUS-001 attack outcome; CLI output gap survives even sec-defense (d).
```
