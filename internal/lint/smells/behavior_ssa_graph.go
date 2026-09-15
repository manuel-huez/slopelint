package smells

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"sort"
	"strconv"

	"golang.org/x/tools/go/ssa"
)

type behaviorSummary struct {
	digest  string
	effects behaviorEffects
}

func behaviorCalleeSummaries(
	functions []*ssa.Function,
	aliases map[*ssa.Function]*ssa.Function,
	objectTargets map[string]*ssa.Function,
) map[*ssa.Function]behaviorSummary {
	edges := behaviorFunctionEdges(functions, aliases)
	summaries := make(map[*ssa.Function]behaviorSummary, len(functions))

	// Tarjan emits a caller-to-callee graph in dependency-first component order.
	for _, component := range behaviorStrongComponents(functions, edges) {
		if len(component) == 1 && !behaviorHasEdge(edges, component[0], component[0]) {
			key, effects, _, ok := newBehaviorSSAEncoder(
				component[0],
				summaries,
				aliases,
				objectTargets,
			).encode()
			if ok {
				summaries[component[0]] = behaviorSummary{
					digest:  behaviorKeyDigest(key),
					effects: effects,
				}
			}

			continue
		}

		behaviorSummarizeRecursiveComponent(
			component,
			summaries,
			aliases,
			objectTargets,
		)
	}

	return summaries
}

func behaviorFunctionEdges(
	functions []*ssa.Function,
	aliases map[*ssa.Function]*ssa.Function,
) map[*ssa.Function][]*ssa.Function {
	known := make(map[*ssa.Function]struct{}, len(functions))
	for _, fn := range functions {
		known[fn] = struct{}{}
	}

	edges := make(map[*ssa.Function][]*ssa.Function, len(functions))
	for _, fn := range functions {
		seen := make(map[*ssa.Function]struct{})

		for _, block := range fn.Blocks {
			for _, instruction := range block.Instrs {
				for _, operand := range instruction.Operands(nil) {
					callee, ok := behaviorLocalOperandFunction(operand, known, aliases, seen)
					if !ok {
						continue
					}

					seen[callee] = struct{}{}
					edges[fn] = append(edges[fn], callee)
				}
			}
		}
	}

	return edges
}

func behaviorLocalOperandFunction(
	operand *ssa.Value,
	known map[*ssa.Function]struct{},
	aliases map[*ssa.Function]*ssa.Function,
	seen map[*ssa.Function]struct{},
) (*ssa.Function, bool) {
	if operand == nil || *operand == nil {
		return nil, false
	}

	callee, ok := (*operand).(*ssa.Function)
	if !ok {
		return nil, false
	}

	callee = aliases[callee]
	if callee == nil {
		return nil, false
	}

	if _, local := known[callee]; !local {
		return nil, false
	}

	if _, exists := seen[callee]; exists {
		return nil, false
	}

	return callee, true
}

func behaviorStrongComponents(
	functions []*ssa.Function,
	edges map[*ssa.Function][]*ssa.Function,
) [][]*ssa.Function {
	state := behaviorTarjanState{
		edges:   edges,
		indexes: make(map[*ssa.Function]int, len(functions)),
		low:     make(map[*ssa.Function]int, len(functions)),
		onStack: make(map[*ssa.Function]bool, len(functions)),
	}
	for _, fn := range functions {
		if _, visited := state.indexes[fn]; !visited {
			state.visit(fn)
		}
	}

	return state.components
}

type behaviorTarjanState struct {
	edges      map[*ssa.Function][]*ssa.Function
	nextIndex  int
	indexes    map[*ssa.Function]int
	low        map[*ssa.Function]int
	stack      []*ssa.Function
	onStack    map[*ssa.Function]bool
	components [][]*ssa.Function
}

func (s *behaviorTarjanState) visit(fn *ssa.Function) {
	s.nextIndex++
	s.indexes[fn] = s.nextIndex
	s.low[fn] = s.nextIndex
	s.stack = append(s.stack, fn)
	s.onStack[fn] = true

	for _, callee := range s.edges[fn] {
		if _, visited := s.indexes[callee]; !visited {
			s.visit(callee)
			s.low[fn] = min(s.low[fn], s.low[callee])
		} else if s.onStack[callee] {
			s.low[fn] = min(s.low[fn], s.indexes[callee])
		}
	}

	if s.low[fn] != s.indexes[fn] {
		return
	}

	component := make([]*ssa.Function, 0, 1)

	for {
		last := len(s.stack) - 1
		member := s.stack[last]
		s.stack = s.stack[:last]
		s.onStack[member] = false

		component = append(component, member)
		if member == fn {
			break
		}
	}

	s.components = append(s.components, component)
}

