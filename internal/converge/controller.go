package converge

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// Controller orchestrates the convergence loop. M1 is output-only;
// v0.4.2 (M2) adds the Implementer hook — when a round produces
// unresolved Critical/Warning findings AND Implementer is non-nil, the
// controller asks the implementer for a patch, persists it under the
// convergence directory, and (when AutoApply is true) calls
// ApplyPatch to apply it via `git apply`. The controller still exits
// with StatusNeedsFixes after either action so the operator drives
// validation + commit + re-invocation; M3 will close the loop within
// a single invocation.
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

	// Implementer authors a patch when a round produces unresolved
	// Critical/Warning findings. Nil → M1 output-only behavior (the loop
	// exits with NeedsFixes and the operator authors the patch manually).
	Implementer Implementer

	// AutoApply controls whether the controller invokes ApplyPatch on
	// the implementer's output. Default false (M2 default: present
	// patch on disk, exit with NeedsFixes). Set true via --auto-apply
	// to also run `git apply` against the working tree.
	AutoApply bool

	// AutoContinue (M3) extends AutoApply: after a patch lands, the
	// controller runs VerifyGate.Verify; if it passes, the loop
	// continues to the next round in the same invocation. If it
	// fails, the controller calls RevertWorkingTree and exits
	// StatusNeedsFixes with the verify summary. Requires both
	// AutoApply=true and VerifyGate non-nil — the CLI enforces both.
	AutoContinue bool

	// VerifyGate is the "did the patch break the build/tests" check
	// the M3 path consults after each implementer apply. Nil disables
	// the gate (AutoContinue is then a no-op).
	VerifyGate VerifyGate

	// FindingBodyLookup is an optional hook the CLI wires to resolve
	// finding.ClaimURI → file body so the implementer prompt can
	// include the full per-finding markdown. Nil → empty bodies.
	FindingBodyLookup func(findings []RoundFinding) map[string]string

	// IntentLoader is an optional hook the CLI wires to load the plan's
	// intent.md body for the implementer prompt. Nil → empty intent.
	IntentLoader func(planID string) string

	// DiffLoader is an optional hook the CLI wires to expand DiffSpec
	// into the actual diff text. Nil → empty diff.
	DiffLoader func(target ConvergenceTarget) string

	// Reputation is the implementer-feedback sink. When non-nil, the
	// controller calls it after every invokeImplementer with a
	// structured ImplementerOutcome; the sink decides what (if anything)
	// to record in the reputation ledger. v0.4.5 ships a LedgerReputationSink
	// in cmd/tribunal that writes synthetic Findings + auto-Resolutions.
	Reputation ReputationSink
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
		// findings? If so, the loop pauses for operator action. If an
		// Implementer is configured (M2), invoke it to author a patch
		// before exiting — operators get a tangible artifact to review
		// or apply instead of just a finding list. If M3 is enabled
		// (AutoApply + AutoContinue + VerifyGate), the controller may
		// continue the loop after a verified patch instead of pausing.
		if needsFixes(final) {
			if c.Implementer != nil {
				c.invokeImplementer(ctx, &final, target)
				history[len(history)-1] = final
				_, _ = SaveRound(target.ProjectRoot, target.PlanID, &final)
				result.Rounds[len(result.Rounds)-1] = final
			}

			// M3 auto-continue: the patch applied cleanly; run the verify
			// gate. Pass → continue loop. Fail → revert + exit.
			if c.AutoApply && c.AutoContinue && c.VerifyGate != nil && final.PatchApplied {
				vr, err := c.VerifyGate.Verify(ctx, target.ProjectRoot)
				final.VerifyRan = true
				if err != nil {
					// Gate itself errored — revert and exit so operator
					// debugs the gate (not a verify failure per se).
					_ = RevertWorkingTree(ctx, target.ProjectRoot)
					final.Reverted = true
					final.VerifySummary = "gate error: " + err.Error()
					c.emitImplementerOutcome(ctx, &final, target)
					history[len(history)-1] = final
					_, _ = SaveRound(target.ProjectRoot, target.PlanID, &final)
					result.Rounds[len(result.Rounds)-1] = final
					result.Status = StatusNeedsFixes
					result.Reason = fmt.Sprintf("round %d: verify gate errored (working tree reverted): %v", roundNum, err)
					return result, nil
				}
				final.VerifyPassed = vr.Passed
				final.VerifySummary = vr.Summary
				if vr.Passed {
					c.emitImplementerOutcome(ctx, &final, target)
					history[len(history)-1] = final
					_, _ = SaveRound(target.ProjectRoot, target.PlanID, &final)
					result.Rounds[len(result.Rounds)-1] = final
					// Continue the loop — next iteration will dispatch the
					// next round against the post-patch working tree.
					continue
				}
				// Verify failed — revert and pause.
				_ = RevertWorkingTree(ctx, target.ProjectRoot)
				final.Reverted = true
				c.emitImplementerOutcome(ctx, &final, target)
				history[len(history)-1] = final
				_, _ = SaveRound(target.ProjectRoot, target.PlanID, &final)
				result.Rounds[len(result.Rounds)-1] = final
				result.Status = StatusNeedsFixes
				result.Reason = fmt.Sprintf("round %d: verify gate failed (working tree reverted) — %s", roundNum, vr.Summary)
				return result, nil
			}

			// M1/M2 path: emit the outcome before exiting (no verify,
			// settlement awaits manual operator action).
			c.emitImplementerOutcome(ctx, &final, target)
			history[len(history)-1] = final
			_, _ = SaveRound(target.ProjectRoot, target.PlanID, &final)
			result.Rounds[len(result.Rounds)-1] = final
			result.Status = StatusNeedsFixes
			result.Reason = fmt.Sprintf("round %d produced %d critical + %d warning finding(s); operator action required",
				roundNum, countSeverity(final, "critical"), countSeverity(final, "warning"))
			if final.PatchAuthored {
				result.Reason += fmt.Sprintf(" — implementer patch at %s%s",
					final.PatchPath, applyTag(final))
			}
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

// emitImplementerOutcome builds an ImplementerOutcome from the round
// state and dispatches it to the configured ReputationSink. No-op when
// the sink is nil or the round didn't author a patch (refused / not
// invoked). Errors are recorded on the round via PatchError but don't
// halt the loop — reputation feedback is best-effort.
func (c *Controller) emitImplementerOutcome(ctx context.Context, round *RoundResult, target ConvergenceTarget) {
	if c.Reputation == nil {
		return
	}
	// Nothing happened that's worth recording — implementer wasn't
	// invoked, or it refused to author a patch.
	if !round.PatchAuthored && !round.PatchRefused && round.PatchError == "" {
		return
	}
	if round.PatchRefused {
		// Refusal isn't a reputation event; the implementer chose not
		// to act. Operator sees the readme on disk.
		return
	}
	severities := make([]string, 0, len(round.Findings))
	for _, f := range round.Findings {
		s := strings.ToLower(f.Severity)
		if s == "critical" || s == "warning" {
			severities = append(severities, s)
		}
	}
	outcome := ImplementerOutcome{
		PlanID:           target.PlanID,
		Round:            round.Round,
		ImplementerLabel: c.Implementer.Label(),
		PatchHash:        patchHash(round.PatchPath),
		Severities:       severities,
		Refused:          round.PatchRefused,
		Applied:          round.PatchApplied,
		VerifyRan:        round.VerifyRan,
		VerifyPassed:     round.VerifyPassed,
		VerifySummary:    round.VerifySummary,
		PatchError:       round.PatchError,
	}
	if err := c.Reputation.RecordImplementerOutcome(ctx, outcome); err != nil {
		// Append to PatchError but don't overwrite — the apply/verify
		// error is more useful to the operator than the sink error.
		if round.PatchError == "" {
			round.PatchError = "reputation sink: " + err.Error()
		} else {
			round.PatchError = round.PatchError + " (reputation sink: " + err.Error() + ")"
		}
	}
}

// patchHash returns sha256 of the file at patchPath as
// "sha256:<64-hex>", or empty when the file is missing / unreadable.
func patchHash(patchPath string) string {
	if patchPath == "" {
		return ""
	}
	body, err := os.ReadFile(patchPath)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(body)
	return fmt.Sprintf("sha256:%x", sum)
}

// invokeImplementer asks the configured Implementer for a patch
// addressing this round's unresolved Critical/Warning findings, persists
// it under .tribunal/convergence/<plan>/round-NNNN-patch.{diff,md}, and
// (when AutoApply is true) routes it through ApplyPatch. Mutates the
// round in place with PatchAuthored / PatchPath / PatchApplied state.
//
// Failures are recorded on the round via PatchError but don't propagate
// — the controller still surfaces NeedsFixes so the operator can fall
// back to manual fix authoring.
func (c *Controller) invokeImplementer(ctx context.Context, round *RoundResult, target ConvergenceTarget) {
	// Filter findings to the actionable subset (Critical + Warning).
	actionable := make([]RoundFinding, 0, len(round.Findings))
	for _, f := range round.Findings {
		s := strings.ToLower(f.Severity)
		if s == "critical" || s == "warning" {
			actionable = append(actionable, f)
		}
	}
	if len(actionable) == 0 {
		return
	}
	in := PatchInput{
		PlanID:      target.PlanID,
		ProjectRoot: target.ProjectRoot,
		Round:       round.Round,
		Findings:    actionable,
	}
	if c.IntentLoader != nil {
		in.Intent = c.IntentLoader(target.PlanID)
	}
	if c.DiffLoader != nil {
		in.Diff = c.DiffLoader(target)
	}
	if c.FindingBodyLookup != nil {
		in.FindingBodies = c.FindingBodyLookup(actionable)
	}

	out, err := c.Implementer.Patch(ctx, in)
	if err != nil {
		round.PatchError = err.Error()
		return
	}
	round.PatchTokens = out.TokenCost
	round.PatchRefused = out.Refused
	if out.Refused || strings.TrimSpace(out.Patch) == "" {
		// Save the reasoning even when no patch — it's the implementer
		// telling the operator what they couldn't do.
		path, _ := saveImplementerArtifacts(target.ProjectRoot, target.PlanID, round.Round, "", out.Reasoning)
		round.PatchReadme = path
		return
	}

	diffPath, readmePath, err := saveImplementerArtifactsBoth(target.ProjectRoot, target.PlanID, round.Round, out.Patch, out.Reasoning)
	if err != nil {
		round.PatchError = err.Error()
		return
	}
	round.PatchAuthored = true
	round.PatchPath = diffPath
	round.PatchReadme = readmePath

	if c.AutoApply {
		files, err := ApplyPatch(ctx, target.ProjectRoot, out.Patch)
		if err != nil {
			round.PatchError = err.Error()
			return
		}
		round.PatchApplied = true
		round.PatchFiles = files
	}
}

func applyTag(r RoundResult) string {
	if r.PatchApplied {
		return " (APPLIED — review + test before commit)"
	}
	if r.PatchAuthored {
		return " (NOT applied — review before `git apply`)"
	}
	if r.PatchRefused {
		return " (implementer REFUSED — see reasoning)"
	}
	if r.PatchError != "" {
		return " (implementer failed: " + r.PatchError + ")"
	}
	return ""
}

// saveImplementerArtifactsBoth writes the patch + reasoning files and
// returns their paths. Naming: round-NNNN-patch.diff + round-NNNN-patch.md.
func saveImplementerArtifactsBoth(projectRoot, planID string, round int, patch, reasoning string) (diffPath, readmePath string, err error) {
	dir := LedgerDir(projectRoot, planID)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	diffName := fmt.Sprintf("round-%04d-patch.diff", round)
	readmeName := fmt.Sprintf("round-%04d-patch.md", round)
	diffPath = filepath.Join(dir, diffName)
	readmePath = filepath.Join(dir, readmeName)
	if err = os.WriteFile(diffPath, []byte(ensureTrailingNewline(patch)), 0o644); err != nil {
		return "", "", err
	}
	body := "# Implementer reasoning — round " + fmt.Sprintf("%d", round) + "\n\n" + strings.TrimSpace(reasoning) + "\n"
	if err = os.WriteFile(readmePath, []byte(body), 0o644); err != nil {
		return "", "", err
	}
	return diffPath, readmePath, nil
}

// saveImplementerArtifacts is the refused/no-patch variant — writes only
// the reasoning. Returns the readme path.
func saveImplementerArtifacts(projectRoot, planID string, round int, _, reasoning string) (string, error) {
	dir := LedgerDir(projectRoot, planID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	readmePath := filepath.Join(dir, fmt.Sprintf("round-%04d-patch.md", round))
	body := "# Implementer reasoning — round " + fmt.Sprintf("%d", round) + " (no patch)\n\n" + strings.TrimSpace(reasoning) + "\n"
	if err := os.WriteFile(readmePath, []byte(body), 0o644); err != nil {
		return "", err
	}
	return readmePath, nil
}

func ensureTrailingNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
