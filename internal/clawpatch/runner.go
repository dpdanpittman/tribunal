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
