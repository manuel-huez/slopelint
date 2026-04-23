package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/tools/go/analysis"
)

const analysisCacheSchema = 1

const cacheDirPerm = 0o755

var analysisCacheHitHook func(string)

var errAnalysisCacheDisabled = errors.New("analysis cache disabled")

type analysisCache struct {
	path string
}

type analysisCacheEntry struct {
	Issues  []analysisCacheIssue  `json:"issues"`
	Exports []analysisCacheExport `json:"exports"`
}

type analysisCacheIssue struct {
	Filename string `json:"filename"`
	Offset   int    `json:"offset"`
	Kind     string `json:"kind"`
	Message  string `json:"message"`
}

type analysisCacheExport struct {
	FuncKey string          `json:"func_key"`
	Fact    callSummaryFact `json:"fact"`
}

type analysisCacheFingerprint struct {
	Schema        int                         `json:"schema"`
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
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
}

type analysisCacheImportedFact struct {
	FuncKey string          `json:"func_key"`
	Fact    callSummaryFact `json:"fact"`
}

func newAnalysisCache(
	pass *analysis.Pass,
	pkg *LoadedPackage,
	opts Options,
) (*analysisCache, error) {
	if !opts.CacheEnabled {
		return nil, errAnalysisCacheDisabled
	}

	root, err := analysisCacheRoot(opts.CacheDir)
	if err != nil {
		return nil, err
	}

	key, err := analysisCacheKey(pass, pkg, opts)
	if err != nil {
		return nil, err
	}

	return &analysisCache{
		path: filepath.Join(root, key[:2], key[2:]+".json"),
	}, nil
}

func analysisCacheRoot(dir string) (string, error) {
	if dir == "" {
		userCacheDir, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}

		dir = filepath.Join(userCacheDir, "defenselint")
	}

	return filepath.Join(dir, fmt.Sprintf("analysis-v%d", analysisCacheSchema)), nil
}

func analysisCacheKey(
	pass *analysis.Pass,
	pkg *LoadedPackage,
	opts Options,
) (string, error) {
	maxStates := opts.MaxStates
	if maxStates <= 0 {
		maxStates = 32
	}

	fingerprint := analysisCacheFingerprint{
		Schema:        analysisCacheSchema,
		Package:       pkg.ImportPath,
		MaxStates:     maxStates,
		Executable:    analysisCacheExecutableStamp(),
		ImportedFacts: analysisCacheImportedFacts(pass, pkg),
	}

	files, err := analysisCacheFiles(pass)
	if err != nil {
		return "", err
	}

	fingerprint.Files = files

	data, err := json.Marshal(fingerprint)
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), nil
}

func analysisCacheExecutableStamp() analysisCacheExecutable {
	path, err := os.Executable()
	if err != nil {
		return analysisCacheExecutable{}
	}

	info, err := os.Stat(path)
	if err != nil {
		return analysisCacheExecutable{Path: path}
	}

	return analysisCacheExecutable{
		Path:             path,
		Size:             info.Size(),
		ModTimeUnixNanos: info.ModTime().UnixNano(),
	}
}

func analysisCacheFiles(pass *analysis.Pass) ([]analysisCacheFile, error) {
	files := make([]analysisCacheFile, 0, len(pass.Files))

	for _, file := range pass.Files {
		tokenFile := pass.Fset.File(file.Package)
		if tokenFile == nil {
			return nil, errors.New("missing token file for package file")
		}

		name := tokenFile.Name()

		content, err := readAnalysisFile(pass, name)
		if err != nil {
			return nil, err
		}

		sum := sha256.Sum256(content)
		files = append(files, analysisCacheFile{
			Filename: name,
			SHA256:   hex.EncodeToString(sum[:]),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Filename < files[j].Filename
	})

	return files, nil
}

func readAnalysisFile(pass *analysis.Pass, name string) ([]byte, error) {
	if pass.ReadFile != nil {
		return pass.ReadFile(name)
	}

	return os.ReadFile(name)
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
			FuncKey: funcObjectKey(fn),
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

	data, err := os.ReadFile(cache.path)
	if err != nil {
		return nil, false
	}

	var entry analysisCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, false
	}

	return &entry, true
}

func (cache *analysisCache) store(
	pass *analysis.Pass,
	pkg *LoadedPackage,
	l *linter,
	issues []Issue,
) error {
	if cache == nil {
		return nil
	}

	entry, err := buildAnalysisCacheEntry(pass, pkg, l, issues)
	if err != nil {
		return err
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	dir := filepath.Dir(cache.path)
	if err := os.MkdirAll(dir, cacheDirPerm); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(cache.path)+".tmp-*")
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

	return os.Rename(name, cache.path)
}

func buildAnalysisCacheEntry(
	pass *analysis.Pass,
	pkg *LoadedPackage,
	l *linter,
	issues []Issue,
) (analysisCacheEntry, error) {
	entry := analysisCacheEntry{
		Issues:  make([]analysisCacheIssue, 0, len(issues)),
		Exports: cachedExportsForLinter(pkg, l),
	}

	for _, issue := range issues {
		pos := pass.Fset.Position(issue.Pos)
		if pos.Filename == "" || pos.Offset < 0 {
			return analysisCacheEntry{}, errors.New(
				"cannot cache issue without stable source position",
			)
		}

		entry.Issues = append(entry.Issues, analysisCacheIssue{
			Filename: pos.Filename,
			Offset:   pos.Offset,
			Kind:     issue.Kind,
			Message:  issue.Message,
		})
	}

	return entry, nil
}

func cachedExportsForLinter(pkg *LoadedPackage, l *linter) []analysisCacheExport {
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

	files := packageTokenFiles(pass.Files, pass.Fset)
	issues := make([]Issue, 0, len(entry.Issues))

	for _, cached := range entry.Issues {
		file := files[cached.Filename]
		if file == nil || cached.Offset < 0 || cached.Offset > file.Size() {
			return nil, false
		}

		issues = append(issues, Issue{
			Pos:     file.Pos(cached.Offset),
			Kind:    cached.Kind,
			Message: cached.Message,
		})
	}

	if analysisCacheHitHook != nil {
		analysisCacheHitHook(pkg.ImportPath)
	}

	return issues, true
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

func packageTokenFiles(files []*ast.File, fset *token.FileSet) map[string]*token.File {
	out := make(map[string]*token.File, len(files))

	for _, file := range files {
		tokenFile := fset.File(file.Package)
		if tokenFile == nil {
			continue
		}

		out[tokenFile.Name()] = tokenFile
	}

	return out
}

func cloneCallSummaryFact(fact *callSummaryFact) *callSummaryFact {
	if fact == nil {
		return &callSummaryFact{}
	}

	return callSummaryFactFromSummary(callSummaryFromFact(fact))
}
