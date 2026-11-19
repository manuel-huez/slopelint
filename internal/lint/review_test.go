package lint

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	smellcheck "github.com/manuel-huez/slopelint/internal/lint/smells"
)

const (
	reviewTestRestateAdvice = "Delete the comment because the function name already says this."
	reviewTestExcuseAdvice  = "Delete the compatibility branch and keep one implementation."
	reviewTestCrutchAdvice  = "Rename total to runningSum so the comment becomes unnecessary."
	reviewTestSwallowAdvice = "Return lostErr to callers instead of silently using zero."
	reviewTestTrivialAdvice = "Delete TestCountTrivial because it only checks its own literal."
	reviewTestSerialAdvice  = "Name the shared state TestCountFlaky races on, or call t.Parallel."
)

const reviewTestSource = `package sample

import (
	"fmt"
	"log"
	"os"
	"strconv"
)

//go:generate echo generated

// Count restates its name.
func Count(values []int) int {
	// excuse: kept for compatibility with old callers.
	total := 0
	for _, value := range values {
		total += value // crutch: adds value to total
	}

	return total
}

//nolint:gocyclo
func bare() {}

//nolint:gocyclo // generated table needs one switch
func reasoned() {}

//slopelint:ensures value != ""
func ensured(value string) {}

func parse(value string) int {
	parsed, lostErr := strconv.Atoi(value)
	if lostErr != nil {
		return 0
	}

	return parsed
}

func optional(path string) []byte {
	data, optionalErr := os.ReadFile(path)
	if optionalErr != nil {
		return nil
	}

	return data
}

func overwrite(first, second string) error {
	_, gapErr := os.Stat(first)
	if gapErr != nil {
		first = second
	}

	_, gapErr = os.Stat(first)

	return gapErr
}

func readLater(path string) error {
	_, readErr := os.Stat(path)
	if readErr != nil {
		path = ""
	}

	return readErr
}

func loopRead(paths []string) error {
	var loopErr error
	for _, path := range paths {
		if loopErr != nil {
			continue
		}

		_, loopErr = os.Stat(path)
	}

	return loopErr
}

func wrapped(path string) error {
	if _, wrapErr := os.Stat(path); wrapErr != nil {
		return fmt.Errorf("stat %s: %w", path, wrapErr)
	}

	return nil
}

func logged(path string) {
	if _, logErr := os.Stat(path); logErr != nil {
		log.Printf("stat: %v", logErr)
	}
}

func exists(path string) bool {
	if _, boolErr := os.Stat(path); boolErr != nil {
		return false
	}

	return true
}

func named(path string) (namedErr error) {
	if _, namedErr = os.Stat(path); namedErr != nil {
		return
	}

	return nil
}

func must(path string) {
	if _, panicErr := os.Stat(path); panicErr != nil {
		panic("missing " + path)
	}
}

func loopOuter(paths []string) {
	var outerLoopErr error
	for _, path := range paths {
		if outerLoopErr != nil {
			path = ""
		}

		_, outerLoopErr = os.Stat(path)
	}
}

func captured(path string) func() error {
	_, capturedErr := os.Stat(path)
	if capturedErr != nil {
		path = ""
	}

	_, capturedErr = os.Stat(path)

	return func() error { return capturedErr }
}

func addressed(path string) {
	_, addressedErr := os.Stat(path)
	if addressedErr != nil {
		path = ""
	}

	_, addressedErr = os.Stat(path)
	keepError(&addressedErr)
}

func keepError(*error) {}

func outerInLiteral(path string) {
	_, outerErr := os.Stat(path)
	check := func() {
		if outerErr != nil {
			return
		}
	}
	check()
}

func background(path string) {
	go func() {
		if _, litErr := os.Stat(path); litErr != nil {
			return
		}
	}()
}

func kind(op string) int {
	//exhaustive:ignore
	switch op {
	case "a":
		return 1
	}

	return 0
}
`

