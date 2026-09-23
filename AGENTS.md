# AGENTS.md

## Response style

Write smart caveman: short words, fragments, abbreviations (`DB/auth/config/req/res/fn/impl`),
and arrows. Drop articles, filler, pleasantries, hedging, and fluff; keep technical
terms exact. Pattern: `[thing] [action] [reason]. [next step].`

Example: `Inline obj prop → new ref → re-render. Use useMemo.`

Keep this style every response until user says "stop caveman" or "normal mode",
or session ends. Use normal prose for code, commits, and PRs. Use full clarity for
security warnings, irreversible confirmations, risky multi-step sequences,
clarification asks, and repeated questions; then resume caveman. Code blocks stay
unchanged; quote errors exactly.

## Change rules (mandatory)

Before writing code, identify owner, overlapping logic, and what to delete,
merge, inline, or rename. Choose one surviving implementation.

- Edit owning module directly. No wrappers, helper modules, or layers to avoid
  the real refactor.
- Replace rather than accumulate. When adding a function, type, file, flag, or
  path, name what it replaces; remove obsolete implementation, callers, tests,
  docs, help text, fixtures, and config in the same change.
- If nothing can be replaced, justify new surface. Ask before coding when that
  justification is not obvious.
- Internal symbols are not stable APIs: rename/delete directly and update all
  in-repo callers. No pass-through wrappers, aliases, rename adapters, `_v2`
  modules, compatibility shims, or deprecated parallel paths. No staged
  migrations unless user explicitly requests a phased rollout.
- Inline single-use logic by default. Extract only for clear ownership,
  readability, or actual multi-caller reuse. Avoid thin variants of the same
  helper unless behavior differs materially or readability clearly improves.
- Do not append to an owner file that is too large for whole-file reasoning.
  Split only when responsibility or whole-file readability improves, never
  mechanically or to avoid the owner. Ask before a new package, cache policy,
  persisted format, or non-obvious design split.
- No speculative hooks, interfaces, option structs, flags, or extension points.
  Refactors must keep or reduce entrypoints, flags, and code paths unless user
  requests new surface.

When unsure, prefer correct data, simpler public APIs, fewer code paths, and
faster local workflows with deliberate API use.

## Comments and readability

- Comment exported Go functions and user-facing CLI behavior when purpose is not
  obvious from the name.
- Add short comments for non-obvious logic, invariants, cache/freshness rules,
  source-quality tradeoffs, and intentional workarounds. Explain ordering,
  batching, slicing, and data layout when correctness or performance depends on
  them.
- Explain why, constraints, and assumptions. Do not narrate code, add boilerplate
  to every function, or use comments to defend confusing structure.
- If a function needs more than one or two short comment lines to explain,
  simplify, inline, rename, or split first. Rename helpers whose comments merely
  repeat their purpose. Treat files too large for end-to-end inspection as a
  structure problem.

## Before finishing

- Search stale references to removed names and paths; delete obsolete tests,
  docs, help text, fixtures, and config.
- Confirm only the supported implementation remains.
- Check lines added; keep necessary changes only and simplify where possible.
- Final response must include `Removed/merged:` and the changes, or
  `Nothing removed because:` and the reason.
