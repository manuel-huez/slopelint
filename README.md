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

A clean run writes `.slopelint-similarity.json`. Commit it: CI checks it.

| Flag | Purpose |
| --- | --- |
| `-json` | JSON output: `{"issues":[{"position","kind","message"}]}` |
| `-closed-world` | Treat matched `main` packages as all entrypoints, so exported dead code is found |
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
- run: go install github.com/manuel-huez/slopelint/cmd/slopelint@v0.4.3
- run: slopelint ./...
```

A stale stamp fails CI: run slopelint locally and commit the new stamp.

## Environment

| Variable | Values |
| --- | --- |
| `SLOPELINT_LLAMA_URL` | Embedding server; default `http://127.0.0.1:18080` |
| `SLOPELINT_SIMILARITY` | `local`, `ci`, or `off` (no similarity or review); default `ci` when `CI`, `WORKERS_CI`, or `CF_PAGES` is set, else `local` |
| `SLOPELINT_CODEX_DESCRIPTIONS` | `auto` or `off` (no Codex: no descriptions or review) |
| `SLOPELINT_SIMILARITY_ACCEPT` | Reviewed `sim-...` pair IDs to accept, comma-separated, or `all` |
| `SLOPELINT_CACHE` | `0` disables the cache in `os.UserCacheDir()/slopelint` |

## Languages

Go packages, plus tracked `.ts`, `.tsx`, and `.py` files under the scanned
directories. Skipped: hidden, `node_modules`, `vendor`, `testdata`, `.d.ts`,
`.gen.ts`, `.min.` files, and generated files. TypeScript and Python get the
size, structure, test, similarity, and review rules that one file's syntax can
prove. A file the parser cannot read gets one `parse_error`.

## Model checks

**Similarity:** functions of 50+ tokens are compared by source embedding, and
Go functions also by Codex-written behavior descriptions, within a package and
its neighbors. Similarity does not prove equal behavior: check each finding,
then fix it or accept its pair ID with `SLOPELINT_SIMILARITY_ACCEPT`.

**Code review:** Codex (`gpt-6.1-terra`) judges each declaration's comments,
suppressions, dropped or caught errors, tests, `//slopelint:serial` reasons, and
groups of similar tests. Each finding is the model's advice. It accepts an item
when unsure, and a test group must be flagged by two separate answers. Review
findings have no override: fix the code.

Verdicts and vectors are cached per declaration, so unchanged code causes no
model calls.

## Embedding server

Run `llama-server` with the pinned
[jina-embeddings-v2-base-code GGUF](https://registry.ollama.ai/v2/unclemusclez/jina-embeddings-v2-base-code/blobs/sha256:33a8a1b6a1cbba662f292d32bb55f8d109c0e6cb02de2d243a1b70705ea20986).
Cached vectors depend on this exact model and pooling.

```bash
llama-server --model /path/to/jina-embeddings-v2-base-code.gguf \
  --alias jina-embeddings-v2-base-code --embedding --pooling mean \
  --ctx-size 8192 --batch-size 2048 --ubatch-size 2048 --parallel 4 \
  --host 127.0.0.1 --port 18080 --sleep-idle-seconds 300
```

## Rules

| Checks | IDs |
| --- | --- |
| Repeated guards, unreachable branches | `redundant_condition`, `redundant_subexpression`, `unreachable_case` |
| Simplifiable control flow | `boolean_ceremony`, `control_flow_merge`, `control_flow_pyramid`, `redundant_default`, `complexity_simplification`, `guard_complexity` |
| Needless collection code and aliases | `append_ceremony`, `range_ceremony`, `temp_alias` |
| Loop performance | `loop_membership_scan`, `loop_sort`, `loop_invariant_work`, `loop_external_call`, `nested_lookup_loop`, `pairwise_comparison_loop` |
| Wrappers and ceremony | `trivial_wrapper`, `normalization_ceremony`, `serialization_ceremony` |
| Repeated behavior | `behavior_clone`, `semantic_duplicate` |
| Overbuilt APIs | `abstraction_overkill`, `api_overkill`, `result_wrapper`, `generic_naming`, `predicate_signature`, `unused_private_param`, `bool_mode_param`, `optional_result_triple` |
| Panics and sentinel breaks | `prod_must_panic`, `sentinel_error_break` |
| Dead code | `dead_code` |
| Declaration grouping | `const_grouping`, `var_grouping`, `type_grouping`, `mixed_const_prefixes` |
| Size: files 1,000 lines, functions 100, literals 40, 6 params, 3 results, 40 methods | `oversized_owner_file`, `long_function`, `long_func_literal`, `long_param_list`, `long_result_list`, `god_receiver` |
| Tests | `table_test_grouping`, `const_value_test`, `repeated_test_fixture`, `test_fatal_panic`, `test_global_func_stub`, `test_no_assertion`, `test_not_parallel`, `test_sleep`, `test_focused`, `test_support_filename` |
| Code review | `comment_noise`, `comment_excuse`, `comment_crutch`, `swallowed_error`, `test_trivial`, `test_mock_only`, `test_wasteful`, `test_serial_excuse`, `test_repeated_steps` |
| Directives and parsing | `invalid_contract`, `stale_complexity_suppression`, `parse_error` |

### Directives

```go
//slopelint:ignore behavior_clone -- separate protocol boundary
//slopelint:serial SetMutableBanner writes the package-level banner
//slopelint:ensures req != nil
```

- `ignore behavior_clone` marks an intentional clone; code review judges the
  reason.
- `serial` goes in the doc comment of a test that cannot call `t.Parallel()`;
  code review judges the reason.
- `ensures` states a helper's guarantee for its callers:
  `<param>[.<field>...] ==|!= <nil|true|false|"string"|int>`.
- Go files of internal packages that only tests import need `test_support` in
  their name, such as `capture_test_support_linux.go`.

## Limits

- Reports only; no autofix.
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
