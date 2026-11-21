package smells

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"slices"
	"strings"

	"github.com/manuel-huez/slopelint/internal/lint/outline"
)

const (
	ownerFileLineLimit = 1000
	funcStatementLimit = 40
	funcParamLimit     = 6
	funcResultLimit    = 3
	typeMethodLimit    = 40
)

// OutlineFinding is one finding in files[File] of a CheckSizes call.
type OutlineFinding struct {
	File    int
	Pos     outline.Pos
	Kind    string
	Message string
}

// CheckSizes reports size limits for files that share method owners: one Go
// package, or one TypeScript or Python file. Test files get only the file
// limit; generated files get none but still declare types.
func CheckSizes(files []outline.File) []OutlineFinding {
	var findings []OutlineFinding

	methods := make(map[string]int)
	methodFiles := make(map[string]map[int]struct{})

	for index, file := range files {
		if file.Generated {
			continue
		}

		if file.Lines > ownerFileLineLimit {
			findings = append(findings, OutlineFinding{
				File: index,
				Pos:  file.Start,
				Kind: "oversized_owner_file",
				Message: fmt.Sprintf(
					"source file has %d lines",
					file.Lines,
				),
			})
		}

		if file.Test {
			continue
		}

		for _, fn := range file.Funcs {
			for _, finding := range funcOutlineFindings(fn) {
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

func godReceiverFindings(
	files []outline.File,
	methods map[string]int,
	methodFiles map[string]map[int]struct{},
) []OutlineFinding {
	// A type embedded by several types is a shared helper, not part of one
	// type split up, so only types with a single embedder add methods.
	embeds := make(map[string][]string)
	embedders := make(map[string]int)

	for _, file := range files {
		for _, typ := range file.Types {
			embeds[typ.Name] = typ.Embeds
			for _, embedded := range typ.Embeds {
				embedders[embedded]++
			}
		}
	}

	for name, embedded := range embeds {
		embeds[name] = slices.DeleteFunc(slices.Clone(embedded), func(embedded string) bool {
			return embedders[embedded] > 1
		})
	}

	var findings []OutlineFinding

	for index, file := range files {
		for _, typ := range file.Types {
			own := methods[typ.Name]
			total, embedded := own, promotedFrom(typ.Name, embeds, methods, map[string]bool{})

			for _, count := range embedded {
				total += count
			}

			if file.Test || total <= typeMethodLimit {
				continue
			}

			findings = append(findings, OutlineFinding{
				File: index,
				Pos:  typ.Pos,
				Kind: "god_receiver",
				Message: godReceiverMessage(
					typ.Name,
					own,
					total,
					len(methodFiles[typ.Name]),
					embedded,
				),
			})
		}
	}

	return findings
}

// promotedFrom counts the methods each embedded type adds to name, nested
// embeds included. seen stops pointer-embedding cycles.
func promotedFrom(
	name string,
	embeds map[string][]string,
	methods map[string]int,
	seen map[string]bool,
) map[string]int {
	seen[name] = true
	counts := make(map[string]int)

	for _, embedded := range embeds[name] {
		if seen[embedded] {
			continue
		}

		counts[embedded] += methods[embedded]
		for nested, count := range promotedFrom(embedded, embeds, methods, seen) {
			counts[nested] += count
		}
	}

	return counts
}

func godReceiverMessage(name string, own, total, fileCount int, embedded map[string]int) string {
	files := fmt.Sprintf("%d files", fileCount)
	if fileCount == 1 {
		files = "1 file"
	}

	if len(embedded) == 0 {
		return fmt.Sprintf("type %q has %d methods in %s", name, total, files)
	}

	parts := make([]string, 0, len(embedded))
	for _, embed := range slices.Sorted(maps.Keys(embedded)) {
		parts = append(parts, fmt.Sprintf("%s %d", embed, embedded[embed]))
	}

	return fmt.Sprintf(
		"type %q has %d methods: %d of its own in %s, plus embedded %s",
		name, total, own, files, strings.Join(parts, ", "),
	)
}

func funcOutlineFindings(fn outline.Func) []OutlineFinding {
	var findings []OutlineFinding

	report := func(kind, format string, args ...any) {
		findings = append(
			findings,
			OutlineFinding{Pos: fn.Pos, Kind: kind, Message: fmt.Sprintf(format, args...)},
		)
	}

	// A literal's statements count toward the function that holds it.
	if fn.Literal {
		return findings
	}

	if fn.Params > funcParamLimit && !fn.Callback {
		report(
			"long_param_list",
			"function %q has %d parameters",
			fn.Name,
			fn.Params,
		)
	}

	if fn.Results > funcResultLimit {
		report("long_result_list",
			"function %q returns %d results",
			fn.Name, fn.Results,
		)
	}

	if fn.Body && fn.Statements > funcStatementLimit {
		report(
			"long_function",
			"function %q has %d statements%s%s",
			fn.Name,
			fn.Statements,
			largestStatementsText(fn.Largest),
			movableClosuresText(fn.Movable),
		)
	}

	return findings
}

func movableClosuresText(closures []outline.StatementBlock) string {
	switch len(closures) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf(
			"; the closure at %s uses nothing from the function, so it can move out",
			blockText(closures[0]),
		)
	default:
		return "; the closures at " + blockList(
			closures,
		) + " use nothing from the function, so they can move out"
	}
}

func blockText(block outline.StatementBlock) string {
	return fmt.Sprintf("line %d (%d)", block.Line, block.Statements)
}

func blockList(blocks []outline.StatementBlock) string {
	parts := make([]string, len(blocks))
	for index, block := range blocks {
		parts[index] = blockText(block)
	}

	if len(parts) == 1 {
		return parts[0]
	}

	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

func largestStatementsText(blocks []outline.StatementBlock) string {
	switch len(blocks) {
	case 0:
		return ""
	case 1:
		return "; the largest statement is at " + blockText(blocks[0])
	default:
		return "; the largest are at " + blockList(blocks)
	}
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
						Name:   spec.Name.Name,
						Pos:    l.outlinePos(spec.Name.Pos()),
						Embeds: l.embeddedTypes(spec),
					})
				}
			}
		case *ast.FuncDecl:
			if !out.Generated {
				out.Funcs = append(out.Funcs, l.outlineFunc(decl))
			}
		}
	}
}

