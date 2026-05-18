<!--
Adversary: qwen3-coder:latest (Alibaba) via ollama at 192.168.6.56:11434
Date: 2026-05-18
Model substitution note: the original P-multi-adversary intent specified
qwen3:32b dense. We substituted qwen3-coder:latest (30B-A3B MoE) after
observing that qwen3:32b dense + the 36k-token prompt doesn't fit in
the RTX 4090's 24GB VRAM on either ollama 0.15.2 or 0.23.2 (CPU-split
to 36/65 layers, ~25-45 min per run). qwen3-coder fits in ~24GB,
generated at 13.7 tok/s over 626s total. Still Alibaba family — H1
test (cross-family additive) remains valid. Slight contamination
because qwen3-coder is code-fine-tuned; we'll discuss this in
SYNTHESIS.md.

Performance:
  total_duration: 626.7s
  prompt_eval_count: 39759
  eval_count: 8192 (hit num_predict cap mid-finding 45)
  eval_rate: 13.72 tok/s
-->

VERDICT: PIVOTED BUT NOT CONVERGED (The structured-query primitive has a new trust boundary defect that is strictly worse than the v0.3.3 regex approach, as demonstrated by the Critical F-SEC-401 attack that allows a hostile LCD to suppress entire batches silently)

## 1. Category: **shared_blind_spot**
### Concrete scenario
The new structured-query recovery primitive in `submitCommitBatch` and `submitResolveBatch` trusts LCD responses as authoritative state-of-the-world, allowing a hostile LCD to suppress entire batches by returning empty `{"data":{"finding":{}}}` responses for every finding in a batch. This results in `FindingsSent=0` with no error, making the attack indistinguishable from a successful idempotent sync.

### Why it succeeds
The security review (reviewer-sec) identified this as **F-SEC-401 Critical**. The primitive's trust model changed from "parse LCD-sourced text" (v0.3.3) to "trust LCD smart-query responses" (v0.3.4). However, the new primitive's trust boundary is expanded: instead of parsing a potentially malicious `raw_log` string, it now accepts any non-nil `FindingResp.Finding` as proof of commitment. The attack scenario is detailed in the security report:

1. `preflight()` queries each finding and gets `{"data": null}` or absent `finding` field → `committedOnChain[F] = false` for all
2. `submitCommitBatch` calls `Execute` → LCD returns `{"tx_response":{"code":11,"raw_log":"out of gas"}}` 
3. Recovery loop calls `s.preflight(ctx, planID, ids)` with 50 findings
4. Hostile LCD responds to EACH query with `{"data": {"finding": {}}}` 
5. `preflight()` marks `committed[F_i] = true` for all 50 because `resp.Finding != nil`
6. Filter drops all 50 → `len(filtered) == 0` → `return nil, 0, nil` (silent success)
7. Operator sees clean exit with `FindingsSent=0` and no error

The key quote from the security report: "v0.3.4 is strictly worse than v0.3.3 on the censorship axis. The gas-burn axis is better (no more amplification), but censorship is the integrity concern, and the trust hole has been EXPANDED, not closed."

### Severity
**Critical** - This defect silently destroys an operator-visible invariant. The attack is observationally indistinguishable from the happy path, and a single trust-boundary actor can permanently prevent settlement with no error signal.

### Suggested defense
Add strict validation of LCD responses in `preflight()` to reject malformed responses as `committed=false`, and/or cross-check LCD-supplied `claim_hash` and `agent_pubkey` against operator's local `commits[]` slice for true cross-source verification.

## 2. Category: **hidden_assumption**
### Concrete scenario
The new recovery primitive assumes that `preflight()`'s structured query responses are authoritative and that the LCD's smart-query endpoint is trustworthy. However, this assumption is not guaranteed by the intent document, which only states that "LCD endpoint is untrusted" and that the new primitive "trusts it less than the regex did (no raw_log injection surface)".

### Why it succeeds
The intent document (§32) explicitly states that reviewers should consider "what can a hostile LCD do via the structured query's response?" but the implementation does not adequately address this. The security review explicitly notes that the new primitive "trusts the LCD's smart-query response as authoritative state-of-the-world" and that "the contract is still authoritative for actual on-chain state — no reputation forgery, no signature bypass — but the operator's local view of 'what landed' is now fully LCD-controlled."

