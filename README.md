# defenselint

`defenselint` is a standalone Go linter that looks for defensive checks that are
already impossible or already guaranteed on the current control-flow path.

It is aimed at the kind of redundant `if` logic that shows up in hand-written or
AI-generated Go code, for example:

- repeated `nil` checks after an early return
- repeated empty-string / zero-value checks after a guard clause
- repeated `len(...)` checks after an earlier emptiness or non-emptiness guard
- switch cases that can no longer match on the current path
- redundant right-hand sides of `&&` and `||` expressions
- struct-field checks that are already filtered by earlier conditions
- post-guard defensive checks after helper calls, including same-repo package boundaries

## What it does

The linter parses and type-checks matched packages, then symbolically tracks a
small fact set for local variables and selector chains such as:

- `x == nil` / `x != nil`
- `name == ""` / `name != ""`
- `len(items) == 0` / `len(items) > 0`
- `ok == true` / `ok == false`
- `req.User.Role == Admin` / `req.User.Role != Admin`

It also preserves equivalence across simple copies such as `name := req.Name`, so a
later guard on `name` can still make a repeated `req.Name` check redundant.

For helper calls, the analyzer now infers summaries automatically. That lets
facts flow across call boundaries, through result variables, and across
same-repo package boundaries for patterns such as:

```go
func valid(req *Req) bool {
    if req == nil { return false }
    if req.Name == "" { return false }
    return true
}

func handle(req *Req) {
    if !valid(req) { return }
    if req == nil { println("redundant") }    // reported
    if req.Name == "" { println("redundant") } // reported
}
```

It also handles result-variable flow:

```go
func check(req *Req) error {
    if req == nil { return errors.New("bad") }
    if req.Name == "" { return errors.New("bad") }
    return nil
}

func handle(req *Req) {
    err := check(req)
    if err != nil { return }
    if req == nil { println("redundant") }    // reported
    if req.Name == "" { println("redundant") } // reported
}
```

For helper-style guard functions, you can add opt-in contracts in doc comments:

```go
//defenselint:ensures req != nil
//defenselint:ensures req.Name != ""
func requireReq(req *Req) {}
```

After `requireReq(req)`, the analyzer will treat those facts as established on the
reachable return path.

Contracts are now optional. They are mainly useful for helpers whose bodies are not
available to the analyzer or for making intended guarantees explicit.

When later control flow contradicts or duplicates those facts, it reports the
condition.

## Example

```go
func handle(req Request) {
    if req.Name == "" {
        return
    }

    if req.Name == "" { // reported: always false
        panic("unreachable")
    }

    if req.Name != "" { // reported: always true
        log.Println("already known")
    }
}
```

## Build

```bash
go build ./cmd/defenselint
```

## Run

```bash
./defenselint ./...
./defenselint ./internal/...
./defenselint -max-states=64 ./...
go vet -vettool=$(which defenselint) ./...
```

The standalone binary now uses Go's `go/analysis` driver, so the same executable
works directly, under multicheckers, or as a `go vet` tool via `-vettool`.

## Current model

The first version is intentionally conservative and focuses on the patterns that
cause the most noisy defensive code:

- path-sensitive `if` handling
- expression switches
- statement-local `select` and `type switch` fallback
- automatic call summaries for helper functions and boolean predicates
- same-repo cross-package summary import/export via `go/analysis` object facts
- result-variable propagation for boolean and nil/error-style helpers
- single-pass loop analysis with widening
- copy propagation for simple assignments like `name := req.Name`
- alias-aware fact propagation for simple symbol copies
- field tracking for selector chains like `req.User.Role`
- derived `len(...)` facts for empty vs non-empty guards
- opt-in call contracts via `//defenselint:ensures ...`
- conservative invalidation across calls and writes

## Current limits

To keep the implementation predictable, the linter skips functions containing
control flow that would require a more global CFG model:

- `goto`
- labeled `break` / `continue`
- `fallthrough`

It also does **not** try to infer semantic guarantees from named types alone.
For example, a type like `type SanitizedString string` is still just a string to
this analyzer unless earlier control flow already filtered its possible values.

That means the tool is strong at **path-based filtering** and intentionally weak
at **type-level semantic inference**.

When consumed through `go/analysis`, findings also carry machine-readable
categories such as `redundant_condition`, `redundant_subexpression`, and
`unreachable_case`.
