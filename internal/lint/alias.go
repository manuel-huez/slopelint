package lint

import "sort"

func aliasClosure(st state, key string) []string {
	seen := map[string]struct{}{key: {}}
	queue := []string{key}

	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]

		for peer := range st.aliases[current] {
			if _, ok := seen[peer]; ok {
				continue
			}

			seen[peer] = struct{}{}
			queue = append(queue, peer)
		}
	}

	out := make([]string, 0, len(seen))
	for key := range seen {
		out = append(out, key)
	}

	sort.Strings(out)

	return out
}

func linkAlias(st state, left, right string) state {
	if left == right {
		return st
	}

	out := st.clone()
	addAliasEdge(&out, left, right)
	addAliasEdge(&out, right, left)

	return out
}

func addAliasEdge(st *state, left, right string) {
	if st.aliases == nil {
		st.aliases = make(map[string]map[string]struct{})
	}

	if st.aliases[left] == nil {
		st.aliases[left] = make(map[string]struct{})
	}

	st.aliases[left][right] = struct{}{}
}

func removeAliasPrefix(st *state, prefix string, descendantsOnly bool) {
	if len(st.aliases) == 0 {
		return
	}

	toRemove := make(map[string]struct{})

	for key := range st.aliases {
		if descendantsOnly {
			if key != prefix && isSameOrChild(key, prefix) {
				toRemove[key] = struct{}{}
			}

			continue
		}

		if isSameOrChild(key, prefix) {
			toRemove[key] = struct{}{}
		}
	}

	for key := range toRemove {
		for peer := range st.aliases[key] {
			delete(st.aliases[peer], key)

			if len(st.aliases[peer]) == 0 {
				delete(st.aliases, peer)
			}
		}

		delete(st.aliases, key)
	}
}

func intersectAliases(states []state) map[string]map[string]struct{} {
	if len(states) == 0 {
		return nil
	}

	shared := aliasEdgeSet(states[0])
	for i := 1; i < len(states); i++ {
		other := aliasEdgeSet(states[i])
		for edge := range shared {
			if _, ok := other[edge]; !ok {
				delete(shared, edge)
			}
		}

		if len(shared) == 0 {
			return nil
		}
	}

	out := make(map[string]map[string]struct{})

	for edge := range shared {
		left, right, ok := splitAliasEdge(edge)
		if !ok {
			continue
		}

		if out[left] == nil {
			out[left] = make(map[string]struct{})
		}

		if out[right] == nil {
			out[right] = make(map[string]struct{})
		}

		out[left][right] = struct{}{}
		out[right][left] = struct{}{}
	}

	return out
}

func aliasEdgeSet(st state) map[string]struct{} {
	out := make(map[string]struct{})

	for left, peers := range st.aliases {
		for right := range peers {
			if left == right {
				continue
			}

			out[aliasEdgeName(left, right)] = struct{}{}
		}
	}

	return out
}

func splitAliasEdge(edge string) (string, string, bool) {
	for i := range len(edge) {
		if edge[i] != '=' {
			continue
		}

		return edge[:i], edge[i+1:], true
	}

	return "", "", false
}
