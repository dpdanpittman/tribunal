package ledger

// GateConfig holds the score thresholds that drive Tribunal's reputation
// gates. Defaults match the methodology doc; projects override via
// tribunal.yaml.
type GateConfig struct {
	RHigh  float64 // auto-elevate above this
	RLow   float64 // require corroboration below this
	RFloor float64 // rotate out below this
}

// DefaultGateConfig returns the methodology's documented gate thresholds.
func DefaultGateConfig() GateConfig {
	return GateConfig{
		RHigh:  50,
		RLow:   0,
		RFloor: -10,
	}
}

// GateDecision is the recommendation the reputation system makes for a
// finding given the filing agent's current score.
type GateDecision int

const (
	// GateAutoElevate: filing agent has a strong track record; treat this
	// finding's severity as one tier higher for triage priority.
	GateAutoElevate GateDecision = iota
	// GateNormal: handle the finding at its filed severity.
	GateNormal
	// GateRequireCorroboration: filing agent's track record is weak;
	// require a second-agent corroborating finding in the same round
	// before actioning.
	GateRequireCorroboration
	// GateRotateOut: filing agent's track record is so weak the agent
	// should be removed from the next round's pool. The finding itself
	// is still recorded; it just doesn't enter the action queue.
	GateRotateOut
)

// String returns the conventional name of the decision for logging and CLI
// output.
func (g GateDecision) String() string {
	switch g {
	case GateAutoElevate:
		return "auto_elevate"
	case GateNormal:
		return "normal"
	case GateRequireCorroboration:
		return "require_corroboration"
	case GateRotateOut:
		return "rotate_out"
	}
	return "unknown"
}

// Decide maps a reputation score to a gate decision per the configured
// thresholds.
func (c GateConfig) Decide(score float64) GateDecision {
	switch {
	case score < c.RFloor:
		return GateRotateOut
	case score < c.RLow:
		return GateRequireCorroboration
	case score >= c.RHigh:
		return GateAutoElevate
	default:
		return GateNormal
	}
}

// DecideForAgent looks up an agent's reputation in the slice and returns
// the gate decision. If the agent isn't in the slice, the decision is
// GateRequireCorroboration — new agents start without track record.
func (c GateConfig) DecideForAgent(reps []*Reputation, agentPubkey string) GateDecision {
	for _, r := range reps {
		if r.AgentPubkey == agentPubkey {
			return c.Decide(r.Score)
		}
	}
	return GateRequireCorroboration
}
