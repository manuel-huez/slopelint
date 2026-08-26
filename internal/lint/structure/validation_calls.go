package structure

import (
	"go/ast"
	"go/token"
	"go/types"
)

type validationEffect uint8

const (
	validationPure validationEffect = iota
	validationUnknown
	validationWork
)

func (l *Runner) inputValidationCall(
	call *ast.CallExpr,
	validationTemps map[types.Object]struct{},
) bool {
	if l.validationCallEffect(call, make(map[*types.Func]bool)) != validationPure {
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

func (l *Runner) validationCallEffect(
	call *ast.CallExpr,
	visited map[*types.Func]bool,
) validationEffect {
	if literal, ok := l.unparen(call.Fun).(*ast.FuncLit); ok {
		return l.validationNodeEffect(literal.Body, literal, visited)
	}

	if typ := l.pkg.TypesInfo.Types[call.Fun]; typ.IsType() {
		return validationPure
	}

	if ident, ok := l.unparen(call.Fun).(*ast.Ident); ok {
		if builtin, ok := l.pkg.TypesInfo.ObjectOf(ident).(*types.Builtin); ok {
			switch builtin.Name() {
			case "len", "cap", "complex", "imag", "real", "min", "max", "make", "new":
				return validationPure
			default:
				return validationWork
			}
		}
	}

	if _, external := l.externalCallLabel(call); external {
		return validationWork
	}

	fn := l.funcObject(call.Fun)
	if fn == nil {
		return validationUnknown
	}

	switch fn.FullName() {
	case "errors.New",
		"strings.TrimSpace", "strings.TrimPrefix", "strings.TrimSuffix", "strings.Trim",
		"strings.ToLower", "strings.ToUpper", "strings.EqualFold",
		"strings.HasPrefix", "strings.HasSuffix", "strings.Contains",
		"math.IsNaN", "math.IsInf", "encoding/hex.DecodeString", "mime.ParseMediaType",
		"(time.Time).IsZero", "(*net/url.URL).Hostname",
		"(net/http.Header).Get", "(net/textproto.MIMEHeader).Get", "(net/url.Values).Get":
		return validationPure
	case "fmt.Errorf", "fmt.Sprintf":
		return l.validationFormatEffect(call.Args)
	case "fmt.Print", "fmt.Printf", "fmt.Println", "fmt.Fprint", "fmt.Fprintf", "fmt.Fprintln",
		"log.Print", "log.Printf", "log.Println", "os.Open", "os.OpenFile", "os.Create",
		"os.ReadFile", "os.WriteFile", "os.Remove", "os.Rename",
		"io.ReadAll", "io.ReadFull", "io.ReadAtLeast", "io.Copy", "io.CopyN", "io.CopyBuffer":
		return validationWork
	}

	return l.validationFuncEffect(fn, visited)
}

func (l *Runner) validationFormatEffect(args []ast.Expr) validationEffect {
	// Named and interface values can invoke user-defined formatting methods.
	for _, arg := range args {
		if _, basic := types.Unalias(l.pkg.TypesInfo.TypeOf(arg)).(*types.Basic); !basic {
			return validationWork
		}
	}

	return validationPure
}

func (l *Runner) validationFuncEffect(
	fn *types.Func,
	visited map[*types.Func]bool,
) validationEffect {
	// Unknown imported predicates are not evidence of interleaved work. Inspect
	// available bodies and known effects instead of guessing from function names.
	if fn.Pkg() != l.pkg.TypesPkg || visited[fn] {
		return validationUnknown
	}

	visited[fn] = true
	defer delete(visited, fn)

	if l.validationFuncBodies == nil {
		l.validationFuncBodies = make(map[*types.Func]*ast.FuncDecl)
		for _, file := range l.pkg.Files {
			for _, decl := range file.Decls {
				if body, ok := decl.(*ast.FuncDecl); ok {
					if obj, ok := l.pkg.TypesInfo.ObjectOf(body.Name).(*types.Func); ok {
						l.validationFuncBodies[obj] = body
					}
				}
			}
		}
	}

	fnDecl := l.validationFuncBodies[fn]
	if fnDecl == nil || fnDecl.Body == nil {
		return validationUnknown
	}

	return l.validationNodeEffect(fnDecl.Body, fnDecl, visited)
}

func (l *Runner) validationStatementEffect(stmt ast.Stmt, body *ast.BlockStmt) validationEffect {
	visited := make(map[*types.Func]bool)

	if guard, ok := stmt.(*ast.IfStmt); ok && guard.Else == nil && len(guard.Body.List) == 1 {
		if _, returns := guard.Body.List[0].(*ast.ReturnStmt); returns {
			// A returning branch does not execute before subsequent guards.
			return max(l.validationNodeEffect(guard.Init, body, visited),
				l.validationNodeEffect(guard.Cond, body, visited))
		}
	}

	return l.validationNodeEffect(stmt, body, visited)
}

func (l *Runner) validationNodeEffect(
	node ast.Node,
	scope ast.Node,
	visited map[*types.Func]bool,
) validationEffect {
	if node == nil {
		return validationPure
	}

	effect := validationPure

	ast.Inspect(node, func(node ast.Node) bool {
		if effect == validationWork {
			return false
		}

		switch node := node.(type) {
		case *ast.GoStmt, *ast.DeferStmt, *ast.SendStmt, *ast.SelectStmt:
			effect = validationWork
		case *ast.FuncLit:
			return false

		case *ast.CallExpr:
			effect = max(effect, l.validationCallEffect(node, visited))
		default:
			effect = max(effect, l.validationMutationEffect(node, scope))
		}

		return effect != validationWork
	})

	return effect
}

func (l *Runner) validationMutationEffect(node ast.Node, scope ast.Node) validationEffect {
	effect := validationPure

	var targets []ast.Expr

	switch node := node.(type) {
	case *ast.RangeStmt:
		// Ranging a channel receives; a range function executes arbitrary code.
		switch l.pkg.TypesInfo.TypeOf(node.X).Underlying().(type) {
		case *types.Chan, *types.Signature:
			effect = validationWork
		}

		if node.Tok == token.ASSIGN {
			targets = []ast.Expr{node.Key, node.Value}
		}
	case *ast.UnaryExpr:
		if node.Op == token.ARROW {
			effect = validationWork
		}
	case *ast.AssignStmt:
		targets = node.Lhs
	case *ast.IncDecStmt:
		targets = []ast.Expr{node.X}
	}

	for _, target := range targets {
		if target != nil && !l.validationLocalWrite(target, scope) {
			effect = validationWork
			break
		}
	}

	return effect
}

func (l *Runner) validationLocalWrite(expr ast.Expr, scope ast.Node) bool {
	ident, ok := l.unparen(expr).(*ast.Ident)
	if !ok {
		return false
	}

	if ident.Name == "_" {
		return true
	}

	obj := l.pkg.TypesInfo.ObjectOf(ident)

	return obj != nil && obj.Pos() >= scope.Pos() && obj.Pos() < scope.End()
}
