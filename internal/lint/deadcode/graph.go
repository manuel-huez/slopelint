package deadcode

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"
)

type deadCodeMode uint8

const (
	deadCodePrivate deadCodeMode = iota
	deadCodeRepo
)

type deadCodeDecl struct {
	obj      types.Object
	node     ast.Node
	name     string
	kind     string
	pos      token.Pos
	exported bool
	pkg      *Package
}

type deadCodeGraph struct {
	candidates map[string]deadCodeDecl
	edges      map[string]map[string]struct{}
	roots      map[string]struct{}
	ignored    map[*Package]map[token.Pos]struct{}
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

		l := newPackageLinter(pkg)
		graph.addPackage(l, deadCodeRepo)
	}

	graph.indexEdges()

	return graph.findings()
}

func (graph deadCodeGraph) findings() []Finding {
	live := graph.liveObjects()
	dead := make([]deadCodeDecl, 0)

	for obj, decl := range graph.candidates {
		if _, ok := live[obj]; ok {
			continue
		}

		dead = append(dead, decl)
	}

	sort.Slice(dead, func(i, j int) bool {
		return compareDeadCodeDecl(dead[i], dead[j])
	})

	findings := make([]Finding, 0, len(dead))
	for _, decl := range dead {
		findings = append(findings, decl.issue())
	}

	return findings
}

func (l *packageLinter) deadPrivateDeclGraph() deadCodeGraph {
	graph := newDeadCodeGraph()
	graph.addPackage(l, deadCodePrivate)
	graph.indexEdges()

	return graph
}

func newDeadCodeGraph() deadCodeGraph {
	return deadCodeGraph{
		candidates: make(map[string]deadCodeDecl),
		edges:      make(map[string]map[string]struct{}),
		roots:      make(map[string]struct{}),
		ignored:    make(map[*Package]map[token.Pos]struct{}),
	}
}

func (graph *deadCodeGraph) addPackage(l *packageLinter, mode deadCodeMode) {
	graph.ignored[l.pkg] = l.deadCodeIgnoredIdentPositions()
	l.forEachProductionDecl(func(decl ast.Decl) {
		graph.addDecl(l, decl, mode)
	})
}

func (graph *deadCodeGraph) indexEdges() {
	for obj, decl := range graph.candidates {
		l := newPackageLinter(decl.pkg)
		graph.edges[obj] = graph.usesFrom(l, decl.node)
	}
}
