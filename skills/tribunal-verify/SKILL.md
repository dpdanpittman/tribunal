---
name: tribunal-verify
description: Run the Tribunal verification pyramid against a project. Each property is routed to the cheapest tool that can verify it; halt on first failure; failures route to `tribunal-classify`. Use after the hybrid review survives the adversarial gate. Do NOT use to interpret failures yourself — this skill only runs tools and routes.
compatibility: Requires the Tribunal CLI + each layer's underlying tool installed locally (Go, Rust, Node, etc. per the project's layer stack defined in `tribunal.yaml`).
metadata:
  version: 1.1.0
  last_updated: 2026-05-19
---

> **Prompt defense baseline:** see `../_shared/prompt-defense.md`.

You are running the Tribunal verification pyramid. The principle: route every property to the cheapest tool that can verify it. Wide cheap base, narrow expensive top. Each layer either verifies what it can or hands the unhandled remainder to the next.

You do not verify properties yourself. You run tools, capture results, halt on failure, and route failures to `tribunal-classify`.

## Inputs

- **Project root** — where the project's `tribunal.yaml` lives (or defaults are inferred).
- **Halt-on-failure** — default `true`. The first failing layer stops the run.
- **Layers to skip** — by default, all available layers run. The user may exclude layers if they're not wired up.

## The layered pyramid

For **Go** (reference stack):

| #   | Layer          | Tool                                    | Notes                                                    |
| --- | -------------- | --------------------------------------- | -------------------------------------------------------- |
| 1   | Build          | `go build ./...`                        | Compilation is foundational; any failure halts.          |
| 2   | Format         | `gofmt -s -d .`                         | Style. Treat diffs as failure unless explicitly allowed. |
| 3   | Vet            | `go vet ./...`                          | Cheap correctness checks.                                |
| 4   | Static         | `staticcheck ./...`                     | Deeper analysis.                                         |
| 5   | Lint           | `golangci-lint run`                     | Project-configured linter pass.                          |
| 6   | Tests          | `go test -race ./...`                   | Behavioral coverage.                                     |
| 7   | Property tests | `go test -tags=property ./...` (gopter) | If `property` tag exists.                                |
| 8   | Fuzz           | `go test -fuzz=Fuzz ... -fuzztime=Ns`   | Per project policy.                                      |

For **Rust** (opt-in via `tribunal.yaml`):

| #   | Layer         | Tool                            |
| --- | ------------- | ------------------------------- |
| 1   | Build         | `cargo check`                   |
| 2   | Lint          | `cargo clippy -- -D warnings`   |
| 3   | Tests         | `cargo test`                    |
| 4   | Kani          | bounded model checking (opt-in) |
| 5   | Verus         | SMT-backed annotations (opt-in) |
| 6   | Aeneas → Lean | deep theorem proofs (opt-in)    |

For **TypeScript** (opt-in via `tribunal.yaml`):

| #   | Layer          | Tool                   |
| --- | -------------- | ---------------------- |
| 1   | Build          | `tsc --noEmit`         |
| 2   | Lint           | `eslint .`             |
| 3   | Tests          | `vitest run` or `jest` |
| 4   | Property tests | `fast-check`           |

For other languages: declare the layer stack in `tribunal.yaml` and Tribunal will run them in order.

## On failure

When a layer fails:

1. Record the layer result.
2. Dispatch `tribunal-classify` with the failure output, the relevant spec artifact (the test, the assertion, the lint config), the source under verification, and the intent doc.
3. Persist the classifier's report to `.tribunal/classifications/<layer>-<ISO-timestamp>.md`.
4. If halt-on-failure: stop and produce the final pyramid report.
5. If not halt-on-failure: continue, recording each layer's result.

## Persistence

Save the full pyramid run to `.tribunal/verify/<ISO-timestamp>.md`:

```markdown
# Tribunal verification pyramid run

- Project root: <path>
- Started: <ISO>
- Completed: <ISO>
- Halt-on-failure: <true/false>
- Layers excluded: <list or none>

## Per-layer results

| #   | Layer | Status | Duration | Details |
| --- | ----- | ------ | -------- | ------- |
| ... |

## Failure classifications

(for each failure, embed or link the classifier's report)

## Coverage snapshot

- Layers passed: N
- Layers failed: M
- Layers skipped: K
- Layers not_applicable: L

## Suggested next action

<concrete step based on classifications>
```

## Examples

### Example 1 — clean Go pyramid run

User: "Verify the current diff."

1. Read `tribunal.yaml`; confirm Go stack with 6 layers enabled.
2. Run build → format → vet → static → lint → tests in order.
3. All pass. Write the pyramid run to `.tribunal/verify/<timestamp>.md`.
4. Suggest next: `tribunal-incentive` for resolution recording at plan close.

### Example 2 — `go vet` fails

1. Build passes, format passes, vet fails on `something-suspicious.go:42`.
2. Halt (default).
3. Dispatch `tribunal-classify` with vet output + intent.md + plan.md + the file.
4. Classifier returns `code_wrong` with high confidence.
5. Write the pyramid run + classification.
6. Suggested next: route to `@tribunal-implementer` with the classifier's location.

## Troubleshooting

- **A layer fails for environmental reasons (missing binary, wrong version)** — the classifier should return `infrastructure`. If it returns anything else, surface the discrepancy; don't accept a wrong-route quietly.
- **Property layer hangs** — kani / fast-check can run forever on poorly-bounded harnesses. Apply the time budget from `tribunal.yaml`; if exceeded, the classifier should return `state_space_blowup`.
- **Pyramid passes but the user still sees the bug at runtime** — that's a coverage gap, not a pyramid failure. Surface it to the PM as a new intent-doc clause needing to be added in the next round.

## What you do not do

- You do not interpret layer failures yourself. Route to `tribunal-classify`.
- You do not skip the classifier even when the failure seems "obviously" a particular category.
- You do not modify source code.
- You do not advance past a failing layer when halt-on-failure is true.

## Spirit

The pyramid only earns its keep when run consistently. Ad-hoc verification — "I ran cargo test that one time" — drifts. This skill is the canonical run.

## Composability

This skill pairs with:

- [`tribunal-review`](../tribunal-review/SKILL.md) — upstream; runs after the adversarial gate clears.
- [`tribunal-classify`](../tribunal-classify/SKILL.md) — downstream; consumes any layer failure this skill produces.
- [`tribunal-plan`](../tribunal-plan/SKILL.md) — referenced; the plan's verification section determines which layers apply.
