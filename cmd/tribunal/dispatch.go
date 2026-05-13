package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dpdanpittman/tribunal/internal/dispatch"
)

func newDispatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dispatch",
		Short: "Ad-hoc adversary-panel dispatch (without the full review pipeline)",
	}
	cmd.AddCommand(
		newDispatchTestCmd(),
		newDispatchAttackCmd(),
	)
	return cmd
}

// newDispatchTestCmd performs a connectivity check against a panel without
// running an adversarial review. Useful for verifying API keys.
func newDispatchTestCmd() *cobra.Command {
	var panelName string
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Verify configured providers are reachable",
		RunE: func(_ *cobra.Command, _ []string) error {
			cwd, _ := os.Getwd()
			cfg, err := dispatch.LoadConfig(cwd)
			if err != nil {
				return err
			}
			panel, err := cfg.Select(panelName)
			if err != nil {
				return err
			}
			if err := dispatch.ValidatePanel(panel); err != nil {
				return err
			}
			fmt.Printf("Panel %q has %d members:\n", panel.Name, len(panel.Members))
			for _, m := range panel.Members {
				fmt.Printf("  - %s (provider=%s model=%s temp=%.1f focus=%s)\n", m.Label, m.Provider, m.Model, m.Temperature, m.Focus)
			}
			reg := buildRegistry()
			// Report which providers are actually registered.
			seen := map[string]bool{}
			for _, m := range panel.Members {
				if seen[m.Provider] {
					continue
				}
				seen[m.Provider] = true
				if _, err := reg.Get(m.Provider); err != nil {
					fmt.Printf("\nprovider %s: NOT REGISTERED (%v)\n", m.Provider, err)
				} else {
					fmt.Printf("\nprovider %s: registered\n", m.Provider)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&panelName, "panel", "default", "Panel name to inspect (default | high-stakes)")
	return cmd
}

// newDispatchAttackCmd runs the adversary panel against the contents of
// stdin (treated as the user-prompt body). Useful for quick CLI dogfooding
// without the full review pipeline.
func newDispatchAttackCmd() *cobra.Command {
	var (
		panelName string
		bucket    string
	)
	cmd := &cobra.Command{
		Use:   "attack",
		Short: "Dispatch the adversary panel against stdin (debug / dogfood path)",
		Long: `Reads the user prompt from stdin, dispatches the configured adversary
panel (defaults to the three Claude variants), and prints the synthesized
verdict + per-member reports.

The user-prompt body should already include intent / plan / diff / reviewer
reports as the methodology specifies — this command is the raw dispatch,
not the full review orchestration. For the full hybrid review, use
'tribunal review'.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			cwd, _ := os.Getwd()
			cfg, err := dispatch.LoadConfig(cwd)
			if err != nil {
				return err
			}
			panel, err := cfg.Select(panelName)
			if err != nil {
				return err
			}
			if err := dispatch.ValidatePanel(panel); err != nil {
				return err
			}
			user, err := readAllStdin()
			if err != nil {
				return err
			}
			if strings.TrimSpace(user) == "" {
				return fmt.Errorf("dispatch attack: stdin is empty (pipe the user prompt in)")
			}
			adversaryBody := "(adversary system prompt resolution is wired through the review skill; for raw dispatch the user-prompt is sent without the canonical preamble)"
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			reg := buildRegistry()
			reports, err := dispatch.Dispatch(ctx, reg, panel, dispatch.BuildSystemPrompt(adversaryBody, ""), user)
			if err != nil {
				return err
			}
			bucketFn, err := dispatch.SelectBucket(bucket)
			if err != nil {
				return err
			}
			syn := dispatch.Synthesize(reports, bucketFn)
			printSynthesis(syn)
			if syn.Overall == dispatch.VerdictBreaks {
				os.Exit(3)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&panelName, "panel", "default", "Panel name (default | high-stakes)")
	cmd.Flags().StringVar(&bucket, "bucket", "composite:vendor_family,focus", "Diversity bucket axis")
	return cmd
}

// buildRegistry resolves all available providers from environment. Missing
// API keys cause the corresponding provider to remain unregistered, which
// in turn surfaces as a per-member INDETERMINATE in the dispatch output.
func buildRegistry() *dispatch.Registry {
	reg := dispatch.NewRegistry()
	if claude, err := dispatch.NewClaudeProvider(); err == nil {
		reg.Register(claude)
	}
	// OpenAI / Google / Local providers land in v0.2.1+ — registered once
	// their HTTP scaffolding is in place. Unregistered providers in the
	// panel get reported but don't crash the run.
	return reg
}

func readAllStdin() (string, error) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", fmt.Errorf("stdin is a terminal; pipe the user-prompt body in (e.g. 'cat prompt.md | tribunal dispatch attack')")
	}
	buf := make([]byte, 0, 64*1024)
	chunk := make([]byte, 32*1024)
	for {
		n, err := os.Stdin.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if err != nil {
			break
		}
	}
	return string(buf), nil
}

func printSynthesis(s *dispatch.Synthesis) {
	fmt.Printf("Overall verdict: %s\n\n", s.Overall)

	fmt.Println("Per-member verdicts:")
	for label, v := range s.Verdicts {
		bucket := s.Buckets[label]
		fmt.Printf("  - %s [%s]: %s\n", label, bucket, v)
	}
	fmt.Println()

	if len(s.Shared) > 0 {
		fmt.Printf("Shared findings (≥ 2 members):\n")
		for _, f := range s.Shared {
			fmt.Printf("  - %s [%s] (members: %s)\n", f.Category, f.Severity, strings.Join(f.Members, ", "))
		}
		fmt.Println()
	}

	if len(s.Unique) > 0 {
		fmt.Printf("Unique findings (single-member, blind-spot escapes):\n")
		for _, f := range s.Unique {
			fmt.Printf("  - %s [%s] %s [bucket=%s]\n", f.Category, f.Severity, f.Member, f.Bucket)
		}
		fmt.Println()
	}

	if len(s.Coverage) > 0 {
		fmt.Println("Category coverage:")
		for cat, members := range s.Coverage {
			fmt.Printf("  - %s: %s\n", cat, strings.Join(members, ", "))
		}
	}

	// For each member with non-empty raw text, print a short preview.
	fmt.Println("\nPer-member reports (preview):")
	for _, r := range s.Reports {
		if r == nil {
			continue
		}
		preview := r.RawText
		if len(preview) > 500 {
			preview = preview[:500] + "\n... (truncated)"
		}
		fmt.Printf("\n--- %s (%s, verdict=%s) ---\n%s\n", r.Member.Label, r.Member.Provider, r.Verdict, preview)
		if r.Error != "" {
			fmt.Printf("ERROR: %s\n", r.Error)
		}
	}
}
