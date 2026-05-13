# Tribunal v0.3.0 — Audit Synthesis

**Date:** 2026-05-12
**Methodology:** Tribunal hybrid review (lens-parallel trio + adversarial gate)
**Scope:** `contracts/tribunal-reputation/`, `internal/chain/`, `cmd/tribunal/chain.go`, `scripts/`
**Reviewers:** `tribunal-reviewer-arch`, `tribunal-reviewer-sec`, `tribunal-reviewer-perf`, `tribunal-adversary`
**Final verdict:** **Request Changes** — 3 blockers, 6 warnings, 6 suggestions

## Outcome

Three lens reviewers + one adversary attack landed on a unified verdict: the v0.3 release works for cw-multi-test but has a **wire-format mismatch that will fail at first contact with a real chain** plus two other blockers worth fixing before mainnet. The methodology produced its expected shape: the trio caught the obvious problems (mismatches in numeric serialization); the adversary caught what the trio missed (rotation lifecycle, dead admin).

## Blockers (must fix before deploy)

### B1: Wire-format mismatch on `ResolutionRecord` and every `u128` field

**Surfaced by:** `F-ARCH-001` (critical), corroborated by `F-SEC-001` (critical) and `A-ADV-003` (critical).

The Rust contract uses bare `u128` for `balance`, `stake`, `rotation_floor`, `initial_balance`, `reward_applied`, and `outcome_reward_multiplier` (state.rs, msg.rs). The Go client treats every numeric wire field as `string` (the cosmwasm `Uint128` convention). Additionally, `ResolutionRecord` has a single `reward_applied: u128` field on Rust but the Go side declares two fields (`stake_returned`, `reward`).

**Root cause:** The Rust contract never adopted `cosmwasm_std::Uint128`. Bare `u128` serializes as a JSON number, which (a) trips JS clients on numbers > 2^53, and (b) doesn't match what the Go client expects to deserialize.

