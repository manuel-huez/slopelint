# slopelint

Go analyzer for redundant checks, repeated behavior, dead code, and overbuilt APIs,
with size, structure, test, review, and similarity checks for TypeScript and Python.
Built on `go/analysis`, with optional Codex descriptions for semantic similarity
and Codex review of comments and dropped errors.
Early-stage: expect breaking changes and conservative misses.

## Install and run

Requires Go 1.26.5+.

```bash
go install github.com/manuel-huez/slopelint/cmd/slopelint@v0.4.2
slopelint ./...
```

Default local runs need the [embedding server](#embedding-server) for uncached
semantic checks. For structural checks alone:

```bash
SLOPELINT_SIMILARITY=off slopelint ./...
```

Build from source or use as a vettool:

```bash
go build -o ./bin/slopelint ./cmd/slopelint
./bin/slopelint ./...
go vet -vettool="$(pwd)/bin/slopelint" ./...
```

### Languages

Standalone scans also lint tracked TypeScript (`.ts`, `.tsx`) and Python files
of the module's Git repository, parsed with pure-Go tree-sitter. A directory
pattern covers them: `./...` its tree, `./web` one directory; import-path
patterns cover none. Skipped: hidden, `node_modules`, `vendor`, and `testdata`
directories, `.d.ts`, `.gen.ts`, `.min.` files, and files with `@generated`,
`DO NOT EDIT`, or `Code generated` in the first 5 lines. A nested `go.mod`
does not stop them. A file the parser cannot read gets one `parse_error`
finding and no other rules; the parser also rejects some valid TypeScript.
These files get the size rules, [code review](#code-review),
[semantic similarity](#semantic-similarity), and the Go structure rules
below, narrowed to what one file's syntax proves. Test files (`*.test.ts`,
`*.spec.ts`, `__tests__/`, `test_*.py`, `*_test.py`, `tests/`, `conftest.py`)
get the file limit, as Go test files do, and two test rules:

- `test_sleep`: an awaited real wait (`setTimeout` promise, `sleep`, `delay`,
  `setTimeout` from `node:timers/promises`, `waitForTimeout`, `Bun.sleep`,
  `Bun.sleepSync`), or `time.sleep`/`asyncio.sleep`. A literal zero delay only
  yields. A TypeScript file that calls `useFakeTimers` is exempt, as Go files
  that use `testing/synctest` are; a Python file that patches `time.sleep` or
  `asyncio.sleep` is exempt for that function. freezegun and time-machine do
  not count: they fake the clock, not sleep.
- `test_focused` (TypeScript only): `.only` on `describe`/`it`/`test`/`suite`,
  `fit`, or `fdescribe`.

Structure rules for TypeScript and Python, in all files:

- `control_flow_merge`: identical if and else bodies; adjacent `else if` or
  `elif` arms with identical bodies (join the conditions); adjacent `case`s
  with identical bodies that end in a jump; and guard runs that return what
  the next `return` returns. A guard counts only when its condition reads
  names and literals: `!`/`not`, `&&`/`||`/`and`/`or`, `===`, `==` against
  `null`, ordering against a number, Python `is` and comparison against a
  literal. Member reads and calls can run getters, so they never count.
- `boolean_ceremony`: an if that returns or assigns `true`/`false`. The
  suggested expression is wrapped in `Boolean()` or `bool()` unless it always
  yields a boolean. A comment above the final `return` blocks it.
- `control_flow_pyramid`: nested ifs ending a function that returns and
  yields no value.
- `temp_alias`: the name appears only in its declaration and its one use in
  the next statement. The use runs exactly once whenever that statement runs:
  not in a nested function, loop body, branch, or after `&&`, `||`, `??`, or
  an optional chain, since a getter or tracking proxy can observe a moved
  read. The statement must not await, yield, assign, or call through the
  source's root.
- TypeScript only: `trivial_wrapper`, `abstraction_overkill`, and
  `generic_naming` for top-level functions that are not exported, async, or
  generic, and that the file mentions only in direct calls, even in strings
  and comments. Type predicates and assertion functions are skipped. A
  conditional expression (`?:`, `??`, `?.`) is control flow, as Go needs an
  `if` for it. Python has no file-local functions: other modules import, and
  tests patch, underscore names.

`test_no_assertion` is Go only: in TypeScript and Python an exception fails a
test, and real tests without an assertion construct were deliberate
"does not throw" checks. Named functions include methods, `def`, and function values
bound to a name, also through a curried wrapper such as
`Effect.fn("f")(function* () {})`; other function values and lambdas are
literals. Classes own methods for `god_receiver`.

Standalone scans compare loaded packages. Vettool mode analyzes one package at a
time and does not run semantic similarity, code review, or repo-wide dead-code
reachability.

| Standalone flag | Purpose | Default |
| --- | --- | --- |
| `-max-states=N` | Maximum symbolic states before widening | `32` |
| `-json` | Write JSON diagnostics to stdout | `false` |
| `-closed-world` | Use matched `main` packages as complete production entrypoints for exported dead-code checks | `false` |
| `-cache` | Reuse cached results | `true` |
| `-version` | Print the slopelint version and exit | `false` |

Standalone exit codes: `0` clean, `1` failure, `2` usage error, `3` findings.
JSON format:

```json
{"issues":[{"position":"file.go:10:2","kind":"behavior_clone","message":"..."}]}
```

Analyzer driver flags include `-test=false` (skip tests), `-c=N` (source context),
and `-flags` (list flags as JSON). These select analyzer mode.

## Semantic similarity

Local scans compare named functions with at least 50 Go tokens, and
TypeScript and Python functions, methods, and test calls of that size, by
source embedding only; the description channel below covers Go only. Script
pairs never cross languages, and a directory stands in for a package. Generated files
and function literals are excluded. Comparisons cover the same file, package,
and immediate sibling or parent-child packages. Large functions use overlapping
chunks without truncation; connected matches produce one grouped finding.

Source embeddings and behavior-description embeddings can each report matches.
When authenticated Codex CLI can access `gpt-6-luna`, it describes eligible
functions and adds detail to findings. **Uncached source is sent to the configured
Codex service.** Set `SLOPELINT_CODEX_DESCRIPTIONS=off` to disable this channel.
For local-only source processing, also use a local embedding server.
Codex work stops when the run fails or gets SIGINT/SIGTERM (exit 130); only
records accepted before the stop stay cached. Each embedding request times out
after 3 minutes with an error naming the URL.

Review findings against source: similarity does not prove equivalent behavior.
Each match has a stable `sim-...` pair ID. Accept reviewed intentional pairs:

```bash
SLOPELINT_SIMILARITY_ACCEPT=sim-1234,sim-5678 slopelint ./...
```

`SLOPELINT_SIMILARITY_ACCEPT=all` accepts the entire current baseline; use only
after review. Accepted pairs carry forward until their source changes.
Once semantic findings are fixed or accepted and review findings are fixed,
commit the generated `.slopelint-similarity.json` stamp.

CI validates the stamp's policy and Git digest before structural lint, without
model inference. Unless `SLOPELINT_CODEX_DESCRIPTIONS=off`, CI also requires the
stamp to attest a current [code review](#code-review). Policy changes rescore cached vectors; unchanged embeddings
remain reusable. Missing or stale stamps fail. Refresh locally and commit the
stamp; the stamp itself is excluded from its digest. Each Go module needs its own
stamp.

### Code review

Local standalone scans send each top-level declaration of handwritten Go,
TypeScript, and Python files (production and test) that has a judged item to
`gpt-6.1-terra` (low reasoning) through the Codex CLI. Adjacent TypeScript and
Python statements form one declaration. **Uncached declarations, with numbered source lines, are
sent to the configured Codex service.** Items:

- Comment groups in or attached to the declaration. Suppressions
  (`//nolint`, `//exhaustive:ignore`, `//slopelint:ignore`, `//lint:ignore`) are
  always judged, bare or with a reason; they pass only when the code shows a real
  constraint. A comment that ends on the line above a suppression is judged with
  it as its reason. Other directives (`//go:build`, `//go:generate`, `//go:embed`,
  `//slopelint:ensures`), cgo preambles, and file-level comments are skipped.
- TypeScript and Python comments, including JSDoc and docstrings, and the
  suppressions `@ts-ignore`, `@ts-expect-error`, `@ts-nocheck`,
  `eslint-disable*`, `oxlint-disable*`, `biome-ignore`, `# noqa`,
  `# type: ignore`, `# pylint: disable`, `# pyright: ignore`, and
  `# pragma: no cover`. A suppression outside every declaration is judged
  alone. Module docstrings, triple-slash and source-map directives, and type
  comments are skipped. Public API means `export` in TypeScript and names
  without a leading underscore in Python.
- Production `if err != nil` checks that drop an error which is not read again
  before being overwritten. Errors carried across loop iterations, captured by a
  closure, or address-taken are not judged.
- Runnable test functions, `func TestXxx(t *testing.T)` in `_test.go` files, whole.
  `TestMain`, benchmarks, fuzz tests, and examples are not judged.
- TypeScript `catch` clauses and promise `.catch(fn)` callbacks, and Python
  `except` clauses and `contextlib.suppress` blocks, in production files,
  unless they raise again on every path. Each is judged in its outermost
  function, apart from comments.
- TypeScript `test`/`it` calls with a callback (not `.skip`/`.todo`) and Python
  `test*` functions and `Test*`/`TestCase` methods in `test*.py`/`*_test.py`,
  each with its suite or class header lines and setup hooks.
- The `//slopelint:serial <reason>` marker of such a test, judged alone: the
  reason must name shared state the test races on.
- Test groups: in a file with at least 3 such tests, tests that share 40%
  of the callees of either (at least 2, or all of a smaller set) form
  groups, cut in file order at 8 tests and 400 lines. A group that could not
  save 15 lines even by dropping all its tests but one is skipped. Each group
  is sent with the one-line signatures of file-local helpers its tests call,
  and judged `test_repeated_steps` when two or more of its tests repeat one
  block of setup, call, or assertion steps that a table or shared fixture
  would replace, saving at least 15 lines. The finding is at the first of
  those tests and names the others. It is dropped when a `semantic_duplicate`
  or `repeated_test_fixture` finding lies in one of them.

The model returns one verdict and one sentence of advice per item. The finding
message is that advice: `comment_noise` (restates code), `comment_excuse`
(excuses a shortcut, suppression, or compatibility code), `comment_crutch`
(explains code a rename or restructure would clarify), `swallowed_error`,
`test_trivial` (can fail only if a constant, literal, default, getter, or the
test's own setup changes), `test_mock_only` (asserts only calls to or values
from fakes), `test_wasteful` (real waits, repeated runs, or oversized
fixtures the checked behavior does not need), and `test_serial_excuse` (a
generic serial reason, or one naming state the test does not touch or could
isolate). The model sees only the test, so it
accepts a test or serial reason when the verdict depends on code it cannot see.
A doc comment on an unexported declaration must add something the name,
signature, and body do not show, such as a reason, constraint, ordering, or
result contract. The prompt prefers accepting an item when unsure.

There is no accept list, ignore directive, or override for review findings. The
stamp records the review policy and is written only when review finds nothing.
Without the Codex channel there are no `swallowed_error` findings. If `codex` is
missing, the run warns and writes a stamp without review, which CI rejects unless
`SLOPELINT_CODEX_DESCRIPTIONS=off`.

Verdicts are cached per declaration, keyed by prompt version, model, effort,
source, and items; each language and rule family has its own prompt version; unchanged declarations cause no model calls. A test group's source holds only its tests and helper signatures, so edits elsewhere in the file keep its record. Records accepted
before a failure or SIGINT stay cached.

### Embedding server

The Go binary needs no CGO. Run a shared HTTP server separately; slopelint does
not download models or start inference processes. Use the fixed
`jina-embeddings-v2-base-code` model with 768-dimensional output and mean pooling.
Weights and pooling must match because cached vectors and thresholds depend on
this configuration.

Use the pinned [Jina GGUF](https://registry.ollama.ai/v2/unclemusclez/jina-embeddings-v2-base-code/blobs/sha256:33a8a1b6a1cbba662f292d32bb55f8d109c0e6cb02de2d243a1b70705ea20986)
(SHA-256 `33a8a1b6a1cbba662f292d32bb55f8d109c0e6cb02de2d243a1b70705ea20986`):

```bash
llama-server --model /path/to/jina-embeddings-v2-base-code.gguf \
  --alias jina-embeddings-v2-base-code --embedding --pooling mean \
  --ctx-size 8192 --batch-size 2048 --ubatch-size 2048 --parallel 4 \
  --host 127.0.0.1 --port 18080 --sleep-idle-seconds 300

slopelint ./...  # default SLOPELINT_LLAMA_URL
```

### Environment and cache

| Variable | Values / default |
| --- | --- |
| `SLOPELINT_LLAMA_URL` | Embedding server URL; default `http://127.0.0.1:18080` |
| `SLOPELINT_SIMILARITY` | `local`, `ci`, or `off`; auto-selects `ci` when `CI`, `WORKERS_CI`, or `CF_PAGES` is truthy, otherwise `local` |
| `SLOPELINT_SIMILARITY_ACCEPT` | Comma-separated reviewed pair IDs, or `all` |
| `SLOPELINT_CODEX_DESCRIPTIONS` | `auto` (default) or `off`; `off` also disables code review |
| `SLOPELINT_CACHE` | `0` disables persistent caching |

Results, descriptions, review verdicts, and vectors are cached under `os.UserCacheDir()/slopelint`.
Linked Git worktrees share caches by repository identity. Unchanged scans replay
before package loading; changed scans reuse unaffected work. Cache cleanup is
automatic. Thresholds and cache rules live in [similarity.go](internal/lint/similarity.go)
and [cache_gc.go](internal/lint/cache_gc.go).

## Rules

| Checks | Diagnostic IDs |
| --- | --- |
| Repeated guards and unreachable branches | `redundant_condition`, `redundant_subexpression`, `unreachable_case` |
| Simplifiable control flow | `boolean_ceremony`, `control_flow_merge`, `control_flow_pyramid`, `redundant_default`, `complexity_simplification`, `guard_complexity` |
| Redundant collection guards, loops, and aliases | `append_ceremony`, `range_ceremony`, `temp_alias` |
| Loop performance traps | `loop_membership_scan`, `loop_sort`, `loop_invariant_work`, `loop_external_call`, `nested_lookup_loop`, `pairwise_comparison_loop` |
| Wrappers, repeated normalization, and comments that restate names | `trivial_wrapper`, `normalization_ceremony`, `serialization_ceremony`, `comment_noise` |
| Codex [code review](#code-review) of comments, dropped errors, and tests | `comment_noise`, `comment_excuse`, `comment_crutch`, `swallowed_error`, `test_trivial`, `test_mock_only`, `test_wasteful`, `test_serial_excuse`, `test_repeated_steps` |
| Repeated function or block behavior | `behavior_clone`, `semantic_duplicate` |
| Overbuilt APIs and signatures | `abstraction_overkill`, `api_overkill`, `result_wrapper`, `generic_naming`, `predicate_signature`, `unused_private_param`, `bool_mode_param`, `optional_result_triple` |
| Error panics and sentinel breaks | `prod_must_panic`, `sentinel_error_break` |
| Unreachable declarations | `dead_code` |
| Declaration grouping | `const_grouping`, `var_grouping`, `type_grouping`, `mixed_const_prefixes` |
| Files over 1,000 lines; functions over 100; literals over 40; over 6 params or 3 results (Go only); types or classes over 40 methods; also for [TypeScript and Python](#languages) | `oversized_owner_file`, `long_function`, `long_func_literal`, `long_param_list`, `long_result_list`, `god_receiver` |
| Test structure and support code | `table_test_grouping`, `const_value_test`, `repeated_test_fixture`, `test_fatal_panic`, `test_global_func_stub`, `test_no_assertion`, `test_not_parallel`, `test_sleep`, `test_focused`, `test_support_filename` |
| Invalid contracts and stale suppressions | `invalid_contract`, `stale_complexity_suppression` |

Path analysis tracks nil, string, length, boolean, enum, and no-argument `IsX()`
facts through guards and helper summaries. For example:

```go
func handle(req *Req) {
    if req == nil {
        return
    }
    if req == nil { // reported: guard already excludes nil
        panic("dead")
    }
}
```

Exact behavior-clone checks use typed SSA summaries and normalized statement
blocks. Intentional clones need an attached reason:

```go
//slopelint:ignore behavior_clone -- separate protocol boundary
```

`test_not_parallel` reports a runnable test that does not call `t.Parallel()`
in its body or in a same-package helper given `t`; tests that use `t.Setenv` or
`t.Chdir` are exempt. A serial test needs a reason in its doc comment, which
[code review](#code-review) judges. A marker without a reason, or on a parallel
or exempt test, is reported:

```go
//slopelint:serial SetMutableBanner writes the package-level banner
```

Production dead-code checks ignore test references. A full module-root `./...`
scan also identifies internal packages used only through test imports. Their
handwritten Go filenames must contain an underscore-delimited `test_support`
marker, such as `capture_test_support_linux.go`. Package-local helpers belong in
`_test.go`. Filenames alone never establish test-only status; structural checks
still apply.

## Contracts

Declare helper guarantees when its body is unavailable or the guarantee should
be explicit:

```go
//slopelint:ensures req != nil
//slopelint:ensures req.Name != ""
func requireReq(req *Req) {}
```

Reachable paths after `requireReq(req)` inherit these facts. Syntax:

```text
//slopelint:ensures <param-or-receiver>[.<field>...] ==|!= <scalar>
```

Scalars: `nil`, `true`, `false`, quoted strings, base-10 integers. Receivers must
be named. Malformed contracts emit `invalid_contract`.

## Limits

- Reports only; no custom autofix.
- Results cover the active `GOOS`, `GOARCH`, and build tags. Run each relevant
  configuration, for example `GOFLAGS='-tags=mytag' slopelint ./...`.
- Path analysis skips functions containing `goto` and is conservative around
  writes, unknown calls, loops, closures, and concurrent control flow.
- Exact clones are equivalent within the supported static model. Runtime-generated
  behavior and calls through unknown function values can cause misses.
- Semantic descriptions see each function in isolation; caller context and
  unknown helper behavior can be missed.
- Exported dead-code checks require standalone `-closed-world` with a loaded
  `main` package. Partial scans cannot establish complete repository reachability.

## Development

```bash
go test ./...
./scripts/format-code.sh
./scripts/check-code-health.sh
```

The health script runs `go vet`, this analyzer, `go test`, and `golangci-lint`
(required separately). With the embedding server running, check HTTP integration:

```bash
go test -tags=integration ./internal/lint -run '^TestLlamaServerSimilarityIntegration$' -count=1
```

Test behavior-preserving rewrites against evaluation order, aliases, captured
writes, and named types. For read-only scans of another repo, set
`SLOPELINT_SIMILARITY=off` to avoid writing its stamp and compare Git status before
and after.

## Release and install

Releases use Go module tags. After the full health gate passes for the intended
commit, create and push the requested tag, then verify the remote tag resolves
to that commit. Install the exact tag with
`go install github.com/manuel-huez/slopelint/cmd/slopelint@<tag>`; an untagged
commit does not become `@latest`.

Update every active installation, including `~/.local/bin/slopelint` or
`$(go env GOPATH)/bin/slopelint` when present; set `GOBIN` for each destination. Verify each binary with `go version -m`, compare their SHA-256
hashes, and run the installed analyzer on this repository. If a proxy returns
an older tag, retry exact-tag resolution with `GOPROXY=direct`.
