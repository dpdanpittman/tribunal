# Synthesis — P-v034-audit (the convergence test)

**Date:** 2026-05-14
**Diff:** `fb37c3c^..fb37c3c` (`v0.3.4: audit-driven fix release (P-v033-audit findings)`)
**Verdict:** **Escalate.** Convergence outcome: **#2 — Pivoted but not converged.** The methodology converged on the specific defect shape P-v033-audit named (regex-input-grammar narrowness), but the LCD-as-truth-oracle defect family persists in the new primitive. v0.3.5 must address the family architecturally, not patch another instance.

## What this audit was designed to answer

P-v033-audit's adversary identified F-NEW-403: across three audit cycles, each fix was a more precise version of the same primitive (parse-the-LCD-error-string), and each version was narrower than the contract's true error grammar. The adversary prescribed the architectural pivot for v0.3.4: replace the regex with a structured contract-state query.

v0.3.4 shipped the pivot. The regex helpers (`matchDuplicate`, `alreadyCommittedRE`, `alreadyResolvedRE`) and the `regexp` import are gone. Recovery now re-uses `preflight()` — the same parallel contract-state-query primitive the success path uses.

The convergence question this audit was designed to answer: **did the pivot break the recursion, or just relocate it?**

Three possible outcomes were named in advance:

1. **Converged.** No new Critical; the recursion is broken.
2. **Pivoted but not converged.** A Critical in a DIFFERENT defect class.
3. **Still iterating.** A Critical in the SAME recursion shape.

The audit produced empirical evidence for outcome #2.

## The trio's split verdict

| Lens         | Verdict             | Findings (C/W/S)       | Convergence framing                                         |
| ------------ | ------------------- | ---------------------- | ----------------------------------------------------------- |
| Architecture | **Approve**         | 0 / 2 / 3              | "Converged on the defect class P-v033-audit named."         |
| Security     | **Request Changes** | 1 / 1 / 2 + 5 carryfwd | "PIVOTED BUT NOT CONVERGED."                                |
| Performance  | **Approve**         | 0 / 0 / 3              | "Unambiguous perf win; no recursion defect from this lens." |