**Fix:** Change every bare `u128` in `state.rs` and `msg.rs` to `cosmwasm_std::Uint128`. Rename `reward_applied` to either two fields (`stake_returned: Uint128`, `reward: Uint128`) to match the Go side, OR collapse the Go side to a single `RewardApplied` field. Add a Go test that round-trips a hand-crafted contract response through `json.Unmarshal` for each query type to catch regressions (cw-multi-test doesn't exercise this path).

### B2: Input validation on `plan_id` / `finding_id` / `claim_hash`

**Surfaced by:** `F-SEC-002` (warning), sharpened by `A-ADV-002` (warning).

The contract uses `(plan_id, finding_id)` as a storage key with no length or charset validation. The canonical signing format is pipe-separated. A pipe character or NUL byte in any of these fields makes the canonical bytes ambiguous for downstream parsers — even though signature verification still works.

**Fix:** Reject `plan_id` / `finding_id` / `claim_hash` containing `|`, `\x00`, control chars, or exceeding bounded lengths (suggested: 64 / 64 / 128 chars). Enforce on both Rust and Go sides for fast-fail.

### B3: Stake type alignment (covered by B1)

The Go canonical builder takes `uint64`, the Rust contract takes `u128`. Today both produce identical bytes for stakes < 2^64. A protocol upgrade that increases stake range breaks signature verification silently. Folded into B1 — using `Uint128` (decimal-string) on both sides aligns this.

## Warnings (fix before mainnet, ship-acceptable on testnet)

### W1: Unsafe default `keyring_backend: "test"`

**Surfaced by:** `F-SEC-003`. Operators may deploy to mainnet without overriding the test-backend default, leaking the operator key.

**Fix:** Either remove the default entirely, or log a `WARNING` whenever the client is constructed with `keyring_backend: test` against a non-test chain ID.

### W2: Leaderboard query is O(n_agents)

**Surfaced by:** `F-PERF-001`. The query iterates every agent regardless of `limit`. Fine for 100 agents; not fine for 10k.

**Fix:** Add `MAX_AGENTS` cap (≈500) for v0.3, document the limit. Defer the sorted-index restructure to v0.4 if/when scale matters.

### W3: Batch operations accept unbounded `Vec<...>`

**Surfaced by:** `F-PERF-002`. Both `commit_finding_batch` and `resolve_finding_batch` will burn gas linearly with batch size.

**Fix:** Add `MAX_BATCH_SIZE = 100` on the contract. Document the gas budget per item.

### W4: HTTP client uses a flat 30s timeout

**Surfaced by:** `F-PERF-004`. Too long for `/status` probes, too short for tx broadcasts.

**Fix:** Either context-specific timeouts inside the client, or remove the http.Client timeout and rely on caller-supplied `context.Context` deadlines.

### W5: Old label remains queryable after rotation

**Surfaced by:** `A-ADV-001`. After `rotate_agent`, the old label still resolves to the retired pubkey. Labels are also burned forever — no other agent can claim them.

**Fix:** Either (a) `AGENTS_BY_LABEL.remove(old_label)` in `rotate.rs`, or (b) document the behavior in `docs/on-chain-protocol.md` and have the Go `AgentByLabel` query surface the `retired_at` field prominently.

### W6: No retry / partial-progress recovery for batch settlement

**Surfaced by:** `F-SEC-006`. `SyncPlan` submits commit batch + resolve batch as separate txs. If the second fails, half the plan is on-chain unresolved.

**Fix:** Add idempotent retry-with-backoff in `SyncPlan`. The contract already rejects double-commits and double-resolves, so retries are safe.

## Suggestions (tech debt)

### S1: `Config.admin` is stored but never checked

**Surfaced by:** `A-ADV-004`. Dead authority. Either remove until needed, or add a regression test asserting no `cfg.admin` references in `src/execute/`.

### S2: Misleading error code on batch plan-mismatch

**Surfaced by:** `F-SEC-004`. `FindingAlreadyCommitted` is returned when the real issue is `plan_id` mismatch. Add `ContractError::BatchMixedPlanID`.

### S3: Retired-agent rotation semantics undocumented

**Surfaced by:** `F-SEC-005`. Resolutions of pre-rotation findings credit the retired agent record, not the successor. Intentional per spec, but undocumented in code.

### S4: Plan-batch invariant uncommented in Go

**Surfaced by:** `F-ARCH-005`. Add a comment at `internal/chain/sync.go:89` documenting the per-plan invariant.

### S5: Deploy script doesn't fail-fast if xiond is missing

**Surfaced by:** `F-ARCH-003`. Add `command -v $XIOND` check before the cargo build.

### S6: No Go integration test exercising contract JSON responses

**Surfaced by:** `F-ARCH-002`, `F-ARCH-004`. The Go tests stub HTTP; they don't exercise unmarshaling of real contract output. Add fixtures from `xiond query wasm smart` (or generate them in the contract tests) and assert Go can read them.

### S7: Real-time commit error classification

**Surfaced by:** `F-PERF-006`. `CommitRealtime` failures don't distinguish transient vs. permanent.

### S8: Queue drain is O(n) per call

**Surfaced by:** `F-PERF-003`. Acceptable for small queues; document operator cleanup procedure.

## Methodology observations

- **Lens parallelism worked.** Three reviewers ran genuinely in parallel via independent Agent dispatches. Total wall time: ~140 seconds for the three lens reviews.
- **Lens overlap was minimal.** Arch and Sec both caught the wire mismatch but from different framings (Arch: type-system level, Sec: cryptographic-impact level). That redundancy strengthens confidence, not waste.
- **The adversary earned its keep.** A-ADV-001 (label cleanup) and A-ADV-004 (dead admin) are pure shared-blind-spot findings — no lens reviewer has "agent-record lifecycle" or "permission-system dead-code" as its primary scope.
- **The contract integration tests gave false confidence.** All 15 cw-multi-test tests passed, but the wire mismatch would have failed at first contact with real LCD JSON. **Cw-multi-test does not exercise the JSON serialization boundary.** This is the most important methodology takeaway: the contract-side test suite needs Go-side wire roundtrip tests to be trustworthy.

## Recommended next action

Open three follow-up tickets for B1 / B2 / B3-via-B1, and one for W1 (the operational risk). The remaining warnings can land as part of a v0.3.1 maintenance release. Suggestions are tech-debt backlog material.

Files in this audit packet:
- `reviewer-arch.md`
- `reviewer-sec.md`
- `reviewer-perf.md`
- `adversary.md`
- `SYNTHESIS.md` (this file)
