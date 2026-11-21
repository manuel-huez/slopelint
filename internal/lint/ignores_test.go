package lint

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The directive above first covers the duplicate reported there. The others
// are stale or cannot be ignored, and a marker inside a string is no
// directive. The functions are unused, so dead_code findings are skipped.
const ignoredSimilaritySource = `package sample

//slopelint:ignore semantic_duplicate -- both mirror one external wire format
` + similarityTestSourceBody + `
//slopelint:ignore long_function -- kept short on purpose
func third() string {
	//slopelint:ignore comment_noise -- review findings must be fixed
	return "//slopelint:ignore dead_code -- not a directive"
}
`

func TestLintRepositoryAppliesIgnores(t *testing.T) {
	t.Setenv(codexEnv, offText)

	dir := newTestModule(t)
	path := filepath.Join(dir, similarityTestFilename)
	writeFile(t, path, ignoredSimilaritySource)

	run := func() []string {
		t.Helper()

		issues, err := LintRepository(
			t.Context(),
			[]string{allPackagesPattern},
			dir,
			Options{},
			&SimilarityOptions{
				cacheDir: t.TempDir(),
				embedder: &similarityTestEmbedder{vector: func(string) []float32 {
					return []float32{1, 0}
				}},
			},
		)
		if err != nil {
			t.Fatal(err)
		}

		var found []string

		for _, issue := range issues {
			if issue.Kind == "dead_code" {
				continue
			}

			found = append(
				found,
				issue.Kind+" "+strings.TrimPrefix(FormatIssuePosition(issue), dir),
			)
		}

		return found
	}

	want := []string{
		"stale_ignore /sample.go:36:1",
		"invalid_ignore /sample.go:38:1",
	}
	if got := run(); !slices.Equal(got, want) {
		t.Fatalf("issues = %q, want %q", got, want)
	}

	if _, err := os.Stat(filepath.Join(dir, similarityStampName)); err != nil {
		t.Fatalf("ignored duplicate should leave a stamp: %v", err)
	}

	writeFile(
		t,
		path,
		strings.Replace(
			ignoredSimilaritySource,
			"//slopelint:ignore semantic_duplicate",
			"// moved",
			1,
		),
	)

	if got := run(); !slices.Contains(got, similarityIssueKind+" /sample.go:4:1") {
		t.Fatalf("issues = %q, want the duplicate once its ignore is gone", got)
	}
}

func TestLintRepositoryIgnoresBehaviorClones(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, source string
		want         []string
	}{{
		name: "function",
		source: `package sample

func First(value int) int {
	next := value + 1
	if next > 10 { return next * 2 }
	return next
}

//slopelint:ignore behavior_clone
func MissingReason(input int) int {
	next := input + 1
	if next > 10 { return next * 2 }
	return next
}

//slopelint:ignore behavior_clone -- separate protocol boundary
func Suppressed(input int) int {
	next := input + 1
	if next > 10 { return next * 2 }
	return next
}
`,
		want: []string{"invalid_ignore /sample.go:9:1", "behavior_clone /sample.go:10:6"},
	}, {
		name: "block",
		source: `package sample

var errBad error
type request struct { name, kind, scope string; size int }

func First(value request) error {
	if value.name == "" { return errBad }
	if value.kind == "" { return errBad }
	if value.size == 0 { return errBad }
	if value.scope == "" { return errBad }
	return nil
}

func Second(value request) error {
	//slopelint:ignore behavior_clone -- validation stays at API boundary
	if value.name == "" { return errBad }
	if value.kind == "" { return errBad }
	if value.size == 0 { return errBad }
	if value.scope == "" { return errBad }
	return errBad
}
`,
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			dir := newTestModule(t)
			writeFile(t, filepath.Join(dir, "sample.go"), test.source)

			issues, err := LintRepository(
				t.Context(),
				[]string{allPackagesPattern},
				dir,
				Options{},
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}

			var got []string

			for _, issue := range issues {
				if issue.Kind == "behavior_clone" || strings.HasSuffix(issue.Kind, "_ignore") {
					got = append(
						got,
						issue.Kind+" "+strings.TrimPrefix(FormatIssuePosition(issue), dir),
					)
				}
			}

			if !slices.Equal(got, test.want) {
				t.Fatalf("issues = %q, want %q", got, test.want)
			}
		})
	}
}
