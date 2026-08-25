package lint

import (
	"go/types"
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
			if issues, valid := replayRepoAnalysisCache(
				entry,
				opts.CacheHitHook,
				cache.sourceRoot,
			); valid {
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

	repoDeadCode := opts.ClosedWorld && hasMainPackage(pkgs)
	repoOpts := opts
	repoOpts.skipDeadCode = repoDeadCode

	sort.Slice(pkgs, func(i, j int) bool {
		return pkgs[i].ImportPath < pkgs[j].ImportPath
	})

	summaries := make(map[string]callSummary)
	results := make(map[string]repoPackageLintResult, len(pkgs))
	typeDigests := analysisCacheTypeDigests(pkgs)

	for _, level := range repoPackageDependencyLevels(pkgs) {
		current := lintRepoPackageLevel(
			level,
			summaries,
			repoOpts,
			typeDigests,
			repoDeadCode,
		)

		for _, result := range current {
			for _, export := range result.exports {
				summaries[export.FuncKey] = callSummaryFromFact(&export.Fact)
			}

			results[result.pkg.ImportPath] = result
		}
	}

	var (
		issues   []Issue
		deadPkgs = make([]*deadcodecheck.Package, 0, len(pkgs))
	)

	for _, pkg := range pkgs {
		result := results[pkg.ImportPath]
		issues = append(issues, result.issues...)

		if repoDeadCode {
			deadPkgs = append(deadPkgs, result.deadCode)
		}
	}

	if repoDeadCode {
		issues = append(issues, repoDeadCodeIssues(deadPkgs)...)
	}

	sortIssues(issues)

	return issues
}

func lintRepoPackageLevel(
	pkgs []*LoadedPackage,
	summaries map[string]callSummary,
	opts Options,
	typeDigests map[string]string,
	repoDeadCode bool,
) []repoPackageLintResult {
	results := make([]repoPackageLintResult, len(pkgs))

	_ = runPackageJobs(len(pkgs), func(index int) error {
		results[index] = lintRepoPackage(
			pkgs[index],
			summaries,
			opts,
			typeDigests,
			repoDeadCode,
		)

		return nil
	})

	return results
}

func lintRepoPackage(
	pkg *LoadedPackage,
	summaries map[string]callSummary,
	opts Options,
	typeDigests map[string]string,
	repoDeadCode bool,
) repoPackageLintResult {
	cache, _ := analysisCacheForPackage(pkg, opts, "packages", func() (string, error) {
		return standaloneAnalysisCacheKey(pkg, opts, typeDigests)
	})
	if cached, ok := cachedRepoPackageLintResult(
		cache,
		pkg,
		summaries,
		opts,
		repoDeadCode,
	); ok {
		return cached
	}

	l := newLinter(pkg, opts)
	dependencies := make(map[string]analysisCacheImportedFact)
	l.externalSummary = func(obj *types.Func) (callSummary, bool) {
		if obj.Pkg() == nil || obj.Pkg().Path() == pkg.ImportPath {
			return callSummary{}, false
		}

		key := funcObjectKey(obj)
		summary, ok := summaries[key]

		dependency := analysisCacheImportedFact{FuncKey: key, Present: ok}
		if ok {
			dependency.Fact = *callSummaryFactFromSummary(summary)
		}

		dependencies[key] = dependency

		return summary, ok
	}

	l.run()
	sortIssues(l.issues)

	result := repoPackageLintResult{
		pkg:     pkg,
		issues:  l.issues,
		exports: cachedExportsForLinter(l),
	}
	if cache != nil {
		_ = cache.storeStandalone(
			l,
			l.issues,
			sortedAnalysisCacheDependencies(dependencies),
		)
	}

	if repoDeadCode {
		result.deadCode = l.deadCodePackage()
	}

	return result
}

func cachedRepoPackageLintResult(
	cache *analysisCache,
	pkg *LoadedPackage,
	summaries map[string]callSummary,
	opts Options,
	repoDeadCode bool,
) (repoPackageLintResult, bool) {
	if cache == nil {
		return repoPackageLintResult{}, false
	}

	entry, ok := cache.load()
	if !ok {
		return repoPackageLintResult{}, false
	}

	issues, exports, ok := replayStandaloneAnalysisCache(
		entry,
		summaries,
		opts.CacheHitHook,
		pkg,
	)
	if !ok {
		return repoPackageLintResult{}, false
	}

	result := repoPackageLintResult{pkg: pkg, issues: issues, exports: exports}
	if repoDeadCode {
		result.deadCode = newLinter(pkg, opts).deadCodePackage()
	}

	return result, true
}

func sortedAnalysisCacheDependencies(
	dependencies map[string]analysisCacheImportedFact,
) []analysisCacheImportedFact {
	keys := make([]string, 0, len(dependencies))
	for key := range dependencies {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	ordered := make([]analysisCacheImportedFact, 0, len(keys))
	for _, key := range keys {
		dependency := dependencies[key]
		dependency.Fact = *cloneCallSummaryFact(&dependency.Fact)
		ordered = append(ordered, dependency)
	}

	return ordered
}

type repoPackageLintResult struct {
	pkg      *LoadedPackage
	issues   []Issue
	exports  []analysisCacheExport
	deadCode *deadcodecheck.Package
}

func hasMainPackage(pkgs []*LoadedPackage) bool {
	for _, pkg := range pkgs {
		if pkg != nil && pkg.Name == mainPkgName {
			return true
		}
	}

	return false
}

func repoPackageDependencyLevels(pkgs []*LoadedPackage) [][]*LoadedPackage {
	byPath := make(map[string]*LoadedPackage, len(pkgs))
	for _, pkg := range pkgs {
		byPath[pkg.ImportPath] = pkg
	}

	levels := make(map[string]int, len(pkgs))
	visiting := make(map[string]struct{}, len(pkgs))

	var assignLevel func(*LoadedPackage) int

	assignLevel = func(pkg *LoadedPackage) int {
		if level, ok := levels[pkg.ImportPath]; ok {
			return level
		}

		if _, cycle := visiting[pkg.ImportPath]; cycle {
			return 0
		}

		visiting[pkg.ImportPath] = struct{}{}
		level := 0

		imports := append([]*types.Package(nil), pkg.TypesPkg.Imports()...)
		sort.Slice(imports, func(i, j int) bool { return imports[i].Path() < imports[j].Path() })

		for _, imported := range imports {
			if dependency := byPath[imported.Path()]; dependency != nil {
				level = max(level, assignLevel(dependency)+1)
			}
		}

		delete(visiting, pkg.ImportPath)
		levels[pkg.ImportPath] = level

		return level
	}

	maximumLevel := 0

	for _, pkg := range pkgs {
		maximumLevel = max(maximumLevel, assignLevel(pkg))
	}

	ordered := make([][]*LoadedPackage, maximumLevel+1)

	for _, pkg := range pkgs {
		level := levels[pkg.ImportPath]
		ordered[level] = append(ordered[level], pkg)
	}

	for _, level := range ordered {
		sort.Slice(level, func(i, j int) bool {
			return level[i].ImportPath < level[j].ImportPath
		})
	}

	return ordered
}
