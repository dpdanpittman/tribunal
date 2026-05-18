package converge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dpdanpittman/tribunal/internal/dispatch"
)

// stubImplementer is a deterministic Implementer used by the controller-
// integration tests. Whatever's queued is returned; queue exhaustion
// returns an empty PatchOutput.
type stubImplementer struct {
	queue []PatchOutput
	calls int
	in    []PatchInput
	err   error
}

func (s *stubImplementer) Label() string { return "stub-implementer" }
func (s *stubImplementer) Patch(ctx context.Context, in PatchInput) (*PatchOutput, error) {
	s.in = append(s.in, in)
	if s.err != nil {
		return nil, s.err
	}
	if s.calls >= len(s.queue) {
		s.calls++
		return &PatchOutput{}, nil
	}
	out := s.queue[s.calls]
	s.calls++
	return &out, nil
}

// TestController_ImplementerAuthorsPatchOnFindings — when the round
// produces a Critical finding and an Implementer is configured, the
// controller calls Patch() and writes the patch + reasoning artifacts.
// AutoApply is false → no `git apply` invoked.
func TestController_ImplementerAuthorsPatchOnFindings(t *testing.T) {
	stage := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "BREAKS", Findings: []RoundFinding{
				{ClaimHash: "h1", Category: "shared_blind_spot", Severity: "critical", Member: "claude-opus-spec", Scenario: "imagined hostile LCD"},
			}, Verdicts: map[string]string{}},
		},
	}
	impl := &stubImplementer{
		queue: []PatchOutput{{
			Patch:     "diff --git a/x b/x\nindex 1..2 100644\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-foo\n+bar\n",
			Reasoning: "Add bar.",
		}},
	}
	c, target := newTestController(t, stage)
	c.Implementer = impl
	c.AutoApply = false

	res, err := c.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != StatusNeedsFixes {
		t.Fatalf("status=%s want %s", res.Status, StatusNeedsFixes)
	}
	if impl.calls != 1 {
		t.Fatalf("implementer calls=%d want 1", impl.calls)
	}
	round := res.Rounds[0]
	if !round.PatchAuthored {
		t.Fatalf("round.PatchAuthored=false (want true)")
	}
	if round.PatchApplied {
		t.Fatalf("AutoApply=false but PatchApplied=true")
	}
	if _, err := os.Stat(round.PatchPath); err != nil {
		t.Fatalf("patch file not at %s: %v", round.PatchPath, err)
	}
	if _, err := os.Stat(round.PatchReadme); err != nil {
		t.Fatalf("readme file not at %s: %v", round.PatchReadme, err)
	}
	// Reason should mention the patch path.
	if !strings.Contains(res.Reason, round.PatchPath) {
		t.Fatalf("result.Reason should reference patch path: %q", res.Reason)
	}
}

// TestController_ImplementerOnlyForActionableFindings — Suggestion-
// severity findings don't trigger the implementer. The loop keeps
// running (suggestion doesn't gate release).
func TestController_ImplementerNotInvokedForSuggestionsOnly(t *testing.T) {
	stage := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "SURVIVES", Findings: []RoundFinding{
				{ClaimHash: "h-sugg", Severity: "suggestion", Member: "x"},
			}, Verdicts: map[string]string{}},
			{OverallVerdict: "SURVIVES", Verdicts: map[string]string{}},
		},
	}
	impl := &stubImplementer{queue: []PatchOutput{{Patch: "diff"}}}
	c, target := newTestController(t, stage)
	c.Implementer = impl

	res, err := c.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if impl.calls != 0 {
		t.Fatalf("implementer should not be called on suggestion-only round (calls=%d)", impl.calls)
	}
	if res.Status != StatusConverged {
		t.Fatalf("status=%s want converged", res.Status)
	}
}

// TestController_ImplementerRefuse — implementer returns Refused=true;
// controller persists reasoning but no patch file.
func TestController_ImplementerRefuse(t *testing.T) {
	stage := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "BREAKS", Findings: []RoundFinding{
				{ClaimHash: "h1", Severity: "critical", Member: "x"},
			}, Verdicts: map[string]string{}},
		},
	}
	impl := &stubImplementer{queue: []PatchOutput{{Refused: true, Reasoning: "Architectural change required."}}}
	c, target := newTestController(t, stage)
	c.Implementer = impl

	res, err := c.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != StatusNeedsFixes {
		t.Fatalf("status=%s want needs_fixes", res.Status)
	}
	r := res.Rounds[0]
	if r.PatchAuthored {
		t.Fatalf("refused should not mark PatchAuthored")
	}
	if !r.PatchRefused {
		t.Fatalf("PatchRefused should be true")
	}
	if r.PatchReadme == "" {
		t.Fatalf("refused should still persist the reasoning readme")
	}
}

