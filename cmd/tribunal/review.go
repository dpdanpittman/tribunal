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
	"github.com/dpdanpittman/tribunal/internal/dispatch"
	"github.com/dpdanpittman/tribunal/internal/review"
)

func newReviewCmd() *cobra.Command {
	var (
		planID           string
		panelName        string
		bucket           string
		diffSpec         string
		adversaryMD      string
		noLedger         bool
		noAutoRegister   bool
		viaClawpatch     bool
		clawpatchModel   string
		clawpatchSkipMap bool
	)
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Run the adversary stage of a hybrid review (lens-parallel trio dispatch happens in your host harness)",
		Long: `Tribunal's hybrid review is two-stage. Stage 1 (the lens-parallel
trio: @tribunal-reviewer-arch, @tribunal-reviewer-sec, @tribunal-reviewer-perf)
is dispatched by your host harness (Claude Code Task tool, OpenCode, Cursor)
because each reviewer is a subagent. Their reports land in
.tribunal/reports/<plan-id>/.

Stage 2 — the adversary panel — is what this command runs. It reads
intent.md, plan.md, the trio's reports, and a diff, then dispatches the
configured adversary panel concurrently. Each member's report is persisted
to .tribunal/reports/<plan-id>/, and parsed findings are appended to
.tribunal/ledger.jsonl signed by the adversary agent's keypair (auto-
registered on first use unless --no-auto-register).`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if planID == "" {
				return fmt.Errorf("--plan is required")
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			body, err := loadAdversaryBody(adversaryMD)
			if err != nil {
				return err
			}
			reg := buildRegistry()
			agentReg, err := defaultRegistry()
			if err != nil {
				return err
			}
			// 20m timeout covers the adversary panel. Clawpatch reviews can
			// take longer on bigger repos; if --via-clawpatch is set, give
			// the whole command a more generous budget.
			cmdTimeout := 20 * time.Minute
			if viaClawpatch {
				cmdTimeout = 45 * time.Minute
			}
			ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
			defer cancel()
			// If --via-clawpatch was passed, drive the lens stage through
			// clawpatch FIRST so the per-lens reports land on disk before
			// review.FindInputs reads them.
			if viaClawpatch {
				lensRun, err := review.RunClawpatchLens(ctx, review.ClawpatchLensOptions{
					ProjectRoot:   cwd,
					PlanID:        planID,
					Round:         1,
					WriteToLedger: !noLedger,
					AutoRegister:  !noAutoRegister,
					Provider:      "acpx",
					Model:         clawpatchModel,
					SkipMap:       clawpatchSkipMap,
				}, agentReg)
				if err != nil {
					return fmt.Errorf("clawpatch lens stage: %w", err)
				}
				printClawpatchLensRun(lensRun)
			}
			in, err := review.FindInputs(cwd, planID, diffSpec)
			if err != nil {
				return err
			}
			result, err := review.Run(ctx, review.Options{
				ProjectRoot:   cwd,
				PlanID:        planID,
				PanelName:     panelName,
				BucketSpec:    bucket,
				AdversaryBody: body,
				WriteToLedger: !noLedger,
				AutoRegister:  !noAutoRegister,
			}, in, reg, agentReg)
			if err != nil {
				return err
			}
			printAdversaryRun(result)
			switch result.OverallVerdict {
			case dispatch.VerdictBreaks:
				os.Exit(3)
			case dispatch.VerdictIndeterminate:
				os.Exit(4)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&planID, "plan", "", "Plan ID under .tribunal/plans/ (e.g. P-42)")
	cmd.Flags().StringVar(&panelName, "panel", "default", "Adversary panel name (default | high-stakes)")
	cmd.Flags().StringVar(&bucket, "bucket", "composite:vendor_family,focus", "Diversity bucket axis")
	cmd.Flags().StringVar(&diffSpec, "diff", "HEAD~1..HEAD", "Diff spec: git range, file path, or 'staged'")
	cmd.Flags().StringVar(&adversaryMD, "adversary-md", "", "Path to tribunal-adversary.md (defaults to installed agents/ dir or this repo's agents/)")
	cmd.Flags().BoolVar(&noLedger, "no-ledger", false, "Do not sign + append findings to the ledger")
	cmd.Flags().BoolVar(&noAutoRegister, "no-auto-register", false, "Refuse to auto-create adversary agent keypairs")
	cmd.Flags().BoolVar(&viaClawpatch, "via-clawpatch", false, "Run the lens stage via clawpatch subprocess instead of expecting skill-trio reports on disk")
	cmd.Flags().StringVar(&clawpatchModel, "clawpatch-model", "", "Model passed to clawpatch (--model). Default: clawpatch's provider default.")
	cmd.Flags().BoolVar(&clawpatchSkipMap, "clawpatch-skip-map", false, "Skip clawpatch map step (use the existing feature graph). Debugging escape hatch.")
	return cmd
}

func printClawpatchLensRun(r *review.ClawpatchLensRun) {
	fmt.Println("Clawpatch lens stage")
	fmt.Printf("  Plan:           %s\n", r.PlanID)
	if r.MapResult != nil {
		fmt.Printf("  Map:            %d features (source=%s, new=%d, changed=%d, stale=%d)\n",
			r.MapResult.Features, r.MapResult.Source, r.MapResult.New, r.MapResult.Changed, r.MapResult.Stale)
	}
	if r.ReviewResult != nil {
		fmt.Printf("  Review:         run=%s reviewed=%d findings=%d jobs=%d\n",
			r.ReviewResult.Run, r.ReviewResult.Reviewed, r.ReviewResult.Findings, r.ReviewResult.Jobs)
	}
	for _, lens := range []string{"arch", "sec", "perf"} {
		count := 0
		for k, v := range r.FindingsByLens {
			if string(k) == lens {
				count = len(v)
				break
			}
		}
		fmt.Printf("  %-4s findings:  %d\n", lens, count)
	}
	if len(r.LedgerFindings) > 0 {
		fmt.Printf("  Ledger entries appended: %d\n", len(r.LedgerFindings))
	}
	if len(r.Skipped) > 0 {
		fmt.Printf("  Skipped (%d):\n", len(r.Skipped))
		for _, s := range r.Skipped {
			fmt.Printf("    - %s\n", s)
		}
	}
	fmt.Printf("  Duration:       %s\n", r.Duration.Truncate(time.Millisecond))
	fmt.Println()
}

// loadAdversaryBody reads agents/tribunal-adversary.md from the user's
// installed location, or — if a path is given — from there directly. The
// body is used to construct the adversary system prompt.
func loadAdversaryBody(override string) (string, error) {
	candidates := []string{}
	if override != "" {
		candidates = append(candidates, override)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".claude", "agents", "tribunal-adversary.md"),
			filepath.Join(home, ".tribunal", "agents", "tribunal-adversary.md"),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(cwd, "agents", "tribunal-adversary.md"),
			filepath.Join(cwd, "..", "agents", "tribunal-adversary.md"),
			filepath.Join(cwd, "..", "..", "agents", "tribunal-adversary.md"),
		)
	}
	for _, c := range candidates {
		if data, err := os.ReadFile(c); err == nil {
			return string(data), nil
		}
	}
	// Fall back to a minimal embedded prompt so the command still works in
	// development sandboxes without the markdown agent files installed.
	return "You are the Tribunal adversary. Find what the lens-parallel trio missed. Output VERDICT: BREAKS | SURVIVES | INDETERMINATE first, then numbered findings each with Category / Severity / Scenario / Suggested defense.", nil
}

func printAdversaryRun(r *review.AdversaryRun) {
	fmt.Printf("Plan:        %s\n", r.PlanID)
	fmt.Printf("Panel:       %s\n", r.Panel)
	fmt.Printf("Verdict:     %s\n", r.OverallVerdict)
	fmt.Printf("Duration:    %s\n", r.Duration.Truncate(time.Millisecond))
	fmt.Println()
	if r.Synthesis != nil {
		printSynthesis(r.Synthesis)
	}
	if len(r.LedgerFindings) > 0 {
		fmt.Printf("\nLedger findings appended: %d\n", len(r.LedgerFindings))
	}
	if len(r.Skipped) > 0 {
		fmt.Println("\nSkipped:")
		for _, s := range r.Skipped {
			fmt.Printf("  - %s\n", s)
		}
	}
	if len(r.WrittenReports) > 0 {
		fmt.Println("\nReports written:")
		for _, p := range r.WrittenReports {
			fmt.Printf("  - %s\n", relTo(p))
		}
	}
}

func relTo(abs string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return abs
	}
	r, err := filepath.Rel(cwd, abs)
	if err != nil || strings.HasPrefix(r, "..") {
		return abs
	}
	return r
}

// ensure agent imports referenced
var _ = agent.RoleAdversary
