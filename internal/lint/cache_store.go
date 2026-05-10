package lint

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/tools/go/analysis"
)

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
