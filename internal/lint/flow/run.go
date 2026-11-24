package flow

import (
	"go/ast"
	"go/token"
	"go/types"
	"maps"

	smellcheck "github.com/manuel-huez/slopelint/internal/lint/smells"
)

const (
	defaultMaxStates = 32
	boolTrueText     = "true"
	boolFalseText    = "false"
	zeroIntText      = "0"
	panicText        = "panic"
)

// Package holds the parsed and type-checked inputs for path analysis.
type Package struct {
	Files     []*ast.File
	FSet      *token.FileSet
	TypesPkg  *types.Package
	TypesInfo *types.Info
}

// Export is one package function's inferred and explicit summary.
type Export struct {
	Obj     *types.Func
	Key     string
	Summary CallSummary
}

// Options configures a single-package flow run.
type Options struct {
	MaxStates int
	External  func(*types.Func) (CallSummary, bool)
	Report    func(pos token.Pos, kind, msg string)
	OnFunc    func(fnType *ast.FuncType, recv *ast.FieldList, body *ast.BlockStmt)
}

type flowResult struct {
	next             []state
	breaks           []state
	continues        []state
	fallthroughs     []state
	labeledBreaks    map[string][]state
	labeledContinues map[string][]state
	returns          []returnState
}

type returnKind uint8

const (
	returnUnspecified returnKind = iota
	returnBoolTrue
	returnBoolFalse
	returnNil
	returnNonNil
)

type returnState struct {
	state state
	kinds map[int]returnKind
}

func cloneReturnKinds(in map[int]returnKind) map[int]returnKind {
	if len(in) == 0 {
		return nil
	}

	out := make(map[int]returnKind, len(in))
	maps.Copy(out, in)

	return out
}

// Run infers package call summaries and walks all functions in pkg.
func Run(pkg *Package, opts Options) []Export {
	if opts.MaxStates <= 0 {
		opts.MaxStates = defaultMaxStates
	}

	external := opts.External
	if external == nil {
		external = func(*types.Func) (CallSummary, bool) { return CallSummary{}, false }
	}

	report := opts.Report
	if report == nil {
		report = discardFinding
	}

	q := &exprQuery{
		fset:          pkg.FSet,
		info:          pkg.TypesInfo,
		typesPkg:      pkg.TypesPkg,
		renderCache:   make(map[ast.Node]string),
		localFuncLits: collectLocalFuncLits(pkg.Files, pkg.TypesInfo),
	}
	explicit := collectContracts(pkg, report)
	funcs := summarizableFuncs(pkg)
	calls := &callFacts{
		q:        q,
		budget:   stateBudget(opts.MaxStates),
		explicit: explicit,
		inferred: make(map[string]CallSummary, len(funcs)),
		external: external,
	}

	parallel := smellcheck.ParallelParams(pkg.TypesInfo, pkg.Files, func(fn *types.Func) []int {
		summary, _ := external(fn)

		return summary.ParallelParams
	})
	for index := range funcs {
		funcs[index].parallel = parallel[funcs[index].obj]
	}

	// Summary walks repeat the main walk, so their diagnostics are dropped.
	inferCallSummaries(funcs, calls, newPathWalker(q, calls, discardFinding))
	analyzePackageFiles(pkg.Files, newPathWalker(q, calls, report), opts.OnFunc)

	exports := make([]Export, 0, len(funcs))
	for _, fn := range funcs {
		exports = append(exports, Export{
			Obj:     fn.obj,
			Key:     fn.key,
			Summary: calls.exported(fn),
		})
	}

	return exports
}

// PackageFuncObjects maps function keys to type objects for summarizable package functions.
func PackageFuncObjects(pkg *Package) map[string]*types.Func {
	out := make(map[string]*types.Func)

	for _, fn := range summarizableFuncs(pkg) {
		out[fn.key] = fn.obj
	}

	return out
}

func analyzePackageFiles(
	files []*ast.File,
	walker *pathWalker,
	onFunc func(*ast.FuncType, *ast.FieldList, *ast.BlockStmt),
) {
	analyzeFunction := func(fnType *ast.FuncType, recv *ast.FieldList, body *ast.BlockStmt) {
		if hasUnsupportedJumps(body) {
			return
		}

		if onFunc != nil {
			onFunc(fnType, recv, body)
		}

		walker.execBlock(body.List, []state{newState()})
	}

	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			switch fn := n.(type) {
			case *ast.FuncDecl:
				if fn.Body != nil {
					analyzeFunction(fn.Type, fn.Recv, fn.Body)
				}

				return false
			case *ast.FuncLit:
				analyzeFunction(fn.Type, nil, fn.Body)
				return false
			default:
				return true
			}
		})
	}
}

func hasUnsupportedJumps(body *ast.BlockStmt) bool {
	unsupported := false

	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.BranchStmt:
			if n.Tok == token.GOTO {
				unsupported = true
				return false
			}
		}

		return !unsupported
	})

	return unsupported
}