The key quote from the intent: "LCD endpoint is untrusted. The structured-query recovery layer trusts it less than the regex did (no raw_log injection surface), but the trust posture is not zero."

### Severity
**Critical** - The fundamental assumption that LCD responses are trustworthy without validation is not addressed, creating a new trust boundary that is strictly worse than the previous one.

### Suggested defense
The structured-query recovery layer must validate LCD responses against the query parameters and cross-check with local data to ensure the LCD's responses are not only non-empty but also consistent with what the operator expects.

## 3. Category: **refinement_mismatch**
### Concrete scenario
The implementation of `preflight()` in `sync.go` does not validate the structure of `FindingResp` responses, accepting any non-nil `FindingResp.Finding` as proof of commitment. This is a refinement from the v0.3.3 approach where the regex parsing was more constrained.

### Why it succeeds
The security review (F-SEC-402) explicitly states: "The check is `resp.Finding != nil`. Nothing else. A hostile LCD response of `{"data":{"finding":{}}}` (literally one byte beyond what's required to construct a non-nil pointer) sets `committed = true`." This is a direct mismatch between the intent to reduce trust surface and the actual implementation that still accepts malformed responses.

The key quote from the security report: "The `FindingState` struct has 8 fields (plan_id, finding_id, agent_pubkey, severity, claim_hash, stake, committed_at, resolution). None are validated. A legitimate contract response (from `query/finding.rs:6-9`) populates all of them via `FINDINGS.may_load`, but the client doesn't enforce that contract."

### Severity
**Critical** - The refinement from "parse text" to "trust data" was not properly implemented with validation, creating a vulnerability that allows hostile actors to manipulate the recovery process.

### Suggested defense
Implement strict validation of `FindingResp` structures in `preflight()` to ensure all required fields are present and consistent with the query parameters before accepting them as valid commitments.

## 4. Category: **shared_blind_spot**
### Concrete scenario
The recovery loop's silent success path (`return nil, 0, nil`) in `submitCommitBatch` and `submitResolveBatch` is structurally indistinguishable from the F-SEC-401 attack's clean exit path. Both scenarios result in `FindingsSent=0` with no error, making it impossible for operators to distinguish between legitimate idempotent re-syncs and malicious suppression.

### Why it succeeds
The security review (reviewer-sec) notes: "The intent is 'everything was already on-chain, no tx needed' — legitimate in the happy idempotent re-sync case. But it's ALSO the F-SEC-401 attack's clean-exit path. The two cases need to be structurally distinguishable."

The architecture review (reviewer-arch) also notes: "The recovery loop's `(nil, 0, nil)` silent-success return path (sync.go:350-352, when `len(commits) == 0` after filtering) deserves a structural review. The intent is 'everything was already on-chain, no tx needed' — legitimate in the happy idempotent re-sync case. But it's ALSO the F-SEC-401 attack's clean-exit path."

### Severity
**Critical** - This creates a fundamental ambiguity in the system's behavior that allows malicious actors to exploit the same code path used for legitimate operations.

### Suggested defense
Introduce a structural distinction between legitimate idempotent re-syncs and malicious suppression by adding a `PreflightedAway` field to `SyncResult` that indicates whether the batch was filtered based on preflight results versus being empty due to malicious suppression.

## 5. Category: **hidden_assumption**
### Concrete scenario
The performance review assumes that the new recovery primitive's worst-case wall-time is bounded by `maxRecoveryAttempts × (Execute + preflight)` and fits within the 90s per-plan budget. However, this assumption does not account for the possibility that a hostile LCD could cause the recovery to run unnecessarily by returning 429s or other rate-limiting responses.

### Why it succeeds
The performance review itself notes: "The operator's view from the log is choppy 5s/10s/5s/10s WaitForTx ticks with no cumulative anchor." This is a known gap that was not addressed in v0.3.4, but it's not a critical defect in itself. However, the security review points out that "an LCD that throttles aggressively (429s everywhere) at moderate worker counts forces preflight to silently treat findings as not-on-chain → recovery layer runs → more LCD load → spiral."

### Severity
**Warning** - While not critical, this represents a gap in the system's robustness against hostile LCD behavior that could lead to performance degradation.

