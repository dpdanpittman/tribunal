package review

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dpdanpittman/tribunal/internal/agent"
	"github.com/dpdanpittman/tribunal/internal/clawpatch"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

// ClawpatchLensRun summarises the lens stage of a clawpatch-driven review.
// Shape mirrors AdversaryRun so a caller can reuse formatting code.
type ClawpatchLensRun struct {
	PlanID         string                          `json:"plan_id"`
	Round          int                             `json:"round"`
	FindingsByLens map[clawpatch.Lens][]clawpatch.Finding `json:"-"`
	WrittenReports []string                        `json:"written_reports"`
	LedgerFindings []*ledger.Finding               `json:"ledger_findings,omitempty"`
	Skipped        []string                        `json:"skipped_findings,omitempty"`
	MapResult      *clawpatch.MapResult            `json:"map_result,omitempty"`
	ReviewResult   *clawpatch.ReviewResult         `json:"review_result,omitempty"`
	Duration       time.Duration                   `json:"duration"`
}

// ClawpatchLensOptions shape RunClawpatchLens. Same defaults as the
// adversary stage where they overlap.
type ClawpatchLensOptions struct {
	ProjectRoot   string
	PlanID        string
	Round         int
	WriteToLedger bool // append signed findings; default true
	AutoRegister  bool // auto-create lens reviewer keypairs; default true
	// Provider passes through to clawpatch (e.g. "acpx"). Empty = clawpatch
	// default.
	Provider string
	// Model passes through to clawpatch. Empty = provider default.
	Model string
	// Timeout per clawpatch call. Zero = clawpatch.Runner default.
	Timeout time.Duration
	// SkipMap is a debugging escape hatch: skip `clawpatch map` and assume
	// the feature graph is already populated.
	SkipMap bool
}

// RunClawpatchLens runs clawpatch's discovery layer as the Tribunal lens
// stage. Sequence: doctor → map → review → list findings → bucket by lens
// → sign + append → emit per-lens report markdowns.
//
// On return, the existing adversary stage (review.Run) can read the
// freshly-written reports off disk via review.FindInputs and continue
// unchanged.
func RunClawpatchLens(ctx context.Context, opts ClawpatchLensOptions, agentReg *agent.Registry) (*ClawpatchLensRun, error) {
	if opts.ProjectRoot == "" {
		return nil, errors.New("clawpatch lens: ProjectRoot required")
	}
	if opts.PlanID == "" {
		return nil, errors.New("clawpatch lens: PlanID required")
	}
	if opts.Round == 0 {
		opts.Round = 1
	}

	runner := &clawpatch.Runner{
		Cwd:      opts.ProjectRoot,
		Provider: opts.Provider,
		Model:    opts.Model,
		Timeout:  opts.Timeout,
	}

	started := time.Now()
	run := &ClawpatchLensRun{
		PlanID:         opts.PlanID,
		Round:          opts.Round,
		FindingsByLens: map[clawpatch.Lens][]clawpatch.Finding{},
	}

	// 1. Preflight. Surfaces missing clawpatch binary OR a provider config
	// error (exit code 4) before we spend tokens on review.
	if err := runner.Doctor(ctx); err != nil {
		return nil, fmt.Errorf("clawpatch preflight: %w", err)
	}

	// 2. Map. Discarded for Phase 1 but cached on the result for
	// debugging. The decision (locked) is map-once-review-N-times; map is
	// where deterministic feature discovery happens.
	if !opts.SkipMap {
		mapRes, err := runner.Map(ctx)
		if err != nil {
			return nil, fmt.Errorf("clawpatch map: %w", err)
		}
		run.MapResult = mapRes
	}

	// 3. Single review pass (Phase 1). Phase 2 will run three lens-aware
	// reviews once clawpatch accepts custom prompts upstream.
	reviewRes, err := runner.Review(ctx, clawpatch.ReviewOpts{Jobs: 10})
	if err != nil {
		return nil, fmt.Errorf("clawpatch review: %w", err)
	}
	run.ReviewResult = reviewRes

	// 4. Load the per-finding records from disk. The review summary only
	// gives counts.
	findings, err := runner.ListFindings(ctx)
	if err != nil {
		return nil, fmt.Errorf("list clawpatch findings: %w", err)
	}

	// 5. Translate, sign, persist. Each finding's lens is determined
	// post-hoc by its clawpatch category (LensBucket).
	for _, lens := range clawpatch.AllLenses() {
		run.FindingsByLens[lens] = nil
	}
	keypairs := map[clawpatch.Lens]*agent.Keypair{}
	labels := map[clawpatch.Lens]string{}

	for _, cp := range findings {
		lens := clawpatch.LensBucket(cp.Category)
		run.FindingsByLens[lens] = append(run.FindingsByLens[lens], cp)

		if !opts.WriteToLedger || agentReg == nil {
			continue
		}
		kp, label, err := lookupOrCreateLensKey(agentReg, lens, opts.AutoRegister, opts.Model)
		if err != nil {
			run.Skipped = append(run.Skipped, fmt.Sprintf("%s: no signing key (%v)", cp.FindingID, err))
			continue
		}
		keypairs[lens] = kp
		labels[lens] = label

		fnd, err := clawpatch.ToTribunalFinding(cp, opts.ProjectRoot, opts.PlanID, opts.Round, kp, label)
		if err != nil {
			run.Skipped = append(run.Skipped, fmt.Sprintf("%s: translate (%v)", cp.FindingID, err))
			continue
		}
		ledgerPath := filepath.Join(opts.ProjectRoot, ".tribunal", "ledger.jsonl")
		l := ledger.New(ledgerPath)
		if err := l.AppendFinding(fnd); err != nil {
			run.Skipped = append(run.Skipped, fmt.Sprintf("%s: ledger append (%v)", cp.FindingID, err))
			continue
		}
		// Write the human-readable claim markdown so ClaimURI resolves.
		findingsDir := filepath.Join(opts.ProjectRoot, ".tribunal", "findings")
		_ = os.MkdirAll(findingsDir, 0o755)
		_ = os.WriteFile(filepath.Join(opts.ProjectRoot, fnd.ClaimURI),
			[]byte(formatClawpatchFindingMarkdown(fnd, cp, label)),
			0o644,
		)
		run.LedgerFindings = append(run.LedgerFindings, fnd)
	}

	// 6. Per-lens report markdowns. Adversary stage reads these via
	// review.FindInputs (no change needed there).
	reportsDir := filepath.Join(opts.ProjectRoot, ".tribunal", "reports", opts.PlanID)
	if err := os.MkdirAll(reportsDir, 0o755); err != nil {
		return nil, err
	}
	for _, lens := range clawpatch.AllLenses() {
		fname := fmt.Sprintf("%s-%s-clawpatch.md", opts.PlanID, lens)
		path := filepath.Join(reportsDir, fname)
		body := clawpatch.FormatLensReport(lens, opts.PlanID, run.FindingsByLens[lens])
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return nil, fmt.Errorf("write lens report %s: %w", fname, err)
		}
		run.WrittenReports = append(run.WrittenReports, path)
	}
	sort.Strings(run.WrittenReports)

	run.Duration = time.Since(started)
	return run, nil
}

