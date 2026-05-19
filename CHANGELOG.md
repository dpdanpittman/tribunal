# Changelog

All notable changes to Tribunal will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Public site at `tribunal.mabus.ai`** (`site/`) — Astro 4 + Tailwind. Hero + methodology, the canonical docs rendered from `docs/*.md`, case studies for each Tribunal self-audit, and a live on-chain leaderboard that queries the deployed contract on `xion-testnet-2` client-side. Multi-stage `Dockerfile` (Astro build → nginx), k8s manifests in `site/k8s/`, `site/deploy.sh` builds + deploys to the zaphod node via `hostPort`. Caddy on zaphod reverse-proxies `tribunal.mabus.ai` to `localhost:3400`.
- **Testnet deployment** at `xion1rw526nsectccl335slusux4szcpk77h23y8tyg5g9drhkhnhhnss9cps84` on `xion-testnet-2`. Same contract, public chain. Audits replayed on testnet so anyone can verify the commit + resolve txs via the LCD.
- **P-v033-audit** — Tribunal's second self-audit (against v0.3.3). 21 findings (1 Critical + 9 Warning + 11 Suggestion). Verdict Escalate. The adversary's headline meta-finding (`F-NEW-403`): the methodology is not converging on a fixed point — each fix is a more precise version of the same primitive (parse-the-LCD-error-string), and each version is narrower than the contract's true error grammar. Motivated v0.3.4. Settlement: commit `5126E66E...`, resolve `F2C0758C...`.
- **Methodology extension: convergence (`docs/convergence.md`, `docs/adr/0001-convergence-controller.md`).** Single-pass review tells you what's wrong; a converging review tells you when you're done. Spec for a multi-round loop with rotated panel composition per round, configurable stopping criteria (`consecutive-clean(n)`, `no-novel-findings`, `adversary-explicit-pass`, `severity-floor`, `max-rounds`), implementer separation by keypair label, and per-round reputation feedback. Implementation phased: v0.4.0 ships output-only loop (`tribunal converge`), v0.4.1 adds the implementer interface, M3 adds auto-apply.

## [0.5.1] — 2026-05-18

The trajectory-access release (M2 of ADR-0003). The temporal lens (v0.5.0) audits longitudinal claims; v0.5.1 gives it the primitive it needs to read trajectory data — the full history of a plan's convergence rounds and signed-ledger entries — without ad-hoc filesystem walks. Operators get the same view for inspecting multi-round audits.

### Added

- **`tribunal history --plan <id>` command** (`cmd/tribunal/history.go`). Emits a structured timeline of a plan: per-round convergence results (loaded from `.tribunal/convergence/<plan>/round-NNNN.json` via the existing `converge.LoadHistory`), the signed-ledger findings + resolutions filtered to the plan, and a high-level summary (round count, unique claims, carry-forward count, final verdict, stop reason).
- **`--format text|json` flag**. Text is for operators; json is the canonical machine input for the temporal reviewer. Per-round fields include `findings_by_severity`, `novel_findings`, `carried_forward`, `patch_authored` / `patch_applied`, `verify_ran` / `verify_passed`, `stopped` / `stop_criterion`. Summary fields include `unique_claims`, `carried_forward`, `final_verdict`, `stopped_at_round`, `outcomes_by_kind`. Empty slices serialise as `[]`, not `null`, so consumers don't need to handle nil specially.
- **Documentation update to `agents/tribunal-reviewer-temporal.md`** — a new "Using `tribunal history`" section that explains which json fields the lens should reach for during audit. The lens is expected to invoke `tribunal history` via `Bash` early in its run when a multi-round trajectory exists.

### Read primitives (already present)

- `internal/converge.LoadHistory(projectRoot, planID)` returns `[]RoundResult` sorted ascending by round. Predates v0.5; v0.5.1 only adds a CLI surface on top.
- `internal/ledger.Ledger.All()` returns all findings + resolutions; v0.5.1's `loadPlanLedger` adds a plan-id filter at the CLI layer.

### Out of scope (still ahead)

- **M3 (v0.5.2)**: stateful PBT via `rapid` — convert temporal-lens findings about state-machine properties into executable trajectory tests.
- **Cross-plan findings**: still deferred per the v0.5.0 decision; revisit when there's empirical demand from the lens.

## [0.5.0] — 2026-05-18

The temporal-lens release (M1 of ADR-0003). Tribunal's lens-parallel methodology gains a fourth, opt-in reviewer for systems whose central claim is longitudinal — memory, identity, accumulation, drift, continuity. The first audit of `session-essence` (P-session-essence-audit) surfaced 11 adversary findings the trio missed, all living in the seams between co-designed components. Generalising the observation: when a system's load-bearing property is the integral of small per-cycle changes, the per-component lenses systematically underweight it. The temporal lens is the first-line reviewer for that class of defect.

### Added

- **`agents/tribunal-reviewer-temporal.md`** — the fourth lens-parallel reviewer. Six-axis checklist: reflexive loops (output → own-input cycles), accumulation properties (per-cycle vs. per-trajectory invariants), composition seams (co-designed but separately versioned components), calibration / drift detection (is there a ground-truth check?), failure-mode visibility over time (when does the operator find out?), marketing-vs-engineering split (does the documentation make falsifiable claims?). Severity ladder tuned for longitudinal stakes: Critical reserved for undefended + undetectable properties; Warning for documented-but-contradicted; Suggestion for latent failure modes that haven't fired yet.
- **`intent.md` schema: `audit_axes` field** — selects which reviewer lenses dispatch. Defaults to `[architecture, security, performance]`; add `temporal` to opt in the fourth lens.
- **`intent.md` schema: `temporal_artifact_paths` field** — optional list of paths the temporal lens may `Read` during audit. Lets the lens audit the deployed evidence (e.g., `~/.claude/essence/portrait.md`, archive directories) rather than the spec alone. Read-only by contract; inherits operator filesystem permissions.

### Changed

- **`skills/tribunal-review/SKILL.md` — Stage 1 dispatch is now `audit_axes`-driven.** When the intent declares `temporal`, the skill dispatches `@tribunal-reviewer-temporal` in the same single message as the trio. When absent or only the three defaults are listed, behavior is identical to v0.4.x.
- **`docs/methodology.md` — four-lens panel documented.** Roles table adds `@reviewer-temporal` (optional). Stage 1 section adds the `audit_axes` schema, the opt-in trigger ("is the system's central claim longitudinal?"), and a clawpatch-path note (clawpatch upstream owns its lens set; temporal coverage in v0.5 is a native-dispatch feature).

### Decisions captured (vs. ADR-0003's open questions)

- **Cross-plan findings: deferred to M2.** v0.5.0 lens files per-plan only ("as of this plan, the trajectory shows X"). The ledger's `plan_id` schema does not change. Revisit when M2's `tribunal history <plan>` primitive ships and there's real cross-plan demand.
- **Default model: `opus-4-7`, overridable.** The temporal lens is integrative (holds many cycles in working memory, notices trajectory patterns) — adversary-shaped, not trio-shaped. Different cognitive demand from the parallel-narrow trio. Empirical validation in M2/M3 can drop to sonnet if findings prove a cheaper model is sufficient.
- **Live artifact access: opt in via `temporal_artifact_paths`.** Operator declares paths in `intent.md`; lens inherits operator filesystem permissions. No new trust boundary crossed (the parent Claude could already read the same paths) — just explicit use of existing access, bounded by OS perms.

### Adversary intake

The adversary stage's report glob (`internal/review/hybrid.go`) already accepts any `reviewer*` filename, so `reviewer-temporal.md` is picked up automatically alongside the trio's reports when present. No code change in the adversary path.

### Out of scope (later milestones)

