# Intent: <System Name>

> Authored by: <author>
> Plan ID: <plan-id>
> Date: <YYYY-MM-DD>

## 1. System Identity

- **Name**: <name>
- **Scope**: <one or two sentences — single function? module? service?>
- **Purpose**: <one sentence: what does this system do for whom?>

## 2. Behaviors

Worked examples. Concrete inputs, concrete outputs. Not abstract rules.

### Happy path

- Input: `<exact value or structure>`
- Output: `<exact value or structure>`
- Notes: <if any>

### Boundary case(s)

- Input: `<boundary input>`
- Output: `<expected output>`
- Notes: <why this is a boundary>

### Explicit failure case(s)

- Input: `<input that should fail>`
- Expected failure: `<error type / panic / etc.>`
- Notes: <what the caller should observe>

### 2.5 Structured behavior blocks (state-machine systems only)

For each state transition the system performs, fill in:

#### Transition: <name>

- From state: `<state name>`
- Requires: `<precondition>`
- Forbids: `<negative precondition>`
- Produces: `<resulting state + observable side effect>`

## 3. Invariants

### 3.1 Structural invariants

- <data-shape invariant; what's always true about a data structure>
- ...

### 3.2 Behavioral invariants

For each, tag as `state` or `temporal`.

- (`state`) <invariant that holds at every system state>
- (`temporal`) <invariant about sequences of events / orderings>

## 4. Failure Modes

For each failure mode, name + cause + handling.

- **<NamedFailure>** — <cause>. Handling: <panic? typed error? silent default? recoverable?>
- ...

## 5. Non-Goals

What is explicitly out of scope. Concrete exclusions.

- We will not handle <X>.
- We will not optimize for <Y>.
- ...

## 6. Trust Boundaries

What is assumed about callers, external systems, runtime.

- Caller is trusted to <X>.
- External system <Y> is treated as <trusted / untrusted / partial>.
- Input validation begins at <boundary>.

## 7. Performance Bounds

Only fill in if performance is correctness-relevant (e.g. consensus timeouts, real-time guarantees). Otherwise: `Not applicable — performance is best-effort.`

- <bound>: <target>.
- ...

## 8. Concrete Scenarios

Three to five narrative walkthroughs. Each tells a story end-to-end.

### Scenario 1: <name>

When <trigger>, the system does <step 1>, then <step 2>, then <step 3>, ending in state <S>.

If <variant>, the system instead does <variant path>, ending in state <S'>.

### Scenario 2: <name>

...

## TBD

Anything the user explicitly declined to specify yet:

- `TBD: <thing>`
- ...
