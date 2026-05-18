package converge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// LedgerDir returns the on-disk directory the convergence loop writes
// round-N.json files to: <projectRoot>/.tribunal/convergence/<planID>/.
// The directory is created lazily on first write.
func LedgerDir(projectRoot, planID string) string {
	return filepath.Join(projectRoot, ".tribunal", "convergence", planID)
}

// roundFilenameRE is the format LedgerDir writes: round-<N>.json with N
// 1-indexed and zero-padded for stable lexical ordering when scanning.
var roundFilenameRE = regexp.MustCompile(`^round-(\d{4})\.json$`)

// SaveRound writes one round's result to <ledger-dir>/round-<NNNN>.json.
// Idempotent: overwrites if the same round number already exists.
func SaveRound(projectRoot, planID string, r *RoundResult) (string, error) {
	if r == nil {
		return "", errors.New("converge: SaveRound nil result")
	}
	if r.Round < 1 {
		return "", fmt.Errorf("converge: round must be 1-indexed (got %d)", r.Round)
	}
	dir := LedgerDir(projectRoot, planID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("converge: mkdir %s: %w", dir, err)
	}
	name := fmt.Sprintf("round-%04d.json", r.Round)
	path := filepath.Join(dir, name)
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", fmt.Errorf("converge: marshal round %d: %w", r.Round, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("converge: write %s: %w", path, err)
	}
	return path, nil
}

// LoadHistory reads every round-NNNN.json under the plan's ledger dir
// and returns them in ascending Round order. A missing directory is not
// an error — it returns an empty slice (first invocation case).
func LoadHistory(projectRoot, planID string) ([]RoundResult, error) {
	dir := LedgerDir(projectRoot, planID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("converge: read %s: %w", dir, err)
	}
	var rounds []RoundResult
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := roundFilenameRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		num, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("converge: read %s: %w", e.Name(), err)
		}
		var rr RoundResult
		if err := json.Unmarshal(raw, &rr); err != nil {
			return nil, fmt.Errorf("converge: parse %s: %w", e.Name(), err)
		}
		// Trust the filename over the field — guards against operators
		// hand-editing the JSON.
		rr.Round = num
		rounds = append(rounds, rr)
	}
	sort.Slice(rounds, func(i, j int) bool { return rounds[i].Round < rounds[j].Round })
	return rounds, nil
}

// HistoricalClaimHashes returns the set of every claim_hash filed across
// the loaded history. Stopping criteria use this to classify "novel" vs
// "carry-forward" findings in the current round.
func HistoricalClaimHashes(history []RoundResult) map[string]bool {
	seen := map[string]bool{}
	for _, r := range history {
		for _, f := range r.Findings {
			h := strings.TrimSpace(f.ClaimHash)
			if h != "" {
				seen[h] = true
			}
		}
	}
	return seen
}
