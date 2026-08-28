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
- redundant `default` clauses in exhaustive `bool` switches
- redundant `len(src) > 0` guards before `append(dst, src...)`
- redundant `len(items) > 0`, `items != nil`, and combined guards before
  `range` loops
- redundant empty-return guards before final `range` loops
- adjacent `range` loops that repeat the same body
- one-read temp aliases like `name := req.Name`
- clusters of behavior-preserving simplifications inside one function
- input validation guards scattered after other function work
- loop performance traps like invariant membership scans, sort calls, derived
  work, N+1 database/network calls, nested lookups, and pairwise scans

Package-level smells:

- trivial private function/method wrappers, plus exported method wrappers that
  do not touch private receiver fields or methods
- doc comments that only restate private declaration names
- `IsX` predicates that do not return `bool` or `(bool, error)`
- redundant `MarshalJSON` methods covered by `MarshalText`
- repeated normalization calls like duplicate `strings.TrimSpace(name)`
- large const, var, or type chunks without blank/comment grouping
- const chunks mixing unrelated prefixes without grouping
- large table tests without case names
- tests that only check fixed package values against fixed expectations
- duplicate validation ladders
- single-use private helpers with tiny bodies
- single-implementation private interfaces
- functional options around tiny private APIs
- private parameters always passed as zero value by production uses
- unused parameters on private functions
- private result wrappers that only carry value plus status
- generic helper names when paired with another smell
- unused private functions, methods, types, vars, consts, and struct fields in production code;
  references from internal or external tests do not keep production declarations live
- exported functions, methods, types, vars, consts, and struct fields unreachable from repo entrypoints
- one source diagnostic per unreachable declaration subgraph instead of cascading through every member
- repeated test fixture contents that should live in one test builder
- complexity suppressions on functions with no decision points
- unreachable panic calls immediately after `testing.T.Fatal` or `Fatalf`
- non-generated owner files over 1,000 lines

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
- replays the complete repo result before `go list` when relevant source,
  options, toolchain, similarity policy, and stamp are unchanged

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

- Go 1.26.5+
- Linux amd64 with AVX2 and CGO for local semantic similarity checks
- Optional: authenticated Codex CLI for Luna behavior descriptions
- For full repo health checks: `golangci-lint`

## Build

```bash
go build -o ./bin/slopelint ./cmd/slopelint
```

Local semantic inference currently needs Linux amd64 and CGO. Static llama.cpp
libraries ship with the Go module, so normal `go build` and `go install` need no
C++ build step and produce one self-contained executable. CI stamp validation
works on every platform supported by the rest of slopelint.

## Run

```bash
go run ./cmd/slopelint ./...
go run ./cmd/slopelint -max-states=64 ./...
go run ./cmd/slopelint -closed-world ./...

./bin/slopelint ./...
go vet -vettool=$(pwd)/bin/slopelint ./...
```

The normal standalone command also checks named functions with at least 50 Go
tokens for semantic similarity. Local runs request only missing embeddings from
the in-process llama.cpp engine, reuse the content-addressed vector cache, and
write `.slopelint-similarity.json` after every finding is fixed or accepted:

```bash
go run ./cmd/slopelint ./...
```

First inference downloads the digest-pinned 308 MiB Jina GGUF file into the
slopelint cache. Later runs memory-map that file and compute only missing vectors.
No Ollama service or model install is required.

When an authenticated `codex` executable is available, the same lint command
runs source inference while `gpt-5.6-luna` creates three signatures for every
missing block with low reasoning. Production functions get intent, flow, and
boundary signatures. Tests get contract, scenario, and oracle signatures.
Slopelint embeds one compact labeled bundle containing all three; one embedding
avoids tripling local inference work. After either channel finds a pair, Luna
creates full purpose/input/output/processing/effect/error details, or the
test-specific subject/scenario/setup/action/assertion/fixture/contract shape,
only for reported blocks. Those details appear in the diagnostic for agent review
and never filter a match.

Source-code and behavior-description similarity are independent channels over
the same block set. Either can report a pair at its own locality-weighted
threshold. Neither channel filters, promotes, fuses with, or weakens the other.

