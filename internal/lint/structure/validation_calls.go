package structure

import (
	"go/ast"
	"go/token"
	"go/types"
)

func (l *Runner) inputValidationCall(
	call *ast.CallExpr,
	validationTemps map[types.Object]struct{},
) bool {
	if !l.validationCallHasNoEffects(call) {
		return false
	}

	if selector, ok := l.unparen(call.Fun).(*ast.SelectorExpr); ok {
		if selection := l.pkg.TypesInfo.Selections[selector]; selection != nil &&
			selection.Kind() == types.MethodVal &&
			!l.inputValidationExpr(selector.X, validationTemps) {
			return false
		}
	}

	for _, arg := range call.Args {
		if !l.inputValidationExpr(arg, validationTemps) {
			return false
		}
	}

	return true
}

func (l *Runner) validationCallHasNoEffects(call *ast.CallExpr) bool {
	if call == nil {
		return false
	}

	if typ := l.pkg.TypesInfo.Types[call.Fun]; typ.IsType() {
		return true
	}

	if ident, ok := l.unparen(call.Fun).(*ast.Ident); ok {
		if builtin, ok := l.pkg.TypesInfo.ObjectOf(ident).(*types.Builtin); ok {
			switch builtin.Name() {
			case "len", "cap", "complex", "imag", "real", "min", "max", "make", "new":
				return true
			default:
				return false
			}
		}
	}

	fn := l.funcObject(call.Fun)
	if fn == nil {
		return false
	}

	if fn.FullName() == "fmt.Errorf" || fn.FullName() == "fmt.Sprintf" {
		return l.validationFormatArgsAreBasic(call.Args)
	}

	return l.validationFuncHasNoEffects(fn)
}

func (l *Runner) validationFormatArgsAreBasic(args []ast.Expr) bool {
	// Named and interface values can invoke user-defined formatting methods.
	for _, arg := range args {
		if _, basic := types.Unalias(l.pkg.TypesInfo.TypeOf(arg)).(*types.Basic); !basic {
			return false
		}
	}

	return true
}

func (l *Runner) validationFuncHasNoEffects(fn *types.Func) bool {
	// Keep imported calls explicit: a bool result or an Is/Parse name proves no purity.
	switch fn.FullName() {
	case "errors.New",
		"strings.TrimSpace", "strings.TrimPrefix", "strings.TrimSuffix", "strings.Trim",
		"strings.ToLower", "strings.ToUpper", "strings.EqualFold",
		"strings.HasPrefix", "strings.HasSuffix", "strings.Contains",
		"math.IsNaN", "math.IsInf", "encoding/hex.DecodeString", "mime.ParseMediaType",
		"(time.Time).IsZero", "(*net/url.URL).Hostname",
		"(net/http.Header).Get", "(net/textproto.MIMEHeader).Get", "(net/url.Values).Get":
		return true
	}

	if fn.Pkg() != l.pkg.TypesPkg {
		return false
	}

	if pure, found := l.validationPureFuncs[fn]; found {
		return pure
	}

	if l.validationPureFuncs == nil {
		l.validationPureFuncs = make(map[*types.Func]bool)
	}

	// A recursive or unavailable body stays unknown; never assume it is preparation.
	l.validationPureFuncs[fn] = false
	for _, file := range l.pkg.Files {
		for _, decl := range file.Decls {
			body, ok := decl.(*ast.FuncDecl)
			if ok && l.pkg.TypesInfo.ObjectOf(body.Name) == fn {
				l.validationPureFuncs[fn] = l.validationBodyHasNoEffects(body)
				return l.validationPureFuncs[fn]
			}
		}
	}

	return false
}

func (l *Runner) validationBodyHasNoEffects(fn *ast.FuncDecl) bool {
	if fn.Body == nil {
		return false
	}

	pure := true

	ast.Inspect(fn.Body, func(node ast.Node) bool {
		if !pure {
			return false
		}

		switch node := node.(type) {
		case *ast.GoStmt, *ast.DeferStmt, *ast.SendStmt,
			*ast.SelectStmt, *ast.RangeStmt, *ast.FuncLit:
			pure = false
		case *ast.UnaryExpr:
			pure = node.Op != token.ARROW
		case *ast.CallExpr:
			pure = l.validationCallHasNoEffects(node)
		case *ast.AssignStmt:
			for _, target := range node.Lhs {
				if !l.validationLocalWrite(target, fn) {
					pure = false
					break
				}
			}
		case *ast.IncDecStmt:
			pure = l.validationLocalWrite(node.X, fn)
		}

		return pure
	})

	return pure
}

func (l *Runner) validationLocalWrite(expr ast.Expr, fn *ast.FuncDecl) bool {
	ident, ok := l.unparen(expr).(*ast.Ident)
	if !ok {
		return false
	}

	if ident.Name == "_" {
		return true
	}

	obj := l.pkg.TypesInfo.ObjectOf(ident)

	return obj != nil && obj.Pos() >= fn.Pos() && obj.Pos() < fn.End()
}
