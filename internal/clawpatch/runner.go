package clawpatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Runner is a subprocess wrapper around the clawpatch CLI. The binary is
// resolved on $PATH lazily; nothing else about clawpatch's installation is
// assumed.
type Runner struct {
	// Bin is the clawpatch binary to invoke. Empty = "clawpatch" looked up
	// via exec.LookPath at Doctor() / call time.
	Bin string
	// Cwd is the project root clawpatch operates on. Required.
	Cwd string
	// StateDir overrides clawpatch's `--state-dir` (default ".clawpatch"
	// inside Cwd). Empty = clawpatch default.
	StateDir string
	// Provider passes through `--provider <name>` (e.g. "acpx"). Empty =
	// clawpatch default.
	Provider string
	// Model passes through `--model <name>`. Empty = clawpatch default.
	Model string
	// Timeout is the per-call hard timeout. Zero = 30 min, which is the
	// upper bound on a single clawpatch review against a medium-size repo.
	Timeout time.Duration
}

const defaultTimeout = 30 * time.Minute

// Doctor invokes `clawpatch doctor`. Returns nil if the install is healthy,
// or a wrapped error including exit code semantics from
// /home/dan/src/clawpatch/src/errors.ts:
//
//	exit 0 = ok
//	exit 4 = provider unavailable (acpx missing or model unresolvable)
//	other  = something else broke
func (r *Runner) Doctor(ctx context.Context) error {
	out, code, err := r.run(ctx, "doctor")
	if err != nil {
		return fmt.Errorf("clawpatch doctor failed (exit %d): %w\nstdout/stderr: %s", code, err, out.String())
	}
	return nil
}

// Map runs `clawpatch map --json` once. The result is parsed but Phase 1
// callers can typically ignore the body — its only use is to populate
// clawpatch's internal feature registry which review then consumes.
func (r *Runner) Map(ctx context.Context) (*MapResult, error) {
	out, code, err := r.run(ctx, "map")
	if err != nil {
		return nil, fmt.Errorf("clawpatch map failed (exit %d): %w\nstdout/stderr: %s", code, err, out.String())
	}
	var res MapResult
	if perr := decodeJSON(out.Bytes(), &res); perr != nil {
		return nil, fmt.Errorf("clawpatch map: parse JSON: %w (stdout was: %s)", perr, out.String())
	}
	return &res, nil
}

// Review runs `clawpatch review --json`. Note: the per-finding records
// land on disk at `.clawpatch/findings/*.json`; the returned ReviewResult
// is just a summary. Call ListFindings to pull the full records.
//
// If opts.ExportTribunalLedger is set, `--export-tribunal-ledger <path>`
// is appended and clawpatch writes a JSONL file with thin signed-ledger-
// shaped entries at that path (clawpatch PR #65). Parse it with
// ListFindingsFromExport. Useful for bulk-ingest consumers that don't
// need finding bodies.
//
// If opts.PromptFile is set, `--prompt-file <path>` is appended
// (clawpatch PR #64). Use "-" to read prompt content from stdin.
func (r *Runner) Review(ctx context.Context, opts ReviewOpts) (*ReviewResult, error) {
	args := []string{"review"}
	if opts.Limit > 0 {
		args = append(args, "--limit", fmt.Sprintf("%d", opts.Limit))
	}
	if opts.Jobs > 0 {
		args = append(args, "--jobs", fmt.Sprintf("%d", opts.Jobs))
	}
	if opts.Since != "" {
		args = append(args, "--since", opts.Since)
	}
	if opts.Feature != "" {
		args = append(args, "--feature", opts.Feature)
	}
	if opts.DryRun {
		args = append(args, "--dry-run")
	}
	if opts.PromptFile != "" {
		args = append(args, "--prompt-file", opts.PromptFile)
	}
	if opts.ExportTribunalLedger != "" {
		args = append(args, "--export-tribunal-ledger", opts.ExportTribunalLedger)
	}
	out, code, err := r.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("clawpatch review failed (exit %d): %w\nstdout/stderr: %s", code, err, out.String())
	}
	var res ReviewResult
	if perr := decodeJSON(out.Bytes(), &res); perr != nil {
		return nil, fmt.Errorf("clawpatch review: parse JSON: %w (stdout was: %s)", perr, out.String())
	}
	return &res, nil
}