const reviewTestTests = `package sample

import "testing"

func TestMain(m *testing.M) { m.Run() }

func TestCount(t *testing.T) {
	if got := Count([]int{1, 2}); got != 3 {
		t.Fatalf("Count = %d", got)
	}
}

func TestCountTrivial(t *testing.T) {
	values := []int{1}
	if len(values) != 1 {
		t.Fatal(values)
	}
}

func BenchmarkCount(b *testing.B) { Count(nil) }

func Testcount(t *testing.T) { TestCount(t) }

// TestCountFlaky keeps one count.
//slopelint:serial excuse: flaky
func TestCountFlaky(t *testing.T) { TestCount(t) }
`

// reviewTestModel answers from markers: comment text names its verdict,
// errors named lostErr are swallowed, only TestCountTrivial is trivial, and
// only a flaky serial reason is an excuse.
type reviewTestModel struct {
	sync.Mutex
	requests []codeReviewRequest
	verdict  func(codeReviewItem, string) codeReviewVerdict
}

func (model *reviewTestModel) review(
	_ context.Context,
	requests []codeReviewRequest,
	accept func([]codeReviewRecord) error,
) error {
	model.Lock()
	model.requests = append(model.requests, requests...)
	model.Unlock()

	records := make([]codeReviewRecord, len(requests))
	for index, request := range requests {
		records[index].ID = request.ID
		for _, item := range request.Items {
			verdict := reviewTestVerdict(item, reviewTestItemText(request, item))
			if model.verdict != nil {
				verdict = model.verdict(item, reviewTestItemText(request, item))
			}

			verdict.ID = item.ID
			records[index].Items = append(records[index].Items, verdict)
		}
	}

	return accept(records)
}

func (model *reviewTestModel) requestCount() int {
	model.Lock()
	defer model.Unlock()

	return len(model.requests)
}

func reviewTestVerdict(item codeReviewItem, text string) codeReviewVerdict {
	for _, finding := range []struct{ kind, marker, verdict, advice string }{
		{codeReviewTestItem, "TestCountTrivial", codeReviewTrivial, reviewTestTrivialAdvice},
		{codeReviewSerialItem, "flaky", codeReviewSerialExcuse, reviewTestSerialAdvice},
		{codeReviewErrorItem, "lostErr", codeReviewSwallowed, reviewTestSwallowAdvice},
		{codeReviewCommentItem, codeReviewRestates, codeReviewRestates, reviewTestRestateAdvice},
		{codeReviewCommentItem, codeReviewExcuse, codeReviewExcuse, reviewTestExcuseAdvice},
		{codeReviewCommentItem, codeReviewCrutch, codeReviewCrutch, reviewTestCrutchAdvice},
	} {
		if item.Kind == finding.kind && strings.Contains(text, finding.marker) {
			return codeReviewVerdict{Verdict: finding.verdict, Advice: finding.advice}
		}
	}

	return keepAllForTest(item, text)
}

func reviewTestItemText(request codeReviewRequest, item codeReviewItem) string {
	first, last, ranged := strings.Cut(item.Lines, "-")
	if !ranged {
		last = first
	}

	start, _ := strconv.Atoi(first)
	end, _ := strconv.Atoi(last)
	lines := strings.Split(request.Source, "\n")

	return strings.Join(lines[start-1:end], "\n")
}

func newReviewTestModule(t *testing.T) (string, string) {
	t.Helper()

	dir := newTestModule(t)
	sourcePath := filepath.Join(dir, "sample.go")
	writeFile(t, sourcePath, reviewTestSource)
	writeFile(t, filepath.Join(dir, "sample_test.go"), reviewTestTests)

	return dir, sourcePath
}

