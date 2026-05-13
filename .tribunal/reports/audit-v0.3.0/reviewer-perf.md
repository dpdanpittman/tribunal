# Performance Review — Tribunal v0.3.0

**Reviewer:** `tribunal-reviewer-perf` (lens: gas, complexity, resource lifecycle, observability, degraded-mode behavior)
**Scope:** `contracts/tribunal-reputation/`, `internal/chain/`, `cmd/tribunal/chain.go`
**Verdict:** **Request Changes**

## Summary

Tribunal v0.3.0 implements a sound on-chain reputation contract with generally good resource management, but introduces three performance risks in the hybrid settlement flow and two observability gaps that could impede debugging under degraded conditions. The contract's leaderboard query iterates all agents (O(n)), the queue drain-and-rewrite pattern scales linearly with queue size, the HTTP client timeout is misaligned between fast and slow operations, and batch operations lack on-chain size bounds. While each individual issue is manageable for 10–100 agents and small batches, their interaction under peak load (plan close with 1000+ queued findings) could create cascading delays or incomplete settlements without clear visibility.

## Findings

### F-PERF-001: Leaderboard query iterates every agent, unsorted (Warning)

**Scenario:** `contracts/tribunal-reputation/src/query/leaderboard.rs:19-36` — The `leaderboard` function calls `AGENTS.range(...)` with no bounds and iterates every active agent, filters client-side, sorts in-memory, then truncates. If the registry grows to 1000 agents, the query deserializes and compares all 1000 entries even if `limit=20`.

**Why this matters:** A CosmWasm query is metered against a per-block gas budget. Iterating 1000 `AgentRecord`s is O(n) in both deserialization and sorting. The on-chain protocol doc says the leaderboard is approximately off-chain cost, but it's actually a full range scan. If run frequently (e.g., displayed on a web dashboard), this adds up. The 100-cap is a cap on result size, not on iteration cost.

**Suggested defense:** Add a `MAX_AGENTS` cap (e.g., 500) and document why leaderboard suits only small registries — or restructure storage to a sorted index for efficient pagination (e.g., `Map<(u128_balance, label), pubkey>` keyed by balance descending).

### F-PERF-002: Batch operations accept unbounded `Vec<...>` (Warning)

**Scenario:** `contracts/tribunal-reputation/src/execute/commit.rs:24-49` and `resolve.rs:23-47` — Both `commit_finding_batch` and `resolve_finding_batch` accept `Vec<...>` with no length check. Per item: one storage read (agent), one ed25519 verification (costly), one finding lookup or create, one balance update. The integration tests exercise batches of 3 items.

**Why this matters:** An adversary or buggy client could submit a 10,000-item batch, blow out the gas budget mid-transaction, and burn gas for nothing. The Go client's `SyncPlan` (sync.go:136-144) builds commits in memory without a batch-size cap either.

**Suggested defense:** Add `const MAX_BATCH_SIZE = 100` and check `findings.len() <= MAX_BATCH_SIZE` at the top of both batch functions. Document the per-item gas budget and recommended batch size in `docs/on-chain-protocol.md`.

### F-PERF-003: `Queue.Drain` reads entire queue file and rewrites it (Suggestion)

**Scenario:** `internal/chain/queue.go:71-93` and `116-142` — On each plan sync, `Drain` calls `All()` (which does `os.ReadFile(q.path)`), filters, then `rewrite` recreates a temp file and atomically renames. For a queue with 10,000 entries, this is O(n) memory and O(n) disk I/O on every sync.

**Why this matters:** The queue is a fallback for failed real-time commits (rare events), but it's not bounded. If the chain is down for a day and 1000 real-time commits fail, the next sync reads and rewrites 1000 entries even if only 50 match the current plan.

