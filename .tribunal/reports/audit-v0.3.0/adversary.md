# Adversary Attack Report — Tribunal v0.3.0 Consensus

**Adversary:** `tribunal-adversary` (single-model)
**Targets:** the three trio reports at `.tribunal/reports/audit-v0.3.0/reviewer-{arch,sec,perf}.md`
**Verdict:** **BREAKS**

## Summary

The trio's consensus is undermined by four distinct attack categories they either missed or incompletely analyzed: (1) a label→pubkey index leak after agent rotation that violates the assumption of "one agent per label"; (2) missing character validation on `finding_id` / `plan_id` / `claim_hash` that enables canonical-format ambiguity for downstream parsers; (3) a latent mismatch in the Go client's stake type (uint64 vs u128) that's invisible until stakes exceed 2^64; (4) a `Config.admin` field that's stored but never checked — latent-permission antipattern. The trio's two main blockers (wire-format mismatch F-ARCH-001, input validation F-SEC-002) are sound, but they under-explored the rotation lifecycle and omitted label cleanup entirely.

## Attacks

### A-ADV-001: Old label remains queryable after rotation (Warning, refinement_mismatch)

**Target:** Architectural assumption — implicit in all three trio reports — that `AGENTS_BY_LABEL` maintains 1:1 active-agent uniqueness. Reviewer-arch F-ARCH-004 hinted at rotation gaps but didn't surface this.

**Attack:** `contracts/tribunal-reputation/src/execute/rotate.rs:64` saves only the new label binding:
```rust
AGENTS_BY_LABEL.save(deps.storage, new_label.as_str(), &new_pubkey)?;
// old label is never deleted
```

After rotation, `agent_by_label(old_label)` still resolves to the retired pubkey, returning a record with:
- `retired_at: Some(timestamp)` (accurate)
- `balance: <pre-rotation balance>` (stale — the successor has reset to `rotation_floor`)
- `tp_count, fp_count: <carried over>` (accurate for the retired agent)

Worse, the old label can NEVER be reused — rotate.rs:32 blocks any new agent from claiming a label already in the index, including labels of retired agents. Labels are effectively burned forever once used.

**Why it succeeds:** The contract has no cleanup of the old-label binding in either rotate or any subsequent execute. The Go `AgentByLabel` query (`internal/chain/query.go:123-134`) returns whatever the index points to with no liveness check.

