// Package trajectory wraps pgregory.net/rapid's stateful-PBT primitive
// (rapid.T.Repeat) in a Tribunal-idiomatic shape so temporal-lens
// findings about state-machine properties can be encoded as executable
// tests with minimal boilerplate.
//
// The motivation, from ADR-0003 M3: the temporal lens identifies
// trajectory properties ("no surgical edit pass should produce a
// portrait that differs from any prior portrait by more than X
// tokens"). Those properties are prose findings. To make them
// load-bearing — i.e., enforceable in CI rather than diagnostic only —
// the operator (or an implementer agent) encodes the finding as a
// Property and registers it as a `_test.go` file in their repo. rapid
// generates random operation sequences and checks the invariants
// after every operation; counterexamples are shrunk to a minimal
// failing trajectory.
//
// The scaffold is intentionally thin. rapid does the heavy lifting; we
// add (a) named composition of multiple invariants, (b) a stable
// counterexample shape that ties back to a Tribunal finding ID, and
// (c) the convention that the empty-key entry in t.Repeat is reserved
// for invariant checking. Operators who already know rapid can use it
// directly — Property exists to standardise the idiom across temporal
// tests in different repos.
package trajectory

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// Property is a named state-machine property derived from a
// temporal-lens finding. One Property typically corresponds to one
// finding (FindingID is the back-reference). When the temporal lens
// files a finding with category=temporal_invariant, the operator's job
// is to encode the property as a Property struct and register it as a
// _test.go file.
type Property struct {
	// FindingID ties the property to the finding it enforces. May be
	// empty for properties not yet filed as findings (e.g., during
	// initial development).
	FindingID string

	// Name is a short identifier used in test output and counterexample
	// reports. Convention: short, kebab-case, scoped to the system under
	// test ("portrait-prune-preserves-load-bearing").
	Name string

	// Description is one-line prose for the property. Surfaced when a
	// counterexample is reported so operators can diagnose without
	// chasing the finding.
	Description string

	// SetUp builds a fresh system-under-test for one rapid run. Called
	// once per property check (rapid will invoke the check many times
	// with different operation sequences). May draw initial values via
	// rapid generators.
	SetUp func(*rapid.T) SUT

	// Operations is the set of state-transition functions. Each key is
	// the operation name (used in rapid's shrinker output); each value
	// applies a randomised operation against the SUT. Empty strings are
	// reserved for invariants — do not register an operation under "".
	Operations map[string]func(*rapid.T, SUT)

	// Invariants run after every operation rapid invokes. Each invariant
	// is named for diagnostic clarity. Failure should call t.Fatalf or
	// t.Errorf with a self-contained message; rapid will shrink the
	// failing trajectory to a minimal counterexample automatically.
	Invariants []Invariant
}

// SUT is the system-under-test for a Property. Operators define their
// own concrete type (struct, interface, etc.); the scaffold just passes
// it through. Use `any` here so each Property can name its own SUT
// shape; type-assert inside Operations and Invariants.
type SUT any

// Invariant is a named property check run after every operation.
// Failures should call t.Fatalf or t.Errorf with enough detail that
// rapid's shrunk counterexample is diagnosable on its own.
type Invariant struct {
	Name  string
	Check func(*rapid.T, SUT)
}

// Run executes the property under rapid's PBT engine. On failure,
// rapid reports the shrunk operation sequence; the report includes the
// Property's Name, Description, and FindingID so the failure is
// traceable to the lens finding that motivated the test.
//
// Use this in standard `Test*` functions:
//
//	func TestPortraitPrunePreservesLoadBearing(t *testing.T) {
//	    p := trajectory.Property{...}
//	    trajectory.Run(t, p)
//	}
//
// The scaffold intentionally does not accept rapid options (seed,
// numTests, etc.) — operators who need them should call rapid.Check
// directly with a hand-rolled function. The scaffold's job is the
// common case.
func Run(t *testing.T, p Property) {
	t.Helper()
	if err := p.validate(); err != nil {
		t.Fatalf("trajectory: invalid Property %q: %v", p.Name, err)
	}
	rapid.Check(t, func(rt *rapid.T) {
		sut := p.SetUp(rt)
		repeat := make(map[string]func(*rapid.T), len(p.Operations)+1)
		for name, op := range p.Operations {
			op := op
			repeat[name] = func(rt *rapid.T) { op(rt, sut) }
		}
		repeat[""] = func(rt *rapid.T) {
			for _, inv := range p.Invariants {
				inv.Check(rt, sut)
			}
		}
		rt.Repeat(repeat)
	})
}

// validate returns an error if the Property is missing required fields
// or violates the scaffold's conventions. Run calls validate before
// invoking rapid so misconfigurations surface immediately instead of
// after a long rapid run.
func (p Property) validate() error {
	if p.Name == "" {
		return missingFieldErr("Name")
	}
	if p.SetUp == nil {
		return missingFieldErr("SetUp")
	}
	if len(p.Operations) == 0 {
		return missingFieldErr("Operations (at least one)")
	}
	if _, ok := p.Operations[""]; ok {
		return reservedKeyErr()
	}
	if len(p.Invariants) == 0 {
		return missingFieldErr("Invariants (at least one)")
	}
	return nil
}

type validateErr string

func (e validateErr) Error() string { return string(e) }

func missingFieldErr(field string) error {
	return validateErr(strings.Join([]string{"missing required field:", field}, " "))
}

func reservedKeyErr() error {
	return validateErr(`operation name "" is reserved for invariants; rename it`)
}
