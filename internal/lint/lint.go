package lint

import (
	"go/ast"
	"go/token"
	"go/types"
	"maps"

	structurecheck "github.com/manuel-huez/slopelint/internal/lint/structure"
)

const (
	allPackagesPattern = "./..."
	boolTrueText       = "true"
	boolFalseText      = "false"
	zeroIntText        = "0"
	offText            = "off"
	panicText          = "panic"
	mainPkgName        = "main"
	unknownPos         = "unknown position"
)

// Options controls linter behavior.
type Options struct {
	MaxStates          int
	CacheEnabled       bool
	cacheDir           string
	cacheBuildID       string
	CacheHitHook       func(string)
	ClosedWorld        bool
	skipDeadCode       bool
	skipBehaviorClones bool
}

// linter runs every check for one package: path analysis plus the smells,
// structure and dead-code sub-packages. run fills funcs and calls, which
// cache and fact export read afterwards.
type linter struct {
	pkg                *LoadedPackage
	index              packageIndex
	budget             stateBudget
	sink               *issueSink
	funcs              []summarizableFunc
	calls              *callFacts
	skipDeadCode       bool
	skipBehaviorClones bool
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

func newLinter(pkg *LoadedPackage, opts Options) *linter {
	if opts.MaxStates <= 0 {
		opts.MaxStates = 32
	}

	return &linter{
		pkg:                pkg,
		index:              newPackageIndex(pkg),
		budget:             stateBudget(opts.MaxStates),
		sink:               newIssueSink(pkg.FSet),
		skipDeadCode:       opts.skipDeadCode,
		skipBehaviorClones: opts.skipBehaviorClones,
	}
}

func cloneReturnKinds(in map[int]returnKind) map[int]returnKind {
	if len(in) == 0 {
		return nil
	}

	out := make(map[int]returnKind, len(in))
	maps.Copy(out, in)

	return out
}

// run analyzes the package. external returns summaries of functions from
// other packages.
func (l *linter) run(external func(*types.Func) (callSummary, bool)) {
	q := &exprQuery{
		fset:          l.pkg.FSet,
		info:          l.pkg.TypesInfo,
		typesPkg:      l.pkg.TypesPkg,
		renderCache:   make(map[ast.Node]string),
		localFuncLits: collectLocalFuncLits(l.pkg.Files, l.pkg.TypesInfo),
	}
	explicit := collectContracts(l.pkg, l.sink)
	l.funcs = summarizableFuncs(l.pkg)
	l.calls = &callFacts{
		q:        q,
		budget:   l.budget,
		explicit: explicit,
		inferred: make(map[string]callSummary, len(l.funcs)),
		external: external,
	}

	// Summary walks repeat the main walk, so their diagnostics are dropped.
	l.inferCallSummaries(newPathWalker(q, l.calls, newIssueSink(l.pkg.FSet)))
	l.analyzeFiles(newPathWalker(q, l.calls, l.sink))
}

func (l *linter) analyzeFiles(walker *pathWalker) {
	structure := structurecheck.New(&structurecheck.Package{
		Files:     l.pkg.Files,
		FSet:      l.pkg.FSet,
		TypesPkg:  l.pkg.TypesPkg,
		TypesInfo: l.pkg.TypesInfo,
	})
	analyzeFunction := func(fnType *ast.FuncType, recv *ast.FieldList, body *ast.BlockStmt) {
		if hasUnsupportedJumps(body) {
			return
		}

		for _, finding := range structure.ScanFunctionBody(fnType, recv, body) {
			l.sink.report(finding.Pos, finding.Kind, finding.Message)
		}

		walker.execBlock(body.List, []state{newState()})
	}

	for _, file := range l.pkg.Files {
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

	l.scanPackageChecks()
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
