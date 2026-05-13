# go-fizzbuzz-verified — Tribunal v0.1 walkthrough

This is the Tribunal walkthrough example: a tiny Go function reviewed and verified end-to-end through the methodology.

## Files

- `intent.md` — the Tribunal intent doc: behaviors, invariants, failure modes, scenarios.
- `main.go` — the implementation. ~30 lines, exhaustively commented.
- `main_test.go` — table-driven tests, invariant tests, panic test, native fuzz target.
- `.tribunal/` — the populated demo state:
  - `status.json` — plan registry (one plan: `P-fizzbuzz`, state: `Done`).
  - `ledger.jsonl` — append-only signed ledger with 4 findings and 4 resolutions.
  - `findings/F-001.md … F-004.md` — full text of each finding.
  - `resolutions/F-001.md … F-004.md` — full text of each resolution.

## What the demo shows

The methodology was walked through this example by 6 deterministic agents (their ed25519 keys are derived from fixed seeds so the demo is reproducible byte-for-byte):

- `claude-pm` — project manager + resolver
- `claude-reviewer-arch` — architecture lens reviewer
- `claude-reviewer-sec` — security lens reviewer
- `claude-reviewer-perf` — performance lens reviewer
- `claude-adversary` — hostile reviewer
- `claude-qa` — QA + resolver

Each reviewer filed a signed finding. The adversary surfaced a shared blind spot the trio missed. The PM and QA filed signed resolutions.

## Try it

From the repo root:

```bash
go install ./cmd/tribunal
cd examples/go-fizzbuzz-verified
tribunal ledger summary
```

Expected output:

```
AGENT                 SCORE  TP  FP  FINDINGS
claude-adversary      3.95   1   0   1
claude-reviewer-arch  1.98   1   0   1
claude-reviewer-perf  0.00   0   0   1
claude-reviewer-sec   0.00   0   0   1
```

- The adversary scores highest because its finding was Critical + TP.
- The architecture reviewer is second because its finding was Warning + TP.
- The performance and security reviewers both have score 0.00 because their findings resolved as `indeterminate` and `stale_duplicate` respectively (no reward, no slash).

## Read the findings

Try:

```bash
tribunal ledger find F-004
cat .tribunal/findings/F-004.md
cat .tribunal/resolutions/F-004.md
```

`F-004` is the adversary's finding — exactly the kind of shared blind spot the methodology is built to catch. Three lens-parallel reviewers approved, then the adversary asked: _what did the three of you miss?_ and found that the test file's invariant coverage was incomplete.

## Verify every signature

```bash
tribunal ledger verify
```

Expected: `✓ 4 findings and 4 resolutions verified`. The signatures use real ed25519 over the canonical JSON of each ledger entry; any tampering would fail verification.

## Re-seed (advanced)

The `.tribunal/` directory is populated by `cmd/seed-fizzbuzz-demo`:

```bash
# From the repo root:
go run ./cmd/seed-fizzbuzz-demo
```

This regenerates the ledger.jsonl deterministically. The git diff after running should be empty (the seed is byte-stable across runs).
