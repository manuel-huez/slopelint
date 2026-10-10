package lint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSimilarityLocalWritesStampAndCIUsesItWithoutServer(t *testing.T) {
	tmp := newTestModule(t)
	cacheDir := t.TempDir()
	writeSimilarityTestSource(t, tmp)

	embedder := &similarityTestEmbedder{vector: func(input string) []float32 {
		if strings.Contains(input, "func first") {
			return []float32{1, 0}
		}

		return []float32{0, 1}
	}}

	pkgs := loadPackagesForTest(t, tmp)

	issues, err := CheckSimilarCode(pkgs, SimilarityOptions{
		CacheEnabled:        true,
		cacheDir:            cacheDir,
		embedder:            embedder,
		descriptionDisabled: true,
	})
	if err != nil {
		t.Fatalf("local check: %v", err)
	}

	if len(issues) != 0 {
		t.Fatalf("local issues: %v", issues)
	}

	if embedder.calls != 1 {
		t.Fatalf("embedding batches = %d, want 1", embedder.calls)
	}

	stampPath := filepath.Join(tmp, similarityStampName)
	if _, err := os.Stat(stampPath); err != nil {
		t.Fatalf("stamp: %v", err)
	}

	pkgs = loadPackagesForTest(t, tmp)

	issues, err = CheckSimilarCode(pkgs, SimilarityOptions{descriptionDisabled: true})
	if err != nil {
		t.Fatalf("stamped local check: %v", err)
	}

	if len(issues) != 0 {
		t.Fatalf("stamped local issues: %v", issues)
	}

	issues, err = CheckSimilarCode(pkgs, SimilarityOptions{CI: true})
	if err != nil {
		t.Fatalf("CI check: %v", err)
	}

	if len(issues) != 0 {
		t.Fatalf("CI issues: %v", issues)
	}

	writeFile(
		t,
		filepath.Join(tmp, similarityTestFilename),
		similarityTestSource+"\n// source changed\n",
	)
	pkgs = loadPackagesForTest(t, tmp)

	_, err = CheckSimilarCode(pkgs, SimilarityOptions{CI: true})
	if err == nil || !strings.Contains(err.Error(), "is stale") {
		t.Fatalf("stale CI check error = %v", err)
	}
}

func TestSimilarityLocalRefreshesRepositoryDigestWithoutInference(t *testing.T) {
	tmp := newTestModule(t)
	writeSimilarityTestSource(t, tmp)
	initTestGitRepository(t, tmp)
	pkgs := loadPackagesForTest(t, tmp)

	sourceDigest, err := similaritySourceDigest(pkgs, tmp)
	if err != nil {
		t.Fatal(err)
	}

	stamp := newSimilarityStamp(sourceDigest, 0, nil, false, "")
	stamp.RepositoryDigest = "old-digest"

	encoded, err := json.Marshal(stamp)
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(tmp, similarityStampName), string(encoded))

	if _, err := CheckSimilarCode(pkgs, SimilarityOptions{descriptionDisabled: true}); err != nil {
		t.Fatalf("refresh stamp: %v", err)
	}

	stored, err := loadSimilarityStamp(tmp)
	if err != nil {
		t.Fatal(err)
	}

	digest, err := similarityRepositoryDigest(tmp)
	if err != nil || stored.RepositoryDigest != digest {
		t.Fatalf("refreshed digest = %q, want %q, err=%v", stored.RepositoryDigest, digest, err)
	}
}

func TestLintRepositoryCIMissingStampFailsBeforeGoList(t *testing.T) {
	tmp := newTestModule(t)
	forbidGoCommand(t)

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

	forbidGoCommand(t)

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

func TestLintRepositoryCIValidStampStillRunsStructuralLint(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(
		t,
		filepath.Join(tmp, "sample.go"),
		"package sample\n"+strings.Repeat("// filler\n", 1000),
	)
	initTestGitRepository(t, tmp)

	stamp := newSimilarityStamp("source", 1, nil, false, "")
	if err := storeSimilarityStamp(tmp, stamp); err != nil {
		t.Fatal(err)
	}

	issues, err := LintRepository(
		[]string{allPackagesPattern},
		tmp,
		Options{CacheEnabled: true, cacheDir: t.TempDir()},
		&SimilarityOptions{CI: true, CacheEnabled: true},
	)
	if err != nil {
		t.Fatalf("valid CI stamp: %v", err)
	}

	if messages := joinMessages(issues); !strings.Contains(messages, "source file has 1001 lines") {
		t.Fatalf("valid CI issues = %s", messages)
	}
}

func TestLintRepositoryCIRejectsStampWithoutRepositoryDigest(t *testing.T) {
	tmp := newTestModule(t)
	stamp := newSimilarityStamp("source", 1, nil, false, "")

	data, err := json.Marshal(stamp)
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(tmp, similarityStampName), string(data))

	_, err = LintRepository(
		[]string{allPackagesPattern},
		tmp,
		Options{CacheEnabled: true, cacheDir: t.TempDir()},
		&SimilarityOptions{CI: true, CacheEnabled: true},
	)
	if err == nil || !strings.Contains(err.Error(), "lacks a repository digest") {
		t.Fatalf("missing-repository-digest error = %v", err)
	}
}

func forbidGoCommand(t *testing.T) {
	t.Helper()

	binDir := t.TempDir()

	goPath := filepath.Join(binDir, "go")
	if err := os.WriteFile(
		goPath,
		[]byte("#!/bin/sh\n: > \"$0.called\"\nexit 1\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}

	// Keep Git wrappers' interpreters and tools available while blocking Go.
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		if _, err := os.Stat(goPath + ".called"); err == nil {
			t.Error("Go ran before CI stamp rejection")
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	})
}