- **M2 (v0.5.1)**: `tribunal history <plan>` for emitting a structured timeline of all rounds; ledger read primitives for the temporal lens.
- **M3 (v0.5.2)**: stateful PBT via `rapid` — convert temporal-lens findings about state-machine properties into executable trajectory tests.

## [0.4.5] — 2026-05-17

The implementer-reputation release. v0.4.2 added the implementer; v0.4.3 added the verify gate; v0.4.5 closes the feedback loop. Implementers that ship patches passing verify accumulate reputation; implementers that ship patches failing verify (or failing `git apply --check`) lose stake. The local ledger captures the signal immediately; on-chain settlement happens via the existing `tribunal chain sync` path.

### Added

- **`converge.ReputationSink` interface** + `ImplementerOutcome` struct. The controller calls the sink after every implementer invocation with a structured outcome (patch hash, severities addressed, verify verdict). Decouples the converge package from the agent registry + ledger persistence so the controller stays embeddable.
- **`ledgerReputationSink`** (in `cmd/tribunal/converge_reputation.go`) — production sink that:
  - Auto-registers the implementer's keypair on first use (role=`implementer`).
  - Auto-registers a system `convergence-verifier` keypair (role=`qa`) for signing auto-resolutions.
  - Writes a synthetic `ledger.Finding` per authored patch with the implementer as agent + patch sha256 as claim_hash + severity drawn from the highest severity of the findings the patch addressed.
  - Writes an auto-`ledger.Resolution` when the verify gate produced a verdict: `TruePositive` on pass, `FalsePositive` on fail. Resolver = `convergence-verifier`.
- **`--no-implementer-reputation` CLI flag** to disable the feedback loop. On by default when `--implementer` is set.
- **Controller emission points**: outcome emitted after each implementer exit branch (M2 no-verify, M3 verify-pass, M3 verify-fail, M3 gate-error). The verify-pass branch is the only one that records `TruePositive`; everything else lands `FalsePositive` or no resolution (depending on whether the outcome is terminal).

### Semantics

- **Refused outcomes are not reputation events.** When the implementer returns `Refused: true`, the controller skips the sink entirely — refusal is operator signal that the patch was outside the implementer's safe scope, not a sign of low quality.
- **M2 outcomes (patch applied, no verify) file the Finding but no Resolution.** Settlement awaits the next round's verify gate OR manual operator action (PM/QA marking the finding TP/FP via `tribunal ledger ...`). This keeps the reputation math honest — only verified outcomes settle automatically.
- **Apply-failure outcomes file a `FalsePositive`** even without a verify gate. A patch that fails `git apply --check` is by definition a worthless artifact; the implementer's claim was wrong before verify even got a chance to run.
- **Sink errors are non-fatal.** A failing reputation write lands in the round's `PatchError` field but doesn't halt the loop — reputation is best-effort, not load-bearing.
- **Severity selection**: the synthetic Finding's severity equals the highest severity of the findings the patch was authored to address. A critical-fix that ships clean accumulates more reputation than a warning-fix.

### On-chain settlement

v0.4.5 ships the LOCAL feedback layer. To settle implementer reputation on-chain:

1. `tribunal chain register implementer-<model-id>` — register the implementer keypair (one-time per implementer).
2. `tribunal chain register convergence-verifier` — register the system verifier (one-time per project).
3. `tribunal chain sync` — flushes the ledger entries the controller wrote.

The on-chain registration step is intentional in v0.4.5 — auto-registration would invoke `xiond` from inside the converge loop, which expands the failure surface (xiond not in path, key resolution races, gas pricing). The local feedback works end-to-end without it; on-chain settlement is an explicit operator step.

### Tests

