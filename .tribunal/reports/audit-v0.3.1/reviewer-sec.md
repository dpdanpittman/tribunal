# Security Review — Tribunal v0.3.1 (re-review)

**Reviewer:** `tribunal-reviewer-sec`
**Scope:** v0.3.0..v0.3.1 diff + prior v0.3.0 audit packet for cross-reference
**Verdict:** **Approve**

## Summary

Tribunal v0.3.1 is an audit-driven fix release that addresses the critical wire-format mismatch and input validation gaps identified in the v0.3.0 audit. The key changes are sound: bare `u128` migrated to `cosmwasm_std::Uint128` (fixing numeric serialization), `ResolutionRecord` split into `stake_returned` + `reward` (fixing the shape mismatch), canonical signing format now uses string-type stakes (eliminating the uint64/u128 truncation vector), identifier validation added to reject pipe/NUL/control characters (blocking storage-key poisoning), rotation now deletes the old label binding (fixing the label-reuse issue), and a keyring backend warning implemented (reducing the unsafe-default footgun). The fixes are targeted and correct. The implementation introduces two minor anomalies: (1) inconsistent error-index reporting between Go (byte index) and Rust (char index) in validation error messages — purely cosmetic, and (2) missing integration test coverage for the new validation rules — exercised by unit tests but not end-to-end.

## Verification of prior findings

### F-SEC-001: Wire-format type mismatch — stake serialization — **FIXED**

`msg.rs:32` `stake: Uint128`. `canonical.go:12` signature `stake string`. Decimal-string representation identical on both sides regardless of magnitude. Truncation risk eliminated.

### F-SEC-002: No length validation on identifiers — **FIXED**

New `validate.rs` (Rust) + `validate.go` (Go). Both reject empty, exceed-length, pipe-containing, control-char-containing strings. Enforcement at every entry point. Symmetric constants (`MAX_ID_LEN=64`, `MAX_HASH_LEN=128`).

### F-SEC-003: Unsafe default `keyring_backend = "test"` — **FIXED**

`client.go:39-47` `New()` emits stderr warning when test backend pairs with non-test chain id. `looksLikeTestChain()` heuristic matches "devnet", "testnet", "test", "local".

### F-SEC-004: Batch-mismatch wrong error — **FIXED**

`error.rs` adds `BatchMixedPlanID { batch_plan_id, found_plan_id, finding_id }`. Used in `commit.rs:34-38` and `resolve.rs:35-39`.

### F-SEC-005: Reputation deltas to retired agents after rotation — **INTENTIONAL**

By design; documented in code comments. No change required.

### F-SEC-006: No rollback for partial batch settlement — **DEFERRED**

CHANGELOG defers to v0.4. No security issue (contract is idempotent).

### A-ADV-001: Old label after rotation — **FIXED** (see arch report)

### A-ADV-002: Pipe injection — **FIXED** (covered by F-SEC-002)

### A-ADV-003: Stake type mismatch — **FIXED** (covered by F-SEC-001)

## New findings

### F-SEC-101: Inconsistent error-index reporting (Cosmetic)

`validate.rs:41-46` iterates `value.chars().enumerate()` → reports char index. `validate.go:29-35` iterates `for i, r := range value` where `i` is the byte index. For a string `"hello☃|world"`, Rust reports index 6, Go reports index 8.

**Suggested defense:** Either align both to char index, or document the difference.

### F-SEC-102: Missing integration test coverage for validation (Low)

`validate.rs` constants and behavior are not exercised by any of the 15 integration tests. A regression that removes the pipe check would still pass `cargo test`.

**Suggested defense:** Add `reject_finding_with_pipe_in_plan_id`, `reject_resolution_with_control_char_in_finding_id`, `reject_register_with_oversized_label` tests.

### F-SEC-103: outcome_reward_multiplier has no upper bound at instantiate (Low)

`contract.rs:23-37` accepts any `Uint128`. A deployer fat-fingers `outcome_reward_multiplier = Uint128::MAX`. First TP saturates reward to `Uint128::MAX`. Agent balance becomes effectively infinite. Soulbound reputation loses meaning.

**Suggested defense:** Bound the multiplier at instantiate (e.g., `1 <= multiplier <= 100`). Same risk class on `rotation_floor` (should be < `initial_balance`).

### F-SEC-104: Wire tests cover happy path only (Low)

`wire_test.go` doesn't test rejection of extra fields, missing fields, wrong types.

**Suggested defense:** Add negative tests for type mismatch (`"balance": 123` instead of `"balance": "123"`) and missing required fields.

### F-SEC-105: Keyring warning bypassable by confusing chain ids (Accepted)

`looksLikeTestChain("burnt-testnet-upgrade-mainnet-1")` returns `true` because "testnet" is in the name. Operator gets no warning despite mainnet intent.

**Suggested defense:** Could make stricter, but the check is a best-effort heuristic and the fundamental safety is operator-config. Accept.

## Cross-Reviewer Notes

- **For reviewer-arch:** Wire-format round-trip tests are a good pattern. Consider expanding to cover error responses.
- **For reviewer-perf:** `MAX_BATCH_SIZE = 100` hardcoded. If future config makes this tunable, audit it then.
- **For v0.4 adversary:** `Config.admin` still latent. Audit before any execute path uses it.

## Verdict

**Approve.**

All critical and high-severity findings from v0.3.0 are fixed. The implementations are sound. The five new findings are low-severity or cosmetic. The conditions for approval (all blockers fixed, no new critical bugs, code quality appropriate for mainnet) are met. Recommend tracking F-SEC-102 and F-SEC-103 for v0.4.
