# defenselint

`defenselint` is a standalone Go linter that looks for defensive checks that are
already impossible or already guaranteed on the current control-flow path.

It is aimed at the kind of redundant `if` logic that shows up in hand-written or
AI-generated Go code, for example:

- repeated `nil` checks after an early return
- repeated empty-string / zero-value checks after a guard clause
- switch cases that can no longer match on the current path
- redundant right-hand sides of `&&` and `||` expressions
- struct-field checks that are already filtered by earlier conditions

## What it does

The linter parses and type-checks matched packages, then symbolically tracks a
small fact set for local variables and selector chains such as:

- `x == nil` / `x != nil`
- `name == ""` / `name != ""`
- `ok == true` / `ok == false`
- `req.User.Role == Admin` / `req.User.Role != Admin`

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
- single-pass loop analysis with widening
- copy propagation for simple assignments like `name := req.Name`
- field tracking for selector chains like `req.User.Role`
- conservative invalidation across calls and writes

## Current limits

To keep the implementation predictable, the linter skips functions containing
control flow that would require a more global CFG model:

- `goto`
- labeled `break` / `continue`
- `fallthrough`
- `select`
- `type switch`

It also does **not** try to infer semantic guarantees from named types alone.
For example, a type like `type SanitizedString string` is still just a string to
this analyzer unless earlier control flow already filtered its possible values.

That means the tool is strong at **path-based filtering** and intentionally weak
at **type-level semantic inference**.
