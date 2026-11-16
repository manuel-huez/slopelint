package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

const repeatedNormalizationReportCount = 2

type normalizationOccurrence struct {
	key     string
	deps    map[types.Object]struct{}
	lastEnd token.Pos
	count   int
}

// normalizationCheck reports a strings normalization computed twice on the
// same unchanged inputs in one function.
type normalizationCheck struct {
	ruleEnv
}

func (l normalizationCheck) check() {
	for _, file := range l.pkg.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			switch fn := n.(type) {
			case *ast.FuncDecl:
				l.checkBody(fn.Body)
				return false
			case *ast.FuncLit:
				l.checkBody(fn.Body)
				return false
			default:
				return true
			}
		})
	}
}

func (l normalizationCheck) checkBody(body *ast.BlockStmt) {
	if body == nil {
		return
	}

	occurrences := make([]normalizationOccurrence, 0)

	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			l.checkBody(n.Body)

			return false
		case *ast.CallExpr:
			if !isNormalizationCall(l.pkg.TypesInfo, n) {
				return true
			}

			occurrences = l.recordCall(body, occurrences, n)

			return false
		default:
			return true
		}
	})
}

func (l normalizationCheck) recordCall(
	body *ast.BlockStmt,
	occurrences []normalizationOccurrence,
	call *ast.CallExpr,
) []normalizationOccurrence {
	key := l.render(call)
	deps := normalizationDependencies(l.pkg.TypesInfo, call)
	index := normalizationOccurrenceIndex(occurrences, key, deps)

	if index < 0 {
		return append(occurrences, normalizationOccurrence{
			key:     key,
			deps:    deps,
			lastEnd: call.End(),
			count:   1,
		})
	}

	occurrence := &occurrences[index]
	// Binding once is safe only while every input object keeps the same value.
	if normalizationDependenciesMayChange(
		l.pkg.TypesInfo,
		body,
		occurrence.lastEnd,
		call.Pos(),
		deps,
	) {
		occurrence.lastEnd = call.End()
		occurrence.count = 1

		return occurrences
	}

	occurrence.count++
	occurrence.lastEnd = call.End()

	if occurrence.count == repeatedNormalizationReportCount {
		l.report(
			call.Pos(),
			"normalization_ceremony",
			fmt.Sprintf(
				`%q is computed multiple times in this function; bind normalized value once`,
				key,
			),
		)
	}

	return occurrences
}

func normalizationDependencies(info *types.Info, call *ast.CallExpr) map[types.Object]struct{} {
	out := make(map[types.Object]struct{})

	for node := range ast.Preorder(call) {
		ident, ok := node.(*ast.Ident)
		if !ok {
			continue
		}

		if obj, ok := info.ObjectOf(ident).(*types.Var); ok {
			out[obj] = struct{}{}
		}
	}

	return out
}

func normalizationOccurrenceIndex(
	occurrences []normalizationOccurrence,
	key string,
	deps map[types.Object]struct{},
) int {
	for index := range occurrences {
		if occurrences[index].key == key && sameObjectSet(occurrences[index].deps, deps) {
			return index
		}
	}

	return -1
}

func sameObjectSet(left, right map[types.Object]struct{}) bool {
	if len(left) != len(right) {
		return false
	}

	for obj := range left {
		if _, ok := right[obj]; !ok {
			return false
		}
	}

	return true
}

func normalizationDependenciesMayChange(
	info *types.Info,
	body *ast.BlockStmt,
	after token.Pos,
	before token.Pos,
	deps map[types.Object]struct{},
) bool {
	changed := false

	inspectNodesBetween(body, after, before, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if _, ok := deps[mutationRootObject(info, lhs)]; ok {
					changed = true
					return false
				}
			}
		case *ast.IncDecStmt:
			_, changed = deps[mutationRootObject(info, node.X)]
		case *ast.CallExpr:
			for _, arg := range node.Args {
				if _, ok := deps[mutationRootObject(info, arg)]; ok {
					changed = true
					return false
				}
			}

			if selector, ok := ast.Unparen(node.Fun).(*ast.SelectorExpr); ok {
				_, changed = deps[mutationRootObject(info, selector.X)]
			}
		}

		return !changed
	})

	return changed
}

func mutationRootObject(info *types.Info, expr ast.Expr) types.Object {
	switch expr := ast.Unparen(expr).(type) {
	case *ast.Ident:
		return info.ObjectOf(expr)
	case *ast.SelectorExpr:
		return mutationRootObject(info, expr.X)
	case *ast.IndexExpr:
		return mutationRootObject(info, expr.X)
	case *ast.StarExpr:
		return mutationRootObject(info, expr.X)
	case *ast.UnaryExpr:
		if expr.Op == token.AND {
			return mutationRootObject(info, expr.X)
		}
	}

	return nil
}

// isNormalizationCall matches strings.TrimSpace(x) and ToLower or ToUpper of
// strings.TrimSpace(x).
func isNormalizationCall(info *types.Info, call *ast.CallExpr) bool {
	fn, _, ok := calledFunc(info, call)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != stringsImportPath || len(call.Args) != 1 {
		return false
	}

	switch fn.Name() {
	case "TrimSpace":
		return true
	case "ToLower", "ToUpper":
		inner, ok := ast.Unparen(call.Args[0]).(*ast.CallExpr)

		return ok && len(inner.Args) == 1 &&
			isPackageFuncCall(info, inner, stringsImportPath, "TrimSpace")
	default:
		return false
	}
}
