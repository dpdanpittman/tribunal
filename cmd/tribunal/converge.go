package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dpdanpittman/tribunal/internal/agent"
	"github.com/dpdanpittman/tribunal/internal/converge"
	"github.com/dpdanpittman/tribunal/internal/dispatch"
	"github.com/dpdanpittman/tribunal/internal/review"
)

func newConvergeCmd() *cobra.Command {
	var (
		planID           string
		diffSpec         string
		maxRounds        int
		maxTokens        int
		maxWallclock     time.Duration
		severityFloor    string
		rotationSpec     string
		stopOnSpec       string
		adversaryMD      string
		bucket           string
		noLedger         bool
		noAutoRegister   bool
		implementerModel string
		autoApply        bool
		autoContinue     bool
		noImplementerRep bool
	)
	cmd := &cobra.Command{
		Use:   "converge",
		Short: "Drive the single-pass review loop until convergence under rotated adversarial pressure (v0.4.1 M1: output-only)",
		Long: `tribunal converge wraps tribunal review in a release-gating loop. Each
round rotates the adversary panel composition, dispatches the lens-parallel
trio + adversary, classifies findings as novel vs carry-forward against
the on-disk round ledger, and evaluates stopping criteria.

v0.4.1 ships milestone M1 (output-only). The controller does NOT author
fixes — when a round produces unresolved Critical/Warning findings, the
loop exits with status "needs_fixes" so the operator can apply the
implementer role manually and re-invoke. On re-invocation, the prior
rounds are loaded from .tribunal/convergence/<plan-id>/ so panel rotation
stays informed by the full history.

Stopping criteria (ANDed):
  consecutive-clean(N)  N back-to-back rounds with zero critical+warning
  no-novel-findings     every finding in this round was already filed
  max-rounds(N)         hard escape valve (also derived from --max-rounds)

Rotation strategies:
  focus-shuffle         permute member focus axis per round (local-only)
  composite:focus,...   default v0.4.1; meaningful diversity per round

See docs/convergence.md and docs/adr/0001-convergence-controller.md.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if planID == "" {
				return fmt.Errorf("--plan is required")
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			rotator, err := converge.SelectRotator(rotationSpec)
			if err != nil {
				return err
			}
			stopping, err := converge.ParseStoppingCriteria(stopOnSpec)
			if err != nil {
				return err
			}
			cfg, err := dispatch.LoadConfig(cwd)
			if err != nil {
				return err
			}

			adversaryBody, err := loadAdversaryBody(adversaryMD)
			if err != nil {
				return err
			}
			providerReg := buildRegistry()
			agentReg, err := defaultRegistry()
			if err != nil {
				return err
			}

			stage := &cliAdversaryStage{
				ProjectRoot:   cwd,
				PanelBucket:   bucket,
				AdversaryBody: adversaryBody,
				ProviderReg:   providerReg,
				AgentReg:      agentReg,
				DiffSpec:      diffSpec,
				WriteToLedger: !noLedger,
				AutoRegister:  !noAutoRegister,
				SeverityFloor: severityFloor,
			}

			ctrl := &converge.Controller{
				Adversary: stage,
				Rotator:   rotator,
				Stopping:  stopping,
				Budget: converge.Budget{
					MaxRounds:    maxRounds,
					MaxTokens:    maxTokens,
					MaxWallclock: maxWallclock,
				},
				DispatchConfig:    cfg,
				AutoApply:         autoApply,
				AutoContinue:      autoContinue,
				IntentLoader:      intentLoaderForCWD(cwd),
				DiffLoader:        diffLoaderForCWD(cwd, diffSpec),
				FindingBodyLookup: findingBodyLookupForCWD(cwd),
			}
			if autoContinue {
				if !autoApply {
					return fmt.Errorf("--auto-continue requires --auto-apply (nothing to verify against)")
				}
				ctrl.VerifyGate = &converge.PyramidVerifyGate{}
			}
			if implementerModel != "" {
				claude, err := dispatch.NewClaudeProvider()
				if err != nil {
					return fmt.Errorf("--implementer requires ANTHROPIC_API_KEY: %w", err)
				}
				ctrl.Implementer = &converge.ClaudeImplementer{
					Provider:    claude,
					Model:       implementerModel,
					Temperature: 0,
					MaxTokens:   8192,
					LabelStr:    "implementer-" + implementerModel,
				}
				// v0.4.5 reputation feedback: implementer outcomes flow
				// into the ledger so the leaderboard learns which
				// implementers ship working patches. Opt out via
				// --no-implementer-reputation.
				if !noImplementerRep {
					ctrl.Reputation = &ledgerReputationSink{
						ProjectRoot: cwd,
						Registry:    agentReg,
					}
				}
			} else if autoApply {
				return fmt.Errorf("--auto-apply requires --implementer (nothing to apply)")
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if maxWallclock > 0 {
				ctx, cancel = context.WithTimeout(ctx, maxWallclock+5*time.Minute)
				defer cancel()
			}

			res, err := ctrl.Run(ctx, converge.ConvergenceTarget{
				PlanID:      planID,
				DiffSpec:    diffSpec,
				ProjectRoot: cwd,
			})
			if err != nil {
				if res != nil {
					printConvergeResult(res)
				}
				return err
			}
			printConvergeResult(res)
			switch res.Status {
			case converge.StatusConverged:
				return nil
			case converge.StatusNeedsFixes:
				os.Exit(5)
			case converge.StatusBudgetExhausted:
				os.Exit(6)
			case converge.StatusErrored:
				os.Exit(2)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&planID, "plan", "", "Plan ID under .tribunal/plans/ (e.g. P-42)")
	cmd.Flags().StringVar(&diffSpec, "diff", "HEAD~1..HEAD", "Diff spec: git range, file path, or 'staged'")
	cmd.Flags().IntVar(&maxRounds, "max-rounds", 5, "Escape valve — abort after N total rounds across all invocations")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 200_000, "Token budget cap across all rounds (0 = unbounded)")
	cmd.Flags().DurationVar(&maxWallclock, "max-wallclock", 30*time.Minute, "Wallclock cap on a single converge invocation (0 = ctx-only)")
	cmd.Flags().StringVar(&severityFloor, "severity-floor", "warning", "Stop fighting over Suggestion-only findings (critical|warning|suggestion)")
	cmd.Flags().StringVar(&rotationSpec, "rotation", "composite:focus,model_tier", "Panel rotation strategy (focus-shuffle | composite:axis1,axis2,...)")
	cmd.Flags().StringVar(&stopOnSpec, "stop-on", "consecutive-clean(2)", "Comma-separated stopping criteria (ANDed)")
	cmd.Flags().StringVar(&adversaryMD, "adversary-md", "", "Path to tribunal-adversary.md (defaults to installed agents/ dir)")
	cmd.Flags().StringVar(&bucket, "bucket", "composite:model_tier,focus", "Diversity bucket axis passed through to the adversary stage")
	cmd.Flags().BoolVar(&noLedger, "no-ledger", false, "Do not sign + append per-round findings to the ledger")
	cmd.Flags().BoolVar(&noAutoRegister, "no-auto-register", false, "Refuse to auto-create adversary agent keypairs")
	cmd.Flags().StringVar(&implementerModel, "implementer", "", "Claude model id to author patches between rounds (e.g. claude-opus-4-7). Empty disables the implementer (M1 output-only).")
	cmd.Flags().BoolVar(&autoApply, "auto-apply", false, "Apply the implementer's patch via `git apply` after authoring. Requires --implementer; refuses on a dirty working tree.")
	cmd.Flags().BoolVar(&autoContinue, "auto-continue", false, "M3 auto-continue: after --auto-apply, run the verification pyramid; on pass continue the loop, on fail revert + exit. Requires --auto-apply (and therefore --implementer).")
	cmd.Flags().BoolVar(&noImplementerRep, "no-implementer-reputation", false, "Disable the v0.4.5 implementer-reputation feedback (synthetic Finding + auto-Resolution written to .tribunal/ledger.jsonl per patch). On by default when --implementer is set.")
	return cmd
}

// intentLoaderForCWD returns a closure that reads .tribunal/plans/<id>/intent.md
// for the implementer prompt. Missing file → empty string (no error).
func intentLoaderForCWD(cwd string) func(planID string) string {
	return func(planID string) string {
		path := filepath.Join(cwd, ".tribunal", "plans", planID, "intent.md")
		body, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		return string(body)
	}
}

// diffLoaderForCWD resolves the same DiffSpec the adversary stage uses
// so the implementer sees the same review surface. Errors collapse to
// empty string — the implementer prompt notes when no diff is captured.
func diffLoaderForCWD(cwd, spec string) func(t converge.ConvergenceTarget) string {
	return func(_ converge.ConvergenceTarget) string {
		if spec == "" {
			return ""
		}
		// review.resolveDiff is unexported; reuse the FindInputs path
		// which already handles the same diff-spec semantics.
		in, err := review.FindInputs(cwd, "", spec)
		if err != nil || in == nil {
			return ""
		}
		return in.Diff
	}
}

// findingBodyLookupForCWD walks .tribunal/findings/ on disk and indexes
// per-finding markdown files by claim_hash. The adversary stage writes
// these alongside the ledger entries.
func findingBodyLookupForCWD(cwd string) func(findings []converge.RoundFinding) map[string]string {
	return func(findings []converge.RoundFinding) map[string]string {
		out := map[string]string{}
		root := filepath.Join(cwd, ".tribunal", "findings")
		for _, f := range findings {
			// Findings filed by the adversary stage are named
			// F-<plan>-<agent>-<idx>.md but indexed by ClaimHash inside.
			// Cheap fallback: read every file and match on the claim_hash
			// embedded as `- Claim hash: <h>`. For a large findings dir
			// this is O(N×M); the dir is plan-scoped so N stays small.
			matches, _ := filepath.Glob(filepath.Join(root, "*.md"))
			for _, m := range matches {
				body, err := os.ReadFile(m)
				if err != nil {
					continue
				}
				if strings.Contains(string(body), f.ClaimHash) {
					out[f.ClaimHash] = string(body)
					break
				}
			}
		}
		return out
	}
}

// cliAdversaryStage adapts review.Run to the converge.AdversaryStage
// interface. The rotator-selected panel for each round is plumbed into
// review.Run via Options.PanelOverride, so the on-disk tribunal.yaml is
// never mutated during a converge invocation.
type cliAdversaryStage struct {
	ProjectRoot   string
	PanelBucket   string
	AdversaryBody string
	ProviderReg   *dispatch.Registry
	AgentReg      *agent.Registry
	DiffSpec      string
	WriteToLedger bool
	AutoRegister  bool
	SeverityFloor string
}

func (s *cliAdversaryStage) RunRound(ctx context.Context, in converge.RoundInput) (*converge.RoundOutput, error) {
	inputs, err := review.FindInputs(s.ProjectRoot, in.Target.PlanID, s.DiffSpec)
	if err != nil {
		return nil, err
	}
	run, err := review.Run(ctx, review.Options{
		ProjectRoot:   s.ProjectRoot,
		PlanID:        in.Target.PlanID,
		PanelName:     "default",
		PanelOverride: in.Panel.Members,
		BucketSpec:    s.PanelBucket,
		AdversaryBody: s.AdversaryBody,
		WriteToLedger: s.WriteToLedger,
		AutoRegister:  s.AutoRegister,
	}, inputs, s.ProviderReg, s.AgentReg)
	if err != nil {
		return nil, err
	}
	return adaptAdversaryRunToRoundOutput(run, s.SeverityFloor), nil
}

// adaptAdversaryRunToRoundOutput projects review.AdversaryRun into the
// shape the converge.Controller expects. severityFloor filters out
// findings below the configured floor so they don't keep the loop
// running for Suggestion-only churn.
func adaptAdversaryRunToRoundOutput(run *review.AdversaryRun, severityFloor string) *converge.RoundOutput {
	out := &converge.RoundOutput{
		OverallVerdict: run.OverallVerdict,
		Verdicts:       map[string]string{},
	}
	if run.Synthesis != nil {
		for label, v := range run.Synthesis.Verdicts {
			out.Verdicts[label] = v
		}
	}
	floorRank := severityRank(severityFloor)
	for _, f := range run.LedgerFindings {
		sev := strings.ToLower(string(f.Severity))
		if severityRank(sev) < floorRank {
			continue
		}
		out.Findings = append(out.Findings, converge.RoundFinding{
			ClaimHash: f.ClaimHash,
			Category:  string(f.Category),
			Severity:  sev,
			Member:    f.AgentLabel,
			Scenario:  "", // not surfaced via signed-finding (lives in the per-member raw report)
		})
	}
	return out
}

// severityRank maps the controller's severity strings to a comparable
// integer; higher = more severe. Mirrors dispatch's internal helper but
// keeps the surface local so we don't expand dispatch's public API.
func severityRank(s string) int {
	switch strings.ToLower(s) {
	case "critical":
		return 3
	case "warning", "serious":
		return 2
	case "suggestion", "cosmetic":
		return 1
	}
	return 0
}

func printConvergeResult(r *converge.ConvergenceResult) {
	if r == nil {
		return
	}
	fmt.Printf("Plan:      %s\n", r.PlanID)
	fmt.Printf("Status:    %s\n", r.Status)
	if r.Reason != "" {
		fmt.Printf("Reason:    %s\n", r.Reason)
	}
	fmt.Printf("Rounds:    %d (this invocation)\n", len(r.Rounds))
	fmt.Printf("Tokens:    %d (cumulative)\n", r.TotalTokenCost)
	fmt.Printf("Duration:  %s\n", r.TotalDuration.Truncate(time.Millisecond))
	for _, rr := range r.Rounds {
		fmt.Printf("\n  Round %d  verdict=%s  duration=%s  panel=%s\n",
			rr.Round, rr.OverallVerdict, rr.Duration.Truncate(time.Millisecond), summarizePanel(rr.Panel))
		if len(rr.Findings) > 0 {
			carry := 0
			for _, f := range rr.Findings {
				if f.CarryForward {
					carry++
				}
			}
			fmt.Printf("    findings: %d (%d novel, %d carry-forward)\n", len(rr.Findings), len(rr.Findings)-carry, carry)
			for _, f := range rr.Findings {
				marker := "+"
				if f.CarryForward {
					marker = "·"
				}
				fmt.Printf("      %s [%s] %s  member=%s  hash=%s\n", marker, f.Severity, f.Category, f.Member, shortHash(f.ClaimHash))
			}
		}
		if rr.Stopped {
			fmt.Printf("    STOPPED: %s — %s\n", rr.StopCriterion, rr.StopReason)
		}
	}
}

func summarizePanel(p converge.PanelComposition) string {
	if len(p.Members) == 0 {
		return "(empty)"
	}
	parts := make([]string, len(p.Members))
	for i, m := range p.Members {
		parts[i] = m.Label + ":" + m.Focus
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func shortHash(h string) string {
	if len(h) > 16 {
		return h[:16] + "…"
	}
	return h
}
