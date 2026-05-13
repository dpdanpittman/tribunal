# Security Review — Tribunal v0.3.0

**Reviewer:** `tribunal-reviewer-sec` (lens: auth, input validation, injection, state integrity, secrets, unsafe defaults)
**Scope:** `contracts/tribunal-reputation/`, `internal/chain/`, `cmd/tribunal/chain.go`, `scripts/deploy-contract.sh`, `scripts/init-testnet.sh`
**Verdict:** **Request Changes**

## Summary

Tribunal v0.3 implements a soulbound reputation contract for tracking agent-signed findings on Burnt XION. The core architecture enforces critical invariants: pubkey/label uniqueness, signature verification on finding commits and resolutions, authorization gates restricting resolvers to PM/QA roles, and stake mechanics tied to reputation. The review identified **one critical wire-format mismatch** that breaks signature verification under specific conditions, **one input validation gap** that could enable storage-key pollution, and **one unsafe default** that trades security for testnet convenience. The contract's CosmWasm integration and Go client are otherwise sound: signature binding is comprehensive, resolver authorization gates work correctly, and the double-resolve check is race-safe by CosmWasm's execution model.

## Findings

### F-SEC-001: Wire-format type mismatch — stake serialization (uint64 vs u128) (Critical)

**Scenario:** `internal/chain/canonical.go::CanonicalFindingMessage` (line 10-12) accepts `stake uint64`, but `contracts/tribunal-reputation/src/execute/commit.rs::canonical_finding_message` (line 125-136) expects `stake: u128`. The canonical bytes are identical for small stakes (< 2^64) because `fmt.Sprintf("%d", uint64)` in Go produces the same decimal string as Rust's `format!("{}", u128)`. However, the contract's JSON deserialization on `FindingCommit.stake` is `u128`. If a stake value is constructed in Go larger than `uint64::MAX`, the Go canonical message builder will silently truncate.

**Why this matters:** Signature verification works today because current stakes (8, 4, 2) are safely below 2^64. But this is a latent trap: any future code path that wants stake > uint64::MAX will produce a canonical message that doesn't match what was signed, and signatures will silently fail. More importantly, it violates the single-source-of-truth principle — the contract declares `stake: u128`, but the Go client is hardcoded to `uint64`. A protocol upgrade that increases default stakes or allows custom stakes > uint64::MAX will silently break verification.

**Suggested defense:** Update `internal/chain/canonical.go::CanonicalFindingMessage` to accept a stake type that matches u128 (e.g., `*big.Int` or a `Uint128` wrapper). Document the range constraint. Better yet, change the Rust side to use `cosmwasm_std::Uint128` for the typed field, which serializes to a decimal string (so Go can store it as a string and pass it through without needing big.Int arithmetic for the canonical bytes).

### F-SEC-002: No length validation on `plan_id` / `finding_id` / `claim_hash` (Warning)

**Scenario:** `contracts/tribunal-reputation/src/execute/commit.rs::process_finding` (lines 51-118) and `resolve.rs::process_resolution` (lines 49-146) accept untrusted `plan_id` and `finding_id` strings and use them as storage map keys via `FINDINGS.save(deps.storage, (f.plan_id.as_str(), f.finding_id.as_str()), ...)`. There is no length check, charset validation, or collision detection.

**Why this matters:** Two concrete issues:
1. **Storage cost amplification.** Very long strings increase storage cost and could be used for resource exhaustion. CosmWasm's `Map` stores the full key in the prefix; a 10 KB plan_id costs real gas on every read.
2. **Canonical-format ambiguity.** The canonical message format is pipe-separated (`TRIBUNAL_FINDING|plan_id|finding_id|severity|claim_hash|stake`). If a `plan_id` contains `|`, the message is ambiguous for downstream parsers (off-chain auditing tools, ledger viewers). The signature verification still works (canonical bytes are deterministic), but the design assumes pipe-free fields.

**Suggested defense:** At the top of `process_finding` / `process_resolution`, add length caps and a forbidden-char check (`|`, `\x00`, control chars). Suggested limits: `plan_id` ≤ 64 chars, `finding_id` ≤ 64 chars, `claim_hash` ≤ 128 chars.

### F-SEC-003: Unsafe default — `keyring_backend = "test"` (Warning)

