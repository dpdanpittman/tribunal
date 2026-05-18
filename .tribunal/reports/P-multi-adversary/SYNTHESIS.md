# P-multi-adversary — Synthesis

> The empirical test of cross-family adversary diversity claimed by Colosseum's pillar #2: _"different model families have different blind spots; combining them gives additive coverage."_ Run on the v0.3.4 diff (`fb37c3c`) with four adversaries given identical context.

## Panel + outputs

| Slot | Model              | Family    | Verdict                          | Findings                | Wall time |
|------|--------------------|-----------|----------------------------------|-------------------------|-----------|
| A1   | claude-opus-4-7    | Anthropic | BREAKS                           | 1 Critical + 4 Serious + 1 Suggestion | 553s |
| A2   | claude-sonnet-4-6  | Anthropic | INDETERMINATE                    | 2 Serious + 1 Cosmetic  | 291s      |
| A3   | claude-haiku-4-5   | Anthropic | SURVIVES (with caveats)          | 2 Serious               | 152s      |
| A4   | qwen3-coder:latest | Alibaba   | "PIVOTED BUT NOT CONVERGED" *    | ~5 substantive + 40 restatements | 627s |

\* qwen-coder used a non-standard verdict label phrased almost verbatim from the P-v034-audit's _own_ self-assessment, suggesting input-anchoring rather than independent verdict derivation.

### Methodology note

The original intent specified **qwen3:32b dense** (general, not code-tuned). Substituted with **qwen3-coder:latest** (30B-A3B MoE) because qwen3:32b dense + the 36k-token prompt didn't fit in the RTX 4090's 24GB VRAM (CPU-split to 36/65 layers, observed on both ollama 0.15.2 and 0.23.2 — model + KV cache at this context length is ~40GB regardless of ollama version). qwen3-coder is still cross-family (Alibaba) from the Claudes; the H1 cross-family-additive test remains valid in principle. **Contamination caveat**: qwen-coder is code-fine-tuned, which may bias toward verbose enumeration of restated grievances rather than the tight scenario-grounded findings the system prompt asks for. Of qwen-coder's 45 numbered findings, ~5 are substantive; the rest are restatements (e.g., F-QWEN-#4 and F-QWEN-#1 both restate F-SEC-401's silent-success indistinguishability; F-QWEN-#7 restates a perf-reviewer observation; F-QWEN-#19/#30/#34+ devolve into generic "add explicit timeout handling" / "add high-load testing" / "ensure consistent behavior across scenarios").

## Shared findings (all 4 adversaries)