Code in missing blocks is sent through the configured Codex service. Set
`SLOPELINT_CODEX_DESCRIPTIONS=off` when source must stay local. Description requests
use at most four Codex processes, independent of the Go CPU budget. Requests
balance source sizes, hold at most 16 blocks, and target 64 KB; one larger function
stays intact instead of being truncated. Each valid response subset is cached
before retrying missing IDs. One local embedding context consumes completed
signatures while remaining requests run; it never runs concurrent native calls.
Cached descriptions and vectors make later runs local. The complete finding set is also cached. An unchanged Git worktree
uses a source-scoped Git fingerprint and replays before package loading or model
startup. Changed or untracked Go inputs fall through to package-level replay:
unchanged package diagnostics and exported behavior summaries stay cached, while
the edited package and importers affected by a type API or used-summary change
run again. Semantic block extraction parses changed files only, retains matches
between unchanged blocks, and compares changed blocks only with locality-eligible
candidates.
Within an edited file, bound local/parameter renames and private function
declaration renames reuse prior descriptions when all other tokens match.
API names, types, literals, literal keys, and control flow remain exact.
Public function names and method names are not erased. Source vectors, pair IDs,
and CI attestations still use exact source; no summary-cache reset is required.
Linked Git worktrees share exact-result, package, vector, description, and scan
caches through the repository's common Git directory. Cached diagnostics rebase
to the active checkout. Package analysis keys use compiled content rather than
basenames, so an ordinary source rename reuses analysis. Semantic block records
also follow same-content renames; path and test/package role remain metadata for
correct finding locations and locality. Source-addressed scan snapshots let
parallel worktrees keep different branch states without overwriting each other.
Successful cold or changed runs also maintain the shared cache. Retained scan
snapshots protect vectors and descriptions still used by any recent worktree;
compact reference manifests keep that sweep independent of snapshot size.
Unreferenced blobs are removed after a one-hour concurrent-write grace period.
Analysis entries and abandoned snapshots expire after 30 days without use, old
cache schemas are removed, and non-model cache data is capped at 512 MiB. The
pinned embedding model is kept outside that cap. Exact hot cache hits do not run
the sweep.

Every finding includes a stable `sim-...` pair ID. After review, accept specific
intentional pairs and rerun the same lint command:

```bash
SLOPELINT_SIMILARITY_ACCEPT=sim-1234,sim-5678 slopelint ./...
```

`SLOPELINT_SIMILARITY_ACCEPT=all` records every current finding. Use it only for
an explicitly reviewed baseline. Unchanged accepted pairs carry forward; changed
code gets a new pair ID and needs review again.

When `CI`, Cloudflare Workers Builds' `WORKERS_CI`, or Cloudflare Pages'
`CF_PAGES` is truthy, the same standalone command never starts Codex, downloads a
model, or loads inference weights. It checks the committed semantic stamp's policy
and Git repository digest before running the normal structural lint. Missing,
stale, or obsolete stamps fail before Go starts. Local mode owns semantic analysis
and writes the reviewed attestation. The digest excludes the stamp, so committing
it does not invalidate itself.

Source-code thresholds are `0.970` in one file, `0.975` in one package, and
`0.980` across immediate sibling or parent-child packages; tests add `0.025`.
Description thresholds are `0.950` in one file and `0.960` in one package or
immediate sibling or parent-child packages; tests add `0.015`. Deeper package
branches are not compared. Structural similarity provides diagnostic context
and gates neither semantic channel. Review findings against source; similarity
is not proof of equivalent behavior.

Useful `slopelint` flags:

- `-max-states`: maximum symbolic states before widening, default `32`
- `-closed-world`: treat matched `main` packages as complete production entrypoints;
  enables repo-wide exported dead-code findings
- `-cache`: replay the complete result for unchanged repo inputs, default `true`

Useful inherited `singlechecker` flags:

- `-json`: emit machine-readable diagnostics
- `-test=false`: skip test files
- `-c=N`: show source context around diagnostics
- `-flags`: print analyzer flags as JSON

Useful env vars:

- `SLOPELINT_CACHE=0`: disable cache
- `SLOPELINT_SIMILARITY=local|ci|off`: override automatic local/CI mode;
  default is `ci` when `CI`, `WORKERS_CI`, or `CF_PAGES` is truthy, otherwise
  `local`
