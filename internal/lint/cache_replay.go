package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"golang.org/x/tools/go/analysis"
)

func cachedExportsForLinter(l *linter) []analysisCacheExport {
	funcs := l.collectSummarizableFuncs()
	if len(funcs) == 0 {
		return nil
	}

	out := make([]analysisCacheExport, 0, len(funcs))

	for _, fn := range funcs {
		summary := l.summaryWithExplicit(fn.key, l.inferredFacts[fn.key])
		out = append(out, analysisCacheExport{
			FuncKey: fn.key,
			Fact:    *callSummaryFactFromSummary(summary),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].FuncKey < out[j].FuncKey
	})

	return out
}

func replayAnalysisCache(
	pass *analysis.Pass,
	pkg *LoadedPackage,
	entry *analysisCacheEntry,
	cacheHitHook func(string),
) ([]Issue, bool) {
	if entry == nil {
		return nil, false
	}

	funcs := packageFuncObjects(pkg)
	for _, export := range entry.Exports {
		obj := funcs[export.FuncKey]
		if obj == nil {
			return nil, false
		}

		fact := cloneCallSummaryFact(&export.Fact)
		pass.ExportObjectFact(obj.Origin(), fact)
	}

	files, filesByContent := packageCacheFiles(pass.Files, pass.Fset, pkg.Dir)
	issues := make([]Issue, 0, len(entry.Issues))

	for _, cached := range entry.Issues {
		file := cachedPackageFile(cached, files, filesByContent)
		if file == nil || cached.Offset < 0 || cached.Offset > file.Size() {
			return nil, false
		}

		issues = append(issues, Issue{
			Pos:     file.Pos(cached.Offset),
			Kind:    cached.Kind,
			Message: cached.Message,
			fset:    pass.Fset,
		})
	}

	if cacheHitHook != nil {
		cacheHitHook(pkg.ImportPath)
	}

	return issues, true
}

func replayRepoAnalysisCache(
	entry *analysisCacheEntry,
	cacheHitHook func(string),
	sourceRoot string,
) ([]Issue, bool) {
	issues, ok := replayPositionedAnalysisIssues(entry, sourceRoot)
	if !ok {
		return nil, false
	}

	if cacheHitHook != nil {
		cacheHitHook(repoAnalysisCacheHitName)
	}

	return issues, true
}

func replayStandaloneAnalysisCache(
	entry *analysisCacheEntry,
	summaries map[string]callSummary,
	cacheHitHook func(string),
	pkg *LoadedPackage,
) ([]Issue, []analysisCacheExport, bool) {
	if entry == nil {
		return nil, nil, false
	}

	if !analysisCacheDependenciesMatch(entry.Dependencies, summaries) {
		return nil, nil, false
	}

	exports, ok := validAnalysisCacheExports(entry.Exports)
	if !ok {
		return nil, nil, false
	}

	issues, ok := replayStandaloneAnalysisIssues(entry, pkg)
	if !ok {
		return nil, nil, false
	}

	if cacheHitHook != nil {
		cacheHitHook(pkg.ImportPath)
	}

	return issues, exports, true
}

func analysisCacheDependenciesMatch(
	cached []analysisCacheImportedFact,
	summaries map[string]callSummary,
) bool {
	dependencies := make(map[string]struct{}, len(cached))
	for _, dependency := range cached {
		if dependency.FuncKey == "" {
			return false
		}

		if _, duplicate := dependencies[dependency.FuncKey]; duplicate {
			return false
		}

		dependencies[dependency.FuncKey] = struct{}{}

		current, present := summaries[dependency.FuncKey]
		if present != dependency.Present {
			return false
		}

		if present && !callSummaryEqual(current, callSummaryFromFact(&dependency.Fact)) {
			return false
		}
	}

	return true
}

func validAnalysisCacheExports(
	cached []analysisCacheExport,
) ([]analysisCacheExport, bool) {
	exports := make([]analysisCacheExport, len(cached))
	seen := make(map[string]struct{}, len(cached))

	for index, export := range cached {
		if export.FuncKey == "" {
			return nil, false
		}

		if _, duplicate := seen[export.FuncKey]; duplicate {
			return nil, false
		}

		seen[export.FuncKey] = struct{}{}
		exports[index] = analysisCacheExport{
			FuncKey: export.FuncKey,
			Fact:    *cloneCallSummaryFact(&export.Fact),
		}
	}

	return exports, true
}

