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
	candidates                   map[string]deadCodeDecl
	edges                        map[string]map[string]struct{}
	roots                        map[string]struct{}
	rootUses                     []deadCodeRootUse
	ignored                      map[*Package]map[token.Pos]struct{}
	packages                     map[string]*Package
	fmtStringerForwarded         map[string][]fmtStringerParamUse
	fmtStringerResults           map[string]fmtStringerResultState
	fmtStringerVarValues         map[string][]types.Type
	fmtStringerVarSlices         map[string][]types.Type
	fmtStringerVarUnknown        map[string]struct{}
	fmtStringerSummary           map[string]struct{}
	fmtStringerLive              map[string]struct{}
	funcDeclCache                map[*types.Func]*ast.FuncDecl
	funcDeclMisses               map[*types.Func]struct{}
	reflectedWrapperDecodeCache  map[string]reflectedWrapperSummary
	reflectedWrapperMarshalCache map[string]reflectedWrapperSummary
}

type deadCodeRootUse struct {
	pkg  *Package
	node ast.Node
}

// Private reports production-dead private declarations within one package.
func Private(pkg *Package) []Finding {
	if pkg == nil {
		return nil
	}

	graph := newPackageLinter(pkg).deadPrivateDeclGraph()

	return graph.findings()
}

// Repo reports declarations unreachable from repository entrypoints.
func Repo(pkgs []*Package) []Finding {
	graph := newDeadCodeGraph()

	for _, pkg := range pkgs {
		if pkg == nil {
			continue
		}

		graph.registerPackage(pkg)
	}

	for _, pkg := range pkgs {
		if pkg == nil {
			continue
		}

		graph.addPackageDecls(newPackageLinter(pkg), deadCodeRepo)
	}

	graph.indexEdges()

	return graph.findings()
}

func (l *packageLinter) deadPrivateDeclGraph() deadCodeGraph {
	graph := newDeadCodeGraph()
	graph.registerPackage(l.pkg)
	graph.addPackageDecls(l, deadCodePrivate)
	graph.indexEdges()

	return graph
}

func newDeadCodeGraph() deadCodeGraph {
	return deadCodeGraph{
		candidates:                   make(map[string]deadCodeDecl),
		edges:                        make(map[string]map[string]struct{}),
		roots:                        make(map[string]struct{}),
		rootUses:                     make([]deadCodeRootUse, 0),
		ignored:                      make(map[*Package]map[token.Pos]struct{}),
		packages:                     make(map[string]*Package),
		fmtStringerForwarded:         make(map[string][]fmtStringerParamUse),
		fmtStringerResults:           make(map[string]fmtStringerResultState),
		fmtStringerVarValues:         make(map[string][]types.Type),
		fmtStringerVarSlices:         make(map[string][]types.Type),
		fmtStringerVarUnknown:        make(map[string]struct{}),
		fmtStringerSummary:           make(map[string]struct{}),
		funcDeclCache:                make(map[*types.Func]*ast.FuncDecl),
		funcDeclMisses:               make(map[*types.Func]struct{}),
		reflectedWrapperDecodeCache:  make(map[string]reflectedWrapperSummary),
		reflectedWrapperMarshalCache: make(map[string]reflectedWrapperSummary),
	}
}

func (graph *deadCodeGraph) registerPackage(pkg *Package) {
	l := newPackageLinter(pkg)

	graph.packages[pkg.ImportPath] = pkg
	graph.ignored[pkg] = l.deadCodeIgnoredIdentPositions()
}

func (graph *deadCodeGraph) addPackageDecls(l *packageLinter, mode deadCodeMode) {
	l.forEachProductionDecl(func(decl ast.Decl) {
		graph.addDecl(l, decl, mode)
	})
}

func (graph *deadCodeGraph) indexEdges() {
	graph.reindexRootUses()

	for obj, decl := range graph.candidates {
		l := newPackageLinter(decl.pkg)
		graph.edges[obj] = graph.usesFrom(l, decl.node)
	}
}

func (graph *deadCodeGraph) reindexRootUses() {
	graph.roots = make(map[string]struct{})
	for _, root := range graph.rootUses {
		graph.addRootUseEdges(root.pkg, root.node)
	}
}
