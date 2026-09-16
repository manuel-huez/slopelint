package lint

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRepoAnalysisGitDigestTracksLintInputs(t *testing.T) {
	tmp := newTestModule(t)
	sourcePath := filepath.Join(tmp, "sample.go")
	source := "package sample\n"
	writeFile(t, sourcePath, source)
	writeFile(t, filepath.Join(tmp, "notes.md"), "first\n")
	initTestGitRepository(t, tmp)

	baseline, err := repoAnalysisSourceDigestForTest(tmp, []string{allPackagesPattern})
	if err != nil {
		t.Fatalf("baseline digest: %v", err)
	}

	writeFile(t, filepath.Join(tmp, "notes.md"), "second\n")

	markdownOnly, err := repoAnalysisSourceDigestForTest(tmp, []string{allPackagesPattern})
	if err != nil {
		t.Fatalf("markdown digest: %v", err)
	}

	if markdownOnly != baseline {
		t.Fatal("non-lint Markdown change invalidated repo cache")
	}

	writeFile(t, sourcePath, source+"func changed() {}\n")

	changedSource, err := repoAnalysisSourceDigestForTest(tmp, []string{allPackagesPattern})
	if err != nil {
		t.Fatalf("changed source digest: %v", err)
	}

	if changedSource == baseline {
		t.Fatal("tracked Go change did not invalidate repo cache")
	}

	writeFile(t, sourcePath, source)
	writeFile(t, filepath.Join(tmp, "new.go"), "package sample\n\nfunc added() {}\n")

	untrackedSource, err := repoAnalysisSourceDigestForTest(tmp, []string{allPackagesPattern})
	if err != nil {
		t.Fatalf("untracked source digest: %v", err)
	}

	if untrackedSource == baseline {
		t.Fatal("untracked Go file did not invalidate repo cache")
	}

	writeFile(t, filepath.Join(tmp, similarityStampName), "{}\n")

	stampSource, err := repoAnalysisSourceDigestForTest(tmp, []string{allPackagesPattern})
	if err != nil {
		t.Fatalf("stamp digest: %v", err)
	}

	if stampSource == untrackedSource {
		t.Fatal("similarity stamp did not invalidate repo cache")
	}
}

func TestLintRepositoryCacheTracksIgnoredGoFiles(t *testing.T) {
	tests := []struct {
		name   string
		before string
		after  string
		delete bool
	}{
		{name: "add", after: analysisCacheDiagnosticSource},
		{
			name:   "change",
			before: analysisCacheDiagnosticSource,
			after:  "package sample\n",
		},
		{name: "delete", before: analysisCacheDiagnosticSource, delete: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tmp := newTestModule(t)
			cacheDir := t.TempDir()
			generatedPath := filepath.Join(tmp, "generated.go")
			writeFile(t, filepath.Join(tmp, ".gitignore"), "generated.go\n")
			writeFile(t, filepath.Join(tmp, "sample.go"), "package sample\n")

			if test.before != "" {
				writeFile(t, generatedPath, test.before)
			}

			initTestGitRepository(t, tmp)

			cacheOpts := Options{MaxStates: 32, CacheEnabled: true, cacheDir: cacheDir}
			if _, err := LintRepository(
				[]string{allPackagesPattern},
				tmp,
				cacheOpts,
				nil,
			); err != nil {
				t.Fatalf("warm repo cache: %v", err)
			}

			if test.delete {
				if err := os.Remove(generatedPath); err != nil {
					t.Fatalf("delete ignored Go file: %v", err)
				}
			} else {
				writeFile(t, generatedPath, test.after)
			}

			repoHit := false
			cacheOpts.CacheHitHook = func(name string) {
				repoHit = repoHit || name == repoAnalysisCacheHitName
			}

			cached, err := LintRepository(
				[]string{allPackagesPattern},
				tmp,
				cacheOpts,
				nil,
			)
			if err != nil {
				t.Fatalf("cached lint after ignored file %s: %v", test.name, err)
			}

			fresh, err := LintRepository(
				[]string{allPackagesPattern},
				tmp,
				Options{MaxStates: 32},
				nil,
			)
			if err != nil {
				t.Fatalf("fresh lint after ignored file %s: %v", test.name, err)
			}

			if repoHit {
				t.Fatalf("ignored Go file %s reused stale repo cache", test.name)
			}

			if got, want := joinMessages(cached), joinMessages(fresh); got != want {
				t.Fatalf(
					"ignored Go file %s changed cached result:\nwant:\n%s\n\ngot:\n%s",
					test.name,
					want,
					got,
				)
			}
		})
	}
}

func TestRepoAnalysisGitDigestStableAcrossCommits(t *testing.T) {
	tmp := newTestModule(t)
	sourcePath := filepath.Join(tmp, "sample.go")
	writeFile(t, sourcePath, "package sample\n")
	writeFile(t, filepath.Join(tmp, "notes.md"), "first\n")
	initTestGitRepository(t, tmp)

	writeFile(t, sourcePath, "package sample\n\nfunc added() {}\n")

	changedSource, err := repoAnalysisSourceDigestForTest(tmp, []string{allPackagesPattern})
	if err != nil {
		t.Fatalf("changed source digest: %v", err)
	}

	commitTestGitRepository(t, tmp, "sample.go", "source")

	afterSourceCommit, err := repoAnalysisSourceDigestForTest(tmp, []string{allPackagesPattern})
	if err != nil {
		t.Fatalf("source commit digest: %v", err)
	}

	if afterSourceCommit != changedSource {
		t.Fatal("committing unchanged Go worktree invalidated repo cache")
	}

	writeFile(t, filepath.Join(tmp, "notes.md"), "second\n")
	commitTestGitRepository(t, tmp, "notes.md", "notes")

	afterNotesCommit, err := repoAnalysisSourceDigestForTest(tmp, []string{allPackagesPattern})
	if err != nil {
		t.Fatalf("notes commit digest: %v", err)
	}

	if afterNotesCommit != changedSource {
		t.Fatal("non-lint commit invalidated repo cache")
	}
}

