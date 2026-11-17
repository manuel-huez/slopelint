package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"github.com/manuel-huez/slopelint/internal/lint/outline"
)

const (
	ownerFileLineLimit = 1000
	funcDeclLineLimit  = 100
	funcLitLineLimit   = 40
	funcParamLimit     = 6
	funcResultLimit    = 3
	typeMethodLimit    = 40
)

// sizeLimitDeleteFirst leads size advice: removing unneeded code often fixes
// the size, and splitting first would spread dead code across new owners.
const sizeLimitDeleteFirst = "first delete code that is no longer needed (legacy compatibility layers, redundant checks, defensive code for cases that cannot happen)"

// SizeFinding is one size-limit finding in files[File] of a CheckSizes call.
type SizeFinding struct {
	File    int
	Pos     outline.Pos
	Kind    string
	Message string
}

// CheckSizes reports size limits for files that share method owners: one Go
// package, or one TypeScript or Python file. Test files get only the file
// limit; generated files get none but still declare types.
func CheckSizes(files []outline.File) []SizeFinding {
	var findings []SizeFinding

	methods := make(map[string]int)
	methodFiles := make(map[string]map[int]struct{})

	for index, file := range files {
		if file.Generated {
			continue
		}

		if file.Lines > ownerFileLineLimit {
			findings = append(findings, SizeFinding{
				File: index,
				Pos:  file.Start,
				Kind: "oversized_owner_file",
				Message: fmt.Sprintf(
					"source file has %d lines; "+sizeLimitDeleteFirst+", then split the rest by responsibility so whole-file review stays tractable",
					file.Lines,
				),
			})
		}

		if file.Test {
			continue
		}

		for _, fn := range file.Funcs {
			for _, finding := range funcSizeFindings(fn) {
				finding.File = index
				findings = append(findings, finding)
			}

			if !fn.Literal && fn.Receiver != "" {
				methods[fn.Receiver]++

				if methodFiles[fn.Receiver] == nil {
					methodFiles[fn.Receiver] = make(map[int]struct{})
				}

				methodFiles[fn.Receiver][index] = struct{}{}
			}
		}
	}

	return append(findings, godReceiverFindings(files, methods, methodFiles)...)
}

// godReceiverFindings reports types of non-test files whose method count,
// gathered across files, passes the limit.
func godReceiverFindings(
	files []outline.File,
	methods map[string]int,
	methodFiles map[string]map[int]struct{},
) []SizeFinding {
	var findings []SizeFinding

	for index, file := range files {
		for _, typ := range file.Types {
			if file.Test || methods[typ.Name] <= typeMethodLimit {
				continue
			}

			findings = append(findings, SizeFinding{
				File: index,
				Pos:  typ.Pos,
				Kind: "god_receiver",
				Message: fmt.Sprintf(
					"type %q has %d methods across %d files; "+sizeLimitDeleteFirst+", then split the rest by responsibility so each new type owns its own state; moving methods to functions that take the type as a parameter is not a fix",
					typ.Name,
					methods[typ.Name],
					len(methodFiles[typ.Name]),
				),
			})
		}
	}

	return findings
}

func funcSizeFindings(fn outline.Func) []SizeFinding {
	var findings []SizeFinding

	report := func(kind, format string, args ...any) {
		findings = append(
			findings,
			SizeFinding{Pos: fn.Pos, Kind: kind, Message: fmt.Sprintf(format, args...)},
		)
	}

	lines := fn.EndLine - fn.StartLine + 1

	if fn.Literal {
		if lines > funcLitLineLimit {
			report(
				"long_func_literal",
				"function literal has %d lines; "+sizeLimitDeleteFirst+", then extract the rest into a named function",
				lines,
			)
		}

		return findings
	}

	if fn.Params > funcParamLimit {
		report(
			"long_param_list",
			"function %q has %d parameters; "+sizeLimitDeleteFirst+", then group related parameters into a struct or split the function",
			fn.Name,
			fn.Params,
		)
	}

	if fn.Results > funcResultLimit {
		report("long_result_list",
			"function %q returns %d results; "+sizeLimitDeleteFirst+", then return a named struct",
			fn.Name, fn.Results,
		)
	}

	if fn.Body && lines > funcDeclLineLimit {
		report(
			"long_function",
			"function %q has %d lines; "+sizeLimitDeleteFirst+", then split the rest by responsibility",
			fn.Name,
			lines,
		)
	}

	return findings
}

