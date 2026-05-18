// Package review wires Tribunal's hybrid review orchestration: locating
// the per-plan artifacts (intent, plan, diff, reviewer reports), running
// the adversary panel via internal/dispatch, persisting per-member
// reports + signed findings, and producing a structured outcome the
// caller can act on.
package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dpdanpittman/tribunal/internal/agent"
	"github.com/dpdanpittman/tribunal/internal/dispatch"
	"github.com/dpdanpittman/tribunal/internal/ledger"
)

// AdversaryRun is the result of running the adversary stage of a hybrid
// review. The full per-member reports are persisted to disk; this struct
// summarizes the outcome for CLI display and exit-code purposes.
type AdversaryRun struct {
	PlanID         string              `json:"plan_id"`
	Panel          string              `json:"panel"`
	Synthesis      *dispatch.Synthesis `json:"synthesis"`
	WrittenReports []string            `json:"written_reports"`
	LedgerFindings []*ledger.Finding   `json:"ledger_findings,omitempty"`
	Skipped        []string            `json:"skipped_members,omitempty"`
	OverallVerdict string              `json:"overall_verdict"`
	Duration       time.Duration       `json:"duration"`
}

// Inputs collects the locations of every artifact the adversary stage
// needs. Use FindInputs to populate from .tribunal/<plan-id>/ + git.
type Inputs struct {
	PlanID          string
	IntentPath      string
	PlanPath        string
	Diff            string
	ReviewerReports map[string]string // label -> body
}

// FindInputs resolves the artifacts for the given plan ID under
// projectRoot. The diff is computed via git when DiffSpec is non-empty;
// otherwise the function leaves it blank (caller may set it manually).
func FindInputs(projectRoot, planID, diffSpec string) (*Inputs, error) {
	in := &Inputs{
		PlanID:          planID,
		ReviewerReports: map[string]string{},
	}
	planDir := filepath.Join(projectRoot, ".tribunal", "plans", planID)
	intentPath := filepath.Join(planDir, "intent.md")
	if _, err := os.Stat(intentPath); err == nil {
		in.IntentPath = intentPath
	} else if errors.Is(err, fs.ErrNotExist) {
		// Fallback: examples directory uses ./intent.md at project root.
		alt := filepath.Join(projectRoot, "intent.md")
		if _, err2 := os.Stat(alt); err2 == nil {
			in.IntentPath = alt
		}
	}
	planPath := filepath.Join(planDir, "plan.md")
	if _, err := os.Stat(planPath); err == nil {
		in.PlanPath = planPath
	}

	reportsDir := filepath.Join(projectRoot, ".tribunal", "reports", planID)
	if entries, err := os.ReadDir(reportsDir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".md") {
				continue
			}
			if !strings.Contains(name, "reviewer") && !strings.Contains(name, "-arch") && !strings.Contains(name, "-sec") && !strings.Contains(name, "-perf") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(reportsDir, name))
			if err != nil {
				return nil, fmt.Errorf("read reviewer report %s: %w", name, err)
			}
			label := strings.TrimSuffix(name, ".md")
			in.ReviewerReports[label] = string(body)
		}
	}

	if diffSpec != "" {
		diff, err := resolveDiff(projectRoot, diffSpec)
		if err != nil {
			return nil, err
		}
		in.Diff = diff
	}
	return in, nil
}

// resolveDiff interprets diffSpec as one of:
//   - a path to a file containing a unified diff
//   - a git range (e.g. "main..HEAD", "HEAD~1..HEAD", "HEAD")
//   - "staged" / "cached" → `git diff --staged`
func resolveDiff(projectRoot, spec string) (string, error) {
	if spec == "staged" || spec == "cached" {
		out, err := runGit(projectRoot, "diff", "--staged")
		return out, err
	}
	if _, err := os.Stat(spec); err == nil {
		body, err := os.ReadFile(spec)
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
	return runGit(projectRoot, "diff", spec)
}

func runGit(projectRoot string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = projectRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w (output: %s)", strings.Join(args, " "), err, string(out))
	}
	return string(out), nil
}

// Options shape the adversary stage's behavior.
type Options struct {
	ProjectRoot   string
	PlanID        string
	PanelName     string // "default" | "high-stakes"
	BucketSpec    string // diversity bucket; "" → composite:vendor_family,focus
	AdversaryBody string // verbatim agent prompt body
	WriteToLedger bool   // append signed findings; default true
	AutoRegister  bool   // auto-create missing adversary agent keys; default true

	// PanelOverride, when non-empty, replaces the panel selected by
	// PanelName for this invocation. Used by the convergence controller
	// (v0.4.1+) to drive per-round rotated panels without mutating the
	// on-disk tribunal.yaml. The PanelName is still surfaced in the
	// AdversaryRun.Panel field so output remains stable.
	PanelOverride []dispatch.PanelMember
}

