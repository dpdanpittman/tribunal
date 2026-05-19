package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/dpdanpittman/tribunal/internal/converge"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

// newHistoryCmd emits the structured timeline of a plan: per-round
// convergence results (.tribunal/convergence/<plan>/round-*.json) plus
// the signed ledger entries (.tribunal/ledger.jsonl) filtered to the
// plan. Designed for two consumers:
//
//   - Operators inspecting a multi-round audit ("what happened across
//     these five rounds?"). Text output, scannable.
//   - The temporal reviewer (v0.5.0 ADR-0003 M2) consuming the json
//     output as evidence for longitudinal-property findings. The lens
//     reads trajectory data the per-cycle reviewer reports don't
//     surface: claim_hash deduplication across rounds, panel rotation,
//     verify-pass/fail rates, stopping-criterion firing.
//
// History is read-only by contract — this command does not write to
// the convergence dir or the ledger.
func newHistoryCmd() *cobra.Command {
	var (
		planID       string
		trajectoryID string
		format       string
	)
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Emit a structured timeline of a plan's convergence rounds and signed ledger entries",
		Long: `Tribunal history loads:

  .tribunal/convergence/<plan>/round-NNNN.json   per-round results
  .tribunal/ledger.jsonl                         signed findings + resolutions

and emits a structured timeline filtered to either a plan (--plan) or a
trajectory (--trajectory, v0.5.6+). Exactly one is required.

Plan-scoped queries surface convergence rounds + signed findings/
resolutions whose plan_id matches. Trajectory-scoped queries (used by
the temporal lens for cross-plan findings) surface only the signed
trajectory-scoped entries; convergence rounds are by definition
per-plan and don't apply to a trajectory.

Text format (default) is for human inspection. JSON format is the
canonical machine input for the temporal lens (v0.5.0+) and other
trajectory-aware tools.

When the convergence dir is absent (single-pass review, no converge run),
only the signed-ledger view is emitted. When the ledger is absent, only
the convergence rounds are emitted. Empty queries return an empty
timeline with exit 0 — absence is not an error.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if planID == "" && trajectoryID == "" {
				return errors.New("one of --plan or --trajectory is required")
			}
			if planID != "" && trajectoryID != "" {
				return errors.New("--plan and --trajectory are mutually exclusive")
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			var tl *Timeline
			if planID != "" {
				rounds, err := converge.LoadHistory(cwd, planID)
				if err != nil {
					return fmt.Errorf("load convergence history: %w", err)
				}
				findings, resolutions, err := loadPlanLedger(cwd, planID)
				if err != nil {
					return fmt.Errorf("load signed ledger: %w", err)
				}
				tl = buildTimeline(planID, rounds, findings, resolutions)
			} else {
				// trajectory-scoped: no convergence rounds (they're per-plan)
				findings, resolutions, err := loadTrajectoryLedger(cwd, trajectoryID)
				if err != nil {
					return fmt.Errorf("load signed ledger: %w", err)
				}
				tl = buildTrajectoryTimeline(trajectoryID, findings, resolutions)
			}

			switch format {
			case "json":
				return writeHistoryJSON(os.Stdout, tl)
			case "text", "":
				return writeText(os.Stdout, tl)
			default:
				return fmt.Errorf("unknown --format %q (want: text | json)", format)
			}
		},
	}
	cmd.Flags().StringVar(&planID, "plan", "", "Plan ID (matches .tribunal/plans/<id>/ + on-chain plan_id)")
	cmd.Flags().StringVar(&trajectoryID, "trajectory", "", "Trajectory ID for cross-plan findings (v0.5.6+). Mutex with --plan.")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text | json")
	return cmd
}

// loadTrajectoryLedger (v0.5.6+) mirrors loadPlanLedger but filters by
// TrajectoryID instead of PlanID. Used by `tribunal history --trajectory
// <id>` for cross-plan findings.
func loadTrajectoryLedger(projectRoot, trajectoryID string) ([]*ledger.Finding, []*ledger.Resolution, error) {
	path := ledger.DefaultPath(projectRoot)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	l := ledger.New(path)
	findings, resolutions, err := l.All()
	if err != nil {
		return nil, nil, err
	}
	var fOut []*ledger.Finding
	for _, f := range findings {
		if f.TrajectoryID == trajectoryID && trajectoryID != "" {
			fOut = append(fOut, f)
		}
	}
	var rOut []*ledger.Resolution
	for _, r := range resolutions {
		if r.TrajectoryID == trajectoryID && trajectoryID != "" {
			rOut = append(rOut, r)
		}
	}
	sort.SliceStable(fOut, func(i, j int) bool { return fOut[i].Timestamp.Before(fOut[j].Timestamp) })
	sort.SliceStable(rOut, func(i, j int) bool { return rOut[i].Timestamp.Before(rOut[j].Timestamp) })
	return fOut, rOut, nil
}

// buildTrajectoryTimeline projects trajectory-scoped findings into the
// same Timeline shape that plan-scoped queries use. Rounds is always
// empty (convergence is per-plan). The PlanID field in the output is
// reused to surface the trajectory ID — operators reading text output
// see "Plan: <trajectory-name>" but readers of the JSON should rely on
// the TrajectoryID field once that lands in the Timeline schema.
//
// Schema note (v0.5.6): we intentionally don't bump the Timeline JSON
// shape this version. Trajectory queries reuse the existing schema with
// the trajectory's name appearing in plan_id. v0.5.7+ may split out a
// dedicated trajectory_id field on the Timeline struct once external
// consumers of the json format firm up.
func buildTrajectoryTimeline(trajectoryID string, findings []*ledger.Finding, resolutions []*ledger.Resolution) *Timeline {
	// Reuse the plan-scoped builder; the trajectory ID flows into the
	// PlanID field at the projection level.
	tl := buildTimeline("trajectory:"+trajectoryID, nil, findings, resolutions)
	return tl
}

// loadPlanLedger reads the default signed ledger and filters findings +
// resolutions to the given plan. A missing ledger file returns empty
// slices (treated like an unconfigured project).
func loadPlanLedger(projectRoot, planID string) ([]*ledger.Finding, []*ledger.Resolution, error) {
	path := ledger.DefaultPath(projectRoot)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	l := ledger.New(path)
	findings, resolutions, err := l.All()
	if err != nil {
		return nil, nil, err
	}
	var fOut []*ledger.Finding
	for _, f := range findings {
		if f.PlanID == planID {
			fOut = append(fOut, f)
		}
	}
	var rOut []*ledger.Resolution
	for _, r := range resolutions {
		if r.PlanID == planID {
			rOut = append(rOut, r)
		}
	}
	// Sort each by Timestamp ascending so the timeline is causally
	// ordered for the temporal lens regardless of how the ledger was
	// appended.
	sort.SliceStable(fOut, func(i, j int) bool { return fOut[i].Timestamp.Before(fOut[j].Timestamp) })
	sort.SliceStable(rOut, func(i, j int) bool { return rOut[i].Timestamp.Before(rOut[j].Timestamp) })
	return fOut, rOut, nil
}

// Timeline is the json-serialisable shape `tribunal history --format
// json` emits. It is also the input shape the temporal lens consumes.
// Fields are explicit (not embedded slices of internal types) so the
// schema stays stable across internal refactors.
type Timeline struct {
	PlanID         string            `json:"plan_id"`
	Rounds         []TimelineRound   `json:"rounds"`
	SignedFindings []TimelineFinding `json:"signed_findings"`
	Resolutions    []TimelineResolve `json:"resolutions"`
	Summary        TimelineSummary   `json:"summary"`
}

// TimelineRound is the per-round projection of converge.RoundResult.
// Includes only the fields the lens and the operator actually need.
type TimelineRound struct {
	Round          int            `json:"round"`
	StartedAt      time.Time      `json:"started_at"`
	CompletedAt    time.Time      `json:"completed_at"`
	DurationSec    float64        `json:"duration_sec"`
	OverallVerdict string         `json:"overall_verdict"`
	PanelMembers   []string       `json:"panel_members"`
	RotationAxis   string         `json:"rotation_axis,omitempty"`
	FindingsByLens map[string]int `json:"findings_by_severity"`
	NovelFindings  int            `json:"novel_findings"`
	CarriedForward int            `json:"carried_forward"`
	TokenCost      int            `json:"token_cost,omitempty"`
	PatchAuthored  bool           `json:"patch_authored,omitempty"`
	PatchApplied   bool           `json:"patch_applied,omitempty"`
	PatchRefused   bool           `json:"patch_refused,omitempty"`
	VerifyRan      bool           `json:"verify_ran,omitempty"`
	VerifyPassed   bool           `json:"verify_passed,omitempty"`
	Stopped        bool           `json:"stopped,omitempty"`
	StopReason     string         `json:"stop_reason,omitempty"`
	StopCriterion  string         `json:"stop_criterion,omitempty"`
}

// TimelineFinding projects ledger.Finding to the timeline shape.
type TimelineFinding struct {
	FindingID  string    `json:"finding_id"`
	Round      int       `json:"round"`
	AgentLabel string    `json:"agent_label"`
	Severity   string    `json:"severity"`
	Category   string    `json:"category"`
	ClaimHash  string    `json:"claim_hash"`
	ClaimURI   string    `json:"claim_uri,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

// TimelineResolve projects ledger.Resolution to the timeline shape.
type TimelineResolve struct {
	FindingID     string    `json:"finding_id"`
	Outcome       string    `json:"outcome"`
	ResolverLabel string    `json:"resolver_label"`
	EvidenceURI   string    `json:"evidence_uri,omitempty"`
	Timestamp     time.Time `json:"timestamp"`
}

// TimelineSummary is a high-level rollup the lens can read without
// walking every round.
type TimelineSummary struct {
	RoundCount      int            `json:"round_count"`
	SignedCount     int            `json:"signed_count"`
	ResolutionCount int            `json:"resolution_count"`
	OpenFindings    int            `json:"open_findings"`
	UniqueClaims    int            `json:"unique_claims"`
	CarriedForward  int            `json:"carried_forward"`
	FinalVerdict    string         `json:"final_verdict,omitempty"`
	StoppedAtRound  int            `json:"stopped_at_round,omitempty"`
	StopCriterion   string         `json:"stop_criterion,omitempty"`
	OutcomesByKind  map[string]int `json:"outcomes_by_kind,omitempty"`
}

// buildTimeline projects the raw round + ledger data into the
// Timeline shape.
func buildTimeline(planID string, rounds []converge.RoundResult, findings []*ledger.Finding, resolutions []*ledger.Resolution) *Timeline {
	tl := &Timeline{
		PlanID:         planID,
		Rounds:         []TimelineRound{},
		SignedFindings: []TimelineFinding{},
		Resolutions:    []TimelineResolve{},
	}

	allClaims := map[string]bool{}
	totalCarried := 0
	for _, r := range rounds {
		round := projectRound(r)
		for _, f := range r.Findings {
			h := f.ClaimHash
			if h != "" {
				if !allClaims[h] {
					allClaims[h] = true
					round.NovelFindings++
				} else {
					round.CarriedForward++
				}
			}
		}
		totalCarried += round.CarriedForward
		tl.Rounds = append(tl.Rounds, round)
	}

	for _, f := range findings {
		tl.SignedFindings = append(tl.SignedFindings, TimelineFinding{
			FindingID:  f.FindingID,
			Round:      f.Round,
			AgentLabel: f.AgentLabel,
			Severity:   string(f.Severity),
			Category:   string(f.Category),
			ClaimHash:  f.ClaimHash,
			ClaimURI:   f.ClaimURI,
			Timestamp:  f.Timestamp,
		})
	}

	resolvedFindings := map[string]string{} // FindingID -> outcome
	outcomesByKind := map[string]int{}
	for _, r := range resolutions {
		tl.Resolutions = append(tl.Resolutions, TimelineResolve{
			FindingID:     r.FindingID,
			Outcome:       string(r.Outcome),
			ResolverLabel: r.ResolverLabel,
			EvidenceURI:   r.EvidenceURI,
			Timestamp:     r.Timestamp,
		})
		resolvedFindings[r.FindingID] = string(r.Outcome)
		outcomesByKind[string(r.Outcome)]++
	}
	openCount := 0
	for _, f := range findings {
		if _, ok := resolvedFindings[f.FindingID]; !ok {
			openCount++
		}
	}

	tl.Summary = TimelineSummary{
		RoundCount:      len(tl.Rounds),
		SignedCount:     len(tl.SignedFindings),
		ResolutionCount: len(tl.Resolutions),
		OpenFindings:    openCount,
		UniqueClaims:    len(allClaims),
		CarriedForward:  totalCarried,
		OutcomesByKind:  outcomesByKind,
	}
	if len(tl.Rounds) > 0 {
		last := tl.Rounds[len(tl.Rounds)-1]
		tl.Summary.FinalVerdict = last.OverallVerdict
		if last.Stopped {
			tl.Summary.StoppedAtRound = last.Round
			tl.Summary.StopCriterion = last.StopCriterion
		}
	}
	return tl
}

func projectRound(r converge.RoundResult) TimelineRound {
	out := TimelineRound{
		Round:          r.Round,
		StartedAt:      r.StartedAt,
		CompletedAt:    r.CompletedAt,
		DurationSec:    r.Duration.Seconds(),
		OverallVerdict: r.OverallVerdict,
		RotationAxis:   r.Panel.RotationAxis,
		FindingsByLens: map[string]int{},
		TokenCost:      r.TokenCost,
		PatchAuthored:  r.PatchAuthored,
		PatchApplied:   r.PatchApplied,
		PatchRefused:   r.PatchRefused,
		VerifyRan:      r.VerifyRan,
		VerifyPassed:   r.VerifyPassed,
		Stopped:        r.Stopped,
		StopReason:     r.StopReason,
		StopCriterion:  r.StopCriterion,
	}
	for _, m := range r.Panel.Members {
		out.PanelMembers = append(out.PanelMembers, m.Label)
	}
	for _, f := range r.Findings {
		sev := f.Severity
		if sev == "" {
			sev = "unspecified"
		}
		out.FindingsByLens[sev]++
	}
	return out
}

func writeHistoryJSON(w io.Writer, tl *Timeline) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(tl)
}

func writeText(w io.Writer, tl *Timeline) error {
	fmt.Fprintf(w, "Plan:           %s\n", tl.PlanID)
	fmt.Fprintf(w, "Rounds:         %d\n", tl.Summary.RoundCount)
	fmt.Fprintf(w, "Signed:         %d findings, %d resolutions, %d open\n",
		tl.Summary.SignedCount, tl.Summary.ResolutionCount, tl.Summary.OpenFindings)
	if tl.Summary.UniqueClaims > 0 {
		fmt.Fprintf(w, "Unique claims:  %d (%d carried forward across rounds)\n",
			tl.Summary.UniqueClaims, tl.Summary.CarriedForward)
	}
	if tl.Summary.FinalVerdict != "" {
		fmt.Fprintf(w, "Final verdict:  %s", tl.Summary.FinalVerdict)
		if tl.Summary.StoppedAtRound > 0 {
			fmt.Fprintf(w, " (stopped at round %d: %s)", tl.Summary.StoppedAtRound, tl.Summary.StopCriterion)
		}
		fmt.Fprintln(w)
	}
	if len(tl.Summary.OutcomesByKind) > 0 {
		fmt.Fprintf(w, "Outcomes:       ")
		first := true
		for outcome, n := range tl.Summary.OutcomesByKind {
			if !first {
				fmt.Fprint(w, ", ")
			}
			fmt.Fprintf(w, "%s=%d", outcome, n)
			first = false
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w)

	if len(tl.Rounds) == 0 && len(tl.SignedFindings) == 0 {
		fmt.Fprintln(w, "(no convergence rounds or signed findings recorded for this plan)")
		return nil
	}

	for _, r := range tl.Rounds {
		fmt.Fprintf(w, "Round %d  %s  %.1fs\n", r.Round, r.StartedAt.UTC().Format(time.RFC3339), r.DurationSec)
		if len(r.PanelMembers) > 0 {
			fmt.Fprintf(w, "  Panel:        %v", r.PanelMembers)
			if r.RotationAxis != "" {
				fmt.Fprintf(w, "  [%s]", r.RotationAxis)
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "  Verdict:      %s\n", r.OverallVerdict)
		if len(r.FindingsByLens) > 0 {
			fmt.Fprint(w, "  Findings:     ")
			keys := make([]string, 0, len(r.FindingsByLens))
			for k := range r.FindingsByLens {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			first := true
			for _, k := range keys {
				if !first {
					fmt.Fprint(w, ", ")
				}
				fmt.Fprintf(w, "%s=%d", k, r.FindingsByLens[k])
				first = false
			}
			if r.NovelFindings > 0 || r.CarriedForward > 0 {
				fmt.Fprintf(w, " (novel=%d, carry-forward=%d)", r.NovelFindings, r.CarriedForward)
			}
			fmt.Fprintln(w)
		}
		if r.PatchAuthored {
			status := "authored"
			if r.PatchRefused {
				status += ", refused"
			}
			if r.PatchApplied {
				status += ", applied"
			}
			if r.VerifyRan {
				if r.VerifyPassed {
					status += ", verify PASS"
				} else {
					status += ", verify FAIL"
				}
			}
			fmt.Fprintf(w, "  Patch:        %s\n", status)
		}
		if r.Stopped {
			fmt.Fprintf(w, "  Stopped:      %s — %s\n", r.StopCriterion, r.StopReason)
		}
		fmt.Fprintln(w)
	}

	if len(tl.SignedFindings) > 0 {
		fmt.Fprintln(w, "Signed findings (plan-scoped):")
		resolvedOutcome := map[string]string{}
		for _, r := range tl.Resolutions {
			resolvedOutcome[r.FindingID] = r.Outcome
		}
		for _, f := range tl.SignedFindings {
			outcome := "open"
			if o, ok := resolvedOutcome[f.FindingID]; ok {
				outcome = o
			}
			fmt.Fprintf(w, "  %s  [%s]  %s  round=%d  %s\n",
				f.FindingID, f.Severity, f.AgentLabel, f.Round, outcome)
		}
	}
	return nil
}
