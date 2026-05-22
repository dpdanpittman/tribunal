# Temporal lens

Status: shipped — v0.5.0 (M1), v0.5.1 (M2), v0.5.2 (M3), v0.5.6 (cross-plan findings). Contract-side PBT coverage closed in v0.5.7 (7 properties total). Design: [ADR-0003](https://github.com/dpdanpittman/tribunal/blob/main/docs/adr/0003-temporal-lens.md).

## The gap it closes

The three default lens reviewers — architecture, security, performance — each hold one lens stable on the diff under review. For most systems, this is the right primitive.

But for systems whose central claim is **longitudinal** — memory, identity, accumulation, drift, continuity — the lens-parallel methodology systematically underweights the load-bearing property. The first audit of `session-essence` (a portrait-synthesis pipeline whose output is reflexively consumed as the next session's input) made this concrete: 11 of the adversary's findings lived in the **seams between co-designed components**. None of the three lenses saw them; each component passed its individual lens review because the seam was, by definition, between components.

Generalising the observation: when a system's load-bearing property is the integral of small per-cycle changes, per-component review systematically underweights it.

The temporal lens is the first-line reviewer for that class of defect.

## When to opt in

`intent.md` declares which lenses run via an `audit_axes` field. The three defaults always dispatch; `temporal` is opt-in:

```yaml
audit_axes:
  - architecture
  - security
  - performance
  - temporal # opt in for longitudinal systems
temporal_artifact_paths: # optional; live artifacts the lens may read
  - ~/.claude/essence/
```

Ask: _is the system's central claim longitudinal?_ Memory systems, identity systems, accumulation ledgers, archives with surgical-edit pipelines, multi-binary releases whose seams are co-designed — these have load-bearing properties that emerge only over many cycles. For everything else, leave `audit_axes` at the three defaults; the lens is silent when not declared.

## What the lens checks

Six axes, in order of where the most consequential defects tend to live:

1. **Reflexive loops** — output that becomes the system's own input. Audit each trust boundary independently.
2. **Accumulation properties** — components that grow over time. Audit growth rates, integrity controls, prune strategies, and per-cycle vs per-trajectory invariants.
3. **Composition seams** — components co-designed but separately versioned, deployed, or prompted. Audit prompt overlap, contract drift between siblings, conflated names.
4. **Calibration / drift detection** — is there a check that the system's longitudinal output still tracks reality?
5. **Failure-mode visibility over time** — when does the operator find out something has gone wrong? Per-cycle, per-month, per-incident, or never?
6. **Marketing vs. engineering split** — does the documentation make claims about longitudinal trust that the code can verify?

The severity ladder is tuned for longitudinal stakes: **Critical** for undefended + undetectable properties; **Warning** for documented-but-contradicted properties or seam contracts that can't be reviewed as a pair; **Suggestion** for latent failure modes that haven't fired yet but will, given enough cycles.

## Trajectory access (v0.5.1)

`tribunal history --plan <id> [--format text|json]` emits a structured timeline of a plan: per-round convergence results, signed-ledger findings + resolutions filtered to the plan, and a high-level summary (unique claims, carry-forward count, final verdict, stop reason). The json format is the canonical machine input for this lens — fields like `summary.carried_forward`, `rounds[].verify_passed`, and `summary.outcomes_by_kind` are what longitudinal-property audits reach for.

When the plan has no convergence rounds (single-pass review), only the signed-ledger view is emitted. The lens still finds trajectory-shaped defects in the ledger alone.

## Making findings executable (v0.5.2)

When the lens identifies a state-machine property — "no Synthesize pass should ever drop a load-bearing section" — the finding gets `category: temporal_invariant` and a clear state-machine description. The operator (or an implementer agent) then encodes the property as a `trajectory.Property` and registers it as a `_test.go` file in their repo:

```go
import "github.com/dpdanpittman/tribunal/trajectory"

func TestPortraitPrunePreservesLoadBearing(t *testing.T) {
    prop := trajectory.Property{
        FindingID:   "F-temporal-001",
        Name:        "portrait-synthesize-preserves-load-bearing",
        Description: "Synthesize pass must never decrease load-bearing section count.",
        SetUp:       func(t *rapid.T) trajectory.SUT { /* build initial state */ },
        Operations:  map[string]func(*rapid.T, trajectory.SUT){ /* add, edit, synthesize */ },
        Invariants:  []trajectory.Invariant{ /* monotone-non-decreasing */ },
    }
    trajectory.Run(t, prop)
}
```

`rapid` generates random operation sequences; the invariant fires after every operation. Counterexamples are shrunk to the minimal failing trajectory and include the `FindingID` — a CI failure traces straight back to the finding that motivated the test.

Worked example: [`examples/trajectory-portrait/`](https://github.com/dpdanpittman/tribunal/tree/main/examples/trajectory-portrait) — a session-essence-flavoured portrait with safe + buggy `Synthesize` variants. The safe variant's Property passes 100 trials in milliseconds; the buggy variant's invariant trips after one rapid action (test is `t.Skip`'d in CI so the suite stays green; un-skip to watch rapid shrink the counterexample).

## Cross-plan findings (v0.5.6)

Some longitudinal claims are about a trajectory that spans many plans — portrait drift across N audit cycles, defect-class recurrence, implementer-reputation trends. The ledger's `plan_id`-keyed schema couldn't represent those natively. v0.5.6 closes the gap with a `trajectory_id` field on both `Finding` and `Resolution`, with an `exactly-one-of(plan_id, trajectory_id)` constraint enforced at the signing layer:

```go
f := ledger.NewTrajectoryFinding(
    "portrait-drift-across-cycles", // trajectory_id
    /* round */ 0,
    /* claim */ "load-bearing section count decreased monotonically over 6 audit cycles",
    /* severity */ "warning",
    /* category */ "temporal_invariant",
    /* evidence_hash */ hash,
)
```

`tribunal history --trajectory <id>` (a sibling of `--plan`) reads the same timeline shape across trajectory-scoped entries. Trajectory findings stay local-only by design — they don't settle on-chain in v0.5.6, since the contract's `plan_id` is the natural settlement key. `chain sync` filters them out and surfaces a one-line count on stderr.

## v0.5 milestone summary

| Version | What shipped                                                                                             |
| ------- | -------------------------------------------------------------------------------------------------------- |
| v0.5.0  | M1 — temporal lens agent + `audit_axes` intent schema + Stage 1 dispatch wiring + methodology docs       |
| v0.5.1  | M2 — `tribunal history --plan <id>` CLI + plan-scoped TimelineSummary projection                         |
| v0.5.2  | M3 — `trajectory.Property` PBT scaffold + worked example + finding-to-test pathway                       |
| v0.5.3  | Rust contract PBT via `proptest` — 3 properties (TP / FP / leaderboard sort) against `cw-multi-test`     |
| v0.5.4  | Rust contract PBT — +2 properties (rotation accountability trail + batch-commit equivalence)             |
| v0.5.5  | Auto on-chain registration for `chain sync --auto-register` — closes the manual per-agent register step  |
| v0.5.6  | Cross-plan findings — `trajectory_id` field + `--trajectory` filter on history; local-only by design     |
| v0.5.7  | Rust contract PBT closure — +2 properties (Stale/Indeterminate noop + `ResolveFindingBatch` equivalence) |

The lens can now: **identify** longitudinal properties (M1), **read** the trajectory they live on (M2), **enforce** them as executable rapid tests (M3), and **span many plans** when the longitudinal property lives across audit cycles (v0.5.6). The Rust contract's stake/reward math is pinned by 7 property tests (v0.5.3 + v0.5.4 + v0.5.7).

## What's still ahead

- **Clawpatch parity** — the clawpatch lens stage (an alternative to native dispatch) is owned by clawpatch upstream and currently does not include `temporal`. Native-dispatch path is the v0.5 implementation; clawpatch parity is an upstream concern.

The temporal lens is the lens the methodology was missing.
