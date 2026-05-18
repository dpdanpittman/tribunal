// tribunal-seed is a tiny throwaway harness for end-to-end testing of the
// chain settlement path. It seeds the local ledger with one signed finding
// and one signed resolution, then exits. After running, invoke
// `tribunal chain sync` to flush the entries on-chain.
//
// Refuses to run against a chain whose chain_id doesn't look like a
// dev/test environment unless --allow-prod is passed. Catches the failure
// mode where chain.yaml accidentally points at mainnet and the harness
// signs a fake true_positive resolution against a real reputation balance.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/dpdanpittman/tribunal/internal/agent"
	"github.com/dpdanpittman/tribunal/internal/chain"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

func main() {
	var planID, advLabel, pmLabel string
	var send, allowProd bool
	var execTimeout time.Duration

	flag.StringVar(&planID, "plan", "P-e2e-001", "plan id to use for the seeded finding + resolution")
	flag.StringVar(&advLabel, "adversary", "adversary-alpha", "agent label of the filer (must exist in ~/.tribunal/agents)")
	flag.StringVar(&pmLabel, "pm", "pm-alpha", "agent label of the resolver (must exist in ~/.tribunal/agents)")
	flag.BoolVar(&send, "send", false, "after writing to the local ledger, also fire the resolution on-chain via the configured chain client")
	flag.BoolVar(&allowProd, "allow-prod", false, "permit --send against a non-test-looking chain_id. Default rejects mainnet-like configs to prevent accidentally signing a fake TP against real reputation stake.")
	flag.DurationVar(&execTimeout, "timeout", 60*time.Second, "ctx timeout for the on-chain Execute when --send is set")
	flag.Parse()

	root, err := agent.DefaultRoot()
	if err != nil {
		log.Fatal(err)
	}
	reg := agent.NewRegistry(root)

	advKP, err := reg.LoadKeypair(advLabel)
	if err != nil {
		log.Fatalf("load adversary key %q: %v", advLabel, err)
	}
	pmKP, err := reg.LoadKeypair(pmLabel)
	if err != nil {
		log.Fatalf("load pm key %q: %v", pmLabel, err)
	}

	lg := ledger.New(ledger.DefaultPath("."))

	f := ledger.NewFinding(
		"F-e2e-001",
		planID,
		1,
		advKP,
		advLabel,
		ledger.SeverityCritical,
		ledger.CategoryEdgeCase,
		"hashclaim-e2e",
		"file://e2e-test-claim",
	)
	if err := f.Sign(advKP); err != nil {
		log.Fatalf("sign finding: %v", err)
	}
	if err := lg.AppendFinding(f); err != nil {
		log.Fatalf("append finding: %v", err)
	}
	fmt.Printf("appended finding %s (plan=%s severity=%s)\n", f.FindingID, planID, f.Severity)

	r := ledger.NewResolution(
		"F-e2e-001",
		planID,
		ledger.OutcomeTruePositive,
		pmKP,
		pmLabel,
		"the-bug-was-real",
		"file://e2e-test-evidence",
	)
	if err := r.Sign(pmKP); err != nil {
		log.Fatalf("sign resolution: %v", err)
	}
	if err := lg.AppendResolution(r); err != nil {
		log.Fatalf("append resolution: %v", err)
	}
	fmt.Printf("appended resolution for %s outcome=%s\n", r.FindingID, r.Outcome)

	if !send {
		return
	}

	cfg, err := chain.LoadConfig("")
	if err != nil {
		log.Fatalf("load chain config: %v", err)
	}

	if !chain.LooksLikeTestChain(cfg.ChainID) && !allowProd {
		log.Fatalf("tribunal-seed refusing to --send against chain_id=%q (looks like production, or contains non-ASCII bytes). Pass --allow-prod to override, or point ~/.tribunal/chain.yaml at a devnet/testnet first.", cfg.ChainID)
	}

	cli := chain.New(cfg)
	rc, err := chain.BuildResolutionCommit(r, pmKP)
	if err != nil {
		log.Fatalf("build resolution commit: %v", err)
	}
	exec := &chain.ExecuteMsg{ResolveFindingBatch: &chain.ResolveBatchMsg{
		PlanID:      planID,
		Resolutions: []chain.ResolutionCommit{*rc},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	res, err := cli.Execute(ctx, exec)
	if err != nil {
		log.Fatalf("execute: %v", err)
	}
	fmt.Printf("resolve_finding_batch tx: %s\n", res.TxHash)
}

// v0.3.5: looksLikeTestChain was duplicated here and in internal/chain.
// F-OPUS-004 surfaced that the duplicate had to be fixed twice to close
// the Unicode bypass; consolidated to chain.LooksLikeTestChain.
