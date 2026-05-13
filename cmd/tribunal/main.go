// Command tribunal is the Tribunal CLI: agent registry, signed-finding
// ledger, hybrid review orchestration, and (v0.3+) on-chain settlement.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags. Default is "dev" for
// in-tree builds.
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "tribunal",
		Short:         "Tribunal — adversarial multi-model code review with on-chain reputation",
		Long:          longHelp,
		SilenceUsage:  true,
		SilenceErrors: false,
		Version:       version,
	}
	cmd.AddCommand(
		newAgentsCmd(),
		newLedgerCmd(),
		newInitCmd(),
		newReviewCmd(),
		newVerifyCmd(),
		newDispatchCmd(),
	)
	return cmd
}

const longHelp = `Tribunal composes a process backbone (state machine, spec-driven gates,
role separation) with a correctness toolkit (lens-parallel review + an
adversarial gate + a verification pyramid) and an on-chain reputation
layer that learns over time which agents actually find real bugs.

See docs/methodology.md in the Tribunal repo for the design.

Common workflows:

  tribunal init --target claude-code
  tribunal agents add claude-adversary --model claude-opus-4-7 --role adversary
  tribunal review .
  tribunal ledger summary
`

// printErr formats an error to stderr with the program prefix. Used by
// subcommands when reporting expected errors that shouldn't trigger a
// cobra-emitted Usage line.
func printErr(err error) {
	fmt.Fprintf(os.Stderr, "tribunal: %v\n", err)
}
