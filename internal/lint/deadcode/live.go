package deadcode

import (
	"maps"
)

// liveObjects repeats reachability until it is stable: fmt var summaries
// only follow functions that the previous pass found live.
func (graph *deadCodeGraph) liveObjects(uses *useCollector) map[string]struct{} {
	live := graph.reachableObjects()

	for {
		uses.fmt.summaries.resetFmtStringerVarSummaries(live)
		graph.indexEdges(uses)

		next := graph.reachableObjects()
		if maps.Equal(live, next) {
			return live
		}

		live = next
	}
}

func (graph *deadCodeGraph) reachableObjects() map[string]struct{} {
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