// reviewTestOptions uses fake models; a nil model leaves Codex discovery to
// the environment.
func reviewTestOptions(cacheDir string, model *reviewTestModel) *SimilarityOptions {
	options := &SimilarityOptions{
		CacheEnabled: true,
		cacheDir:     cacheDir,
		embedder: &similarityTestEmbedder{
			vector: func(string) []float32 { return []float32{1, 0} },
		},
	}
	if model != nil {
		options.describer = new(similarityTestDescriber)
		options.reviewer = model
	}

	return options
}

func lintWithReviewForTest(
	t *testing.T,
	dir string,
	cacheDir string,
	model *reviewTestModel,
) ([]Issue, error) {
	t.Helper()

	// Fake models need the Codex channel on, whatever the host exports.
	if model != nil {
		t.Setenv(codexEnv, "auto")
	}

	return LintRepository(
		t.Context(),
		[]string{allPackagesPattern},
		dir,
		Options{CacheEnabled: true, cacheDir: cacheDir},
		reviewTestOptions(cacheDir, model),
	)
}

func reviewIssueSummary(issues []Issue) []string {
	var out []string

	for _, issue := range issues {
		switch issue.Kind {
		case "comment_noise",
			"comment_excuse",
			"comment_crutch",
			codeReviewFindingKinds[codeReviewSwallowed],
			codeReviewFindingKinds[codeReviewTrivial],
			codeReviewFindingKinds[codeReviewSerialExcuse]:
			out = append(
				out,
				strconv.Itoa(issuePosition(issue).Line)+" "+issue.Kind+": "+issue.Message,
			)
		}
	}

	return out
}

func TestCodeReviewVerdictsBecomeFindings(t *testing.T) {
	tmp, _ := newReviewTestModule(t)

	model := new(reviewTestModel)

	issues, err := lintWithReviewForTest(t, tmp, t.TempDir(), model)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"12 comment_noise: " + reviewTestRestateAdvice,
		"14 comment_excuse: " + reviewTestExcuseAdvice,
		"17 comment_crutch: " + reviewTestCrutchAdvice,
		"34 swallowed_error: " + reviewTestSwallowAdvice,
		"13 test_trivial: " + reviewTestTrivialAdvice,
		"25 test_serial_excuse: " + reviewTestSerialAdvice,
	}
	if got := reviewIssueSummary(issues); !slices.Equal(got, want) {
		t.Fatalf(
			"review findings:\n%s\nwant:\n%s",
			strings.Join(got, "\n"),
			strings.Join(want, "\n"),
		)
	}

	items := make(map[string][]string)

	for _, request := range model.requests {
		for _, item := range request.Items {
			value := item.Error
			if value == "" {
				line, _, _ := strings.Cut(reviewTestItemText(request, item), "\n")
				_, value, _ = strings.Cut(line, "| ")
			}

			items[item.Kind] = append(items[item.Kind], strings.TrimSpace(value))
		}
	}

	errs, comments, tests := items[codeReviewErrorItem], items[codeReviewCommentItem], items[codeReviewTestItem]
	slices.Sort(errs)
	slices.Sort(tests)

	// Every runnable test is sent, also one without comments; TestMain,
	// benchmarks, and lower-case names are not tests. A serial marker is its
	// own item; the findings above fail if a comment item also covers it.
	if want := []string{
		"func TestCount(t *testing.T) {",
		"func TestCountFlaky(t *testing.T) { TestCount(t) }",
		"func TestCountTrivial(t *testing.T) {",
		"//slopelint:serial excuse: flaky",
	}; !slices.Equal(append(tests, items[codeReviewSerialItem]...), want) {
		t.Fatalf("test items = %q, want %q", tests, want)
	}

	// A plain overwrite ends the dropped value and a closure owns its own
	// errors. Later reads, named results, outer variables, loop-carried errors,
	// captures, and taken addresses keep the value alive.
	want = []string{"gapErr", "litErr", "lostErr", "optionalErr"}
	if !slices.Equal(errs, want) {
		t.Fatalf("dropped-error items = %v, want %v", errs, want)
	}

	if sent := strings.Join(comments, "\n"); strings.Contains(sent, "go:generate") ||
		strings.Contains(sent, "slopelint:ensures") {
		t.Fatalf("directive was sent for review: %q", comments)
	}

	for _, suppression := range []string{
		"//nolint:gocyclo",
		"//nolint:gocyclo // generated table needs one switch",
		"//exhaustive:ignore",
	} {
		if !slices.Contains(comments, suppression) {
			t.Fatalf("suppression %q was not sent for review: %q", suppression, comments)
		}
	}
}