- `TestReputation_M2NoVerifyEmitsFindingOnly` — implementer authors a patch in M2 mode (no verify gate); sink sees `VerifyRan=false`, `NeedsResolution()=false`. Local Finding lands; no auto-Resolution.
- `TestReputation_M3VerifyPassEmitsTruePositive` — full M3 happy path; sink sees `IsTruePositive=true`; loop continues to clean rounds + converges.
- `TestReputation_M3VerifyFailEmitsFalsePositive` — verify failed; sink sees `IsTruePositive=false` + the verify summary forwarded.
- `TestReputation_RefusalEmitsNothing` — implementer refused; sink not invoked (refusal isn't a reputation event).
- `TestReputation_SinkErrorRecordedNotFatal` — sink returns error; loop completes; error lands in round's `PatchError` field.

### Internal API

- `Controller.Reputation` field — non-nil sink enables feedback emission.
- `Controller.emitImplementerOutcome(ctx, round, target)` — internal helper that builds the outcome from round state. Called from every implementer-bearing exit path.
- `patchHash(path) string` — sha256 of the patch file body, formatted as `sha256:<64-hex>` for ledger compatibility.

### What v0.4.5 does NOT ship

- **Auto on-chain registration**. Operators still run `tribunal chain register` manually for the implementer + verifier keypairs before `tribunal chain sync` will accept the entries. Auto-registration is v0.4.6 scope.
- **Stake math for implementer findings.** The synthetic Finding uses the severity's default stake (same as a regular adversary finding); ADR-0001 hinted at a separate stake schedule for implementers (proportional to findings-addressed). Deferred.
- **Implementer leaderboard view.** The current leaderboard lumps all roles together; a per-role split (adversaries vs implementers vs reviewers) is a separate site-side feature.
- **PM-driven manual resolution of M2 outcomes.** The ledger entries are visible but `tribunal ledger resolve <finding-id> --outcome true_positive` needs explicit operator action. A `--auto-resolve` flag on `tribunal ledger` for IMPL-prefixed findings might land in v0.4.6.

## [0.4.4] — 2026-05-17

The property-based testing release. The multi-adversary synthesis recommended PBT as "the most likely primitive to find what no adversary articulates" — the v0.4.x line has been deferring it since v0.4.0. v0.4.4 wires `pgregory.net/rapid` into the verification pyramid and ships 12 properties across 4 packages.

### Added

- **`pgregory.net/rapid` v1.3.0** as a runtime test dependency. Modern Go PBT library with automatic shrinking; properties live in `*_pbt_test.go` files and run under the existing `go test ./...` (no new build target needed — they're picked up by the `go-test` layer of the verification pyramid).
- **12 property tests across 4 packages**, covering the invariants the v0.3.x / v0.4.x releases established:
  - **`internal/ledger`** — 3 properties (signature roundtrip, tamper-detection, canonical-payload determinism). The cryptographic primitive at the heart of every signed Finding; tamper-detection runs 1000+ random Finding shapes through Sign → mutate any field → Verify-must-fail.
  - **`internal/chain`** — 2 properties (commit + resolution chunking invariants). Concatenation correctness, ≤MAX_BATCH_SIZE per chunk, exact-max-except-last shape, and the ceil(N/100) count rule. Pins v0.3.5's F-OPUS-003 fix across the full input space.
  - **`internal/converge`** — 3 properties (ConsecutiveCleanCriterion biconditional, MaxRoundsCriterion threshold, NoNovelFindings carry-forward logic). Each criterion has a reference implementation in the test file; rapid drives random histories through the production criterion + the oracle and asserts they agree on every input.
  - **`internal/dispatch`** — 3 properties (BucketByModelTier purity + known-tier classification + BucketByVendorFamily fixed-map). The v0.4.0 intra-Claude reshape depends on opus/sonnet/haiku each occupying distinct model_tier buckets; the PBT pins that across any input.

### Methodology

- The methodology has had PBT in the verification pyramid as a documented layer since v0.1 — `_pbt_test.go` files now make it real for the first time. Tests run as standard Go tests under the existing `go-test` pyramid layer; no special invocation needed.
- See `docs/methodology.md` "verification pyramid" section for guidance on when to write a PBT vs. an example-based test. Short version: PBT when the property is a cross-input invariant (every signed payload verifies; every chunking output reassembles; every panel rotation preserves member count); example-based test when the property is a specific scenario (a hostile LCD claims X under conditions Y).

### What v0.4.4 does NOT ship

- **PBT against the Rust contract surface** — `contracts/tribunal-reputation/src/validate.rs` has the most interesting input-space invariants (printable-ASCII enforcement, length bounds, character class). `proptest`-driven PBT for the Rust side is a separate v0.5.x scope.
- **Stateful PBT** — rapid supports state-machine-style PBT (random sequences of operations against a system). Tribunal's ledger + chain layer would benefit from this; deferred.
- **Implementer reputation feedback** — the remaining open item from the v0.4.x line. Targeting v0.4.5.

## [0.4.3] — 2026-05-17

The auto-continue release. v0.4.1 shipped the convergence loop (M1); v0.4.2 added the implementer (M2); v0.4.3 closes ADR-0001 with M3 — the loop can now self-drive end-to-end with a verify gate and revert-on-failure safety. The "ship at convergence, not on a schedule" methodology promise from v0.3.4 finally compiles.

### Added

- **`VerifyGate` interface** + `PyramidVerifyGate` adapter in `internal/converge/`. After an implementer patch lands, the M3 path calls `Verify(ctx, projectRoot)` and routes the verdict back into the controller's loop decision. `PyramidVerifyGate` delegates to `internal/verify.Run` so it reuses the same layers as `tribunal verify` (go-build, go-fmt, go-vet, go-test, plus any operator-configured extras).
- **`RevertWorkingTree(ctx, projectRoot)`** — `git reset --hard HEAD` + `git clean -fd`. Called when the verify gate fails so the operator inherits a clean tree instead of a half-broken patch. Safe under the M3 precondition (ApplyPatch ran from a clean tree, so reset reverts exactly the patch and nothing else).
- **`Controller.AutoContinue` field + `--auto-continue` CLI flag.** Requires `--auto-apply` (CLI-enforced; the library is permissive). When set, the controller runs the verify gate after each implementer apply; on pass it `continue`s the loop iteration instead of exiting `StatusNeedsFixes`. The loop terminates the same way it always has — converged when a stopping criterion fires, budget-exhausted when the budget bounds hit.
- **`RoundResult` extension**: `VerifyRan`, `VerifyPassed`, `VerifySummary`, `Reverted` fields on the persisted round JSON. `omitempty` so old rounds decode unchanged.

### Changed

- **`workingTreeClean` now ignores `.tribunal/` paths.** The controller writes round ledgers, patch artifacts, and signed findings to `.tribunal/` during the same flow that invokes ApplyPatch — those writes don't represent operator pending changes and shouldn't block the apply. Modifications outside `.tribunal/` still refuse as in v0.4.2.

### Semantics

- **M3 trigger**: `AutoApply` true AND `AutoContinue` true AND `VerifyGate` non-nil AND the just-completed round actually applied a patch (`PatchApplied == true`). Any of these false → fall through to the M2 path (exit `StatusNeedsFixes`).
- **Verify pass**: the round records `VerifyRan + VerifyPassed`; the working tree stays modified; the loop continues to the next round. The next round's preflight sees the patched tree as the new baseline.
- **Verify fail OR gate error**: the controller calls `RevertWorkingTree`, records `Reverted + VerifySummary`, exits `StatusNeedsFixes` with the verify summary surfaced in the result reason. Operator inspects, fixes manually, re-invokes.
- **Budget bounds still apply unchanged**: every round (apply or not) increments the round count and adds to the cumulative token cost. M3 doesn't bypass `MaxRounds` / `MaxTokens` / `MaxWallclock` — auto-continue just lets one invocation cover more of the budget.

### Tests

- `TestM3_VerifyPassContinuesLoop` — happy path; round 1 patches + verify passes; rounds 2-3 are clean and `consecutive-clean(2)` converges. File ends at the patched state.
- `TestM3_VerifyFailRevertsAndExits` — patch lands but verify fails; controller reverts; `StatusNeedsFixes` returned with the verify summary in `Reason`. File ends at the pre-patch state.
- `TestM3_VerifyGateErrorReverts` — gate itself errors (not just a failing verdict); still triggers revert + exit.
- `TestM3_RequiresAutoApply` — `AutoContinue=true` with `AutoApply=false` falls through to M2; verify gate is not consulted.
- `TestRevertWorkingTree` — direct unit test of the helper: modified tracked file restored, untracked file removed.

### Usage

```
# Full M3 loop:
tribunal converge --plan P-42 --implementer claude-opus-4-7 --auto-apply --auto-continue

# Still-supported earlier modes:
tribunal converge --plan P-42                                         # M1 output-only
tribunal converge --plan P-42 --implementer claude-opus-4-7           # M2 patch authored, presented for review
tribunal converge --plan P-42 --implementer claude-opus-4-7 --auto-apply
                                                                      # M2 patch authored + applied, no continue
```

### ADR-0001

Promoted: M1 + M2 + M3 all implemented. The "ships last with extensive testing" guidance in the ADR is honored by (a) `--auto-continue` requiring an explicit operator opt-in over both `--implementer` and `--auto-apply`, (b) the verify-gate + revert-on-fail safety, and (c) the unchanged budget caps that still bound the loop end-to-end.

## [0.4.2] — 2026-05-17

The implementer release. v0.4.1 shipped the convergence loop without fix authoring; v0.4.2 ships ADR-0001 milestone M2 — a pluggable Implementer that drafts a patch between rounds.

### Added

- **`Implementer` interface** in `internal/converge/`. `Patch(ctx, PatchInput) → (*PatchOutput, error)` takes the round's unresolved Critical/Warning findings plus intent + diff + per-finding bodies and returns a unified-diff patch (or a structured refusal).
- **`ClaudeImplementer`** — production impl on top of `dispatch.ClaudeProvider.Generate`. Single-turn prompt with a strict two-block response format (REASONING + fenced PATCH diff); reasoning is preserved alongside the patch for the audit trail.
- **`dispatch.ClaudeProvider.Generate`** — raw text-completion helper. Same wire shape as `Attack` minus the adversary-report parser, so the implementer (and future Plan/Implement stages) can reach the Anthropic Messages API without going through the dispatch panel surface.
- **`Controller.invokeImplementer`** — called when a round produces unresolved Critical/Warning AND `Controller.Implementer` is non-nil. Persists artifacts under `.tribunal/convergence/<plan-id>/round-NNNN-patch.{diff,md}` and (when `AutoApply` is true) routes the patch through `git apply`.
- **`ApplyPatch(ctx, projectRoot, patch)`** — `git apply --check` + `git apply`. Refuses on a dirty working tree (conflation of implementer hunks with operator pending changes). Returns the list of touched files parsed from `git apply --numstat`.
- **Controller hooks**: `IntentLoader`, `DiffLoader`, `FindingBodyLookup` — injection points the CLI wires to feed the implementer prompt with on-disk artifacts (plan intent, raw diff, per-finding markdown). Keeps the converge package free of filesystem assumptions.
- **CLI flags**:
  - `--implementer <model-id>` — opt in to LLM patch authoring. Empty (default) keeps the v0.4.1 M1 output-only behavior. Models: `claude-opus-4-7`, `claude-sonnet-4-6`, `claude-haiku-4-5-20251001`, or any future Anthropic model id.
  - `--auto-apply` — apply the patch via `git apply` after authoring. Requires `--implementer`. Refused on dirty tree. The controller still exits `needs_fixes` so the operator runs tests + commits + re-invokes.

### Safety

- The patch flow always exits with `StatusNeedsFixes`, even after `--auto-apply`. M3 (auto-apply + auto-continue) is explicitly out of v0.4.2 scope.
- `git apply --check` runs before any mutation; failures keep the working tree untouched.
- Dirty-tree refusal is non-negotiable in v0.4.2 — the message lands as the round's `PatchError` so the audit trail records why no apply happened.
- The implementer system prompt forbids invented file paths / symbol names and requires REFUSE for architectural changes that can't ship in one patch. Refusal reasoning is persisted to `round-NNNN-patch.md` even with no diff.

### RoundResult schema extension

The `RoundResult` struct (and its on-disk JSON) gain implementer fields: `patch_authored`, `patch_path`, `patch_readme`, `patch_refused`, `patch_applied`, `patch_files`, `patch_tokens`, `patch_error`. Old rounds without these fields decode cleanly — the JSON tags are `omitempty`.

### Tests

- `TestController_ImplementerAuthorsPatchOnFindings` — implementer is invoked when a round produces a Critical finding; patch + readme land on disk; `AutoApply=false` means no `git apply`.
- `TestController_ImplementerNotInvokedForSuggestionsOnly` — Suggestion-only rounds keep the loop running without involving the implementer.
- `TestController_ImplementerRefuse` — `Refused=true` persists the reasoning readme but no `.diff` file.
- `TestController_ImplementerPropagatesContext` — IntentLoader / DiffLoader / FindingBodyLookup hooks are forwarded into the PatchInput.
- `TestApplyPatch_RefusesOnDirtyTree` — guard fires when uncommitted changes are present.
- `TestApplyPatch_AppliesToCleanTree` — happy path: `git apply --check` passes, mutation visible in the target file.
- `TestParseImplementerResponse` — pins the REASONING + `diff` parser against valid + REFUSE response shapes.

### What v0.4.2 does NOT ship

- **M3 (auto-continue loop)** — controller doesn't dispatch the next round in the same invocation after a patch is applied. Operator still drives test/commit/re-invoke. Targeted v0.5+.
- **Implementer reputation feedback** — patches that fail `git apply --check` don't yet penalize the implementer's on-chain balance. The audit trail records the failures; the on-chain settle hook follows.
- **Multi-model implementer panel** — v0.4.2 ships one implementer at a time; a competing-implementers panel where multiple LLMs propose patches and the operator picks is a future expansion (no ADR yet).

## [0.4.1] — 2026-05-17

The convergence-controller release. Single-pass review tells you what's wrong right now; a converging review tells you when you're done. v0.4.1 ships milestone M1 of [ADR-0001](./docs/adr/0001-convergence-controller.md) — the output-only loop. The implementer interface (M2) and auto-apply (M3) follow in later releases.

### Added

- **`tribunal converge`** — release-gating loop that drives the single-pass adversary stage repeatedly with rotated panel composition until a stopping criterion fires or the budget exhausts. Each round writes a `round-NNNN.json` to `.tribunal/convergence/<plan-id>/`; subsequent invocations load history from disk so rotation stays informed across operator-fix gaps.
- **`internal/converge/` package** — the controller, panel rotators, stopping criteria, and round ledger.
  - `Controller.Run` orchestrates the loop; takes an injectable `AdversaryStage` so the loop is testable without LLM calls.
  - `PanelRotator` interface with two M1 implementations: `FocusShuffleRotator` (permutes member focus per round; works in single-provider environments) and `CompositeRotator` (the v0.4.1 default — composite of focus + model_tier axes).
  - `StoppingCriterion` interface with three M1 implementations: `ConsecutiveCleanCriterion(N)` (N back-to-back clean rounds), `NoNovelFindingsCriterion` (every finding in the round is a carry-forward by claim_hash), `MaxRoundsCriterion(N)` (escape valve).
  - `BudgetTracker` caps total rounds / tokens / wallclock with graceful exhaustion (each axis produces a distinct reason).
  - Per-round persistence at `.tribunal/convergence/<plan-id>/round-NNNN.json` with zero-padded names for stable lexical ordering.
- **CLI flags**: `--plan`, `--diff`, `--max-rounds`, `--max-tokens`, `--max-wallclock`, `--severity-floor`, `--rotation`, `--stop-on`, plus the standard `--adversary-md` / `--bucket` / `--no-ledger` / `--no-auto-register` flags inherited from `tribunal review`.
- **`review.Options.PanelOverride`** — non-empty value replaces the panel selected by `PanelName` for one invocation. Lets the convergence controller drive per-round rotated panels without mutating `tribunal.yaml`. Internal API; no behavior change for `tribunal review` callers.

### Semantics

- M1 is output-only: when a round produces unresolved Critical or Warning findings, the loop exits with status `needs_fixes`. The operator applies fixes manually and re-invokes. Subsequent invocations resume — round numbering, rotation, and stopping criteria all see the full history from disk.
- Stopping criteria AND together. The CLI also wires `MaxRoundsCriterion` from `--max-rounds` regardless of `--stop-on` so a misconfigured loop can't run forever.
- Findings are classified `carry_forward` against the historical claim_hash set on save; `NoNovelFindingsCriterion` uses that classification to declare convergence.

### Tests

- `TestController_StopsOnConsecutiveClean` — happy path; two clean rounds fire `consecutive-clean(2)`.
- `TestController_PausesOnFindings` — a round with a Critical finding exits with `needs_fixes`.
- `TestController_RotatesAcrossRounds` — the panel composition genuinely changes between rounds via `FocusShuffleRotator`.
- `TestController_LoadsHistoryAcrossInvocations` — a second `Controller.Run` picks up where the first left off via the on-disk ledger.
- `TestController_BudgetExhaustion` — `--max-rounds=1` halts before convergence.
- `TestController_NoNovelFindingsFires` — round 2 with all-carry-forward findings converges via the no-novel criterion.
- `TestParseStoppingCriteria` — `--stop-on` parser; valid cases + error cases.
- `TestSelectRotator` — `--rotation` parser; defaults + unknown-name error + composite axis validation.
- `TestLedgerRoundtrip` — `SaveRound` + `LoadHistory` are inverses; filename uses the zero-padded scheme.

### Exit codes

- `0` — converged
- `2` — fatal error
- `5` — needs_fixes (operator action expected before next invocation)
- `6` — budget exhausted

### Docs

- `docs/adr/0001-convergence-controller.md` — promoted from Proposed to Implemented (M1). The M2 / M3 milestone sections remain as the v0.4.2 / later roadmap.

### What v0.4.1 does NOT ship

- **Implementer interface (M2)** — pluggable agent that authors fixes between rounds. Deferred to v0.4.2.
- **Auto-apply mode (M3)** — the end-to-end loop without operator intervention. Deferred to v0.5+.
- **Property-based testing scaffolding** — promoted out of v0.4.0 but still on the roadmap; targeting v0.4.3.

## [0.4.0] — 2026-05-17

The intra-Claude diversity release. The first Tribunal version whose adversary-panel composition is grounded in empirical multi-adversary measurement, not theoretical vendor diversity.

P-multi-adversary (2026-05-17) ran four adversaries — Opus 4.7, Sonnet 4.6, Haiku 4.5, and a cross-family Qwen3-coder — against the v0.3.4 diff (`fb37c3c`). Four hypotheses tested. **H1 — cross-family additive diversity: REFUTED.** Qwen produced zero unique substantive findings the Claudes hadn't already raised. **H2 — intra-family disagreement: CONFIRMED.** Opus said BREAKS, Sonnet said INDETERMINATE, Haiku said SURVIVES on the same input; each surfaced distinct findings. The most novel finding of the entire panel — F-OPUS-004 (Unicode bypass of `looksLikeTestChain`) — came from intra-Claude diversity, not cross-family. Full synthesis at `.tribunal/reports/P-multi-adversary/SYNTHESIS.md`.

v0.4.0 ships the panel composition the data points at. The cross-family hypothesis stays live as TIER-2; intra-Claude is promoted to the load-bearing primitive.

### Changed

- **`default_panel` is now three distinct Claude model tiers** — `claude-opus-4-7` + `claude-sonnet-4-6` + `claude-haiku-4-5-20251001`, each with a different focus axis (spec / impl / temporal). The pre-v0.4 default was two opus variants + one sonnet — overlapping tiers that suppressed exactly the diversity signal the experiment validated. No extra API spend; fits inside a single Claude subscription.
- **`high_stakes_panel` reshape: intra-Claude trio + one cross-family slot.** Pre-v0.4 high-stakes was 4 distinct vendors with no Claude redundancy — strictly worse on the empirical evidence. v0.4.0 keeps the cross-family slot (defaults to local `qwen3:32b`) for the TIER-2 opt-in signal but makes intra-Claude the load-bearing layer underneath.
- **Default diversity bucket flipped from `composite:vendor_family,focus` to `composite:model_tier,focus`** in `tribunal review`, `tribunal dispatch attack`, and `SelectBucket("")`. The old default lumped all three Claudes into the same `anthropic` bucket — would have hidden the intra-tier disagreement signal v0.4.0 is built around.

### Tests

- `TestDefaultDispatchConfigShape` rewritten to pin the v0.4.0 invariants — default panel spans three distinct Claude model tiers (opus / sonnet / haiku); high-stakes panel includes the full intra-Claude trio plus at least one non-Claude vendor.

### Docs

- `docs/methodology.md` "Diversity" section rewritten with the P-multi-adversary empirical motivation. The pre-v0.4 framing ("three Claude variants with temperature × focus") is replaced with the new intra-tier framing and the explicit observation that the v0.3.X composition was the strictly-worse predecessor.

### What v0.4.0 does NOT ship

- **`tribunal converge`** — the convergence-controller M1 described in `docs/adr/0001-convergence-controller.md`. SYNTHESIS recommended deprioritizing this in favor of the panel-composition pivot. Re-scheduled for v0.4.1.
- **Property-based testing scaffolding.** SYNTHESIS recommended promoting PBT from v0.4.1; deferred to v0.4.2 after the convergence controller lands.

## [0.3.5] — 2026-05-17

Audit-driven fix release. P-multi-adversary's four-adversary panel (claude-opus, claude-sonnet, qwen3-coder, deepseek-r1) refuted H1 (cross-family additive diversity) but confirmed H2 (intra-family disagreement is real) — and surfaced six novel opus findings whose blast radius extended past what the lens reviewers caught. F-OPUS-004 (`looksLikeTestChain` Unicode bypass) shipped at e035a4f. v0.3.5 closes the remaining four — F-OPUS-001 / 002 / 003 / 005 — plus addresses F-OPUS-006 transitively via the F-OPUS-001 success-path fix. No contract changes; no migration. Audit reports: `.tribunal/reports/P-multi-adversary/SYNTHESIS.md` and `.tribunal/reports/P-v035-followup/`.

### Fixed

- **Hostile-LCD defense on the success-path preflight.** `preflight()` now returns the full `FindingState` / `ResolutionRecord` per id instead of opaque booleans. Both call sites — the success path in `SyncPlan` and the recovery loop in `submit{Commit,Resolve}Batch` — verify that the on-chain `claim_hash`, `agent_pubkey`, `severity`, and `stake` (or `evidence_hash`, `outcome`, `resolver_pubkey` on the resolution side) match the local copy before treating an entry as already-committed. A hostile LCD that fabricates a "this finding is already committed" response under a different payload is caught at `verifyOnChainCommit` and surfaced as a `preflight conflict` error; v0.3.4's recovery-path defense ([F-SEC-401]) is now mirrored on the success path. ([F-OPUS-001], [F-OPUS-006])
- **Client-side batch chunking against contract `MAX_BATCH_SIZE = 100`.** `submitCommitBatch` and `submitResolveBatch` now split inputs through `chunkFindingCommits` / `chunkResolutionCommits` before invoking the per-chunk recovery loop. Plans with >100 findings used to hit `BatchTooLarge` on every attempt and the structured-query recovery couldn't help (preflight returned no committed entries for fresh ones, so the loop bailed immediately). On partial chunk failure the function returns the chunks that did land so the operator sees what landed before seeing what failed. ([F-OPUS-003])
- **Outer sync ctx scales with plan count.** `cmd/tribunal/chain.go`'s 5-minute outer ctx silently truncated plans 4+ once their cumulative per-plan budget exceeded the fixed bound. New `chain.SyncBudgetForPlans(n)` returns `max(5m, n × perPlanSyncBudget × 1.2)`, and the CLI reads the ledger first to size the budget against the actual plan count. ([F-OPUS-002])
- **Recovery exhaustion error names remaining findings + last contract error.** v0.3.4's terminal error read `commit batch exhausted recovery attempts (cap=5)`, which gave the operator no information about which findings still needed to settle or why the contract was rejecting them. v0.3.5 includes the remaining finding IDs and wraps the last broadcast error: `commit batch exhausted recovery attempts (cap=5, remaining=2 [F-3 F-7]): last_error=...`. Symmetric on the resolve side. ([F-OPUS-005])

### Tests

- New `TestVerifyOnChainCommit_MatchAndMismatch` / `TestVerifyOnChainResolution_MatchAndMismatch` pin the F-OPUS-001 verification helpers across matching state plus one-field-mismatch cases per field.
- New `TestChunkFindingCommits` / `TestChunkResolutionCommits` pin the F-OPUS-003 chunking helper at 0 / 1 / 100 / 101 / 200 / 250 / 235 entries.
- New `TestSyncBudgetForPlans_Scales` pins the F-OPUS-002 helper at n=0,1,4,10,20.

### Internal

- `preflight()` signature change: returns `(committed map[string]*FindingState, resolved map[string]*ResolutionRecord)`. All three call sites updated. Public API surface unchanged.
- `submitCommitBatch` / `submitResolveBatch` split into a chunking outer (`submit{Commit,Resolve}Batch`) and a per-chunk inner (`submit{Commit,Resolve}Chunk`); the inner loop is what v0.3.4's outer was.
- New imports in `internal/chain/sync.go`: `strconv` (for stake comparison), `strings` (for chunk hash concat).

## [0.3.4] — 2026-05-14

Audit-driven fix release. P-v033-audit's adversary identified that the methodology was not converging on a fixed point — each fix was a more precise version of the same primitive (parse-the-LCD's-error-string), narrower than the contract's true error grammar. v0.3.4 changes the primitive, not the regex. No contract changes; no migration. Audit report: `.tribunal/reports/P-v033-audit/SYNTHESIS.md`.

### Fixed

- **Recovery via structured contract-state query, not regex.** `submitCommitBatch` and `submitResolveBatch` no longer parse the contract's `raw_log` for `FindingAlreadyCommitted` / `FindingAlreadyResolved` strings. On batch rejection they re-run `preflight()` — the same parallel contract-state query primitive the success path uses — get back an authoritative map of which entries are now committed/resolved, filter the batch accordingly, and retry. The regex helpers (`matchDuplicate`, `alreadyCommittedRE`, `alreadyResolvedRE`) are deleted entirely. This resolves F-ARCH-301 (regex narrower than `validate_id_field` permits — broke on slash/space in identifiers), F-SEC-302 (`TrimRight` corrupted legitimate IDs containing dots/quotes/parens), F-NEW-402 (regex didn't recognize `BatchMixedPlanID`), and F-SEC-301 (hostile LCD could choose which entry to drop via `raw_log`) — all four collapse into one architectural pivot. ([F-NEW-403])
- **`SyncAll` per-plan ctx isolation.** Each plan now runs under a `context.WithTimeout(ctx, perPlanSyncBudget)` (90s default), so one slow plan's recovery cycle can't starve subsequent plans of the caller's outer ctx. T7's "continue past per-plan failure" now works at the timing layer as well as the data layer. ([F-NEW-401])
- **`tribunal chain sync` renders partial results before erroring.** v0.3.3's `errors.Join` aggregation produced partial results on failure but the CLI discarded them; v0.3.4 prints what landed before returning the error. ([F-ARCH-303])
- **`looksLikeTestChain` is token-aware.** v0.3.3's `strings.Contains(id, "test")` false-positived on `xion-mainnet-test-fork` and similar hostile/borderline chain ids. v0.3.4 splits on `-`, treats `mainnet`/`main`/`prod`/`production` as always-wins, then checks `devnet`/`testnet`/`test`/`dev`/`local` as discrete tokens. Applied in both `internal/chain/client.go` and `cmd/tribunal-seed/main.go`. New test `TestLooksLikeTestChain_TokenAware` covers 11 cases including the hostile substring patterns. ([F-SEC-303])
- **Recovery loop bounded by a constant, not `len(batch)`.** v0.3.3 retried up to `len(commits)` times, which let a hostile LCD amplify gas consumption against large batches. v0.3.4 caps at `maxRecoveryAttempts = 5` regardless of batch size — five retries handles every realistic duplicate scenario; the remainder surfaces as an explicit error. ([F-ARCH-307])
- **`preflight_concurrency` is operator-tunable.** New `preflight_concurrency` field in `chain.yaml`; defaults to 8 when unset. Tune up on low-latency local LCDs; tune down on high-RTT or rate-limited LCDs. ([F-PERF-301])

