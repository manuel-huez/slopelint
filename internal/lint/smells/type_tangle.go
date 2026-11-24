package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"
)

const (
	typeTangleLimit = 7
	listedNameLimit = 7
)

type typeTangleGraph struct {
	specs      []*ast.TypeSpec
	components [][]int
	tangleOf   map[*types.TypeName]int
}

type typeTangleCheck struct {
	ruleEnv
}

func (l typeTangleCheck) check() map[*types.TypeName]int {
	graph := buildTypeTangleGraph(l.pkg)

	for _, comp := range graph.components {
		if len(comp) <= typeTangleLimit {
			continue
		}

		names := make([]string, len(comp))
		for index, node := range comp {
			names[index] = graph.specs[node].Name.Name
		}

		slices.Sort(names)

		anchor := graph.specs[comp[0]].Name.Pos()
		for _, node := range comp {
			if graph.specs[node].Name.Name == names[0] {
				anchor = graph.specs[node].Name.Pos()
				break
			}
		}

		l.report(
			anchor,
			"type_tangle",
			fmt.Sprintf(
				"%d types form a dependency cycle: %s",
				len(names),
				formatListedNames(names, listedNameLimit),
			),
		)
	}

	return graph.tangleOf
}

type typeDepIndex struct {
	info       *types.Info
	byType     map[*types.TypeName]int
	funcOwner  map[*types.Func]int
	fieldOwner map[*types.Var]int
}

func (d typeDepIndex) targetFor(obj types.Object) (int, bool) {
	switch obj := obj.(type) {
	case *types.TypeName:
		target, ok := d.byType[obj]
		return target, ok
	case *types.Func:
		target, ok := d.funcOwner[obj.Origin()]
		return target, ok
	case *types.Var:
		target, ok := d.fieldOwner[obj.Origin()]
		return target, ok
	default:
		return 0, false
	}
}

func (d typeDepIndex) collectDeps(root ast.Node, self int, targets map[int]struct{}) {
	add := func(obj types.Object) {
		if target, ok := d.targetFor(obj); ok && target != self {
			targets[target] = struct{}{}
		}
	}

	ast.Inspect(root, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.Ident:
			add(d.info.Uses[node])
		case *ast.SelectorExpr:
			if sel := d.info.Selections[node]; sel != nil {
				add(sel.Obj())
			}
		}

		return true
	})
}

func buildTypeTangleGraph(pkg *Package) typeTangleGraph {
	specs, byType := collectProductionTypes(pkg)
	if len(specs) < minCycleSize {
		return typeTangleGraph{specs: specs, tangleOf: map[*types.TypeName]int{}}
	}

	funcOwner, ownedFuncs := collectTypeMethods(pkg, byType)
	deps := typeDepIndex{
		info:       pkg.TypesInfo,
		byType:     byType,
		funcOwner:  funcOwner,
		fieldOwner: collectTypeFields(specs, pkg.TypesInfo, byType),
	}

	edges := make([][]int, len(specs))
	for index, spec := range specs {
		targets := make(map[int]struct{})
		deps.collectDeps(spec.Type, index, targets)

		for _, fn := range ownedFuncs[index] {
			deps.collectDeps(fn, index, targets)
		}

		edges[index] = sortedIntKeys(targets)
	}

	components := StronglyConnectedComponents(edges)
	tangleOf := make(map[*types.TypeName]int)

	for compIndex, comp := range components {
		for _, node := range comp {
			if obj, ok := pkg.TypesInfo.Defs[specs[node].Name].(*types.TypeName); ok {
				tangleOf[obj] = compIndex + 1
			}
		}
	}

	return typeTangleGraph{
		specs:      specs,
		components: components,
		tangleOf:   tangleOf,
	}
}

func collectProductionTypes(pkg *Package) ([]*ast.TypeSpec, map[*types.TypeName]int) {
	var specs []*ast.TypeSpec

	byType := make(map[*types.TypeName]int)

	for _, file := range pkg.ProductionFiles {
		if ast.IsGenerated(file) {
			continue
		}

		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}

			for _, raw := range gen.Specs {
				spec, ok := raw.(*ast.TypeSpec)
				if !ok {
					continue
				}

				obj, ok := pkg.TypesInfo.Defs[spec.Name].(*types.TypeName)
				if !ok || obj.IsAlias() {
					continue
				}

				byType[obj] = len(specs)
				specs = append(specs, spec)
			}
		}
	}

	return specs, byType
}

func collectTypeMethods(
	pkg *Package,
	byType map[*types.TypeName]int,
) (map[*types.Func]int, [][]*ast.FuncDecl) {
	funcOwner := make(map[*types.Func]int)
	ownedFuncs := make([][]*ast.FuncDecl, len(byType))

	for _, file := range pkg.ProductionFiles {
		if ast.IsGenerated(file) {
			continue
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil {
				continue
			}

			obj, ok := pkg.TypesInfo.Defs[fn.Name].(*types.Func)
			if !ok {
				continue
			}

			owner := methodOwnerTypeName(pkg.TypesPkg, obj.Signature())
			if owner == nil {
				continue
			}

			index, ok := byType[owner]
			if !ok {
				continue
			}

			funcOwner[obj.Origin()] = index
			ownedFuncs[index] = append(ownedFuncs[index], fn)
		}
	}

	return funcOwner, ownedFuncs
}