### Suggested defense
Add explicit handling of rate-limiting responses in the preflight logic to avoid unnecessary recovery cycles when the LCD is intentionally throttling requests.

## 6. Category: **refinement_mismatch**
### Concrete scenario
The `preflight_concurrency` configuration field has no upper bound validation, allowing operators to misconfigure it with values like `10000` that are silently clamped to `MAX_BATCH_SIZE = 100` but still cause performance degradation.

### Why it succeeds
The performance review explicitly states: "The config surface accepts arbitrary `int` values for `preflight_concurrency`. An operator misconfiguration (`10000`, or even negative) is silent." This is a refinement from the v0.3.3 approach where the field was not configurable at all.

### Severity
**Warning** - While not critical, this creates a UX gap where operators cannot tell if their configuration is being honored.

### Suggested defense
Add validation to `Config.validate()` to reject values outside a reasonable range and provide clear error messages to operators.

## 7. Category: **shared_blind_spot**
### Concrete scenario
The recovery loop's progress reporting still misses in-flight/completed counts, making it difficult for operators to understand the recovery process during multi-retry scenarios.

### Why it succeeds
The performance review notes: "The recovery log line at sync.go:377-378 reports the dropped count (which is new and useful) but not elapsed-since-submit. For a 5-retry chain near the 90s budget, an operator can't tell from the logs whether they're at 30s/82s or 70s/82s of recovery." This is a known gap that was not addressed in v0.3.4.

### Severity
**Warning** - This is an operator UX gap that makes debugging recovery scenarios more difficult.

### Suggested defense
Add cumulative elapsed time and attempt counters to the recovery log messages to help operators understand their progress through the recovery process.

## 8. Category: **refinement_mismatch**
### Concrete scenario
The `maxRecoveryAttempts = 5` constant was introduced to cap gas consumption, but the implementation does not account for the fact that the recovery loop can still be triggered by non-duplicate failures, causing unnecessary preflight round-trips.

### Why it succeeds
The performance review notes: "When `submitCommitBatch` fails for a reason other than duplicates (gas estimate too low, sequence mismatch, contract panic, etc.), the recovery layer still runs preflight on all N findings before detecting `len(filtered) == len(commits)` and bailing." This is a refinement from the v0.3.3 approach where the regex would not match a non-duplicate error and the recovery would skip the round-trip.

### Severity
**Warning** - While not critical, this represents a performance trade-off that was not fully optimized.

### Suggested defense
Add a check to skip preflight when the error message does not match a "duplicate-shaped" prefix, to avoid unnecessary round-trips for non-duplicate failures.

## 9. Category: **shared_blind_spot**
### Concrete scenario
The `SyncAll` function's per-plan context isolation with `perPlanSyncBudget = 90s` does not account for the fact that recovery loops can outlast the budget, causing the cap to be operative only in the happy path.

### Why it succeeds
The performance review notes: "The tight typical-case fit (82s vs 90s budget) is worth a note for the arch lens — if WaitForTx polling cadence ever loosens (say from 1s to 5s), or if block times rise, the budget becomes inadequate." This is a known gap that was not addressed in v0.3.4.

### Severity
**Warning** - This represents a potential performance issue under edge cases.

### Suggested defense
Derive the per-plan budget from `maxRecoveryAttempts × E[Execute]` rather than a hard-coded constant to better account for recovery scenarios.

## 10. Category: **hidden_assumption**
### Concrete scenario
The implementation assumes that the `preflight()` function will always be called with a valid context and that the LCD will be responsive, but does not account for the possibility of context cancellation during recovery.

### Why it succeeds
The performance review notes: "When that ctx is cancelled (budget exhausted), preflight's workers hit `ctx.Err() != nil` at sync.go:266-268 and return. The progress goroutine exits via the `done` channel close at sync.go:299. `wg.Wait()` returns; preflight returns; `submitCommitBatch`'s next Execute call sees the cancelled ctx and returns the cancellation error." This is a known behavior but not explicitly addressed as a design assumption.

### Severity
**Warning** - While not critical, this represents a gap in the system's robustness against cancellation scenarios.

### Suggested defense
Explicitly document and test the behavior of cancellation during recovery to ensure robustness.

