// Package verify implements the Tribunal verification pyramid: a sequenced
// run of language-appropriate correctness tools (build → format → vet →
// lint → test → fuzz → ...) where each layer routes its failure to the
// classifier and halts the pipeline.
package verify

import "time"

// LayerStatus is the outcome of one pyramid layer.
type LayerStatus string

const (
	StatusPassed        LayerStatus = "passed"
	StatusFailed        LayerStatus = "failed"
	StatusSkipped       LayerStatus = "skipped"        // user excluded it
	StatusNotApplicable LayerStatus = "not_applicable" // tool unavailable or layer doesn't apply
)

// LayerResult captures one layer's outcome with enough detail for both
// reporting and classifier hand-off.
type LayerResult struct {
	Layer    string        `json:"layer"`             // canonical layer name, e.g. "go-vet"
	Tool     string        `json:"tool"`              // the binary or function invoked
	Status   LayerStatus   `json:"status"`            // passed | failed | skipped | not_applicable
	Duration time.Duration `json:"duration"`          // wall-clock time
	Command  []string      `json:"command,omitempty"` // argv if executed
	Stdout   string        `json:"stdout,omitempty"`  // captured stdout, possibly truncated
	Stderr   string        `json:"stderr,omitempty"`  // captured stderr, possibly truncated
	Note     string        `json:"note,omitempty"`    // human-readable reason for skipped / not_applicable
	ExitCode int           `json:"exit_code,omitempty"`
}

// PyramidReport is the full output of a verification run.
type PyramidReport struct {
	ProjectRoot     string        `json:"project_root"`
	Started         time.Time     `json:"started"`
	Completed       time.Time     `json:"completed"`
	HaltOnFailure   bool          `json:"halt_on_failure"`
	LayersExcluded  []string      `json:"layers_excluded,omitempty"`
	Layers          []LayerResult `json:"layers"`
	OverallPassed   bool          `json:"overall_passed"`
	SuggestedAction string        `json:"suggested_action,omitempty"`
}

// Counts returns the aggregate counts by status, useful for one-line
// summaries.
func (r *PyramidReport) Counts() (passed, failed, skipped, notApplicable int) {
	for _, l := range r.Layers {
		switch l.Status {
		case StatusPassed:
			passed++
		case StatusFailed:
			failed++
		case StatusSkipped:
			skipped++
		case StatusNotApplicable:
			notApplicable++
		}
	}
	return
}