arch and sec are reviewing the SAME mechanism (the structured-query recovery layer's trust in LCD responses) and reaching different verdicts because they're using different threat models:

- **arch's model:** "LCD might be unavailable" (loud failure surface; mapped to F-ARCH-401 Warning).
- **sec's model:** "LCD might be actively hostile" (silent suppression surface; mapped to F-SEC-401 Critical).

The trio's split itself is methodology signal. The lenses are working as designed — each catches what its training corpus is anchored on. Resolving the disagreement requires looking at WHAT defect family these mechanisms belong to, not whether they qualify under one lens' narrow definition. That resolution is what the adversary stage exists for.

## The adversary's resolution + the load-bearing Critical

The adversary's verdict: **sec is correct.** F-SEC-401 belongs to the same defect FAMILY as v0.3.2's F-SEC-201 (LCD trust) and v0.3.3's F-SEC-301 (LCD-tainted text). v0.3.4 broke the specific SHAPE (regex-grammar-narrowness) but the FAMILY (LCD-as-truth-oracle) persists.

But the adversary's load-bearing contribution is **F-NEW-501**, a Critical the trio entirely missed:

> The silent-suppression vulnerability sec found on the RECOVERY path also exists on the SUCCESS path. The attacker doesn't need to trigger Execute failure at all — just lie on the initial preflight, `commits` ends up empty, `submitCommitBatch` is never invoked, `SyncPlan` returns `{FindingsSent: 0}` with nil error. Observationally identical to a normal idempotent re-sync.

F-NEW-501 has been exploitable since v0.3.2 — across THREE audit cycles — because each cycle's lens reviewers only audit the NEW code in that diff. The success-path preflight is older code (added in v0.3.2, unchanged in v0.3.3, unchanged in v0.3.4). The lens reviewers' diff-bounded scope is exactly the gap that adversarial review exists to fill.

This is one of the most important methodology findings to date. It demonstrates:

1. **The lens trio has a structural blind spot.** Diff-bounded review by design only examines the new code in the diff. Latent defects in OLDER code (still load-bearing in the new release) survive cycles forever unless something else looks at them.
2. **The adversary's role is broader than "find what the trio missed in THIS diff."** It's "find what the trio missed across all cycles, given what's still load-bearing now."
3. **The methodology is finding defect SHAPES but not defect FAMILIES.** Each fix patches one instance of LCD-trust; the family keeps surfacing in new code that joins the same family.

## The architectural recommendation: F-NEW-505

The adversary's prescription for v0.3.5:

> v0.3.5 must address the LCD-as-truth-oracle family architecturally (ABCI-proof reads), not patch another instance within it.

ABCI-proof reads (CometBFT's verifiable state queries) would let the Go client cryptographically verify that the contract's state, as reported by an LCD, is actually the state the validator set agreed on. The LCD becomes untrustworthy infrastructure — its responses are checked against signed commitments — and the trust hole closes architecturally instead of by patching individual instances.

The convergence theory observation: **a methodology can converge on a defect SHAPE without converging on its FAMILY.** That's a real lesson. Subsequent cycles must search not just for the same shape (which the current cycle is already designed against) but for new instances of the same FAMILY in code the cycle hasn't directly modified.

## Cumulative state across all audits

**Findings across three audit cycles:**

| Audit  | Diff   | Findings (C/W/S)    | Verdict                  |
| ------ | ------ | ------------------- | ------------------------ |
| P-v032 | v0.3.2 | 3 / 12 / 15 = 30    | Escalate (F-NEW-301)     |
| P-v033 | v0.3.3 | 1 / 9 / 11 = 21     | Escalate (F-NEW-403)     |
| P-v034 | v0.3.4 | **2 / 6 / 15 = 23** | **Escalate (F-NEW-501)** |

**Severity trajectory across audits:**

- Critical count: 3 → 1 → 2 (the 2nd in v0.3.4 is the latent 3-cycle defect, not a new shape)
- Warning count: 12 → 9 → 6 (downward trend)
- Suggestion count: 15 → 11 → 15 (stable)

Read those numbers carefully: the Warning count is converging downward (12 → 9 → 6), which suggests the surface-level defects ARE being reduced. The Critical count is non-monotonic because the adversary's role keeps surfacing latent defects in older code. That's evidence of **per-shape convergence with per-family non-convergence** — exactly what F-NEW-505 names.

## On-chain settlement

All 23 findings filed to the local ledger signed by their filing agent's keypair. All 23 resolutions written by `pm-alpha` as `true_positive`. Settled on `xion-testnet-2` via single `commit_finding_batch` + single `resolve_finding_batch`, total ~14s wall time.

| Tx      | Hash                                                               |
| ------- | ------------------------------------------------------------------ |
| commit  | `637BE806A0C1EAF806E18E600494BBFEA6C168663EC64C2E1A31FF6382723A2E` |
| resolve | `94CBE524124567D8A21627B28D6677C4FBBCEE35B5865C1814F1699974736124` |

Cumulative reputation across three audits (visible at [tribunal.mabus.ai/leaderboard](https://tribunal.mabus.ai/leaderboard)):

| Rank | Agent           | Balance | TP  | FP  |
| ---- | --------------- | ------- | --- | --- |
| 1    | reviewer-arch   | 260     | 23  | 0   |
| 2    | reviewer-sec    | 248     | 25  | 0   |
| 3    | adversary-alpha | 240     | 15  | 0   |
| 4    | reviewer-perf   | 160     | 12  | 0   |
| 5    | pm-alpha        | 100     | 0   | 0   |

reviewer-sec moved to #2 by filing the Critical (F-SEC-401) and 9 total findings in this cycle. adversary-alpha is third by count but every cycle they file 5-7 findings concentrated in Critical+Warning — the highest per-finding stake yield. reviewer-arch keeps the volume lead.

## v0.3.5 scope

The audit's prescription is unusual: don't ship a v0.3.5 that just patches F-SEC-401 + F-NEW-501. Instead, **address the LCD-as-truth-oracle family architecturally**.

Options:

### Option A — ABCI-proof reads (the adversary's recommendation)

CometBFT supports verifiable state queries via the `/abci_query` RPC endpoint with `prove=true`. The response includes the value, a proof, and the latest block header. The Go client verifies the proof against the validator set's signature, making the LCD untrustable as anything other than a transport.

Lift: substantial. New Go code, new dependency (or hand-rolled IAVL proof verification), and integration with the validator set. ~500 LOC estimate.

Tradeoff: closes the entire LCD-trust family in one architectural change. Future audits can't find another instance because there are no instances.

### Option B — Authenticated LCD via TLS-pinned cert

Continue trusting the LCD's response but require it to be served from a TLS endpoint with a pinned cert chain. A hostile LCD must compromise the cert authority, not just MITM the TCP stream.

Lift: small. ~50 LOC. Operator burden: needs to maintain cert pins.

Tradeoff: closes MITM, doesn't close compromised-LCD-operator. The defect family persists in a smaller form.

### Option C — Cross-LCD quorum

Query N independent LCDs (operator configures); accept response only if K of N agree. Trust the consensus, not any single LCD.

Lift: medium. ~150 LOC + config.

Tradeoff: closes most adversarial-LCD attacks if operator picks LCDs with independent operator-bases. Doesn't close coordinated quorum compromise.

The adversary recommends Option A explicitly. The other options are mentioned for completeness; they're patches of the family, not closure of it.

## Methodology meta-observations

This audit produced more methodology-level findings than any prior cycle. They're worth surfacing:

- **The lens trio's diff-bounded scope is a structural blind spot.** F-NEW-501 has been exploitable across three cycles because no lens reviewer ever looked outside the diff. The adversary stage is the methodology's only line of defense against latent defects in unchanged code. This is now empirically documented.
- **Per-shape convergence with per-family non-convergence is a real pattern.** Each cycle's specific defect shape (regex grammar, identifier character set, structured-query trust posture) gets fixed; the underlying family (LCD-trust) keeps generating new shapes. The methodology must search by family, not just by shape.
- **Split trio verdicts are signal, not noise.** When the trio disagrees (as arch and sec did here), the disagreement points to a genuine ambiguity in the defect class. The adversary's role becomes "resolve the disagreement at the family level" rather than "find what they all missed."
- **The methodology is more effective per-cycle than its first-cycle ratio suggests.** P-v032 produced 30 findings; v0.3.3's fix introduced 21 new ones (net surface reduction). P-v033 produced 21; v0.3.4's fix introduced 23 new ones BUT one is a 3-cycle-latent defect that doesn't reflect on v0.3.4's surface. Net surface reduction continues.

## Recommendation

1. **Do NOT cut a v0.3.5 patch release.** A patch of F-SEC-401 + F-NEW-501 would address the SHAPES but leave the FAMILY exploitable.
2. **Start v0.4 with the ABCI-proof reads as the architectural primitive** (Option A above). v0.4 was scoped for multi-org tenancy, slashing appeals, cross-chain reputation, fungible operator rewards, and the convergence controller. ABCI-proof reads becomes a sibling priority — it closes the LCD-trust family, after which the other v0.4 work proceeds on a sound foundation.
3. **Run P-v04-audit on the v0.4 commit that lands ABCI-proof reads** before any further v0.4 work. The convergence question moves from "did v0.3.X converge on this defect family?" to "did v0.4 close the family?" That's the next empirical test.
4. **Document F-NEW-501's three-cycle latency** in the convergence doc (`docs/convergence.md`) as a methodology lesson about diff-bounded review. Future audits should explicitly include "look outside the diff for instances of recently-identified defect families" in the adversary's charge.
