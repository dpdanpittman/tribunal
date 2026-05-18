package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dpdanpittman/tribunal/internal/agent"
	"github.com/dpdanpittman/tribunal/internal/chain"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

// newChainCmd is the root for every on-chain operation. v0.3-only —
// fully ignorable for v0.1/v0.2 workflows.
func newChainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chain",
		Short: "On-chain operations against the Tribunal Reputation contract (Burnt XION)",
		Long: `chain subcommands talk to the deployed tribunal-reputation contract.

Configuration lives at ~/.tribunal/chain.yaml. Use 'tribunal chain init'
to bootstrap it after deploying the contract via scripts/deploy-contract.sh.`,
	}
	cmd.AddCommand(
		newChainInitCmd(),
		newChainStatusCmd(),
		newChainRegisterCmd(),
		newChainSyncCmd(),
		newChainQueryCmd(),
		newChainRotateCmd(),
		newChainQueueCmd(),
	)
	return cmd
}

// ---- chain init ----

func newChainInitCmd() *cobra.Command {
	cfg := &chain.Config{}
	var path string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write ~/.tribunal/chain.yaml with the given chain + contract config",
		RunE: func(_ *cobra.Command, _ []string) error {
			if rewritten, changed := chain.NormalizeRPCScheme(cfg.NodeRPC); changed {
				fmt.Fprintf(os.Stderr,
					"tribunal: rewriting --node-rpc %q -> %q (Go HTTP client requires http://; xiond accepts both)\n",
					cfg.NodeRPC, rewritten)
				cfg.NodeRPC = rewritten
			}

			if err := cfg.Save(path); err != nil {
				return err
			}

			// Best-effort: query the contract for outcome_reward_multiplier
			// and update the saved file. Failures here are non-fatal — the
			// basic config is already on disk and the operator can edit
			// the multiplier in by hand if the chain is unreachable.
			client := chain.New(cfg)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if resp, err := client.ContractConfig(ctx); err != nil {
				fmt.Fprintf(os.Stderr,
					"tribunal: WARNING — could not query contract for reward multiplier (%v); chain.yaml has outcome_reward_multiplier=0. Edit by hand or re-run chain init when the chain is reachable.\n",
					err)
			} else {
				var n uint64
				if _, parseErr := fmt.Sscanf(resp.OutcomeRewardMultiplier, "%d", &n); parseErr == nil {
					cfg.OutcomeRewardMultiplier = n
					if err := cfg.Save(path); err != nil {
						return err
					}
				}
			}

			fmt.Println("✓ chain config saved")
			return nil
		},
	}
	cmd.Flags().StringVar(&cfg.ChainID, "chain-id", "", "Cosmos chain id (e.g. xion-testnet-2)")
	cmd.Flags().StringVar(&cfg.NodeRPC, "node-rpc", "", "Tendermint RPC endpoint")
	cmd.Flags().StringVar(&cfg.NodeREST, "node-rest", "", "LCD REST endpoint")
	cmd.Flags().StringVar(&cfg.ContractAddress, "contract", "", "Deployed contract address (cosmwasm1...)")
	cmd.Flags().StringVar(&cfg.OperatorKeyName, "key", "", "xiond keyring entry to sign txs with")
	cmd.Flags().StringVar(&cfg.KeyringBackend, "keyring-backend", "test", "xiond keyring backend (os/file/test/...)")
	cmd.Flags().StringVar(&cfg.GasPrices, "gas-prices", "0.025uxion", "Fee unit string")
	cmd.Flags().StringVar(&cfg.GasAdjustment, "gas-adjustment", "1.4", "Simulated-gas safety multiplier")
	cmd.Flags().StringVar(&cfg.XiondBinary, "xiond-binary", "xiond", "xiond command (may include 'docker exec ...' prefix)")
	cmd.Flags().StringVar(&path, "out", "", "Override config path (defaults to ~/.tribunal/chain.yaml)")
	return cmd
}

// ---- chain status ----

func newChainStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Ping the configured RPC endpoint and print the latest block height",
		RunE: func(_ *cobra.Command, _ []string) error {
			client, err := newChainClient()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			h, err := client.Status(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("chain=%s height=%d contract=%s\n",
				client.Config().ChainID, h, client.Config().ContractAddress)
			return nil
		},
	}
	return cmd
}

// ---- chain register ----

