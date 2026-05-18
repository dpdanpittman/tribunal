# Tribunal

> _The unit of trust is not consensus — it is surviving adversarial scrutiny by identified agents whose history is on the public record._

Tribunal is a methodology and tooling for shipping LLM-assisted code without
inheriting LLM failure modes. It composes three layers:

1. A **process backbone** — state machine + spec-driven gates + role separation + git discipline.
2. A **correctness toolkit** — adversarial multi-model review on top of cooperative-parallel lens review, plus a verification pyramid.
3. An **on-chain incentive layer** — a soulbound reputation token on Burnt XION (CosmWasm) that tracks per-agent finding outcomes over time.

The methodology is a synthesis of two existing patterns:

- [`reliqlabs/colosseum`](https://github.com/reliqlabs/colosseum) — adversarial spec review + Rust-flavored verification pyramid.
- [`btspoony/mstar-harness`](https://github.com/btspoony/mstar-harness) — multi-role agent harness with spec-driven gates and lens-diverse QC trio.

The novel piece neither has: **reputation-weighted findings**. Agents have verifiable identities (ed25519 keypairs), every finding is signed and recorded, outcomes are settled by PMs/QA, and reputation deltas land on-chain.

**Live**: [tribunal.mabus.ai](https://tribunal.mabus.ai) — methodology, [sample audits](https://tribunal.mabus.ai/audits/), and the [on-chain leaderboard](https://tribunal.mabus.ai/leaderboard) that queries the deployed contract on `xion-testnet-2` from the browser.

## How it composes with clawpatch

As of v0.3.4+ ([ADR-0002](./docs/adr/0002-clawpatch-absorption.md), 2026-05-17), Tribunal's lens-parallel review stage can run through [clawpatch](https://github.com/openclaw/clawpatch) as a subprocess. The trust/discovery split:

- **Clawpatch** owns discovery — heuristic + agent-based feature mapping, per-feature LLM review, fix-and-revalidate loops.
- **Tribunal** owns trust — agent identity, ed25519-signed findings, adversarial multi-model review, PM/QA-settled outcomes, on-chain reputation.

Run `tribunal review --via-clawpatch` and the lens stage dispatches via clawpatch instead of expecting skill-trio reports on disk. Findings come back through `internal/clawpatch/translate.go`, get signed by Tribunal-orchestrator, and land in the existing ledger. Two upstream PRs ([#64 `--prompt-file`](https://github.com/openclaw/clawpatch/pull/64), [#65 `--export-tribunal-ledger`](https://github.com/openclaw/clawpatch/pull/65)) added the integration hooks; both merged 2026-05-18. `tribunal fix --finding <id>` and `tribunal revalidate` round-trip state back to clawpatch via signed triage events.

## Quick start

> Requires Go 1.23+. Optional: an Anthropic API key (`ANTHROPIC_API_KEY`) for the Claude adversary panel; v0.3+ adds Burnt XION on-chain settlement.

```bash
go install github.com/dpdanpittman/tribunal/cmd/tribunal@latest
tribunal init                            # scaffold .tribunal/ in the project
cp tribunal.yaml.example tribunal.yaml   # tune panels + verify stack to taste

# Verification pyramid (runs build / fmt / vet / test / ... per stack).
tribunal verify .

# Adversary review stage (lens-parallel trio is dispatched by your host
# harness; this command runs the adversary panel + writes signed findings
# to .tribunal/ledger.jsonl).
ANTHROPIC_API_KEY=sk-... tribunal review --plan P-42

# Inspect what's in the ledger.
tribunal ledger summary
tribunal ledger leaderboard

# v0.3: settle to Burnt XION. Deploy once, then sync per plan.
./scripts/deploy-contract.sh                  # produces a chain.yaml snippet
tribunal chain init --chain-id xion-testnet-2 --contract cosmwasm1... ...
tribunal chain register claude-adversary
tribunal chain sync --plan P-42
tribunal chain query leaderboard
```

## Status

This repo is in active development. v0.1 ships the methodology, CLI, skills/agents, local ledger, and a Go fizzbuzz example. v0.2 adds multi-model adversarial dispatch and the real verification pyramid. v0.3 adds the CosmWasm contract and on-chain settlement.

See [`CHANGELOG.md`](./CHANGELOG.md) for what's released, and [`docs/methodology.md`](./docs/methodology.md) for the design.

## Public site

[**tribunal.mabus.ai**](https://tribunal.mabus.ai) — landing, the methodology rendered with sidebar nav, the [P-v032-audit case study](https://tribunal.mabus.ai/audits/p-v032-audit) (Tribunal reviewing its own release), and a [live on-chain leaderboard](https://tribunal.mabus.ai/leaderboard) that queries the deployed contract on `xion-testnet-2` client-side. Source under [`site/`](./site/).

## Why Tribunal

LLMs are fast, broad, and characteristically unreliable. Code review, tests, and audit are human-bottlenecked and scale linearly with reviewer attention. LLM output scales 10–100× faster. That mismatch is the trust gap.

Most multi-agent systems being built today default to cooperative patterns: agents that help, vote, and converge. _That is the wrong primitive for correctness._ Cooperation amplifies shared mistakes. Adversaries hunt them.

But adversaries that are never held accountable hallucinate findings as freely as cooperators hallucinate code. Tribunal's bet: trust is a function of three things — surviving adversarial scrutiny, by agents with verifiable identity, whose history of findings is on the public record.

## Documents

- [Methodology](./docs/methodology.md) — the load-bearing design doc
- [Incentive mechanism](./docs/incentive-mechanism.md) — reputation math
- [On-chain protocol](./docs/on-chain-protocol.md) — CosmWasm contract surface (v0.3)
- [Installation](./docs/installation.md) — per-host setup
- [ADRs](./docs/adr/) — architecture decisions

## License

[MIT](./LICENSE).
