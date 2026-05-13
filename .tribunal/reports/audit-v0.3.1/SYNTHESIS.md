# Tribunal v0.3.1 — Audit Synthesis

**Date:** 2026-05-12
**Methodology:** Tribunal hybrid review (lens-parallel trio + adversarial gate)
**Scope:** v0.3.0..v0.3.1 diff (the audit-driven fix release)
**Reviewers:** `tribunal-reviewer-arch`, `tribunal-reviewer-sec`, `tribunal-reviewer-perf`, `tribunal-adversary`
**Final verdict:** **Approved — change advances to verification**

## Outcome

This is the closing-the-loop run on the v0.3.0 → v0.3.1 fix release. The trio unanimously approved; the adversary attacked the consensus and surveyed five potential issues, none of which break the verdict. Per the methodology, a change that survives adversarial scrutiny under all three lenses is trustworthy.

| Reviewer      | Verdict      | New findings                             |
| ------------- | ------------ | ---------------------------------------- |
| reviewer-arch | **Approve**  | 3 (1 warning, 2 suggestion)              |
| reviewer-sec  | **Approve**  | 5 (1 low, 4 cosmetic/accepted)           |
| reviewer-perf | **Approve**  | 0 new issues; 1 follow-up                |
| adversary     | **SURVIVES** | 5 attacks logged, all suggestion/warning |

## v0.3.0 findings status

Every blocker from v0.3.0 is verified fixed. Every adversary attack from v0.3.0 is verified fixed.

### Blockers (3) — all FIXED

- **B1** Wire-format mismatch (u128 → Uint128, ResolutionRecord split) ✅
- **B2** Input validation on identifiers ✅
- **B3** Stake type alignment ✅

### Warnings (6) — 3 FIXED, 3 deferred (as planned)

- **W1** keyring_backend=test default ✅ FIXED (stderr warning)
- **W2** Leaderboard O(n_agents) — deferred
- **W3** Unbounded batch Vec ✅ FIXED (MAX_BATCH_SIZE = 100)
- **W4** Flat 30s HTTP timeout — deferred
- **W5** Old label after rotation ✅ FIXED (label removed on rotate)
- **W6** No batch rollback — deferred

### Adversary attacks (4) — all addressed

- **A-ADV-001** Label after rotation ✅ FIXED
- **A-ADV-002** Pipe injection ✅ FIXED (covered by B2)
- **A-ADV-003** Stake type ✅ FIXED (covered by B1)
- **A-ADV-004** Dead admin field — acknowledged, deferred

## v0.3.1 new findings

Five from the trio, five from the adversary. All are suggestion / warning / cosmetic. None block.

### Worth tracking for v0.4 (action recommended)

- **A-V31-001 / F-SEC-103** `outcome_reward_multiplier` has no upper bound at instantiate. Deployer can saturate balance to `Uint128::MAX` by fat-fingering the multiplier. Low risk (self-sabotage; deployer is trusted) but worth bounding.
- **A-V31-003** Go-side `MaxBatchSize` missing. Contract rejects oversized batches but the client doesn't fail fast. UX improvement.
- **A-V31-004 / F-ARCH-102** Wire tests cover unmarshal only; add marshal tests to detect future schema drift.
- **A-V31-005 / F-SEC-102 / F-ARCH-103** No integration tests for the new validation rules. Same methodology lesson as v0.3.0 — bypasses unit-only confidence.
- **F-ARCH-101** Doc gap: `agent_by_label` returns `not_found` for retired labels but `docs/on-chain-protocol.md` doesn't say so.
- **A-V31-002** `rotation reason` is validated but discarded. Either store it (audit trail) or remove from the message.

### Pure cosmetic / accepted

- **F-SEC-101** Rust char-index vs Go byte-index in validation error messages.
- **F-SEC-104** Wire tests don't cover malformed JSON.
- **F-SEC-105 / F-SEC-106** Keyring warning heuristic edge cases.

## Methodology observations

1. **The loop closed cleanly.** v0.3.0 surfaced 3 blockers and 4 adversary attacks. v0.3.1 fixed all 7. The re-review confirmed every fix at file:line. The adversary attacked the new code and surveyed 5 issues, none breaking. This is what the methodology promises: a change becomes trustworthy by surviving adversarial scrutiny, not by consensus.

2. **The wire-test pattern earned its place.** `internal/chain/wire_test.go` was added in v0.3.1 specifically to close the cw-multi-test JSON-boundary gap. It worked — the resolved-finding path that broke in v0.3.0 is now permanently fixture-tested.

3. **The adversary stayed valuable.** It surfaced two attacks (A-V31-001 reward saturation, A-V31-002 dead reason field) that no lens reviewer caught. This is consistent with v0.3.0, where the adversary's wins (A-ADV-001 label cleanup, A-ADV-004 dead admin) were lens-blindspot finds. The single-model adversary is paying for itself.

4. **Test count flatness is a smell.** The trio + adversary all noted that v0.3.1 fixed validation logic but kept the integration test count at 15. The methodology lesson from v0.3.0 — "cw-multi-test bypasses the JSON boundary" — generalizes: it also bypasses validation flow. The v0.4 follow-up should add full-flow rejection tests.

## Recommended next action

**Ship v0.3.1.** Open a v0.4 backlog with:

1. Reward-multiplier and rotation-floor bounds at instantiate (A-V31-001).
2. Go-side `MaxBatchSize` validation (A-V31-003).
3. Integration tests for validation rejection paths (A-V31-005, F-SEC-102, F-ARCH-103).
4. Wire marshal tests (A-V31-004, F-ARCH-102).
5. Doc updates for `agent_by_label` rotation semantics (F-ARCH-101).
6. Decide: store rotation `reason` or remove it from the message (A-V31-002).
7. Plus the deferred v0.3.0 items: leaderboard, HTTP timeouts, batch retry, dead admin.

Files in this audit packet:

- `reviewer-arch.md`
- `reviewer-sec.md`
- `reviewer-perf.md`
- `adversary.md`
- `SYNTHESIS.md` (this file)
