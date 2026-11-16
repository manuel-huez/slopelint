package lint

import (
	"strings"
)

// stateBudget is the most path states kept at one program point (-max-states).
// More states merge into one, which keeps only facts all paths share.
type stateBudget int

func (b stateBudget) normalize(states []state) []state {
	out := dedupeStateSlice(states)
	if len(out) <= int(b) {
		return out
	}

	return []state{mergeStates(out)}
}

func (b stateBudget) normalizeLabeled(in map[string][]state) map[string][]state {
	if len(in) == 0 {
		return nil
	}

	out := make(map[string][]state, len(in))
	for label, states := range in {
		normalized := b.normalize(states)
		if len(normalized) == 0 {
			continue
		}

		out[label] = normalized
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

func (s *state) copyFactsFrom(src state, lhs, rhs symbol) {
	for key, f := range src.facts {
		if key == rhs.key || isSameOrChild(key, rhs.key) {
			newKey := lhs.key + strings.TrimPrefix(key, rhs.key)
			s.facts[newKey] = f.clone()
		}
	}
}

func dedupeStateSlice(states []state) []state {
	if len(states) == 0 {
		return nil
	}

	// Diagnostics retain evidence from the first equivalent state.
	seen := make(map[string]struct{}, len(states))
	out := make([]state, 0, len(states))

	for _, st := range states {
		key := st.hash()
		if _, ok := seen[key]; ok {
			continue
		}

		seen[key] = struct{}{}

		out = append(out, st)
	}

	return out
}

// mergeStates needs at least one state.
func mergeStates(states []state) state {
	out := states[0].clone()
	for key, f := range out.facts {
		merged := f.clone()

		for i := 1; i < len(states); i++ {
			other, ok := states[i].facts[key]
			if !ok {
				merged = fact{}
				break
			}

			merged = joinFacts(merged, other)
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
	out.bindings = intersectBindings(states)

	return out
}

func joinFacts(a, b fact) fact {
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

func intersectBindings(states []state) map[string]resultBinding {
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