// ListFindingsFromExport reads the JSONL file produced by
// `clawpatch review --export-tribunal-ledger`. Each line is one
// ExportEntry (thin, no body). Useful for consumers that bulk-ingest
// signed-ledger-shaped entries without needing finding bodies — the
// existing ListFindings is still the right call when bodies are
// required (e.g., to render lens reports).
func (r *Runner) ListFindingsFromExport(_ context.Context, path string) ([]ExportEntry, error) {
	if path == "" {
		return nil, errors.New("clawpatch.ListFindingsFromExport: path is required")
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	var out []ExportEntry
	dec := json.NewDecoder(f)
	for dec.More() {
		var e ExportEntry
		if err := dec.Decode(&e); err != nil {
			return nil, fmt.Errorf("parse export entry: %w", err)
		}
		out = append(out, e)
	}
	return out, nil
}

// Fix runs `clawpatch fix --finding <id> --json`. Exit codes:
//
//	exit 0 = patch planned (dry-run) or applied + validated (live)
//	exit 3 = dirty worktree (refuses live fix; dry-run still works)
//	exit 6 = patch applied but validation commands failed
//
// On exit 6 clawpatch still emits a valid JSON object describing the
// failed patch attempt; the wrapper returns both the parsed FixResult
// and the wrapped error so callers can distinguish "validation failed"
// from "everything broke".
func (r *Runner) Fix(ctx context.Context, opts FixOpts) (*FixResult, error) {
	if opts.Finding == "" {
		return nil, errors.New("clawpatch.Fix: Finding is required")
	}
	args := []string{"fix", "--finding", opts.Finding}
	if opts.DryRun {
		args = append(args, "--dry-run")
	}
	out, code, runErr := r.run(ctx, args...)
	// Parse first — clawpatch emits JSON even on the validation-failed exit 6.
	var res FixResult
	parseErr := decodeJSON(out.Bytes(), &res)
	if runErr != nil {
		if parseErr == nil {
			return &res, fmt.Errorf("clawpatch fix exit %d: %w", code, runErr)
		}
		return nil, fmt.Errorf("clawpatch fix failed (exit %d): %w\nstdout/stderr: %s", code, runErr, out.String())
	}
	if parseErr != nil {
		return nil, fmt.Errorf("clawpatch fix: parse JSON: %w (stdout was: %s)", parseErr, out.String())
	}
	return &res, nil
}

// Revalidate runs `clawpatch revalidate --json` in either single-finding
// or bulk mode. The returned slice has one outcome per finding the
// subprocess actually revalidated. In single-finding mode the slice has
// length 1.
func (r *Runner) Revalidate(ctx context.Context, opts RevalidateOpts) ([]RevalidateOutcome, error) {
	args := []string{"revalidate"}
	switch {
	case opts.Finding != "":
		args = append(args, "--finding", opts.Finding)
	case opts.All:
		args = append(args, "--all")
	case opts.Since != "":
		args = append(args, "--since", opts.Since)
	default:
		return nil, errors.New("clawpatch.Revalidate: one of Finding / All / Since must be set")
	}
	if opts.Limit > 0 {
		args = append(args, "--limit", fmt.Sprintf("%d", opts.Limit))
	}
	out, code, err := r.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("clawpatch revalidate failed (exit %d): %w\nstdout/stderr: %s", code, err, out.String())
	}
	// Single-finding mode returns a single object with {finding, outcome,
	// reasoning}. Bulk mode returns a summary object without per-finding
	// detail. The two shapes need different handling.
	raw := bytes.TrimSpace(out.Bytes())
	if idx := bytes.IndexByte(raw, '{'); idx > 0 {
		raw = raw[idx:]
	}
	if opts.Finding != "" {
		var single RevalidateOutcome
		if perr := json.Unmarshal(raw, &single); perr != nil {
			return nil, fmt.Errorf("clawpatch revalidate: parse JSON: %w (stdout was: %s)", perr, out.String())
		}
		return []RevalidateOutcome{single}, nil
	}
	// Bulk mode: the JSON summary doesn't carry per-finding outcomes, so
	// we re-derive them by reading clawpatch's on-disk finding files after
	// the run. The runId-by-runId history is on each FindingRecord; the
	// most recent revalidate history entry is the freshly written outcome.
	bulk, berr := r.bulkRevalidateOutcomes(ctx)
	if berr != nil {
		return nil, fmt.Errorf("clawpatch revalidate: reduce bulk outcomes: %w", berr)
	}
	return bulk, nil
}