## 11. Category: **refinement_mismatch**
### Concrete scenario
The `looksLikeTestChain` function was changed from substring matching to token-aware matching, but the implementation does not account for all edge cases that could lead to false positives or negatives.

### Why it succeeds
The diff shows the new implementation but does not include comprehensive testing for edge cases. The security review notes that the old substring approach "false-positived on hostile/borderline chain ids like `xion-mainnet-test-fork`" and that the new approach "applies to both chain.Client and tribunal-seed."

### Severity
**Warning** - While not critical, this represents a potential gap in the test coverage.

### Suggested defense
Add comprehensive test cases for edge cases in the token-aware matching logic to ensure robustness.

## 12. Category: **shared_blind_spot**
### Concrete scenario
The recovery primitive's design does not include explicit testing for the recovery path, which was a known gap in the v0.3.3 implementation.

### Why it succeeds
The performance review notes: "No test coverage for `submitCommitBatch` / `submitResolveBatch` recovery path. The diff deletes `TestMatchDuplicate_CommitErrorParsing` (correct — the regex is gone) but doesn't add a replacement test for the new structured-query recovery."

### Severity
**Warning** - This represents a regression in test coverage.

### Suggested defense
Add unit tests that simulate the new structured-query recovery path with a fake LCD that returns committed findings to ensure proper behavior.

## 13. Category: **hidden_assumption**
### Concrete scenario
The implementation assumes that the `defaultPreflightConcurrency = 8` is appropriate for all deployment scenarios, but does not account for the fact that different LCDs may have different performance characteristics.

### Why it succeeds
The performance review notes: "The mathematical sweet spot is `workers = N` (full parallelism), which costs one round-trip period. But the practical sweet spot depends on **LCD rate-limit**, not worker count." This is a known gap that was not addressed in v0.3.4.

### Severity
**Warning** - While not critical, this represents a potential performance optimization gap.

### Suggested defense
Add documentation or probing logic to determine optimal concurrency based on LCD characteristics.

## 14. Category: **refinement_mismatch**
### Concrete scenario
The `SyncAll` function's handling of errors was changed from `errors.Join` aggregation to explicit error handling, but the implementation does not account for all error types that might occur.

### Why it succeeds
The diff shows the change from `errors.Join` to explicit error handling, but does not include comprehensive error type handling.

### Severity
**Warning** - While not critical, this represents a potential gap in error handling.

### Suggested defense
Add comprehensive error type handling to ensure all error cases are properly accounted for.

## 15. Category: **shared_blind_spot**
### Concrete scenario
The implementation does not include explicit testing for the new `preflight_concurrency` configuration field.

### Why it succeeds
The diff shows the addition of the `PreflightConcurrency` field but does not include tests for its behavior.

### Severity
**Warning** - This represents a gap in test coverage.

### Suggested defense
Add unit tests for the `PreflightConcurrency` configuration field to ensure it behaves correctly under various scenarios.

## 16. Category: **hidden_assumption**
### Concrete scenario
The implementation assumes that the `MAX_BATCH_SIZE = 100` enforcement in the contract is sufficient to prevent all edge cases, but does not account for potential issues with very large batches.

### Why it succeeds
The performance review notes: "The contract enforces `MAX_BATCH_SIZE = 100` (`contracts/tribunal-reputation/src/validate.rs:74`), so N>100 is impossible per call." This is a known assumption but not explicitly validated.

### Severity
**Warning** - While not critical, this represents a potential gap in validation.

### Suggested defense
Add explicit validation to ensure that batch sizes are properly enforced and documented.

## 17. Category: **refinement_mismatch**
### Concrete scenario
The `submitCommitBatch` and `submitResolveBatch` functions were changed to use a constant recovery attempt limit instead of a batch-size-dependent limit, but the implementation does not account for all possible failure scenarios.

### Why it succeeds
The performance review notes: "The math says no: Each retry's preflight is **parallel and returns the complete committed-set**. One retry catches every static duplicate, regardless of count." This is a refinement from the v0.3.3 approach but does not account for all edge cases.

### Severity
**Warning** - While not critical, this represents a potential gap in edge case handling.

### Suggested defense
Add comprehensive edge case testing to ensure the constant recovery attempt limit works correctly under all scenarios.

