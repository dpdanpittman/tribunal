# Adversary Attack Report — Tribunal v0.3.1 Consensus

**Adversary:** `tribunal-adversary` (single-model)
**Targets:** the three trio Approve verdicts at `.tribunal/reports/audit-v0.3.1/reviewer-{arch,sec,perf}.md`
**Verdict:** **SURVIVES**

## Summary

The trio's Approve verdict withstands adversarial scrutiny. v0.3.1 successfully fixes all four attacks that broke v0.3.0: (1) label cleanup on rotation now works, removing the retired-label impersonation vector; (2) canonical-format pipe/control-char validation is symmetric across Rust and Go; (3) wire format is aligned (Uint128 as decimal strings both sides); (4) the admin field remains dead code but poses no new attack surface since no execute path references it. The batch-size cap, the reason-field validation (even though unused), and the new wire tests all survive scrutiny. The contract's design is permissionless on register/commit/resolve paths (as intended) and doesn't invite the vector of registering many agents to bloat the AGENTS map in a way that was missed — the trio's architecture is sound.

## Attacks

### A-V31-001: Reward multiplier unbounded at instantiate (Suggestion, insufficient_bounds)

**Target:** Reviewer-sec F-SEC-103, dismissed as "low-risk."

**Attack:** Deployer instantiates with `outcome_reward_multiplier = Uint128::MAX`. First TP resolution saturates `reward = state.stake.saturating_mul(Uint128::MAX) = Uint128::MAX`. Agent balance becomes effectively infinite. Soulbound reputation loses meaning.

**Why it succeeds:** `contract.rs:23-37` accepts the multiplier with no upper-bound check. `saturating_mul` does what it's designed to do — cap at MAX — but the operator's mistake is silent.

**Severity:** Suggestion (deployer error, not attacker-exploitable; impact is self-sabotage).

**Suggested defense:** Bound the multiplier at instantiate (`1 <= multiplier <= 100`). Same risk class for `rotation_floor` vs `initial_balance`.

### A-V31-002: Rotation reason validated but discarded (Suggestion, dead_code)

**Target:** No reviewer flagged this; continuation of A-ADV-004 (dead fields).

**Attack:** `rotate.rs:37` calls `validate_optional_text("reason", &reason, MAX_REASON_LEN)` but the `reason` parameter is never stored anywhere — not in `AgentRecord`, not in event attributes. The 256-char validation is enforced for a field that has no persistence.

**Severity:** Suggestion (wasted validation, indicates incomplete refactoring).

**Suggested defense:** Either store `reason` in `AgentRecord` as `Option<String>` so rotation audit trails include the justification, or remove from the message entirely.

### A-V31-003: Go client does not validate batch size before submission (Warning, client_gap)

**Target:** Reviewer-perf noted Go-side `MaxBatchSize` constant missing.

**Attack:** A Go client submits 500 items. Contract rejects with `BatchTooLarge`, but the client already burned local time on keypair lookup, signature generation, and tx submission. No gas wasted (rejection at the contract boundary), but UX is degraded.

**Severity:** Warning (UX regression, not fund-loss).

**Suggested defense:** Add `const MaxBatchSize = 100` to `internal/chain/validate.go` and call it in `Sync.SyncPlan` before signing.

### A-V31-004: Wire tests cover responses only, not requests (Warning, test_gap)

**Target:** Reviewer-arch F-ARCH-102.

**Attack:** A future contract `migrate` adds a required field to `FindingCommit`. The Go side's struct omits it, `json.Marshal` silently skips, the request lands incomplete, the contract rejects with "missing field." `cargo test` still passes — no marshal-side test catches it.

**Severity:** Warning (latent risk on contract upgrade; no current bug).

**Suggested defense:** Add `TestWire_FindingCommit_Marshal` etc. that build a message, marshal, unmarshal, and assert roundtrip.

### A-V31-005: Integration test count unchanged (Indeterminate, methodology_risk)

**Target:** Reviewer-sec F-SEC-102.

**Attack:** v0.3.1 added validation logic but test count stayed at 15 (same as v0.3.0). A future refactor that accidentally removes the `|` check passes `cargo test` because no integration test sends a `|`-containing string through the full execute flow.

**Severity:** Indeterminate (no current bug; indicates brittle coverage — the same methodology lesson as v0.3.0).

**Suggested defense:** Add `reject_finding_with_pipe_in_plan_id`, `reject_resolution_with_control_char_in_finding_id`, `reject_register_with_oversized_label`.

## Attacks attempted that survived

1. **Label impersonation after rotation.** `rotate.rs:53` removes the old label binding. ✓
2. **Canonical-format pipe ambiguity.** Symmetric validation rejects `|` before signing on both sides. ✓
3. **Wire-format Uint128 mismatch.** Contract + Go both use decimal-string Uint128 wire. Signatures match identically regardless of magnitude. ✓
4. **Stake uint64 truncation.** `CanonicalFindingMessage` now takes stake as `string`. No cast. ✓
5. **Admin latent authority.** `cfg.admin` still has zero callsites. ✓
6. **Misleading batch error.** `BatchMixedPlanID` replaces the misleading `FindingAlreadyCommitted`. ✓
7. **Permissionless registration bloating AGENTS map.** Vector exists (no rate limit on `register_agent`), but it existed in v0.3.0 and the trio approved it as part of the permissionless design. Out of scope for this release. ✓

## Calibration note

The trio's consensus holds. v0.3.1 successfully fixes the four vulnerabilities that broke v0.3.0's consensus and adds conservative guards (validation, batch caps, wire tests). No new attacks surface. The five attacks logged here are suggestion/warning-level — none block. The methodology has done its job: the change is now trustworthy in a way v0.3.0 was not.

**SURVIVES.**
