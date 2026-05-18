package converge

import (
	"fmt"
	"time"
)

// Budget bounds the convergence loop on three dimensions independently:
// rounds, tokens, wallclock. Each exhaustion produces a distinct reason
// string so operators can tell at a glance which axis ran out.
//
// Zero on any axis means "unbounded on that axis" (operator opted out).
// MaxWallclock zero leaves only the outer ctx as the wallclock bound.
type Budget struct {
	MaxRounds    int
	MaxTokens    int
	MaxWallclock time.Duration
}

// DefaultBudget returns the v0.4.1 documented defaults: 5 rounds,
// 200_000 tokens, 30 minutes wallclock. See ADR-0001.
func DefaultBudget() Budget {
	return Budget{
		MaxRounds:    5,
		MaxTokens:    200_000,
		MaxWallclock: 30 * time.Minute,
	}
}

// BudgetTracker is the running counter the controller consults before
// each round and updates after. Methods are not safe for concurrent use —
// the controller is single-threaded, one round at a time.
type BudgetTracker struct {
	cfg       Budget
	startedAt time.Time
	rounds    int
	tokens    int
}

// NewBudgetTracker starts a tracker at the current time with zero usage.
func NewBudgetTracker(cfg Budget) *BudgetTracker {
	return &BudgetTracker{cfg: cfg, startedAt: time.Now()}
}

// Config returns the configured limits (read-only).
func (b *BudgetTracker) Config() Budget { return b.cfg }

// Rounds returns the number of rounds completed so far.
func (b *BudgetTracker) Rounds() int { return b.rounds }

// Tokens returns the cumulative token cost across all rounds.
func (b *BudgetTracker) Tokens() int { return b.tokens }

// Elapsed returns the wallclock since the tracker was created.
func (b *BudgetTracker) Elapsed() time.Duration { return time.Since(b.startedAt) }

// CanRunAnother returns nil if a new round is allowed, or an error
// naming the exhausted axis. Called before dispatching round N+1.
func (b *BudgetTracker) CanRunAnother() error {
	if b.cfg.MaxRounds > 0 && b.rounds >= b.cfg.MaxRounds {
		return fmt.Errorf("max-rounds exhausted (%d/%d)", b.rounds, b.cfg.MaxRounds)
	}
	if b.cfg.MaxTokens > 0 && b.tokens >= b.cfg.MaxTokens {
		return fmt.Errorf("max-tokens exhausted (%d/%d)", b.tokens, b.cfg.MaxTokens)
	}
	if b.cfg.MaxWallclock > 0 && b.Elapsed() >= b.cfg.MaxWallclock {
		return fmt.Errorf("max-wallclock exhausted (%s/%s)", b.Elapsed().Round(time.Second), b.cfg.MaxWallclock)
	}
	return nil
}

// RecordRound bumps the round count and adds tokens to the cumulative
// total. Called after a round completes.
func (b *BudgetTracker) RecordRound(tokens int) {
	b.rounds++
	if tokens > 0 {
		b.tokens += tokens
	}
}
