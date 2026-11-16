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

// validationEffects classifies whether code that runs before input guards
// does real work. bodies indexes package functions on first use; visiting
// holds the functions on the current call path to stop recursion.
type validationEffects struct {
	pkg      *Package
	bodies   map[*types.Func]*ast.FuncDecl
	visiting map[*types.Func]bool
}

func (e *validationEffects) callEffect(call *ast.CallExpr) validationEffect {
	info := e.pkg.TypesInfo

	if literal, ok := ast.Unparen(call.Fun).(*ast.FuncLit); ok {
		return e.nodeEffect(literal.Body, literal)
	}

	if typ := info.Types[call.Fun]; typ.IsType() {
		return validationPure
	}

	if name := builtinName(info, call); name != "" {
		switch name {
		case "len", "cap", "complex", "imag", "real", "min", "max", "make", "new":
			return validationPure
		default:
			return validationWork
		}
	}

	if _, external := externalCallKind(info, call); external {
		return validationWork
	}

	fn := funcObject(info, call.Fun)
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
		return validationFormatEffect(info, call.Args)
	case "fmt.Print", "fmt.Printf", "fmt.Println", "fmt.Fprint", "fmt.Fprintf", "fmt.Fprintln",
		"log.Print", "log.Printf", "log.Println", "os.Open", "os.OpenFile", "os.Create",
		"os.ReadFile", "os.WriteFile", "os.Remove", "os.Rename",
		"io.ReadAll", "io.ReadFull", "io.ReadAtLeast", "io.Copy", "io.CopyN", "io.CopyBuffer":
		return validationWork
	}

	return e.funcEffect(fn)
}

func validationFormatEffect(info *types.Info, args []ast.Expr) validationEffect {
	// Named and interface values can invoke user-defined formatting methods.
	for _, arg := range args {
		if _, basic := types.Unalias(info.TypeOf(arg)).(*types.Basic); !basic {
			return validationWork
		}
	}

	return validationPure
}

func (e *validationEffects) funcEffect(fn *types.Func) validationEffect {
	// Unknown imported predicates are not evidence of interleaved work. Inspect
	// available bodies and known effects instead of guessing from function names.
	if fn.Pkg() != e.pkg.TypesPkg || e.visiting[fn] {
		return validationUnknown
	}

	e.visiting[fn] = true
	defer delete(e.visiting, fn)

	if e.bodies == nil {
		e.bodies = make(map[*types.Func]*ast.FuncDecl)
		for _, file := range e.pkg.Files {
			for _, decl := range file.Decls {
				if body, ok := decl.(*ast.FuncDecl); ok {
					if obj, ok := e.pkg.TypesInfo.ObjectOf(body.Name).(*types.Func); ok {
						e.bodies[obj] = body
					}
				}
			}
		}
	}

	fnDecl := e.bodies[fn]
	if fnDecl == nil || fnDecl.Body == nil {
		return validationUnknown
	}

	return e.nodeEffect(fnDecl.Body, fnDecl)
}

func (e *validationEffects) statementEffect(stmt ast.Stmt, body *ast.BlockStmt) validationEffect {
	if guard, ok := stmt.(*ast.IfStmt); ok && guard.Else == nil && len(guard.Body.List) == 1 {
		if _, returns := guard.Body.List[0].(*ast.ReturnStmt); returns {
			// A returning branch does not execute before subsequent guards.
			return max(e.nodeEffect(guard.Init, body), e.nodeEffect(guard.Cond, body))
		}
	}

	return e.nodeEffect(stmt, body)
}

// nodeEffect classifies node; writes count as work unless they target
// locals declared inside scope.
func (e *validationEffects) nodeEffect(node ast.Node, scope ast.Node) validationEffect {
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
			effect = max(effect, e.callEffect(node))
		default:
			effect = max(effect, validationMutationEffect(e.pkg.TypesInfo, node, scope))
		}

		return effect != validationWork
	})

	return effect
}

func validationMutationEffect(info *types.Info, node ast.Node, scope ast.Node) validationEffect {
	effect := validationPure

	var targets []ast.Expr

	switch node := node.(type) {
	case *ast.RangeStmt:
		// Ranging a channel receives; a range function executes arbitrary code.
		switch info.TypeOf(node.X).Underlying().(type) {
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
		if target != nil && !validationLocalWrite(info, target, scope) {
			effect = validationWork
			break
		}
	}

	return effect
}

func validationLocalWrite(info *types.Info, expr ast.Expr, scope ast.Node) bool {
	ident, ok := ast.Unparen(expr).(*ast.Ident)
	if !ok {
		return false
	}

	if ident.Name == "_" {
		return true
	}

	obj := info.ObjectOf(ident)

	return obj != nil && obj.Pos() >= scope.Pos() && obj.Pos() < scope.End()
}