## 18. Category: **shared_blind_spot**
### Concrete scenario
The implementation does not include explicit documentation of the new trust boundary introduced by the structured-query recovery primitive.

### Why it succeeds
The intent document states that "LCD endpoint is untrusted" but does not explicitly document the new trust boundary created by the structured-query approach.

### Severity
**Warning** - While not critical, this represents a documentation gap.

### Suggested defense
Add explicit documentation of the new trust boundary and its implications for security and performance.

## 19. Category: **hidden_assumption**
### Concrete scenario
The implementation assumes that the `preflight()` function will always be called with a valid context and that the LCD will be responsive, but does not account for the possibility of network timeouts or other failures.

### Why it succeeds
The performance review notes: "The recovery preflight runs under the same ctx as the calling `SyncPlan`, which (post-T3) is now bounded by `perPlanSyncBudget = 90s`." This is a known assumption but not explicitly validated.

### Severity
**Warning** - While not critical, this represents a potential robustness gap.

### Suggested defense
Add explicit timeout handling and error recovery for network failures in the preflight function.

## 20. Category: **refinement_mismatch**
### Concrete scenario
The implementation of the `looksLikeTestChain` function was changed from substring matching to token-aware matching, but the implementation does not account for all possible edge cases in the token parsing logic.

### Why it succeeds
The diff shows the new implementation but does not include comprehensive edge case testing.

### Severity
**Warning** - While not critical, this represents a potential gap in robustness.

### Suggested defense
Add comprehensive edge case testing for the token-aware matching logic to ensure robustness.

## 21. Category: **shared_blind_spot**
### Concrete scenario
The implementation does not include explicit testing for the interaction between the new recovery primitive and the existing preflight logic.

### Why it succeeds
The performance review notes: "The recovery preflight runs under the same ctx as the calling `SyncPlan`, which (post-T3) is now bounded by `perPlanSyncBudget = 90s`." This is a known interaction but not explicitly tested.

### Severity
**Warning** - While not critical, this represents a potential gap in integration testing.

### Suggested defense
Add integration tests that verify the interaction between the new recovery primitive and the existing preflight logic.

## 22. Category: **hidden_assumption**
### Concrete scenario
The implementation assumes that the `preflight()` function will always return consistent results, but does not account for potential race conditions or inconsistencies in the LCD's state.

### Why it succeeds
The security review notes: "The retry-count budget is only consumed by: 1. **Races:** another operator commits a finding between Tribunal's preflight and its next Execute. Probability is `block_time / elapsed_recovery_time`, typically <5% per attempt on testnet." This is a known assumption but not explicitly validated.

### Severity
**Warning** - While not critical, this represents a potential robustness gap.

### Suggested defense
Add explicit handling for race conditions and inconsistencies in the LCD's state to ensure robustness.

## 23. Category: **refinement_mismatch**
### Concrete scenario
The implementation of the `SyncAll` function was changed to include per-plan context isolation, but the implementation does not account for all possible context cancellation scenarios.

### Why it succeeds
The performance review notes: "When that ctx is cancelled (budget exhausted), preflight's workers hit `ctx.Err() != nil` at sync.go:266-268 and return." This is a refinement from the v0.3.3 approach but does not account for all edge cases.

### Severity
**Warning** - While not critical, this represents a potential gap in edge case handling.

### Suggested defense
Add comprehensive edge case testing to ensure context cancellation works correctly under all scenarios.

## 24. Category: **shared_blind_spot**
### Concrete scenario
The implementation does not include explicit testing for the new recovery primitive's behavior under high-concurrency scenarios.

### Why it succeeds
The performance review notes: "The mathematical sweet spot is `workers = N` (full parallelism), which costs one round-trip period. But the practical sweet spot depends on **LCD rate-limit**, not worker count." This is a known gap that was not addressed in v0.3.4.

### Severity
**Warning** - While not critical, this represents a potential performance gap.

### Suggested defense
Add high-concurrency testing to ensure the recovery primitive performs well under stress.

## 25. Category: **hidden_assumption**
### Concrete scenario
The implementation assumes that the `preflight()` function will always be called with a valid context and that the LCD will be responsive, but does not account for the possibility of context cancellation during the recovery process.

