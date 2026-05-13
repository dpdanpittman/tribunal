# Performance Review — Tribunal v0.3.1 (re-review)

**Reviewer:** `tribunal-reviewer-perf`
**Scope:** v0.3.0..v0.3.1 diff + prior v0.3.0 audit packet for cross-reference
**Verdict:** **Approve**

## Summary

Tribunal v0.3.1 addresses the primary perf finding from the v0.3.0 audit: **MAX_BATCH_SIZE = 100** on the Rust contract side, with the check placed early in both batch entry points so a malformed client can't burn gas on oversized batches. The fix is sound but asymmetric — the contract rejects at execution time, but the Go client has no matching pre-flight check, so a 500-item batch goes all the way through keypair lookup, signature generation, and submission before the contract rejects it. No gas wasted (rejection happens at the contract boundary), but UX could be improved by failing fast on the client side. Other v0.3.1 changes introduce minimal new overhead: validation is O(n) on bounded string length (negligible vs. ed25519), `Uint128` is a thin wrapper over `u128` with zero runtime cost, and the new storage write in rotation (label remove) is single-call per rotation.

## Verification of prior findings

### F-PERF-001: Leaderboard O(n_agents) — **DEFERRED**

`leaderboard.rs` unchanged. Acceptable for current scale; revisit at 1000+ agents.

### F-PERF-002: Unbounded batch Vec — **FIXED (contract side)**

- `validate.rs:74` `const MAX_BATCH_SIZE: usize = 100`.
- `validate.rs:76-87` `validate_batch_size()` returns `BatchTooLarge` or `EmptyBatch`.
- `commit.rs:32` and `resolve.rs:31` call the validator first, before any per-item work.

### F-PERF-003: Queue.Drain O(n) — **DEFERRED**

`queue.go` unchanged.

### F-PERF-004: Flat 30s HTTP timeout — **DEFERRED**

`client.go` unchanged.

### F-PERF-005: SyncPlan in-memory batches — **DEFERRED**

`sync.go` unchanged.

### F-PERF-006 / F-PERF-007: Error classification / retry — **DEFERRED**

Acknowledged in CHANGELOG as v0.4 follow-ups.

## New findings

### F-PERF-101: Redundant plan_id validation in commit/resolve batches (Suggestion, OK)

Batch entry validates `plan_id` once; `process_finding` / `process_resolution` validate it again per-item. With `MAX_ID_LEN = 64`, a 100-item batch validates plan_id 101 times — ~6400 char iterations. Cost is ~0.5% of the signature-verification cost (15ms total for 100 ed25519 verifies). Defense-in-depth is justified.

### F-PERF-102: Identifier validation on every commit/resolve/register (Suggestion, OK)

4 fields validated per finding commit. 256-char string scan is ~0.1µs; 78k iterations on a 100-item batch is ~8µs. Negligible vs. signature work. Correctness-critical, cost-justified.

### F-PERF-103: Rotation adds one storage delete (Suggestion, OK)

`rotate.rs:53` removes the old label binding. ~15k gas. Rotations are rare (not per-plan). Dwarfed by registration cost (~300k gas). Approved.

### F-PERF-104: wire_test.go runtime cost (Suggestion, OK)

Pure CPU, < 1ms total. Negligible.

### F-PERF-105: Uint128 vs u128 arithmetic (Suggestion, OK)

`Uint128` is a `pub struct Uint128(u128)` newtype. `saturating_add` / `saturating_mul` are inlined to the bare `u128` methods. Zero runtime overhead. Correctness improvement.

## Cross-Reviewer Notes

- **For reviewer-arch:** Validation symmetric Rust ↔ Go. Wire format locked via `wire_test.go`. Consider Go-side `MaxBatchSize` for fail-fast UX (see below).
- **For reviewer-sec:** `BatchTooLarge` error carries debugging info. Keyring warning fires on stderr.

## Follow-up suggestion (not blocking)

**Go-side batch size check.** Add `const MaxBatchSize = 100` to `internal/chain/validate.go` and call it in `Sync.SyncPlan` before submitting. Shifts error from execution-time to build-time.

## Deferred items from v0.3.0 audit

All these remain open and are acceptable for current scale:

- F-PERF-001: Leaderboard O(n_agents)
- F-PERF-003: Queue drain O(n)
- F-PERF-004: HTTP timeout strategy
- F-PERF-005: Batch chunking for large plans
- F-PERF-006: Error classification
- F-PERF-007: Retry/backoff

## Verdict

**Approve.**

The fix release successfully addresses the critical perf finding (F-PERF-002) with an early, symmetric check. New overhead is minimal and justified. Deferred items don't block deployment at current scale.