**Scenario:** `internal/chain/config.go:79` (`applyDefaults`) sets `KeyringBackend` to `"test"` if unset. The `"test"` backend stores keys in plaintext in `~/.xiond/keyrings/test/` with minimal protection.

**Why this matters:** The Tribunal operator key signs all contract transactions (CommitFindingBatch, ResolveFindingBatch, RotateAgent). If leaked, an attacker can impersonate the operator and submit false findings, slash agent balances, or rotate agents. An operator who deploys to testnet without realizing that `~/.tribunal/chain.yaml` lacks a `keyring_backend` field will inherit `"test"`. The methodology doc mentions "For production use `keyring_backend: os`" but this is guidance, not enforcement.

**Suggested defense:** Either (1) remove the default and require explicit `keyring_backend` in the config, or (2) detect production endpoints (e.g., mainnet chain IDs) and error if `KeyringBackend` is `"test"`. At minimum, log a `WARNING` on every Client construction when the backend is `test` against a non-test chain id.

### F-SEC-004: Batch-mismatch check uses wrong error (Suggestion)

**Scenario:** `contracts/tribunal-reputation/src/execute/commit.rs::commit_finding_batch` (lines 35-42) iterates over findings and checks `if f.plan_id != plan_id`. On mismatch, it returns `ContractError::FindingAlreadyCommitted` — semantically incorrect.

**Why this matters:** Not a security bug, but poor error routing makes debugging harder. An operator receiving `FindingAlreadyCommitted` when submitting a batch with mixed `plan_id` may retry in a loop, wasting gas.

**Suggested defense:** Add a `ContractError::BatchMixedPlanID { plan_id, finding_id }` variant and use it.

### F-SEC-005: Reputation deltas apply to retired agents after rotation (Suggestion / documentation)

**Scenario:** When an agent is rotated, the old agent is marked `retired_at = Some(...)`. Findings committed by the old agent before rotation remain in storage with `agent_pubkey` pointing to the old (now retired) agent. A subsequent `ResolveFinding` on such a finding will apply the reward/slash to the *retired* agent's balance, not the successor's.

**Why this matters:** This is intentional by design (the on-chain protocol doc says "Old agent's `tp_count` + `fp_count` carry forward"). The filing agent's reputation is credited to the retired agent record, not the successor. It's auditable and preserves history, but the user-facing behavior is surprising: the successor inherits TP/FP counts but not the post-rotation reward for findings filed before rotation.

**Suggested defense:** This is behavior, not a bug — but document it explicitly in `docs/on-chain-protocol.md`. Add a code comment in `resolve.rs` explaining that reputation applies to the agent at commit time, not rotation time.

### F-SEC-006: No rollback for partial batch settlement (Suggestion)

**Scenario:** `internal/chain/sync.go::SyncPlan` (lines 136-144) submits `CommitFindingBatch` and `ResolveFindingBatch` as **separate transactions**. If commit succeeds but resolve fails, findings are on-chain but unresolved. The calling code in `chain.go::newChainSyncCmd` does not retry, re-queue, or roll back.

**Why this matters:** Robustness gap. If a resolve batch fails due to transient chain issues, the sync loop stops and requires manual cleanup. The queue mechanism only handles real-time commit failures, not batch settlement failures.

**Suggested defense:** Make resolve idempotent at the contract level (it already is — double-resolve returns an error, so re-trying after a partial failure is safe to script). Add a "resolve-only" CLI mode (`tribunal chain sync --plan X --skip-commits`) so the operator can retry resolutions independently.

## Cross-Reviewer Notes

- **For reviewer-arch**: The finding-key design `(plan_id, finding_id)` is sound but depends on Go and Rust clients maintaining identical `plan_id` / `finding_id` formats. No schema versioning or migration path is documented if the format changes.
- **For reviewer-perf**: The `Map<(&str, &str), FindingState>` storage layout is linear in finding count per plan. A plan with 10k findings will incur O(10k) storage reads during batch operations. Consider a `findings_by_plan_id` index for large-scale deployments.

## Verdict

**Request Changes**

The critical wire-format mismatch (F-SEC-001) must be resolved before any production deployment. The constraint is currently satisfied because all stakes fit in uint64, but it's a latent trap. The input validation gap (F-SEC-002) should be addressed before mainnet to prevent storage-key pollution. The unsafe default (F-SEC-003) should either be removed or gated behind production-endpoint detection.

All other findings are lower-severity improvements that can ship in v0.3 but should be tracked for v0.4.
