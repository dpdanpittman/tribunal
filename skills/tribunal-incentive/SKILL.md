---
name: tribunal-incentive
description: Explain and operate the Tribunal incentive layer — signed findings, signed resolutions, reputation gates, and (v0.3+) on-chain settlement. Use when an agent files a finding, when a PM/QA resolves one, or when the user wants to understand why a reviewer's finding was elevated, required corroboration, or didn't enter the action queue.
---

You are operating the Tribunal incentive layer. Every finding is signed by its author agent's keypair. Every resolution is signed by a PM or QA agent's keypair. The local ledger records everything; v0.3+ settles to Burnt XION.

## When to invoke

- An agent files a finding → record it signed to `.tribunal/ledger.jsonl`.
- A PM or QA closes a finding → record a signed resolution.
- A user asks why a finding was treated a certain way → compute current reputation, decide gate, explain.
- A PM closes a plan → run `tribunal ledger sync` (v0.3+) to batch the plan's findings/resolutions to chain.

## Filing a finding

Required fields (from `internal/ledger/Finding`):

- `finding_id` — globally unique, e.g. `F-001`.
- `plan_id` — which plan this finding belongs to.
- `round` — review round (1, 2, 3, ...). New finding in this plan's first review → 1.
- `agent_pubkey` / `agent_label` — from the filing agent's keypair.
- `severity` — `critical` / `warning` / `suggestion`.
- `category` — one of the well-known categories from `tribunal-review` or a custom string.
- `claim_hash` / `claim_uri` — sha256 hash of the finding's full text + path to that text in `.tribunal/findings/`.
- `stake` — defaults from severity: critical=8, warning=4, suggestion=2.

The full finding text (the human-readable claim, reproduction, suggested defense) lives at `.tribunal/findings/<finding-id>.md`. The ledger entry only stores the hash + URI, so the ledger stays small.

After filling in the fields, **sign with the agent's keypair** and append via `tribunal` CLI or SDK.

## Closing a finding

Required fields (from `internal/ledger/Resolution`):

- `finding_id` — the finding being resolved.
- `plan_id` — same plan.
- `outcome` — `true_positive` / `false_positive` / `stale_duplicate` / `indeterminate`.
- `resolver_pubkey` / `resolver_label` — must be an agent with role `project-manager` or `qa`.
- `evidence_hash` / `evidence_uri` — sha256 + path to the merged fix diff (TP), dismissal note (FP), duplicate-of pointer (stale), or staleness note (indeterminate).
- `reward` — computed from outcome and stake. TP = 2× stake. Others = 0.

Sign with the resolver's keypair and append.

## Reputation gates (operational)

Before treating a finding as actionable, consult `tribunal ledger summary` for the filing agent's current score and the configured thresholds:

- `R >= R_high` (default 50) → auto-elevate severity by one tier for triage. Recorded severity unchanged.
- `R_low <= R < R_high` → normal flow.
- `R < R_low` (default 0) → require corroboration from a different-family agent in the same round.
- `R < R_floor` (default -10) → recorded but agent rotates out of next round's pool.

These decisions are queryable: `tribunal ledger gate <agent-label>` returns the decision.

## Diversity bonus

When settling a true positive, check whether the finding's _diversity bucket_ was the only bucket to surface that bug class in the round. If so, apply a 1.5× reward multiplier.

The diversity bucket is configurable per project (see `docs/incentive-mechanism.md`). Reasonable choices include:

- **`vendor_family`** — Anthropic / OpenAI / Google / local. Most theoretically strong, most expensive. Use for high-stakes opt-in panels.
- **`temperature_band` + `focus`** — `(deterministic, spec)`, `(creative, impl)`, etc. Useful for multi-Claude panels (the v0.2 default).
- **`model_tier`** — Opus / Sonnet / Haiku, GPT-5 / Gemini-Pro, large-local / small-local.

The bonus encourages variance along whatever axis you've configured. The reputation ledger then _measures_ which axis actually pays off — over time, you can decide whether the cost of vendor diversity is worth it for your project.

## On-chain settlement (v0.3+)

At plan close:

1. Run `tribunal ledger sync --plan <id>`.
2. The CLI batches all signed findings + resolutions for the plan and submits them to the Tribunal Reputation contract on Burnt XION.
3. The contract verifies signatures, applies reputation deltas, and updates the on-chain leaderboard.

The local ledger remains the day-to-day source of truth; the chain is the audit anchor.

## What you do not do

- You do not file an unsigned finding. The CLI refuses unsigned writes.
- You do not let a non-PM, non-QA agent file a resolution. Resolutions are signed and the contract enforces resolver role.
- You do not retroactively edit ledger lines. The ledger is append-only.
- You do not soften reputation gates because a particular agent is "important." The thresholds apply uniformly. If an agent's reputation is too low, rotate it.

## Spirit

The incentive layer makes the methodology _learn_. Every finding is data. Every resolution is a labelled outcome. Over time, the reputation snapshot becomes the system's read on which agent + role combinations are worth listening to.
