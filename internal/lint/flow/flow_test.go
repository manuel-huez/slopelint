package flow

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

func TestStateDedupePreservesFirstSeenOrder(t *testing.T) {
	t.Parallel()

	const (
		firstKey  = "first"
		secondKey = "second"
	)

	first := newState()
	first.facts[firstKey] = fact{not: map[string]evidence{"nil": {text: firstKey}}}
	second := newState()
	second.facts[secondKey] = fact{not: map[string]evidence{"nil": {text: secondKey}}}

	states := dedupeStateSlice([]state{first, second, first})
	if len(states) != 2 {
		t.Fatalf("dedupeStateSlice length = %d, want 2", len(states))
	}

	if states[0].hash() != first.hash() || states[1].hash() != second.hash() {
		t.Fatalf("dedupeStateSlice reordered first-seen states")
	}

	returns := dedupeReturnStates([]returnState{
		{state: first},
		{state: second},
		{state: first},
	})
	if len(returns) != 2 {
		t.Fatalf("dedupeReturnStates length = %d, want 2", len(returns))
	}

	if returns[0].state.hash() != first.hash() || returns[1].state.hash() != second.hash() {
		t.Fatalf("dedupeReturnStates reordered first-seen states")
	}
}

func TestRangeLoopTracksLocalHelperClosureWrites(t *testing.T) {
	t.Parallel()

	const src = `package sample

func f(values []int) {
	var (
		firstErr error
		run      func(func())
	)

	fail := func(err error) {
		if err == nil { return }
		run(func() {
			firstErr = err
		})
	}

	for _, value := range values {
		if value > 0 {
			fail(nil)
		}
	}

	_ = firstErr
}
`

	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, "sample.go", src, 0)
	if err != nil {
		t.Fatalf("parse sample: %v", err)
	}

	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}

	cfg := types.Config{Importer: importer.Default()}
	if _, err := cfg.Check("sample", fset, []*ast.File{file}, info); err != nil {
		t.Fatalf("typecheck sample: %v", err)
	}

	rangeStmt := firstRangeStmt([]*ast.File{file})
	if rangeStmt == nil {
		t.Fatal("expected range loop")
	}

	firstErrObj := namedObject([]*ast.File{file}, info, "firstErr")
	if firstErrObj == nil {
		t.Fatal("expected firstErr object")
	}

	q := &exprQuery{
		info:          info,
		localFuncLits: collectLocalFuncLits([]*ast.File{file}, info),
	}

	invalidations := loopInvalidations(q, rangeStmt.Body.List, nil)
	if invalidations[symbolForObject(firstErrObj).key] != invalidateFullPrefix {
		t.Fatalf(
			"expected local helper closure write to invalidate firstErr, got %#v",
			invalidations,
		)
	}
}

func TestStateHashIncludesAliasAndBindingsWithoutFacts(t *testing.T) {
	t.Parallel()

	empty := newState()
	withAlias := linkAlias(newState(), "pkg:a:1", "pkg:b:2")

	if empty.hash() == withAlias.hash() {
		t.Fatalf("alias-only state collapsed into empty hash %q", empty.hash())
	}

	withBinding := newState()
	withBinding.bindings["pkg:ok:3"] = resultBinding{roots: []string{"pkg:req:4"}}

	if empty.hash() == withBinding.hash() {
		t.Fatalf("binding-only state collapsed into empty hash %q", empty.hash())
	}
}

func firstRangeStmt(files []*ast.File) *ast.RangeStmt {
	var out *ast.RangeStmt

	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			if out != nil {
				return false
			}

			stmt, ok := n.(*ast.RangeStmt)
			if !ok {
				return true
			}

			out = stmt

			return false
		})
	}

	return out
}

func namedObject(files []*ast.File, info *types.Info, name string) *types.Var {
	for _, file := range files {
		var out *types.Var

		ast.Inspect(file, func(n ast.Node) bool {
			if out != nil {
				return false
			}

			ident, ok := n.(*ast.Ident)
			if !ok || ident.Name != name {
				return true
			}

			obj, ok := info.ObjectOf(ident).(*types.Var)
			if !ok || obj == nil {
				return true
			}

			out = obj

			return false
		})

		if out != nil {
			return out
		}
	}

	return nil
}
