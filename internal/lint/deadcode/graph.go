package deadcode

import (
	"go/ast"
	"go/token"
	"go/types"
	"maps"
)

type deadCodeMode uint8

const (
	deadCodePrivate deadCodeMode = iota
	deadCodeRepo
)

type deadCodeDecl struct {
	obj      types.Object
	node     ast.Node
	owner    string
	name     string
	kind     string
	pos      token.Pos
	exported bool
	pkg      *Package
}

type deadCodeGraph struct {
	candidates map[string]deadCodeDecl
	rootUses   []deadCodeRootUse
	roots      map[string]struct{}
	edges      map[string]map[string]struct{}
}

type deadCodeRootUse struct {
	pkg  *Package
	node ast.Node
}

// funcDeclIndex finds source declarations of functions in the analyzed
// packages. Lookups are cached for the whole run.
type funcDeclIndex struct {
	packages map[string]*Package
	cache    map[*types.Func]*ast.FuncDecl
	misses   map[*types.Func]struct{}
}

func (index *funcDeclIndex) forEachPackage(fn func(*Package)) {
	for _, pkg := range index.packages {
		fn(pkg)
	}
}

// Private reports production-dead private declarations within one package.
func Private(pkg *Package) []Finding {
	return findDeadCode([]*Package{pkg}, deadCodePrivate)
}

// Repo reports declarations unreachable from repository entrypoints.
func Repo(pkgs []*Package) []Finding {
	return findDeadCode(pkgs, deadCodeRepo)
}

func findDeadCode(pkgs []*Package, mode deadCodeMode) []Finding {
	graph := &deadCodeGraph{
		candidates: make(map[string]deadCodeDecl),
		rootUses:   make([]deadCodeRootUse, 0),
		roots:      make(map[string]struct{}),
		edges:      make(map[string]map[string]struct{}),
	}
	decls := &funcDeclIndex{
		packages: make(map[string]*Package),
		cache:    make(map[*types.Func]*ast.FuncDecl),
		misses:   make(map[*types.Func]struct{}),
	}
	ignored := make(map[*Package]map[token.Pos]struct{})

	for _, pkg := range pkgs {
		if pkg != nil {
			decls.packages[pkg.ImportPath] = pkg
			ignored[pkg] = newPackageLinter(pkg).deadCodeIgnoredIdentPositions()
		}
	}

	iface := ifaceUses{candidates: graph.candidates}
	uses := &useCollector{
		ignored: ignored,
		iface:   iface,
		fmt: fmtConcreteFlow{
			iface: iface,
			decls: decls,
			params: fmtParamFlow{
				decls:     decls,
				forwarded: make(map[string][]fmtStringerParamUse),
				results:   make(map[string]fmtStringerResultState),
			},
			summaries: &fmtVarSummaries{
				values:  make(map[string][]types.Type),
				slices:  make(map[string][]types.Type),
				unknown: make(map[string]struct{}),
			},
		},
		reflect: reflectionUses{
			decls: decls,
			wrappers: reflectedWrappers{
				decls:        decls,
				decodeCache:  make(map[string]reflectedWrapperSummary),
				marshalCache: make(map[string]reflectedWrapperSummary),
			},
			typeParams: reflectedTypeParams{decls: decls},
		},
	}

	index := declIndex{graph: graph, uses: uses}

	for _, pkg := range pkgs {
		if pkg != nil {
			index.addPackageDecls(newPackageLinter(pkg), mode)
		}
	}

	graph.indexEdges(uses)

	return graph.findings(uses)
}

func (graph *deadCodeGraph) addCandidate(key string, decl deadCodeDecl) {
	graph.candidates[key] = decl
}

func (graph *deadCodeGraph) addRootUse(uses *useCollector, pkg *Package, node ast.Node) {
	graph.rootUses = append(graph.rootUses, deadCodeRootUse{pkg: pkg, node: node})
	graph.addRootUseEdges(uses, pkg, node)
}

func (graph *deadCodeGraph) indexEdges(uses *useCollector) {
	graph.roots = make(map[string]struct{})
	for _, root := range graph.rootUses {
		graph.addRootUseEdges(uses, root.pkg, root.node)
	}

	for obj, decl := range graph.candidates {
		graph.edges[obj] = uses.usesFrom(newPackageLinter(decl.pkg), decl.node)
	}
}

func (graph *deadCodeGraph) addRootUseEdges(uses *useCollector, pkg *Package, node ast.Node) {
	for key := range uses.usesFrom(newPackageLinter(pkg), node) {
		graph.roots[key] = struct{}{}
	}
}

// liveObjects repeats reachability until it is stable: fmt var summaries
// only follow functions that the previous pass found live.
func (graph *deadCodeGraph) liveObjects(uses *useCollector) map[string]struct{} {
	live := graph.reachableObjects()

	for {
		uses.resetFmtStringerVarSummaries(live)
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
		current := work[len(work)-1]
		work = work[:len(work)-1]

		for next := range graph.edges[current] {
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
