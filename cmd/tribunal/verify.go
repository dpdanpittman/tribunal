package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dpdanpittman/tribunal/internal/verify"
)

func newVerifyCmd() *cobra.Command {
	var (
		jsonOut bool
		noHalt  bool
	)
	cmd := &cobra.Command{
		Use:   "verify [path]",
		Short: "Run the Tribunal verification pyramid against a project",
		Long: `Runs the configured verification stack (default: Go) layer by layer,
halting at the first failure unless --no-halt is set. Configuration lives
in tribunal.yaml at the project root; defaults apply if the file is absent.

For Go projects the canonical layers are:
  go build → gofmt -s -d → go vet → (staticcheck) → (golangci-lint) → go test → (go-fuzz)

Optional layers (staticcheck, golangci-lint, go-fuzz) are off by default;
opt in via tribunal.yaml.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			root := "."
			if len(args) > 0 {
				root = args[0]
			}
			cfg, err := verify.LoadConfig(root)
			if err != nil {
				return err
			}
			if noHalt {
				f := false
				cfg.HaltOnFailure = &f
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			report, err := verify.Run(ctx, root, cfg)
			if err != nil {
				return err
			}
			if jsonOut {
				return writeJSON(report)
			}
			printReport(report)
			if !report.OverallPassed {
				os.Exit(2)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit the report as JSON instead of a table")
	cmd.Flags().BoolVar(&noHalt, "no-halt", false, "Run all applicable layers even if one fails")
	return cmd
}

func printReport(r *verify.PyramidReport) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "LAYER\tSTATUS\tDURATION\tNOTE")
	for _, l := range r.Layers {
		dur := l.Duration.Truncate(time.Millisecond).String()
		if l.Duration == 0 {
			dur = "-"
		}
		note := l.Note
		if note == "" && l.Status == verify.StatusFailed {
			note = "exit=" + fmt.Sprint(l.ExitCode)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", l.Layer, l.Status, dur, note)
	}
	_ = w.Flush()

	passed, failed, skipped, na := r.Counts()
	fmt.Printf("\nResult: %d passed, %d failed, %d skipped, %d not_applicable in %s\n",
		passed, failed, skipped, na, r.Completed.Sub(r.Started).Truncate(time.Millisecond))

	if r.SuggestedAction != "" {
		fmt.Printf("\n%s\n", r.SuggestedAction)
	}

	// On failure, print the first failing layer's stderr/stdout snippet to
	// help the user route quickly.
	for _, l := range r.Layers {
		if l.Status == verify.StatusFailed {
			fmt.Printf("\n--- %s output ---\n", l.Layer)
			if l.Stdout != "" {
				fmt.Println(l.Stdout)
			}
			if l.Stderr != "" {
				fmt.Println(l.Stderr)
			}
			break
		}
	}
}

func writeJSON(r *verify.PyramidReport) error {
	// Defer json import to avoid pulling encoding/json into the binary's
	// hot path when not needed.
	enc := newJSONEncoder(os.Stdout)
	return enc.Encode(r)
}
