package trajectory

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// TestProperty_Validate_RequiredFields asserts that Run rejects a
// Property missing any required field, before invoking rapid. The
// scaffold should fail fast — a half-built Property would otherwise
// produce a confusing panic deep inside rapid.
func TestProperty_Validate_RequiredFields(t *testing.T) {
	cases := []struct {
		name string
		p    Property
		want string
	}{
		{
			name: "missing Name",
			p: Property{
				SetUp:      func(*rapid.T) SUT { return 0 },
				Operations: map[string]func(*rapid.T, SUT){"op": func(*rapid.T, SUT) {}},
				Invariants: []Invariant{{Name: "i", Check: func(*rapid.T, SUT) {}}},
			},
			want: "Name",
		},
		{
			name: "missing SetUp",
			p: Property{
				Name:       "x",
				Operations: map[string]func(*rapid.T, SUT){"op": func(*rapid.T, SUT) {}},
				Invariants: []Invariant{{Name: "i", Check: func(*rapid.T, SUT) {}}},
			},
			want: "SetUp",
		},
		{
			name: "missing Operations",
			p: Property{
				Name:       "x",
				SetUp:      func(*rapid.T) SUT { return 0 },
				Invariants: []Invariant{{Name: "i", Check: func(*rapid.T, SUT) {}}},
			},
			want: "Operations",
		},
		{
			name: "missing Invariants",
			p: Property{
				Name:       "x",
				SetUp:      func(*rapid.T) SUT { return 0 },
				Operations: map[string]func(*rapid.T, SUT){"op": func(*rapid.T, SUT) {}},
			},
			want: "Invariants",
		},
		{
			name: "operation registered under reserved empty-key",
			p: Property{
				Name:       "x",
				SetUp:      func(*rapid.T) SUT { return 0 },
				Operations: map[string]func(*rapid.T, SUT){"": func(*rapid.T, SUT) {}},
				Invariants: []Invariant{{Name: "i", Check: func(*rapid.T, SUT) {}}},
			},
			want: "reserved",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.p.validate()
			if err == nil {
				t.Fatalf("expected validate to fail, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q missing substring %q", err, tc.want)
			}
		})
	}
}

// TestRun_SafeProperty_Passes runs a trivially-correct property
// against a counter SUT (operations: increment, double-increment;
// invariant: counter is always >= 0). Should pass 100 trials cleanly,
// proving the scaffold wires rapid correctly.
func TestRun_SafeProperty_Passes(t *testing.T) {
	prop := Property{
		Name:        "counter-non-negative",
		Description: "Counter never goes below zero given only increment operations.",
		SetUp: func(*rapid.T) SUT {
			c := 0
			return &c
		},
		Operations: map[string]func(*rapid.T, SUT){
			"inc": func(_ *rapid.T, sut SUT) {
				*sut.(*int)++
			},
			"inc-twice": func(_ *rapid.T, sut SUT) {
				*sut.(*int) += 2
			},
		},
		Invariants: []Invariant{
			{
				Name: "non-negative",
				Check: func(t *rapid.T, sut SUT) {
					if *sut.(*int) < 0 {
						t.Fatalf("counter went negative")
					}
				},
			},
		},
	}
	Run(t, prop)
}

// End-to-end failure behaviour (Run reporting a counterexample) is
// proved by examples/trajectory-portrait/portrait_test.go — the buggy
// SynthesizeBuggy variant trips the invariant after exactly one
// rapid action. We don't reproduce that test here because Go's
// testing package marks a parent test as failed when any subtest
// fails, making "this should fail" hard to assert in-process without
// either an exec-out-and-check-exit-code harness or a TB mock. The
// worked example serves as the integration check; this package keeps
// the scaffold's unit tests focused on validate() — the layer that
// fails fast before rapid is involved.
