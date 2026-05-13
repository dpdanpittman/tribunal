# Architecture Review — Tribunal v0.3.1 (re-review)

**Reviewer:** `tribunal-reviewer-arch`
**Scope:** v0.3.0..v0.3.1 diff + prior v0.3.0 audit packet for cross-reference
**Verdict:** **Approve**

## Summary

v0.3.1 successfully fixes the critical wire-format mismatch (F-ARCH-001) that would have broken the v0.3.0 Go client at runtime, by migrating all on-chain numeric fields from bare `u128` to `cosmwasm_std::Uint128`, which serializes as decimal strings per cosmwasm convention. The `ResolutionRecord` is correctly split into `stake_returned` + `reward` to match the Go side's expectation. The label cleanup on rotation (A-ADV-001) is implemented in code with internal documentation. Validation for identifier fields (B2 / A-ADV-002) is symmetric across Rust and Go. Go wire-roundtrip tests (`wire_test.go`) were added to catch JSON deserialization regressions. However, the public documentation (`docs/on-chain-protocol.md`, `docs/methodology.md`) was not updated to clarify that `agent_by_label` lookups after rotation return no result (the old label is removed from the index), which differs from the prior implicit assumption that retired agents remain discoverable by label.

## Verification of prior findings

### F-ARCH-001: ResolutionRecord wire-format mismatch — **FIXED**

- `contracts/tribunal-reputation/src/state.rs:155-158` (v0.3.1): `ResolutionRecord` now has `stake_returned: Uint128` + `reward: Uint128` instead of a single `reward_applied: u128`.
- `internal/chain/query.go:66-72`: Go struct matches with `StakeReturned string` + `Reward string`.
- `internal/chain/wire_test.go:74-114` tests the exact path that broke in v0.3.0.

### F-ARCH-002: Contract surface not deterministically testable from Go — **FIXED**

- `internal/chain/wire_test.go` (new, 160 lines, 6 test functions). Covers `ReputationResp`, `AgentResp` with rotation history, `FindingResp` resolved-TP path, null finding, `LeaderboardResp`, `ConfigResp`.

### F-ARCH-003: Deploy script silently tolerates xiond not on PATH — **NOT FIXED (DEFERRED)**

CHANGELOG lists this implicitly as deferred. `scripts/deploy-contract.sh` still lacks the `command -v $XIOND` check.

### F-ARCH-004: Rotation semantics not enforced on the Go side — **PARTIAL**

Rust integration test `rotate_preserves_history_and_resets_balance` covers contract correctness. The audit's suggested Go-side test gap remains, but lower-priority than the blockers.

### F-ARCH-005: Plan-close batching invariant not documented in Go — **NOT FIXED**

`internal/chain/sync.go:89` still lacks the comment. Enforcement is correct (contract rejects mismatches with `BatchMixedPlanID`).

### A-ADV-001: Old label remains queryable after rotation — **FIXED**

- `contracts/tribunal-reputation/src/execute/rotate.rs:53-54`: `AGENTS_BY_LABEL.remove(...)` before saving the new binding.
- `src/state.rs:185-187` documents the cleanup. The new label is allowed to equal the old one.
- **Doc gap:** `docs/on-chain-protocol.md` was not updated (see F-ARCH-101).

### A-ADV-004: Admin field stored but never used — **ACKNOWLEDGED (NO FIX)**

`Config.admin` is still saved at instantiate, still never checked in any execute path. CHANGELOG explicitly defers.

## New findings

### F-ARCH-101: Documentation gap on rotation label removal (Warning)

`docs/on-chain-protocol.md` describes `agent_by_label` as returning "full AgentRecord" but the v0.3.1 contract returns `not_found` for retired labels. The contract behavior is intentional; the docs don't reflect it.

**Suggested defense:** Add a sentence under the `agent_by_label` query description: "Returns `not_found` if the label was retired via rotation."

### F-ARCH-102: Wire tests cover unmarshal only, not marshal (Suggestion)

`internal/chain/wire_test.go` tests response unmarshaling (contract → client). Request marshaling (client → contract) is not tested.

**Suggested defense:** Add `TestWire_FindingCommit_JSONFormat()` etc. that marshal a built message and verify field shapes (e.g., `stake` is a string, `signature` is base64).

### F-ARCH-103: No validation rejection tests (Suggestion)

`validate_id_field` (Rust + Go) is not exercised by integration tests. A regression that removes the pipe check would not be caught.

**Suggested defense:** Add `TestValidateIDField_RejectsPipeCharacter()` etc. on both sides.

## Cross-Reviewer Notes

- **For reviewer-sec:** Uint128 migration removes A-ADV-003's truncation risk. Validation correctly rejects pipe/control chars.
- **For reviewer-perf:** MAX_BATCH_SIZE = 100 enforced. Leaderboard still O(n) but bounded by MAX_LEADERBOARD = 100. Acceptable for v0.3.1.

## Verdict

**Approve.**

The fix release resolves all three blockers + the adversary's primary attack. The deferred items (F-ARCH-003, F-ARCH-005, W2, W4, W6, admin field) are properly acknowledged in CHANGELOG and don't block deployment.
