package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/dpdanpittman/tribunal/internal/clawpatch"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

func newFixCmd() *cobra.Command {
	var (
		findingID      string
		dryRun         bool
		triagerLabel   string
		noAutoRegister bool
		clawpatchModel string
	)
	cmd := &cobra.Command{
		Use:   "fix",
		Short: "Drive clawpatch fix for a Tribunal finding and record the outcome in the ledger",
		Long: `tribunal fix --finding <tribunal-id> wraps clawpatch fix. It resolves
the matching clawpatch finding ID off the signed ledger, runs the
subprocess, persists the resulting PatchAttempt under
.tribunal/patches/, and appends signed triage events so the finding's
disposition stays auditable.

Triage transitions written to the ledger:
  - in-progress: appended before the subprocess starts
  - fixed:       on a clean clawpatch exit with status "applied"
  - open:        on clawpatch exit 6 (validation failed) — the patch
                 landed but tests/typecheck rejected it
  - uncertain:   on dry-run or any other terminal state

Pass --dry-run to invoke clawpatch's planner without applying a patch.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if findingID == "" {
				return fmt.Errorf("--finding is required")
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			l := projectLedger()
			fnd, err := l.FindingByID(findingID)
			if err != nil {
				return err
			}
			if fnd == nil {
				return fmt.Errorf("finding %q not found in ledger", findingID)
			}
			if fnd.ClawpatchID == "" {
				return fmt.Errorf("finding %q has no clawpatch_id — only clawpatch-sourced findings are fixable via this command", findingID)
			}
			reg, err := defaultRegistry()
			if err != nil {
				return err
			}
			kp, label, err := resolveTriagerKey(reg, triagerLabel, !noAutoRegister)
			if err != nil {
				return err
			}

			startNote := fmt.Sprintf("clawpatch fix started (clawpatch_id=%s, dry_run=%v)", fnd.ClawpatchID, dryRun)
			startEvt := ledger.NewTriageEvent(fnd.FindingID, fnd.PlanID, ledger.TriageStatusInProgress, kp, label, startNote)
			if err := startEvt.Sign(kp); err != nil {
				return err
			}
			if err := l.AppendTriage(startEvt); err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			runner := &clawpatch.Runner{
				Cwd:      cwd,
				Provider: "acpx",
				Model:    clawpatchModel,
			}
			res, runErr := runner.Fix(ctx, clawpatch.FixOpts{
				Finding: fnd.ClawpatchID,
				DryRun:  dryRun,
			})

			// Determine the outcome triage status.
			var (
				finalStatus ledger.TriageStatus
				note        string
			)
			switch {
			case dryRun && res != nil:
				finalStatus = ledger.TriageStatusInProgress
				note = fmt.Sprintf("dry-run: plan=%q validation=%q patch_attempt=%s", res.Plan, res.Validation, res.PatchAttempt)
			case runErr != nil && res != nil:
				// clawpatch fix exited non-zero but emitted a parseable JSON
				// object (e.g. exit 6 / validation-failed). Trust the body's
				// status field for the triage decision.
				finalStatus = clawpatch.FixStatusToTriage(res.Status)
				note = fmt.Sprintf("clawpatch fix exit non-zero: status=%s validation=%s", res.Status, res.Validation)
			case runErr != nil:
				finalStatus = ledger.TriageStatusUncertain
				note = fmt.Sprintf("clawpatch fix subprocess error: %v", runErr)
			default:
				finalStatus = clawpatch.FixStatusToTriage(res.Status)
				note = fmt.Sprintf("clawpatch fix ok: status=%s patch_attempt=%s files_changed=%d", res.Status, res.PatchAttempt, res.FilesChanged)
			}

			doneEvt := ledger.NewTriageEvent(fnd.FindingID, fnd.PlanID, finalStatus, kp, label, note)
			if err := doneEvt.Sign(kp); err != nil {
				return err
			}
			if err := l.AppendTriage(doneEvt); err != nil {
				return err
			}

			// Persist the patch attempt JSON for the audit trail. Skip on
			// outright subprocess failure (no result body to write).
			if res != nil {
				if err := writePatchAttemptMirror(cwd, fnd.FindingID, res); err != nil {
					fmt.Fprintf(os.Stderr, "warn: failed to write patch mirror: %v\n", err)
				}
			}

			// Print a short summary the user can paste into a comment.
			if res != nil {
				fmt.Printf("Finding:       %s (clawpatch %s)\n", fnd.FindingID, fnd.ClawpatchID)
				fmt.Printf("Patch attempt: %s\n", res.PatchAttempt)
				if dryRun {
					fmt.Printf("Plan:          %s\n", res.Plan)
				} else {
					fmt.Printf("Status:        %s\n", res.Status)
					fmt.Printf("Files changed: %d (%s)\n", res.FilesChanged, res.ChangedFiles)
				}
				fmt.Printf("Validation:    %s\n", res.Validation)
				fmt.Printf("Triage:        %s\n", finalStatus)
				if res.Next != "" {
					fmt.Printf("Next:          %s\n", res.Next)
				}
			}
			if runErr != nil {
				return runErr
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&findingID, "finding", "", "Tribunal finding ID to fix (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Plan a fix without applying a patch")
	cmd.Flags().StringVar(&triagerLabel, "as", "human-triager", "Agent label to sign the triage events")
	cmd.Flags().BoolVar(&noAutoRegister, "no-auto-register", false, "Refuse to auto-create the triager agent keypair")
	cmd.Flags().StringVar(&clawpatchModel, "clawpatch-model", "", "Model passed to clawpatch (--model)")
	_ = cmd.MarkFlagRequired("finding")
	return cmd
}

// writePatchAttemptMirror persists clawpatch's FixResult under
// .tribunal/patches/<tribunal-finding-id>.json so the audit trail lives
// next to the ledger. Best-effort: callers log but do not fail on error.
func writePatchAttemptMirror(projectRoot, tribunalFindingID string, res *clawpatch.FixResult) error {
	dir := filepath.Join(projectRoot, ".tribunal", "patches")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, tribunalFindingID+".json")
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