// Run executes the adversary stage. It locates artifacts, dispatches the
// panel, persists per-member reports, optionally signs and appends
// findings to the ledger, and returns a summary.
func Run(ctx context.Context, opts Options, in *Inputs, registry *dispatch.Registry, agentReg *agent.Registry) (*AdversaryRun, error) {
	if opts.ProjectRoot == "" {
		return nil, errors.New("review: ProjectRoot required")
	}
	if opts.PlanID == "" {
		return nil, errors.New("review: PlanID required")
	}
	if in == nil {
		return nil, errors.New("review: Inputs required")
	}

	cfg, err := dispatch.LoadConfig(opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	panel, err := cfg.Select(opts.PanelName)
	if err != nil {
		return nil, err
	}
	if len(opts.PanelOverride) > 0 {
		panel.Members = opts.PanelOverride
	}
	if err := dispatch.ValidatePanel(panel); err != nil {
		return nil, err
	}

	system := dispatch.BuildSystemPrompt(opts.AdversaryBody, panelFocus(panel))
	intentBody := readOrPlaceholder(in.IntentPath, "(intent doc missing — supply --intent or ensure .tribunal/plans/<plan>/intent.md exists)")
	planBody := readOrPlaceholder(in.PlanPath, "(plan doc missing — fallback to intent)")
	diff := in.Diff
	if diff == "" {
		diff = "(no diff captured — pass --diff <range|path|staged>)"
	}
	user := dispatch.BuildUserPrompt(intentBody, planBody, diff, in.ReviewerReports)

	started := time.Now()
	reports, err := dispatch.Dispatch(ctx, registry, panel, system, user)
	if err != nil {
		return nil, err
	}
	bucketFn, err := dispatch.SelectBucket(opts.BucketSpec)
	if err != nil {
		return nil, err
	}
	syn := dispatch.Synthesize(reports, bucketFn)

	// Persist per-member reports.
	outDir := filepath.Join(opts.ProjectRoot, ".tribunal", "reports", opts.PlanID)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	var written []string
	for _, r := range reports {
		if r == nil {
			continue
		}
		name := fmt.Sprintf("%s-adversary-%s.md", opts.PlanID, sanitizeFilename(r.Member.Label))
		path := filepath.Join(outDir, name)
		if err := os.WriteFile(path, []byte(formatMemberReport(r)), 0o644); err != nil {
			return nil, err
		}
		written = append(written, path)
	}
	// Also write the synthesis as a JSON sibling.
	synPath := filepath.Join(outDir, fmt.Sprintf("%s-adversary-synthesis.json", opts.PlanID))
	if data, err := json.MarshalIndent(syn, "", "  "); err == nil {
		_ = os.WriteFile(synPath, append(data, '\n'), 0o644)
		written = append(written, synPath)
	}
	sort.Strings(written)

	// Sign and append findings to the ledger.
	var ledgerFindings []*ledger.Finding
	var skipped []string
	if opts.WriteToLedger && agentReg != nil {
		ledgerPath := filepath.Join(opts.ProjectRoot, ".tribunal", "ledger.jsonl")
		l := ledger.New(ledgerPath)
		for _, r := range reports {
			if r == nil || r.Error != "" {
				if r != nil && r.Error != "" {
					skipped = append(skipped, fmt.Sprintf("%s: provider error", r.Member.Label))
				}
				continue
			}
			kp, agentLabel, err := resolveAdversaryKey(agentReg, r.Member, opts.AutoRegister)
			if err != nil {
				skipped = append(skipped, fmt.Sprintf("%s: no signing key (%v)", r.Member.Label, err))
				continue
			}
			for i, f := range r.Findings {
				if f.Category == "" || f.Severity == "" {
					skipped = append(skipped, fmt.Sprintf("%s: finding %d incomplete", agentLabel, i))
					continue
				}
				sev := ledger.Severity(strings.ToLower(f.Severity))
				if !sev.IsValid() {
					skipped = append(skipped, fmt.Sprintf("%s: unknown severity %q", agentLabel, f.Severity))
					continue
				}
				findingID := fmt.Sprintf("F-%s-%s-%d", opts.PlanID, sanitizeFilename(agentLabel), i+1)
				claimHash := "sha256:" + sha256hex(f.Scenario)
				claimURI := filepath.Join(".tribunal", "findings", findingID+".md")
				fnd := ledger.NewFinding(findingID, opts.PlanID, 1, kp, agentLabel, sev, ledger.Category(strings.ToLower(f.Category)), claimHash, claimURI)
				if err := fnd.Sign(kp); err != nil {
					skipped = append(skipped, fmt.Sprintf("%s: sign error %v", agentLabel, err))
					continue
				}
				if err := l.AppendFinding(fnd); err != nil {
					skipped = append(skipped, fmt.Sprintf("%s: ledger append error %v", agentLabel, err))
					continue
				}
				// Also write the human-readable claim text so the URI resolves.
				_ = os.MkdirAll(filepath.Join(opts.ProjectRoot, ".tribunal", "findings"), 0o755)
				_ = os.WriteFile(filepath.Join(opts.ProjectRoot, claimURI), []byte(formatFindingMarkdown(fnd, f, agentLabel)), 0o644)
				ledgerFindings = append(ledgerFindings, fnd)
			}
		}
	}

	return &AdversaryRun{
		PlanID:         opts.PlanID,
		Panel:          panel.Name,
		Synthesis:      syn,
		WrittenReports: written,
		LedgerFindings: ledgerFindings,
		Skipped:        skipped,
		OverallVerdict: syn.Overall,
		Duration:       time.Since(started),
	}, nil
}

// resolveAdversaryKey returns a keypair to sign findings with for the
// given panel member. If a matching agent already exists in the registry
// (by label), that keypair is reused. Otherwise — when AutoRegister is true
// — a new keypair is generated and registered for future runs.
func resolveAdversaryKey(reg *agent.Registry, m dispatch.PanelMember, autoRegister bool) (*agent.Keypair, string, error) {
	label := m.Label
	if label == "" {
		label = m.Provider + "-" + m.Model
	}
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
	a, err := reg.Add(label, m.Model, agent.RoleAdversary)
	if err != nil {
		return nil, label, err
	}
	kp, err := reg.LoadKeypair(a.Label)
	if err != nil {
		return nil, label, err
	}
	return kp, a.Label, nil
}

// panelFocus returns the dominant focus across panel members for the
// system prompt. If members disagree, the focus modifier is left blank;
// each member's per-attack prompt gets its own focus appended elsewhere.
//
// For v0.2 we keep this simple: take the first non-empty focus or default.
func panelFocus(p dispatch.Panel) string {
	for _, m := range p.Members {
		if m.Focus != "" {
			return m.Focus
		}
	}
	return ""
}

func readOrPlaceholder(path, placeholder string) string {
	if path == "" {
		return placeholder
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return placeholder
	}
	return string(body)
}

func formatMemberReport(r *dispatch.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Adversary report: %s\n\n", r.Member.Label)
	fmt.Fprintf(&b, "- Provider:   %s\n", r.Member.Provider)
	fmt.Fprintf(&b, "- Model:      %s\n", r.Member.Model)
	fmt.Fprintf(&b, "- Temperature:%.2f\n", r.Member.Temperature)
	fmt.Fprintf(&b, "- Focus:      %s\n", r.Member.Focus)
	fmt.Fprintf(&b, "- Verdict:    %s\n", r.Verdict)
	if r.Reason != "" {
		fmt.Fprintf(&b, "- Reason:     %s\n", r.Reason)
	}
	if r.Error != "" {
		fmt.Fprintf(&b, "- Error:      %s\n", r.Error)
	}
	fmt.Fprintf(&b, "- Duration:   %s\n\n", r.Duration.Truncate(time.Millisecond))

	fmt.Fprintln(&b, "## Raw model output")
	fmt.Fprintln(&b)
	if r.RawText == "" {
		fmt.Fprintln(&b, "_(no model output captured)_")
	} else {
		fmt.Fprintln(&b, r.RawText)
	}
	fmt.Fprintln(&b)

	if len(r.Findings) > 0 {
		fmt.Fprintln(&b, "## Parsed findings")
		fmt.Fprintln(&b)
		for i, f := range r.Findings {
			fmt.Fprintf(&b, "### Finding %d — %s\n\n", i+1, f.Category)
			fmt.Fprintf(&b, "- Severity: %s\n", f.Severity)
			if f.Scenario != "" {
				fmt.Fprintf(&b, "- Scenario: %s\n", f.Scenario)
			}
			if f.Defense != "" {
				fmt.Fprintf(&b, "- Suggested defense: %s\n", f.Defense)
			}
			fmt.Fprintln(&b)
		}
	}

	return b.String()
}

func formatFindingMarkdown(fnd *ledger.Finding, f dispatch.ParsedFinding, agentLabel string) string {
	return fmt.Sprintf(`# %s

- Plan ID:   %s
- Author:    %s
- Severity:  %s
- Category:  %s
- Stake:     %d
- Timestamp: %s

## Scenario

%s

## Suggested defense

%s

## Provenance

- Filed via Tribunal adversary dispatch.
- Signed by agent pubkey: %s
- Claim hash: %s
`, fnd.FindingID, fnd.PlanID, agentLabel, fnd.Severity, fnd.Category, fnd.Stake, fnd.Timestamp.Format(time.RFC3339), f.Scenario, f.Defense, fnd.AgentPubkey, fnd.ClaimHash)
}

func sanitizeFilename(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
