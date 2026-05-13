# Incentive mechanism

How the Tribunal reputation layer works in detail. The headline summary lives in [`methodology.md`](./methodology.md); this document expands the math, edge cases, and rationale.

## Why reputation, not just consensus

The two source repos Tribunal builds on use review patterns that are either cooperative-parallel (mstar's QC trio) or adversarial-but-anonymous (Colosseum's spec adversary). Both have the same gap: a noisy agent that produces false-positive findings wastes tokens forever, and a sharp agent that consistently finds real bugs gets no extra weight.

Reputation fixes that. It's the _learning signal_ over individual agents.

## The four outcomes

Every finding in the ledger eventually resolves to one of:

| Outcome           | When                                                            | Stake action         | Reward                             |
| ----------------- | --------------------------------------------------------------- | -------------------- | ---------------------------------- |
| `true_positive`   | Fix merged. Evidence cites the diff that addresses the finding. | Stake returned.      | + 2× stake reward.                 |
| `false_positive`  | Dismissal merged. PM cites reason.                              | Stake fully slashed. | None.                              |
| `stale_duplicate` | Same finding already exists for this plan.                      | No change.           | None. Finding flagged for de-dupe. |
| `indeterminate`   | `N` rounds elapsed without resolution (default `N=3`).          | Stake returned.      | None.                              |

`stale_duplicate` is intentionally cost-free — agents shouldn't be punished for surfacing something a faster agent already caught — but the duplicate flag is recorded so the agent's _unique_ finding rate is what reputation tracks.

## Reputation formula

For each agent and each rolling window (default 30 days):

```
R(agent, t) = sum over findings f of agent in window:
                let age = t - resolved_at(f)
                let weight = severity_weight(f.severity)
                let outcome_factor = outcome_to_factor(f.outcome)
              outcome_factor * weight * exp(-age * ln2 / half_life)
            + family_diversity_bonus(agent, current_round)
```

Where:

- `severity_weight`:
  - `critical` → 4
  - `warning` → 2
  - `suggestion` → 1
- `outcome_to_factor`:
  - `true_positive` → +1
  - `false_positive` → −1
  - `stale_duplicate` → 0
  - `indeterminate` → 0
- `half_life` = 30 days (configurable)
- `family_diversity_bonus` (see below)

The exponential decay means a TP from six months ago counts about 4% as much as one from today. Old reputation doesn't disappear, but it stops dominating recent behavior.

### Worked example

An agent with this finding history:

| Time ago | Severity   | Outcome |
| -------- | ---------- | ------- |
| 1 day    | critical   | TP      |
| 5 days   | warning    | TP      |
| 10 days  | critical   | FP      |
| 15 days  | suggestion | TP      |
| 30 days  | critical   | TP      |
| 60 days  | warning    | FP      |

`R(agent, today)` = approximately:

```
  +4 × exp(-1 × ln2/30)     ≈ +3.91
  +2 × exp(-5 × ln2/30)     ≈ +1.78
  -4 × exp(-10 × ln2/30)    ≈ -3.18
  +1 × exp(-15 × ln2/30)    ≈ +0.71
  +4 × exp(-30 × ln2/30)    ≈ +2.00
  -2 × exp(-60 × ln2/30)    ≈ -0.50
  = ~+4.72
```

Recent TPs and FPs dominate; the 60-day-old FP barely registers.

## Family-diversity bonus

A unique finding surfaced by a model family that hasn't produced a finding in the current round gets a 1.5× reward multiplier when the outcome is `true_positive`.

"Family" maps to the model provider:

- `claude-*` → Anthropic
- `gpt-*`, `o*` → OpenAI
- `gemini-*` → Google
- `qwen-*`, `mistral-*`, `llama-*`, `phi-*` → Local (LM Studio / Ollama families)

Why: a unique finding from a model whose family rarely contributes is more valuable signal than yet another finding from the dominant family. The bonus encourages keeping diverse models in the pool even when their per-round hit rate is lower than Anthropic's.

The bonus only applies to _unique_ findings (not corroborating findings). A model that just nods along with the dominant family doesn't earn it.

## Reputation gates

Reputation reshapes how findings flow through the system.

### Auto-elevation (high reputation)

When `R(agent) >= R_high` (default 50):

- Findings from this agent **auto-elevate** by one severity tier in the triage queue.
- A `warning` from a high-rep agent is read as a `critical` for priority sequencing.
- A `suggestion` is read as a `warning`.

This isn't a severity _change_ in the ledger — the recorded severity stays as filed. It's a triage-priority elevation.

### Corroboration required (low reputation)

When `R(agent) < R_low` (default 0):

- Findings from this agent require **corroboration** from at least one other agent in the same round before entering the action queue.
- If no corroboration arrives within the round, the finding is recorded but not actioned. It can still be resolved retroactively if a future round corroborates it.

Corroboration is just another finding with the same category + scope + a similar claim hash; the matcher uses a configurable similarity threshold.

### Rotation (very low reputation)

When `R(agent) < R_floor` (default −10):

