# The Tribunal Methodology

> _"The unit of trust is not consensus — it is surviving adversarial scrutiny by identified agents whose history is on the public record."_

Tribunal is a methodology for shipping LLM-assisted code without inheriting LLM failure modes. It composes three layers:

1. A **process backbone** — state machine, spec-driven gates, role separation, git discipline — that constrains agents to a well-defined workflow.
2. A **correctness toolkit** — adversarial multi-model review on top of cooperative-parallel lens review, plus a verification pyramid — that maximizes the chance of catching bugs and hallucinations before merge.
3. An **on-chain incentive layer** — a soulbound reputation token on Burnt XION — that tracks per-agent finding outcomes over time, slashes noisy agents, and rewards agents that find real bugs.

The first two are syntheses of two existing repos (`reliqlabs/colosseum` and `btspoony/mstar-harness`); the third is the novel piece that makes the system learn over time.

## The trust gap

LLMs are fast, broad, and characteristically unreliable. Even frontier models at high effort produce hallucinated APIs, subtle reasoning errors, plan drift, and confidently-wrong answers. Traditional correctness mechanisms — code review, human-written tests, manual audit — are human-bottlenecked and scale linearly with reviewer attention. LLM output scales 10–100× faster. That mismatch is the trust gap.

Most agent systems being built today default to cooperative multi-agent patterns: agents that help, vote, converge. _That is the wrong primitive for correctness._ Cooperation amplifies shared mistakes. Adversaries hunt them.

But adversaries that are never held accountable can hallucinate findings as freely as cooperators can hallucinate code. A noisy adversary that produces false positives wastes tokens and trains the team to ignore review feedback. The system needs a way to learn which agents are worth listening to.

Tribunal's thesis: **trust is a function of three things — surviving adversarial scrutiny, by agents with verifiable identity, whose history of findings is on the public record.**

## The four guarantees

Tribunal-managed code carries four mechanical guarantees, each enforced by a different layer:

1. **Process discipline** — every non-trivial change passes through `specify → clarify → plan → tasks → implement → review → verify`. No silently-skipped gates. State transitions logged.
2. **Lens-diverse review** — every diff is reviewed in parallel by three reviewers with non-overlapping lenses: architecture, security, performance.
3. **Adversarial review** — every proposed approval is then attacked by a hostile reviewer whose only job is to find what the trio missed. Multi-model when stakes warrant.
4. **Reputation-weighted findings** — findings are signed by their author agent, recorded in an append-only ledger, settled by outcome, and reflected as on-chain reputation. Low-reputation agents' findings require corroboration; high-reputation agents' findings auto-elevate.

## The process backbone

Inherited from `mstar-harness`, simplified for Tribunal.

### State machine

```
Todo → InProgress → InReview → (Done | Blocked)
```

Only `@project-manager` (or `@qa` after verification) can set `Done`. Implementing agents move to `InReview` after self-checks pass.

### Spec-driven dual-phase gate

**Prepare**: `specify → clarify → plan`

- `specify` produces the intent doc — problem, scope, acceptance.
- `clarify` collapses ambiguity; high-impact ambiguity must be resolved or block.
- `plan` produces technical approach with explicit module/interface contracts, risk register, and verification plan.

**Execute**: `plan(locked) → tasks → implement`

- `plan(locked)` freezes the baseline. New constraints during implementation must be written back to the plan before continuing.
- `tasks` decomposes plan into ordered work items with completion criteria.
- `implement` produces diffs + self-check evidence.

A **hotfix lane** allows compressed `specify(min) → plan(min) → implement` but requires post-hoc `clarify/RCA`.

### Roles

| Role               | Responsibility                                                                                        |
| ------------------ | ----------------------------------------------------------------------------------------------------- |
| `@project-manager` | Routing, assignment, gate decisions, branch policy, on-chain settlement.                              |
| `@architect`       | Module boundaries, interface contracts, cross-cutting tradeoffs.                                      |
| `@implementer`     | The code (`fullstack-dev` analog, simplified to single role for v0.1).                                |
| `@reviewer-arch`   | QC lens #1: dependency direction, boundary integrity, abstraction cost.                               |
| `@reviewer-sec`    | QC lens #2: auth, state integrity, unsafe defaults, injection surfaces.                               |
| `@reviewer-perf`   | QC lens #3: hot-path complexity, resource lifecycle, observability.                                   |
| `@adversary`       | Hostile reviewer. One job: find what the trio missed.                                                 |
| `@classifier`      | Failure router. When verification fails, decides if spec/code/prover/tool/state-space/infra is wrong. |
| `@qa`              | Verifies acceptance against intent + plan.                                                            |

