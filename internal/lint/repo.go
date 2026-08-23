package lint

import (
	"sort"

	deadcodecheck "github.com/manuel-huez/slopelint/internal/lint/deadcode"
)

// LintRepository resolves patterns, replays valid cached analysis before type-checking,
// and runs semantic similarity when requested.
func LintRepository(
	patterns []string,
	dir string,
	opts Options,
	similarity *SimilarityOptions,
) ([]Issue, error) {
	cache, cacheErr := newRepoAnalysisCache(patterns, dir, opts, similarity)
	if cacheErr == nil {
		if entry, ok := cache.load(); ok {
			if issues, valid := replayRepoAnalysisCache(entry, opts.CacheHitHook); valid {
				return issues, nil
			}
		}
	}

	targets, byImportPath, err := resolvePackageMetadata(patterns, dir)
	if err != nil {
		return nil, err
	}

	pkgs, err := loadPackageTargets(targets, byImportPath)
	if err != nil {
		return nil, err
	}

	issues := lintLoadedPackages(pkgs, opts)

	issues, err = appendSimilarityIssues(issues, pkgs, similarity)
	if err != nil {
		return nil, err
	}

	// Similarity can update its committed stamp. Recompute the fast source key so
	// the stored result describes the state that the next command will observe.
	if freshCache, cacheErr := newRepoAnalysisCache(
		patterns,
		dir,
		opts,
		similarity,
	); cacheErr == nil {
		_ = freshCache.store(issues)
	}

	return issues, nil
}

func appendSimilarityIssues(
	issues []Issue,
	pkgs []*LoadedPackage,
	opts *SimilarityOptions,
) ([]Issue, error) {
	if opts == nil {
		return issues, nil
	}

	similarityIssues, err := CheckSimilarCode(pkgs, *opts)
	if err != nil {
		return nil, err
	}

	issues = append(issues, similarityIssues...)
	sortIssues(issues)

	return issues, nil
}

func lintLoadedPackages(pkgs []*LoadedPackage, opts Options) []Issue {
	if len(pkgs) == 0 {
		return nil
	}

	pkgs = append([]*LoadedPackage(nil), pkgs...)

	explicitFacts, inferredFacts := inferRepoSummaries(pkgs, opts)
	repoDeadCode := opts.ClosedWorld && hasMainPackage(pkgs)
	repoOpts := opts
	repoOpts.skipDeadCode = repoDeadCode

	sort.Slice(pkgs, func(i, j int) bool {
		return pkgs[i].ImportPath < pkgs[j].ImportPath
	})

	var (
		deadPkgs = make([]*deadcodecheck.Package, 0, len(pkgs))
		issues   []Issue
	)

	for _, pkg := range pkgs {
		l := newLinter(pkg, repoOpts)
		l.explicitFacts = explicitFacts
		l.inferredFacts = inferredFacts
		l.checkContractComments()
		l.collectLocalFuncLits()
		l.analyzeFiles()
		sortIssues(l.issues)
		issues = append(issues, l.issues...)

		if repoDeadCode {
			deadPkgs = append(deadPkgs, l.deadCodePackage())
		}
	}

	if repoDeadCode {
		issues = append(issues, repoDeadCodeIssues(deadPkgs)...)
	}

	sortIssues(issues)

	return issues
}

func hasMainPackage(pkgs []*LoadedPackage) bool {
	for _, pkg := range pkgs {
		if pkg != nil && pkg.Name == mainPkgName {
			return true
		}
	}

	return false
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

		for _, fn := range l.collectSummarizableFuncs() {
			funcs = append(funcs, repoSummarizableFunc{
				l:  l,
				fn: fn,
			})
		}
	}

	maxPasses := len(funcs) + 1
	for range maxPasses {
		changed := false

		for _, item := range funcs {
			item.l.inferredFacts = summaries

			summary := item.l.summarizeFunc(item.fn)

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
	l  *linter
	fn summarizableFunc
}
