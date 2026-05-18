package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dpdanpittman/tribunal/internal/agent"
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
		newLedgerTriageCmd(),
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
			if f.ClawpatchID != "" {
				fmt.Printf("Clawpatch ID: %s\n", f.ClawpatchID)
			}
			if err := f.Verify(); err != nil {
				fmt.Printf("Signature:    INVALID (%v)\n", err)
			} else {
				fmt.Printf("Signature:    OK\n")
			}
			// Surface the latest triage state if one exists.
			triage, err := l.LatestTriageByFinding()
			if err == nil {
				if t, ok := triage[f.FindingID]; ok {
					fmt.Printf("Triage:       %s (%s)\n", t.Status, t.Timestamp.Format(time.RFC3339))
					if t.Note != "" {
						fmt.Printf("Triage note:  %s\n", t.Note)
					}
				}
			}
			return nil
		},
	}
}

func newLedgerTriageCmd() *cobra.Command {
	var status string
	var note string
	var triagerLabel string
	var noAutoRegister bool
	cmd := &cobra.Command{
		Use:   "triage <finding-id>",
		Short: "Append a triage event for a finding (open|in-progress|fixed|false-positive|wont-fix|uncertain)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			findingID := args[0]
			st := ledger.TriageStatus(status)
			if !st.IsValid() {
				return fmt.Errorf("invalid status %q (want one of: open, in-progress, fixed, false-positive, wont-fix, uncertain)", status)
			}
			l := projectLedger()
			fnd, err := l.FindingByID(findingID)
			if err != nil {
				return err
			}
			if fnd == nil {
				return fmt.Errorf("finding %q not found", findingID)
			}
			reg, err := defaultRegistry()
			if err != nil {
				return err
			}
			kp, label, err := resolveTriagerKey(reg, triagerLabel, !noAutoRegister)
			if err != nil {
				return err
			}
			evt := ledger.NewTriageEvent(findingID, fnd.PlanID, st, kp, label, note)
			if err := evt.Sign(kp); err != nil {
				return err
			}
			if err := l.AppendTriage(evt); err != nil {
				return err
			}
			fmt.Printf("✓ triage %s → %s by %s\n", findingID, st, label)
			if note != "" {
				fmt.Printf("  note: %s\n", note)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "open|in-progress|fixed|false-positive|wont-fix|uncertain (required)")
	cmd.Flags().StringVar(&note, "note", "", "Optional free-text note recorded with the triage event")
	cmd.Flags().StringVar(&triagerLabel, "as", "human-triager", "Agent label to sign the triage event")
	cmd.Flags().BoolVar(&noAutoRegister, "no-auto-register", false, "Refuse to auto-create the triager agent keypair")
	_ = cmd.MarkFlagRequired("status")
	return cmd
}

// resolveTriagerKey returns a keypair to sign a triage event with. If the
// triager agent does not exist and autoRegister is true, a new keypair is
// created and registered with the qa role (because triage is a QA-side
// action, not an adversarial one).
func resolveTriagerKey(reg *agent.Registry, label string, autoRegister bool) (*agent.Keypair, string, error) {
	if label == "" {
		label = "human-triager"
	}
	if existing, err := reg.Get(label); err == nil {
		kp, err := reg.LoadKeypair(existing.Label)
		if err != nil {
			return nil, label, err
		}
		return kp, existing.Label, nil
	}
	if !autoRegister {
		return nil, label, fmt.Errorf("no registered agent for %q (run `tribunal agents add` or omit --no-auto-register)", label)
	}
	a, err := reg.Add(label, "human", agent.RoleQA)
	if err != nil {
		return nil, label, err
	}
	kp, err := reg.LoadKeypair(a.Label)
	if err != nil {
		return nil, label, err
	}
	return kp, a.Label, nil
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
