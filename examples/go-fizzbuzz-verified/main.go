// Package main is the Tribunal v0.1 walkthrough example: a tiny function
// reviewed and verified end-to-end through the Tribunal methodology.
//
// Run `go test ./...` to exercise the verification pyramid for this
// example. See intent.md for the load-bearing spec and .tribunal/ for the
// signed ledger walkthrough.
package main

import (
	"errors"
	"fmt"
	"strconv"
)

// ErrNegativeInput is the sentinel value passed to panic when FizzBuzz
// receives n < 0. Callers should not recover from this — it indicates a
// programmer error rather than a runtime condition.
var ErrNegativeInput = errors.New("fizzbuzz: negative input is forbidden")

// FizzBuzz returns the canonical FizzBuzz string for the given non-negative
// integer.
//
// Rules (from intent.md §2 and §3):
//
//   - n divisible by 15 → "FizzBuzz"
//   - n divisible by 3 (but not 5) → "Fizz"
//   - n divisible by 5 (but not 3) → "Buzz"
//   - otherwise → strconv.Itoa(n)
//
// Note that 0 returns "FizzBuzz" because 0 % 3 == 0 and 0 % 5 == 0 —
// there is intentionally no special case for zero.
//
// Panics with ErrNegativeInput if n < 0.
func FizzBuzz(n int) string {
	if n < 0 {
		panic(ErrNegativeInput)
	}
	switch {
	case n%15 == 0:
		return "FizzBuzz"
	case n%3 == 0:
		return "Fizz"
	case n%5 == 0:
		return "Buzz"
	default:
		return strconv.Itoa(n)
	}
}

func main() {
	for i := 0; i < 20; i++ {
		fmt.Println(FizzBuzz(i))
	}
}
