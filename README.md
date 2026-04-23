# slopelint

`slopelint` is a Go analyzer for preventing code slop in Go codebases.

It targets code that keeps getting longer, noisier, and more ceremonial without
adding meaning: redundant guards, dead branches, copied validation, trivial
wrappers, and comments that only restate code.

LLM output is one source of that slop, not the only one. The same patterns show
up after rushed refactors, cargo-cult defensive programming, and copy-paste
maintenance.

## Current focus

Today `slopelint` is strongest at path-sensitive redundancy and a small set of
structural simplifications.

Shipped rules:

- repeated `nil`, empty-string, boolean, enum, or `len(...)` checks after guard
  clauses
- redundant right-hand side of `&&` / `||`
- unreachable `switch` cases on already-filtered path
- boolean-return ceremony like `if cond { return true }; return false`
- identical `if` / `else` or adjacent `switch` branch bodies
- redundant `default` in exhaustive `bool` switches
- one-read temp aliases like `name := req.Name`

Experimental rules behind `-experimental`:

- trivial private wrappers with one production callsite
- doc comments that only restate private declaration names

## Why this exists

Classic Go linters already cover many syntax bugs, suspicious APIs, and generic
style rules.

`slopelint` goes after a different failure mode: code that looks cautious or
structured, but no longer carries real information.

Examples:

- checks that are already proven true or false
- branch structure that can be merged or deleted
- helpers and locals that add indirection without meaning
- comments that explain nothing

## How it works

`slopelint` runs on top of `go/analysis`.

It parses and type-checks matched packages, then tracks small path facts for:

- `x == nil` / `x != nil`
- `name == ""` / `name != ""`
- `len(items) == 0` / `len(items) > 0`
- boolean facts
- selector chains like `req.User.Role == Admin`

It also:

- preserves simple aliases like `name := req.Name`
- derives facts from helper return values
- imports and exports summaries across same-repo package boundaries
- propagates boolean and `error`/`nil`-style result facts
- supports opt-in contracts via `//slopelint:ensures ...`
- caches diagnostics and exported summaries on disk for faster reruns

## Example

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

## Build

```bash
go build -o slopelint ./cmd/slopelint
```

## Run

```bash
./slopelint ./...
./slopelint ./internal/...
./slopelint -max-states=64 ./...
./slopelint -experimental ./...
go vet -vettool=$(pwd)/slopelint ./...
```

Useful flags:

- `-max-states`: widening cap for symbolic paths
- `-cache=false`: disable persistent cache
- `-cache-dir=/path/to/cache`: override cache location
- `-experimental`: turn on smell checks

Useful env vars:

- `SLOPELINT_CACHE=0`
- `SLOPELINT_CACHE_DIR=/path/to/cache`

Legacy aliases still work:

- `DEFENSELINT_CACHE=0`
- `DEFENSELINT_CACHE_DIR=/path/to/cache`

Default cache location: `os.UserCacheDir()/slopelint/analysis-v*`

## Current limits

- not a general style or architecture linter
- skips functions containing `goto`
- conservative around writes, unknown calls, loops, `select`, `type switch`,
  labeled control flow
- strongest today on path-based proof; broader smell rules stay experimental
- weak at type-level semantic meaning
- report-only today; no custom autofix implementation yet

## Rule IDs

Machine-readable categories emitted today:

- `redundant_condition`
- `redundant_subexpression`
- `unreachable_case`
- `boolean_ceremony`
- `control_flow_merge`
- `redundant_default`
- `temp_alias`

Experimental categories:

- `trivial_wrapper`
- `comment_noise`

## Contracts

Use contracts when helper body is unavailable or when you want guarantees to be
explicit:

```go
//slopelint:ensures req != nil
//slopelint:ensures req.Name != ""
func requireReq(req *Req) {}
```

After `requireReq(req)`, reachable path inherits those facts.

## Roadmap

Planned checks, rule lanes, priorities: [docs/rule-roadmap.md](docs/rule-roadmap.md)
