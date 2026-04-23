# Rule Roadmap

This document turns the "clean, readable, maintainable Go" goal into a concrete
rule backlog for `slopelint`.

Current status in this worktree:

- proof lane shipped: `redundant_post_success_check`
- proof lane shipped: `redundant_bool_return`
- proof lane shipped: `identical_branch_body` for `if` / `else` and adjacent
  `switch` arms with identical bodies
- proof lane shipped: `exhaustive_defensive_default` for exhaustive `bool`
  switches
- proof lane shipped: `single_use_temp_alias` for immediate one-read cheap
  aliases
- smell lane experimental: `trivial_forwarder` for docless same-package private
  wrappers with one production callsite
- smell lane experimental: `restatement_comment` for one-line private
  declaration docs that only restate identifier words
- next backlog: `duplicate_validation_ladder`, `single_use_private_helper`,
  broader exhaustive-default support for closed const sets

## Rule lanes

Ship rules in two lanes:

- **proof**: low-false-positive rules backed by local control-flow, exact AST
  shape, or repo-wide usage evidence. Safe to enable by default.
- **smell**: heuristic rules backed by multiple signals. Start as advisory or
  behind an experimental flag.

## Priorities

### P0: proof rules already delivered

These are implemented and enabled by default.

#### `redundant_post_success_check`

- Status: shipped
- Lane: `proof`
- Goal: flag repeated checks after helper success already guarantees the same
  fact.
- Examples:
  - `if err := validate(req); err != nil { return err }; if req == nil { ... }`
  - `if !valid(req) { return }; if req.Name == "" { ... }`
- Why: this is the next natural step after current contract and summary support.
- Guardrails:
  - only emit when helper success maps to an already-supported fact kind
  - require no writes or unknown calls between helper result check and repeated
    condition
  - keep category under existing `redundant_condition`

#### `redundant_bool_return`

- Status: shipped
- Lane: `proof`
- Goal: flag bool-return ceremony such as:
  - `if cond { return true }; return false`
  - `if cond { ok = true } else { ok = false }`
- Why: AI-written Go often expands obvious boolean expressions into branches.
- Guardrails:
  - only when both branches are side-effect free
  - only when both branches assign or return literal booleans
  - skip when comments make branches intentionally distinct
- Suggested category: `boolean_ceremony`

#### `identical_branch_body`

- Status: shipped
- Lane: `proof`
- Goal: flag `if`/`else` or `switch` branches that do the same thing.
- Examples:
  - both branches `return nil`
  - both branches call same function with same args
  - adjacent `switch` arms return same value
- Why: this is high-signal dead structure; AI often keeps stale branch shells.
- Guardrails:
  - require exact AST equivalence after stripping formatting noise
  - skip if branch comments differ
  - skip if branch-local defs change scope in a meaningful way
- Suggested category: `control_flow_merge`

#### `exhaustive_defensive_default`

- Status: shipped for exhaustive `bool` switches; planned extension for named
  types with closed in-package const sets
- Lane: `proof`
- Goal: flag `default` arms kept "for safety" when switch is already exhaustive.
- Examples:
  - `switch ok { case true: ...; case false: ...; default: ... }`
  - `switch mode { case Read: ...; case Write: ...; default: ... }` where all
    in-package constants of `mode` are covered
- Why: defensive defaults hide real future exhaustiveness gaps.
- Guardrails:
  - start with `bool` switches first
  - only extend to named types with a closed in-package const set
  - skip when `default` panics with an explicit impossible-state message and the
    project wants that style
- Suggested category: `redundant_default`

### P1: structural simplifiers

Mixed state: one proof rule shipped, one smell rule experimental, remaining
items still need stronger repo-local evidence.

#### `single_use_temp_alias`

- Status: shipped
- Lane: `proof`
- Goal: flag locals that only rename a cheap expression one time.
- Examples:
  - `name := req.Name; if name == "" { ... }`
  - `errValue := err; return errValue`
- Why: AI often inserts names that do not carry extra meaning.
- Guardrails:
  - local variable only
  - one read after declaration
  - RHS must be cheap and side-effect free
  - skip when name adds clear domain meaning or declaration has comment
- Suggested category: `temp_alias`

#### `single_use_private_helper`

- Status: planned
- Lane: `smell`
- Goal: flag unexported helpers with one callsite and tiny bodies.
- Why: AI often splits straightforward logic into helpers that add indirection
  but no reuse.