func replayPositionedAnalysisIssues(
	entry *analysisCacheEntry,
	sourceRoot string,
) ([]Issue, bool) {
	if entry == nil {
		return nil, false
	}

	issues := make([]Issue, 0, len(entry.Issues))

	for _, cached := range entry.Issues {
		if !validCachedAnalysisIssue(cached) {
			return nil, false
		}

		issues = append(issues, Issue{
			Kind:    cached.Kind,
			Message: cached.Message,
			position: token.Position{
				Filename: filepath.Join(sourceRoot, filepath.FromSlash(cached.Filename)),
				Offset:   cached.Offset,
				Line:     cached.Line,
				Column:   cached.Column,
			},
		})
	}

	return issues, true
}

func replayStandaloneAnalysisIssues(
	entry *analysisCacheEntry,
	pkg *LoadedPackage,
) ([]Issue, bool) {
	files, filesByContent := packageCacheFiles(pkg.Files, pkg.FSet, pkg.Dir)
	issues := make([]Issue, 0, len(entry.Issues))

	for _, cached := range entry.Issues {
		if !validCachedAnalysisIssue(cached) {
			return nil, false
		}

		file := cachedPackageFile(cached, files, filesByContent)
		if file == nil || cached.Offset > file.Size() {
			return nil, false
		}

		issues = append(issues, Issue{
			Kind:    cached.Kind,
			Message: cached.Message,
			position: token.Position{
				Filename: file.Name(),
				Offset:   cached.Offset,
				Line:     cached.Line,
				Column:   cached.Column,
			},
		})
	}

	return issues, true
}

func validCachedAnalysisIssue(cached analysisCacheIssue) bool {
	return cached.Filename != "" && filepath.IsLocal(cached.Filename) &&
		len(cached.FileID) == sha256.Size*2 && cached.Offset >= 0 &&
		cached.Line > 0 && cached.Column > 0
}

func packageFuncObjects(pkg *LoadedPackage) map[string]*types.Func {
	l := newLinter(pkg, Options{})

	out := make(map[string]*types.Func)

	for _, fn := range l.collectSummarizableFuncs() {
		obj, ok := pkg.TypesInfo.ObjectOf(fn.decl.Name).(*types.Func)
		if !ok || obj == nil {
			continue
		}

		out[fn.key] = obj
	}

	return out
}

func packageCacheFiles(
	files []*ast.File,
	fset *token.FileSet,
	sourceRoot string,
) (map[string]*token.File, map[string][]*token.File) {
	byPath := make(map[string]*token.File, len(files))
	byContent := make(map[string][]*token.File, len(files))

	for _, file := range files {
		tokenFile := fset.File(file.Package)
		if tokenFile == nil {
			continue
		}

		relativePath, err := filepath.Rel(sourceRoot, tokenFile.Name())
		if err != nil || !filepath.IsLocal(relativePath) {
			continue
		}

		content, err := os.ReadFile(tokenFile.Name())
		if err != nil {
			continue
		}

		digest := sha256.Sum256(content)
		contentID := hex.EncodeToString(digest[:])
		byPath[filepath.ToSlash(relativePath)] = tokenFile
		byContent[contentID] = append(byContent[contentID], tokenFile)
	}

	return byPath, byContent
}

func cachedPackageFile(
	cached analysisCacheIssue,
	files map[string]*token.File,
	filesByContent map[string][]*token.File,
) *token.File {
	if hinted := files[cached.Filename]; hinted != nil {
		if slices.Contains(filesByContent[cached.FileID], hinted) {
			return hinted
		}
	}

	candidates := filesByContent[cached.FileID]
	if len(candidates) == 1 {
		return candidates[0]
	}

	return nil
}

func cloneCallSummaryFact(fact *callSummaryFact) *callSummaryFact {
	if fact == nil {
		return &callSummaryFact{}
	}

	return callSummaryFactFromSummary(callSummaryFromFact(fact))
}