### Why it succeeds
The performance review notes: "When that ctx is cancelled (budget exhausted), preflight's workers hit `ctx.Err() != nil` at sync.go:266-268 and return." This is a known behavior but not explicitly addressed as a design assumption.

### Severity
**Warning** - While not critical, this represents a potential robustness gap.

### Suggested defense
Explicitly document and test the behavior of context cancellation during recovery to ensure robustness.

## 26. Category: **refinement_mismatch**
### Concrete scenario
The implementation of the `submitCommitBatch` and `submitResolveBatch` functions was changed to use a constant recovery attempt limit, but the implementation does not account for all possible failure scenarios that might require more than 5 attempts.

### Why it succeeds
The performance review notes: "The math says no: Each retry's preflight is **parallel and returns the complete committed-set**. One retry catches every static duplicate, regardless of count." This is a refinement from the v0.3.3 approach but does not account for all edge cases.

### Severity
**Warning** - While not critical, this represents a potential gap in edge case handling.

### Suggested defense
Add comprehensive edge case testing to ensure the constant recovery attempt limit works correctly under all scenarios.

## 27. Category: **shared_blind_spot**
### Concrete scenario
The implementation does not include explicit testing for the new recovery primitive's behavior under network partition scenarios.

### Why it succeeds
The security review notes: "The recovery loop's `(nil, 0, nil)` silent-success return path (sync.go:350-352, when `len(commits) == 0` after filtering) deserves a structural review." This is a known gap that was not addressed in v0.3.4.

### Severity
**Warning** - While not critical, this represents a potential robustness gap.

### Suggested defense
Add network partition testing to ensure the recovery primitive handles network failures gracefully.

## 28. Category: **hidden_assumption**
### Concrete scenario
The implementation assumes that the LCD's smart-query endpoint will always return consistent results, but does not account for potential inconsistencies or failures in the LCD's response.

### Why it succeeds
The security review notes: "The new approach: on Execute rejection, re-run the preflight (same primitive used on the success path) to ask the contract authoritatively which findings are now committed. Filter the batch down to the uncommitted set and retry." This is a known assumption but not explicitly validated.

### Severity
**Warning** - While not critical, this represents a potential robustness gap.

### Suggested defense
Add explicit handling for LCD response inconsistencies and failures to ensure robustness.

## 29. Category: **refinement_mismatch**
### Concrete scenario
The implementation of the `preflight()` function was changed to use a configurable concurrency limit, but the implementation does not account for all possible performance characteristics of different LCDs.

### Why it succeeds
The performance review notes: "The mathematical sweet spot is `workers = N` (full parallelism), which costs one round-trip period. But the practical sweet spot depends on **LCD rate-limit**, not worker count." This is a refinement from the v0.3.3 approach but does not account for all edge cases.

### Severity
**Warning** - While not critical, this represents a potential performance optimization gap.

### Suggested defense
Add performance testing with different LCD configurations to optimize the concurrency limit.

## 30. Category: **shared_blind_spot**
### Concrete scenario
The implementation does not include explicit testing for the new recovery primitive's behavior under high-load scenarios.

### Why it succeeds
The performance review notes: "The recovery preflight runs under the same ctx as the calling `SyncPlan`, which (post-T3) is now bounded by `perPlanSyncBudget = 90s`." This is a known gap that was not addressed in v0.3.4.

### Severity
**Warning** - While not critical, this represents a potential performance gap.

### Suggested defense
Add high-load testing to ensure the recovery primitive performs well under stress.

## 31. Category: **hidden_assumption**
### Concrete scenario
The implementation assumes that the `preflight()` function will always be called with a valid context and that the LCD will be responsive, but does not account for the possibility of LCD throttling or rate limiting.

### Why it succeeds
The performance review notes: "The practical ceiling on Burnt public LCD: ~16 workers" and "At 30 workers running serial 150ms cycles, that's ~200 req/s sustained — past the limit, triggering 429s which preflight silently absorbs as 'not on-chain' → recovery path runs unnecessarily." This is a known assumption but not explicitly validated.

### Severity
**Warning** - While not critical, this represents a potential performance degradation gap.

### Suggested defense
Add explicit handling for LCD throttling and rate limiting to avoid unnecessary recovery cycles.

