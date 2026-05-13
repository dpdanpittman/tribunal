package main

import (
	"errors"
	"strconv"
	"testing"
)

// TestFizzBuzzTable covers the canonical happy-path + boundary cases
// listed in intent.md §2.
func TestFizzBuzzTable(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		// Happy path
		{1, "1"},
		{2, "2"},
		{3, "Fizz"},
		{4, "4"},
		{5, "Buzz"},
		{6, "Fizz"},
		{10, "Buzz"},
		{15, "FizzBuzz"},
		{30, "FizzBuzz"},
		// Boundary: zero is divisible by both 3 and 5 ⇒ "FizzBuzz".
		{0, "FizzBuzz"},
		// Boundary: large numbers still return the right modular class.
		{999, "Fizz"},
		{1000, "Buzz"},
		{1_000_005, "FizzBuzz"},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.n), func(t *testing.T) {
			if got := FizzBuzz(tc.n); got != tc.want {
				t.Errorf("FizzBuzz(%d) = %q, want %q", tc.n, got, tc.want)
			}
		})
	}
}

// TestFizzBuzzInvariants exercises the four behavioral invariants from
// intent.md §3.2 over a representative range. Each invariant corresponds
// to one of the spec's tagged `state` properties.
func TestFizzBuzzInvariants(t *testing.T) {
	for n := 0; n < 200; n++ {
		got := FizzBuzz(n)
		switch {
		case n%15 == 0:
			if got != "FizzBuzz" {
				t.Errorf("invariant (div 15 → FizzBuzz) violated at n=%d: got %q", n, got)
			}
		case n%3 == 0:
			if got != "Fizz" {
				t.Errorf("invariant (div 3 → Fizz) violated at n=%d: got %q", n, got)
			}
		case n%5 == 0:
			if got != "Buzz" {
				t.Errorf("invariant (div 5 → Buzz) violated at n=%d: got %q", n, got)
			}
		default:
			if got != strconv.Itoa(n) {
				t.Errorf("invariant (otherwise → decimal) violated at n=%d: got %q", n, got)
			}
		}
	}
}

// TestFizzBuzzPanicsOnNegative confirms the failure mode in intent.md §4.
func TestFizzBuzzPanicsOnNegative(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on negative input, got none")
		}
		err, ok := r.(error)
		if !ok || !errors.Is(err, ErrNegativeInput) {
			t.Fatalf("expected ErrNegativeInput, got %v", r)
		}
	}()
	FizzBuzz(-1)
}

// FuzzFizzBuzz exercises the function against random non-negative inputs
// to guard against undocumented panics or empty returns. The fuzzer skips
// negative inputs (those are the documented panic path).
func FuzzFizzBuzz(f *testing.F) {
	f.Add(0)
	f.Add(15)
	f.Add(1000)
	f.Fuzz(func(t *testing.T, n int) {
		if n < 0 {
			t.Skip("negative inputs are the documented panic path")
		}
		got := FizzBuzz(n)
		if got == "" {
			t.Errorf("FizzBuzz(%d) returned empty string", n)
		}
		switch got {
		case "Fizz", "Buzz", "FizzBuzz":
			// ok
		default:
			if _, err := strconv.Atoi(got); err != nil {
				t.Errorf("FizzBuzz(%d) = %q is neither a special string nor a decimal", n, got)
			}
		}
	})
}
