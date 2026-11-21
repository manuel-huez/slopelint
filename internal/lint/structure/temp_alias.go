package structure

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/manuel-huez/slopelint/internal/lint/astutil"
	"github.com/manuel-huez/slopelint/internal/lint/outline"
)

const ancestorStackCap = 8

type tempAliasDecl struct {
	name *ast.Ident
	obj  types.Object
	rhs  ast.Expr
}

type objectUseSummary struct {
	reads  int
	unsafe bool
}

type tempAliasRules struct {
	q *pkgQuery
}

func (r tempAliasRules) singleUseAt(stmts []ast.Stmt, idx int) (Finding, bool) {
	if idx+1 >= len(stmts) {
		return Finding{}, false
	}

	info := r.q.pkg.TypesInfo

	decl, ok := tempAliasDeclFromStmt(info, stmts[idx])
	if !ok || r.q.hasAttachedComment(stmts[idx]) ||
		!tempAliasNameLooksDisposable(decl.name.Name, decl.rhs) {
		return Finding{}, false
	}

	nextUse := summarizeObjectUse(info, stmts[idx+1], decl.obj)
	if nextUse.unsafe || nextUse.reads != 1 {
		return Finding{}, false
	}
	// Inlining must not replace the captured value with a later field or variable value.
	if nodeMayMutateTempAliasSource(info, stmts[idx+1], decl.rhs) {
		return Finding{}, false
	}

	for _, later := range stmts[idx+2:] {
		if astutil.UsesObject(later, decl.obj, info) {
			return Finding{}, false
		}
	}

	return Finding{
		Pos:  decl.name.Pos(),
		Kind: "temp_alias",
		Message: fmt.Sprintf(
			`local %q only renames cheap expression %q for one use; inline expression`,
			decl.name.Name,
			r.q.render(decl.rhs),
		),
	}, true
}

func nodeMayMutateTempAliasSource(info *types.Info, node ast.Node, source ast.Expr) bool {
	objects := make(map[types.Object]struct{})

	ast.Inspect(source, func(node ast.Node) bool {
		ident, ok := node.(*ast.Ident)
		if !ok {
			return true
		}

		if obj, ok := info.ObjectOf(ident).(*types.Var); ok {
			objects[obj] = struct{}{}
		}

		return true
	})

	if len(objects) == 0 {
		return false
	}

	for candidate := range ast.Preorder(node) {
		if mutationTargetsObject(info, candidate, objects) {
			return true
		}
	}

	return false
}

func mutationTargetsObject(
	info *types.Info,
	node ast.Node,
	objects map[types.Object]struct{},
) bool {
	rootMatches := func(expr ast.Expr) bool {
		_, ok := objects[objectForMutationRoot(info, expr)]

		return ok
	}

	switch node := node.(type) {
	case *ast.AssignStmt:
		return slices.ContainsFunc(node.Lhs, rootMatches)
	case *ast.IncDecStmt:
		return rootMatches(node.X)
	case *ast.CallExpr:
		if slices.ContainsFunc(node.Args, rootMatches) {
			return true
		}

		selector, ok := ast.Unparen(node.Fun).(*ast.SelectorExpr)

		return ok && rootMatches(selector.X)
	default:
		return false
	}
}

func tempAliasDeclFromStmt(info *types.Info, stmt ast.Stmt) (tempAliasDecl, bool) {
	name, rhs, ok := singleLocalDecl(stmt)
	if !ok || name.Name == "_" {
		return tempAliasDecl{}, false
	}

	obj, ok := info.ObjectOf(name).(*types.Var)
	if !ok || obj.IsField() {
		return tempAliasDecl{}, false
	}

	rhs = ast.Unparen(rhs)
	if !isVarOrFieldRead(info, rhs) {
		return tempAliasDecl{}, false
	}

	return tempAliasDecl{name: name, obj: obj, rhs: rhs}, true
}

// singleLocalDecl matches `name := rhs` and `var name = rhs`.
func singleLocalDecl(stmt ast.Stmt) (*ast.Ident, ast.Expr, bool) {
	switch stmt := stmt.(type) {
	case *ast.AssignStmt:
		if stmt.Tok != token.DEFINE || len(stmt.Lhs) != 1 || len(stmt.Rhs) != 1 {
			return nil, nil, false
		}

		ident, ok := stmt.Lhs[0].(*ast.Ident)

		return ident, stmt.Rhs[0], ok
	case *ast.DeclStmt:
		decl, ok := stmt.Decl.(*ast.GenDecl)
		if !ok || decl.Tok != token.VAR || len(decl.Specs) != 1 {
			return nil, nil, false
		}

		return singleUntypedValue(decl.Specs[0])
	default:
		return nil, nil, false
	}
}