**Severity:** Warning (label lookups are convenience, not a correctness path — pubkey lookups work correctly. But it's undocumented behavior that violates an intent the methodology doc implies.)

**Suggested defense:** In `rotate.rs`, either (a) `AGENTS_BY_LABEL.remove(deps.storage, old_label.as_str())` after retiring the agent, or (b) document explicitly that label lookups may return retired agents and clients must check `retired_at`. The current methodology doc is silent on this.

### A-ADV-002: Canonical format allows pipe-character injection in `finding_id` / `plan_id` / `claim_hash` (Warning, shared_blind_spot + adversarial_input)

**Target:** Reviewer-sec F-SEC-002 flagged unvalidated string lengths and noted "pipe-separated fields assume pipe-free fields", but concluded "signature verification still works (canonical bytes are deterministic)". True — but the downstream attack is parsing ambiguity, not signature verification.

**Attack:** The canonical finding message format is `TRIBUNAL_FINDING|<plan_id>|<finding_id>|<severity>|<claim_hash>|<stake>`. If a `finding_id` contains a pipe, e.g. `"F-001|malicious"`, the bytes become:

```
TRIBUNAL_FINDING|P-42|F-001|malicious|critical|sha256:abc|8
```

A downstream parser that splits on `|` mis-aligns every field. The on-chain commit succeeds (canonical bytes are deterministic). Off-chain ledger viewers, audit dashboards, and any code that parses the canonical bytes naively will misinterpret.

**Why it succeeds:** No length check or charset validation on `plan_id` / `finding_id` / `claim_hash` in either `commit.rs::process_finding` or the Go side's `BuildFindingCommit`.

**Severity:** Warning (signature still verifies; impact is downstream tooling, not fund loss).

**Suggested defense:** In Rust `process_finding` and `process_resolution`, reject any of `plan_id` / `finding_id` / `claim_hash` containing `|`, `\x00`, or other control chars. Add the same check on the Go side in `BuildFindingCommit` for fast-fail.

### A-ADV-003: Stake type mismatch — Go uint64 vs Rust u128 — silent failure above 2^64 (Critical, hidden_assumption)

**Target:** Reviewer-sec F-SEC-001 flagged this but concluded it's not actionable today because current stakes (8, 4, 2) are safely below 2^64. The hidden assumption: stakes will never exceed `uint64::MAX`. If that assumption breaks, signatures silently fail.

**Attack:**
1. Operator configures a future stake or `outcome_reward_multiplier` value > 2^64.
2. Go `CanonicalFindingMessage` (`internal/chain/canonical.go:10`) accepts only `uint64`; values from a larger source get silently truncated when cast.
3. Agent signs the canonical bytes with the truncated stake.
4. Contract reconstructs the canonical bytes with the full `u128` value.
5. `ed25519_verify` returns false. Signature fails.

The contract correctly declares `stake: u128` (`state.rs:141`, `msg.rs::FindingCommit`). The Go canonical builder is hardcoded to `uint64`. The mismatch is silent until it fires.

**Why it succeeds:** The Go signature reflects truncated bytes; the on-chain signature check reflects untruncated bytes. No code in either direction guards against the cast.

**Severity:** Critical (blocks any future protocol upgrade that wants stakes > uint64::MAX, and the failure mode is silent — verification just stops working).

**Suggested defense:** Change `CanonicalFindingMessage` to accept stake as `string` (matching how `cosmwasm-std::Uint128` serializes on the wire), so the canonical representation is identical on both sides regardless of magnitude. This requires changing `Stake` in the Rust contract from `u128` to `Uint128` as well.

### A-ADV-004: Admin field stored but never used (Suggestion, hidden_assumption)

**Target:** No reviewer flagged this. Reviewer-arch focused on dependency direction; nobody audited the auth model for "what powers the admin holds".

**Attack:** `contracts/tribunal-reputation/src/contract.rs:24` saves `admin`. `src/state.rs:163` declares the `admin: Addr` field. Grepping for `cfg.admin` or `config.admin` across `src/execute/` returns zero callsites. No `UpdateAdmin`, no `MigrateContract`, no permission gates anywhere.

The admin is dead authority. A future maintainer who adds an admin-gated operation will believe admin "has powers" because the field exists — and may forget that the field has never been audited under power.

**Why it succeeds:** Dead code paths are hard to audit. The risk surface only opens when the next maintainer wires authority into the field without a fresh review.

**Severity:** Suggestion (no current attack; latent debt).

**Suggested defense:** Either (a) remove the field until there's a need, or (b) add a `// SAFETY: admin has zero powers — must be revisited if any execute path checks cfg.admin` comment, plus a test that grep-asserts no `cfg.admin` references in `src/execute/`.

## Attacks attempted that survived

1. **Rotation race on pending real-time commits.** A pre-rotation finding gets resolved post-rotation. `resolve.rs:100-102` loads `state.agent_pubkey` (the recorded filer), not the current agent — reputation applies to the retired record at commit time, matching the spec. ✓
2. **Batch plan-mismatch error code is misleading.** Reviewer-sec F-SEC-004 noted this; verified — it's UX, not a bug. ✓
3. **Half-applied state on batch failure.** CosmWasm rolls back the entire tx on any error return, so partial writes don't persist. ✓
4. **`(plan_id, finding_id)` replay with different claim_hash.** The contract's `(plan_id, finding_id)` key blocks the second commit regardless of `claim_hash`. The first committer "owns" the slot. There IS a frontrun vector (an attacker who knows a future finding_id can squat the slot with garbage), but the trust model already assumes operator-controlled finding_id minting. ✓
5. **Ed25519 malleability replay.** The contract uses `deps.api.ed25519_verify`, which is the underlying chain's verifier. Even if signature malleability existed, the `(plan_id, finding_id)` uniqueness check blocks replays at the same key. ✓
6. **xiond shell-out injection.** `exec.Command` doesn't go through a shell, so embedded spaces / metacharacters in `ChainID` etc. won't escape into a new command. The args are operator-controlled in any case. ✓
7. **Determinism of canonical bytes below 2^64.** Rust `format!("{}", u128)` and Go `strconv.FormatUint` both produce the same decimal representation for values that fit in both. No locale, no padding, no leading zeros. ✓

## Calibration note

I found 4 actionable issues. The trio's consensus on the two main blockers (F-ARCH-001 wire mismatch, F-SEC-002 input validation) is sound, but they under-explored the rotation lifecycle and the admin trust model. A-ADV-001 (label cleanup) and A-ADV-004 (dead admin) are pure refinement gaps that wouldn't be caught by any of the three lenses because no lens owns "agent-record lifecycle" as its primary scope. A-ADV-002 (pipe injection) extends F-SEC-002 from "storage cost" to "downstream parsing ambiguity" — a class the sec reviewer acknowledged but didn't classify as a separate attack. A-ADV-003 (stake type) sharpens F-SEC-001 from "latent constraint" to "critical-on-upgrade" — same defect, different prioritization.

The methodology promise: "trust is a function of surviving adversarial scrutiny, not consensus." The trio approached consensus on `Request Changes`; the adversary breaks that consensus on rotation lifecycle and admin trust. **BREAKS.**
