package deadcode

import (
	"go/ast"
	"go/token"
	"go/types"
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
	iface := ifaceUses{candidates: graph.candidates}

	uses := &useCollector{
		ignored: make(map[*Package]map[token.Pos]struct{}),
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

	for _, pkg := range pkgs {
		if pkg != nil {
			decls.packages[pkg.ImportPath] = pkg
			uses.ignored[pkg] = newPackageLinter(pkg).deadCodeIgnoredIdentPositions()
		}
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
