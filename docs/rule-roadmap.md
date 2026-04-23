# Rule Roadmap

This document turns the "clean, readable, maintainable Go" goal into a concrete
rule backlog for `slopelint`.

Current status in this worktree:

- proof lane shipped: `redundant_post_success_check`
- proof lane shipped: `redundant_bool_return`
- proof lane shipped: `identical_branch_body` for `if` / `else` and adjacent
  `switch` arms with identical bodies
- proof lane shipped: `exhaustive_defensive_default` for exhaustive `bool`
  switches and conservative closed const-set switches
- proof lane shipped: `single_use_temp_alias` for immediate one-read cheap
  aliases
- smell lane shipped: `trivial_forwarder` for docless same-package private
  wrappers with one production callsite
- smell lane shipped: `restatement_comment` for one-line private
  declaration docs that only restate identifier words
- proof lane shipped: `redundant_json_marshal_text` for `MarshalJSON` methods
  that only call `json.Marshal(value.String())` when `MarshalText` exists
- smell lane shipped: `repeated_normalization_call` for duplicate
  `strings.TrimSpace(...)` and case-folded trim calls inside one function
- smell lane experimental: `duplicate_validation_ladder`,
  `single_use_private_helper`, `single_impl_interface`, `options_overkill`,
  `internal_result_wrapper`, and paired `generic_name`
- current proving corpus: `stocks-researcher` raises redundant scalar
  `MarshalJSON` methods through `serialization_ceremony` and repeated
  normalization through `normalization_ceremony`

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

- Status: shipped for exhaustive `bool` switches and conservative named types
  with closed in-package const sets
- Lane: `proof`
- Goal: flag `default` arms kept "for safety" when switch is already exhaustive.
- Examples:
  - `switch ok { case true: ...; case false: ...; default: ... }`
  - `switch mode { case Read: ...; case Write: ...; default: ... }` where all
    in-package constants of `mode` are covered
- Why: defensive defaults hide real future exhaustiveness gaps.
- Guardrails:
  - named const-set mode only covers same-package private named types
  - named const-set mode requires the zero value to be part of the known set
  - skip defaults that explicitly handle invalid, unsupported, or unknown values
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

#### `redundant_json_marshal_text`

- Status: shipped
- Lane: `proof`
- Goal: flag `MarshalJSON` methods that only return
  `json.Marshal(value.String())` when the same value receiver type already has
  `MarshalText`.
- Examples:
  - `func (value Symbol) MarshalJSON() ([]byte, error) { return json.Marshal(value.String()) }`
- Why: `encoding/json` already uses `encoding.TextMarshaler` for JSON string
  values, so the JSON method is redundant boilerplate.
- Guardrails:
  - require same type to declare value-receiver `MarshalText`
  - require exact one-statement `return json.Marshal(receiver.String())`
  - require `json.Marshal` to resolve to `encoding/json`
- Suggested category: `serialization_ceremony`

#### `repeated_normalization_call`

- Status: shipped
- Lane: `smell`
- Goal: flag repeated normalization of the same expression inside one function.
- Examples:
  - `if strings.TrimSpace(name) != "" { return strings.TrimSpace(name) }`
  - repeated `strings.ToLower(strings.TrimSpace(value))` in one function
- Why: repeated normalization in deeper branches hides one obvious normalized
  value and can drift when one call changes.
- Guardrails:
  - require exact rendered expression match
  - only count repeated calls inside the same function body
  - treat nested function literals as separate scopes
  - only cover `strings.TrimSpace` and
    `strings.ToLower/ToUpper(strings.TrimSpace(...))` today
- Suggested category: `normalization_ceremony`

#### `single_use_private_helper`

- Status: experimental
- Lane: `smell`
- Goal: flag unexported helpers with one callsite and tiny bodies.
- Why: AI often splits straightforward logic into helpers that add indirection
  but no reuse.
- Guardrails:
  - same-package only
  - exactly one production callsite
  - small body threshold
  - skip recursion, methods satisfying interfaces, generics, `func` literals,
    branches, loops, `defer`, `go`, `select`, or helpers with doc comments
- Suggested category: `abstraction_overkill`

#### `trivial_forwarder`

- Status: shipped
- Lane: `smell`
- Goal: flag wrappers that only forward args/results to another function.
- Examples:
  - `func run(ctx context.Context) error { return execute(ctx) }`
  - `func newCache() *Cache { return buildCache() }`
  - `func run(ctx context.Context) error { err := execute(ctx); return err }`
- Why: wrapper layers make call graphs noisy if they do not add naming value.
- Guardrails:
  - private helpers first
  - no added logging, metrics, tracing, panic recovery, or argument rewriting
  - assign-and-return wrappers only when call results are returned unchanged and
    in order
  - skip if wrapper name is materially better than callee name
  - stronger signal when caller count is one
- Suggested category: `trivial_wrapper`

#### `duplicate_validation_ladder`

- Status: experimental
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

- Status: experimental
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

- Status: experimental
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

- Status: experimental
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

- Status: shipped
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

- Status: experimental
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

All listed rules now have a first implementation.

Next work:

1. run the experimental lane on more healthy Go repos and record noisy cases
2. tune or split `abstraction_overkill` before considering default-on behavior
3. add optional autofix support for syntax-preserving trivial wrappers and
   restatement comments
4. keep P2 rules behind `-experimental` until multiple corpora stay clean

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
