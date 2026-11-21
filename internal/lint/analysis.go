package lint

import (
	"errors"
	"path/filepath"

	"go/types"

	"golang.org/x/tools/go/analysis"
)

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
	l.run(func(obj *types.Func) (callSummary, bool) {
		fact := new(callSummaryFact)
		if !pass.ImportObjectFact(obj.Origin(), fact) {
			return callSummary{}, false
		}

		return callSummaryFromFact(fact), true
	})

	for _, fn := range l.funcs {
		pass.ExportObjectFact(fn.obj.Origin(), callSummaryFactFromSummary(l.calls.exported(fn)))
	}

	sortIssues(l.sink.issues)

	if cache != nil {
		// Cache persistence is best-effort; analysis results remain valid without it.
		_ = cache.store(pass, l, l.sink.issues)

		maybePruneCaches(opts.cacheDir)
	}

	// Vettool mode has no code review, so dropped-error candidates never report.
	issues, _ := splitCodeReviewCandidates(l.sink.issues)

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

func callSummaryFactFromSummary(summary callSummary) *callSummaryFact {
	fact := &callSummaryFact{
		Always:         contractsToFacts(summary.always),
		ParallelParams: summary.parallelParams,
	}

	for _, index := range sortedSummaryResultIndices(summary.results) {
		result := summary.results[index]
		fact.Results = append(fact.Results, resultSummaryFact{
			Index:      index,
			WhenTrue:   contractsToFacts(result.whenTrue),
			WhenFalse:  contractsToFacts(result.whenFalse),
			WhenNil:    contractsToFacts(result.whenNil),
			WhenNonNil: contractsToFacts(result.whenNonNil),
		})
	}

	return fact
}

func callSummaryFromFact(fact *callSummaryFact) callSummary {
	summary := callSummary{
		always:         contractsFromFacts(fact.Always),
		parallelParams: fact.ParallelParams,
	}

	for _, result := range fact.Results {
		if summary.results == nil {
			summary.results = make(map[int]resultSummary, len(fact.Results))
		}

		summary.results[result.Index] = normalizeResultSummary(resultSummary{
			whenTrue:   contractsFromFacts(result.WhenTrue),
			whenFalse:  contractsFromFacts(result.WhenFalse),
			whenNil:    contractsFromFacts(result.WhenNil),
			whenNonNil: contractsFromFacts(result.WhenNonNil),
		})
	}

	return normalizeCallSummary(summary)
}

func contractsToFacts(contracts []guardContract) []guardContractFact {
	if len(contracts) == 0 {
		return nil
	}

	out := make([]guardContractFact, 0, len(contracts))
	for _, contract := range contracts {
		out = append(out, guardContractFact{
			Param:    contract.target.param,
			Recv:     contract.target.recv,
			Variadic: contract.target.variadic,
			Path:     append([]string(nil), contract.target.path...),
			Kind:     int(contract.value.kind),
			Text:     contract.value.text,
			WantEq:   contract.wantEq,
		})
	}

	return out
}

func contractsFromFacts(contracts []guardContractFact) []guardContract {
	if len(contracts) == 0 {
		return nil
	}

	out := make([]guardContract, 0, len(contracts))
	for _, contract := range contracts {
		out = append(out, guardContract{
			target: contractTarget{
				param:    contract.Param,
				recv:     contract.Recv,
				variadic: contract.Variadic,
				path:     append([]string(nil), contract.Path...),
			},
			value: scalar{
				kind: scalarKind(contract.Kind),
				text: contract.Text,
			},
			wantEq: contract.WantEq,
		})
	}

	return normalizeGuardContracts(out)
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