func TestCodeReviewWithoutModelReportsNoCandidates(t *testing.T) {
	t.Parallel()

	tmp, _ := newReviewTestModule(t)

	issues, err := LintRepository(
		t.Context(),
		[]string{allPackagesPattern},
		tmp,
		Options{CacheEnabled: true, cacheDir: t.TempDir()},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, issue := range issues {
		if issue.Kind == codeReviewFindingKinds[codeReviewSwallowed] ||
			issue.Kind == smellcheck.DroppedErrorCandidate {
			t.Fatalf("unexpected %s without the model: %s", issue.Kind, issue.Message)
		}
	}
}

func TestCodeReviewCachesVerdictsPerDeclaration(t *testing.T) {
	tmp, sourcePath := newReviewTestModule(t)
	cacheDir := t.TempDir()

	model := new(reviewTestModel)

	first, err := lintWithReviewForTest(t, tmp, cacheDir, model)
	if err != nil {
		t.Fatal(err)
	}

	cold := model.requestCount()

	if _, err := os.Stat(filepath.Join(tmp, similarityStampName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("review findings still wrote a stamp: %v", err)
	}

	second, err := lintWithReviewForTest(t, tmp, cacheDir, model)
	if err != nil {
		t.Fatal(err)
	}

	if model.requestCount() != cold {
		t.Fatalf("unchanged scan asked %d more declarations", model.requestCount()-cold)
	}

	if !slices.Equal(reviewIssueSummary(first), reviewIssueSummary(second)) {
		t.Fatalf(
			"cached findings differ:\n%v\n%v",
			reviewIssueSummary(first),
			reviewIssueSummary(second),
		)
	}

	// Moving declarations keeps their keys: cached verdicts report at the new lines.
	writeFile(
		t,
		sourcePath,
		strings.Replace(reviewTestSource, "//go:generate", "var shifted = 1\n\n//go:generate", 1),
	)

	moved, err := lintWithReviewForTest(t, tmp, cacheDir, model)
	if err != nil {
		t.Fatal(err)
	}

	if model.requestCount() != cold {
		t.Fatalf("moved declarations asked %d more declarations", model.requestCount()-cold)
	}

	if got := reviewIssueSummary(
		moved,
	); !slices.Contains(
		got,
		"36 swallowed_error: "+reviewTestSwallowAdvice,
	) {
		t.Fatalf("moved findings = %v, want swallowed_error at line 36", got)
	}

	writeFile(t, sourcePath, strings.Replace(
		reviewTestSource,
		"// excuse: kept for compatibility with old callers.",
		"\n// excuse: kept for compatibility with older callers.",
		1,
	))

	if _, err := lintWithReviewForTest(t, tmp, cacheDir, model); err != nil {
		t.Fatal(err)
	}

	if asked := model.requests[cold:]; len(asked) != 1 ||
		!strings.Contains(asked[0].Source, "Count") {
		t.Fatalf("changed scan asked %d declarations, want only Count", len(asked))
	}
}

func TestCodeReviewPolicyChangeSelectsNewRecords(t *testing.T) {
	tmp, sourcePath := newReviewTestModule(t)
	cacheDir := t.TempDir()

	model := new(reviewTestModel)
	if _, err := lintWithReviewForTest(t, tmp, cacheDir, model); err != nil {
		t.Fatal(err)
	}

	cold := model.requestCount()

	cacheRoot, err := similarityVectorCacheRoot(cacheDir)
	if err != nil {
		t.Fatal(err)
	}

	before := model.requestCount()
	bumped := codeReviewCurrentPolicy
	bumped.Go++

	review := codeReview{reviewer: model, policy: bumped, cacheRoot: cacheRoot, cacheEnabled: true}
	if _, _, err := review.run(t.Context(), []string{sourcePath}, nil); err != nil {
		t.Fatal(err)
	}

	// Without candidates only Count, bare, reasoned, and kind own items.
	if model.requestCount()-before != 4 {
		t.Fatalf("new prompt version asked %d declarations, want 4", model.requestCount()-before)
	}

	records, err := filepath.Glob(filepath.Join(cacheRoot, "reviews", "*", "*.json"))
	if err != nil || len(records) != cold+4 {
		t.Fatalf("records = %d, want %d kept by key, err=%v", len(records), cold+4, err)
	}
}

func installReviewCodexForTest(t *testing.T, model *reviewTestModel) *atomic.Int32 {
	t.Helper()

	var calls atomic.Int32

	cli := codexCLIForTest(t, func(prompt string) (any, error) {
		_, encoded, ok := strings.Cut(prompt, codeReviewInputMarker)
		if !ok {
			return nil, errors.New("unexpected non-review prompt")
		}

		var inputs []codeReviewCodexInput
		if err := json.Unmarshal([]byte(encoded), &inputs); err != nil {
			return nil, err
		}

		calls.Add(1)

		requests := make([]codeReviewRequest, len(inputs))
		for index, input := range inputs {
			requests[index] = codeReviewRequest{
				ID:     input.ID,
				Source: input.Source,
				Items:  input.Items,
			}
		}

		var response codeReviewCodexResponse

		err := model.review(t.Context(), requests, func(records []codeReviewRecord) error {
			response.Declarations = records
			return nil
		})

		return response, err
	})

	t.Setenv("PATH", filepath.Dir(cli.path)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(codexEnv, "auto")

	return &calls
}

func lintWithCodexForTest(t *testing.T, dir string, opts Options) ([]Issue, error) {
	t.Helper()

	return LintRepository(
		t.Context(),
		[]string{allPackagesPattern},
		dir,
		opts,
		&SimilarityOptions{CacheEnabled: opts.CacheEnabled, cacheDir: opts.cacheDir},
	)
}

func TestCodeReviewRejectsInvalidModelOutput(t *testing.T) {
	for _, test := range []struct {
		name    string
		verdict func(codeReviewItem, string) codeReviewVerdict
		want    string
	}{
		{
			name: "verdict for another kind",
			verdict: func(item codeReviewItem, _ string) codeReviewVerdict {
				if item.Kind == codeReviewErrorItem {
					return codeReviewVerdict{Verdict: codeReviewRestates, Advice: reviewTestRestateAdvice}
				}

				return keepAllForTest(item, "")
			},
			want: `verdict "restates" is invalid for dropped_error`,
		},
		{
			name: "comment verdict for a test",
			verdict: func(item codeReviewItem, _ string) codeReviewVerdict {
				if item.Kind == codeReviewTestItem {
					return codeReviewVerdict{Verdict: codeReviewKeep}
				}

				return keepAllForTest(item, "")
			},
			want: `verdict "keep" is invalid for test`,
		},
		{
			name: "terminal escape in advice",
			verdict: func(item codeReviewItem, text string) codeReviewVerdict {
				verdict := reviewTestVerdict(item, text)
				verdict.Advice += "\x1b[31m"

				return verdict
			},
			want: "advice contains control characters",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tmp, _ := newReviewTestModule(t)
			installReviewCodexForTest(t, &reviewTestModel{verdict: test.verdict})

			cacheDir := t.TempDir()

			_, err := lintWithCodexForTest(t, tmp, Options{CacheEnabled: true, cacheDir: cacheDir})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid output error = %v, want %q", err, test.want)
			}

			if _, statErr := os.Stat(
				filepath.Join(tmp, similarityStampName),
			); !errors.Is(
				statErr,
				os.ErrNotExist,
			) {
				t.Fatalf("failed review wrote a stamp: %v", statErr)
			}
		})
	}
}

func TestCodeReviewFindingsReplayFromRepositoryCache(t *testing.T) {
	tmp, _ := newReviewTestModule(t)
	calls := installReviewCodexForTest(t, new(reviewTestModel))

	var hits []string

	opts := Options{
		CacheEnabled: true,
		cacheDir:     t.TempDir(),
		CacheHitHook: func(name string) { hits = append(hits, name) },
	}

	first, err := lintWithCodexForTest(t, tmp, opts)
	if err != nil {
		t.Fatal(err)
	}

	cold := calls.Load()
	hits = nil

	second, err := lintWithCodexForTest(t, tmp, opts)
	if err != nil {
		t.Fatal(err)
	}

	if calls.Load() != cold || !slices.Equal(hits, []string{repoAnalysisCacheHitName}) {
		t.Fatalf("second scan: %d more model calls, cache hits %v", calls.Load()-cold, hits)
	}

	if got := reviewIssueSummary(
		second,
	); len(got) == 0 ||
		!slices.Equal(got, reviewIssueSummary(first)) {
		t.Fatalf("replayed review findings = %v, want %v", got, reviewIssueSummary(first))
	}
}

func TestCodeReviewUsesFileLinesUnderLineDirectives(t *testing.T) {
	tmp := newTestModule(t)
	sourcePath := filepath.Join(tmp, "sample.go")
	writeFile(t, sourcePath, `package sample

import "strconv"

//line generated.tmpl:1000
// first restates its name.
func first(value string) int {
	parsed, lostErr := strconv.Atoi(value)
	if lostErr != nil {
		return 0
	}

	return parsed
}
`)

	issues, err := lintWithReviewForTest(t, tmp, t.TempDir(), new(reviewTestModel))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"5 comment_noise: " + reviewTestRestateAdvice,
		"9 swallowed_error: " + reviewTestSwallowAdvice,
	}
	if got := reviewIssueSummary(issues); !slices.Equal(got, want) {
		t.Fatalf("findings = %v, want %v", got, want)
	}

	for _, issue := range issues {
		if strings.HasPrefix(issue.Kind, "comment_") ||
			issue.Kind == codeReviewFindingKinds[codeReviewSwallowed] {
			if issuePosition(issue).Filename != sourcePath {
				t.Fatalf("finding in %s, want %s", issuePosition(issue).Filename, sourcePath)
			}
		}
	}
}