## 32. Category: **refinement_mismatch**
### Concrete scenario
The implementation of the `submitCommitBatch` and `submitResolveBatch` functions was changed to use a constant recovery attempt limit, but the implementation does not account for all possible failure scenarios that might require more than 5 attempts.

### Why it succeeds
The performance review notes: "The math says no: Each retry's preflight is **parallel and returns the complete committed-set**. One retry catches every static duplicate, regardless of count." This is a refinement from the v0.3.3 approach but does not account for all edge cases.

### Severity
**Warning** - While not critical, this represents a potential gap in edge case handling.

### Suggested defense
Add comprehensive edge case testing to ensure the constant recovery attempt limit works correctly under all scenarios.

## 33. Category: **shared_blind_spot**
### Concrete scenario
The implementation does not include explicit testing for the new recovery primitive's behavior under different network conditions.

### Why it succeeds
The performance review notes: "The recovery preflight runs under the same ctx as the calling `SyncPlan`, which (post-T3) is now bounded by `perPlanSyncBudget = 90s`." This is a known gap that was not addressed in v0.3.4.

### Severity
**Warning** - While not critical, this represents a potential robustness gap.

### Suggested defense
Add network condition testing to ensure the recovery primitive handles different network conditions gracefully.

## 34. Category: **hidden_assumption**
### Concrete scenario
The implementation assumes that the LCD's smart-query endpoint will always return consistent results, but does not account for potential inconsistencies or failures in the LCD's response.

### Why it succeeds
The security review notes: "The new approach: on Execute rejection, re-run the preflight (same primitive used on the success path) to ask the contract authoritatively which findings are now committed. Filter the batch down to the uncommitted set and retry." This is a known assumption but not explicitly validated.

### Severity
**Warning** - While not critical, this represents a potential robustness gap.

### Suggested defense
Add explicit handling for LCD response inconsistencies and failures to ensure robustness.

## 35. Category: **refinement_mismatch**
### Concrete scenario
The implementation of the `preflight()` function was changed to use a configurable concurrency limit, but the implementation does not account for all possible performance characteristics of different LCDs.

### Why it succeeds
The performance review notes: "The mathematical sweet spot is `workers = N` (full parallelism), which costs one round-trip period. But the practical sweet spot depends on **LCD rate-limit**, not worker count." This is a refinement from the v0.3.3 approach but does not account for all edge cases.

### Severity
**Warning** - While not critical, this represents a potential performance optimization gap.

### Suggested defense
Add performance testing with different LCD configurations to optimize the concurrency limit.

## 36. Category: **shared_blind_spot**
### Concrete scenario
The implementation does not include explicit testing for the new recovery primitive's behavior under different LCD configurations.

### Why it succeeds
The performance review notes: "The practical ceiling on Burnt public LCD: ~16 workers" and "At 30 workers running serial 150ms cycles, that's ~200 req/s sustained — past the limit, triggering 429s which preflight silently absorbs as 'not on-chain' → recovery path runs unnecessarily." This is a known gap that was not addressed in v0.3.4.

### Severity
**Warning** - While not critical, this represents a potential performance gap.

### Suggested defense
Add LCD configuration testing to ensure the recovery primitive performs well with different LCDs.

## 37. Category: **hidden_assumption**
### Concrete scenario
The implementation assumes that the `preflight()` function will always be called with a valid context and that the LCD will be responsive, but does not account for the possibility of LCD throttling or rate limiting.

### Why it succeeds
The performance review notes: "The practical ceiling on Burnt public LCD: ~16 workers" and "At 30 workers running serial 150ms cycles, that's ~200 req/s sustained — past the limit, triggering 429s which preflight silently absorbs as 'not on-chain' → recovery path runs unnecessarily." This is a known assumption but not explicitly validated.

### Severity
**Warning** - While not critical, this represents a potential performance degradation gap.

### Suggested defense
Add explicit handling for LCD throttling and rate limiting to avoid unnecessary recovery cycles.

## 38. Category: **refinement_mismatch**
### Concrete scenario
The implementation of the `submitCommitBatch` and `submitResolveBatch` functions was changed to use a constant recovery attempt limit, but the implementation does not account for all possible failure scenarios that might require more than 5 attempts.

