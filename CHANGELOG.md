# Changelog

All notable changes to Tribunal will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