- Guardrails:
  - same-package only
  - exactly one production callsite
  - small body threshold
  - skip recursion, methods satisfying interfaces, generics, `defer`, `go`,
    `select`, or helpers with doc comments
- Suggested category: `abstraction_overkill`

#### `trivial_forwarder`

- Status: experimental
- Lane: `smell`
- Goal: flag wrappers that only forward args/results to another function.
- Examples:
  - `func run(ctx context.Context) error { return execute(ctx) }`
  - `func newCache() *Cache { return buildCache() }`
- Why: wrapper layers make call graphs noisy if they do not add naming value.
- Guardrails:
  - private helpers first
  - no added logging, metrics, tracing, panic recovery, or argument rewriting
  - skip if wrapper name is materially better than callee name
  - stronger signal when caller count is one
- Suggested category: `trivial_wrapper`

#### `duplicate_validation_ladder`

- Status: planned
- Lane: `smell`
- Goal: flag repeated validation sequences that should become one helper or
  constructor.
- Examples:
  - same `nil` / empty / length checks repeated across multiple funcs
  - same guard order, same error messages, same return shape
- Why: repeated validation is common in AI patches copied across handlers.
- Guardrails:
  - require at least two sites with a normalized predicate sequence match
  - require three or more checks, or enough AST weight to matter
  - start within one package
- Suggested category: `duplicate_validation`

### P2: advisory smells

Useful, but likely too subjective for default-on mode. Keep behind
`-experimental` or a dedicated style flag until proven low-noise.

#### `single_impl_interface`

- Status: planned
- Lane: `smell`
- Goal: flag internal interfaces with one implementation and no real
  substitution pressure.
- Why: AI often adds interfaces "for testability" before tests need them.
- Guardrails:
  - private or package-local interfaces only
  - ignore standard library integration points
  - ignore generic constraints
  - ignore interfaces used by tests as seams unless production benefit is still
    absent
- Suggested category: `abstraction_overkill`

#### `options_overkill`

- Status: planned
- Lane: `smell`
- Goal: flag functional options or builder-style setup around tiny private APIs.
- Why: this pattern is expensive when the call surface is small and stable.
- Guardrails:
  - private constructors first
  - small callsite count
  - small option count
  - skip exported APIs where compatibility pressure justifies options
- Suggested category: `api_overkill`

#### `internal_result_wrapper`

- Status: planned
- Lane: `smell`
- Goal: flag private result structs that only carry `(value, ok)` or `(value,
  err)` style data without extra behavior.
- Why: AI often creates `Result`/`Response` types where normal Go tuples read
  better.
- Guardrails:
  - private type only
  - no methods
  - no tags / serialization role
  - used only as transient local return plumbing
- Suggested category: `result_wrapper`

#### `restatement_comment`

- Status: experimental
- Lane: `smell`
- Goal: flag comments that only restate names or obvious code.
- Examples:
  - `// Validate user` above `func validateUser(...)`
  - `// Increment count` above `count++`
- Why: AI comments often add noise instead of intent.
- Guardrails:
  - private declarations and local comments only at first
  - high lexical overlap between comment and code identifier tokens
  - skip exported API docs, package comments, examples, and warnings
- Suggested category: `comment_noise`

#### `generic_name`

- Status: planned
- Lane: `smell`
- Goal: flag weak names such as `Helper`, `Manager`, `Processor`, `Util`,
  `Base`, or `Impl` when body behavior is still generic.
- Why: these names correlate with low-intent abstractions.
- Guardrails:
  - private identifiers first
  - only emit when paired with another smell signal, such as single use or
    trivial forwarding
  - do not emit on its own
- Suggested category: `generic_naming`

## Suggested remaining implementation order

1. `duplicate_validation_ladder`
2. `single_use_private_helper`
3. extend `exhaustive_defensive_default` beyond `bool` to named types with
   closed const sets
4. graduate `trivial_forwarder` from experimental if corpus stays clean
5. graduate `restatement_comment` from experimental if corpus stays clean
6. everything else behind an experimental or style flag

## Non-goals

Do not chase rules already covered well by `staticcheck`, `go vet`, or
`gocritic` unless `slopelint` can add one of these:

- path-sensitive proof
- cross-function or cross-package evidence
- repo-wide usage evidence
- lower-noise Go-specific guardrails

## Acceptance bar for any new rule

Before a rule ships:

- it needs at least one positive example and one non-example fixture
- autofix must be optional and syntax-preserving
- suppression story must be clear
- false-positive risk must stay below "annoying in healthy hand-written Go"