// lookupOrCreateLensKey resolves the Tribunal agent that owns clawpatch-
// sourced findings for the given lens. Same shape as resolveAdversaryKey
// but parametrised on Lens (not PanelMember) and using the lens-specific
// reviewer role.
func lookupOrCreateLensKey(reg *agent.Registry, lens clawpatch.Lens, autoRegister bool, model string) (*agent.Keypair, string, error) {
	label := clawpatch.AgentLabel(lens)
	if existing, err := reg.Get(label); err == nil {
		kp, err := reg.LoadKeypair(existing.Label)
		if err != nil {
			return nil, label, err
		}
		return kp, existing.Label, nil
	}
	if !autoRegister {
		return nil, label, fmt.Errorf("no registered agent for %q (run `tribunal agents add` or pass --auto-register)", label)
	}
	modelID := model
	if modelID == "" {
		// Synthetic value: clawpatch's actual model is opaque to Tribunal
		// (it lives in clawpatch's provider config). Tag the model field
		// so a human can tell what produced the finding from `tribunal
		// agents list`.
		modelID = "clawpatch-via-acpx"
	}
	a, err := reg.Add(label, modelID, clawpatch.AgentRole(lens))
	if err != nil {
		return nil, label, err
	}
	kp, err := reg.LoadKeypair(a.Label)
	if err != nil {
		return nil, label, err
	}
	return kp, a.Label, nil
}

// formatClawpatchFindingMarkdown produces the body of the .tribunal/
// findings/<id>.md file. Mirrors the shape produced by the adversary
// stage so downstream readers (PM, classifier) don't need to special-case
// provenance.
func formatClawpatchFindingMarkdown(fnd *ledger.Finding, cp clawpatch.Finding, agentLabel string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", cp.Title)
	fmt.Fprintf(&b, "- **Tribunal finding ID**: %s\n", fnd.FindingID)
	fmt.Fprintf(&b, "- **Clawpatch finding ID**: %s\n", cp.FindingID)
	fmt.Fprintf(&b, "- **Agent**: %s\n", agentLabel)
	fmt.Fprintf(&b, "- **Severity**: %s\n", fnd.Severity)
	fmt.Fprintf(&b, "- **Category**: %s\n", fnd.Category)
	fmt.Fprintf(&b, "- **Plan**: %s\n", fnd.PlanID)
	fmt.Fprintf(&b, "- **Round**: %d\n", fnd.Round)
	fmt.Fprintf(&b, "- **Stake**: %d\n", fnd.Stake)
	fmt.Fprintf(&b, "- **Timestamp**: %s\n\n", fnd.Timestamp.Format(time.RFC3339))
	if len(cp.Evidence) > 0 {
		b.WriteString("## Evidence\n\n")
		for _, e := range cp.Evidence {
			loc := e.Path
			if e.StartLine != nil {
				loc = fmt.Sprintf("%s:%d", e.Path, *e.StartLine)
				if e.EndLine != nil && *e.EndLine != *e.StartLine {
					loc = fmt.Sprintf("%s-%d", loc, *e.EndLine)
				}
			}
			fmt.Fprintf(&b, "- `%s`", loc)
			if e.Symbol != nil && *e.Symbol != "" {
				fmt.Fprintf(&b, " (`%s`)", *e.Symbol)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if cp.Reasoning != "" {
		b.WriteString("## Reasoning\n\n")
		b.WriteString(cp.Reasoning)
		b.WriteString("\n\n")
	}
	if cp.Recommendation != "" {
		b.WriteString("## Recommendation\n\n")
		b.WriteString(cp.Recommendation)
		b.WriteString("\n\n")
	}
	return b.String()
}