type sizeLimitCheck struct {
	ruleEnv
}

// check feeds the package to CheckSizes. Positions carry byte offsets so
// findings map back to token positions; lines follow //line directives as
// the rest of the Go report does.
func (l sizeLimitCheck) check() {
	production := make(map[*ast.File]bool, len(l.pkg.ProductionFiles))
	for _, file := range l.pkg.ProductionFiles {
		production[file] = true
	}

	files := make([]outline.File, len(l.pkg.Files))
	tokenFiles := make([]*token.File, len(l.pkg.Files))

	for index, file := range l.pkg.Files {
		tokenFiles[index] = l.pkg.FSet.File(file.Pos())
		files[index] = outline.File{
			Lines:     tokenFiles[index].LineCount(),
			Start:     l.outlinePos(file.Package),
			Test:      !production[file],
			Generated: ast.IsGenerated(file),
		}

		if production[file] {
			l.outlineDecls(file, &files[index])
		}
	}

	for _, finding := range CheckSizes(files) {
		l.report(tokenFiles[finding.File].Pos(finding.Pos.Offset), finding.Kind, finding.Message)
	}
}

func (l sizeLimitCheck) outlineDecls(file *ast.File, out *outline.File) {
	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *ast.GenDecl:
			if decl.Tok != token.TYPE {
				continue
			}

			for _, spec := range decl.Specs {
				if spec, ok := spec.(*ast.TypeSpec); ok {
					out.Types = append(out.Types, outline.Type{
						Name: spec.Name.Name,
						Pos:  l.outlinePos(spec.Name.Pos()),
					})
				}
			}
		case *ast.FuncDecl:
			if !out.Generated {
				out.Funcs = append(out.Funcs, l.outlineFunc(decl))
				out.Funcs = append(out.Funcs, l.outlineFuncLits(decl)...)
			}
		}
	}
}

func (l sizeLimitCheck) outlineFunc(fn *ast.FuncDecl) outline.Func {
	out := outline.Func{
		Name:      fn.Name.Name,
		Pos:       l.outlinePos(fn.Name.Pos()),
		StartLine: l.pkg.FSet.Position(fn.Pos()).Line,
		EndLine:   l.pkg.FSet.Position(fn.End()).Line,
		Params:    funcParamCount(fn.Type.Params),
		Results:   funcParamCount(fn.Type.Results),
		Body:      fn.Body != nil,
	}

	recv := l.pkg.TypesInfo.Defs[fn.Name].(*types.Func).Signature().Recv()
	if recv == nil {
		return out
	}

	typ := types.Unalias(recv.Type())
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = types.Unalias(ptr.Elem())
	}

	// Origin maps generic receivers such as Set[T] to their base type.
	out.Receiver = typ.(*types.Named).Origin().Obj().Name()

	return out
}

// outlineFuncLits returns the function literals inside a declaration's body;
// package-level literals are not checked.
func (l sizeLimitCheck) outlineFuncLits(fn *ast.FuncDecl) []outline.Func {
	if fn.Body == nil {
		return nil
	}

	var lits []outline.Func

	ast.Inspect(fn.Body, func(node ast.Node) bool {
		if lit, ok := node.(*ast.FuncLit); ok {
			lits = append(lits, outline.Func{
				Pos:       l.outlinePos(lit.Pos()),
				StartLine: l.pkg.FSet.Position(lit.Pos()).Line,
				EndLine:   l.pkg.FSet.Position(lit.End()).Line,
				Body:      true,
				Literal:   true,
			})
		}

		return true
	})

	return lits
}

func (l sizeLimitCheck) outlinePos(pos token.Pos) outline.Pos {
	position := l.pkg.FSet.Position(pos)

	return outline.Pos{Offset: position.Offset, Line: position.Line, Column: position.Column}
}
