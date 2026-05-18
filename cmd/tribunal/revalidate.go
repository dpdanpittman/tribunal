package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/dpdanpittman/tribunal/internal/clawpatch"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

func newRevalidateCmd() *cobra.Command {
	var (
		findingID      string
		all            bool
		since          string
		limit          int
		triagerLabel   string
		noAutoRegister bool
		clawpatchModel string
	)
	cmd := &cobra.Command{
		Use:   "revalidate",
		Short: "Re-check Tribunal findings via clawpatch revalidate and record outcomes",
		Long: `tribunal revalidate is a thin wrapper around clawpatch revalidate.
For every finding it re-checks, a signed TriageEvent is appended to the
ledger reflecting the new clawpatch status (open|fixed|false-positive|
uncertain|wont-fix).

Pass exactly one of:
  --finding <tribunal-id>   re-check a single finding
  --all                     re-check every still-open finding
  --since <git-ref>         re-check findings touching files changed since
                            the given git reference`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if !exactlyOne(findingID != "", all, since != "") {
				return fmt.Errorf("specify exactly one of --finding, --all, --since")
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			l := projectLedger()
			reg, err := defaultRegistry()
			if err != nil {
				return err
			}
			kp, label, err := resolveTriagerKey(reg, triagerLabel, !noAutoRegister)
			if err != nil {
				return err
			}

			opts := clawpatch.RevalidateOpts{
				All:   all,
				Since: since,
				Limit: limit,
			}
			if findingID != "" {
				fnd, err := l.FindingByID(findingID)
				if err != nil {
					return err
				}
				if fnd == nil {
					return fmt.Errorf("finding %q not found in ledger", findingID)
				}
				if fnd.ClawpatchID == "" {
					return fmt.Errorf("finding %q has no clawpatch_id — only clawpatch-sourced findings are revalidatable via this command", findingID)
				}
				opts.Finding = fnd.ClawpatchID
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			runner := &clawpatch.Runner{
				Cwd:      cwd,
				Provider: "acpx",
				Model:    clawpatchModel,
			}
			outcomes, err := runner.Revalidate(ctx, opts)
			if err != nil {
				return err
			}

			// Index Tribunal findings by ClawpatchID for write-back.
			findings, _, err := l.All()
			if err != nil {
				return err
			}
			byClawpatchID := make(map[string]*ledger.Finding, len(findings))
			for _, f := range findings {
				if f.ClawpatchID != "" {
					byClawpatchID[f.ClawpatchID] = f
				}
			}

			var written, skipped int
			for _, o := range outcomes {
				target, ok := byClawpatchID[o.Finding]
				if !ok {
					skipped++
					fmt.Printf("  - %s: outcome=%s (no Tribunal finding; skipped)\n", o.Finding, o.Outcome)
					continue
				}
				triageStatus := clawpatch.TriageFromClawpatch(o.Outcome)
				note := fmt.Sprintf("clawpatch revalidate: outcome=%s", o.Outcome)
				if o.Reasoning != "" {
					note += " — " + truncate(o.Reasoning, 240)
				}
				evt := ledger.NewTriageEvent(target.FindingID, target.PlanID, triageStatus, kp, label, note)
				if err := evt.Sign(kp); err != nil {
					return err
				}
				if err := l.AppendTriage(evt); err != nil {
					return err
				}
				written++
				fmt.Printf("  - %s (clawpatch %s): %s\n", target.FindingID, o.Finding, triageStatus)
			}
			fmt.Printf("\nRevalidated %d (skipped %d)\n", written, skipped)
			return nil
		},
	}
	cmd.Flags().StringVar(&findingID, "finding", "", "Tribunal finding ID to revalidate")
	cmd.Flags().BoolVar(&all, "all", false, "Revalidate every still-open finding")
	cmd.Flags().StringVar(&since, "since", "", "Revalidate findings touching files changed since the given git ref")
	cmd.Flags().IntVar(&limit, "limit", 0, "Cap on revalidations per run (forwarded to clawpatch --limit)")
	cmd.Flags().StringVar(&triagerLabel, "as", "human-triager", "Agent label to sign the triage events")
	cmd.Flags().BoolVar(&noAutoRegister, "no-auto-register", false, "Refuse to auto-create the triager agent keypair")
	cmd.Flags().StringVar(&clawpatchModel, "clawpatch-model", "", "Model passed to clawpatch (--model)")
	return cmd
}

func exactlyOne(vs ...bool) bool {
	n := 0
	for _, v := range vs {
		if v {
			n++
		}
	}
	return n == 1
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