func TestCodeReviewCodexRetriesThenFailsOnInvalidBatch(t *testing.T) {
	var attempts atomic.Int32

	cli := codexCLIForTest(t, func(string) (any, error) {
		attempts.Add(1)

		return codeReviewCodexResponse{Declarations: []codeReviewRecord{{
			ID:    codexBatchID(0),
			Items: []codeReviewVerdict{{ID: "i1", Verdict: codeReviewExcuse, Advice: "Fix."}},
		}}}, nil
	})

	request := codeReviewRequest{
		ID:       strings.Repeat("a", 64),
		Location: "sample.go:3",
		Source:   "1| // excuse\n2| func f() {}\n",
		Items:    []codeReviewItem{{ID: "i1", Kind: codeReviewCommentItem, Lines: "1"}},
	}

	err := cli.review(t.Context(), []codeReviewRequest{request}, func([]codeReviewRecord) error {
		t.Error("invalid record accepted")
		return nil
	})
	if err == nil || attempts.Load() != codexAttempts ||
		!strings.Contains(
			err.Error(),
			"codex review batch 1/1: review sample.go:3: item i1: advice needs",
		) {
		t.Fatalf("attempts=%d, err=%v", attempts.Load(), err)
	}
}

func TestCodeReviewStampAndCI(t *testing.T) {
	for _, test := range []struct {
		name     string
		codex    string
		model    *reviewTestModel
		reviewed bool
	}{
		{name: "review on", model: &reviewTestModel{verdict: keepAllForTest}, reviewed: true},
		{name: "review off", codex: offText},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(codexEnv, test.codex)

			tmp, _ := newReviewTestModule(t)
			initTestGitRepository(t, tmp)

			if _, err := lintWithReviewForTest(t, tmp, t.TempDir(), test.model); err != nil {
				t.Fatal(err)
			}

			stamp, err := loadSimilarityStamp(tmp)
			if err != nil || stamp.Schema == 0 || (stamp.Review != nil) != test.reviewed {
				t.Fatalf(
					"stamp review = %v, want reviewed=%v, err=%v",
					stamp.Review,
					test.reviewed,
					err,
				)
			}

			requireReviewCI(t, tmp, test.reviewed)
		})
	}
}