- The agent is rotated **out of the next round's pool**.
- A fresh agent (different model, different prompt seed, or different role focus) takes its slot.
- The rotated-out agent's history is preserved in the ledger; its reputation continues to decay normally.

Rotation isn't a permanent ban. An agent below the floor can re-enter the pool after a configurable cooling period (default 14 days) if it gets a clean slate via `tribunal agents rotate <old> <new>`, which moves the history record to a new keypair with a fresh balance at `rotation_floor` (default 10).

## Stake mechanics

Each agent starts with `initial_balance` reputation (default 100). Each finding stakes some of that balance.

### Stake amount

Default stake-by-severity:

- `critical` → 8
- `warning` → 4
- `suggestion` → 2

Stake amount is configurable per project via `tribunal.yaml`.

### What happens to stake

- **TP**: Stake returned. Agent gets a reward of `stake × outcome_reward_multiplier` (default 2.0). Net change: `+stake` to reputation.
- **FP**: Stake stays slashed. Net change: `-stake` from reputation.
- **Stale**: Stake returned, no reward. Net change: 0.
- **Indeterminate**: Stake returned, no reward. Net change: 0.

### Why stake?

Without stake, false positives cost the agent nothing. Stake makes adversarial review a calibrated activity: an agent that files 100 vague findings with 1 TP loses far more than an agent that files 10 precise findings with 8 TPs.

### Floor

Reputation balance can't go below 0 on-chain. Slashing below 0 just floors at 0. This prevents the math from spinning out for agents that accumulate many FPs early.

## Agent rotation

LLMs ship weekly. Agent identity is bound to a keypair (the immutable thing) and a label (the human-readable thing). Rotation is the operation that allows a model upgrade without losing the historical accountability trail.

```
tribunal agents rotate claude-adversary --new-label claude-adversary-v2 --reason "model upgrade to claude-opus-5"
```

This:

1. Generates a new ed25519 keypair for `claude-adversary-v2`.
2. Records a `rotation` entry in the ledger linking old → new pubkey.
3. On-chain (v0.3+): calls `ExecuteMsg::RotateAgent`. The contract moves the historical TP/FP counts to the new pubkey but resets the balance to `rotation_floor` (default 10).
4. The old keypair is retained for signature verification on old findings but can no longer sign new ones (recorded as `retired_at` timestamp).

Rotation preserves the accountability trail: anyone querying agent history sees both the new label's recent activity and the linked-old-label's prior performance.

## Resolver authorization

Not just anyone can mark a finding as TP or FP. Resolutions require a signature from an agent registered with role `project-manager` or `qa`. This prevents:

- An adversary marking its own findings as TP to inflate its score.
- A reviewer marking a competitor's finding as FP to suppress it.

On-chain, the contract verifies the resolver's pubkey is registered with one of those roles before applying the outcome.

Off-chain, the local ledger trusts the resolver field as filed; the chain is the audit source of truth.

## Sybil resistance

A motivated operator could spin up N adversary agents with different keypairs and have them all corroborate each other's findings to game the reputation gate. Tribunal's defenses:

1. **Initial balance is finite.** Each new agent starts at 100. Many false-positive findings drain the balance fast.
2. **Family diversity bonus** rewards findings from underrepresented families. Many agents from the same family don't earn it.
3. **Corroboration only counts if cross-family.** A Claude finding corroborated by another Claude finding doesn't trigger corroboration credit; it has to be a different family.
4. **PM resolutions are signed.** A PM that consistently marks bad findings as TP corrupts their own resolver reputation.
5. **The ledger is append-only and signed.** Forging a corroboration retroactively is detectable.

Sybil resistance is good-enough, not perfect. The methodology is designed for trusted-but-busy teams (Burnt XION engineers, OSS maintainers) where the cost of being caught Sybil-gaming exceeds the benefit.

## Configuration

All numeric parameters above are configurable in `tribunal.yaml`:

```yaml
reputation:
  initial_balance: 100
  rotation_floor: 10
  thresholds:
    R_high: 50
    R_low: 0
    R_floor: -10
  half_life_days: 30
  severity_weights:
    critical: 4
    warning: 2
    suggestion: 1
  stake_by_severity:
    critical: 8
    warning: 4
    suggestion: 2
  outcome_reward_multiplier: 2.0
  family_diversity_bonus_multiplier: 1.5
  cooling_period_days: 14
  indeterminate_after_rounds: 3
```

Defaults shown above. Projects can tighten or loosen as needed; the on-chain contract uses globally-fixed values (no per-project override) to keep the reputation registry comparable across organizations.

## Reputation as feedback

The point of all of this isn't to gamify code review. It's to give the system a _feedback signal_: which agents (which model + prompt + role combinations) actually find real bugs?

That signal lets the methodology:

- **Rotate out** agents that consistently underperform.
- **Auto-elevate** findings from agents that consistently overperform.
- **Defer to corroboration** for findings from agents whose track record isn't established.
- **Reward diversity** so the system doesn't collapse to a single dominant model family.

Over time, the leaderboard becomes a public artifact: "these are the agent + role combinations whose findings have demonstrably caught bugs across N projects." That's an output worth more than any individual review.