func collectTypeFields(
	specs []*ast.TypeSpec,
	info *types.Info,
	byType map[*types.TypeName]int,
) map[*types.Var]int {
	fieldOwner := make(map[*types.Var]int)

	for _, spec := range specs {
		obj, ok := info.Defs[spec.Name].(*types.TypeName)
		if !ok {
			continue
		}

		index := byType[obj]

		st, ok := obj.Type().Underlying().(*types.Struct)
		if !ok {
			continue
		}

		for field := range st.Fields() {
			fieldOwner[field.Origin()] = index
		}
	}

	return fieldOwner
}

func methodOwnerTypeName(pkg *types.Package, sig *types.Signature) *types.TypeName {
	recv := sig.Recv()
	if recv == nil {
		return nil
	}

	return namedPackageType(pkg, recv.Type())
}

// constructorTypeName returns the package named type that a free constructor
// function builds (returns T or *T while taking neither T nor *T as a param).
func constructorTypeName(pkg *types.Package, sig *types.Signature) *types.TypeName {
	if sig.Recv() != nil || sig.Results().Len() == 0 {
		return nil
	}

	built := namedPackageType(pkg, sig.Results().At(0).Type())
	if built == nil {
		return nil
	}

	for param := range sig.Params().Variables() {
		if namedPackageType(pkg, param.Type()) == built {
			return nil
		}
	}

	return built
}

func namedPackageType(pkg *types.Package, typ types.Type) *types.TypeName {
	typ = types.Unalias(typ)
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = types.Unalias(ptr.Elem())
	}

	named, ok := typ.(*types.Named)
	if !ok || named.Obj().Pkg() != pkg {
		return nil
	}

	return named.Origin().Obj()
}

func sortedIntKeys(values map[int]struct{}) []int {
	if len(values) == 0 {
		return nil
	}

	out := make([]int, 0, len(values))
	for value := range values {
		out = append(out, value)
	}

	slices.Sort(out)

	return out
}

func formatListedNames(names []string, limit int) string {
	if len(names) <= limit {
		return strings.Join(names, ", ")
	}

	return fmt.Sprintf("%s (+%d more)", strings.Join(names[:limit], ", "), len(names)-limit)
}

const (
	tarjanUnseen = -1
	minCycleSize = 2
)

// StronglyConnectedComponents returns every cycle component of at least two
// nodes in edges, with members and components sorted in ascending node order.
func StronglyConnectedComponents(edges [][]int) [][]int {
	state := tarjanState{
		edges:   edges,
		index:   make([]int, len(edges)),
		low:     make([]int, len(edges)),
		onStack: make([]bool, len(edges)),
	}

	return state.run()
}

type tarjanFrame struct {
	node int
	edge int
}

type tarjanState struct {
	edges   [][]int
	index   []int
	low     []int
	onStack []bool
	stack   []int
	next    int
	found   [][]int
}

func (s *tarjanState) run() [][]int {
	for i := range s.index {
		s.index[i] = tarjanUnseen
	}

	for start := range s.edges {
		if s.index[start] == tarjanUnseen {
			s.visit(start)
		}
	}

	slices.SortFunc(s.found, slices.Compare)

	return s.found
}

func (s *tarjanState) push(node int) {
	s.index[node] = s.next
	s.low[node] = s.next
	s.next++
	s.stack = append(s.stack, node)
	s.onStack[node] = true
}

func (s *tarjanState) popComponent(root int) {
	var component []int

	for {
		last := len(s.stack) - 1
		member := s.stack[last]
		s.stack = s.stack[:last]
		s.onStack[member] = false
		component = append(component, member)

		if member == root {
			break
		}
	}

	if len(component) >= minCycleSize {
		slices.Sort(component)
		s.found = append(s.found, component)
	}
}

func (s *tarjanState) visit(start int) {
	s.push(start)

	work := []tarjanFrame{{node: start}}

	for len(work) > 0 {
		top := &work[len(work)-1]
		node := top.node

		if top.edge < len(s.edges[node]) {
			to := s.edges[node][top.edge]
			top.edge++

			switch {
			case s.index[to] == tarjanUnseen:
				s.push(to)
				work = append(work, tarjanFrame{node: to})
			case s.onStack[to] && s.index[to] < s.low[node]:
				s.low[node] = s.index[to]
			}

			continue
		}

		work = work[:len(work)-1]

		if len(work) > 0 {
			parent := work[len(work)-1].node
			if s.low[node] < s.low[parent] {
				s.low[parent] = s.low[node]
			}
		}

		if s.low[node] == s.index[node] {
			s.popComponent(node)
		}
	}
}
