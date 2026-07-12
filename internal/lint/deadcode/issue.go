package deadcode

import (
	"fmt"
	"slices"
	"sort"
)

func (graph deadCodeGraph) findings() []Finding {
	live := graph.liveObjects()
	dead := make(map[string]deadCodeDecl)

	for obj, decl := range graph.candidates {
		if _, ok := live[obj]; ok {
			continue
		}

		dead[obj] = decl
	}

	dead = graph.unreachableSubgraphRoots(dead)

	ordered := make([]deadCodeDecl, 0, len(dead))
	for _, decl := range dead {
		ordered = append(ordered, decl)
	}

	sort.Slice(ordered, func(i, j int) bool {
		return compareDeadCodeDecl(ordered[i], ordered[j])
	})

	findings := make([]Finding, 0, len(ordered))
	for _, decl := range ordered {
		findings = append(findings, decl.issue())
	}

	return findings
}

func (graph deadCodeGraph) unreachableSubgraphRoots(
	dead map[string]deadCodeDecl,
) map[string]deadCodeDecl {
	edges := graph.unreachableSubgraphEdges(dead)
	finished := deadCodeFinishOrder(dead, edges)
	components, componentDecls := deadCodeComponents(finished, edges.reversed())

	return deadCodeSourceComponents(dead, edges, components, componentDecls)
}

type deadCodeEdges map[string]map[string]struct{}

func (graph deadCodeGraph) unreachableSubgraphEdges(
	dead map[string]deadCodeDecl,
) deadCodeEdges {
	edges := make(deadCodeEdges, len(dead))

	for from := range dead {
		for to := range graph.edges[from] {
			if _, ok := dead[to]; ok {
				edges.add(from, to)
			}
		}
	}

	// A dead type owns dead fields and methods even though type reachability must
	// not keep each member live. Ownership only suppresses diagnostic cascades.
	for key, decl := range dead {
		if _, ok := dead[decl.owner]; ok {
			edges.add(decl.owner, key)
		}
	}

	return edges
}

func (edges deadCodeEdges) add(from string, to string) {
	if from == "" || to == "" || from == to {
		return
	}

	if edges[from] == nil {
		edges[from] = make(map[string]struct{})
	}

	edges[from][to] = struct{}{}
}

func (edges deadCodeEdges) reversed() deadCodeEdges {
	reverse := make(deadCodeEdges, len(edges))

	for from, nextKeys := range edges {
		for to := range nextKeys {
			reverse.add(to, from)
		}
	}

	return reverse
}

func deadCodeFinishOrder(
	dead map[string]deadCodeDecl,
	edges deadCodeEdges,
) []string {
	visited := make(map[string]struct{}, len(dead))
	finished := make([]string, 0, len(dead))

	var visit func(string)

	visit = func(key string) {
		if _, ok := visited[key]; ok {
			return
		}

		visited[key] = struct{}{}
		for next := range edges[key] {
			visit(next)
		}

		finished = append(finished, key)
	}

	for key := range dead {
		visit(key)
	}

	return finished
}

func deadCodeComponents(
	finished []string,
	reverse deadCodeEdges,
) (map[string]int, [][]string) {
	components := make(map[string]int, len(finished))
	componentDecls := make([][]string, 0)

	var assign func(string, int)

	assign = func(key string, component int) {
		if _, ok := components[key]; ok {
			return
		}

		components[key] = component
		componentDecls[component] = append(componentDecls[component], key)

		for previous := range reverse[key] {
			assign(previous, component)
		}
	}

	for _, key := range slices.Backward(finished) {
		if _, ok := components[key]; ok {
			continue
		}

		component := len(componentDecls)
		componentDecls = append(componentDecls, nil)

		assign(key, component)
	}

	return components, componentDecls
}

func deadCodeSourceComponents(
	dead map[string]deadCodeDecl,
	edges deadCodeEdges,
	components map[string]int,
	componentDecls [][]string,
) map[string]deadCodeDecl {
	incoming := make([]bool, len(componentDecls))

	for from, nextKeys := range edges {
		fromComponent := components[from]
		for to := range nextKeys {
			toComponent := components[to]
			if fromComponent != toComponent {
				incoming[toComponent] = true
			}
		}
	}

	roots := make(map[string]deadCodeDecl)

	for component, keys := range componentDecls {
		if incoming[component] {
			continue
		}

		representative := keys[0]
		for _, key := range keys[1:] {
			if compareDeadCodeDecl(dead[key], dead[representative]) {
				representative = key
			}
		}

		roots[representative] = dead[representative]
	}

	return roots
}

func (decl deadCodeDecl) issue() Finding {
	message := fmt.Sprintf(
		`private %s %q is never used by production code; remove it`,
		decl.kind,
		decl.name,
	)

	if decl.exported {
		message = fmt.Sprintf(
			`exported %s %q is unreachable from repo entrypoints; remove it`,
			decl.kind,
			decl.name,
		)
	}

	return Finding{
		Pos:     decl.pos,
		Kind:    "dead_code",
		Message: message,
		FSet:    decl.pkg.FSet,
	}
}

func compareDeadCodeDecl(left, right deadCodeDecl) bool {
	leftPos := left.pkg.FSet.Position(left.pos)
	rightPos := right.pkg.FSet.Position(right.pos)

	if leftPos.Filename != rightPos.Filename {
		return leftPos.Filename < rightPos.Filename
	}

	if leftPos.Line != rightPos.Line {
		return leftPos.Line < rightPos.Line
	}

	if leftPos.Column != rightPos.Column {
		return leftPos.Column < rightPos.Column
	}

	return left.name < right.name
}
