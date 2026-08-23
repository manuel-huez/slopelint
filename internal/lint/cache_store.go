package lint

import (
	"encoding/json"
	"errors"
	"go/token"
	"os"
	"path/filepath"

	"golang.org/x/tools/go/analysis"
)

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

func loadAnalysisCacheEntry(path string) (*analysisCacheEntry, bool) {
	data, err := os.ReadFile(path)
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
	l *linter,
	issues []Issue,
) error {
	if cache == nil {
		return nil
	}

	entry, err := buildAnalysisCacheEntry(pass, l, issues)
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

	entry, err := buildStandaloneAnalysisCacheEntry(l, issues, dependencies)
	if err != nil {
		return err
	}

	return writeAnalysisCacheEntry(cache.path, entry)
}

func (cache *repoAnalysisCache) store(issues []Issue) error {
	if cache == nil {
		return nil
	}

	entry, err := buildRepoAnalysisCacheEntry(issues)
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
) (analysisCacheEntry, error) {
	entry := analysisCacheEntry{
		Issues:  make([]analysisCacheIssue, 0, len(issues)),
		Exports: cachedExportsForLinter(l),
	}

	cachedIssues, err := buildAnalysisCacheIssues(
		issues,
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

func buildRepoAnalysisCacheEntry(issues []Issue) (analysisCacheEntry, error) {
	entry := analysisCacheEntry{
		Issues: make([]analysisCacheIssue, 0, len(issues)),
	}

	cachedIssues, err := buildAnalysisCacheIssues(
		issues,
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
) (analysisCacheEntry, error) {
	entry := analysisCacheEntry{
		Exports:      cachedExportsForLinter(l),
		Dependencies: dependencies,
	}

	cachedIssues, err := buildAnalysisCacheIssues(
		issues,
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
	position func(Issue) (token.Position, error),
) ([]analysisCacheIssue, error) {
	out := make([]analysisCacheIssue, 0, len(issues))

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

		out = append(out, analysisCacheIssue{
			Filename: pos.Filename,
			Offset:   pos.Offset,
			Line:     pos.Line,
			Column:   pos.Column,
			Kind:     issue.Kind,
			Message:  issue.Message,
		})
	}

	return out, nil
}
