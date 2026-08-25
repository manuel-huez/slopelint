package lint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLintRepositoryCIMissingStampFailsBeforeGoList(t *testing.T) {
	tmp := newTestModule(t)
	path := gitOnlyPath(t)
	t.Setenv("PATH", path)

	_, err := LintRepository(
		[]string{allPackagesPattern},
		tmp,
		Options{CacheEnabled: true, cacheDir: t.TempDir()},
		&SimilarityOptions{CI: true, CacheEnabled: true},
	)
	if err == nil || !strings.Contains(err.Error(), similarityStampName+" is missing") {
		t.Fatalf("missing-stamp error = %v", err)
	}
}

func TestLintRepositoryCIStaleStampFailsBeforeGoList(t *testing.T) {
	tmp := newTestModule(t)
	sourcePath := filepath.Join(tmp, "sample.go")
	writeFile(t, sourcePath, "package sample\n")
	initTestGitRepository(t, tmp)

	stamp := newSimilarityStamp("source", 0, nil, false, "")
	if err := storeSimilarityStamp(tmp, stamp); err != nil {
		t.Fatal(err)
	}

	writeFile(t, sourcePath, "package sample\n\nfunc changed() {}\n")

	stored, err := loadSimilarityStamp(tmp)
	if err != nil || stored.RepositoryDigest == "" {
		t.Fatalf("stored repository digest = %q, err=%v", stored.RepositoryDigest, err)
	}

	currentDigest, err := similarityRepositoryDigest(tmp)
	if err != nil || currentDigest == stored.RepositoryDigest {
		t.Fatalf(
			"current repository digest = %q, stored=%q, err=%v",
			currentDigest,
			stored.RepositoryDigest,
			err,
		)
	}

	path := gitOnlyPath(t)
	t.Setenv("PATH", path)

	_, err = LintRepository(
		[]string{allPackagesPattern},
		tmp,
		Options{CacheEnabled: true, cacheDir: t.TempDir()},
		&SimilarityOptions{CI: true, CacheEnabled: true},
	)
	if err == nil || !strings.Contains(err.Error(), similarityStampName+" is stale") {
		t.Fatalf("stale-stamp error = %v", err)
	}
}

func gitOnlyPath(t *testing.T) string {
	t.Helper()

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}

	path := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(path, "git")); err != nil {
		t.Fatal(err)
	}

	return path
}
