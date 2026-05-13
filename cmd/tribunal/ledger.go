package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dpdanpittman/tribunal/internal/ledger"
)

func newLedgerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ledger",
		Short: "Inspect the local signed ledger and per-agent reputation",
	}
	cmd.AddCommand(
		newLedgerSummaryCmd(),
		newLedgerLeaderboardCmd(),
		newLedgerFindCmd(),
		newLedgerVerifyCmd(),
	)
	return cmd
}

func newLedgerSummaryCmd() *cobra.Command {
	var window time.Duration
	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Per-agent reputation snapshot computed from the local ledger",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			l := projectLedger()
			findings, resolutions, err := l.All()
			if err != nil {
				return err
			}
			if len(findings) == 0 {
				fmt.Println("Ledger is empty. Run a review or seed findings to populate.")
				return nil
			}
			cfg := ledger.DefaultReputationConfig()
			if window > 0 {
				cfg.Window = window
			}
			reps := ledger.ComputeReputation(findings, resolutions, cfg, time.Now())
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "AGENT\tSCORE\tTP\tFP\tFINDINGS")
			for _, r := range reps {
				fmt.Fprintf(w, "%s\t%.2f\t%d\t%d\t%d\n", r.AgentLabel, r.Score, r.TPCount, r.FPCount, r.Findings)
			}
			return w.Flush()
		},
	}
	cmd.Flags().DurationVar(&window, "window", 0, "Lookback window (default: 90d)")
	return cmd
}

func newLedgerLeaderboardCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "leaderboard",
		Short: "Top agents by reputation score",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			l := projectLedger()
			findings, resolutions, err := l.All()
			if err != nil {
				return err
			}
			reps := ledger.ComputeReputation(findings, resolutions, ledger.DefaultReputationConfig(), time.Now())
			if len(reps) == 0 {
				fmt.Println("No reputation data yet.")
				return nil
			}
			if limit > 0 && limit < len(reps) {
				reps = reps[:limit]
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "RANK\tAGENT\tSCORE\tTP\tFP")
			for i, r := range reps {
				fmt.Fprintf(w, "%d\t%s\t%.2f\t%d\t%d\n", i+1, r.AgentLabel, r.Score, r.TPCount, r.FPCount)
			}
			return w.Flush()
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum number of rows")
	return cmd
}

func newLedgerFindCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "find <finding-id>",
		Short: "Print a single finding by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			l := projectLedger()
			f, err := l.FindingByID(args[0])
			if err != nil {
				return err
			}
			if f == nil {
				return fmt.Errorf("finding %q not found", args[0])
			}
			fmt.Printf("Finding ID:   %s\n", f.FindingID)
			fmt.Printf("Plan ID:      %s\n", f.PlanID)
			fmt.Printf("Round:        %d\n", f.Round)
			fmt.Printf("Agent:        %s (%s)\n", f.AgentLabel, f.AgentPubkey)
			fmt.Printf("Severity:     %s\n", f.Severity)
			fmt.Printf("Category:     %s\n", f.Category)
			fmt.Printf("Claim hash:   %s\n", f.ClaimHash)
			fmt.Printf("Claim URI:    %s\n", f.ClaimURI)
			fmt.Printf("Stake:        %d\n", f.Stake)
			fmt.Printf("Timestamp:    %s\n", f.Timestamp.Format(time.RFC3339))
			if err := f.Verify(); err != nil {
				fmt.Printf("Signature:    INVALID (%v)\n", err)
			} else {
				fmt.Printf("Signature:    OK\n")
			}
			return nil
		},
	}
}

func newLedgerVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Re-check every signature in the local ledger",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			l := projectLedger()
			findings, resolutions, err := l.All()
			if err != nil {
				return err
			}
			if err := l.VerifyAll(); err != nil {
				return fmt.Errorf("verify failed: %w", err)
			}
			fmt.Printf("✓ %d findings and %d resolutions verified\n", len(findings), len(resolutions))
			return nil
		},
	}
}

func projectLedger() *ledger.Ledger {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	return ledger.New(filepath.Join(cwd, ".tribunal", "ledger.jsonl"))
}
