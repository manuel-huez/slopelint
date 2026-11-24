package lint

import (
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	deadcodecheck "github.com/manuel-huez/slopelint/internal/lint/deadcode"
	flowcheck "github.com/manuel-huez/slopelint/internal/lint/flow"
	smellcheck "github.com/manuel-huez/slopelint/internal/lint/smells"
	structurecheck "github.com/manuel-huez/slopelint/internal/lint/structure"
	"golang.org/x/tools/go/analysis"
)

const (
	allPackagesPattern = "./..."
	boolFalseText      = "false"
	zeroIntText        = "0"
	offText            = "off"
	mainPkgName        = "main"
	unknownPos         = "unknown position"
	defaultMaxStates   = 32
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
// structure and dead-code sub-packages. run fills exports, which cache and
// fact export read afterwards.
type linter struct {
	pkg                *LoadedPackage
	index              packageIndex
	maxStates          int
	sink               *issueSink
	exports            []flowcheck.Export
	external           func(*types.Func) (flowcheck.CallSummary, bool)
	skipDeadCode       bool
	skipBehaviorClones bool
}

func newLinter(pkg *LoadedPackage, opts Options) *linter {
	if opts.MaxStates <= 0 {
		opts.MaxStates = defaultMaxStates
	}

	return &linter{
		pkg:                pkg,
		index:              newPackageIndex(pkg),
		maxStates:          opts.MaxStates,
		sink:               newIssueSink(pkg.FSet),
		skipDeadCode:       opts.skipDeadCode,
		skipBehaviorClones: opts.skipBehaviorClones,
	}
}

// run analyzes the package. external returns summaries of functions from
// other packages.
func (l *linter) run(external func(*types.Func) (flowcheck.CallSummary, bool)) {
	l.external = external

	structure := structurecheck.New(&structurecheck.Package{
		Files:     l.pkg.Files,
		FSet:      l.pkg.FSet,
		TypesPkg:  l.pkg.TypesPkg,
		TypesInfo: l.pkg.TypesInfo,
	})

	l.exports = flowcheck.Run(l.flowPackage(), flowcheck.Options{
		MaxStates: l.maxStates,
		External:  external,
		Report:    l.sink.report,
		OnFunc: func(fnType *ast.FuncType, recv *ast.FieldList, body *ast.BlockStmt) {
			for _, finding := range structure.ScanFunctionBody(fnType, recv, body) {
				l.sink.report(finding.Pos, finding.Kind, finding.Message)
			}
		},
	})

	l.scanPackageChecks()
}

func (l *linter) flowPackage() *flowcheck.Package {
	return &flowcheck.Package{
		Files:     l.pkg.Files,
		FSet:      l.pkg.FSet,
		TypesPkg:  l.pkg.TypesPkg,
		TypesInfo: l.pkg.TypesInfo,
	}
}

func (l *linter) issues() []Issue {
	return l.sink.list()
}

func (l *linter) sortedIssues() []Issue {
	issues := l.sink.list()
	sortIssues(issues)

	return issues
}

func (l *linter) forEachExport(fn func(flowcheck.Export)) {
	for _, export := range l.exports {
		fn(export)
	}
}

// LoadedPackage contains all information the linter needs for one checked package.
type LoadedPackage struct {
	ImportPath string
	Name       string
	Dir        string
	FSet       *token.FileSet
	Files      []*ast.File
	TypesPkg   *types.Package
	TypesInfo  *types.Info
	repoFiles  []string
	testOnly   bool
	// importMap maps source imports to the go list test variants they load.
	importMap map[string]string
	// trimmedDirs maps -trimpath export-data directories to package dirs.
	trimmedDirs map[string]string
}

func repoDeadCodeIssues(pkgs []*deadcodecheck.Package) []Issue {
	findings := deadcodecheck.Repo(pkgs)
	issues := make([]Issue, 0, len(findings))

	for _, finding := range findings {
		issues = append(issues, Issue{
			Pos:     finding.Pos,
			Kind:    finding.Kind,
			Message: finding.Message,
			fset:    finding.FSet,
		})
	}

	return issues
}

func (l *linter) deadCodePackage() *deadcodecheck.Package {
	generated := make(map[ast.Decl]struct{})

	for _, file := range l.index.productionFiles {
		if ast.IsGenerated(file) {
			for _, decl := range file.Decls {
				generated[decl] = struct{}{}
			}
		}
	}

	return &deadcodecheck.Package{
		ImportPath:      l.pkg.ImportPath,
		Name:            l.pkg.Name,
		FSet:            l.pkg.FSet,
		TypesPkg:        l.pkg.TypesPkg,
		TypesInfo:       l.pkg.TypesInfo,
		ProductionDecls: l.index.productionDecls,
		ProductionFuncs: l.index.productionFuncs,
		GeneratedDecls:  generated,
		TrimmedDirs:     l.pkg.trimmedDirs,
	}
}

// scanPackageChecks runs the package-wide smell and dead-code checks after the
// per-function walk, so their findings follow path findings in report order.
func (l *linter) scanPackageChecks() {
	pkg := l.smellsPackage()
	l.addSmellFindings(smellcheck.RunDefault(pkg))

	if !l.skipBehaviorClones {
		l.addSmellFindings(smellcheck.RunBehaviorPackage(pkg))
	}

	l.addSmellFindings(smellcheck.RunPackage(pkg))

	if !l.skipDeadCode {
		for _, finding := range deadcodecheck.Private(l.deadCodePackage()) {
			l.sink.report(finding.Pos, finding.Kind, finding.Message)
		}
	}
}

func (l *linter) smellsPackage() *smellcheck.Package {
	return &smellcheck.Package{
		Files:                  l.index.files,
		ProductionFiles:        l.index.productionFiles,
		TestFiles:              l.index.testFiles,
		TestSupportFiles:       l.index.testSupportFiles,
		ProductionDecls:        l.index.productionDecls,
		ProductionFuncs:        l.index.productionFuncs,
		ProductionTypes:        l.index.productionTypes,
		FSet:                   l.pkg.FSet,
		TypesPkg:               l.pkg.TypesPkg,
		TypesInfo:              l.pkg.TypesInfo,
		ImportedParallelParams: l.importedParallelParams,
	}
}

func (l *linter) importedParallelParams(fn *types.Func) []int {
	if l.external == nil {
		return nil
	}

	summary, _ := l.external(fn)

	return summary.ParallelParams
}

func (l *linter) addSmellFindings(findings []smellcheck.Finding) {
	for _, finding := range findings {
		l.sink.report(finding.Pos, finding.Kind, finding.Message)
	}
}

// issueSink collects issues in report order and drops exact repeats.
type issueSink struct {
	fset     *token.FileSet
	issues   []Issue
	reported map[string]struct{}
}

func newIssueSink(fset *token.FileSet) *issueSink {
	return &issueSink{fset: fset, issues: make([]Issue, 0), reported: make(map[string]struct{})}
}

func (s *issueSink) list() []Issue {
	return s.issues
}

func (s *issueSink) report(pos token.Pos, kind, msg string) {
	p := s.fset.Position(pos)

	key := fmt.Sprintf("%s:%d:%d:%s:%s", p.Filename, p.Line, p.Column, kind, msg)
	if _, dup := s.reported[key]; dup {
		return
	}

	s.reported[key] = struct{}{}
	s.issues = append(s.issues, Issue{
		Pos:     pos,
		Kind:    kind,
		Message: msg,
		fset:    s.fset,
	})
}

// Issue is one linter finding.
type Issue struct {
	Pos      token.Pos
	Kind     string
	Message  string
	fset     *token.FileSet
	position token.Position
}

func sortIssues(issues []Issue) {
	sort.Slice(issues, func(i, j int) bool {
		pi := issuePosition(issues[i])
		pj := issuePosition(issues[j])

		if pi.Filename != pj.Filename {
			return pi.Filename < pj.Filename
		}

		if pi.Line != pj.Line {
			return pi.Line < pj.Line
		}

		if pi.Column != pj.Column {
			return pi.Column < pj.Column
		}

		return issues[i].Message < issues[j].Message
	})
}

func issuePosition(issue Issue) token.Position {
	if issue.position.IsValid() {
		return issue.position
	}

	if issue.fset == nil {
		return token.Position{}
	}

	return issue.fset.Position(issue.Pos)
}

// issueFilePosition ignores //line directives, so it matches positions read
// from the file itself.
func issueFilePosition(issue Issue) token.Position {
	if issue.fset != nil {
		return issue.fset.PositionFor(issue.Pos, false)
	}

	return issuePosition(issue)
}

// FormatIssuePosition returns a stable source position for an issue.
func FormatIssuePosition(issue Issue) string {
	position := issuePosition(issue)
	if !position.IsValid() {
		return unknownPos
	}

	return position.String()
}

type packageIndex struct {
	files            []*ast.File
	productionFiles  []*ast.File
	testFiles        []*ast.File
	testSupportFiles []*ast.File
	productionDecls  []ast.Decl
	productionFuncs  []*ast.FuncDecl
	productionTypes  []*ast.TypeSpec
}

func newPackageIndex(pkg *LoadedPackage) packageIndex {
	if pkg == nil {
		return packageIndex{}
	}

	index := packageIndex{
		files:           make([]*ast.File, 0, len(pkg.Files)),
		productionFiles: make([]*ast.File, 0, len(pkg.Files)),
		testFiles:       make([]*ast.File, 0),
	}

	for _, file := range pkg.Files {
		if file == nil {
			continue
		}

		index.files = append(index.files, file)
		if packageFileIsTest(pkg.FSet, file) {
			index.testFiles = append(index.testFiles, file)
			continue
		}

		if pkg.testOnly {
			index.testSupportFiles = append(index.testSupportFiles, file)
			continue
		}

		index.productionFiles = append(index.productionFiles, file)
		for _, decl := range file.Decls {
			index.productionDecls = append(index.productionDecls, decl)

			switch decl := decl.(type) {
			case *ast.FuncDecl:
				index.productionFuncs = append(index.productionFuncs, decl)
			case *ast.GenDecl:
				if decl.Tok == token.TYPE {
					for _, spec := range decl.Specs {
						typeSpec, ok := spec.(*ast.TypeSpec)
						if ok {
							index.productionTypes = append(index.productionTypes, typeSpec)
						}
					}
				}
			}
		}
	}

	return index
}

func classifyTestOnlyPackages(
	targets []*packageMeta,
	byImportPath map[string]*packageMeta,
	patterns []string,
	absoluteDir string,
) {
	// Reverse imports are complete only for a module-root ./... scan. Public
	// packages remain production because consumers can exist outside this module.
	if !slices.Contains(patterns, allPackagesPattern) {
		return
	}

	bases := moduleRootPackages(targets, byImportPath, absoluteDir)

	var testRoots []string

	for _, base := range bases {
		for _, path := range slices.Concat(base.TestImports, base.XTestImports) {
			// A package's own external tests do not make its API test support.
			if path != base.ImportPath {
				testRoots = append(testRoots, path)
			}
		}
	}

	testReachable := packageImportClosure(bases, testRoots)

	var productionRoots []string

	for path, base := range bases {
		relativePath, withinModule := strings.CutPrefix(path, base.Module.Path+"/")

		internalLibrary := withinModule && base.Name != "main" &&
			strings.Contains("/"+relativePath+"/", "/internal/")
		if !internalLibrary || !testReachable[path] {
			productionRoots = append(productionRoots, path)
		}
	}

	productionReachable := packageImportClosure(bases, productionRoots)

	for _, target := range targets {
		path := target.targetImportPath()
		target.testOnly = testReachable[path] && !productionReachable[path]
	}
}

func moduleRootPackages(
	targets []*packageMeta,
	byImportPath map[string]*packageMeta,
	absoluteDir string,
) map[string]*packageMeta {
	bases := make(map[string]*packageMeta)

	for _, target := range targets {
		base := byImportPath[target.targetImportPath()]
		if base == nil || base.Module == nil || !base.Module.Main ||
			filepath.Clean(base.Module.Dir) != absoluteDir {
			continue
		}

		bases[base.ImportPath] = base
	}

	return bases
}

func packageImportClosure(packages map[string]*packageMeta, roots []string) map[string]bool {
	reachable := make(map[string]bool)

	for len(roots) > 0 {
		path := roots[len(roots)-1]
		roots = roots[:len(roots)-1]

		pkg := packages[path]
		if pkg == nil || reachable[path] {
			continue
		}

		reachable[path] = true

		roots = append(roots, pkg.Imports...)
	}

	return reachable
}

func packageFileIsTest(fset *token.FileSet, file *ast.File) bool {
	if file == nil {
		return false
	}

	posFile := fset.File(file.Pos())
	if posFile == nil {
		return false
	}

	return strings.HasSuffix(posFile.Name(), "_test.go")
}

type guardContractFact struct {
	Param    int
	Recv     bool
	Variadic bool
	Path     []string
	Kind     int
	Text     string
	WantEq   bool
}

type resultSummaryFact struct {
	Index      int
	WhenTrue   []guardContractFact
	WhenFalse  []guardContractFact
	WhenNil    []guardContractFact
	WhenNonNil []guardContractFact
}

type callSummaryFact struct {
	Always         []guardContractFact
	Results        []resultSummaryFact
	ParallelParams []int
}

func (*callSummaryFact) AFact() {}

func AnalysisFactTypes() []analysis.Fact {
	return []analysis.Fact{new(callSummaryFact)}
}

func RunAnalysis(pass *analysis.Pass, opts Options) ([]Issue, error) {
	pkg := &LoadedPackage{
		ImportPath: pass.Pkg.Path(),
		Name:       pass.Pkg.Name(),
		Dir:        analysisPackageDir(pass),
		FSet:       pass.Fset,
		Files:      pass.Files,
		TypesPkg:   pass.Pkg,
		TypesInfo:  pass.TypesInfo,
	}

	cache, err := analysisCacheForSourceRoot(pkg.Dir, opts, "", func() (string, error) {
		return analysisCacheKey(pass, pkg, opts)
	})
	if err == nil {
		if entry, ok := cache.load(); ok {
			if issues, ok := replayAnalysisCache(pass, pkg, entry, opts.CacheHitHook); ok {
				sortIssues(issues)
				issues, _ = splitCodeReviewCandidates(issues)

				return analysisIgnores(pass).filter(issues), nil
			}
		}
	} else if !errors.Is(err, errAnalysisCacheDisabled) {
		cache = nil
	}

	l := newLinter(pkg, opts)
	l.run(func(obj *types.Func) (flowcheck.CallSummary, bool) {
		fact := new(callSummaryFact)
		if !pass.ImportObjectFact(obj.Origin(), fact) {
			return flowcheck.CallSummary{}, false
		}

		return callSummaryFromFact(fact), true
	})

	l.forEachExport(func(export flowcheck.Export) {
		pass.ExportObjectFact(export.Obj.Origin(), callSummaryFactFromSummary(export.Summary))
	})

	allIssues := l.sortedIssues()

	if cache != nil {
		// Cache persistence is best-effort; analysis results remain valid without it.
		_ = cache.store(pass, l, allIssues)

		maybePruneCaches(opts.cacheDir)
	}

	// Vettool mode has no code review, so dropped-error candidates never report.
	issues, _ := splitCodeReviewCandidates(allIssues)

	return analysisIgnores(pass).filter(issues), nil
}

// analysisIgnores reads the package's directives. One package is never the
// whole scan, so unused directives are not reported here.
func analysisIgnores(pass *analysis.Pass) *ignoreSet {
	files := make([]string, len(pass.Files))
	for index, file := range pass.Files {
		files[index] = pass.Fset.File(file.Pos()).Name()
	}

	return loadIgnores(files)
}

func callSummaryFactFromSummary(summary flowcheck.CallSummary) *callSummaryFact {
	fact := &callSummaryFact{
		Always:         contractsToFacts(summary.Always),
		ParallelParams: summary.ParallelParams,
	}

	for _, index := range flowcheck.SortedSummaryResultIndices(summary.Results) {
		result := summary.Results[index]
		fact.Results = append(fact.Results, resultSummaryFact{
			Index:      index,
			WhenTrue:   contractsToFacts(result.WhenTrue),
			WhenFalse:  contractsToFacts(result.WhenFalse),
			WhenNil:    contractsToFacts(result.WhenNil),
			WhenNonNil: contractsToFacts(result.WhenNonNil),
		})
	}

	return fact
}

func callSummaryFromFact(fact *callSummaryFact) flowcheck.CallSummary {
	summary := flowcheck.CallSummary{
		Always:         contractsFromFacts(fact.Always),
		ParallelParams: fact.ParallelParams,
	}

	for _, result := range fact.Results {
		if summary.Results == nil {
			summary.Results = make(map[int]flowcheck.ResultSummary, len(fact.Results))
		}

		summary.Results[result.Index] = flowcheck.NormalizeResultSummary(flowcheck.ResultSummary{
			WhenTrue:   contractsFromFacts(result.WhenTrue),
			WhenFalse:  contractsFromFacts(result.WhenFalse),
			WhenNil:    contractsFromFacts(result.WhenNil),
			WhenNonNil: contractsFromFacts(result.WhenNonNil),
		})
	}

	return flowcheck.NormalizeCallSummary(summary)
}

func contractsToFacts(contracts []flowcheck.GuardContract) []guardContractFact {
	if len(contracts) == 0 {
		return nil
	}

	out := make([]guardContractFact, 0, len(contracts))
	for _, contract := range contracts {
		out = append(out, guardContractFact{
			Param:    contract.Target.Param,
			Recv:     contract.Target.Recv,
			Variadic: contract.Target.Variadic,
			Path:     append([]string(nil), contract.Target.Path...),
			Kind:     int(contract.Value.Kind),
			Text:     contract.Value.Text,
			WantEq:   contract.WantEq,
		})
	}

	return out
}

func contractsFromFacts(contracts []guardContractFact) []flowcheck.GuardContract {
	if len(contracts) == 0 {
		return nil
	}

	out := make([]flowcheck.GuardContract, 0, len(contracts))
	for _, contract := range contracts {
		out = append(out, flowcheck.GuardContract{
			Target: flowcheck.ContractTarget{
				Param:    contract.Param,
				Recv:     contract.Recv,
				Variadic: contract.Variadic,
				Path:     append([]string(nil), contract.Path...),
			},
			Value: flowcheck.Scalar{
				Kind: flowcheck.ScalarKind(contract.Kind),
				Text: contract.Text,
			},
			WantEq: contract.WantEq,
		})
	}

	return flowcheck.NormalizeGuardContracts(out)
}

func analysisPackageDir(pass *analysis.Pass) string {
	for _, file := range pass.Files {
		pos := pass.Fset.Position(file.Package)
		if pos.Filename != "" {
			return filepath.Dir(pos.Filename)
		}
	}

	return ""
}
