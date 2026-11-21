# slopelint

Opinionated linter for Go, TypeScript, and Python. It reports redundant checks,
repeated code, dead code, overbuilt APIs, oversized code, and weak tests. Model
checks find duplicate behavior and review comments, suppressions, swallowed
errors, and tests. Early-stage: expect breaking changes.

## Requirements

- Go 1.26.5+.
- Local runs with model checks (the default) also need:
  - [Codex CLI](https://github.com/openai/codex), logged in with `codex login`
    (needs a ChatGPT plan). **Uncached source is sent to the Codex service.**
  - An [embedding server](#embedding-server).

A local run stops at once if either is missing. CI needs neither.

## Install and run

```bash
go install github.com/manuel-huez/slopelint/cmd/slopelint@latest
slopelint ./...
```

A clean run writes `.slopelint.json`. Commit it: CI checks it.

| Flag | Purpose |
| --- | --- |
| `-json` | JSON output: `{"issues":[{"position","kind","message"}]}` |
| `-closed-world` | Treat the module's `main` packages as all entrypoints, so exported dead code is found; needs `./...` from the module root |
| `-cache=false` | Ignore cached results |
| `-max-states=N` | Symbolic states before widening (default 32) |
| `-version` | Print version |

Exit codes: `0` clean, `1` failure, `2` usage error, `3` findings.

`go vet -vettool="$(command -v slopelint)" ./...` also works, per package and
without model checks or repo-wide dead code.

## CI

CI runs no models. It checks that the committed stamp matches the source, then
runs the other rules. `CI=true` selects this mode. Pin the version that wrote
the stamp; another version rejects it.

```yaml
- uses: actions/setup-go@v6
  with:
    go-version-file: go.mod
- run: go install github.com/manuel-huez/slopelint/cmd/slopelint@v0.5.1
- run: slopelint ./...
```

A stale stamp fails CI: run slopelint locally and commit the new stamp.

## Environment

| Variable | Values |
| --- | --- |
| `SLOPELINT_LLAMA_URL` | Embedding server; default `http://127.0.0.1:18080` |
| `SLOPELINT_EMBEDDING_MODEL` | Model name the server must report; default `jina-embeddings-v2-base-code` |
| `SLOPELINT_SIMILARITY_THRESHOLD` | Same-file source score gate, between 0 and 1; other gates move with it; default `0.970` |
| `SLOPELINT_SIMILARITY` | `local`, `ci`, or `off` (no similarity or review); default `ci` when `CI`, `WORKERS_CI`, or `CF_PAGES` is set, else `local` |
| `SLOPELINT_CODEX_DESCRIPTIONS` | `auto` or `off` (no Codex: no descriptions or review) |
| `SLOPELINT_CACHE` | `0` disables the cache in `os.UserCacheDir()/slopelint` |

## Embedding server

Run `llama-server` with
[jina-embeddings-v2-base-code](https://registry.ollama.ai/v2/unclemusclez/jina-embeddings-v2-base-code/blobs/sha256:33a8a1b6a1cbba662f292d32bb55f8d109c0e6cb02de2d243a1b70705ea20986)
and the flags below. It is the recommended default, and the default threshold
is tuned for it. To use another model, set `SLOPELINT_EMBEDDING_MODEL`; it
likely needs its own `SLOPELINT_SIMILARITY_THRESHOLD`. The stamp records both,
so every machine and CI must use the same values. slopelint rejects a server
that reports another model name.

```bash
llama-server --model /path/to/jina-embeddings-v2-base-code.gguf \
  --alias jina-embeddings-v2-base-code --embedding --pooling mean \
  --ctx-size 8192 --batch-size 2048 --ubatch-size 2048 --parallel 4 \
  --host 127.0.0.1 --port 18080 --sleep-idle-seconds 300
```

## Rules

slopelint scans Go packages and the tracked `.ts`, `.tsx`, and `.py` files under
the scanned directories. It skips hidden, `node_modules`, `vendor`, and
`testdata` directories, `.d.ts`, `.gen.ts`, and `.min.` files, and generated
files. TypeScript and Python rules use one file's syntax, so they catch less
than the typed Go rules.

### Structure

| Rules | Finds | Go | TS | Py |
| --- | --- | :-: | :-: | :-: |
| `control_flow_merge`, `boolean_ceremony`, `control_flow_pyramid`, `temp_alias` | Branches to merge, ifs that return `true`/`false`, nested final ifs, single-use aliases | ✅ | ✅ | ✅ |
| `trivial_wrapper`, `generic_naming` | Private helpers that only forward, generic helper names | ✅ | ✅ | – |
| `abstraction_overkill` | Private interfaces with one implementation | ✅ | – | – |
| `redundant_condition`, `redundant_subexpression`, `unreachable_case`, `redundant_default` | Guards and cases that earlier checks already decide | ✅ | – | – |
| `complexity_simplification`, `guard_complexity` | Complex conditions with a simpler equivalent | ✅ | – | – |
| `append_ceremony`, `range_ceremony`, `normalization_ceremony`, `serialization_ceremony` | Needless guards around append, range, normalization, and encoding | ✅ | – | – |
| `loop_membership_scan`, `loop_sort`, `loop_invariant_work`, `loop_external_call`, `nested_lookup_loop`, `pairwise_comparison_loop` | Slow work inside loops | ✅ | – | – |
| `api_overkill`, `result_wrapper`, `predicate_signature`, `unused_private_param`, `bool_mode_param`, `optional_result_triple` | Overbuilt signatures and APIs | ✅ | – | – |
| `prod_must_panic`, `sentinel_error_break` | Panics in production code, sentinel errors used for control flow | ✅ | – | – |
| `behavior_clone` | Functions or blocks with the same behavior | ✅ | – | – |
| `dead_code` | Unreachable declarations | ✅ | – | – |
| `const_grouping`, `var_grouping`, `type_grouping`, `mixed_const_prefixes` | Declarations to group | ✅ | – | – |

### Size

| Rules | Finds | Go | TS | Py |
| --- | --- | :-: | :-: | :-: |
| `oversized_owner_file`, `long_function`, `long_param_list`, `god_receiver` | Files over 1,000 lines, functions over 40 statements (closures inside count; a switch counts one per case plus its longest case), over 6 params (TS and Python skip a function their file only passes as a callback), types or classes over 40 methods (Go also counts functions that take `*T` first and methods of package types only it embeds) | ✅ | ✅ | ✅ |
| `long_result_list` | Over 3 results | ✅ | – | – |

### Tests

| Rules | Finds | Go | TS | Py |
| --- | --- | :-: | :-: | :-: |
| `test_sleep` | Real sleeps in tests | ✅ | ✅ | ✅ |
| `test_focused` | `.only`, `fit`, `fdescribe` | – | ✅ | – |
| `test_no_assertion`, `test_not_parallel`, `test_fatal_panic`, `test_global_func_stub` | Tests that cannot fail, run serially or call `t.Parallel()` twice (helpers in other packages count), panic, or stub package functions | ✅ | – | – |
| `table_test_grouping`, `const_value_test`, `repeated_test_fixture`, `test_support_filename` | Unnamed table cases, tests of constants, copied fixtures, misnamed test helpers | ✅ | – | – |

### Model: duplicates

| Rules | Finds | Go | TS | Py |
| --- | --- | :-: | :-: | :-: |
| `semantic_duplicate` | Functions of 50+ tokens with similar source or behavior | ✅ | ✅ | ✅ |

The embedding server turns each function into a vector, and functions with
close vectors in the same or a nearby package are reported. For Go, Codex
(`gpt-6-luna`) also writes a short description of what each function does, and
the descriptions are compared the same way. A match is a hint, not proof: merge
the code, or ignore the finding with the user's approval (see Directives).

### Model: code review

| Rules | Finds | Go | TS | Py |
| --- | --- | :-: | :-: | :-: |
| `comment_noise`, `comment_excuse`, `comment_crutch` | Comments and suppressions that restate code, excuse it, or explain unclear code | ✅ | ✅ | ✅ |
| `swallowed_error` | Dropped or caught errors that hide failures | ✅ | ✅ | ✅ |
| `test_trivial`, `test_mock_only`, `test_wasteful` | Tests that check nothing real, only mocks, or waste time | ✅ | ✅ | ✅ |
| `test_repeated_steps` | Tests that should share a table or fixture | ✅ | ✅ | ✅ |
| `test_serial_excuse` | Weak `//slopelint:serial` reasons | ✅ | – | – |
| `helper_fragment` | Private functions with one caller that are a piece of it, not a job of their own | ✅ | ✅ | – |

Codex (`gpt-6.1-terra`) judges each declaration; each finding is its advice.
It accepts an item when unsure, and a test group must be flagged by two
separate answers. Review findings have no override: fix the code. Verdicts are
cached per declaration, so unchanged code causes no model calls.

### Other

| Rules | Finds | Go | TS | Py |
| --- | --- | :-: | :-: | :-: |
| `invalid_ignore`, `stale_ignore` | Malformed `slopelint:ignore` directives, ignores no finding uses | ✅ | ✅ | ✅ |
| `invalid_contract`, `stale_complexity_suppression` | Malformed `ensures` directives, complexity `//nolint` that no longer applies | ✅ | – | – |
| `parse_error` | Files the parser cannot read | – | ✅ | ✅ |

### Directives

```go
//slopelint:ignore behavior_clone -- separate protocol boundary
//slopelint:serial SetMutableBanner writes the package-level banner
//slopelint:ensures req != nil
```

- `ignore <rule> -- <reason>` skips one finding: put it on the line above the
  reported line (`#` in Python), or anywhere in the file for
  `oversized_owner_file`. Agents add it only after the user approves; code
  review judges the reason. Code review findings cannot be ignored.
  `stale_ignore` reports an ignore that no finding uses, on full `./...` scans
  from the module root, or the Git root without `go.mod`.
- `serial` goes in the doc comment of a test that cannot call `t.Parallel()`;
  code review judges the reason.
- `ensures` states a helper's guarantee for its callers:
  `<param>[.<field>...] ==|!= <nil|true|false|"string"|int>`.
- Go files of internal packages that only tests import need `test_support` in
  their name, such as `capture_test_support_linux.go`.

## Limits

- Reports only; no autofix.
- Without `go.mod`, slopelint lints the Git-tracked TypeScript and Python
  files and keeps `.slopelint.json` at the Git root. JavaScript (`.js`,
  `.mjs`) is not linted.
- Results cover the active `GOOS`, `GOARCH`, and build tags.
- Path analysis skips functions with `goto` and is conservative around unknown
  calls, loops, closures, and concurrency.

## Development

```bash
go test ./...
./scripts/format-code.sh
./scripts/check-code-health.sh  # also needs golangci-lint
```

For read-only scans of another repo, set `SLOPELINT_SIMILARITY=off` so no stamp
is written there.
