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

func (l *Runner) checkRepeatedNormalizationCallsPackage() {
	for _, file := range l.pkg.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			switch fn := n.(type) {
			case *ast.FuncDecl:
				l.checkRepeatedNormalizationCalls(fn.Body)
				return false
			case *ast.FuncLit:
				l.checkRepeatedNormalizationCalls(fn.Body)
				return false
			default:
				return true
			}
		})
	}
}

func (l *Runner) checkRepeatedNormalizationCalls(body *ast.BlockStmt) {
	if body == nil {
		return
	}

	occurrences := make([]normalizationOccurrence, 0)

	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			l.checkRepeatedNormalizationCalls(n.Body)

			return false
		case *ast.CallExpr:
			call, ok := l.normalizationCall(n)
			if !ok {
				return true
			}

			key := l.render(call)
			deps := l.normalizationDependencies(call)
			index := normalizationOccurrenceIndex(occurrences, key, deps)

			if index < 0 {
				occurrences = append(occurrences, normalizationOccurrence{
					key:     key,
					deps:    deps,
					lastEnd: call.End(),
					count:   1,
				})

				return false
			}

			occurrence := &occurrences[index]
			// Binding once is safe only while every input object keeps the same value.
			if l.normalizationDependenciesMayChange(
				body,
				occurrence.lastEnd,
				call.Pos(),
				deps,
			) {
				occurrence.lastEnd = call.End()
				occurrence.count = 1

				return false
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

			return false
		default:
			return true
		}
	})
}

func (l *Runner) normalizationDependencies(call *ast.CallExpr) map[types.Object]struct{} {
	out := make(map[types.Object]struct{})

	for node := range ast.Preorder(call) {
		ident, ok := node.(*ast.Ident)
		if !ok {
			continue
		}

		if obj, ok := l.pkg.TypesInfo.ObjectOf(ident).(*types.Var); ok && obj != nil {
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

func (l *Runner) normalizationDependenciesMayChange(
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
				if _, ok := deps[l.mutationRootObject(lhs)]; ok {
					changed = true
					return false
				}
			}
		case *ast.IncDecStmt:
			_, changed = deps[l.mutationRootObject(node.X)]
		case *ast.CallExpr:
			for _, arg := range node.Args {
				if _, ok := deps[l.mutationRootObject(arg)]; ok {
					changed = true
					return false
				}
			}

			if selector, ok := l.unparen(node.Fun).(*ast.SelectorExpr); ok {
				_, changed = deps[l.mutationRootObject(selector.X)]
			}
		}

		return !changed
	})

	return changed
}

func (l *Runner) mutationRootObject(expr ast.Expr) types.Object {
	switch expr := l.unparen(expr).(type) {
	case *ast.Ident:
		return l.pkg.TypesInfo.ObjectOf(expr)
	case *ast.SelectorExpr:
		return l.mutationRootObject(expr.X)
	case *ast.IndexExpr:
		return l.mutationRootObject(expr.X)
	case *ast.StarExpr:
		return l.mutationRootObject(expr.X)
	case *ast.UnaryExpr:
		if expr.Op == token.AND {
			return l.mutationRootObject(expr.X)
		}
	}

	return nil
}

func (l *Runner) normalizationCall(call *ast.CallExpr) (*ast.CallExpr, bool) {
	fn, _, ok := l.calledFunc(call)
	if !ok || fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != stringsImportPath {
		return nil, false
	}

	switch fn.Name() {
	case "TrimSpace":
		if len(call.Args) != 1 {
			return nil, false
		}

		return call, true
	case "ToLower", "ToUpper":
		if len(call.Args) != 1 || !l.isTrimSpaceCall(call.Args[0]) {
			return nil, false
		}

		return call, true
	default:
		return nil, false
	}
}

func (l *Runner) isTrimSpaceCall(expr ast.Expr) bool {
	call, ok := l.unparen(expr).(*ast.CallExpr)
	if !ok {
		return false
	}

	fn, _, ok := l.calledFunc(call)

	return ok &&
		fn != nil &&
		fn.Pkg() != nil &&
		fn.Pkg().Path() == "strings" &&
		fn.Name() == "TrimSpace" &&
		len(call.Args) == 1
}