**Suggested defense:** Document the queue cleanup procedure for operators (the `tribunal chain queue clear` command exists but isn't called out). Add a warning when the queue exceeds, say, 500 entries.

### F-PERF-004: Flat 30-second HTTP timeout for all operations (Warning)

**Scenario:** `internal/chain/client.go:37-40` — The Client's `http.Client` is configured with a global 30-second timeout, used for:
- `Status()` (Tendermint RPC `/status`): should be ~1s.
- `Query()` (LCD smart contract query): should be 2-5s.
- `Execute()` (xiond shell-out + `broadcast-mode sync`): highly variable.

CLI subcommands use their own timeouts: `chain register` and `chain rotate` use 60s (chain.go:125, 397), `chain query *` uses 10s (chain.go:238 etc).

**Why this matters:** A one-size-fits-all 30s default is loose for fast probes (`Status` against a broken node hangs 30s instead of failing fast) and tight for slow tx broadcasts (a backed-up mempool can blow past 30s legitimately).

**Suggested defense:** Use context-specific timeouts: ~2s for `Status()`, 5-10s for `Query()`, no HTTP-level timeout for `Execute()` (which already has the caller-supplied context). Or expose a `--timeout` flag on the CLI.

### F-PERF-005: `Sync.SyncPlan` builds full batch arrays in memory (Suggestion)

**Scenario:** `internal/chain/sync.go:72-157` — The function collects all findings + resolutions for a plan into Go slices before submitting. A 10,000-finding plan allocates 10k+ structs in memory and submits one giant tx. No chunking.

**Why this matters:** Acceptable for normal workflows (100-1000 findings per plan). A 10k-finding plan could OOM the client and would certainly OOM the contract (see F-PERF-002). A failure mid-batch loses all progress.

**Suggested defense:** Add a tunable batch size (`--findings-per-batch 100`) and split large syncs into multiple transactions.

### F-PERF-006: Minimal error context on `CommitRealtime` failure (Suggestion)

**Scenario:** `internal/chain/sync.go:48-67` — On failure, `CommitRealtime` queues the message and returns a wrapped error including the RawLog from xiond. No structured classification of transient vs. permanent.

**Why this matters:** A reviewer hitting "queued for retry" can't tell whether to retry now or wait for plan close. A duplicate-commit failure (permanent) and a chain-down failure (transient) look the same.

**Suggested defense:** Return an error type with `IsTransient() bool`, or log structured fields (`plan_id`, `finding_id`, severity, error code).

### F-PERF-007: No backoff on `Queue.Enqueue` or chain-level retry (Suggestion)

**Scenario:** `internal/chain/queue.go:48-67` — When a real-time commit fails, `CommitRealtime` calls `queueFailure`. If queue enqueue itself fails (full disk, slow mount), the error returns up with no retry. The Sync layer hits the same transient error per-plan and fails hard on the first error.

**Why this matters:** Acceptable for CLI invocations (human retries). Bad for cron-driven `tribunal chain sync` in CI.

**Suggested defense:** Add a simple retry-with-backoff loop in `SyncPlan` / `SyncAll` (up to 3 retries, 5s backoff) for transient errors. Document that `tribunal chain sync` is idempotent.

## Cross-Reviewer Notes

- **For reviewer-arch**: The hybrid settlement flow is well-designed, but the missing batch-size bound couples Go client and Rust contract — a one-sided change could introduce gas overruns. Consider declaring the max batch size as a contract invariant and enforcing it on both sides.
- **For reviewer-sec**: Signature verification is per-item in a loop. No concern in itself, but unbounded batches mean an attacker could craft a 10k-item batch to burn gas on verification alone. Adding batch-size limits hardens against that.

## Verdict

**Request Changes**

The codebase is production-ready for moderate scale (1-100 agents, 100-1000 findings per plan), but three risks should be mitigated before scale-up:

1. **Required**: Add `MAX_BATCH_SIZE` constant to the contract and enforce it in both batch functions. Document the per-item gas budget.
2. **Recommended**: Align timeouts for different operation types or expose them via flags.
3. **Nice-to-have**: Add debug logging to `CommitRealtime` and document queue cleanup for operators.

All three changes are low-risk and can ship in a follow-up release.
