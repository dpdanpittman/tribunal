package converge

import (
	"fmt"
	"strconv"
	"strings"
)

// StoppingCriterion decides whether the convergence loop should halt
// after a given round. Multiple criteria can be configured and AND
// together — the loop stops when ALL configured criteria fire on the
// same round. (The CLI also wires a max-rounds escape valve as an OR-
// safety regardless of operator config.)
//
// ShouldStop is called AFTER each round, with the full history (including
// the just-completed round) so criteria can reason about consecutive
// state. Returns (stop, reason) where reason is a short human-readable
// string for the ConvergenceResult.
type StoppingCriterion interface {
	ShouldStop(history []RoundResult) (stop bool, reason string)
	Name() string
}

// ConsecutiveCleanCriterion fires when the last N rounds are all clean —
// zero Critical and zero unresolved Warning findings.
type ConsecutiveCleanCriterion struct {
	N int
}

func (c *ConsecutiveCleanCriterion) Name() string {
	return fmt.Sprintf("consecutive-clean(%d)", c.N)
}

func (c *ConsecutiveCleanCriterion) ShouldStop(history []RoundResult) (bool, string) {
	if c.N <= 0 || len(history) < c.N {
		return false, ""
	}
	tail := history[len(history)-c.N:]
	for _, r := range tail {
		for _, f := range r.Findings {
			sev := strings.ToLower(f.Severity)
			if sev == "critical" || sev == "warning" {
				return false, ""
			}
		}
	}
	return true, fmt.Sprintf("last %d round(s) zero critical + zero warning", c.N)
}

// NoNovelFindingsCriterion fires when EVERY finding in the just-completed
// round has appeared in an earlier round under the same claim_hash.
// Indicates the methodology has explored the artifact's surface — new
// rounds will only re-discover known issues.
type NoNovelFindingsCriterion struct{}

func (c *NoNovelFindingsCriterion) Name() string { return "no-novel-findings" }

func (c *NoNovelFindingsCriterion) ShouldStop(history []RoundResult) (bool, string) {
	if len(history) == 0 {
		return false, ""
	}
	current := history[len(history)-1]
	if len(current.Findings) == 0 {
		// No findings at all is "no novel" trivially — but
		// ConsecutiveClean is the better signal for that case. We only
		// fire when there ARE findings, all classified as carry-forwards.
		return false, ""
	}
	prior := history[:len(history)-1]
	priorHashes := HistoricalClaimHashes(prior)
	for _, f := range current.Findings {
		h := strings.TrimSpace(f.ClaimHash)
		if h == "" {
			// Findings without a stable claim_hash can't be classified
			// as carry-forwards safely; treat as novel.
			return false, ""
		}
		if !priorHashes[h] {
			return false, ""
		}
	}
	return true, fmt.Sprintf("round %d findings are all carry-forwards", current.Round)
}

// MaxRoundsCriterion is the escape valve — fires unconditionally after
// N rounds. Always wired alongside the others so a non-converging loop
// doesn't run forever.
type MaxRoundsCriterion struct {
	N int
}

func (c *MaxRoundsCriterion) Name() string { return fmt.Sprintf("max-rounds(%d)", c.N) }

func (c *MaxRoundsCriterion) ShouldStop(history []RoundResult) (bool, string) {
	if c.N <= 0 {
		return false, ""
	}
	if len(history) >= c.N {
		return true, fmt.Sprintf("max-rounds cap reached (%d rounds)", c.N)
	}
	return false, ""
}

// ParseStoppingCriteria parses the --stop-on CLI flag into a slice of
// criteria. The format is comma-separated names with optional integer
// args in parens: `consecutive-clean(2),no-novel-findings`.
//
// Known names (M1):
//   - consecutive-clean(N)
//   - no-novel-findings
//   - max-rounds(N)
//
// Empty spec → consecutive-clean(2) (the v0.4.1 default).
func ParseStoppingCriteria(spec string) ([]StoppingCriterion, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return []StoppingCriterion{&ConsecutiveCleanCriterion{N: 2}}, nil
	}
	parts := strings.Split(spec, ",")
	out := make([]StoppingCriterion, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		c, err := parseOneStoppingCriterion(p)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("converge: --stop-on parsed to empty list")
	}
	return out, nil
}

func parseOneStoppingCriterion(s string) (StoppingCriterion, error) {
	name, arg := splitNameArg(s)
	switch name {
	case "consecutive-clean":
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("converge: consecutive-clean needs a positive integer arg (got %q)", arg)
		}
		return &ConsecutiveCleanCriterion{N: n}, nil
	case "no-novel-findings":
		if arg != "" {
			return nil, fmt.Errorf("converge: no-novel-findings takes no arg (got %q)", arg)
		}
		return &NoNovelFindingsCriterion{}, nil
	case "max-rounds":
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("converge: max-rounds needs a positive integer arg (got %q)", arg)
		}
		return &MaxRoundsCriterion{N: n}, nil
	default:
		return nil, fmt.Errorf("converge: unknown stopping criterion %q (known: consecutive-clean(N), no-novel-findings, max-rounds(N))", name)
	}
}

// splitNameArg splits "consecutive-clean(2)" into ("consecutive-clean", "2"),
// or "no-novel-findings" into ("no-novel-findings", "").
func splitNameArg(s string) (string, string) {
	open := strings.IndexByte(s, '(')
	if open < 0 {
		return s, ""
	}
	close := strings.LastIndexByte(s, ')')
	if close < 0 || close < open {
		return s, ""
	}
	return s[:open], s[open+1 : close]
}
