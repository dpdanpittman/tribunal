# Trajectory PBT — worked example (v0.5.2 M3)

Demonstrates the loop the temporal lens closes:

1. **Lens finds a state-machine property.** Auditing a system whose central
   claim is longitudinal (session-essence portrait, ledger archive, anything
   that accumulates), the temporal reviewer identifies an invariant: _no
   Synthesize pass should ever drop a section marked load-bearing._

2. **Operator encodes the property as a `trajectory.Property`.** See
   `portrait_test.go`. Operations: add-load-bearing, add-regular, edit,
   synthesize. Invariant: `LoadBearingCount()` is monotone-non-decreasing
   relative to the historical maximum (load-bearing additions raise the
   bar; synthesize must never lower it).

3. **`trajectory.Run(t, property)` runs the rapid stateful engine.** rapid
   draws random sequences of operations; the invariant fires after each
   operation. When it trips, rapid shrinks the trajectory to a minimal
   counterexample.

## Demonstrating both branches

The example ships two implementations of `Synthesize`:

- `SynthesizeSafe` — respects the `LoadBearing` marker. Only drops sections
  where `LoadBearing == false`. The Property defined against this passes.
- `SynthesizeBuggy` — drops sections by index without checking `LoadBearing`.
  This is the failure mode the temporal lens was designed to catch.

Two tests live in `portrait_test.go`:

- `TestPortraitProperty_Safe` — uses `SynthesizeSafe`. Passes in CI.
- `TestPortraitProperty_Buggy_DemonstratesShrink` — uses `SynthesizeBuggy`.
  Skipped by default (CI stays green); un-skip to watch rapid shrink.

## To see the failing trajectory

```bash
cd examples/trajectory-portrait
# remove the t.Skip line in TestPortraitProperty_Buggy_DemonstratesShrink,
# then:
go test -run TestPortraitProperty_Buggy -v
```

Typical shrunk output (the exact trajectory varies by seed):

```
trajectory: add-load-bearing
trajectory: synthesize (drop-at=2)
load-bearing count dropped from 1 (historical max) to 0 — synthesize pass
deleted a load-bearing section. sections now: []
```

That's the loop: lens identifies the property → operator encodes it →
rapid finds the bug → counterexample is reproducible by reading the
shrunk trajectory.

## Why this matters

Without the scaffold, the same property would have to be expressed as
an ad-hoc rapid test in each repo, with each operator inventing their
own idiom for "trajectory invariant." The scaffold standardises:

- **Operation registration** — keyed map, one function per operation,
  naturally named in counterexample output.
- **Invariant composition** — multiple named invariants per Property,
  all checked after every operation.
- **Finding back-reference** — `FindingID` ties the executable test
  to the temporal-lens finding it enforces. Closing the loop from
  audit to CI.

When the temporal lens files `category: temporal_invariant`, the
implementer agent's job is mechanical: translate the prose finding
into a `Property{}` literal and check it in. The scaffold is the
contract that makes that translation mostly boilerplate.
