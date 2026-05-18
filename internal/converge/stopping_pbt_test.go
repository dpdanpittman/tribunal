package converge

import (
	"testing"

	"pgregory.net/rapid"
)

// TestPBT_ConsecutiveCleanCriterion_Iff — the stopping criterion fires
// if-and-only-if the last N rounds in history are all clean (no
// critical, no warning). The property runs across random histories of
// random length with random severity assignments per round and pins
// both directions of the biconditional.
//
// rapid will shrink any counterexample to a minimal failing sequence
// (e.g. exactly the boundary case that breaks the implementation).
func TestPBT_ConsecutiveCleanCriterion_Iff(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(t, "N")
		c := &ConsecutiveCleanCriterion{N: n}

		// Draw a history. Each round either has no findings, or has
		// findings drawn from {suggestion, warning, critical} — the
		// criterion only cares about warning/critical.
		histLen := rapid.IntRange(0, 12).Draw(t, "histLen")
		history := make([]RoundResult, histLen)
		for i := range history {
			numFindings := rapid.IntRange(0, 4).Draw(t, "numFindings")
			findings := make([]RoundFinding, numFindings)
			for j := range findings {
				findings[j] = RoundFinding{
					Severity: rapid.SampledFrom([]string{"suggestion", "warning", "critical"}).Draw(t, "sev"),
				}
			}
			history[i] = RoundResult{Round: i + 1, Findings: findings}
		}

		gotStop, _ := c.ShouldStop(history)
		wantStop := allCleanTail(history, n)

		if gotStop != wantStop {
			t.Fatalf("ShouldStop=%v, want %v (N=%d, histLen=%d)\n  history severities: %v",
				gotStop, wantStop, n, histLen, severitiesPerRound(history))
		}
	})
}

// TestPBT_MaxRoundsCriterion_FiresAtThreshold — fires iff history
// length ≥ N. Simple but worth pinning because it's the OR-safety the
// controller wires regardless of operator config.
func TestPBT_MaxRoundsCriterion_FiresAtThreshold(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 20).Draw(t, "N")
		histLen := rapid.IntRange(0, 30).Draw(t, "histLen")
		history := make([]RoundResult, histLen)
		for i := range history {
			history[i] = RoundResult{Round: i + 1}
		}
		c := &MaxRoundsCriterion{N: n}
		gotStop, _ := c.ShouldStop(history)
		wantStop := histLen >= n
		if gotStop != wantStop {
			t.Fatalf("MaxRoundsCriterion(%d).ShouldStop with histLen=%d: got %v want %v",
				n, histLen, gotStop, wantStop)
		}
	})
}

// TestPBT_NoNovelFindings_OnlyWhenAllCarryForward — fires iff the
// current round is non-empty AND every claim_hash in it appears in an
// earlier round. Property explicitly excludes the trivially-empty case
// because the criterion documentation says ConsecutiveClean is the
// better signal there.
func TestPBT_NoNovelFindings_OnlyWhenAllCarryForward(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Build a pool of claim_hashes; the random history draws from
		// this pool plus optionally novel hashes.
		pool := []string{"h1", "h2", "h3", "h4"}
		histLen := rapid.IntRange(1, 8).Draw(t, "histLen")
		history := make([]RoundResult, histLen)
		for i := range history {
			numFindings := rapid.IntRange(0, 3).Draw(t, "numFindings")
			findings := make([]RoundFinding, numFindings)
			for j := range findings {
				// 70% pool, 30% novel-this-round (deterministic per draw)
				usePool := rapid.Bool().Draw(t, "usePool")
				if usePool {
					findings[j].ClaimHash = rapid.SampledFrom(pool).Draw(t, "hash")
				} else {
					findings[j].ClaimHash = "novel-" + rapid.StringMatching("[a-z]{4,8}").Draw(t, "novelHash")
				}
				findings[j].Severity = "suggestion"
			}
			history[i] = RoundResult{Round: i + 1, Findings: findings}
		}

		c := &NoNovelFindingsCriterion{}
		gotStop, _ := c.ShouldStop(history)

		// Independently compute the expected value.
		current := history[len(history)-1]
		priorHashes := HistoricalClaimHashes(history[:len(history)-1])
		wantStop := len(current.Findings) > 0
		if wantStop {
			for _, f := range current.Findings {
				if f.ClaimHash == "" || !priorHashes[f.ClaimHash] {
					wantStop = false
					break
				}
			}
		}

		if gotStop != wantStop {
			t.Fatalf("NoNovelFindings.ShouldStop=%v, want %v\n  history hashes: %v",
				gotStop, wantStop, hashesPerRound(history))
		}
	})
}

// allCleanTail is the reference implementation of the
// ConsecutiveCleanCriterion logic — used as the oracle for PBT.
func allCleanTail(history []RoundResult, n int) bool {
	if n <= 0 || len(history) < n {
		return false
	}
	for _, r := range history[len(history)-n:] {
		for _, f := range r.Findings {
			if f.Severity == "warning" || f.Severity == "critical" {
				return false
			}
		}
	}
	return true
}

func severitiesPerRound(h []RoundResult) [][]string {
	out := make([][]string, len(h))
	for i, r := range h {
		ss := make([]string, len(r.Findings))
		for j, f := range r.Findings {
			ss[j] = f.Severity
		}
		out[i] = ss
	}
	return out
}

func hashesPerRound(h []RoundResult) [][]string {
	out := make([][]string, len(h))
	for i, r := range h {
		hs := make([]string, len(r.Findings))
		for j, f := range r.Findings {
			hs[j] = f.ClaimHash
		}
		out[i] = hs
	}
	return out
}