func singleUntypedValue(raw ast.Spec) (*ast.Ident, ast.Expr, bool) {
	spec, ok := raw.(*ast.ValueSpec)
	if !ok || spec.Type != nil || len(spec.Names) != 1 || len(spec.Values) != 1 {
		return nil, nil, false
	}

	return spec.Names[0], spec.Values[0], true
}

func isVarOrFieldRead(info *types.Info, expr ast.Expr) bool {
	expr = ast.Unparen(expr)

	switch expr := expr.(type) {
	case *ast.Ident:
		if expr.Name == "_" {
			return false
		}

		if _, ok := info.ObjectOf(expr).(*types.PkgName); ok {
			return false
		}

		_, isFn := types.Unalias(info.TypeOf(expr)).Underlying().(*types.Signature)

		return !isFn
	case *ast.SelectorExpr:
		sel := info.Selections[expr]
		if sel == nil || sel.Kind() != types.FieldVal {
			return false
		}

		_, isFn := types.Unalias(info.TypeOf(expr)).Underlying().(*types.Signature)

		return !isFn && isVarOrFieldRead(info, expr.X)
	default:
		return false
	}
}

func tempAliasNameLooksDisposable(name string, rhs ast.Expr) bool {
	leaf, ok := tempAliasLeafName(rhs)
	if !ok {
		return false
	}

	nameWords := outline.AliasWords(name)

	// A leading underscore changes a Go name, so `_x := x` is not a rename.
	return len(nameWords) != 0 && strings.HasPrefix(name, "_") == strings.HasPrefix(leaf, "_") &&
		slices.Equal(nameWords, outline.AliasWords(leaf))
}

func tempAliasLeafName(expr ast.Expr) (string, bool) {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr.Name, true
	case *ast.SelectorExpr:
		return expr.Sel.Name, true
	default:
		return "", false
	}
}

func summarizeObjectUse(info *types.Info, node ast.Node, target types.Object) objectUseSummary {
	var out objectUseSummary

	inspectWithAncestors(node, func(n ast.Node, ancestors []ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			if astutil.UsesObject(n.Body, target, info) {
				out.unsafe = true
			}

			return false
		case *ast.Ident:
			if info.ObjectOf(n) != target {
				return !out.unsafe
			}

			if objectUseIsUnsafe(n, ancestors) {
				out.unsafe = true
				return false
			}

			out.reads++
		}

		return !out.unsafe
	})

	return out
}

func objectUseIsUnsafe(ident *ast.Ident, ancestors []ast.Node) bool {
	for _, ancestor := range slices.Backward(ancestors) {
		switch node := ancestor.(type) {
		case *ast.UnaryExpr:
			if node.Op == token.AND && nodeContains(node.X, ident) {
				return true
			}
		case *ast.IncDecStmt:
			return true
		case *ast.AssignStmt:
			if nodeContainedInExprList(ident, node.Lhs) {
				return true
			}
		case *ast.RangeStmt:
			if nodeContains(node.Key, ident) || nodeContains(node.Value, ident) {
				return true
			}
		}
	}

	return false
}

func inspectWithAncestors(node ast.Node, fn func(ast.Node, []ast.Node) bool) {
	ancestors := make([]ast.Node, 0, ancestorStackCap)

	ast.Inspect(node, func(n ast.Node) bool {
		if n == nil {
			ancestors = ancestors[:len(ancestors)-1]
			return false
		}

		keep := fn(n, ancestors)
		if keep {
			ancestors = append(ancestors, n)
		}

		return keep
	})
}

func nodeContainedInExprList(target ast.Node, exprs []ast.Expr) bool {
	for _, expr := range exprs {
		if nodeContains(expr, target) {
			return true
		}
	}

	return false
}

func nodeContains(root, target ast.Node) bool {
	if root == nil {
		return false
	}

	found := false

	ast.Inspect(root, func(n ast.Node) bool {
		if found || n == nil {
			return !found
		}

		if n == target {
			found = true
			return false
		}

		return true
	})

	return found
}