func requireReviewCI(t *testing.T, dir string, reviewed bool) {
	t.Helper()

	ci := func() error {
		_, err := LintRepository(
			t.Context(), []string{allPackagesPattern}, dir,
			Options{CacheEnabled: true, cacheDir: t.TempDir()},
			&SimilarityOptions{CI: true, CacheEnabled: true},
		)

		return err
	}

	t.Setenv(codexEnv, "auto")

	err := ci()
	if reviewed && err != nil ||
		!reviewed && (err == nil || !strings.Contains(err.Error(), "lacks code review")) {
		t.Fatalf("CI with Codex auto: %v", err)
	}

	t.Setenv(codexEnv, offText)

	if err := ci(); err != nil {
		t.Fatalf("CI with Codex off: %v", err)
	}
}

func keepAllForTest(item codeReviewItem, _ string) codeReviewVerdict {
	switch item.Kind {
	case codeReviewErrorItem:
		return codeReviewVerdict{Verdict: codeReviewHandled}
	case codeReviewTestItem:
		return codeReviewVerdict{Verdict: codeReviewSound}
	case codeReviewSerialItem:
		return codeReviewVerdict{Verdict: codeReviewSerialOK}
	case codeReviewTestGroupItem:
		return codeReviewVerdict{Verdict: codeReviewDistinct}
	}

	return codeReviewVerdict{Verdict: codeReviewKeep}
}