func newChainRegisterCmd() *cobra.Command {
	var initialBalance uint64
	cmd := &cobra.Command{
		Use:   "register <agent-label>",
		Short: "Register a locally-existing agent on-chain",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			label := args[0]
			reg, err := defaultRegistry()
			if err != nil {
				return err
			}
			a, err := reg.Get(label)
			if err != nil {
				return err
			}
			kp, err := reg.LoadKeypair(label)
			if err != nil {
				return err
			}
			msg, err := chain.BuildRegisterAgent(kp, a.Label, a.ModelID, a.Role, initialBalance)
			if err != nil {
				return err
			}
			client, err := newChainClient()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			res, err := client.Execute(ctx, msg)
			if err != nil {
				return err
			}
			fmt.Printf("✓ registered %s on-chain (txhash: %s)\n", label, res.TxHash)
			return nil
		},
	}
	cmd.Flags().Uint64Var(&initialBalance, "initial-balance", 0, "Override contract default (0 = use contract default)")
	return cmd
}

// ---- chain sync ----

func newChainSyncCmd() *cobra.Command {
	var planID string
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Batch-commit local findings + resolutions to the contract",
		Long: `sync flushes the local .tribunal/ledger.jsonl to the on-chain contract.

Without --plan it syncs every plan in the ledger, grouped per plan_id.
With --plan it syncs only the specified plan. Queued real-time commits
for the plan are drained into the same transaction.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			reg, err := defaultRegistry()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			lg := ledger.New(ledger.DefaultPath(cwd))
			qpath := chain.DefaultQueuePath(cwd)

			client, err := newChainClient()
			if err != nil {
				return err
			}
			sync := &chain.Sync{
				Client: client,
				Keys:   chain.NewRegistryResolver(reg),
				Queue:  chain.NewQueue(qpath),
			}

			// v0.3.5 / F-OPUS-002: outer ctx scales with the number of
			// plans that will be synced so plans 4+ aren't truncated by a
			// fixed 5-minute outer bound. Read the ledger first to count
			// distinct plans, then size the budget.
			findings, resolutions, err := lg.All()
			if err != nil {
				return err
			}
			if planID != "" {
				ctx, cancel := context.WithTimeout(context.Background(), chain.SyncBudgetForPlans(1))
				defer cancel()
				res, err := sync.SyncPlan(ctx, planID, findings, resolutions)
				if err != nil {
					return err
				}
				printSyncResult(res)
				return nil
			}

			planSet := map[string]struct{}{}
			for _, f := range findings {
				planSet[f.PlanID] = struct{}{}
			}
			for _, r := range resolutions {
				planSet[r.PlanID] = struct{}{}
			}
			ctx, cancel := context.WithTimeout(context.Background(), chain.SyncBudgetForPlans(len(planSet)))
			defer cancel()

			// v0.3.4 / P-v033-audit F-ARCH-303: SyncAll's `errors.Join`
			// aggregation produces partial results even on error. Render
			// them first so the operator sees what landed before seeing
			// what failed.
			results, err := sync.SyncAll(ctx, lg)
			for _, r := range results {
				printSyncResult(r)
			}
			if err != nil {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&planID, "plan", "", "Sync only this plan id")
	return cmd
}

func printSyncResult(r *chain.SyncResult) {
	fmt.Printf("plan=%s findings=%d resolutions=%d queue_drained=%d commit_tx=%s resolve_tx=%s\n",
		r.PlanID, r.FindingsSent, r.ResolutionsSent, r.QueueDrainedCount, r.CommitTxHash, r.ResolveTxHash)
}

// ---- chain query ----

func newChainQueryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "query",
		Short: "Read-only queries against the contract",
	}
	cmd.AddCommand(
		newChainQueryReputationCmd(),
		newChainQueryAgentCmd(),
		newChainQueryFindingCmd(),
		newChainQueryLeaderboardCmd(),
		newChainQueryConfigCmd(),
	)
	return cmd
}

func newChainQueryReputationCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reputation <agent-label-or-pubkey>",
		Short: "Show the on-chain reputation balance for an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			client, err := newChainClient()
			if err != nil {
				return err
			}
			pub, err := resolvePubkeyArg(args[0])
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			rep, err := client.Reputation(ctx, pub)
			if err != nil {
				return err
			}
			label := "(unregistered)"
			if rep.Label != nil {
				label = *rep.Label
			}
			fmt.Printf("agent=%s pubkey=%s balance=%s tp=%d fp=%d\n",
				label, pub, rep.Balance, rep.TPCount, rep.FPCount)
			return nil
		},
	}
}

func newChainQueryAgentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agent <agent-label-or-pubkey>",
		Short: "Show the full on-chain agent record (JSON)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			client, err := newChainClient()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var resp *chain.AgentResp
			// Prefer label lookup; if it looks like a pubkey, use that.
			if isPubkeyArg(args[0]) {
				resp, err = client.Agent(ctx, args[0])
			} else {
				resp, err = client.AgentByLabel(ctx, args[0])
			}
			if err != nil {
				return err
			}
			return writeJSONTo(os.Stdout, resp)
		},
	}
}

func newChainQueryFindingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "finding <plan-id> <finding-id>",
		Short: "Show the on-chain state for a specific finding",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			client, err := newChainClient()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resp, err := client.Finding(ctx, args[0], args[1])
			if err != nil {
				return err
			}
			return writeJSONTo(os.Stdout, resp)
		},
	}
}

func newChainQueryLeaderboardCmd() *cobra.Command {
	var role string
	var limit uint32
	cmd := &cobra.Command{
		Use:   "leaderboard",
		Short: "Show top agents by on-chain reputation balance",
		RunE: func(_ *cobra.Command, _ []string) error {
			client, err := newChainClient()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resp, err := client.Leaderboard(ctx, role, limit)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "RANK\tLABEL\tROLE\tBALANCE\tTP\tFP")
			for i, e := range resp.Entries {
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%d\t%d\n",
					i+1, e.Label, e.Role, e.Balance, e.TPCount, e.FPCount)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "Filter by role")
	cmd.Flags().Uint32Var(&limit, "limit", 20, "Max entries (contract cap = 100)")
	return cmd
}

func newChainQueryConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Show the contract's stored config (JSON)",
		RunE: func(_ *cobra.Command, _ []string) error {
			client, err := newChainClient()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resp, err := client.ContractConfig(ctx)
			if err != nil {
				return err
			}
			return writeJSONTo(os.Stdout, resp)
		},
	}
}

// ---- chain rotate ----

func newChainRotateCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "rotate <old-label> <new-label>",
		Short: "Rotate an agent on-chain (preserves TP/FP history, resets balance to floor)",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			reg, err := defaultRegistry()
			if err != nil {
				return err
			}
			oldLabel, newLabel := args[0], args[1]
			oldAgent, err := reg.Get(oldLabel)
			if err != nil {
				return err
			}
			newAgent, err := reg.Get(newLabel)
			if err != nil {
				return fmt.Errorf("new agent %q must exist locally before chain rotation; run `tribunal agents add %s ...` first", newLabel, newLabel)
			}
			oldWire, err := chain.PubkeyToWire(oldAgent.Pubkey)
			if err != nil {
				return err
			}
			newWire, err := chain.PubkeyToWire(newAgent.Pubkey)
			if err != nil {
				return err
			}
			msg := &chain.ExecuteMsg{
				RotateAgent: &chain.RotateAgentMsg{
					OldPubkey:  oldWire,
					NewPubkey:  newWire,
					NewLabel:   newAgent.Label,
					NewModelID: newAgent.ModelID,
					Reason:     reason,
				},
			}
			client, err := newChainClient()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			res, err := client.Execute(ctx, msg)
			if err != nil {
				return err
			}
			fmt.Printf("✓ rotated %s → %s (txhash: %s)\n", oldLabel, newLabel, res.TxHash)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Free-text rationale recorded with the rotation")
	return cmd
}

// ---- chain queue ----

func newChainQueueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "Inspect the local retry queue for failed real-time commits",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List queued retry entries",
			RunE: func(_ *cobra.Command, _ []string) error {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				q := chain.NewQueue(chain.DefaultQueuePath(cwd))
				entries, err := q.All()
				if err != nil {
					return err
				}
				if len(entries) == 0 {
					fmt.Println("(queue empty)")
					return nil
				}
				return writeJSONTo(os.Stdout, entries)
			},
		},
		&cobra.Command{
			Use:   "clear",
			Short: "Drain every queued entry without retrying (for cleanup only)",
			RunE: func(_ *cobra.Command, _ []string) error {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				q := chain.NewQueue(chain.DefaultQueuePath(cwd))
				drained, err := q.Drain("")
				if err != nil {
					return err
				}
				fmt.Printf("✓ cleared %d entries\n", len(drained))
				return nil
			},
		},
	)
	return cmd
}

// ---- helpers ----

// newChainClient loads ~/.tribunal/chain.yaml and returns a configured Client.
func newChainClient() (*chain.Client, error) {
	cfg, err := chain.LoadConfig("")
	if err != nil {
		return nil, fmt.Errorf("load chain config: %w", err)
	}
	return chain.New(cfg), nil
}

// resolvePubkeyArg accepts either a canonical "ed25519:<hex>" pubkey or a
// local agent label and returns the canonical pubkey.
func resolvePubkeyArg(s string) (string, error) {
	if isPubkeyArg(s) {
		return s, nil
	}
	reg, err := defaultRegistry()
	if err != nil {
		return "", err
	}
	a, err := reg.Get(s)
	if err != nil {
		return "", err
	}
	return a.Pubkey, nil
}

func isPubkeyArg(s string) bool {
	const prefix = agent.PubkeyPrefix
	return len(s) > len(prefix) && s[:len(prefix)] == prefix
}

// writeJSONTo pretty-prints a Go value to w as indented JSON.
func writeJSONTo(w io.Writer, v any) error {
	return newJSONEncoder(w).Encode(v)
}