Each role corresponds to a markdown agent definition at `agents/tribunal-<role>.md`.

### Git discipline

- **PM is the only branch-decision-maker.** Other roles cannot create branches or switch off the working branch.
- **`main` is protected** unless Assignment explicitly says `Branch policy: direct on main`.
- **Same-repo concurrent writes** require `git worktree` isolation. PM must specify `Worktree path` in the Assignment.
- **QC trio + adversary review** runs against a **single HEAD** that includes all in-scope commits. PM merges parallel tracks to an integration branch before review starts.
- **All review reports** land in `.tribunal/reports/<plan-id>/` and are committed in the same branch as the change.

## The hybrid review

The methodology's signature technique. Two stages.

### Stage 1 — Lens-parallel review

PM dispatches `@reviewer-arch`, `@reviewer-sec`, and `@reviewer-perf` in **parallel** (single invocation message — no serial rollout). Each reviewer:

- Reviews from its assigned lens.
- Files **findings** at three severities: Critical / Warning / Suggestion.
- Each finding is **signed by the reviewer's agent key** and committed to the local ledger (`.tribunal/ledger.jsonl`).
- Produces a Verdict: `Approve` / `Request Changes` / `Needs Discussion`.

PM consolidates verdicts. If any reviewer says `Request Changes` and a finding isn't resolved, the change loops back to `@implementer`.

### Stage 2 — Adversarial gate

If all three reviewers `Approve`, PM dispatches `@adversary` with:

- The intent doc, plan, diff, and **all three reviewer reports**.
- One goal: **find what the trio missed.**

The adversary attacks the _consensus_, not the code. Its categories of attack include but are not limited to:

- **Shared blind spot**: a class of bug none of the lens reviewers cover (e.g. a temporal property, a state-space bug, a cross-component composition issue).
- **Hidden assumption**: a precondition all three reviewers silently agreed to but the intent doesn't actually guarantee.
- **Refinement mismatch**: implementation diverges from spec in a way the lens reviewers' checklists don't expose.
- **Adversarial input**: an input class the reviewers acknowledged but didn't exercise.

If the adversary surfaces a Critical finding, the change loops back. If the adversary `SURVIVES`, the change advances to verification.

**Diversity-aware dispatch.** Tribunal treats adversary diversity as a _spectrum_, not a vendor-axis-only thing. Cross-vendor diversity (Claude vs. OpenAI vs. Gemini vs. local) is theoretically the strongest — different training corpora, different RLHF flavors, different tokenizers — but it's empirically uncertain how much it actually buys for code review, and it's expensive.

In practice, you can get most of the diversity payoff from **within-family variation**:

- Different system-prompt focus (`adversary-spec`, `adversary-impl`, `adversary-temporal`).
- Different temperature (deterministic vs. exploratory).
- Different model tier (Opus vs. Sonnet).
- Different snapshot (4.6 vs. 4.7, etc.).
- Different reasoning budget.

The **default adversary panel** (v0.4.0) is the three distinct Claude model tiers — Opus + Sonnet + Haiku — each with a different focus axis. No extra API spend over a single Claude subscription. The **high-stakes panel** adds one cross-family slot on top of that trio for environments that have keys for a second vendor; the intra-Claude trio is the load-bearing primitive, not a fallback. `@project-manager` opts in via Assignment when the cross-family TIER-2 signal matters (mainnet contracts, security-critical paths, compliance-adjacent code).

This shape is empirically grounded, not theoretical. P-multi-adversary (2026-05-17) ran four adversaries — Opus, Sonnet, Haiku, and a cross-family Qwen — against the same v0.3.4 diff. Cross-family produced **zero** unique findings the Claudes missed (H1 refuted, provisionally). Intra-Claude diversity produced three distinct verdicts on the same input and surfaced the most novel finding of the panel — F-OPUS-004, a Unicode bypass in `looksLikeTestChain` that Sonnet and Haiku both missed. The v0.3.X-era default of three opus/sonnet variants with overlapping tiers was strictly worse than the new opus + sonnet + haiku composition, and the v0.4.0 reshape ships that empirical winner as the default.

