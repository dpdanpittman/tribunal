# ADR-0002: Clawpatch Absorption

**Status:** Sketch — pick up 2026-05-18
**Date:** 2026-05-17 (late evening, post-PII-audit session)
**Driver:** Decision on whether/how Tribunal should subsume `openclaw/clawpatch` capabilities now that v0.2.0 ships the `acpx` provider (Claude via ACP).

## Context

`clawpatch` v0.2.0 landed earlier on 2026-05-17 with the `acpx` provider wired
in (PR by @mvanhorn), making Claude-driven code review viable. We installed it
locally, ran `clawpatch doctor` green, and considered using it as a
standalone fix-loop next to Tribunal. The observation that prompted this ADR:
**Tribunal already does most of what clawpatch does, at a higher level**, but
clawpatch has depth in places where Tribunal hasn't built yet.

This ADR sketches the merger so it can be picked up cold tomorrow.

## Capability comparison

What clawpatch brings that Tribunal lacks today:

| Capability                                             | Tribunal                                      | Clawpatch                                                                                                     |
| ------------------------------------------------------ | --------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| Multi-language semantic feature mapping                | ❌ (lens trio reviews whole repo)             | ✅ deep mappers for Next/Rails/Gradle/SwiftPM/CMake/FastAPI/Flask/Laravel/Ruby/Go/Python/Java/Kotlin/Rust/C++ |
| Provider abstraction beyond Anthropic-direct           | ❌                                            | ✅ `acpx` / `codex` / `grok` / `opencode`                                                                     |
| Triage state machine (open / in-progress / fixed / FP) | Partial (ledger has findings, no transitions) | ✅                                                                                                            |
| Fix-plan worktree workflow                             | ❌                                            | ✅                                                                                                            |
| Revalidate command                                     | ❌                                            | ✅                                                                                                            |
| `--since <ref>` incremental review                     | ❌                                            | ✅                                                                                                            |
| Concurrent-run lock files                              | ❌                                            | ✅                                                                                                            |

What Tribunal has that clawpatch lacks:

- Lens-parallel trio (arch / sec / perf) as the core review primitive
- Adversary stage reading lens findings for cross-cutting chains
- Signed findings + ed25519 agent identity
- On-chain reputation settlement (CosmWasm on Burnt XION)
- Spec-driven gates (plan / classify / implement / verify)
- Verification pyramid (multi-stack build/fmt/vet/test)
- Skills as authored agent prompts (markdown, version-controlled)
- Convergence controller (ADR-0001, v0.4 scope)

The two capability sets are **almost disjoint**. Clawpatch operates at the
per-feature code-review layer. Tribunal operates at the methodology + identity

- reputation layer.

## Decision options

### Option A — Port clawpatch's value layer to Go

Reimplement clawpatch's mappers, provider abstraction, state machine, and
fix-plan workflow in Go inside Tribunal. Result: one binary, one state
directory, one mental model. Many weeks of work — the mappers alone are ~15
languages with non-trivial parsing logic. Highest fidelity, longest path.

### Option B — Wrap clawpatch as a Tribunal subroutine

Tribunal CLI spawns `clawpatch map` / `clawpatch review` as subprocesses, reads
`.clawpatch/findings.json`, ingests into Tribunal's ledger as signed findings.
Adversary stage stays in Tribunal Go code, with input = the union of lens
findings + clawpatch findings. Fastest path, lots of value, but two binaries
to keep alive and version-coordinate.

### Option C — Tribunal as methodology layer above clawpatch _(recommended)_

Same mechanics as Option B with clear architectural framing: Tribunal owns
**trust** (identity, reputation, methodology, adversary, convergence);
clawpatch owns **discovery** (mapping, per-feature LLM review, multi-language).
Each tool does what it's good at, neither is reimplemented in the other's
language.

```
              tribunal (Go, identity + ledger + on-chain)
                     │
                     ▼
         ┌───────────┼───────────┐
         │           │           │
   lens=arch    lens=sec    lens=perf
         │           │           │
         ▼           ▼           ▼
              clawpatch (TS, per-feature mapping + provider)
                     │
                     ▼
              acpx → claude / codex / grok / opencode
                     │
                     ▼
              findings.json
                     │
                     ▼
        tribunal ingests → signs → ledger
                     │
                     ▼
        tribunal adversary (cross-cutting chains)
                     │
                     ▼
        synthesis + on-chain settlement
```

## Decision (proposed)

Adopt **Option C** for v0.4–v0.5 timeline. Re-evaluate Option A once
mapper portfolio stabilizes upstream and we have a stronger sense of which
languages Tribunal users actually exercise.

## What changes in each codebase

### Tribunal (Go) — new modules

- `internal/clawpatch/` — subprocess wrapper, JSON ingest, lens-prompt injection
- `internal/review/lens.go` — extend to dispatch via clawpatch rather than direct Anthropic call
- `internal/ledger/triage.go` — extend finding records with `triage_status`, `triage_history`, `clawpatch_id`
- `cmd/tribunal/fix.go` — wraps `clawpatch fix --finding <id>`
- `cmd/tribunal/revalidate.go` — wraps `clawpatch revalidate`
- Skill update: `tribunal-review/` gets a clawpatch-aware variant that emits lens-tagged prompts

