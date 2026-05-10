package smells

import (
	"fmt"
	"go/ast"
)

const repeatedNormalizationReportCount = 2

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

	counts := make(map[string]int)

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
			counts[key]++

			if counts[key] == repeatedNormalizationReportCount {
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

func (l *Runner) normalizationCall(call *ast.CallExpr) (*ast.CallExpr, bool) {
	fn, _, ok := l.calledFunc(call)
	if !ok || fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != "strings" {
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