func TestRepoAnalysisCacheKeyStableAcrossClones(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), "package sample\n")
	initTestGitRepository(t, tmp)

	clone := filepath.Join(t.TempDir(), "clone")

	cmd := exec.Command("git", "clone", "--no-hardlinks", tmp, clone)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone repository: %v: %s", err, output)
	}

	opts := Options{MaxStates: 32, CacheEnabled: true}
	similarity := &SimilarityOptions{CI: true, CacheEnabled: true}

	primaryKey, _, err := repoAnalysisCacheKey(
		[]string{allPackagesPattern},
		tmp,
		opts,
		similarity,
	)
	if err != nil {
		t.Fatalf("primary cache key: %v", err)
	}

	cloneKey, _, err := repoAnalysisCacheKey(
		[]string{allPackagesPattern},
		clone,
		opts,
		similarity,
	)
	if err != nil {
		t.Fatalf("clone cache key: %v", err)
	}

	if cloneKey != primaryKey {
		t.Fatalf("clone cache key = %q, want %q", cloneKey, primaryKey)
	}
}

func TestRepoAnalysisCacheKeyIgnoresGoLauncherMetadata(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), "package sample\n")
	initTestGitRepository(t, tmp)

	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("find go: %v", err)
	}

	originalPath := os.Getenv("PATH")
	cacheKey := func(name string) string {
		t.Helper()

		binDir := filepath.Join(t.TempDir(), name)
		goPath := filepath.Join(binDir, "go")
		writeFile(t, goPath, "#!/bin/sh\nexec \""+realGo+"\" \"$@\"\n")

		if err := os.Chmod(goPath, 0o755); err != nil {
			t.Fatalf("make go launcher executable: %v", err)
		}

		t.Setenv("PATH", binDir+string(os.PathListSeparator)+originalPath)

		key, _, err := repoAnalysisCacheKey(
			[]string{allPackagesPattern},
			tmp,
			Options{MaxStates: 32, CacheEnabled: true},
			&SimilarityOptions{CI: true, CacheEnabled: true},
		)
		if err != nil {
			t.Fatalf("%s cache key: %v", name, err)
		}

		return key
	}

	first := cacheKey("first")

	second := cacheKey("second-launcher-with-different-metadata")
	if second != first {
		t.Fatalf("launcher cache key = %q, want %q", second, first)
	}
}

func TestRepoAnalysisCacheKeyTracksArchitectureFeatures(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), "package sample\n")
	initTestGitRepository(t, tmp)
	t.Setenv("GOARCH", "amd64")

	cacheKey := func(level string) string {
		t.Helper()
		t.Setenv("GOAMD64", level)

		key, _, err := repoAnalysisCacheKey(
			[]string{allPackagesPattern},
			tmp,
			Options{MaxStates: 32, CacheEnabled: true},
			nil,
		)
		if err != nil {
			t.Fatalf("GOAMD64=%s cache key: %v", level, err)
		}

		return key
	}

	v1 := cacheKey("v1")
	v2 := cacheKey("v2")

	if v2 == v1 {
		t.Fatal("GOAMD64 feature level did not invalidate repo cache")
	}
}

func repoAnalysisSourceDigestForTest(
	dir string,
	patterns []string,
) (string, error) {
	location, err := repositoryCacheLocationForDir(dir)
	if err != nil {
		return "", err
	}

	return repoAnalysisSourceDigest(dir, patterns, "", location)
}

func TestRepoAnalysisSubmoduleSupportFollowsGoDirectoryRules(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), "package sample\n")
	head := initTestGitRepository(t, tmp)

	writeFile(
		t,
		filepath.Join(tmp, ".gitmodules"),
		"[submodule \"fixture\"]\n\tpath = .repos/fixture\n",
	)

	hiddenSubmodule := exec.Command(
		"git",
		"update-index",
		"--add",
		"--cacheinfo",
		"160000,"+head+",.repos/fixture",
	)

	hiddenSubmodule.Dir = tmp
	if output, err := hiddenSubmodule.CombinedOutput(); err != nil {
		t.Fatalf("add ignored submodule: %v: %s", err, output)
	}

	if supported, err := repoAnalysisSubmodulesSupported(tmp); err != nil || !supported {
		t.Fatalf("ignored submodule support = %t, err=%v", supported, err)
	}

	relevantSubmodule := exec.Command(
		"git",
		"update-index",
		"--add",
		"--cacheinfo",
		"160000,"+head+",deps/fixture",
	)

	relevantSubmodule.Dir = tmp
	if output, err := relevantSubmodule.CombinedOutput(); err != nil {
		t.Fatalf("add relevant submodule: %v: %s", err, output)
	}

	if supported, err := repoAnalysisSubmodulesSupported(tmp); err != nil || supported {
		t.Fatalf("relevant submodule support = %t, err=%v", supported, err)
	}
}