// embeddedTypes names the package types a struct embeds. Their methods are
// promoted, so moving methods into a type only it embeds does not shrink it.
func (l sizeLimitCheck) embeddedTypes(spec *ast.TypeSpec) []string {
	st, ok := spec.Type.(*ast.StructType)
	if !ok {
		return nil
	}

	var names []string

	for _, field := range st.Fields.List {
		if len(field.Names) > 0 {
			continue
		}

		typ := types.Unalias(l.pkg.TypesInfo.TypeOf(field.Type))
		if ptr, ok := typ.(*types.Pointer); ok {
			typ = types.Unalias(ptr.Elem())
		}

		if named, ok := typ.(*types.Named); ok && named.Obj().Pkg() == l.pkg.TypesPkg {
			names = append(names, named.Origin().Obj().Name())
		}
	}

	return names
}

func (l sizeLimitCheck) outlineFunc(fn *ast.FuncDecl) outline.Func {
	out := outline.Func{
		Name:       fn.Name.Name,
		Pos:        l.outlinePos(fn.Name.Pos()),
		Statements: statementCount(fn.Body),
		Largest:    l.largestStatements(fn.Body),
		Movable:    l.movableClosures(fn),
		Params:     funcParamCount(fn.Type.Params),
		Results:    funcParamCount(fn.Type.Results),
		Body:       fn.Body != nil,
	}

	signature := l.pkg.TypesInfo.Defs[fn.Name].(*types.Func).Signature()
	if recv := signature.Recv(); recv != nil {
		typ := types.Unalias(recv.Type())
		if ptr, ok := typ.(*types.Pointer); ok {
			typ = types.Unalias(ptr.Elem())
		}

		// Origin maps generic receivers such as Set[T] to their base type.
		out.Receiver = typ.(*types.Named).Origin().Obj().Name()

		return out
	}

	// A function that takes a pointer to a package type first is a method in
	// all but name, so turning methods into such functions does not shrink
	// the type.
	if signature.Params().Len() == 0 {
		return out
	}

	ptr, ok := types.Unalias(signature.Params().At(0).Type()).(*types.Pointer)
	if !ok {
		return out
	}

	if named, ok := types.Unalias(ptr.Elem()).(*types.Named); ok &&
		named.Obj().Pkg() == l.pkg.TypesPkg {
		out.Receiver = named.Origin().Obj().Name()
	}

	return out
}

func (l sizeLimitCheck) outlinePos(pos token.Pos) outline.Pos {
	position := l.pkg.FSet.Position(pos)

	return outline.Pos{Offset: position.Offset, Line: position.Line, Column: position.Column}
}
