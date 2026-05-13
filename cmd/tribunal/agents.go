package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/dpdanpittman/tribunal/internal/agent"
)

func newAgentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Manage Tribunal agent keypairs (per-agent ed25519 identity)",
	}
	cmd.AddCommand(
		newAgentsAddCmd(),
		newAgentsListCmd(),
		newAgentsShowCmd(),
		newAgentsRotateCmd(),
	)
	return cmd
}

func newAgentsAddCmd() *cobra.Command {
	var model, role string
	cmd := &cobra.Command{
		Use:   "add <label>",
		Short: "Generate a new ed25519 keypair and register an agent locally",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			label := args[0]
			if model == "" {
				return fmt.Errorf("--model is required (e.g. claude-opus-4-7)")
			}
			if role == "" {
				return fmt.Errorf("--role is required (one of: %v)", agent.AllRoles())
			}
			r, err := agent.ParseRole(role)
			if err != nil {
				return err
			}
			reg, err := defaultRegistry()
			if err != nil {
				return err
			}
			a, err := reg.Add(label, model, r)
			if err != nil {
				return err
			}
			fmt.Printf("✓ Generated keypair at %s/%s.key\n", reg.AgentsDir(), label)
			fmt.Printf("✓ Agent registered locally (pubkey: %s)\n", a.Pubkey)
			return nil
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "Model identifier (e.g. claude-opus-4-7)")
	cmd.Flags().StringVar(&role, "role", "", "Agent role")
	return cmd
}

func newAgentsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all locally-registered agents",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			reg, err := defaultRegistry()
			if err != nil {
				return err
			}
			agents, err := reg.List()
			if err != nil {
				return err
			}
			if len(agents) == 0 {
				fmt.Println("No agents registered. Add one with: tribunal agents add <label> --model <id> --role <role>")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "LABEL\tROLE\tMODEL\tPUBKEY\tSTATUS")
			for _, a := range agents {
				status := "active"
				if !a.RetiredAt.IsZero() {
					status = "retired → " + a.SupersededBy
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", a.Label, a.Role, a.ModelID, a.Pubkey, status)
			}
			return w.Flush()
		},
	}
}

func newAgentsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <label>",
		Short: "Show metadata for a single agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			reg, err := defaultRegistry()
			if err != nil {
				return err
			}
			a, err := reg.Get(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("Label:        %s\n", a.Label)
			fmt.Printf("Role:         %s\n", a.Role)
			fmt.Printf("Model:        %s\n", a.ModelID)
			fmt.Printf("Pubkey:       %s\n", a.Pubkey)
			fmt.Printf("Created at:   %s\n", a.CreatedAt.Format("2006-01-02 15:04 MST"))
			if !a.RetiredAt.IsZero() {
				fmt.Printf("Retired at:   %s\n", a.RetiredAt.Format("2006-01-02 15:04 MST"))
				fmt.Printf("Superseded by: %s\n", a.SupersededBy)
			}
			return nil
		},
	}
}

func newAgentsRotateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rotate <old-label> <new-label>",
		Short: "Generate a fresh keypair for a successor agent (model upgrade etc.)",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			reg, err := defaultRegistry()
			if err != nil {
				return err
			}
			newAgent, err := reg.Rotate(args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Printf("✓ Retired agent %q\n", args[0])
			fmt.Printf("✓ Created agent %q (pubkey: %s)\n", newAgent.Label, newAgent.Pubkey)
			fmt.Println("Note: on-chain rotation (v0.3+) is a separate step — run `tribunal chain rotate`.")
			return nil
		},
	}
	return cmd
}

// defaultRegistry returns a Registry rooted at ~/.tribunal.
func defaultRegistry() (*agent.Registry, error) {
	root, err := agent.DefaultRoot()
	if err != nil {
		return nil, err
	}
	return agent.NewRegistry(root), nil
}
