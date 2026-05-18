package converge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Implementer authors a fix between convergence rounds. Given the
// findings from the just-completed round (plus diff + intent context),
// it produces a unified-diff patch. The controller then either presents
// the patch for human approval (M2 default) or applies it directly via
// `git apply` (M2 --auto-apply opt-in). M3 will close the loop by
// dispatching the next round in the same invocation; M2 always exits
// with NeedsFixes after a patch is authored so the operator drives
// validation + commit + re-invocation.
//
// Implementations are responsible for producing a self-contained
// unified diff applicable from the project root. Anchor text (function
// signatures, surrounding lines) is essential — the controller will
// refuse to apply a patch git can't verify.
type Implementer interface {
	// Patch returns the proposed unified-diff patch plus optional
	// reasoning prose. The patch text is what the controller writes to
	// disk (and optionally applies); the reasoning is preserved
	// alongside for the audit trail.
	Patch(ctx context.Context, in PatchInput) (*PatchOutput, error)

	// Label returns the implementer's keypair label — used for the
	// audit trail and for the controller's role-separation check.
	Label() string
}

// PatchInput is everything the implementer needs to author a fix.
type PatchInput struct {
	// PlanID + ProjectRoot scope the work for log/path purposes.
	PlanID      string
	ProjectRoot string

	// Round is the round number that produced these findings.
	Round int

	// Findings are the unresolved Critical/Warning findings the
	// implementer should address. Suggestion-severity findings are
	// filtered before this call by the severity-floor flag.
	Findings []RoundFinding

	// Intent is the .tribunal/plans/<plan>/intent.md body (best-effort;
	// empty when missing).
	Intent string

	// Diff is the diff under review (the operator's --diff input).
	Diff string

	// FindingBodies maps finding.ClaimURI → file body for the per-finding
	// markdown the adversary stage wrote. Lets the implementer see the
	// full scenario/defense text the synthesis-only RoundFinding doesn't
	// carry.
	FindingBodies map[string]string
}

// PatchOutput is the implementer's response.
type PatchOutput struct {
	// Patch is the proposed unified-diff patch. Must be empty if no fix
	// is being proposed (with Reasoning explaining why).
	Patch string

	// Reasoning is free-form prose explaining the patch choice. Persisted
	// alongside the patch under .tribunal/convergence/<plan>/round-N-patch.md.
	Reasoning string

	// TokenCost is the implementer's reported cost; best-effort.
	TokenCost int

	// Refused indicates the implementer declined to author a patch
	// (e.g. it didn't understand the finding, or the fix is outside
	// its scope). The controller treats Refused like an empty patch
	// but surfaces the reasoning so the operator knows what happened.
	Refused bool
}

// ApplyPatch writes patch text to a temp file and calls `git apply`
// from projectRoot. Refuses if the working tree is dirty (would conflate
// hunks with operator's pending changes). Verifies via `git apply --check`
// before the real apply so failures are caught without partial writes.
//
// Returns the list of files touched (parsed from --stat output).
func ApplyPatch(ctx context.Context, projectRoot, patch string) ([]string, error) {
	if strings.TrimSpace(patch) == "" {
		return nil, errors.New("ApplyPatch: empty patch")
	}
	clean, err := workingTreeClean(ctx, projectRoot)
	if err != nil {
		return nil, fmt.Errorf("ApplyPatch: dirty-check: %w", err)
	}
	if !clean {
		return nil, errors.New("ApplyPatch: working tree is dirty; commit or stash first (the implementer patch and operator changes would conflate)")
	}
	tmp, err := writeTempPatch(projectRoot, patch)
	if err != nil {
		return nil, fmt.Errorf("ApplyPatch: write temp: %w", err)
	}
	// Verify cleanly applies before actually mutating the tree.
	if out, err := runGitCtx(ctx, projectRoot, "apply", "--check", tmp); err != nil {
		return nil, fmt.Errorf("ApplyPatch: git apply --check failed: %w (output: %s)", err, strings.TrimSpace(out))
	}
	if out, err := runGitCtx(ctx, projectRoot, "apply", tmp); err != nil {
		return nil, fmt.Errorf("ApplyPatch: git apply failed: %w (output: %s)", err, strings.TrimSpace(out))
	}
	stat, _ := runGitCtx(ctx, projectRoot, "apply", "--numstat", tmp)
	files := parseNumstat(stat)
	return files, nil
}

func workingTreeClean(ctx context.Context, projectRoot string) (bool, error) {
	out, err := runGitCtx(ctx, projectRoot, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

func writeTempPatch(projectRoot, patch string) (string, error) {
	// Use the project's .tribunal/convergence/ dir as the scratch
	// location so the tempfile lives alongside the audit trail.
	dir := filepath.Join(projectRoot, ".tribunal", "convergence")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "implementer-*.patch")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(patch); err != nil {
		return "", err
	}
	return f.Name(), nil
}

func runGitCtx(ctx context.Context, projectRoot string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = projectRoot
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// parseNumstat extracts file paths from `git apply --numstat` output.
// Format: "<added>\t<deleted>\t<path>" per line.
func parseNumstat(out string) []string {
	var files []string
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "\t", 3)
		if len(parts) == 3 {
			files = append(files, parts[2])
		}
	}
	return files
}