This dimension-agnostic dispatch lets the reputation ledger _learn_ which kind of diversity actually pays off over time. After enough plans, the leaderboard can tell you whether cross-vendor adversaries find materially more unique critical bugs than the intra-Claude trio — and you decide whether to pay for the vendor diversity going forward based on real data, not a priori theory.

## The verification pyramid

Runs after review survives the adversary. Each property is routed to the cheapest tool that can verify it. Halt on first failure; failure is routed to `@classifier` for triage.

For **Go** (the reference stack):

```
go test (fuzz + property)        ← gopter, native fuzz, behavioral sweeps
go test (unit + integration)     ← deterministic coverage
golangci-lint                    ← combined linter pass
staticcheck                      ← deeper static analysis
go vet                           ← compiler-tier checks
gofmt -s                         ← formatting + simplification
go build                         ← compiles cleanly
```

For **Rust** (the inherited stack from Colosseum, available for projects that opt in):

```
cargo test (property + fuzz)
cargo nextest / cargo test
cargo clippy -- -D warnings
cargo check
Kani                              ← bounded model checking (opt-in)
Verus                             ← SMT-backed annotations (opt-in)
Aeneas → Lean                     ← deep theorem proofs (opt-in)
```

For **TypeScript**: `tsc --noEmit`, `eslint`, `vitest`/`jest`, `fast-check`.

For **other languages**: the framework supports a `tribunal.yaml` per-project config declaring which tools to run at which layer.

## Failure routing

When a verification layer fails, `@classifier` routes the failure to one of six categories:

| Category             | What it means                                                                              | Fix owner                              |
| -------------------- | ------------------------------------------------------------------------------------------ | -------------------------------------- |
| `spec_wrong`         | The intent doc or plan is wrong; code is faithful but to a bad spec.                       | `@architect` / `@project-manager`      |
| `code_wrong`         | Spec is correct; code violates it. Real bug.                                               | `@implementer`                         |
| `prover_stuck`       | Spec + code correct but tool can't discharge obligation. Needs lemma, hint, decomposition. | `@implementer`                         |
| `tool_mismatch`      | Wrong layer of the pyramid for this property.                                              | `@architect`                           |
| `state_space_blowup` | Tool is right layer but abstraction too detailed. Simplify, don't add budget.              | `@architect`                           |
| `infrastructure`     | Not about verification — build error, missing dep, version mismatch.                       | `@implementer` or `@ops` (future role) |

Classification is **evidence-grounded**. `@classifier` cites artifacts and gives `low | medium | high` confidence. `INDETERMINATE` is a valid output when the evidence doesn't decide.

## The incentive layer

The novel piece. Neither source repo has it.

### Mechanics

Every finding becomes an entry in an append-only signed ledger:

```jsonl
{
  "finding_id": "F-001",
  "plan_id": "P-42",
  "round": 1,
  "agent_pubkey": "ed25519:...",
  "agent_label": "claude-adversary",
  "severity": "critical",
  "category": "shared_blind_spot",
  "claim_hash": "sha256:...",
  "claim_uri": ".tribunal/findings/F-001.md",
  "stake": 10,
  "timestamp": "2026-05-12T...",
  "signature": "..."
}
```

When the finding is resolved (fix merged, dismissal merged, or N rounds pass with no movement), an outcome event is appended:

```jsonl
{
  "finding_id": "F-001",
  "outcome": "true_positive",
  "resolved_by_pubkey": "ed25519:...",
  "resolved_by_label": "@qa",
  "evidence_hash": "sha256:...",
  "evidence_uri": ".tribunal/resolutions/F-001.md",
  "reward": 20,
  "timestamp": "2026-05-12T...",
  "signature": "..."
}
```

### Outcomes

- **True positive** — fix merged, evidence cites the diff that addresses the finding. Stake returned + 2× reward.
- **False positive** — PM-merged dismissal cites the reason. Stake slashed fully.
- **Stale duplicate** — finding already exists in the ledger for this plan. No stake change; flagged for de-duplication.
- **Indeterminate** — N rounds elapsed (default N=3 plan revisions) without a resolution. Stake returned, no reward.

### Reputation calculation

Per-agent reputation `R` is a rolling-window function of:

```
R(agent) = decay(TP(agent) × severity_weight, window=30d)
         - decay(FP(agent), window=30d)
         + family_diversity_bonus(agent, current_round)
```

Where `decay` is exponential with a 30-day half-life and `severity_weight` is `{critical: 4, warning: 2, suggestion: 1}`.

### Reputation gates