- `SLOPELINT_SIMILARITY_ACCEPT=id,id`: accept reviewed current pair IDs;
  `all` accepts the reviewed current baseline
- `SLOPELINT_CODEX_DESCRIPTIONS=auto|off`: use authenticated Codex descriptions
  when available, or keep semantic analysis fully local; default `auto`

All repos share one global content-addressed cache root. Content hashes and
repo-scoped scan keys prevent collisions:

- `os.UserCacheDir()/slopelint/analysis-v12`
- `os.UserCacheDir()/slopelint/similarity-v1`
- `os.UserCacheDir()/slopelint/similarity-v1/descriptions`
- `os.UserCacheDir()/slopelint/models/<model-digest>.gguf`

## Development

```bash
go test ./...
./scripts/format-code.sh
./scripts/check-code-health.sh
```

`check-code-health.sh` runs `go vet`, `slopelint` on this repo, `go test`, and
`golangci-lint run`. Local slopelint runs perform cached semantic similarity
analysis. CI runs validate the committed stamp without model inference.

For local model comparisons, see the
[benchmark run instructions](internal/lint/testdata/similarity_models/README.md).

## Current Limits

- not a general style or architecture linter
- skips path analysis for functions containing `goto`
- conservative around writes, unknown calls, loops, closures, goroutines,
  `select`, type switches, and labeled control flow
- strongest on small path facts and local/private API smells
- repo-wide dead-code reachability runs only with standalone `-closed-world`
  when loaded patterns include a `main` package; vettool mode stays package-scoped
- dead-code results cover the active `GOOS`, `GOARCH`, and build-tag configuration;
  run once per relevant configuration, for example with `GOFLAGS='-tags=mytag'`
- semantic similarity compares named functions with at least 50 tokens from the
  loaded package/build configuration; generated files and function literals are
  excluded; large functions use overlapping byte-bounded chunks, and every chunk
  is embedded without truncation; connected similarity groups report one
  finding with every member instead of every possible pair; comparisons stop
  beyond immediate sibling or parent-child packages
- Luna describes each eligible function in isolation; behavior that
  exists only in caller context or an unknown private helper can still be missed
- one semantic similarity run covers one Go module because each module owns one
  committed stamp
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
- `complexity_simplification`
- `guard_complexity`
- `loop_membership_scan`
- `loop_sort`
- `loop_invariant_work`
- `loop_external_call`
- `nested_lookup_loop`
- `pairwise_comparison_loop`
- `predicate_signature`
- `unused_private_param`
- `const_grouping`
- `var_grouping`
- `type_grouping`
- `mixed_const_prefixes`
- `table_test_grouping`
- `const_value_test`
- `repeated_test_fixture`
- `stale_complexity_suppression`
- `test_fatal_panic`
- `oversized_owner_file`
- `trivial_wrapper`
- `comment_noise`
- `serialization_ceremony`
- `normalization_ceremony`
- `duplicate_validation`
- `semantic_duplicate`
- `abstraction_overkill`
- `api_overkill`
- `result_wrapper`
- `generic_naming`
- `dead_code`
- `test_global_func_stub`
- `bool_mode_param`
- `optional_result_triple`
- `prod_must_panic`
- `test_support_filename`
- `sentinel_error_break`
- `invalid_contract`

Production-only checks exclude internal test-support packages when a module-root
`./...` scan proves they are reachable only through test imports, including
transitive helpers. Public packages, unused packages, partial scans, and the
single-package analyzer retain conservative production classification. Structural
checks still run on test-support code. Package cache keys include this role, so
adding a production importer invalidates affected findings without clearing
semantic descriptions or vectors.

Handwritten `.go` files in these shared test-support packages must use an
underscore-delimited `test_support` marker: for example,
`instrument_symbol_test_support.go` or `capture_test_support_linux.go`.
Helpers used only inside one package belong in `_test.go`; shared helpers must
remain ordinary `.go` files so other packages' tests can import them. Filenames
never establish test-only status or disable production checks. Generated files
are excluded from this naming check. Naming changes invalidate only the affected
test-support package's structural cache; ordinary source renames retain reuse.

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
Malformed `slopelint:ensures` comments emit `invalid_contract` instead of being ignored.
