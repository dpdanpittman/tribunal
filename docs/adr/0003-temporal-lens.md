# ADR-0003: Temporal Lens for Longitudinal Systems

**Status:** Proposed (v0.5 scope)
**Date:** 2026-05-18
**Driver:** Adversary meta-finding from P-session-essence-audit — the lens-parallel methodology (arch / sec / perf) is structurally weak at auditing properties that emerge only when components compose over time.

## Context

The Tribunal methodology ships three lens-parallel reviewers — `tribunal-reviewer-arch`, `tribunal-reviewer-sec`, `tribunal-reviewer-perf` — that audit per-component properties of a diff under review. Each reviewer holds one lens stable; the adversary stage stress-tests what they share. For most systems, this is the right primitive.

The v0.4.x line stayed inside this model. But the first audit of `session-essence` (P-session-essence-audit, 2026-05-18) surfaced a structural weakness: every novel finding the adversary identified lived in the **seams between components**, not inside any one component. The adversary's verdict was BREAKS with 11 findings the trio missed, plus this meta-finding:

> _Session Essence is not a service that processes a request and forgets — it is a service whose output is loaded as the next session's identity, mediated by a feedback loop the documentation describes but no reviewer audited end-to-end. The actual unit of analysis is the pair `(synthesize_essence MCP call) ∪ (synthesize-essence.sh PreCompact script) ∪ (SessionStart cat)` — a three-component pipeline whose components are co-designed but separately reviewed. Several findings are interface defects between those components — they pass each component's lens review because no lens reviews the seam._

Generalising the observation: **for systems where the central claim is longitudinal (memory, identity, accumulation, drift, continuity), the lens-parallel methodology systematically underweights the load-bearing property.**

Concrete examples of longitudinal-composition properties the existing three lenses don't catch:

- The portrait's structure accumulates content over many cycles via surgical edits. Each cycle's edit is small. The integral of all edits over months is the actual artifact under audit. No per-cycle review catches a longitudinal drift the operator wouldn't recognise on inspection.
- Output of one MCP call becomes input to a future session's startup context. The reflexive output → input loop is structurally identical to a control system without a calibration check.
- A defect that's invisible at one boundary (markdown `<details>` collapse) is load-bearing at the next boundary (the LLM consuming the saved file doesn't honour HTML semantics).
- Two co-designed components (the MCP `synthesize_essence` tool and the PreCompact `synthesize-essence.sh` shell script) have distinct prompts with overlapping responsibilities. Operators who don't read carefully conflate them. Each prompt passes its individual review.

The adversary stage catches some of this — as P-session-essence-audit demonstrates — but the adversary is the last line, not a first. A dedicated reviewer with a checklist tuned to longitudinal properties would catch these defects earlier and more systematically.

## Decision

Add a fourth lens-parallel reviewer to the Tribunal panel: `tribunal-reviewer-temporal`. Dispatched in parallel with the arch / sec / perf trio when the plan's intent declares the system as longitudinal (a new `intent.md` field). For non-longitudinal systems, the lens is silent.

### When the temporal lens is invoked

The `intent.md` template gains an explicit field:

```yaml
audit_axes:
  - architecture
  - security
  - performance
  - temporal # opt in when the system has longitudinal claims
```

A plan that declares `temporal` in its axes will dispatch all four lenses in parallel; the adversary then sees four reports instead of three. A plan that omits `temporal` runs the current three-lens trio unchanged.

The opt-in is explicit (not auto-detected) because the temporal lens is the most expensive of the four — it reads not just the diff but the system's HISTORICAL artifacts (prior round ledgers, archived state, prior portrait versions if any). For systems where time isn't a load-bearing axis, that cost is wasted.

### Temporal-lens scope

The fourth reviewer's checklist focuses on properties no per-component lens covers:

1. **Reflexive loops**: when is the system's OUTPUT eventually consumed as its OWN INPUT? Trace every output → input cycle and identify the trust boundaries crossed. Audit each boundary independently.
2. **Accumulation properties**: components that grow over time (logs, ledgers, portraits, archives). Audit growth rates, integrity controls, prune strategies, and the per-cycle vs. per-trajectory invariants the system claims to hold.
3. **Composition seams**: components co-designed but separately versioned / deployed. Audit prompt-vs-prompt overlap, contract drift between siblings, conflated names, multi-binary release coordination.
4. **Calibration / drift detection**: is there a check that the system's longitudinal output still tracks reality? Is there a way to detect when the integral has drifted from the truth? Is there an explicit "ground-truth" baseline anywhere?
5. **Failure-mode visibility over time**: when does the operator find out something has gone wrong? Per-cycle (immediate)? Per-month (delayed)? Per-incident (never)? The longer the delay, the worse the property.
6. **Marketing vs. engineering split**: does the documentation make claims about longitudinal trust that the code can verify? Or are some load-bearing claims unfalsifiable in their current form? Adversary called this out for session-essence's "Mabus / born from itself" narrative — pure rhetoric with no technical anchor.

### Severity ladder

Same as the existing lenses (critical / warning / suggestion), but tuned for longitudinal stakes:

