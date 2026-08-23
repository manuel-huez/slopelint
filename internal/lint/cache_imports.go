package lint

import (
	"go/types"
	"sort"

	"golang.org/x/tools/go/analysis"
)

func analysisCacheImportedFacts(
	pass *analysis.Pass,
	pkg *LoadedPackage,
) []analysisCacheImportedFact {
	if pass.AllObjectFacts == nil {
		return nil
	}

	facts := pass.AllObjectFacts()
	if len(facts) == 0 {
		return nil
	}

	out := make([]analysisCacheImportedFact, 0, len(facts))

	for _, objectFact := range facts {
		fn, ok := objectFact.Object.(*types.Func)
		if !ok || fn == nil {
			continue
		}

		fact, ok := objectFact.Fact.(*callSummaryFact)
		if !ok || fact == nil {
			continue
		}

		if fn.Pkg() != nil && fn.Pkg().Path() == pkg.ImportPath {
			continue
		}

		out = append(out, analysisCacheImportedFact{
			FuncKey: funcObjectKey(fn),
			Present: true,
			Fact:    *cloneCallSummaryFact(fact),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].FuncKey < out[j].FuncKey
	})

	return out
}