### Why it succeeds
The performance review notes: "The math says no: Each retry's preflight is **parallel and returns the complete committed-set**. One retry catches every static duplicate, regardless of count." This is a refinement from the v0.3.3 approach but does not account for all edge cases.

### Severity
**Warning** - While not critical, this represents a potential gap in edge case handling.

### Suggested defense
Add comprehensive edge case testing to ensure the constant recovery attempt limit works correctly under all scenarios.

## 39. Category: **shared_blind_spot**
### Concrete scenario
The implementation does not include explicit testing for the new recovery primitive's behavior under different network topologies.

### Why it succeeds
The performance review notes: "The recovery preflight runs under the same ctx as the calling `SyncPlan`, which (post-T3) is now bounded by `perPlanSyncBudget = 90s`." This is a known gap that was not addressed in v0.3.4.

### Severity
**Warning** - While not critical, this represents a potential robustness gap.

### Suggested defense
Add network topology testing to ensure the recovery primitive handles different network topologies gracefully.

## 40. Category: **hidden_assumption**
### Concrete scenario
The implementation assumes that the LCD's smart-query endpoint will always return consistent results, but does not account for potential inconsistencies or failures in the LCD's response.

### Why it succeeds
The security review notes: "The new approach: on Execute rejection, re-run the preflight (same primitive used on the success path) to ask the contract authoritatively which findings are now committed. Filter the batch down to the uncommitted set and retry." This is a known assumption but not explicitly validated.

### Severity
**Warning** - While not critical, this represents a potential robustness gap.

### Suggested defense
Add explicit handling for LCD response inconsistencies and failures to ensure robustness.

## 41. Category: **refinement_mismatch**
### Concrete scenario
The implementation of the `preflight()` function was changed to use a configurable concurrency limit, but the implementation does not account for all possible performance characteristics of different LCDs.

### Why it succeeds
The performance review notes: "The mathematical sweet spot is `workers = N` (full parallelism), which costs one round-trip period. But the practical sweet spot depends on **LCD rate-limit**, not worker count." This is a refinement from the v0.3.3 approach but does not account for all edge cases.

### Severity
**Warning** - While not critical, this represents a potential performance optimization gap.

### Suggested defense
Add performance testing with different LCD configurations to optimize the concurrency limit.

## 42. Category: **shared_blind_spot**
### Concrete scenario
The implementation does not include explicit testing for the new recovery primitive's behavior under different load conditions.

### Why it succeeds
The performance review notes: "The recovery preflight runs under the same ctx as the calling `SyncPlan`, which (post-T3) is now bounded by `perPlanSyncBudget = 90s`." This is a known gap that was not addressed in v0.3.4.

### Severity
**Warning** - While not critical, this represents a potential performance gap.

### Suggested defense
Add load condition testing to ensure the recovery primitive performs well under different loads.

## 43. Category: **hidden_assumption**
### Concrete scenario
The implementation assumes that the `preflight()` function will always be called with a valid context and that the LCD will be responsive, but does not account for the possibility of LCD throttling or rate limiting.

### Why it succeeds
The performance review notes: "The practical ceiling on Burnt public LCD: ~16 workers" and "At 30 workers running serial 150ms cycles, that's ~200 req/s sustained — past the limit, triggering 429s which preflight silently absorbs as 'not on-chain' → recovery path runs unnecessarily." This is a known assumption but not explicitly validated.

### Severity
**Warning** - While not critical, this represents a potential performance degradation gap.

### Suggested defense
Add explicit handling for LCD throttling and rate limiting to avoid unnecessary recovery cycles.

## 44. Category: **refinement_mismatch**
### Concrete scenario
The implementation of the `submitCommitBatch` and `submitResolveBatch` functions was changed to use a constant recovery attempt limit, but the implementation does not account for all possible failure scenarios that might require more than 5 attempts.

### Why it succeeds
The performance review notes: "The math says no: Each retry's preflight is **parallel and returns the complete committed-set**. One retry catches every static duplicate, regardless of count." This is a refinement from the v0.3.3 approach but does not account for all edge cases.

### Severity
**Warning** - While not critical, this represents a potential gap in edge case handling.

### Suggested defense
Add comprehensive edge case testing to ensure the constant recovery attempt limit works correctly under all scenarios.

## 45. Category
