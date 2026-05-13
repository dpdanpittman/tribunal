# Changelog

All notable changes to Tribunal will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.2] — 2026-05-13

Devnet-driven tooling release. The contract itself shipped clean in v0.3.1 — every audit fix verified end-to-end against a live `xion-devnet-1` chain (`wasmd v0.54.0`, `xiond v20.0.0`). What didn't ship clean was the deploy + sync tooling around it. v0.3.2 fixes six defects surfaced by the first real-chain test run. No contract changes; no migration required. Test-run report at `.tribunal/reports/devnet-e2e-2026-05-13.md`.

### Fixed

- **`scripts/deploy-contract.sh`: `--optimize` is now the default, not opt-in.** The dev-built wasm fails on wasmd v0.54+ with `Wasm bytecode could not be deserialized. Deserialization error: "bulk memory support is not enabled"`. The optimizer pass strips those ops and is required against any modern chain. New `--skip-optimize` flag for environments without docker; raw `cargo build` is now the escape hatch instead of the default. ([F1])
- **`scripts/deploy-contract.sh`: bumped `cosmwasm/optimizer` from `0.16.0` to `0.17.0`.** The 0.16.0 image ships Rust 1.78.0, which can't build `base64ct v1.8.3` (transitive through `cosmwasm-std`) because that crate requires `edition2024` (Cargo 1.85+). 0.17.0 ships a new-enough toolchain. Image tag is now also overridable via `OPTIMIZER_IMAGE` env. ([F2])
- **`tribunal chain init` normalizes `tcp://` RPC URLs to `http://`.** xiond accepts `tcp://localhost:26657` (Tendermint convention); Go's `net/http` does not. Without this, every `chain.*` command after init fails with `unsupported protocol scheme "tcp"`. The rewrite is logged to stderr so the user sees what happened. ([F3])
- **`internal/chain.Client.Execute` now waits for tx inclusion.** Previously, `--broadcast-mode sync` only confirmed mempool acceptance; back-to-back Executes (e.g. `register` then `sync`, or sync's own commit→resolve pipeline) hit `account sequence mismatch, expected N, got N-1` because xiond's cached sequence was stale. The client now polls `/cosmos/tx/v1beta1/txs/{hash}` on a 1s cadence until the tx lands or ctx is cancelled. ([F4])
- **`tribunal chain sync` is now idempotent against partial failure.** Before building a commit_finding_batch or resolve_finding_batch, sync queries the contract for each finding's current state and filters out entries already on-chain. Retrying after a partial failure no longer dies with `finding P-X/F-Y already committed`. Pre-flight query failures are tolerated (treated as "unknown") so flaky REST doesn't block sync — the contract's own duplicate guard remains the final authority. ([F5])
- **`tribunal chain init` auto-populates `outcome_reward_multiplier` from the deployed contract.** Previously it wrote `0` regardless of what the contract had; the field is documented as a client-side preview but was lying. Now `chain init` queries `Config{}` and writes the real value. Falls back with a warning if the chain is unreachable at init time. ([F6])

### Internal

- New `cmd/tribunal-seed/` — small throwaway harness used to seed the local ledger with a signed finding + resolution for e2e testing against a running chain. Not user-facing; useful for reproducing the test-run report and verifying future tooling fixes.

## [0.3.1] — 2026-05-12

Audit-driven fix release. The v0.3.0 contract works under `cw-multi-test`
but had a wire-format mismatch with the Go client that `cargo test` did
not catch (cw-multi-test bypasses the JSON boundary). The Tribunal
methodology's own audit — three lens reviewers + one adversary — surfaced
this plus the related issues below. Full audit packet at
`.tribunal/reports/audit-v0.3.0/`.

### Fixed

- **Wire-format alignment between Rust contract and Go client.** Every
  numeric on-chain field migrated from bare `u128` to
  `cosmwasm_std::Uint128`, which serializes as a decimal string — the
  shape the Go client already expected. Affected: `AgentRecord.balance`,
  `FindingState.stake`, `Config.{initial_balance, rotation_floor,
outcome_reward_multiplier}`, plus the corresponding query response
  types. Also adds `pubkey` to `AgentResp` and `retired` to
  `ReputationResp` to match the Go-side shape. ([audit F-ARCH-001,
  F-SEC-001, A-ADV-003])
- **`ResolutionRecord` split.** Replaced the single `reward_applied`
  field with `stake_returned` + `reward` so consumers can distinguish
  stake-return from outcome-reward without re-deriving from severity.
  ([audit F-ARCH-001])
- **Canonical signing format uses string stake.** The Go
  `CanonicalFindingMessage` signature changed from `stake uint64` to
  `stake string` so the canonical bytes mirror the contract's
  `Uint128`-as-decimal-string representation identically regardless of
  magnitude. ([audit F-SEC-001, A-ADV-003])
- **Timestamps deserialize as strings on the Go side.** `created_at` /
  `retired_at` / `committed_at` / `resolved_at` are decimal strings of
  nanoseconds (the way `cosmwasm_std::Timestamp` serializes). Go
  `uint64` → `string`. ([audit follow-up])
- **`tp_count` / `fp_count` aligned to `uint64`.** Was `uint32` on the
  Go side; Rust uses `u64`. ([audit follow-up])
- **Rotation removes the old label binding.** `rotate_agent` now calls
  `AGENTS_BY_LABEL.remove(old_label)` so label lookups no longer
  resolve to a retired record. The retired `AgentRecord` keyed by
  pubkey is preserved with `retired_at` + `superseded_by` — the
  accountability trail survives. The new label is allowed to equal the
  old one. ([audit A-ADV-001])
- **`BatchMixedPlanID` error variant.** Replaces the misleading
  `FindingAlreadyCommitted` that batch-mismatch checks previously
  returned. Carries `batch_plan_id`, `found_plan_id`, `finding_id` for
  debuggability. ([audit F-SEC-004])
- **Keyring warning.** `Client.New()` logs a one-line warning to stderr
  when `keyring_backend=test` is combined with a non-test chain id
  (chain id not containing "devnet", "testnet", "test", or "local").
  ([audit F-SEC-003])

### Added

- **Identifier validation** (`contracts/tribunal-reputation/src/validate.rs`,
  `internal/chain/validate.go`). Rejects `plan_id` / `finding_id` /
  `claim_hash` / `evidence_hash` / `label` / `model_id` / `reason` that
  contain `|`, NUL, or any ASCII control character; enforces length
  caps (64 chars for IDs / labels, 128 for hashes / model IDs, 256 for
  rotation reason). Enforced symmetrically by the contract and the Go
  builders. ([audit F-SEC-002, A-ADV-002])
- **`MAX_BATCH_SIZE = 100`** on `commit_finding_batch` /
  `resolve_finding_batch`. Returns `BatchTooLarge` if exceeded so a
  malformed client can't burn gas linearly. ([audit F-PERF-002])
- **Go wire-roundtrip tests** at `internal/chain/wire_test.go`. Hand-crafted
  fixtures cover every response shape against the v0.3.0 wire format —
  including the resolved-finding path that broke in v0.3.0. ([audit
  F-ARCH-002, F-ARCH-004, methodology lesson])

### Deferred (still open from the audit)

- **`Config.admin` is stored but never checked.** Latent permission
  surface. ([audit A-ADV-004]) — kept until there's a use.
- **Leaderboard remains O(n_agents).** ([audit F-PERF-001])
- **HTTP timeouts remain flat 30s.** ([audit F-PERF-004])
- **No retry / partial-progress recovery for batch settlement.**
  ([audit F-SEC-006])

## [0.3.0] — 2026-05-12

### Added

- **CosmWasm reputation contract** (`contracts/tribunal-reputation/`): single-tenant, soulbound reputation token on Burnt XION. Each agent is identified by a 32-byte ed25519 pubkey + unique label + role. Findings and resolutions are signed off-chain (`ed25519_verify` in the contract) and persisted as `FindingState` keyed by `(plan_id, finding_id)`. Stake debited on commit; on resolve, `true_positive` returns stake + `stake × outcome_reward_multiplier`, `false_positive` keeps the stake slashed, `stale_duplicate` / `indeterminate` return the stake with no reward. Rotation preserves TP/FP history but resets balance to the configured `rotation_floor`. Built against `cosmwasm-std` 2.x, `cw-storage-plus` 2.x, `cw-multi-test` 2.x. 15 integration tests cover every execute path + every error path.
- **Go chain client** (`internal/chain/`): mirrors the contract surface. Hybrid settlement protocol — `Sync.CommitRealtime` for critical findings (with a JSONL retry queue at `.tribunal/chain-queue.jsonl` on failure), `Sync.SyncPlan` / `Sync.SyncAll` for plan-close batched commits + resolutions. Transports split: txs shelled out to `xiond tx wasm execute` (so users keep one keyring for all XION ops), queries direct HTTP to the LCD REST endpoint. `xiond_binary` config accepts prefix args (e.g. `docker exec devnet-xion-1 xiond`) for contributors running the devnet in containers. Canonical signing messages (`TRIBUNAL_FINDING|...`, `TRIBUNAL_RESOLUTION|...`) are byte-identical to the Rust contract helpers.
- **`tribunal chain` CLI tree**: `init` (writes `~/.tribunal/chain.yaml`), `status` (RPC health + height), `register <label>`, `sync [--plan <id>]`, `query {reputation,agent,finding,leaderboard,config}`, `rotate <old> <new>`, `queue {list,clear}`. Reputation / agent lookups accept either an `ed25519:<hex>` pubkey or a local label.
- **Deploy scripts**: `scripts/deploy-contract.sh` (cargo wasm → optional `cosmwasm/optimizer` pass → `xiond tx wasm store` + instantiate → emits a `chain.yaml` snippet to stdout). `scripts/init-testnet.sh` probes a locally-running XION devnet and emits the env-var exports the deploy script needs.
- **Contracts CI workflow** (`.github/workflows/contracts.yml`): `cargo fmt --check`, `cargo clippy -D warnings`, `cargo test --release`, `cargo wasm`, `cosmwasm-check` on the built artifact.
- **On-chain protocol doc** at `docs/on-chain-protocol.md` covering contract surface, wire format, signing canonicalization, gas considerations, and the hybrid settlement flow.

### Changed

- Repo gitignore ignores `contracts/*/target/` and `contracts/*/artifacts/` but commits `Cargo.lock` (binary-crate convention).
- Root CLI help lists the v0.3 chain workflow.

### Not yet implemented (v0.4+)

- Multi-org tenancy (single global agent registry for now).
- Cross-chain reputation (XION only).
- Slashing appeals (resolutions are final; PMs can re-file but old ones stay in the audit trail).
- Fungible operator rewards.
- Web leaderboard dashboard.

## [0.2.0] — 2026-05-12

### Added

- **Verification pyramid** (`internal/verify`): `tribunal verify` runs a halt-on-failure pyramid of language-appropriate correctness tools. Go stack ships ordered layers `go-build → go-fmt → go-vet → (staticcheck) → (golangci-lint) → go-test → (go-fuzz)` with per-layer `LayerResult` + an aggregated `PyramidReport`. Rust + TypeScript stack stubs included; replace with real wiring as the corresponding examples land.
- **Adversary dispatch** (`internal/dispatch`): pluggable `Provider` interface, parallel orchestrator (`Dispatch`), thread-safe `Registry`, and structured `Synthesis` over N panel members (shared / unique / divergent findings + overall verdict). Includes report parser tolerant of formatting variation.
- **Diversity buckets** (`internal/dispatch/bucket.go`): configurable diversity axis via tribunal.yaml — `vendor_family`, `temperature_band`, `focus`, `model_tier`, or `composite:axis1,axis2,...`. Methodology now treats diversity as a spectrum rather than vendor-only; reputation ledger can empirically measure which axis pays off.
- **Default adversary panels** (`internal/dispatch/panel.go`): documented `default_panel` (three Claude variants, cost-efficient) and `high_stakes_panel` (cross-vendor opt-in) that load from tribunal.yaml's `adversary:` section.
- **Claude provider** (`internal/dispatch/claude.go`): direct HTTP to Anthropic's `/v1/messages`. Reads `ANTHROPIC_API_KEY`. Tests use `httptest` stubs.
- **Hybrid review orchestration** (`internal/review/hybrid.go`): given a `--plan` ID, locates `.tribunal/plans/<id>/intent.md` + `plan.md`, reads trio reviewer reports from `.tribunal/reports/<id>/`, computes the diff via git, dispatches the configured panel, persists per-member adversary reports + synthesis JSON, and appends signed findings to `.tribunal/ledger.jsonl` (auto-registering missing adversary keypairs on first use).
- **CLI additions**: `tribunal verify` (pyramid runner), `tribunal dispatch test` (panel inspection), `tribunal dispatch attack` (raw adversary fan-out against stdin), `tribunal review` (real adversary-stage orchestration). Exit codes encode verdicts: `0` SURVIVES, `3` BREAKS, `4` INDETERMINATE.
- **`tribunal.yaml.example`** at the repo root documenting every configurable knob.
- **CI**: pyramid smoke-test runs against the fizzbuzz demo; dispatch panel inspection runs against the default + high-stakes panels on every push.

### Changed

- Methodology + skills + agents updated to describe **diversity-as-spectrum** rather than vendor-family-only.
- `dispatch` mock provider uses `atomic.Int64` for call counts; race detector clean.

### Not yet implemented (v0.3+)

- OpenAI / Google / LM Studio providers (the high-stakes panel currently degrades to per-member INDETERMINATE for missing providers without crashing).
- Burnt XION CosmWasm reputation contract + chain settlement.

## [0.1.0] — 2026-05-12

### Added

- Methodology document at `docs/methodology.md` — process backbone + hybrid review + verification pyramid + incentive layer.
- Companion docs: `incentive-mechanism.md` (reputation math), `installation.md` (per-host setup).
- Go libraries under `internal/`:
  - `agent`: ed25519 keypair generation, on-disk registry (~/.tribunal/agents/), role enum, rotation.
  - `ledger`: signed Finding / Resolution types, canonical-JSON signing, append-only JSONL store, reputation calculation with exponential decay + family-diversity bonus, gate decisions.
- `tribunal` CLI: `init`, `agents {add,list,show,rotate}`, `ledger {summary,leaderboard,find,verify}`, `review` (stub for v0.2).
- 7 installable skills in `skills/`: `tribunal-{intent,plan,implement,review,verify,classify,incentive}` with the canonical methodology workflow.
- 9 installable agents in `agents/`: `tribunal-{pm,architect,implementer,reviewer-arch,reviewer-sec,reviewer-perf,adversary,classifier,qa}`.
- End-to-end demo at `examples/go-fizzbuzz-verified/` — intent doc, implementation, table-driven + invariant + fuzz tests, and a populated `.tribunal/` directory with 4 signed findings + 4 signed resolutions across 6 agents.
- `cmd/seed-fizzbuzz-demo/` — deterministic seed generator for the demo; CI verifies the seed is byte-stable.
- GitHub Actions CI: gofmt, vet, build, test (race detector) on Linux + macOS; CLI smoke test; example module test; demo-determinism check.

### Not yet implemented (v0.2 / v0.3)

- Multi-model adversary dispatch via `external-model-mcp` / `lm-studio-mcp` (v0.2).
- Full verification pyramid orchestration (v0.2).
- Burnt XION CosmWasm reputation contract + chain settlement (v0.3).