// TestController_ImplementerPropagatesIntent — IntentLoader hook is
// invoked and forwarded to the PatchInput.
func TestController_ImplementerPropagatesContext(t *testing.T) {
	stage := &stubStage{
		queue: []RoundOutput{
			{OverallVerdict: "BREAKS", Findings: []RoundFinding{
				{ClaimHash: "h1", Severity: "critical", Member: "x"},
			}, Verdicts: map[string]string{}},
		},
	}
	impl := &stubImplementer{queue: []PatchOutput{{Patch: "diff --git a/x b/x"}}}
	c, target := newTestController(t, stage)
	c.Implementer = impl
	c.IntentLoader = func(planID string) string {
		if planID != "P-test" {
			t.Errorf("IntentLoader got planID=%q", planID)
		}
		return "intent text"
	}
	c.DiffLoader = func(t ConvergenceTarget) string { return "diff text" }
	c.FindingBodyLookup = func(fs []RoundFinding) map[string]string {
		return map[string]string{fs[0].ClaimHash: "full body"}
	}

	_, err := c.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(impl.in) != 1 {
		t.Fatalf("implementer in=%d want 1", len(impl.in))
	}
	in := impl.in[0]
	if in.Intent != "intent text" || in.Diff != "diff text" {
		t.Fatalf("context not forwarded: intent=%q diff=%q", in.Intent, in.Diff)
	}
	if in.FindingBodies["h1"] != "full body" {
		t.Fatalf("FindingBodies not forwarded: %+v", in.FindingBodies)
	}
}

// TestApplyPatch_RefusesOnDirtyTree — verify the safety guard fires
// when the working tree has uncommitted changes.
func TestApplyPatch_RefusesOnDirtyTree(t *testing.T) {
	dir := initGitRepo(t)
	// Add an uncommitted change to dirty the tree.
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := ApplyPatch(ctx, dir, "diff --git a/foo b/foo\nindex 0..1 100644\n--- a/foo\n+++ b/foo\n@@\n-x\n+y\n")
	if err == nil {
		t.Fatalf("expected dirty-tree refusal")
	}
	if !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("error message should mention dirty tree: %v", err)
	}
}

// TestApplyPatch_AppliesToCleanTree — happy path: working tree clean,
// patch validates and applies, the target file is modified.
func TestApplyPatch_AppliesToCleanTree(t *testing.T) {
	dir := initGitRepo(t)
	target := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(target, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOrDie(t, dir, "add", "hello.txt")
	gitOrDie(t, dir, "commit", "-m", "add hello")

	patch := "diff --git a/hello.txt b/hello.txt\nindex 0000000..1111111 100644\n--- a/hello.txt\n+++ b/hello.txt\n@@ -1 +1 @@\n-hello\n+world\n"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	files, err := ApplyPatch(ctx, dir, patch)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(files) != 1 || files[0] != "hello.txt" {
		t.Fatalf("files=%v want [hello.txt]", files)
	}
	body, _ := os.ReadFile(target)
	if strings.TrimSpace(string(body)) != "world" {
		t.Fatalf("file contents after apply: %q want %q", string(body), "world")
	}
}

// TestParseImplementerResponse — pin the parser against the documented
// response shape.
func TestParseImplementerResponse(t *testing.T) {
	resp := `Some preamble that should be ignored.

REASONING:
This is the reasoning.
Two paragraphs.

PATCH:
` + "```diff" + `
diff --git a/x b/x
--- a/x
+++ b/x
@@ -1 +1 @@
-old
+new
` + "```" + `
`
	patch, reasoning, refused := parseImplementerResponse(resp)
	if refused {
		t.Fatalf("expected non-refused")
	}
	if !strings.Contains(reasoning, "This is the reasoning.") {
		t.Fatalf("reasoning drift: %q", reasoning)
	}
	if !strings.Contains(patch, "diff --git a/x b/x") {
		t.Fatalf("patch drift: %q", patch)
	}
	// REFUSE handling
	refuseResp := "REASONING:\nNo patch possible.\n\nPATCH:\n```diff\n# REFUSE\n```"
	_, _, refused = parseImplementerResponse(refuseResp)
	if !refused {
		t.Fatalf("expected refused on # REFUSE block")
	}
}

// initGitRepo creates a temp dir, runs `git init`, configures user, and
// returns the path. Helper for ApplyPatch tests.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitOrDie(t, dir, "init", "-q")
	gitOrDie(t, dir, "config", "user.email", "test@example.com")
	gitOrDie(t, dir, "config", "user.name", "Test")
	gitOrDie(t, dir, "config", "commit.gpgsign", "false")
	// Stamp an initial commit so we can diff against HEAD later if needed.
	if err := os.WriteFile(filepath.Join(dir, ".gitkeep"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOrDie(t, dir, "add", ".gitkeep")
	gitOrDie(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func gitOrDie(t *testing.T, dir string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := runGitCtx(ctx, dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v (out=%s)", args, err, out)
	}
}

// silence unused-import warnings when test trimming happens
var _ = dispatch.PanelMember{}
