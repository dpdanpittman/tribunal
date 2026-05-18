package ledger

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Entry is the discriminated union of ledger lines. Read scans into Entry
// and the caller switches on Kind to handle Finding vs Resolution.
type Entry struct {
	Kind       Kind        `json:"kind"`
	Finding    *Finding    `json:"-"`
	Resolution *Resolution `json:"-"`
	// Raw is the unparsed JSON for the line; useful when an unknown Kind
	// appears (forward-compat without losing data).
	Raw json.RawMessage `json:"-"`
}

// Ledger is the on-disk append-only JSONL store at <root>/ledger.jsonl.
// All operations are safe to call sequentially on a single Ledger value;
// concurrent callers must wrap with their own mutex.
type Ledger struct {
	path string
}

// New returns a Ledger at the given absolute path. The file is created on
// first Append.
func New(path string) *Ledger {
	return &Ledger{path: path}
}

// Path returns the on-disk path of the ledger.
func (l *Ledger) Path() string {
	return l.path
}

// AppendFinding signs (if not already signed) and writes a Finding as a
// single JSONL line. The Finding must already be signed — pass the agent
// keypair via Finding.Sign before calling.
func (l *Ledger) AppendFinding(f *Finding) error {
	if err := f.Verify(); err != nil {
		return fmt.Errorf("ledger: refuse to write unsigned/invalid finding %s: %w", f.FindingID, err)
	}
	return l.appendJSON(f)
}

// AppendResolution writes a signed Resolution to the ledger.
func (l *Ledger) AppendResolution(r *Resolution) error {
	if err := r.Verify(); err != nil {
		return fmt.Errorf("ledger: refuse to write unsigned/invalid resolution for %s: %w", r.FindingID, err)
	}
	return l.appendJSON(r)
}

// AppendTriage writes a signed TriageEvent to the ledger. Multiple events
// per finding are expected; readers reduce them in file order to get the
// current state.
func (l *Ledger) AppendTriage(t *TriageEvent) error {
	if err := t.Verify(); err != nil {
		return fmt.Errorf("ledger: refuse to write unsigned/invalid triage event for %s: %w", t.FindingID, err)
	}
	return l.appendJSON(t)
}

func (l *Ledger) appendJSON(v any) error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	data = append(data, '\n')
	if _, err := f.Write(data); err != nil {
		return err
	}
	return nil
}

// All reads the full ledger from disk and returns Findings + Resolutions
// in file order. Use this for reputation calculation and audits.
func (l *Ledger) All() ([]*Finding, []*Resolution, error) {
	f, err := os.Open(l.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	defer f.Close()

	var findings []*Finding
	var resolutions []*Resolution

	scanner := bufio.NewScanner(f)
	// Findings can carry long claim_uri values; permit large lines.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		// Peek at Kind.
		var stub struct {
			Kind Kind `json:"kind"`
		}
		if err := json.Unmarshal(raw, &stub); err != nil {
			return nil, nil, fmt.Errorf("ledger line %d: %w", lineNo, err)
		}
		switch stub.Kind {
		case KindFinding:
			var f Finding
			if err := json.Unmarshal(raw, &f); err != nil {
				return nil, nil, fmt.Errorf("ledger line %d (finding): %w", lineNo, err)
			}
			findings = append(findings, &f)
		case KindResolution:
			var r Resolution
			if err := json.Unmarshal(raw, &r); err != nil {
				return nil, nil, fmt.Errorf("ledger line %d (resolution): %w", lineNo, err)
			}
			resolutions = append(resolutions, &r)
		case KindTriage:
			// Skip here so the (Findings, Resolutions, error) signature
			// stays backward-compatible. Callers that need triage events
			// should use AllTriage() in addition to All().
			continue
		default:
			// Unknown kind: skip but don't error — forward compat.
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	return findings, resolutions, nil
}

// FindingByID returns the finding with the given ID, or nil if not present.
func (l *Ledger) FindingByID(id string) (*Finding, error) {
	findings, _, err := l.All()
	if err != nil {
		return nil, err
	}
	for _, f := range findings {
		if f.FindingID == id {
			return f, nil
		}
	}
	return nil, nil
}

// AllTriage reads every TriageEvent from the ledger in file order.
// Additive companion to All(); existing callers keep their (Findings,
// Resolutions, error) shape unchanged.
//
// To compute the *current* triage status for a finding, scan the slice in
// file order — the last matching FindingID wins.
func (l *Ledger) AllTriage() ([]*TriageEvent, error) {
	f, err := os.Open(l.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var events []*TriageEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var stub struct {
			Kind Kind `json:"kind"`
		}
		if err := json.Unmarshal(raw, &stub); err != nil {
			return nil, fmt.Errorf("ledger line %d: %w", lineNo, err)
		}
		if stub.Kind != KindTriage {
			continue
		}
		var t TriageEvent
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("ledger line %d (triage): %w", lineNo, err)
		}
		events = append(events, &t)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// LatestTriageByFinding returns the most recent triage status per
// finding, keyed by FindingID. Findings without any triage event do not
// appear in the result; callers should treat absence as TriageStatusOpen.
func (l *Ledger) LatestTriageByFinding() (map[string]*TriageEvent, error) {
	events, err := l.AllTriage()
	if err != nil {
		return nil, err
	}
	out := make(map[string]*TriageEvent, len(events))
	for _, e := range events {
		out[e.FindingID] = e // file-order overwrite = "latest wins"
	}
	return out, nil
}

// VerifyAll re-checks every signature in the ledger. Useful as an audit
// command or precondition before reputation calculation.
func (l *Ledger) VerifyAll() error {
	findings, resolutions, err := l.All()
	if err != nil {
		return err
	}
	for _, f := range findings {
		if err := f.Verify(); err != nil {
			return fmt.Errorf("verify finding %s: %w", f.FindingID, err)
		}
	}
	for _, r := range resolutions {
		if err := r.Verify(); err != nil {
			return fmt.Errorf("verify resolution %s: %w", r.FindingID, err)
		}
	}
	triage, err := l.AllTriage()
	if err != nil {
		return err
	}
	for _, t := range triage {
		if err := t.Verify(); err != nil {
			return fmt.Errorf("verify triage %s: %w", t.FindingID, err)
		}
	}
	return nil
}

// IsEmpty reports whether the on-disk ledger has no entries.
func (l *Ledger) IsEmpty() (bool, error) {
	info, err := os.Stat(l.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	return info.Size() == 0, nil
}

// DefaultPath returns the conventional ledger path inside a project's
// .tribunal/ directory.
func DefaultPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".tribunal", "ledger.jsonl")
}
