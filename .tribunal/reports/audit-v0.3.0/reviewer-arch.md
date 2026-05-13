# Architecture Review — Tribunal v0.3.0

**Reviewer:** `tribunal-reviewer-arch` (lens: architecture, dependency direction, boundary integrity, plan traceability, contract conformance)
**Scope:** `contracts/tribunal-reputation/`, `internal/chain/`, `cmd/tribunal/chain.go`, `scripts/deploy-contract.sh`, `scripts/init-testnet.sh`
**Verdict:** **Request Changes** (F-ARCH-001 is a blocker)

## Summary

The Tribunal v0.3 architecture cleanly separates concerns across three layers: the on-chain CosmWasm contract, the Go chain client, and the CLI. Dependency directions are sound (`cmd` imports `internal`; `internal` avoids `cmd`). However, there is a **critical contract-surface mismatch** where the Rust contract's `ResolutionRecord` wire format (single `reward_applied` field) diverges from the Go side's expectation (separate `stake_returned` and `reward` fields). This will cause runtime JSON deserialization failures when clients attempt to query resolved findings. Additionally, the on-chain protocol doc and methodology doc specify the field names differently than what the contract actually serializes, indicating the mismatch was not caught during planning.

## Findings

### F-ARCH-001: ResolutionRecord wire-format mismatch (Critical)

**Scenario:** `contracts/tribunal-reputation/src/state.rs:155` defines `ResolutionRecord` with a single field `pub reward_applied: u128`, but `internal/chain/query.go:56-57` expects two separate fields: `StakeReturned string` and `Reward string`.

**Why this matters:** When the Go client calls `client.Finding()` to query a resolved finding, it unmarshals the contract's JSON response into `FindingResp`, which contains an optional `FindingState` with an optional `ResolutionRecord`. The Rust contract serializes `ResolutionRecord` with field names `outcome`, `resolver_pubkey`, `evidence_hash`, `resolved_at`, and `reward_applied` (per serde defaults). The Go struct expects `outcome`, `resolver_pubkey`, `evidence_hash`, `resolved_at`, `stake_returned`, and `reward`. The JSON unmarshaling in `client.Query()` will fail or silently drop fields at the first call to `client.Finding(ctx, planID, findingID)`.

This surfaces at runtime, not compile time, because the wire format is only validated when the Go code makes a real query to a deployed contract. The contract integration tests do not fail because they use cw-multi-test (in-memory) and do not serialize/deserialize the full JSON response through the Go path; they call the query functions directly.

**Suggested defense:**
- Decide on the canonical wire format: either merge `stake_returned` and `reward` into a single `reward_applied` field (simpler, matches the Rust contract), or split `reward_applied` into two fields in the Rust contract and the Go side.
- Add a Go test that unmarshals a hand-crafted JSON sample of the actual contract response to catch this at CI time.

### F-ARCH-002: Contract surface not deterministically testable from Go (Warning)

**Scenario:** The Go side has no integration tests that actually marshal/unmarshal the full contract JSON response. The tests in `internal/chain/sync_test.go` and `internal/chain/messages_test.go` use stubs and mock servers, but they do not exercise the query response unmarshaling path (`client.Reputation()`, `client.Agent()`, `client.Finding()`, etc.) against realistic contract JSON.

**Why this matters:** Canonical-byte invariants between Rust and Go (the signing messages) are explicitly tested and pass. But the wire-format invariants (JSON field names, types, presence of optional fields) are not. F-ARCH-001 would have been caught at CI time if a test unmarshaled the actual contract `FindingResp` with a resolved finding.

**Suggested defense:** Add a Go test that constructs a realistic contract JSON response (e.g., by copying a response from a live `xiond query wasm smart` call or by having the Rust test export a JSON snapshot) and verifies that the Go structs unmarshal it without error. Include at least one resolved finding with each outcome (`true_positive`, `false_positive`, `stale_duplicate`, `indeterminate`) so all code paths are exercised.

### F-ARCH-003: Deploy script silently tolerates xiond not on PATH (Suggestion)

**Scenario:** `scripts/deploy-contract.sh:54` defaults `XIOND` to `"xiond"` if unset. The script does no `command -v` check before running the (slow) cargo build.

**Why this matters:** The script is meant to be operator-friendly, but it doesn't validate that xiond is available before running a multi-minute cargo build. An operator without xiond on PATH will waste time waiting for the build to complete before hitting the error.

**Suggested defense:** Add an early `command -v $XIOND >/dev/null || { echo "xiond not found at $XIOND"; exit 1; }` check after `XIOND` is set and before any long operations.

### F-ARCH-004: Rotation semantics not enforced on the Go side (Suggestion)

**Scenario:** The Rust contract's `rotate_agent` correctly resets the new agent's balance to `rotation_floor` and carries forward TP/FP counts. The Go side has no corresponding higher-level wrapper (`chain.Rotate()`) and no integration test that verifies the Go-built `RotateAgent` message preserves the expected semantics.

**Why this matters:** `newChainRotateCmd()` exists at `cmd/tribunal/chain.go:357-407` and calls `ExecuteMsg::RotateAgent` directly, which is fine. But there's no integration test that rotates an agent through the Go path and verifies the resulting on-chain state matches the contract's intended semantics. This is a thin gap, but it's the only Execute path without an end-to-end Go test.

**Suggested defense:** Add an `internal/chain` integration test that constructs a `RotateAgent` message, executes it against a stubbed `Client` (mocking xiond), and verifies the message JSON shape.

### F-ARCH-005: Plan-close batching invariant not documented in code (Suggestion)

**Scenario:** The `CommitFindingBatch` and `ResolveFindingBatch` execute messages require that every entry's `plan_id` matches the batch's `plan_id` (enforced in Rust at `src/execute/commit.rs:36-42`). The Go side's `Sync.SyncPlan` respects this at `internal/chain/sync.go:87-92`, but the invariant is not documented as a comment near the batching logic.

**Why this matters:** A maintainer modifying `SyncPlan` might accidentally ship findings from multiple plans in one batch (violating the invariant), and the contract would reject the entire batch with a misleading `FindingAlreadyCommitted` error.

**Suggested defense:** Add a comment above the loop at `sync.go:89` explaining the invariant.

## Cross-Reviewer Notes

- **For reviewer-sec**: The `ed25519_verify` calls in `commit.rs` and `resolve.rs` are correct (using `deps.api.ed25519_verify` and passing the raw canonical bytes). The Rust error handling for invalid signatures is sound. No auth concerns found from the architecture lens.
- **For reviewer-perf**: The contract uses dense `Map` storage (good for gas). The Go client splits transactions (shell out to xiond) from queries (direct HTTP to LCD), which is architecturally sound for decoupling signing from read-only ops. No obvious hot-path complexity issues from the architecture lens.

## Verdict

**Request Changes**

F-ARCH-001 is a blocker: the contract will not work with the Go client as shipped. The mismatch must be resolved before the contract is deployed or the Go client will fail at runtime on the first call to `client.Finding()` with a resolved finding.

F-ARCH-002 is a process concern: Go integration tests should validate wire-format parity with the contract to catch regressions.

F-ARCH-003 and F-ARCH-004 are QoL improvements; F-ARCH-005 is a documentation nit.