// bulkRevalidateOutcomes reads every finding file on disk and returns the
// current (post-revalidate) status as a RevalidateOutcome list. Used by
// Revalidate when --all or --since was passed; single-finding mode reads
// the subprocess stdout directly.
func (r *Runner) bulkRevalidateOutcomes(_ context.Context) ([]RevalidateOutcome, error) {
	findings, err := r.ListFindings(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]RevalidateOutcome, 0, len(findings))
	for _, f := range findings {
		// Pull the most recent revalidate history entry's reasoning; if
		// none, leave reasoning blank.
		var reasoning string
		for i := len(f.History) - 1; i >= 0; i-- {
			h := f.History[i]
			if h.Kind != "revalidate" {
				continue
			}
			if h.Reasoning != nil {
				reasoning = *h.Reasoning
			}
			break
		}
		out = append(out, RevalidateOutcome{
			Finding:   f.FindingID,
			Outcome:   f.Status,
			Reasoning: reasoning,
		})
	}
	return out, nil
}

// Triage runs `clawpatch triage --finding <id> --status <status> --json`
// so Tribunal can push its own triage decisions back to clawpatch's local
// state. Used by `tribunal ledger triage` when a finding has a
// ClawpatchID so the two stores stay aligned.
func (r *Runner) Triage(ctx context.Context, findingID, status, note string) error {
	if findingID == "" || status == "" {
		return errors.New("clawpatch.Triage: findingID and status are required")
	}
	args := []string{"triage", "--finding", findingID, "--status", status}
	if note != "" {
		args = append(args, "--note", note)
	}
	out, code, err := r.run(ctx, args...)
	if err != nil {
		return fmt.Errorf("clawpatch triage failed (exit %d): %w\nstdout/stderr: %s", code, err, out.String())
	}
	return nil
}

// ListFindings reads every `.clawpatch/findings/<id>.json` under Cwd's
// state dir and returns them. Order is filesystem order, which is stable
// per filesystem but not lexicographic — callers that care should sort.
func (r *Runner) ListFindings(ctx context.Context) ([]Finding, error) {
	dir := r.findingsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // no review has been run yet — empty list is fine
		}
		return nil, fmt.Errorf("read findings dir %s: %w", dir, err)
	}
	out := make([]Finding, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read finding %s: %w", path, err)
		}
		var f Finding
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("parse finding %s: %w", path, err)
		}
		out = append(out, f)
	}
	return out, nil
}

// findingsDir resolves where clawpatch's per-finding JSON files live.
// Default is `<Cwd>/.clawpatch/findings/`; StateDir overrides the
// containing directory.
func (r *Runner) findingsDir() string {
	state := r.StateDir
	if state == "" {
		state = filepath.Join(r.Cwd, ".clawpatch")
	}
	return filepath.Join(state, "findings")
}

// run executes `clawpatch <args>` with global headless flags baked in. It
// returns combined stdout+stderr, the exit code (or -1 on non-exec
// failure), and an error wrapping the exit if non-zero.
//
// Note: `--json` is always passed so output is parseable. `--no-input`
// and `--quiet` are also baked in so a subprocess can't hang on prompts
// or spew progress to a pipe.
func (r *Runner) run(ctx context.Context, args ...string) (*bytes.Buffer, int, error) {
	if r.Cwd == "" {
		return nil, -1, errors.New("clawpatch.Runner: Cwd is required")
	}
	bin := r.Bin
	if bin == "" {
		var err error
		bin, err = exec.LookPath("clawpatch")
		if err != nil {
			return nil, -1, fmt.Errorf("clawpatch binary not found on PATH: %w", err)
		}
	}

	full := []string{"--json", "--no-input", "--quiet"}
	if r.StateDir != "" {
		full = append(full, "--state-dir", r.StateDir)
	}
	if r.Provider != "" {
		full = append(full, "--provider", r.Provider)
	}
	if r.Model != "" {
		full = append(full, "--model", r.Model)
	}
	full = append(full, args...)

	timeout := r.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, bin, full...)
	cmd.Dir = r.Cwd
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		code := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
		return &out, code, err
	}
	return &out, 0, nil
}

// decodeJSON unmarshals data into v. Strips a possible trailing newline
// or whitespace clawpatch may emit; tolerates leading log lines on stderr
// that the `--quiet` flag should already suppress but we defend anyway.
func decodeJSON(data []byte, v any) error {
	// clawpatch in `--json` mode emits a single JSON object on stdout. If
	// the buffer contains anything before the first `{`, drop it (defensive
	// against future log-line leaks).
	idx := bytes.IndexByte(data, '{')
	if idx > 0 {
		data = data[idx:]
	}
	return json.Unmarshal(bytes.TrimSpace(data), v)
}