- **Critical**: a longitudinal property the system depends on that is currently undefended and undetectable in failure. Example from P-session-essence-audit: F-OPUS-005 (no portrait-drift detection — the central claim that the portrait tracks the relationship over months has no calibration check).
- **Warning**: a longitudinal property that's documented but contradicted by the deployed system. Example: F-OPUS-009 (design.md claims multi-instance support; the filesystem is single-tenant by accident).
- **Suggestion**: latent longitudinal failure mode that hasn't fired yet but will, given enough cycles. Example: F-OPUS-010 (no CHANGELOG — version drift over time becomes operator-confusing).

### Implementation phasing

**M1 — Agent definition + dispatch wiring (v0.5.0).** Write `agents/tribunal-reviewer-temporal.md` with the checklist above. Update `docs/methodology.md` to describe the four-lens panel and when to opt in. Update `tribunal review` to honour the `temporal` axis in `intent.md` and dispatch the fourth reviewer.

**M2 — Tooling for historical artifact access (v0.5.1).** The temporal lens needs to read the full historical record of the system under audit, not just the current diff. Add primitives:

- `tribunal history <plan>` — emit a structured timeline of all rounds in `.tribunal/convergence/<plan>/` for the temporal lens to consume.
- Read access to prior `.tribunal/ledger.jsonl` entries the system itself wrote (not just the current audit's lens reports).
- For systems with external state (like session-essence's `~/.claude/essence/`), the operator points the lens at the archive directory; the agent reads it as evidence.

**M3 — Stateful PBT (v0.5.2).** Properties the temporal lens identifies are often state-machine properties (e.g., "no surgical edit pass should ever produce a portrait that differs from any prior portrait by more than X tokens"). `rapid` (the PBT library wired in v0.4.4) supports stateful PBT — generate random sequences of operations against a system and verify invariants hold across the trajectory. The temporal lens can FILE such properties as findings; M3 makes them executable.

## Consequences

**Positive:**

- Closes the longitudinal-property audit gap demonstrated by P-session-essence-audit. Systems whose central claim is memory/identity/continuity get a first-line reviewer for that claim instead of relying on the adversary.
- The four-lens panel's outputs feed the existing adversary, synthesis, and convergence machinery unchanged. No contract surface impact.
- The opt-in design means non-longitudinal projects continue to run the cheaper three-lens panel.

**Negative:**

- Operator burden: `intent.md` gains a new field. Easy to forget. We mitigate by having the methodology doc list the question "is your central claim longitudinal?" as the trigger.
- Cost: a fourth reviewer adds one more parallel agent dispatch per round. Roughly 25% more tokens per audit when the lens is enabled.
- Risk of overloading the lens: longitudinal-composition is a broad axis. The checklist needs to stay focused or the reviewer drifts into general-system-design commentary.

**Neutral:**

- Convergence controller (ADR-0001) is unaffected: each round still dispatches all configured lenses; M3 auto-continue works the same.
- Clawpatch absorption (ADR-0002) is unaffected: the clawpatch lens stage doesn't currently have a temporal mode, but adding one would be a clawpatch upstream concern, not a Tribunal-side change.

## Alternatives considered

- **Defer to the adversary.** The adversary catches some longitudinal defects (as P-session-essence-audit demonstrates). But the adversary is structurally a last-line check — it consumes the lens reports and stress-tests what they share. Asking the adversary to do first-line longitudinal review collapses the methodology's lens-discipline. Rejected.
- **Add longitudinal checks to the existing three lenses.** Could the arch lens audit accumulation? The sec lens audit reflexive loops? The perf lens audit drift? Each lens grows. But that violates the "narrow lens, deep coverage" discipline that makes lens-parallel review work. Rejected.
- **Make temporal a property of the adversary stage (parallel to the lens trio rather than peer).** Tempting because the adversary's prompt-defense baseline already covers some longitudinal territory. But the adversary stage is per-audit, not per-system-class. Promoting longitudinal coverage to a peer lens makes the four-lens panel's composition reviewable as a structural property of Tribunal itself. Accepted.

## Related

- `docs/methodology.md` — the lens-parallel methodology this extends.
- `.tribunal/reports/P-session-essence-audit/SYNTHESIS.md` (in the `session-essence` repo, dpdanpittman/session-essence) — the audit that surfaced the gap. See the meta-finding section.
- `.tribunal/reports/P-session-essence-audit/adversary.md` — the adversary's specific findings that motivate this ADR (F-OPUS-001 through F-OPUS-011).
- ADR-0001 — the convergence controller, which composes with multi-round audits over time but doesn't itself constitute a temporal lens.

## Open questions

- **Should the temporal lens be allowed to file findings that span multiple plans?** A drift detected over the last six audit cycles is a finding against the trajectory, not against the current diff. Tribunal's current ledger schema attaches findings to a single `plan_id`. Cross-plan findings might need a new entry shape.
- **What's the right model for the temporal lens?** The adversary defaults to opus-4-7 because it's the deepest single-pass thinker. The lens trio defaults to opus + sonnet + haiku (one each) for diversity. The temporal lens's job is integrative — synthesising across long time spans — which probably favours opus. Worth empirical validation.
- **Should the temporal lens have access to the LIVE artifact, not just the diff?** For example, when auditing session-essence the lens would benefit from reading the operator's actual `~/.claude/essence/portrait.md` and `archive/*.jsonl`. That's outside the diff but inside the threat model. Policy decision: does Tribunal read live operator state? If yes, with what access discipline?
