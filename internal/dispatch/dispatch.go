// Package dispatch implements Tribunal's adversary-panel dispatch: a
// parallel fan-out to one or more Provider implementations, each of which
// runs an adversarial attack against a spec/diff/intent bundle. The
// synthesis layer aggregates per-member reports into shared / unique /
// divergent findings plus an overall verdict.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Provider is one model endpoint Tribunal can dispatch to. Implementations
// must be safe for concurrent use across multiple panel members (the same
// Provider may appear in the panel several times with different temperature
// or focus configurations).
type Provider interface {
	// Name returns the canonical provider name ("claude", "openai",
	// "google", "local", or a custom name for tests).
	Name() string

	// Attack runs one adversarial review with the given member
	// configuration. system is the orchestrator-assembled system prompt;
	// user is the assignment body (intent + plan + diff + reviewer reports).
	// Implementations should respect the context for cancellation.
	Attack(ctx context.Context, member PanelMember, system, user string) (*Report, error)
}

// PanelMember is one slot in the adversary panel. The same model may appear
// multiple times with different temperatures or focuses; the dispatcher
// treats each slot as a distinct agent for synthesis and reputation
// purposes.
type PanelMember struct {
	// Label is the human-readable identifier used in reports and reputation
	// (e.g. "claude-adversary-spec"). Must be unique within a panel.
	Label string `yaml:"label" json:"label"`
	// Provider names the dispatch target ("claude", "openai", ...).
	Provider string `yaml:"provider" json:"provider"`
	// Model is the provider-specific model id (e.g. "claude-opus-4-7").
	Model string `yaml:"model" json:"model"`
	// Temperature is passed through to the provider. 0 = deterministic.
	Temperature float64 `yaml:"temperature" json:"temperature"`
	// Focus shapes the system prompt: "spec", "impl", "temporal",
	// "security", "perf", "general". Empty defaults to "general".
	Focus string `yaml:"focus" json:"focus"`
	// MaxTokens caps the response length. Zero falls back to provider
	// defaults.
	MaxTokens int `yaml:"max_tokens" json:"max_tokens,omitempty"`
}

// Panel is an ordered set of panel members dispatched concurrently.
type Panel struct {
	Name    string        `yaml:"name" json:"name"`
	Members []PanelMember `yaml:"members" json:"members"`
}

// Report is one panel member's adversarial review output. The dispatch
// orchestrator collects N of these and hands them to the synthesis layer.
type Report struct {
	Member   PanelMember     `json:"member"`
	Verdict  string          `json:"verdict"`          // BREAKS | SURVIVES | INDETERMINATE
	Reason   string          `json:"reason,omitempty"` // for INDETERMINATE
	Findings []ParsedFinding `json:"findings"`
	RawText  string          `json:"raw_text"`
	Duration time.Duration   `json:"duration"`
	Error    string          `json:"error,omitempty"`
}

// ParsedFinding is one structured finding extracted from a Report's raw
// text. Parsing is best-effort; if the model produces malformed output,
// the Report's RawText is still preserved for human review.
type ParsedFinding struct {
	Category string `json:"category"`
	Severity string `json:"severity"`
	Scenario string `json:"scenario"`
	Defense  string `json:"defense,omitempty"`
}

// Verdict constants — kept as plain strings so they're easy to ingest from
// model output, but typed here for use in synthesis logic.
const (
	VerdictBreaks        = "BREAKS"
	VerdictSurvives      = "SURVIVES"
	VerdictIndeterminate = "INDETERMINATE"
)

// Registry maps provider names to live Provider implementations. The
// dispatcher resolves PanelMember.Provider strings through the registry.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{providers: map[string]Provider{}}
}

// Register associates a Provider with its Name(). Re-registering the same
// name overwrites the prior entry, which is the desired behavior for
// dependency-injected tests.
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Name()] = p
}

// Get returns the registered Provider for name, or an error if none.
func (r *Registry) Get(name string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("dispatch: no provider registered for %q", name)
	}
	return p, nil
}

// Dispatch runs every PanelMember against its Provider concurrently and
// returns the collected reports in panel order. Members whose Attack
// returns an error get a Report with Error set; the orchestrator does not
// halt on a single failure — the synthesis layer reasons over partial
// results.
func Dispatch(ctx context.Context, registry *Registry, panel Panel, system, user string) ([]*Report, error) {
	if len(panel.Members) == 0 {
		return nil, errors.New("dispatch: panel has no members")
	}
	results := make([]*Report, len(panel.Members))
	var wg sync.WaitGroup
	for i, m := range panel.Members {
		wg.Add(1)
		go func(i int, m PanelMember) {
			defer wg.Done()
			start := time.Now()
			p, err := registry.Get(m.Provider)
			if err != nil {
				results[i] = &Report{Member: m, Verdict: VerdictIndeterminate, Reason: err.Error(), Error: err.Error(), Duration: time.Since(start)}
				return
			}
			r, err := p.Attack(ctx, m, system, user)
			if err != nil {
				if r == nil {
					r = &Report{Member: m, Verdict: VerdictIndeterminate, Reason: err.Error()}
				}
				r.Error = err.Error()
				r.Duration = time.Since(start)
				results[i] = r
				return
			}
			r.Member = m
			r.Duration = time.Since(start)
			results[i] = r
		}(i, m)
	}
	wg.Wait()
	return results, nil
}

// ValidatePanel checks that the panel is non-empty, labels are unique, and
// every member has a non-empty Provider and Model.
func ValidatePanel(panel Panel) error {
	if len(panel.Members) == 0 {
		return errors.New("panel: must contain at least one member")
	}
	seen := map[string]bool{}
	for i, m := range panel.Members {
		if m.Provider == "" {
			return fmt.Errorf("panel: member %d missing provider", i)
		}
		if m.Model == "" {
			return fmt.Errorf("panel: member %d (%s) missing model", i, m.Provider)
		}
		label := m.Label
		if label == "" {
			label = fmt.Sprintf("%s-%d", m.Provider, i)
		}
		if seen[label] {
			return fmt.Errorf("panel: duplicate label %q", label)
		}
		seen[label] = true
	}
	return nil
}
