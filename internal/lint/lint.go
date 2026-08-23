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
	MaxStates    int
	CacheEnabled bool
	CacheDir     string
	CacheHitHook func(string)
	ClosedWorld  bool
	skipDeadCode bool
}

type linter struct {
	pkg             *LoadedPackage
	index           packageIndex
	maxStates       int
	issues          []Issue
	reported        map[string]struct{}
	suppressReports int
	renderCache     map[ast.Node]string
	explicitFacts   map[string][]guardContract
	inferredFacts   map[string]callSummary
	localFuncLits   map[types.Object]*ast.FuncLit
	externalSummary func(*types.Func) (callSummary, bool)
	skipDeadCode    bool
	structureRunner *structurecheck.Runner
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
		pkg:           pkg,
		index:         newPackageIndex(pkg),
		maxStates:     opts.MaxStates,
		reported:      make(map[string]struct{}),
		issues:        make([]Issue, 0),
		renderCache:   make(map[ast.Node]string),
		explicitFacts: make(map[string][]guardContract),
		inferredFacts: make(map[string]callSummary),
		skipDeadCode:  opts.skipDeadCode,
	}
}

func (ret returnState) kindAt(index int) returnKind {
	if ret.kinds == nil {
		return returnUnspecified
	}

	kind, ok := ret.kinds[index]
	if !ok {
		return returnUnspecified
	}

	return kind
}

func cloneReturnKinds(in map[int]returnKind) map[int]returnKind {
	if len(in) == 0 {
		return nil
	}

	out := make(map[int]returnKind, len(in))
	maps.Copy(out, in)

	return out
}

func (l *linter) run() {
	l.collectContracts()
	l.checkContractComments()
	l.collectLocalFuncLits()
	l.inferCallSummaries()
	l.analyzeFiles()
}

func (l *linter) analyzeFiles() {
	for _, file := range l.pkg.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			switch fn := n.(type) {
			case *ast.FuncDecl:
				if fn.Body != nil {
					l.analyzeFunction(fn.Type, fn.Recv, fn.Body)
				}

				return false
			case *ast.FuncLit:
				l.analyzeFunction(fn.Type, nil, fn.Body)
				return false
			default:
				return true
			}
		})
	}

	l.scanDefaultSmells()
	l.scanPackageSmells()
}

func (l *linter) analyzeFunction(
	fnType *ast.FuncType,
	recv *ast.FieldList,
	body *ast.BlockStmt,
) {
	if body == nil {
		return
	}

	if l.hasUnsupportedJumps(body) {
		return
	}

	l.scanStructuralFunction(fnType, recv, body)
	l.execBlock(body.List, []state{newState()})
}

func (l *linter) hasUnsupportedJumps(body *ast.BlockStmt) bool {
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
