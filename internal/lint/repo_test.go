package lint

import "sort"

// LintPackages runs repo-aware analysis across loaded packages.
func LintPackages(pkgs []*LoadedPackage, opts Options) []Issue {
	if len(pkgs) == 0 {
		return nil
	}

	explicitFacts, inferredFacts := inferRepoSummaries(pkgs, opts)

	sort.Slice(pkgs, func(i, j int) bool {
		return pkgs[i].ImportPath < pkgs[j].ImportPath
	})

	var issues []Issue

	for _, pkg := range pkgs {
		l := newLinter(pkg, opts)
		l.explicitFacts = explicitFacts
		l.inferredFacts = inferredFacts
		l.collectLocalFuncLits()
		l.analyzeFiles()
		sortIssues(pkg.FSet, l.issues)
		issues = append(issues, l.issues...)
	}

	return issues
}

func inferRepoSummaries(
	pkgs []*LoadedPackage,
	opts Options,
) (map[string][]guardContract, map[string]callSummary) {
	explicitFacts := make(map[string][]guardContract)
	summaries := make(map[string]callSummary)
	funcs := make([]repoSummarizableFunc, 0)

	sort.Slice(pkgs, func(i, j int) bool {
		return pkgs[i].ImportPath < pkgs[j].ImportPath
	})

	for _, pkg := range pkgs {
		l := newLinter(pkg, opts)
		l.explicitFacts = explicitFacts
		l.collectLocalFuncLits()

		l.collectContracts()
	}

	for _, pkg := range pkgs {
		l := newLinter(pkg, opts)
		l.explicitFacts = explicitFacts
		l.collectLocalFuncLits()

		for _, fn := range l.collectSummarizableFuncs() {
			funcs = append(funcs, repoSummarizableFunc{
				pkg: pkg,
				fn:  fn,
			})
		}
	}

	maxPasses := len(funcs) + 1
	for range maxPasses {
		changed := false

		for _, item := range funcs {
			l := newLinter(item.pkg, opts)
			l.explicitFacts = explicitFacts
			l.inferredFacts = summaries
			l.collectLocalFuncLits()

			summary := l.summarizeFunc(item.fn)

			prev := summaries[item.fn.key]
			if callSummaryEqual(prev, summary) {
				continue
			}

			summaries[item.fn.key] = summary
			changed = true
		}

		if !changed {
			break
		}
	}

	return explicitFacts, summaries
}

type repoSummarizableFunc struct {
	pkg *LoadedPackage
	fn  summarizableFunc
}
