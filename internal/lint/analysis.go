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
	Always  []guardContractFact
	Results []resultSummaryFact
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

	cache, err := newAnalysisCache(pass, pkg, opts)
	if err == nil {
		if entry, ok := cache.load(); ok {
			if issues, ok := replayAnalysisCache(pass, pkg, entry, opts.CacheHitHook); ok {
				sortIssues(issues)

				return issues, nil
			}
		}
	} else if !errors.Is(err, errAnalysisCacheDisabled) {
		cache = nil
	}

	l := newLinter(pkg, opts)
	l.externalSummary = func(obj *types.Func) (callSummary, bool) {
		fact := new(callSummaryFact)
		if !pass.ImportObjectFact(obj.Origin(), fact) {
			return callSummary{}, false
		}

		return callSummaryFromFact(fact), true
	}

	l.run()
	l.exportAnalysisFacts(pass)
	sortIssues(l.issues)

	if cache != nil {
		// Cache persistence is best-effort; analysis results remain valid without it.
		_ = cache.store(pass, l, l.issues)
	}

	return l.issues, nil
}

func (l *linter) exportAnalysisFacts(pass *analysis.Pass) {
	for _, fn := range l.collectSummarizableFuncs() {
		obj, ok := l.pkg.TypesInfo.ObjectOf(fn.decl.Name).(*types.Func)
		if !ok || obj == nil {
			continue
		}

		summary := l.summaryWithExplicit(fn.key, l.inferredFacts[fn.key])
		pass.ExportObjectFact(obj.Origin(), callSummaryFactFromSummary(summary))
	}
}

func callSummaryFactFromSummary(summary callSummary) *callSummaryFact {
	fact := &callSummaryFact{
		Always: contractsToFacts(summary.always),
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
		always: contractsFromFacts(fact.Always),
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
