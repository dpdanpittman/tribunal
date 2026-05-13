// tribunal-batch-file ingests FINDINGS-TO-FILE blocks from reviewer/adversary
// reports under .tribunal/reports/<plan>/, signs them with the filing
// reviewer's keypair, appends to the ledger, and (optionally) writes
// PM-signed resolutions for each. Throwaway harness for the v0.3.2 audit.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dpdanpittman/tribunal/internal/agent"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

type entry struct {
	Severity   string
	Category   string
	FindingID  string
	ClaimHash  string
	ClaimURI   string
	Summary    string
	FilingAgent string
}

var lineRE = regexp.MustCompile(`^([A-Za-z]+)\|([^|]+)\|([^|]+)\|([^|]+)\|([^|]+)\|(.+)$`)

func parseReport(path, filer string) ([]entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []entry
	inBlock := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "## FINDINGS-TO-FILE") {
			inBlock = true
			continue
		}
		if !inBlock {
			continue
		}
		if strings.HasPrefix(line, "```") {
			continue
		}
		m := lineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		out = append(out, entry{
			Severity:    strings.ToLower(m[1]),
			Category:    m[2],
			FindingID:   m[3],
			ClaimHash:   m[4],
			ClaimURI:    m[5],
			Summary:     m[6],
			FilingAgent: filer,
		})
	}
	return out, sc.Err()
}

func severity(s string) ledger.Severity {
	switch strings.ToLower(s) {
	case "critical":
		return ledger.SeverityCritical
	case "warning":
		return ledger.SeverityWarning
	case "suggestion":
		return ledger.SeveritySuggestion
	}
	return ledger.SeverityWarning
}

func category(s string) ledger.Category {
	switch strings.ReplaceAll(strings.ToLower(s), "-", "_") {
	case "composition":
		return ledger.CategoryCompositionFailure
	case "refinement_mismatch":
		return ledger.CategoryRefinementMismatch
	case "edge_case":
		return ledger.CategoryEdgeCase
	case "shared_blind_spot":
		return ledger.CategorySharedBlindSpot
	case "hidden_assumption":
		return ledger.CategoryHiddenAssumption
	case "adversarial_input":
		return ledger.CategoryAdversarialInput
	}
	// Fall back to a generic category for anything outside the canonical list.
	return ledger.CategoryHiddenAssumption
}

func main() {
	var planID, pmLabel string
	var doResolve bool
	flag.StringVar(&planID, "plan", "P-v032-audit", "plan id under .tribunal/plans")
	flag.StringVar(&pmLabel, "pm", "pm-alpha", "PM agent label")
	flag.BoolVar(&doResolve, "resolve-tp", true, "also file PM-signed true_positive resolution per finding")
	flag.Parse()

	root, err := agent.DefaultRoot()
	if err != nil {
		log.Fatal(err)
	}
	reg := agent.NewRegistry(root)

	reportDir := filepath.Join(".tribunal", "reports", planID)
	mapping := map[string]string{
		"reviewer-arch.md": "reviewer-arch",
		"reviewer-sec.md":  "reviewer-sec",
		"reviewer-perf.md": "reviewer-perf",
		"adversary.md":     "adversary-alpha",
	}

	var all []entry
	for fname, filer := range mapping {
		entries, err := parseReport(filepath.Join(reportDir, fname), filer)
		if err != nil {
			log.Fatalf("parse %s: %v", fname, err)
		}
		all = append(all, entries...)
	}
	fmt.Printf("parsed %d findings across %d reports\n", len(all), len(mapping))

	lg := ledger.New(ledger.DefaultPath("."))

	pmKP, err := reg.LoadKeypair(pmLabel)
	if err != nil {
		log.Fatalf("load PM key: %v", err)
	}

	var filed, resolved int
	for _, e := range all {
		kp, err := reg.LoadKeypair(e.FilingAgent)
		if err != nil {
			log.Printf("skip %s: load %s key: %v", e.FindingID, e.FilingAgent, err)
			continue
		}
		f := ledger.NewFinding(
			e.FindingID,
			planID,
			1,
			kp,
			e.FilingAgent,
			severity(e.Severity),
			category(e.Category),
			e.ClaimHash,
			e.ClaimURI,
		)
		if err := f.Sign(kp); err != nil {
			log.Printf("sign %s: %v", e.FindingID, err)
			continue
		}
		if err := lg.AppendFinding(f); err != nil {
			log.Printf("append %s: %v", e.FindingID, err)
			continue
		}
		filed++

		if !doResolve {
			continue
		}
		r := ledger.NewResolution(
			e.FindingID,
			planID,
			ledger.OutcomeTruePositive,
			pmKP,
			pmLabel,
			"audit-fix-planned-v033",
			"file://.tribunal/reports/"+planID+"/SYNTHESIS.md",
		)
		if err := r.Sign(pmKP); err != nil {
			log.Printf("sign resolution %s: %v", e.FindingID, err)
			continue
		}
		if err := lg.AppendResolution(r); err != nil {
			log.Printf("append resolution %s: %v", e.FindingID, err)
			continue
		}
		resolved++
	}
	fmt.Printf("filed=%d resolved=%d\n", filed, resolved)
}