### Clawpatch (TS) — small upstream contributions worth filing

- Optional `--export-tribunal-ledger <path>` flag on `clawpatch review` that
  emits findings in Tribunal's signed-ledger format directly (skip the JSON
  re-parse round-trip). Reduces ingestion surface.
- Accept incoming prompts from stdin so Tribunal can inject lens-specific
  prompts without writing temp files.

Both are small enough to PR upstream; neither blocks Option C.

## Key design decisions to lock before building

1. **Identity at which layer?** Tribunal's findings are signed by the agent
   that produced them. If clawpatch produced the raw finding, who signs?
   - Option: Tribunal-the-orchestrator signs (signing means "I, Tribunal,
     attest this clawpatch-produced finding is well-formed and reproducible").
     Lower trust signal than the agent itself signing, but no upstream change.
   - Option: pass the agent's keypair through to clawpatch via env, have
     clawpatch sign before emitting. Requires clawpatch changes.
   - **Tentative recommendation:** Tribunal signs on ingest. The clawpatch
     finding ID is part of the signed payload so re-running clawpatch
     reproducibly verifies the same finding was produced.

2. **Mapping cadence** — clawpatch's `map` is heavy. Re-running per lens is
   wasteful. Tribunal should `clawpatch map --json` **once**, then
   `clawpatch review --features <subset>` three times with different prompts.

3. **Adversary input format** — Tribunal's adversary already takes lens
   findings as input. Now lens findings come from clawpatch. Adversary stays
   Go, reads Tribunal's ledger (which has already ingested + signed).

4. **Triage propagation** — when a finding is triaged in Tribunal's ledger,
   clawpatch's local state needs to know so `revalidate` skips false-positives.
   Tribunal pushes triage state to clawpatch via
   `clawpatch triage --finding <id> --status <s>` on every ledger transition.

5. **Versioning compat** — Tribunal pins `clawpatch>=0.2.0 <0.3.0` and verifies
   via `clawpatch --version`. Pin `acpx` too (clawpatch itself notes "tested
   against ^0.8.0").

6. **On-chain settlement of clawpatch-sourced findings** — same protocol as
   Tribunal-native findings since they're signed by Tribunal at ingest. No
   contract change.

## Open questions

- **Adversary timing**: run adversary AFTER all clawpatch lenses finish, OR
  stream lens findings into adversary as they arrive? After is easier to
  reason about; streaming would speed up large repos.
- **Per-language verify pyramid** — clawpatch detects the project type and
  knows the validation commands; Tribunal has its own verification pyramid in
  `internal/verify`. Two sources of truth for "what does it mean to validate
  this repo?". Pick one. Tentative: clawpatch's detection feeds Tribunal's
  verify command list; Tribunal owns verify execution.
- **Skill rewrites** — Tribunal's `tribunal-review` skill is a Claude prompt.
  If clawpatch is now driving the review prompt construction, the skill
  becomes a higher-level orchestration spec ("use lens X with provider Y on
  features Z"). Skill semantics shift from "prompt template" to "review
  configuration".
- **Interaction with convergence controller (ADR-0001)** — does the convergence
  loop re-run clawpatch maps each round, or reuse the first map? Reuse is
  cheaper but misses features added by previous-round fixes. Probably re-run
  map per round, but cache the deterministic-mapper output and only re-invoke
  the agent-assisted mapper.

## Implementation phasing

**Phase 1 (v0.4 stretch):** `internal/clawpatch/` subprocess wrapper +
`tribunal review --via clawpatch` flag (opt-in). Validates the ingest path
end-to-end without disrupting existing Anthropic-direct review.

**Phase 2 (v0.5):** triage state machine extension to ledger + `tribunal fix`

- `tribunal revalidate` subcommands wrapping clawpatch.

**Phase 3 (v0.6):** clawpatch upstream PRs land (`--export-tribunal-ledger`,
stdin prompts), Tribunal switches default review path through clawpatch,
direct-Anthropic path retired.

**Phase 4 (v0.7+, optional):** re-evaluate Option A based on mapper portfolio
stability and language coverage gaps.

## Status

Sketch. Pick up 2026-05-18 to:

1. Validate Option C is the right framing or whether B is sufficient long-term.
2. Decide identity question (#1 above).
3. Spike the `internal/clawpatch/` wrapper against `oracle-driver-scripts`
   (small, already inited with `.clawpatch/`, mostly bash + one Python file —
   minimal mapper exercise, maximal seam-validation).

## Notes from the session that produced this ADR

This was sketched at the tail end of an audit-fix session (Tribunal output
applied to the oracle stack, ~12 hours of work, dashboard v0.7.3 → v0.7.10,
PII scrubbed from argus-iris history, GH_TOKEN rotated). The audit produced
99 findings across the three repos; Tribunal's lens-parallel methodology
caught most exploitable items, the adversary stage found the chains, and
synthesis ordered the fix sequence correctly. Clawpatch was first considered
as the _fix loop_ on top of those findings, but quickly turned into a
question of whether Tribunal should own the discovery layer too.

The merger isn't "Tribunal swallows clawpatch wholesale." It's "Tribunal
becomes the trust layer above clawpatch's discovery layer," and each tool
keeps doing what it's currently good at.