### Tests

- New `TestLooksLikeTestChain_TokenAware` pins the token-aware test-chain heuristic across 11 cases (standard testnet/devnet/mainnet, hostile substring patterns like `xion-mainnet-test-fork` and `xion-test-mainnet-fork`, embedded substrings inside other words like `untested`/`attestation`).
- `TestMatchDuplicate_CommitErrorParsing` removed; the regex it tested no longer exists.

### Internal

- `sync.go` package doc updated to describe the structured-query recovery model.
- `regexp` and `strings` imports removed from `internal/chain/sync.go`.

## [0.3.3] — 2026-05-13

Audit-driven fix release. v0.3.2 itself was reviewed by the full Tribunal methodology — three lens reviewers (architecture, security, performance) plus an adversary panel — and the audit surfaced 3 Critical + 12 Warning defects that the manual e2e couldn't have caught. v0.3.3 fixes the Critical findings, every Warning that affects correctness or operator safety, and the cross-corpus blind spot the adversary alone identified. No contract changes; no migration. Audit report at `.tribunal/reports/P-v032-audit/SYNTHESIS.md`.

### Fixed

- **`WaitForTx` now distinguishes transient HTTP errors from terminal ones.** v0.3.2's wait loop bailed on the first non-404 error, defeating F4's stated goal — a single LCD blip or network reset would surface as a fatal failure even though the tx was in flight. v0.3.3 absorbs 5xx, connection-refused, timeout, and partial-body parse errors as transients (continues polling); 4xx other than 404 are terminal; on-chain `code != 0` is terminal. Each individual REST poll is now bounded by a 3s per-attempt timeout (the one v0.3.2's docstring already lied about) so a slow LCD can't starve the outer ctx. ([F-ARCH/SEC/PERF-201, F-SEC-202, F-PERF-202])
- **`Execute` propagates the `BroadcastResult` on `WaitForTx` error.** v0.3.2 returned non-nil `(res, err)` but every caller discarded `res`, losing the txhash for a tx that may have actually landed. Documented the contract explicitly in the function's docstring; callers can now resume polling or surface the on-chain status to the operator. ([F-NEW-302])
- **`SyncPlan` absorbs batched-tx atomicity failures via post-broadcast recovery.** The adversary's headline finding: v0.3.2's pre-flight tolerance breaks under CosmWasm batched-tx atomicity. The contract uses Rust's `?` operator, so a single already-committed finding short-circuits the entire `commit_finding_batch` — a 100-finding plan loses all 100 commits to one LCD false-negative. v0.3.3 keeps pre-flight as the fast path and adds a recovery layer: when a batch tx fails with `FindingAlreadyCommitted` (or `FindingAlreadyResolved` for resolutions), parse the offending finding ID, drop it from the batch, retry. Bounded by `len(batch)` retries (each retry guarantees one entry leaves), so termination is guaranteed. New unit test `TestMatchDuplicate_CommitErrorParsing` pins the regex against the contract's actual error strings. ([F-NEW-301, F-NEW-305])
- **Sync pre-flight is now parallel and bounded.** v0.3.2's pre-flight was N serial REST round-trips; at 100 findings that dominated sync wall time. v0.3.3 fans out 8 concurrent workers, each with a 3s per-attempt timeout. ([F-ARCH-203, F-PERF-203, F-SEC-203, F-SEC-204])
- **Sync respects `ctx.Err()` between pre-flight iterations.** v0.3.2's `continue` swallowed ctx cancellation, letting sync keep running after the caller had given up. ([F-ARCH-202])
- **Sync deduplicates resolutions.** Commits already had `seen[]` dedup; resolutions did not, so a ledger with a duplicate resolution would trigger `FindingAlreadyResolved` on the second one and revert the whole batch (caught now by the recovery layer, but cheaper to filter upfront). ([F-NEW-304])
- **`SyncAll` collects per-plan errors instead of aborting on the first.** A bad plan no longer prevents every subsequent plan from being settled. Returns successful results plus an `errors.Join`-wrapped error. ([F-NEW-303])
- **Wait loop and sync pre-flight now emit progress notes.** After 5s without resolution, both surfaces print a one-line stderr update with elapsed time + transient-error streak (for WaitForTx) or in-flight count (for pre-flight). Operators no longer stare at multi-minute hangs with zero signal. ([F-PERF-204])
- **`NormalizeRPCScheme` moved into `internal/chain/config.go` and called from `LoadConfig`.** v0.3.2 normalized `tcp://` → `http://` only at `chain init` write time; configs written before v0.3.2 (or by hand) kept their broken scheme and surfaced "unsupported protocol scheme" on every chain operation. Now normalization happens transparently on every config load. ([F-ARCH-204])
- **`outcome_reward_multiplier` is no longer auto-defaulted to 2.** v0.3.2's `applyDefaults` overrode any explicit 0 to 2, defeating F6 (which queries the contract for the real value) whenever the contract was instantiated with multiplier 0. A genuine 0 is a legitimate config — it means the contract returns stake without an outcome bonus. ([F-ARCH-205])
- **`tribunal-seed` now uses `flag` properly + ctx timeout + production-chain guard.** v0.3.2's argv parsing treated `--send` as a plan id; v0.3.3 uses `flag.Parse()`. The `--send` path now wraps Execute in a configurable timeout (default 60s) instead of `context.Background()`. New `--allow-prod` opt-out: by default the harness refuses to `--send` against a non-test-looking `chain_id`, preventing accidental fake-TP settlements against real reputation stake. ([F-ARCH-206, F-PERF-205, F-SEC-207])

### Tests

- New `TestMatchDuplicate_CommitErrorParsing` in `internal/chain/sync_test.go` covering the regex that drives batch recovery.

## [0.3.2] — 2026-05-13

Devnet-driven tooling release. The contract itself shipped clean in v0.3.1 — every audit fix verified end-to-end against a live `xion-devnet-1` chain (`wasmd v0.54.0`, `xiond v20.0.0`). What didn't ship clean was the deploy + sync tooling around it. v0.3.2 fixes six defects surfaced by the first real-chain test run. No contract changes; no migration required. Test-run report at `.tribunal/reports/devnet-e2e-2026-05-13.md`.

### Fixed

- **`scripts/deploy-contract.sh`: `--optimize` is now the default, not opt-in.** The dev-built wasm fails on wasmd v0.54+ with `Wasm bytecode could not be deserialized. Deserialization error: "bulk memory support is not enabled"`. The optimizer pass strips those ops and is required against any modern chain. New `--skip-optimize` flag for environments without docker; raw `cargo build` is now the escape hatch instead of the default. ([F1])
- **`scripts/deploy-contract.sh`: bumped `cosmwasm/optimizer` from `0.16.0` to `0.17.0`.** The 0.16.0 image ships Rust 1.78.0, which can't build `base64ct v1.8.3` (transitive through `cosmwasm-std`) because that crate requires `edition2024` (Cargo 1.85+). 0.17.0 ships a new-enough toolchain. Image tag is now also overridable via `OPTIMIZER_IMAGE` env. ([F2])
- **`tribunal chain init` normalizes `tcp://` RPC URLs to `http://`.** xiond accepts `tcp://localhost:26657` (Tendermint convention); Go's `net/http` does not. Without this, every `chain.*` command after init fails with `unsupported protocol scheme "tcp"`. The rewrite is logged to stderr so the user sees what happened. ([F3])
- **`internal/chain.Client.Execute` now waits for tx inclusion.** Previously, `--broadcast-mode sync` only confirmed mempool acceptance; back-to-back Executes (e.g. `register` then `sync`, or sync's own commit→resolve pipeline) hit `account sequence mismatch, expected N, got N-1` because xiond's cached sequence was stale. The client now polls `/cosmos/tx/v1beta1/txs/{hash}` on a 1s cadence until the tx lands or ctx is cancelled. ([F4])
- **`tribunal chain sync` is now idempotent against partial failure.** Before building a commit_finding_batch or resolve_finding_batch, sync queries the contract for each finding's current state and filters out entries already on-chain. Retrying after a partial failure no longer dies with `finding P-X/F-Y already committed`. Pre-flight query failures are tolerated (treated as "unknown") so flaky REST doesn't block sync — the contract's own duplicate guard remains the final authority. ([F5])
- **`tribunal chain init` auto-populates `outcome_reward_multiplier` from the deployed contract.** Previously it wrote `0` regardless of what the contract had; the field is documented as a client-side preview but was lying. Now `chain init` queries `Config{}` and writes the real value. Falls back with a warning if the chain is unreachable at init time. ([F6])

### Internal

- New `cmd/tribunal-seed/` — small throwaway harness used to seed the local ledger with a signed finding + resolution for e2e testing against a running chain. Not user-facing; useful for reproducing the test-run report and verifying future tooling fixes.

## [0.3.1] — 2026-05-12

Audit-driven fix release. The v0.3.0 contract works under `cw-multi-test`
but had a wire-format mismatch with the Go client that `cargo test` did
not catch (cw-multi-test bypasses the JSON boundary). The Tribunal
methodology's own audit — three lens reviewers + one adversary — surfaced
this plus the related issues below. Full audit packet at
`.tribunal/reports/audit-v0.3.0/`.

### Fixed

- **Wire-format alignment between Rust contract and Go client.** Every
  numeric on-chain field migrated from bare `u128` to
  `cosmwasm_std::Uint128`, which serializes as a decimal string — the
  shape the Go client already expected. Affected: `AgentRecord.balance`,
  `FindingState.stake`, `Config.{initial_balance, rotation_floor,
outcome_reward_multiplier}`, plus the corresponding query response
  types. Also adds `pubkey` to `AgentResp` and `retired` to
  `ReputationResp` to match the Go-side shape. ([audit F-ARCH-001,
  F-SEC-001, A-ADV-003])
- **`ResolutionRecord` split.** Replaced the single `reward_applied`
  field with `stake_returned` + `reward` so consumers can distinguish
  stake-return from outcome-reward without re-deriving from severity.
  ([audit F-ARCH-001])
- **Canonical signing format uses string stake.** The Go
  `CanonicalFindingMessage` signature changed from `stake uint64` to
  `stake string` so the canonical bytes mirror the contract's
  `Uint128`-as-decimal-string representation identically regardless of
  magnitude. ([audit F-SEC-001, A-ADV-003])
- **Timestamps deserialize as strings on the Go side.** `created_at` /
  `retired_at` / `committed_at` / `resolved_at` are decimal strings of
  nanoseconds (the way `cosmwasm_std::Timestamp` serializes). Go
  `uint64` → `string`. ([audit follow-up])
- **`tp_count` / `fp_count` aligned to `uint64`.** Was `uint32` on the
  Go side; Rust uses `u64`. ([audit follow-up])
- **Rotation removes the old label binding.** `rotate_agent` now calls
  `AGENTS_BY_LABEL.remove(old_label)` so label lookups no longer
  resolve to a retired record. The retired `AgentRecord` keyed by
  pubkey is preserved with `retired_at` + `superseded_by` — the
  accountability trail survives. The new label is allowed to equal the
  old one. ([audit A-ADV-001])
- **`BatchMixedPlanID` error variant.** Replaces the misleading
  `FindingAlreadyCommitted` that batch-mismatch checks previously
  returned. Carries `batch_plan_id`, `found_plan_id`, `finding_id` for
  debuggability. ([audit F-SEC-004])
- **Keyring warning.** `Client.New()` logs a one-line warning to stderr
  when `keyring_backend=test` is combined with a non-test chain id
  (chain id not containing "devnet", "testnet", "test", or "local").
  ([audit F-SEC-003])

### Added

- **Identifier validation** (`contracts/tribunal-reputation/src/validate.rs`,
  `internal/chain/validate.go`). Rejects `plan_id` / `finding_id` /
  `claim_hash` / `evidence_hash` / `label` / `model_id` / `reason` that
  contain `|`, NUL, or any ASCII control character; enforces length
  caps (64 chars for IDs / labels, 128 for hashes / model IDs, 256 for
  rotation reason). Enforced symmetrically by the contract and the Go
  builders. ([audit F-SEC-002, A-ADV-002])
- **`MAX_BATCH_SIZE = 100`** on `commit_finding_batch` /
  `resolve_finding_batch`. Returns `BatchTooLarge` if exceeded so a
  malformed client can't burn gas linearly. ([audit F-PERF-002])
- **Go wire-roundtrip tests** at `internal/chain/wire_test.go`. Hand-crafted
  fixtures cover every response shape against the v0.3.0 wire format —
  including the resolved-finding path that broke in v0.3.0. ([audit
  F-ARCH-002, F-ARCH-004, methodology lesson])

### Deferred (still open from the audit)

- **`Config.admin` is stored but never checked.** Latent permission
  surface. ([audit A-ADV-004]) — kept until there's a use.
- **Leaderboard remains O(n_agents).** ([audit F-PERF-001])
- **HTTP timeouts remain flat 30s.** ([audit F-PERF-004])
- **No retry / partial-progress recovery for batch settlement.**
  ([audit F-SEC-006])

## [0.3.0] — 2026-05-12

### Added

- **CosmWasm reputation contract** (`contracts/tribunal-reputation/`): single-tenant, soulbound reputation token on Burnt XION. Each agent is identified by a 32-byte ed25519 pubkey + unique label + role. Findings and resolutions are signed off-chain (`ed25519_verify` in the contract) and persisted as `FindingState` keyed by `(plan_id, finding_id)`. Stake debited on commit; on resolve, `true_positive` returns stake + `stake × outcome_reward_multiplier`, `false_positive` keeps the stake slashed, `stale_duplicate` / `indeterminate` return the stake with no reward. Rotation preserves TP/FP history but resets balance to the configured `rotation_floor`. Built against `cosmwasm-std` 2.x, `cw-storage-plus` 2.x, `cw-multi-test` 2.x. 15 integration tests cover every execute path + every error path.
- **Go chain client** (`internal/chain/`): mirrors the contract surface. Hybrid settlement protocol — `Sync.CommitRealtime` for critical findings (with a JSONL retry queue at `.tribunal/chain-queue.jsonl` on failure), `Sync.SyncPlan` / `Sync.SyncAll` for plan-close batched commits + resolutions. Transports split: txs shelled out to `xiond tx wasm execute` (so users keep one keyring for all XION ops), queries direct HTTP to the LCD REST endpoint. `xiond_binary` config accepts prefix args (e.g. `docker exec devnet-xion-1 xiond`) for contributors running the devnet in containers. Canonical signing messages (`TRIBUNAL_FINDING|...`, `TRIBUNAL_RESOLUTION|...`) are byte-identical to the Rust contract helpers.
- **`tribunal chain` CLI tree**: `init` (writes `~/.tribunal/chain.yaml`), `status` (RPC health + height), `register <label>`, `sync [--plan <id>]`, `query {reputation,agent,finding,leaderboard,config}`, `rotate <old> <new>`, `queue {list,clear}`. Reputation / agent lookups accept either an `ed25519:<hex>` pubkey or a local label.
- **Deploy scripts**: `scripts/deploy-contract.sh` (cargo wasm → optional `cosmwasm/optimizer` pass → `xiond tx wasm store` + instantiate → emits a `chain.yaml` snippet to stdout). `scripts/init-testnet.sh` probes a locally-running XION devnet and emits the env-var exports the deploy script needs.
- **Contracts CI workflow** (`.github/workflows/contracts.yml`): `cargo fmt --check`, `cargo clippy -D warnings`, `cargo test --release`, `cargo wasm`, `cosmwasm-check` on the built artifact.
- **On-chain protocol doc** at `docs/on-chain-protocol.md` covering contract surface, wire format, signing canonicalization, gas considerations, and the hybrid settlement flow.

### Changed

- Repo gitignore ignores `contracts/*/target/` and `contracts/*/artifacts/` but commits `Cargo.lock` (binary-crate convention).
- Root CLI help lists the v0.3 chain workflow.

### Not yet implemented (v0.4+)

- Multi-org tenancy (single global agent registry for now).
- Cross-chain reputation (XION only).
- Slashing appeals (resolutions are final; PMs can re-file but old ones stay in the audit trail).
- Fungible operator rewards.
- Web leaderboard dashboard.

## [0.2.0] — 2026-05-12

### Added

- **Verification pyramid** (`internal/verify`): `tribunal verify` runs a halt-on-failure pyramid of language-appropriate correctness tools. Go stack ships ordered layers `go-build → go-fmt → go-vet → (staticcheck) → (golangci-lint) → go-test → (go-fuzz)` with per-layer `LayerResult` + an aggregated `PyramidReport`. Rust + TypeScript stack stubs included; replace with real wiring as the corresponding examples land.
- **Adversary dispatch** (`internal/dispatch`): pluggable `Provider` interface, parallel orchestrator (`Dispatch`), thread-safe `Registry`, and structured `Synthesis` over N panel members (shared / unique / divergent findings + overall verdict). Includes report parser tolerant of formatting variation.
- **Diversity buckets** (`internal/dispatch/bucket.go`): configurable diversity axis via tribunal.yaml — `vendor_family`, `temperature_band`, `focus`, `model_tier`, or `composite:axis1,axis2,...`. Methodology now treats diversity as a spectrum rather than vendor-only; reputation ledger can empirically measure which axis pays off.
- **Default adversary panels** (`internal/dispatch/panel.go`): documented `default_panel` (three Claude variants, cost-efficient) and `high_stakes_panel` (cross-vendor opt-in) that load from tribunal.yaml's `adversary:` section.
- **Claude provider** (`internal/dispatch/claude.go`): direct HTTP to Anthropic's `/v1/messages`. Reads `ANTHROPIC_API_KEY`. Tests use `httptest` stubs.
- **Hybrid review orchestration** (`internal/review/hybrid.go`): given a `--plan` ID, locates `.tribunal/plans/<id>/intent.md` + `plan.md`, reads trio reviewer reports from `.tribunal/reports/<id>/`, computes the diff via git, dispatches the configured panel, persists per-member adversary reports + synthesis JSON, and appends signed findings to `.tribunal/ledger.jsonl` (auto-registering missing adversary keypairs on first use).
- **CLI additions**: `tribunal verify` (pyramid runner), `tribunal dispatch test` (panel inspection), `tribunal dispatch attack` (raw adversary fan-out against stdin), `tribunal review` (real adversary-stage orchestration). Exit codes encode verdicts: `0` SURVIVES, `3` BREAKS, `4` INDETERMINATE.
- **`tribunal.yaml.example`** at the repo root documenting every configurable knob.
- **CI**: pyramid smoke-test runs against the fizzbuzz demo; dispatch panel inspection runs against the default + high-stakes panels on every push.

### Changed

- Methodology + skills + agents updated to describe **diversity-as-spectrum** rather than vendor-family-only.
- `dispatch` mock provider uses `atomic.Int64` for call counts; race detector clean.

### Not yet implemented (v0.3+)

- OpenAI / Google / LM Studio providers (the high-stakes panel currently degrades to per-member INDETERMINATE for missing providers without crashing).
- Burnt XION CosmWasm reputation contract + chain settlement.

## [0.1.0] — 2026-05-12

### Added

- Methodology document at `docs/methodology.md` — process backbone + hybrid review + verification pyramid + incentive layer.
- Companion docs: `incentive-mechanism.md` (reputation math), `installation.md` (per-host setup).
- Go libraries under `internal/`:
  - `agent`: ed25519 keypair generation, on-disk registry (~/.tribunal/agents/), role enum, rotation.
  - `ledger`: signed Finding / Resolution types, canonical-JSON signing, append-only JSONL store, reputation calculation with exponential decay + family-diversity bonus, gate decisions.
- `tribunal` CLI: `init`, `agents {add,list,show,rotate}`, `ledger {summary,leaderboard,find,verify}`, `review` (stub for v0.2).
- 7 installable skills in `skills/`: `tribunal-{intent,plan,implement,review,verify,classify,incentive}` with the canonical methodology workflow.
- 9 installable agents in `agents/`: `tribunal-{pm,architect,implementer,reviewer-arch,reviewer-sec,reviewer-perf,adversary,classifier,qa}`.
- End-to-end demo at `examples/go-fizzbuzz-verified/` — intent doc, implementation, table-driven + invariant + fuzz tests, and a populated `.tribunal/` directory with 4 signed findings + 4 signed resolutions across 6 agents.
- `cmd/seed-fizzbuzz-demo/` — deterministic seed generator for the demo; CI verifies the seed is byte-stable.
- GitHub Actions CI: gofmt, vet, build, test (race detector) on Linux + macOS; CLI smoke test; example module test; demo-determinism check.

### Not yet implemented (v0.2 / v0.3)

- Multi-model adversary dispatch via `external-model-mcp` / `lm-studio-mcp` (v0.2).
- Full verification pyramid orchestration (v0.2).
- Burnt XION CosmWasm reputation contract + chain settlement (v0.3).