func behaviorSummarizeRecursiveComponent(
	component []*ssa.Function,
	summaries map[*ssa.Function]behaviorSummary,
	aliases map[*ssa.Function]*ssa.Function,
	objectTargets map[string]*ssa.Function,
) {
	keys := make(map[*ssa.Function]string, len(component))
	for _, fn := range component {
		keys[fn] = newBehaviorTypeEncoder(fn.Signature).signatureKey(fn.Signature)
	}

	classes, ok := behaviorRefineRecursiveClasses(
		component,
		keys,
		summaries,
		aliases,
		objectTargets,
	)
	if !ok {
		return
	}

	effects, ok := behaviorRefineRecursiveEffects(
		component,
		classes,
		summaries,
		aliases,
		objectTargets,
	)
	if !ok {
		return
	}

	refs := behaviorRecursiveRefs(summaries, classes, effects)
	for _, fn := range component {
		key, finalEffects, _, ok := newBehaviorSSAEncoder(
			fn,
			refs,
			aliases,
			objectTargets,
		).encode()
		if !ok {
			return
		}

		summaries[fn] = behaviorSummary{
			digest:  behaviorKeyDigest(key),
			effects: finalEffects,
		}
	}
}

func behaviorRefineRecursiveClasses(
	component []*ssa.Function,
	keys map[*ssa.Function]string,
	summaries map[*ssa.Function]behaviorSummary,
	aliases map[*ssa.Function]*ssa.Function,
	objectTargets map[string]*ssa.Function,
) (map[*ssa.Function]string, bool) {
	classes, classCount := behaviorClasses(component, keys)
	emptyEffects := make(map[*ssa.Function]behaviorEffects)

	for {
		refs := behaviorRecursiveRefs(summaries, classes, emptyEffects)
		for _, fn := range component {
			key, _, _, ok := newBehaviorSSAEncoder(
				fn,
				refs,
				aliases,
				objectTargets,
			).encode()
			if !ok {
				return nil, false
			}

			keys[fn] = classes[fn] + "|" + key
		}

		next, nextCount := behaviorClasses(component, keys)
		classes = next

		if nextCount == classCount {
			return classes, true
		}

		classCount = nextCount
	}
}

func behaviorRefineRecursiveEffects(
	component []*ssa.Function,
	classes map[*ssa.Function]string,
	summaries map[*ssa.Function]behaviorSummary,
	aliases map[*ssa.Function]*ssa.Function,
	objectTargets map[string]*ssa.Function,
) (map[*ssa.Function]behaviorEffects, bool) {
	effects := make(map[*ssa.Function]behaviorEffects, len(component))

	for {
		refs := behaviorRecursiveRefs(summaries, classes, effects)
		changed := false

		for _, fn := range component {
			_, next, _, ok := newBehaviorSSAEncoder(
				fn,
				refs,
				aliases,
				objectTargets,
			).encode()
			if !ok {
				return nil, false
			}

			if next != effects[fn] {
				effects[fn] = next
				changed = true
			}
		}

		if !changed {
			return effects, true
		}
	}
}

func behaviorRecursiveRefs(
	summaries map[*ssa.Function]behaviorSummary,
	classes map[*ssa.Function]string,
	effects map[*ssa.Function]behaviorEffects,
) map[*ssa.Function]behaviorSummary {
	refs := make(map[*ssa.Function]behaviorSummary, len(summaries)+len(classes))
	maps.Copy(refs, summaries)

	for fn, class := range classes {
		refs[fn] = behaviorSummary{
			digest:  "recursive:" + class,
			effects: effects[fn],
		}
	}

	return refs
}

func behaviorClasses(
	functions []*ssa.Function,
	keys map[*ssa.Function]string,
) (map[*ssa.Function]string, int) {
	unique := make(map[string]struct{}, len(functions))
	for _, fn := range functions {
		unique[keys[fn]] = struct{}{}
	}

	sorted := make([]string, 0, len(unique))
	for key := range unique {
		sorted = append(sorted, key)
	}

	sort.Strings(sorted)

	classByKey := make(map[string]string, len(sorted))
	for idx, key := range sorted {
		classByKey[key] = "c" + strconv.Itoa(idx)
	}

	classes := make(map[*ssa.Function]string, len(functions))
	for _, fn := range functions {
		classes[fn] = classByKey[keys[fn]]
	}

	return classes, len(sorted)
}

func behaviorHasEdge(
	edges map[*ssa.Function][]*ssa.Function,
	from *ssa.Function,
	to *ssa.Function,
) bool {
	return slices.Contains(edges[from], to)
}

func behaviorKeyDigest(key string) string {
	digest := sha256.Sum256([]byte(key))

	return hex.EncodeToString(digest[:])
}
