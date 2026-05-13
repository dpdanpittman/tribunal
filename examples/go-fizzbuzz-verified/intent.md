# Intent: FizzBuzz (Tribunal demo)

> Authored by: dpdanpittman
> Plan ID: P-fizzbuzz
> Date: 2026-05-12

This is the Tribunal v0.1 walkthrough example: a tiny function that's small enough to read in a minute and rich enough to exercise the full review + ledger pipeline.

## 1. System Identity

- **Name**: `fizzbuzz`
- **Scope**: a single Go function plus its tests.
- **Purpose**: given a non-negative integer `n`, return the standard FizzBuzz string for `n`.

## 2. Behaviors

### Happy path

- Input: `1` → Output: `"1"`
- Input: `3` → Output: `"Fizz"`
- Input: `5` → Output: `"Buzz"`
- Input: `15` → Output: `"FizzBuzz"`

### Boundary cases

- Input: `0` → Output: `"FizzBuzz"`. Zero is divisible by 3 and 5; the canonical FizzBuzz behavior is to return `"FizzBuzz"`.
- Input: the max non-negative `int` (`math.MaxInt`) → Output: `"<n>"` if not divisible by 3 or 5, else the appropriate Fizz/Buzz/FizzBuzz.

### Explicit failure case

- Input: negative integer → behavior: panic with `ErrNegativeInput`.

## 3. Invariants

### 3.1 Structural

- Return value is always a non-empty string.
- Return value is always one of: `"Fizz"`, `"Buzz"`, `"FizzBuzz"`, or the decimal representation of `n`.

### 3.2 Behavioral

- (`state`) For all `n >= 0` divisible by 15, `FizzBuzz(n) == "FizzBuzz"`.
- (`state`) For all `n >= 0` divisible by 3 but not 5, `FizzBuzz(n) == "Fizz"`.
- (`state`) For all `n >= 0` divisible by 5 but not 3, `FizzBuzz(n) == "Buzz"`.
- (`state`) For all `n >= 0` not divisible by 3 or 5, `FizzBuzz(n) == strconv.Itoa(n)`.

## 4. Failure Modes

- **`NegativeInput`** — caller passes `n < 0`. Cause: programmer error. Handling: panic with a sentinel value (callers shouldn't catch; this is unrecoverable).

## 5. Non-Goals

- We will not handle non-integer inputs (Go's type system handles that).
- We will not localize the Fizz/Buzz/FizzBuzz strings.
- We will not produce a buffered iterator for ranges (this is a single-value function).

## 6. Trust Boundaries

- Caller is trusted to pass a non-negative integer or to handle the panic on negatives. Input validation is enforced at function entry, not earlier.

## 7. Performance Bounds

Not applicable — this is a constant-time arithmetic function. Performance is not correctness-relevant for the demo's purposes.

## 8. Concrete Scenarios

### Scenario 1: a typical caller

When a CLI tool wants to print FizzBuzz from 1 to N, it loops and calls `FizzBuzz(i)` for each `i`. Each call returns a string; the tool prints it.

### Scenario 2: edge case (zero)

A caller passes `0`. The function returns `"FizzBuzz"` because `0 % 3 == 0` and `0 % 5 == 0`. This is intentional — there's no special case for zero.

### Scenario 3: failure (negative)

A caller passes `-1`. The function panics with `ErrNegativeInput`. The Go runtime unwinds the stack; the caller is responsible for either recovering or letting the program crash.

## TBD

None.