func TestCodeReviewJoinsSuppressionWithReasonAbove(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "os"

func skip(path string) error {
	if _, err := os.Stat(path); err != nil {
		// An unreadable file is skipped, not fatal.
		return nil //nolint:nilerr
	}

	// first note
	_ = path // second note

	//nolint:gocyclo // trailing reason
	return nil
}
`)

	model := &reviewTestModel{verdict: func(item codeReviewItem, text string) codeReviewVerdict {
		if item.Kind == codeReviewCommentItem && strings.Contains(text, "nolint:nilerr") {
			return codeReviewVerdict{Verdict: codeReviewExcuse, Advice: reviewTestExcuseAdvice}
		}

		return keepAllForTest(item, text)
	}}

	issues, err := lintWithReviewForTest(t, tmp, t.TempDir(), model)
	if err != nil {
		t.Fatal(err)
	}

	var comments []string

	for _, item := range model.requests[0].Items {
		if item.Kind == codeReviewCommentItem {
			comments = append(comments, item.Lines)
		}
	}

	if want := []string{"3-4", "7", "8", "10"}; !slices.Equal(comments, want) {
		t.Fatalf("comment items = %v, want %v: a reason joins only its suppression", comments, want)
	}

	if got := reviewIssueSummary(
		issues,
	); !slices.Equal(
		got,
		[]string{"8 comment_excuse: " + reviewTestExcuseAdvice},
	) {
		t.Fatalf("findings = %v, want the joined item reported at the suppression", got)
	}
}