- Findings from agents with `R >= R_high` **auto-elevate** by one severity tier for triage priority.
- Findings from agents with `R < R_low` require **corroboration** from a second agent within the same round before they enter the action queue.
- Agents below `R_floor` get rotated **out of the next round's pool**. Their slot is taken by a fresh agent (different model, different prompt seed).

### Diversity bonus

A unique finding surfaced by an agent whose _diversity bucket_ hasn't produced a finding in the current round gets a 1.5× reward multiplier. The diversity bucket is configurable per project; reasonable choices include vendor family (`anthropic` / `openai` / `google` / `local`), temperature band (`deterministic` / `creative`), prompt focus (`spec` / `impl` / `temporal`), or a combination.

For the intra-Claude panel (the v0.4.0 default), the bucket is `(model_tier, focus)` — opus / sonnet / haiku each occupy their own bucket, so a finding raised by only one tier reads as a real blind-spot escape rather than a vendor-bucket dedup. For cross-vendor panels (high-stakes opt-in), the bucket is typically `(vendor_family, focus)` since the cross-family slot is the diversity axis the operator paid extra for. The methodology is agnostic — the goal is to encourage variance along whichever axis you've configured.

### On-chain anchoring

Local ledger writes are always synchronous. On-chain settlement happens at plan-close (PM-triggered via `tribunal ledger sync`):

- All finding commitments and resolution outcomes for the plan are batched.
- Batch root is committed to the Tribunal Reputation contract on Burnt XION.
- Reputation deltas are applied per-agent atomically.

Content (finding text, claim, evidence, signatures) stays off-chain in `.tribunal/`. Only hashes and deltas go on-chain.

## The on-chain protocol

CosmWasm contract surface (Rust, in `contracts/tribunal-reputation/`):

### Messages

```rust
pub enum ExecuteMsg {
    RegisterAgent {
        pubkey: Binary,           // ed25519 32-byte
        label: String,            // human-readable agent ID
        model_id: String,         // e.g. "claude-opus-4-7"
        role: AgentRole,          // Adversary | Reviewer | Implementer | Classifier
        initial_balance: u128,    // starting reputation; default 100
    },
    CommitFindingBatch {
        plan_id: String,
        findings: Vec<FindingCommit>,
    },
    ResolveFindingBatch {
        plan_id: String,
        resolutions: Vec<Resolution>,
    },
    RotateAgent {
        old_pubkey: Binary,
        new_pubkey: Binary,
        reason: String,
    },
}

pub enum QueryMsg {
    Reputation { pubkey: Binary } -> RepBalance,
    AgentByLabel { label: String } -> Option<AgentRecord>,
    Finding { plan_id: String, finding_id: String } -> Option<FindingRecord>,
    Leaderboard { window: WindowSpec, role: Option<AgentRole>, limit: u32 } -> Vec<LeaderboardEntry>,
    PlanSettlement { plan_id: String } -> Option<SettlementRecord>,
}
```

### Invariants the contract enforces

- Pubkeys are unique. Labels are unique.
- A finding can only be resolved once.
- Reputation balances are non-negative (slashing floors at 0, not negative).
- Only the registered `resolver_pubkey` (a PM or QA key) can submit a resolution.
- Soulbound: no transfer message exists. Reputation cannot move between agents.
- Rotation moves history (TP/FP record) to the new key but resets balance to a configured floor — fresh start for the model with the same accountability trail.

### Out of scope for v0.3

- Multi-org tenancy (everyone shares the same agent registry for now).
- Fungible reward tokens for human operators.
- Cross-chain reputation (only XION).
- Slashing appeals (PMs can re-file a corrected resolution; old ones stay in history but reputation recomputes from latest).

## Anti-patterns

Things this methodology forbids by construction:

- **Cooperative-only review.** Three reviewers voting can converge on shared wrongness. Always run the adversarial gate.
- **Unsigned findings.** A finding without an agent signature has no reputation consequence and shouldn't be acted on.
- **Skipping plan-locked.** "I'll just refactor while I'm here" violates surgical-changes discipline and breaks the audit trail.
- **Approval despite open Critical.** Severity gate is absolute.
- **Serial QC rollout.** The trio must be dispatched in one message. Serial rollout invalidates the parallelism guarantee.
- **Reputation sycophancy.** PMs marking `false_positive` on a finding to protect a favored agent's score corrupts the ledger. Resolutions are signed and audit-logged.

## License

MIT. See [`../LICENSE`](../LICENSE).
