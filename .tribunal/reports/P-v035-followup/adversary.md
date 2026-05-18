# Adversary Report — P-v035-followup

This is a scoped, post-hoc filing of F-OPUS-004 — the most novel finding
from the four-adversary panel run on 2026-05-17. Full reasoning, evidence,
and attack scenario live in
`.tribunal/reports/P-multi-adversary/adversary-opus.md` (search for
"F-OPUS-004"). This report exists so `tribunal-batch-file` can sign and
append the finding to the ledger.

<!-- F-OPUS-004 was filed in an earlier batch-file run; block removed to
prevent double-filing on re-run. The signed entry is in
.tribunal/ledger.jsonl as of timestamp 2026-05-18T03:09:36Z. -->

## Additional findings from P-multi-adversary opus report

The five substantive opus findings besides F-OPUS-004 — all targeting real
v0.3.4 code paths — are filed here for v0.3.5 tracking. Full evidence and
attack scenarios live in `.tribunal/reports/P-multi-adversary/adversary-opus.md`.

## FINDINGS-TO-FILE-2

```
critical|shared_blind_spot|F-OPUS-001|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-multi-adversary/adversary-opus.md#f-opus-001|F-SEC-401's blast radius is wider than the sec reviewer named — the hostile-LCD attack hits the success-path preflight at sync.go:133, not just the recovery path. Pre-dated v0.3.4; v0.3.4 inherited it. Defense must apply to BOTH preflight call sites.
warning|hidden_assumption|F-OPUS-002|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-multi-adversary/adversary-opus.md#f-opus-002|CLI outer ctx (5m) vs perPlanSyncBudget (90s) — with N>=4 plans the per-plan budget is no longer the binding constraint. Plans 4+ get truncated time. Operator with 20 plans expects ~30m of patience, gets 5m.
warning|refinement_mismatch|F-OPUS-003|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-multi-adversary/adversary-opus.md#f-opus-003|No client-side batch chunking against contract MAX_BATCH_SIZE=100. A plan with >100 findings hits BatchTooLarge on every sync and the new recovery layer can't help (preflight returns no committed findings for fresh entries, immediate bail).
warning|shared_blind_spot|F-OPUS-005|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-multi-adversary/adversary-opus.md#f-opus-005|Recovery exhaustion error at sync.go:381/:419 reports cap=5 with no info on which findings were dropped, which remain, or the underlying contract error. Per-attempt logs print counts but don't accumulate into the terminal error.
suggestion|edge_case|F-OPUS-006|sha256:pending|file:///home/dan/src/tribunal/.tribunal/reports/P-multi-adversary/adversary-opus.md#f-opus-006|submitCommitBatch returns FindingsSent=0 on the everything-was-already-on-chain silent-success path (sync.go:351-352). Observationally indistinguishable from F-SEC-401 attack outcome. Defense (d) addresses the recovery path; success-path also needs it.
```
