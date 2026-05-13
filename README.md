# slopelint

> [!WARNING]
> `slopelint` is still **early-stage**. Expect rough edges, breaking changes, and
> conservative misses.

`slopelint` is a Go analyzer for catching code slop: code that gets longer,
noisier, or more ceremonial without carrying more meaning.

It targets redundant guards, unreachable branches, copied validation, trivial
wrappers, overbuilt private APIs, and comments that only restate names. LLM
output is one source of that slop, not the only one; the same patterns show up
after rushed refactors, defensive copy-paste, and cargo-cult abstractions.

## What It Catches

Path-proven redundancy:

- repeated `nil`, empty-string, boolean, enum, or `len(...)` checks after guard
  clauses
- redundant left or right sides of `&&` / `||`
- repeated no-arg `IsX()` predicate checks after guard clauses
- unreachable `switch` cases on already-filtered paths

Structural ceremony:

- boolean-return or boolean-assignment ceremony like
  `if cond { return true }; return false`
- identical `if` / `else` bodies
- guard returns that duplicate the following fallback return
- nested final `if` pyramids that can become guard clauses
- adjacent `switch` cases with identical bodies
- redundant `default` clauses in exhaustive `bool` and closed const-set switches
- redundant `len(src) > 0` guards before `append(dst, src...)`
- redundant `len(items) > 0`, `items != nil`, and combined guards before
  `range` loops
- redundant empty-return guards before final `range` loops
- adjacent `range` loops that repeat the same body
- one-read temp aliases like `name := req.Name`

Package-level smells:

- trivial private wrappers with one production callsite
- doc comments that only restate private declaration names
- `IsX` predicates that do not return `bool` or `(bool, error)`
- redundant `MarshalJSON` methods covered by `MarshalText`
- repeated normalization calls like duplicate `strings.TrimSpace(name)`
- large const, var, or type chunks without blank/comment grouping
- const chunks mixing unrelated prefixes without grouping
- large table tests without case names
- duplicate validation ladders
- single-use private helpers with tiny bodies
- single-implementation private interfaces
- functional options around tiny private APIs
- private parameters always passed as zero value by production callsites
- private result wrappers that only carry value plus status
- generic helper names when paired with another smell
- unused private functions, methods, types, vars, consts, and struct fields in production code
- exported functions, methods, types, vars, consts, and struct fields unreachable from repo entrypoints

## How It Works

`slopelint` runs on top of `go/analysis`. It parses and type-checks matched
packages, then tracks small path facts for:

- `x == nil` / `x != nil`
- `name == ""` / `name != ""`
- `len(items) == 0` / `len(items) > 0`
- booleans
- no-arg `IsX()` predicate results
- selector chains like `req.User.Role == Admin`

It also:

- preserves simple aliases like `name := req.Name`
- invalidates facts conservatively after writes, unknown calls, loops, closures,
  goroutines, `select`, and type switches
- infers helper summaries for guard-like functions
- exports `go/analysis` facts so imported helpers can carry summaries across
  package boundaries
- propagates boolean and `error`/`nil`-style result facts
- supports opt-in contracts via `//slopelint:ensures ...`
- caches diagnostics and exported summaries on disk for faster reruns

## Examples

Path redundancy:

```go
func valid(req *Req) bool {
	if req == nil {
		return false
	}
	if req.Name == "" {
		return false
	}
	return true
}

func handle(req *Req) {
	if !valid(req) {
		return
	}

	if req == nil { // reported
		panic("dead")
	}
	if req.Name == "" { // reported
		panic("dead")
	}
}
```

Trivial wrapper:

```go
func execute(name string) bool { return name != "" }

func run(name string) bool {
	ok := execute(name)
	return ok // reported
}

func use(name string) bool {
	return run(name)
}
```

Repeated normalization:

```go
func defaultName(name string) string {
	if strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name) // reported
	}

	return "fallback"
}
```

## Requirements

- Go 1.22+
- For full repo health checks: `golangci-lint`, Node.js, and `npx`

## Build

```bash
go build -o ./bin/slopelint ./cmd/slopelint
```

## Run

```bash
go run ./cmd/slopelint ./...
go run ./cmd/slopelint -max-states=64 ./...

./bin/slopelint ./...
go vet -vettool=$(pwd)/bin/slopelint ./...
```

Useful `slopelint` flags:

- `-max-states`: maximum symbolic states before widening, default `32`

Useful inherited `singlechecker` flags:

- `-json`: emit machine-readable diagnostics
- `-test=false`: skip test files
- `-c=N`: show source context around diagnostics
- `-flags`: print analyzer flags as JSON
- `-cache`: reuse cached analysis for unchanged packages, default `true`
- `-cache-dir=/path/to/cache`: override persistent cache location

Useful env vars:

- `SLOPELINT_CACHE=0`: disable cache
- `SLOPELINT_CACHE_DIR=/path/to/cache`: override cache root

Default cache location: `os.UserCacheDir()/slopelint/analysis-v2`

## Development

```bash
go test ./...
./scripts/format-code.sh
./scripts/check-code-health.sh
```

`check-code-health.sh` runs `go vet`, `slopelint` on this repo, `go test`,
`golangci-lint run`, and `jscpd` with production-code clone threshold `0`.

## Current Limits

- not a general style or architecture linter
- skips path analysis for functions containing `goto`
- conservative around writes, unknown calls, loops, closures, goroutines,
  `select`, type switches, and labeled control flow
- strongest on small path facts and local/private API smells
- repo-wide dead-code reachability runs in standalone `slopelint` mode when
  loaded patterns include a `main` package; vettool mode stays package-scoped
- weak at type-level semantic meaning
- reports only; no custom autofix implementation yet

## Rule IDs

Machine-readable diagnostic categories emitted today:

- `redundant_condition`
- `redundant_subexpression`
- `unreachable_case`
- `boolean_ceremony`
- `control_flow_merge`
- `redundant_default`
- `append_ceremony`
- `loop_ceremony`
- `temp_alias`
- `predicate_signature`
- `const_grouping`
- `var_grouping`
- `type_grouping`
- `mixed_const_prefixes`
- `table_test_grouping`
- `trivial_wrapper`
- `comment_noise`
- `serialization_ceremony`
- `normalization_ceremony`
- `duplicate_validation`
- `abstraction_overkill`
- `api_overkill`
- `result_wrapper`
- `generic_naming`
- `dead_code`
- `test_global_func_stub`
- `bool_mode_param`
- `optional_result_triple`
- `prod_must_panic`
- `sentinel_error_break`

## Contracts

Use contracts when a helper body is unavailable, or when a guarantee should be
explicit:

```go
//slopelint:ensures req != nil
//slopelint:ensures req.Name != ""
func requireReq(req *Req) {}
```

After `requireReq(req)`, reachable paths inherit those facts.

Contract form:

```text
//slopelint:ensures <param-or-receiver>[.<field>...] ==|!= <scalar>
```

Supported scalars: `nil`, `true`, `false`, quoted strings, and base-10 ints.
Receiver contracts work on named receivers.
