package converge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dpdanpittman/tribunal/internal/dispatch"
)

// AdversaryStage abstracts the per-round work the controller dispatches.
// The production implementation in cli-wiring adapts review.Run; tests
// supply a stub so the controller's loop logic can be exercised without
// real LLM calls. Inputs include the rotated panel; outputs are the
// findings + per-member verdicts + overall verdict for one round.
type AdversaryStage interface {
	RunRound(ctx context.Context, in RoundInput) (*RoundOutput, error)
}

// RoundInput is what the controller hands the adversary stage. The
// PlanID + ProjectRoot come from ConvergenceTarget; the panel composition
// comes from the rotator.
type RoundInput struct {
	Target ConvergenceTarget
	Panel  PanelComposition
	Round  int
}

// RoundOutput is what the adversary stage gives back. The controller
// merges this into a RoundResult.
type RoundOutput struct {
	Findings       []RoundFinding
	Verdicts       map[string]string
	OverallVerdict string
	TokenCost      int
}

// Controller orchestrates the convergence loop. M1 is output-only — the
// Implementer field is reserved for M2 and ignored in v0.4.1.
type Controller struct {
	// Adversary runs one round (lens + adversary stage).
	Adversary AdversaryStage

	// Rotator selects the panel composition per round.
	Rotator PanelRotator

	// Stopping is the list of criteria AND'd together. The controller
	// also AND's a MaxRoundsCriterion derived from Budget.MaxRounds for
	// safety regardless of what the operator configured.
	Stopping []StoppingCriterion

	// Budget caps the loop on rounds / tokens / wallclock.
	Budget Budget

	// DispatchConfig is the loaded tribunal.yaml view the rotator uses
	// as its base panel pool.
	DispatchConfig dispatch.Config
}