| Finding                                              | Severity | Notes                                                                                            |
|------------------------------------------------------|----------|--------------------------------------------------------------------------------------------------|
| F-SEC-401 — hostile-LCD batch suppression            | Critical | All 4 confirmed real. Highest-confidence signal of the panel — supports **H3 (consensus convergence)**. |
| Silent-success indistinguishable from F-SEC-401 attack | Serious  | Opus (F-OPUS-006), Sonnet (F-SONNET-002), Qwen-coder (F-QWEN-#4) all flagged. Haiku did not.   |

## Family-shared (Anthropic-only — H1 inverse signal)

Two findings raised by ≥2 of the 3 Claude adversaries that **qwen-coder did not raise**:

| Finding | Filed by | Notes |
|---|---|---|
| Per-plan ctx vs outer 5m ctx budget mismatch (plan-N starvation) | Opus (F-OPUS-002), Haiku (F-HAIKU-001 related) | Concrete scenario: 4+ plans truncated to 30s/0s. |
| Ctx-expiry-during-recovery-preflight → unhelpful error swallow | Haiku (F-HAIKU-001), Sonnet (related, less specific) | Concrete code-path attack. |

## Cross-family unique (qwen-coder only — H1 positive signal)

**None.** qwen-coder did not raise any substantive finding that wasn't already filed by at least one Claude adversary. Its first 3-5 findings either restate F-SEC-401 (different framings) or echo the lens reviewers verbatim. Restatement findings #8-45 do not contain ground-truth attack scenarios.

## Unique-per-model (single-Claude findings — H2 intra-family signal)

| Finding | Filed by | What it surfaces |
|---|---|---|
| F-OPUS-003 — no chunking vs contract `MAX_BATCH_SIZE = 100`     | Opus only   | Pre-existing latent issue; v0.3.4 recovery loop hits it on >100-finding plans. Sonnet + Haiku missed. |
| F-OPUS-004 — Unicode bypass of `looksLikeTestChain` (`MAİNNET-test-fork` slips through) | Opus only   | Adversarial input class; Sonnet + Haiku missed. **The most novel finding in the entire panel.** |
| F-OPUS-005 — recovery exhaustion error lacks diagnostic info     | Opus only   | Observability gap; not safety-affecting. |
| F-SONNET-001 — `FindingNotCommitted` has no signal in resolved map | Sonnet only | Edge case the recovery loop doesn't model. Opus + Haiku missed. |
| F-HAIKU-002 — recovery loop empty-batch path returns success    | Haiku only  | Conservative finding; surfaces an idempotence corner case. |

## Verdict spread (H2)

opus=**BREAKS** > sonnet=**INDETERMINATE** > haiku=**SURVIVES**. Same input, three different conclusions. Larger Claude variant is more aggressive — confirming the intra-family diversity hypothesis.

## Hypothesis verdicts

| H | Claim | Verdict |
|---|---|---|
| **H1 — cross-family additive** | qwen finds ≥1 Critical/Serious none of the Claudes raise | **REFUTED** (with methodology caveat — see below) |
| **H2 — intra-family disagreement** | Claudes disagree non-trivially | **CONFIRMED** (3 different verdicts, distinct finding sets) |
| **H3 — consensus convergence** | Some findings raised by all 4 | **CONFIRMED** (F-SEC-401 raised by all 4) |
| **H4 — no cross-family signal** | qwen finds nothing the Claudes miss | **CONFIRMED** for qwen3-coder; unknown for qwen3:32b dense |

## H1 — what this means for v0.4

The cross-family hypothesis is **not validated by this experiment**. Three independent caveats apply:

1. **Model substitution caveat.** qwen3-coder is code-fine-tuned and likely biased toward verbose code-explanation-style output; the verbatim-restatement pathology may not occur on qwen3:32b dense. The cleanest re-run would prompt-trim (drop reviewer reports, keep diff + intent) so qwen3:32b dense fits in 24GB GPU.
2. **Output collapse caveat.** qwen-coder hit the 8192-token output cap mid-finding-45. Among its first 5-6 findings (where quality was acceptable), none were novel relative to the Claude panel. Increasing the output budget would surface more restatements, not more findings — model-collapse signature is unambiguous in findings 8+.
3. **Single-experiment caveat.** One audit cycle isn't enough to refute the hypothesis. If the next P-v04N-audit also produces no qwen-unique findings, the hypothesis is dead. If a different cross-family adversary (Gemini, GPT-5, local llama) surfaces a novel finding next time, H1 stays live.

**The most novel finding of the panel — F-OPUS-004 (Unicode bypass of `looksLikeTestChain`) — came from intra-Claude diversity (opus vs sonnet vs haiku), not cross-family.** That is the empirical signal v0.4.0 should be wired against.

## Recommended v0.4.0 direction

Given H1 is REFUTED (provisionally) and H2 is CONFIRMED strongly:

- **Prioritize**: Wire the multi-model panel into `tribunal review`'s `high_stakes_panel` config, but make the cross-family slot a TIER-2 optimization, not the load-bearing primitive. The cheap win is dispatching opus + sonnet + haiku in parallel and surfacing their disagreements — that already produced F-OPUS-004 (Unicode bypass) and three distinct verdicts on the same input.
- **Deprioritize (vs the previously planned v0.4.0)**: Wiring qwen / GPT-5 / Gemini as the *primary* cross-family adversary. The data says intra-Claude diversity beats cross-family diversity on this input.
- **Move up the queue from v0.4.1**: Property-based testing. If LLMs (even diverse ones) won't surface invariant-shaped defects, PBT remains the most likely primitive to find what _no_ adversary articulates.

## Spike against the most novel single-Claude finding

**F-OPUS-004 (Unicode `looksLikeTestChain` bypass)** is concrete enough to file as a real Tribunal finding against the v0.3.X codebase. The chain-id allowlist needs to add an explicit NFKC normalize + ASCII-only check before the `mainnet` substring match. Outside Phase 2's scope; flag it for a v0.3.5 audit-fix cycle.

## Open items

- Re-run with **qwen3:32b dense** + trimmed prompt to remove the contamination caveat. Trim: drop the 3 reviewer reports (keep intent + plan + diff). Should fit in ~12k tokens, model loads cleanly in 24GB VRAM.
- Re-run with **a different cross-family adversary** (GPT-5, Gemini-2.5, or local llama4) if/when those credentials exist.
- File F-OPUS-004 against `internal/seed/heuristics.go:looksLikeTestChain` for v0.3.5 follow-up.
