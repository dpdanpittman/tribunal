// tribunal-seed is a tiny throwaway harness for end-to-end testing of the
// chain settlement path. It seeds the local ledger with one signed finding
// and one signed resolution, then exits. After running, invoke
// `tribunal chain sync` to flush the entries on-chain.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/dpdanpittman/tribunal/internal/agent"
	"github.com/dpdanpittman/tribunal/internal/chain"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

func main() {
	advLabel := "adversary-alpha"
	pmLabel := "pm-alpha"
	planID := "P-e2e-001"

	if len(os.Args) > 1 {
		planID = os.Args[1]
	}

	root, err := agent.DefaultRoot()
	if err != nil {
		log.Fatal(err)
	}
	reg := agent.NewRegistry(root)

	advKP, err := reg.LoadKeypair(advLabel)
	if err != nil {
		log.Fatalf("load adversary key: %v", err)
	}
	pmKP, err := reg.LoadKeypair(pmLabel)
	if err != nil {
		log.Fatalf("load pm key: %v", err)
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

	// If --send is the next arg, also fire the resolution on-chain right now.
	send := false
	for _, a := range os.Args[1:] {
		if a == "--send" {
			send = true
		}
	}
	if !send {
		return
	}

	cfg, err := chain.LoadConfig("")
	if err != nil {
		log.Fatalf("load chain config: %v", err)
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
	res, err := cli.Execute(context.Background(), exec)
	if err != nil {
		log.Fatalf("execute: %v", err)
	}
	fmt.Printf("resolve_finding_batch tx: %s\n", res.TxHash)
}