// Run drives the convergence loop. Each round: rotate the panel, dispatch
// the adversary stage, classify findings against history, persist the
// round ledger, evaluate stopping criteria, recheck budget. Stops on:
// converged (criteria fired), needs-fixes (round produced new
// Critical/Warning findings before any stop criterion fired), budget
// exhausted, or ctx cancelled.
//
// M1 semantics: each Controller.Run invocation may run multiple rounds
// IF the rounds are clean enough that no operator fix is needed between
// them. Once a round produces unresolved Critical/Warning findings, the
// loop exits with StatusNeedsFixes so the operator can patch and re-run.
// On re-invocation, the prior rounds are loaded from disk so rotation
// stays informed by the full history.
func (c *Controller) Run(ctx context.Context, target ConvergenceTarget) (*ConvergenceResult, error) {
	if c.Adversary == nil {
		return nil, errors.New("converge: Controller.Adversary required")
	}
	if c.Rotator == nil {
		return nil, errors.New("converge: Controller.Rotator required")
	}
	if len(c.Stopping) == 0 {
		c.Stopping = []StoppingCriterion{&ConsecutiveCleanCriterion{N: 2}}
	}
	if c.Budget.MaxRounds == 0 && c.Budget.MaxTokens == 0 && c.Budget.MaxWallclock == 0 {
		c.Budget = DefaultBudget()
	}
	if target.PlanID == "" {
		return nil, errors.New("converge: ConvergenceTarget.PlanID required")
	}

	result := &ConvergenceResult{
		PlanID:    target.PlanID,
		StartedAt: time.Now(),
		Status:    StatusErrored, // overwritten before return on the happy path
	}
	defer func() {
		result.CompletedAt = time.Now()
		result.TotalDuration = result.CompletedAt.Sub(result.StartedAt)
	}()

	// Load history from disk so rotation + stopping criteria see the
	// full picture across invocations.
	history, err := LoadHistory(target.ProjectRoot, target.PlanID)
	if err != nil {
		return result, fmt.Errorf("load history: %w", err)
	}
	for _, r := range history {
		result.TotalTokenCost += r.TokenCost
	}

	tracker := NewBudgetTracker(c.Budget)
	// Pre-charge the tracker with prior round count + tokens so the
	// budget interprets MaxRounds as a cap on TOTAL rounds, not just
	// this invocation. Same for tokens.
	for _, r := range history {
		tracker.RecordRound(r.TokenCost)
	}

	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			result.Status = StatusBudgetExhausted
			result.Reason = fmt.Sprintf("ctx cancelled: %v", ctxErr)
			return result, nil
		}
		if err := tracker.CanRunAnother(); err != nil {
			result.Status = StatusBudgetExhausted
			result.Reason = err.Error()
			return result, nil
		}

		panel, err := c.Rotator.NextPanel(history, c.DispatchConfig)
		if err != nil {
			return result, fmt.Errorf("rotate panel: %w", err)
		}
		roundNum := len(history) + 1
		round := RoundResult{
			Round:     roundNum,
			StartedAt: time.Now(),
			Panel:     panel,
			Verdicts:  map[string]string{},
		}
		out, err := c.Adversary.RunRound(ctx, RoundInput{
			Target: target,
			Panel:  panel,
			Round:  roundNum,
		})
		round.CompletedAt = time.Now()
		round.Duration = round.CompletedAt.Sub(round.StartedAt)
		if err != nil {
			// Persist a stub round capturing the panel + error context
			// so the next invocation knows this round was attempted.
			round.OverallVerdict = "ERRORED"
			_, _ = SaveRound(target.ProjectRoot, target.PlanID, &round)
			return result, fmt.Errorf("round %d adversary: %w", roundNum, err)
		}

		// Classify findings as carry-forward vs novel against the full
		// historical claim_hash set.
		priorHashes := HistoricalClaimHashes(history)
		round.Findings = make([]RoundFinding, len(out.Findings))
		copy(round.Findings, out.Findings)
		for i := range round.Findings {
			h := strings.TrimSpace(round.Findings[i].ClaimHash)
			if h != "" && priorHashes[h] {
				round.Findings[i].CarryForward = true
			}
		}
		round.Verdicts = out.Verdicts
		round.OverallVerdict = out.OverallVerdict
		round.TokenCost = out.TokenCost
		tracker.RecordRound(out.TokenCost)
		result.TotalTokenCost += out.TokenCost

		// Stopping criteria are evaluated against history WITH the
		// just-completed round included. Append, evaluate, then patch
		// the appended copy with the stop verdict so persistence sees
		// the same view the caller does.
		history = append(history, round)
		stoppedAll := true
		reasons := []string{}
		var firstCriterion string
		for _, sc := range c.Stopping {
			stop, reason := sc.ShouldStop(history)
			if !stop {
				stoppedAll = false
				break
			}
			if firstCriterion == "" {
				firstCriterion = sc.Name()
			}
			reasons = append(reasons, sc.Name()+": "+reason)
		}
		if stoppedAll {
			history[len(history)-1].Stopped = true
			history[len(history)-1].StopReason = strings.Join(reasons, "; ")
			history[len(history)-1].StopCriterion = firstCriterion
		}

		final := history[len(history)-1]
		if _, err := SaveRound(target.ProjectRoot, target.PlanID, &final); err != nil {
			return result, fmt.Errorf("save round %d: %w", roundNum, err)
		}
		result.Rounds = append(result.Rounds, final)

		if stoppedAll {
			result.Status = StatusConverged
			result.Reason = final.StopReason
			return result, nil
		}

		// Otherwise: did this round produce unresolved Critical/Warning
		// findings? If so, the loop pauses for operator action.
		if needsFixes(final) {
			result.Status = StatusNeedsFixes
			result.Reason = fmt.Sprintf("round %d produced %d critical + %d warning finding(s); operator action required",
				roundNum, countSeverity(final, "critical"), countSeverity(final, "warning"))
			return result, nil
		}
	}
}

// needsFixes returns true when the round contains at least one
// Critical or Warning finding. Suggestion-only rounds keep the loop
// running (suggestions don't gate release).
func needsFixes(r RoundResult) bool {
	for _, f := range r.Findings {
		s := strings.ToLower(f.Severity)
		if s == "critical" || s == "warning" {
			return true
		}
	}
	return false
}

func countSeverity(r RoundResult, want string) int {
	n := 0
	for _, f := range r.Findings {
		if strings.EqualFold(f.Severity, want) {
			n++
		}
	}
	return n
}
