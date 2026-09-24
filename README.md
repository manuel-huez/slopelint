# slopelint

Go analyzer for redundant checks, repeated behavior, dead code, and overbuilt APIs.
Built on `go/analysis`, with optional Codex descriptions for semantic similarity.
Early-stage: expect breaking changes and conservative misses.

## Install and run

Requires Go 1.26.5+.

```bash
go install github.com/manuel-huez/slopelint/cmd/slopelint@v0.3.2
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

Standalone scans compare loaded packages. Vettool mode analyzes one package at a
time and does not run semantic similarity or repo-wide dead-code reachability.

| Standalone flag | Purpose | Default |
| --- | --- | --- |
| `-max-states=N` | Maximum symbolic states before widening | `32` |
| `-json` | Write JSON diagnostics to stdout | `false` |
| `-closed-world` | Use matched `main` packages as complete production entrypoints for exported dead-code checks | `false` |
| `-cache` | Reuse cached results | `true` |

Standalone exit codes: `0` clean, `1` failure, `2` usage error, `3` findings.
JSON format:

```json
{"issues":[{"position":"file.go:10:2","kind":"behavior_clone","message":"..."}]}
```

Analyzer driver flags include `-test=false` (skip tests), `-c=N` (source context),
and `-flags` (list flags as JSON). These select analyzer mode.

## Semantic similarity

Local scans compare named functions with at least 50 Go tokens. Generated files
and function literals are excluded. Comparisons cover the same file, package,
and immediate sibling or parent-child packages. Large functions use overlapping
chunks without truncation; connected matches produce one grouped finding.

Source embeddings and behavior-description embeddings can each report matches.
When authenticated Codex CLI can access `gpt-6-luna`, it describes eligible
functions and adds detail to findings. **Uncached source is sent to the configured
Codex service.** Set `SLOPELINT_CODEX_DESCRIPTIONS=off` to disable this channel.
For local-only source processing, also use a local embedding server.

Review findings against source: similarity does not prove equivalent behavior.
Each match has a stable `sim-...` pair ID. Accept reviewed intentional pairs:

```bash
SLOPELINT_SIMILARITY_ACCEPT=sim-1234,sim-5678 slopelint ./...
```

`SLOPELINT_SIMILARITY_ACCEPT=all` accepts the entire current baseline; use only
after review. Accepted pairs carry forward until their source changes.
Once semantic findings are fixed or accepted, commit the generated
`.slopelint-similarity.json` stamp.

CI validates the stamp's policy and Git digest before structural lint, without
model inference. Missing or stale stamps fail. Refresh locally and commit the
stamp; the stamp itself is excluded from its digest. Each Go module needs its own
stamp.

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
  --host 127.0.0.1 --port 8080 --sleep-idle-seconds 300

SLOPELINT_LLAMA_URL=http://127.0.0.1:8080 slopelint ./...
```

### Environment and cache

| Variable | Values / default |
| --- | --- |
| `SLOPELINT_LLAMA_URL` | Embedding server URL; default `http://127.0.0.1:8080` |
| `SLOPELINT_SIMILARITY` | `local`, `ci`, or `off`; auto-selects `ci` when `CI`, `WORKERS_CI`, or `CF_PAGES` is truthy, otherwise `local` |
| `SLOPELINT_SIMILARITY_ACCEPT` | Comma-separated reviewed pair IDs, or `all` |
| `SLOPELINT_CODEX_DESCRIPTIONS` | `auto` (default) or `off` |
| `SLOPELINT_CACHE` | `0` disables persistent caching |

Results, descriptions, and vectors are cached under `os.UserCacheDir()/slopelint`.
Linked Git worktrees share caches by repository identity. Unchanged scans replay
before package loading; changed scans reuse unaffected work. Cache cleanup is
automatic. Thresholds and cache rules live in [similarity.go](internal/lint/similarity.go)
and [cache_gc.go](internal/lint/cache_gc.go).

## Rules

| Checks | Diagnostic IDs |
| --- | --- |
| Repeated guards and unreachable branches | `redundant_condition`, `redundant_subexpression`, `unreachable_case` |
| Simplifiable control flow | `boolean_ceremony`, `control_flow_merge`, `redundant_default`, `complexity_simplification`, `guard_complexity` |
| Redundant collection guards, loops, and aliases | `append_ceremony`, `loop_ceremony`, `temp_alias` |
| Loop performance traps | `loop_membership_scan`, `loop_sort`, `loop_invariant_work`, `loop_external_call`, `nested_lookup_loop`, `pairwise_comparison_loop` |
| Wrappers, repeated normalization, and comments that restate names | `trivial_wrapper`, `normalization_ceremony`, `serialization_ceremony`, `comment_noise` |
| Repeated function or block behavior | `behavior_clone`, `semantic_duplicate` |
| Overbuilt APIs and signatures | `abstraction_overkill`, `api_overkill`, `result_wrapper`, `generic_naming`, `predicate_signature`, `unused_private_param`, `bool_mode_param`, `optional_result_triple`, `prod_must_panic`, `sentinel_error_break` |
| Unreachable declarations | `dead_code` |
| Declaration grouping and owner files over 1,000 lines | `const_grouping`, `var_grouping`, `type_grouping`, `mixed_const_prefixes`, `oversized_owner_file` |
| Test structure and support code | `table_test_grouping`, `const_value_test`, `repeated_test_fixture`, `test_fatal_panic`, `test_global_func_stub`, `test_support_filename` |
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
