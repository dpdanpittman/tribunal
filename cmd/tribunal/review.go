package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newReviewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "review <path>",
		Short: "Run hybrid review (lens-parallel + adversarial gate) on a path",
		Args:  cobra.MaximumNArgs(1),
		Long: `In v0.2+ this orchestrates the trio + adversary against the current diff.

In v0.1 this command is a placeholder while the methodology, skills, and
agents stabilize. To exercise the workflow today, dispatch the trio + adversary
from inside your Claude Code / OpenCode / Cursor session using the installed
skills; findings will land in .tribunal/ledger.jsonl and tribunal ledger
summary will show per-agent reputation.`,
		RunE: func(_ *cobra.Command, args []string) error {
			path := "."
			if len(args) > 0 {
				path = args[0]
			}
			fmt.Printf("tribunal review: orchestration ships in v0.2.\n")
			fmt.Printf("Path:    %s\n", path)
			fmt.Println("For now, dispatch the trio + adversary from your harness and inspect via `tribunal ledger summary`.")
			return nil
		},
	}
}
