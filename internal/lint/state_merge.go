package lint

import (
	"strings"
)

func (l *linter) copyFacts(dst *state, lhs, rhs symbol, src state) {
	for key, f := range src.facts {
		if key == rhs.key || isSameOrChild(key, rhs.key) {
			newKey := lhs.key + strings.TrimPrefix(key, rhs.key)
			dst.facts[newKey] = f.clone()
		}
	}
}

func (l *linter) normalizeStates(states []state) []state {
	out := dedupeStateSlice(states)
	if len(out) <= l.maxStates {
		return out
	}

	return []state{l.mergeStates(out)}
}

func dedupeStateSlice(states []state) []state {
	if len(states) == 0 {
		return nil
	}

	seen := make(map[string]state, len(states))
	for _, st := range states {
		seen[st.hash()] = st
	}

	out := make([]state, 0, len(seen))
	for _, st := range seen {
		out = append(out, st)
	}

	return out
}

func (l *linter) mergeStates(states []state) state {
	if len(states) == 0 {
		return newState()
	}

	out := states[0].clone()
	for key, f := range out.facts {
		merged := f.clone()

		for i := 1; i < len(states); i++ {
			other, ok := states[i].facts[key]
			if !ok {
				merged = fact{}
				break
			}

			merged = l.joinFacts(merged, other)
			if merged.empty() {
				break
			}
		}

		if merged.empty() {
			delete(out.facts, key)
		} else {
			out.facts[key] = merged
		}
	}

	out.aliases = intersectAliases(states)
	out.bindings = l.intersectBindings(states)

	return out
}

func (l *linter) joinFacts(a, b fact) fact {
	out := fact{}

	if a.exact != nil && b.exact != nil && a.exact.value == b.exact.value {
		copyExact := *a.exact
		out.exact = &copyExact
	}

	if len(a.not) != 0 && len(b.not) != 0 {
		out.not = make(map[string]evidence)
		for k, ev := range a.not {
			if _, ok := b.not[k]; ok {
				out.not[k] = ev
			}
		}

		if len(out.not) == 0 {
			out.not = nil
		}
	}

	return out
}

func (l *linter) intersectBindings(states []state) map[string]resultBinding {
	if len(states) == 0 {
		return nil
	}

	out := make(map[string]resultBinding)

	for key, binding := range states[0].bindings {
		hash := bindingHash(binding)
		same := true

		for i := 1; i < len(states); i++ {
			other, ok := states[i].bindings[key]
			if !ok || bindingHash(other) != hash {
				same = false
				break
			}
		}

		if same {
			out[key] = binding.clone()
		}
	}

	if len(out) == 0 {
		return nil
	}

	return out
}
