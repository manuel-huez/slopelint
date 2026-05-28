package smells

import (
	"go/ast"
	"go/token"
	"go/types"
)

const (
	boolTrueText              = "true"
	boolFalseText             = "false"
	lenPathSegment            = "#len"
	nilText                   = "nil"
	panicText                 = "panic"
	predicatePathSegmentPrefx = "#pred:"
	unknownPos                = "unknown position"
)

// Finding is one smell diagnostic emitted by this package.
type Finding struct {
	Pos     token.Pos
	Kind    string
	Message string
}

// Package carries parsed package data shared by all smell checks.
type Package struct {
	Files           []*ast.File
	ProductionFiles []*ast.File
	TestFiles       []*ast.File
	ProductionDecls []ast.Decl
	ProductionFuncs []*ast.FuncDecl
	ProductionTypes []*ast.TypeSpec
	FSet            *token.FileSet
	TypesPkg        *types.Package
	TypesInfo       *types.Info
}

type Runner struct {
	pkg            *Package
	findings       []Finding
	reported       map[string]struct{}
	renderCache    map[ast.Node]string
	callCountsProd map[string]int
}

// RunDefault runs smell checks enabled by default.
func RunDefault(pkg *Package) []Finding {
	r := newRunner(pkg)
	r.checkTrivialForwarders()
	r.checkRepeatedNormalizationCallsPackage()
	r.checkRedundantJSONMarshalText()
	r.checkRestatementComments()
	r.checkPredicateReturnSignatures()
	r.checkDeclarationGrouping()
	r.checkUnnamedLargeTableTests()

	return r.findings
}

// RunPackage runs package-wide smell checks enabled by --package.
func RunPackage(pkg *Package) []Finding {
	r := newRunner(pkg)
	r.checkDuplicateValidationLadders()
	r.checkSingleUsePrivateHelpers()
	r.checkSingleImplInterfaces()
	r.checkOptionsOverkill()
	r.checkInternalResultWrappers()
	r.checkTestGlobalFuncStubs()
	r.checkBoolModeParams()
	r.checkZeroValuePrivateArgs()
	r.checkOptionalResultTriples()
	r.checkProductionErrorPanics()
	r.checkSentinelErrorBreaks()

	return r.findings
}
