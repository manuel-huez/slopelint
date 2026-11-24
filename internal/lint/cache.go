package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go/ast"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	flowcheck "github.com/manuel-huez/slopelint/internal/lint/flow"
	smellcheck "github.com/manuel-huez/slopelint/internal/lint/smells"
	"golang.org/x/tools/go/analysis"
)

// Bump when the persisted cache/replay format changes. Analysis keys track builds.
const analysisCacheSchema = 14

const analysisCacheTypeDigestRefreshLimit = 8

const unsafeImportPath = "unsafe"

const cacheDirPerm = 0o755

const repoAnalysisCacheHitName = "repo"

const repoBehaviorCacheHitName = "behavior"

var errAnalysisCacheDisabled = errors.New("analysis cache disabled")

func slopelintCacheRoot(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}

	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(userCacheDir, "slopelint"), nil
}

type analysisCache struct {
	path       string
	sourceRoot string
}

type repoAnalysisCache struct {
	path       string
	sourceRoot string
}

type analysisCacheEntry struct {
	Issues       []analysisCacheIssue        `json:"issues"`
	Exports      []analysisCacheExport       `json:"exports"`
	Dependencies []analysisCacheImportedFact `json:"dependencies,omitempty"`
}

type analysisCacheIssue struct {
	Filename string `json:"filename"`
	FileID   string `json:"file_id"`
	Offset   int    `json:"offset"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Kind     string `json:"kind"`
	Message  string `json:"message"`
}

type analysisCacheExport struct {
	FuncKey string          `json:"func_key"`
	Fact    callSummaryFact `json:"fact"`
}

type analysisCacheFingerprint struct {
	Schema        int                         `json:"schema"`
	BuildID       string                      `json:"build_id"`
	Package       string                      `json:"package"`
	MaxStates     int                         `json:"max_states"`
	Executable    analysisCacheExecutable     `json:"executable"`
	Files         []analysisCacheFile         `json:"files"`
	ImportedFacts []analysisCacheImportedFact `json:"imported_facts"`
}

type analysisCacheExecutable struct {
	Path             string `json:"path"`
	Size             int64  `json:"size"`
	ModTimeUnixNanos int64  `json:"mod_time_unix_nanos"`
}

type analysisCacheFile struct {
	SHA256 string `json:"sha256"`
	Name   string `json:"name,omitempty"`
}

type analysisCacheImportedFact struct {
	FuncKey string          `json:"func_key"`
	Present bool            `json:"present"`
	Fact    callSummaryFact `json:"fact"`
}

func analysisCacheForSourceRoot(
	sourceRoot string,
	opts Options,
	namespace string,
	keyForPackage func() (string, error),
) (*analysisCache, error) {
	if !opts.CacheEnabled {
		return nil, errAnalysisCacheDisabled
	}

	root, err := analysisCacheRoot(opts.cacheDir)
	if err != nil {
		return nil, err
	}

	key, err := keyForPackage()
	if err != nil {
		return nil, err
	}

	return &analysisCache{
		path:       filepath.Join(root, namespace, key[:2], key[2:]+".json"),
		sourceRoot: sourceRoot,
	}, nil
}

func newRepoAnalysisCache(
	patterns []string,
	dir string,
	opts Options,
	similarity *SimilarityOptions,
) (*repoAnalysisCache, error) {
	if !opts.CacheEnabled {
		return nil, errAnalysisCacheDisabled
	}

	root, err := analysisCacheRoot(opts.cacheDir)
	if err != nil {
		return nil, err
	}

	key, sourceRoot, err := repoAnalysisCacheKey(patterns, dir, opts, similarity)
	if err != nil {
		return nil, err
	}

	return &repoAnalysisCache{
		path:       filepath.Join(root, "repo", key[:2], key[2:]+".json"),
		sourceRoot: sourceRoot,
	}, nil
}

// CacheEnabledFromEnv reports whether SLOPELINT_CACHE leaves persistent cache enabled.
func CacheEnabledFromEnv() bool {
	value := strings.TrimSpace(os.Getenv("SLOPELINT_CACHE"))
	if value == "" {
		return true
	}

	switch strings.ToLower(value) {
	case zeroIntText, boolFalseText, offText, "no":
		return false
	default:
		return true
	}
}

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
			FuncKey: smellcheck.FuncObjectKey(fn),
			Present: true,
			Fact:    *cloneCallSummaryFact(fact),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].FuncKey < out[j].FuncKey
	})

	return out
}

func (cache *analysisCache) load() (*analysisCacheEntry, bool) {
	if cache == nil {
		return nil, false
	}

	return loadAnalysisCacheEntry(cache.path)
}

func (cache *repoAnalysisCache) load() (*analysisCacheEntry, bool) {
	if cache == nil {
		return nil, false
	}

	return loadAnalysisCacheEntry(cache.path)
}

func (cache *repoAnalysisCache) replay(hit func(string)) ([]Issue, bool) {
	entry, ok := cache.load()
	if !ok {
		return nil, false
	}

	return replayRepoAnalysisCache(entry, hit, cache.sourceRoot)
}

func loadAnalysisCacheEntry(path string) (*analysisCacheEntry, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}

	var entry analysisCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, false
	}

	refreshCacheEntry(path)

	return &entry, true
}

func (cache *analysisCache) store(
	pass *analysis.Pass,
	l *linter,
	issues []Issue,
) error {
	if cache == nil {
		return nil
	}

	entry, err := buildAnalysisCacheEntry(pass, l, issues, cache.sourceRoot)
	if err != nil {
		return err
	}

	return writeAnalysisCacheEntry(cache.path, entry)
}

func (cache *analysisCache) storeStandalone(
	l *linter,
	issues []Issue,
	dependencies []analysisCacheImportedFact,
) error {
	if cache == nil {
		return nil
	}

	entry, err := buildStandaloneAnalysisCacheEntry(
		l,
		issues,
		dependencies,
		cache.sourceRoot,
	)
	if err != nil {
		return err
	}

	return writeAnalysisCacheEntry(cache.path, entry)
}

func (cache *repoAnalysisCache) store(issues []Issue) error {
	if cache == nil {
		return nil
	}

	entry, err := buildRepoAnalysisCacheEntry(issues, cache.sourceRoot)
	if err != nil {
		return err
	}

	return writeAnalysisCacheEntry(cache.path, entry)
}

func writeAnalysisCacheEntry(path string, entry analysisCacheEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	return writeFileAtomically(path, data)
}

func writeFileAtomically(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, cacheDirPerm); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	// Cache pruning removes empty directories, also one just created here.
	if errors.Is(err, fs.ErrNotExist) {
		if err = os.MkdirAll(dir, cacheDirPerm); err == nil {
			tmp, err = os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
		}
	}

	if err != nil {
		return err
	}

	name := tmp.Name()

	defer func() {
		_ = os.Remove(name)
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(name, path)
}

func buildAnalysisCacheEntry(
	pass *analysis.Pass,
	l *linter,
	issues []Issue,
	sourceRoot string,
) (analysisCacheEntry, error) {
	entry := analysisCacheEntry{
		Issues:  make([]analysisCacheIssue, 0, len(issues)),
		Exports: cachedExportsForLinter(l),
	}

	cachedIssues, err := buildAnalysisCacheIssues(
		issues,
		sourceRoot,
		func(issue Issue) (token.Position, error) {
			return pass.Fset.Position(issue.Pos), nil
		},
	)
	if err != nil {
		return analysisCacheEntry{}, err
	}

	entry.Issues = cachedIssues

	return entry, nil
}

func buildRepoAnalysisCacheEntry(
	issues []Issue,
	sourceRoot string,
) (analysisCacheEntry, error) {
	entry := analysisCacheEntry{
		Issues: make([]analysisCacheIssue, 0, len(issues)),
	}

	cachedIssues, err := buildAnalysisCacheIssues(
		issues,
		sourceRoot,
		func(issue Issue) (token.Position, error) {
			return issuePosition(issue), nil
		},
	)
	if err != nil {
		return analysisCacheEntry{}, err
	}

	entry.Issues = cachedIssues

	return entry, nil
}

func buildStandaloneAnalysisCacheEntry(
	l *linter,
	issues []Issue,
	dependencies []analysisCacheImportedFact,
	sourceRoot string,
) (analysisCacheEntry, error) {
	entry := analysisCacheEntry{
		Exports:      cachedExportsForLinter(l),
		Dependencies: dependencies,
	}

	cachedIssues, err := buildAnalysisCacheIssues(
		issues,
		sourceRoot,
		func(issue Issue) (token.Position, error) {
			return issuePosition(issue), nil
		},
	)
	if err != nil {
		return analysisCacheEntry{}, err
	}

	entry.Issues = cachedIssues

	return entry, nil
}

func buildAnalysisCacheIssues(
	issues []Issue,
	sourceRoot string,
	position func(Issue) (token.Position, error),
) ([]analysisCacheIssue, error) {
	out := make([]analysisCacheIssue, 0, len(issues))
	fileIDs := make(map[string]string)

	for _, issue := range issues {
		pos, err := position(issue)
		if err != nil {
			return nil, err
		}

		if pos.Filename == "" || pos.Offset < 0 {
			return nil, errors.New(
				"cannot cache issue without stable source position",
			)
		}

		relativePath, err := filepath.Rel(sourceRoot, pos.Filename)
		if err != nil || !filepath.IsLocal(relativePath) {
			return nil, errAnalysisCacheDisabled
		}

		fileID := fileIDs[pos.Filename]
		if fileID == "" {
			content, readErr := os.ReadFile(pos.Filename)
			if readErr != nil {
				return nil, readErr
			}

			digest := sha256.Sum256(content)
			fileID = hex.EncodeToString(digest[:])
			fileIDs[pos.Filename] = fileID
		}

		out = append(out, analysisCacheIssue{
			Filename: filepath.ToSlash(relativePath),
			FileID:   fileID,
			Offset:   pos.Offset,
			Line:     pos.Line,
			Column:   pos.Column,
			Kind:     issue.Kind,
			Message:  issue.Message,
		})
	}

	return out, nil
}

func cachedExportsForLinter(l *linter) []analysisCacheExport {
	var out []analysisCacheExport

	l.forEachExport(func(export flowcheck.Export) {
		out = append(out, analysisCacheExport{
			FuncKey: export.Key,
			Fact:    *callSummaryFactFromSummary(export.Summary),
		})
	})

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

	files, filesByContent, err := packageCacheFiles(pass.Files, pass.Fset, pkg.Dir)
	if err != nil {
		return nil, false
	}

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
	summaries map[string]flowcheck.CallSummary,
	cacheHitHook func(string),
	importPath string,
	files []analysisCacheSourceFile,
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

	issues, ok := replayStandaloneAnalysisIssues(entry, files)
	if !ok {
		return nil, nil, false
	}

	if cacheHitHook != nil {
		cacheHitHook(importPath)
	}

	return issues, exports, true
}

func analysisCacheDependenciesMatch(
	cached []analysisCacheImportedFact,
	summaries map[string]flowcheck.CallSummary,
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

		if present && !flowcheck.CallSummaryEqual(current, callSummaryFromFact(&dependency.Fact)) {
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
	files []analysisCacheSourceFile,
) ([]Issue, bool) {
	var (
		filesByPath    = make(map[string]*analysisCacheSourceFile, len(files))
		filesByContent = make(map[string][]*analysisCacheSourceFile, len(files))
	)

	for index := range files {
		file := &files[index]
		filesByPath[file.RelativePath] = file
		filesByContent[file.SHA256] = append(filesByContent[file.SHA256], file)
	}

	issues := make([]Issue, 0, len(entry.Issues))

	for _, cached := range entry.Issues {
		if !validCachedAnalysisIssue(cached) {
			return nil, false
		}

		file := cachedAnalysisSourceFile(cached, filesByPath, filesByContent)
		if file == nil || cached.Offset > file.Size {
			return nil, false
		}

		issues = append(issues, Issue{
			Kind:    cached.Kind,
			Message: cached.Message,
			position: token.Position{
				Filename: file.Name,
				Offset:   cached.Offset,
				Line:     cached.Line,
				Column:   cached.Column,
			},
		})
	}

	return issues, true
}

func cachedAnalysisSourceFile(
	cached analysisCacheIssue,
	files map[string]*analysisCacheSourceFile,
	filesByContent map[string][]*analysisCacheSourceFile,
) *analysisCacheSourceFile {
	if hinted := files[cached.Filename]; hinted != nil && hinted.SHA256 == cached.FileID {
		return hinted
	}

	candidates := filesByContent[cached.FileID]
	if len(candidates) == 1 {
		return candidates[0]
	}

	return nil
}

func validCachedAnalysisIssue(cached analysisCacheIssue) bool {
	return cached.Filename != "" && filepath.IsLocal(cached.Filename) &&
		len(cached.FileID) == sha256.Size*2 && cached.Offset >= 0 &&
		cached.Line > 0 && cached.Column > 0
}

func packageFuncObjects(pkg *LoadedPackage) map[string]*types.Func {
	return flowcheck.PackageFuncObjects(&flowcheck.Package{
		Files:     pkg.Files,
		FSet:      pkg.FSet,
		TypesPkg:  pkg.TypesPkg,
		TypesInfo: pkg.TypesInfo,
	})
}

func packageCacheFiles(
	files []*ast.File,
	fset *token.FileSet,
	sourceRoot string,
) (map[string]*token.File, map[string][]*token.File, error) {
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
			return nil, nil, err
		}

		digest := sha256.Sum256(content)
		contentID := hex.EncodeToString(digest[:])
		byPath[filepath.ToSlash(relativePath)] = tokenFile
		byContent[contentID] = append(byContent[contentID], tokenFile)
	}

	return byPath, byContent, nil
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
