package ledger

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Reputation captures the current rolling-window score for a single agent
// plus the inputs that produced it. Returned by ComputeReputation; useful
// for both the gate logic and the user-facing leaderboard.
type Reputation struct {
	AgentPubkey string
	AgentLabel  string
	Score       float64
	TPCount     int
	FPCount     int
	Stale       int
	Indet       int
	Findings    int // total findings filed in the window, regardless of outcome
}

// ReputationConfig is the configurable side of the reputation calculation.
// Defaults mirror the methodology doc; tests and tribunal.yaml may
// override.
type ReputationConfig struct {
	// HalfLife is the time over which a finding's contribution decays by
	// 50%. Default: 30 days.
	HalfLife time.Duration
	// Window is the lookback window. Findings older than Window are
	// excluded entirely from the score (cheaper than decaying to zero).
	// Default: 90 days.
	Window time.Duration
	// FamilyDiversityBonus is the multiplier applied to a unique
	// true-positive finding from a model family that's currently
	// underrepresented in the round. Default: 1.5.
	FamilyDiversityBonus float64
}

// DefaultReputationConfig returns the methodology's documented defaults.
func DefaultReputationConfig() ReputationConfig {
	return ReputationConfig{
		HalfLife:             30 * 24 * time.Hour,
		Window:               90 * 24 * time.Hour,
		FamilyDiversityBonus: 1.5,
	}
}

// ComputeReputation iterates the ledger and produces per-agent scores.
// The `now` parameter is the reference time; pass time.Now() in
// production. Tests pass a fixed time for determinism.
func ComputeReputation(findings []*Finding, resolutions []*Resolution, cfg ReputationConfig, now time.Time) []*Reputation {
	// Index resolutions by finding_id (each finding can have at most one
	// resolution; later writes are ignored).
	res := make(map[string]*Resolution, len(resolutions))
	for _, r := range resolutions {
		if _, present := res[r.FindingID]; !present {
			res[r.FindingID] = r
		}
	}

	// Bucket findings by agent.
	type bucket struct {
		label    string
		score    float64
		tp, fp   int
		stale    int
		indet    int
		findings int
	}
	per := make(map[string]*bucket)

	for _, f := range findings {
		// Apply window first; older findings drop entirely.
		age := now.Sub(f.Timestamp)
		if age > cfg.Window {
			continue
		}

		b, ok := per[f.AgentPubkey]
		if !ok {
			b = &bucket{label: f.AgentLabel}
			per[f.AgentPubkey] = b
		}
		b.findings++

		r := res[f.FindingID]
		if r == nil {
			// Not yet resolved; no score contribution yet.
			continue
		}

		decay := decayFactor(age, cfg.HalfLife)
		w := f.Severity.Weight()

		switch r.Outcome {
		case OutcomeTruePositive:
			b.tp++
			b.score += w * decay
		case OutcomeFalsePositive:
			b.fp++
			b.score -= w * decay
		case OutcomeStaleDuplicate:
			b.stale++
		case OutcomeIndeterminate:
			b.indet++
		}
	}

	out := make([]*Reputation, 0, len(per))
	for pub, b := range per {
		out = append(out, &Reputation{
			AgentPubkey: pub,
			AgentLabel:  b.label,
			Score:       roundTo(b.score, 2),
			TPCount:     b.tp,
			FPCount:     b.fp,
			Stale:       b.stale,
			Indet:       b.indet,
			Findings:    b.findings,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].AgentLabel < out[j].AgentLabel
	})
	return out
}

// decayFactor returns exp(-age * ln2 / halfLife). Used to weight older
// findings less than recent ones.
func decayFactor(age, halfLife time.Duration) float64 {
	if halfLife <= 0 {
		return 1
	}
	return math.Exp(-float64(age) * math.Ln2 / float64(halfLife))
}

// roundTo rounds f to `places` decimal places, for display stability.
func roundTo(f float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(f*p) / p
}

// ModelFamily maps a model_id (or agent label) to a coarse family bucket.
// Used by the family-diversity bonus and reporting.
func ModelFamily(modelID string) string {
	m := strings.ToLower(modelID)
	switch {
	case strings.HasPrefix(m, "claude"):
		return "anthropic"
	case strings.HasPrefix(m, "gpt") || strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4") || strings.HasPrefix(m, "o5"):
		return "openai"
	case strings.HasPrefix(m, "gemini"):
		return "google"
	case strings.HasPrefix(m, "qwen") || strings.HasPrefix(m, "mistral") || strings.HasPrefix(m, "llama") || strings.HasPrefix(m, "phi") || strings.HasPrefix(m, "deepseek"):
		return "local"
	default:
		return "other"
	}
}
