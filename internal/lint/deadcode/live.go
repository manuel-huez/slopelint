package deadcode

import (
	"go/token"
	"maps"
)

func (graph deadCodeGraph) identIgnored(
	pkg *Package,
	pos token.Pos,
) bool {
	ignored := graph.ignored[pkg]
	if ignored == nil {
		return false
	}

	_, ok := ignored[pos]

	return ok
}

func (graph deadCodeGraph) liveObjects() map[string]struct{} {
	live := graph.reachableObjects()

	for {
		graph.resetFmtStringerVarSummaries(live)
		graph.indexEdges()

		next := graph.reachableObjects()
		if maps.Equal(live, next) {
			return live
		}

		live = next
	}
}

func (graph deadCodeGraph) reachableObjects() map[string]struct{} {
	live := make(map[string]struct{})
	work := make([]string, 0, len(graph.roots))

	for obj := range graph.roots {
		if _, ok := graph.candidates[obj]; !ok {
			continue
		}

		live[obj] = struct{}{}
		work = append(work, obj)
	}

	for len(work) > 0 {
		obj := work[len(work)-1]
		work = work[:len(work)-1]

		for next := range graph.edges[obj] {
			if _, ok := graph.candidates[next]; !ok {
				continue
			}

			if _, seen := live[next]; seen {
				continue
			}

			live[next] = struct{}{}
			work = append(work, next)
		}
	}

	return live
}
