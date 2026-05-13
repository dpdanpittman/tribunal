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
```

## Status

This repo is in active development. v0.1 ships the methodology, CLI, skills/agents, local ledger, and a Go fizzbuzz example. v0.2 adds multi-model adversarial dispatch and the real verification pyramid. v0.3 adds the CosmWasm contract and on-chain settlement.

See [`CHANGELOG.md`](./CHANGELOG.md) for what's released, and [`docs/methodology.md`](./docs/methodology.md) for the design.

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
