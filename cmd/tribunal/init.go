package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize Tribunal in the current project (skills install + .tribunal/ scaffolding)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			tribunalDir := filepath.Join(cwd, ".tribunal")
			if err := os.MkdirAll(tribunalDir, 0o755); err != nil {
				return err
			}
			subdirs := []string{"findings", "resolutions", "reports", "plans"}
			for _, sd := range subdirs {
				if err := os.MkdirAll(filepath.Join(tribunalDir, sd), 0o755); err != nil {
					return err
				}
			}
			fmt.Printf("✓ Created %s/\n", tribunalDir)
			if target != "" {
				fmt.Printf("Note: skill installation for target %q is not yet implemented in v0.1 (skills/* still need to be copied manually into your host).\n", target)
			} else {
				fmt.Println("Note: skill installation will land in v0.1.1. For now, copy skills/* and agents/* into your host manually.")
			}
			fmt.Println("Next steps:")
			fmt.Println("  tribunal agents add <label> --model <id> --role <role>")
			fmt.Println("  tribunal review .   # (full orchestration ships in v0.2)")
			return nil
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "Host harness: claude-code | opencode | cursor")
	return cmd
}
