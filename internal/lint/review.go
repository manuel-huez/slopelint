package lint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/manuel-huez/slopelint/internal/lint/outline"
	smellcheck "github.com/manuel-huez/slopelint/internal/lint/smells"
)

const (
	codeReviewCommentItem = "comment"
	codeReviewErrorItem   = "dropped_error"
	codeReviewTestItem    = "test"
	codeReviewSerialItem  = "serial"
	// codeReviewTestGroupItem spans a comparison unit of tests from one file;
	// its request holds no other item.
	codeReviewTestGroupItem = "test_group"
	// codeReviewSplitHelperItem spans a private function that one caller
	// calls once. Its request holds the caller and its split helpers only.
	codeReviewSplitHelperItem = "split_helper"

	codeReviewAdviceMaxChars = 240
	codeReviewAdviceMinWords = 4
	codeReviewAdviceMaxWords = 40
)

// codeReviewPolicy names the prompt versions and model behind review records.
// Cache keys, records, and the stamp all carry it, so a change selects new
// records and stales the stamp. Bump a family's version for any change to its
// prompt text, its verdicts in the output schema, or which items it is given:
// comments, dropped or caught errors, tests, serial markers, and how tests are
// grouped into units.
type codeReviewPolicy struct {
	Go         int `json:"go,omitempty"`
	TypeScript int `json:"typescript,omitempty"`
	Python     int `json:"python,omitempty"`
	// GoTestGroups and ScriptTestGroups version the repeated-steps prompt.
	GoTestGroups     int `json:"go_test_groups,omitempty"`
	ScriptTestGroups int `json:"script_test_groups,omitempty"`
	// GoSplitHelpers and ScriptSplitHelpers version the prompt that judges
	// single-use helpers.
	GoSplitHelpers     int    `json:"go_split_helpers,omitempty"`
	ScriptSplitHelpers int    `json:"script_split_helpers,omitempty"`
	Model              string `json:"model"`
	Effort             string `json:"effort"`
}

const (
	// codeReviewLanguageVersion 3 asks to delete stale suppressions and to
	// name intended error fallbacks, and a finding needs two votes; see
	// combineFindingVotes.
	codeReviewLanguageVersion = 3
	// codeReviewTestGroupVersion 2 asks each group twice; see
	// combineTestGroupVotes.
	codeReviewTestGroupVersion = 2
	// codeReviewSplitHelperVersion 3 only advises inlining a fragment, and a
	// fragment needs two votes; see combineFindingVotes.
	codeReviewSplitHelperVersion = 3
)

var codeReviewCurrentPolicy = codeReviewPolicy{
	Go:                 codeReviewLanguageVersion,
	TypeScript:         codeReviewLanguageVersion,
	Python:             codeReviewLanguageVersion,
	GoTestGroups:       codeReviewTestGroupVersion,
	ScriptTestGroups:   codeReviewTestGroupVersion,
	GoSplitHelpers:     codeReviewSplitHelperVersion,
	ScriptSplitHelpers: codeReviewSplitHelperVersion,
	Model:              "gpt-6.1-terra",
	Effort:             "low",
}

const (
	codeReviewKeep     = "keep"
	codeReviewRestates = "restates"
	codeReviewExcuse   = "excuse"
	codeReviewCrutch   = "crutch"

	codeReviewHandled   = "handled"
	codeReviewSwallowed = "swallowed"

	codeReviewSound    = "sound"
	codeReviewTrivial  = "trivial"
	codeReviewMockOnly = "mock_only"
	codeReviewWasteful = "wasteful"

	codeReviewSerialOK     = "serial_ok"
	codeReviewSerialExcuse = "serial_excuse"

	codeReviewDistinct = "distinct"
	codeReviewRepeated = "repeated"

	codeReviewOwnJob   = "own_job"
	codeReviewFragment = "fragment"
)

var codeReviewTestGroupFixes = []string{"table", "shared_fixture"}

// codeReviewFindingKinds maps each verdict to its finding kind. Verdicts
// without a kind accept the item.
var codeReviewFindingKinds = map[string]string{
	codeReviewKeep:         "",
	codeReviewRestates:     "comment_noise",
	codeReviewExcuse:       "comment_excuse",
	codeReviewCrutch:       "comment_crutch",
	codeReviewHandled:      "",
	codeReviewSwallowed:    "swallowed_error",
	codeReviewSound:        "",
	codeReviewTrivial:      "test_trivial",
	codeReviewMockOnly:     "test_mock_only",
	codeReviewWasteful:     "test_wasteful",
	codeReviewSerialOK:     "",
	codeReviewSerialExcuse: "test_serial_excuse",
	codeReviewDistinct:     "",
	codeReviewRepeated:     "test_repeated_steps",
	codeReviewOwnJob:       "",
	codeReviewFragment:     "helper_fragment",
}

var codeReviewVerdicts = map[string][]string{
	codeReviewCommentItem: {codeReviewKeep, codeReviewRestates, codeReviewExcuse, codeReviewCrutch},
	codeReviewErrorItem:   {codeReviewHandled, codeReviewSwallowed},
	codeReviewTestItem: {
		codeReviewSound,
		codeReviewTrivial,
		codeReviewMockOnly,
		codeReviewWasteful,
	},
	codeReviewSerialItem:      {codeReviewSerialOK, codeReviewSerialExcuse},
	codeReviewTestGroupItem:   {codeReviewDistinct, codeReviewRepeated},
	codeReviewSplitHelperItem: {codeReviewOwnJob, codeReviewFragment},
}

// codeReviewer returns only records validated against their requests. done
// reports requests another run has finished since this one started.
type codeReviewer interface {
	review(
		context.Context,
		[]codeReviewRequest,
		func(codeReviewRequest) bool,
		func([]codeReviewRecord) error,
	) error
}

// codeReviewItem is one judged comment group, dropped-error check, test
// function, or test group. Lines are relative to the declaration, so moving
// it in its file keeps its cache key. Tests names a test group's tests in
// file order.
type codeReviewItem struct {
	ID    string   `json:"id"`
	Kind  string   `json:"kind"`
	Lines string   `json:"lines"`
	Error string   `json:"error,omitempty"`
	Tests []string `json:"tests,omitempty"`
	Facts string   `json:"facts,omitempty"`
}

// codeReviewRequest is one top-level declaration with numbered source lines.
// ID is the cache key. File is the base name of a TypeScript or Python file,
// which can decide a verdict, as for re-exports in __init__.py.
type codeReviewRequest struct {
	ID       string
	Location string
	Language outline.Language
	File     string
	Test     bool
	Source   string
	Items    []codeReviewItem
}

// codeReviewVerdict is one item's answer. Tests and Fix belong to a
// repeated test group: the tests to merge and how. SavedLines is the
// model's estimate of the lines the merge removes.
type codeReviewVerdict struct {
	ID         string   `json:"id"`
	Verdict    string   `json:"verdict"`
	Advice     string   `json:"advice"`
	Tests      []string `json:"tests,omitempty"`
	SavedLines int      `json:"saved_lines,omitempty"`
	Fix        string   `json:"fix,omitempty"`
}

// codeReviewRecord is one validated model answer for a declaration, as cached.
type codeReviewRecord struct {
	ID     string              `json:"id,omitempty"`
	Policy codeReviewPolicy    `json:"policy,omitzero"`
	Items  []codeReviewVerdict `json:"items"`
}

// codeReviewDeclaration is one occurrence of a request; positions follow
// request.Items, and tests follow the Tests of a test group item.
// Identical declarations in several files share one request.
type codeReviewDeclaration struct {
	request   codeReviewRequest
	positions []token.Position
	tests     []groupedTest
}

type codeReview struct {
	reviewer     codeReviewer
	policy       codeReviewPolicy
	cacheRoot    string
	cacheEnabled bool
}

func (review codeReview) run(
	ctx context.Context,
	files []string,
	candidates []Issue,
) ([]Issue, []testGroupFinding, error) {
	declarations, err := collectCodeReviewDeclarations(files, candidates, review.policy)
	if err != nil {
		return nil, nil, err
	}

	byKey := make(map[string][]codeReviewDeclaration)
	for _, declaration := range declarations {
		byKey[declaration.request.ID] = append(byKey[declaration.request.ID], declaration)
	}

	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	collector := codeReviewCollector{
		policy:       review.policy,
		requested:    make(map[string]codeReviewRequest),
		records:      make(map[string]codeReviewRecord, len(keys)),
		firstVotes:   make(map[string]codeReviewRecord),
		started:      time.Now(),
		cacheRoot:    review.cacheRoot,
		cacheEnabled: review.cacheEnabled,
	}

	var missing []codeReviewRequest

	for _, key := range keys {
		request := byKey[key][0].request
		if record, ok := review.loadRecord(request); ok {
			collector.records[key] = record
			continue
		}

		missing = append(missing, request)
		collector.requested[key] = request
	}

	if len(missing) > 0 {
		if err := review.generate(ctx, missing, &collector); err != nil {
			return nil, nil, err
		}
	}

	issues, groups := codeReviewIssues(keys, byKey, collector.records)

	return issues, groups, nil
}

func (review codeReview) generate(
	ctx context.Context,
	missing []codeReviewRequest,
	collector *codeReviewCollector,
) error {
	counts := make(map[string]int)
	for _, request := range missing {
		counts[codeReviewFamily(request)]++
	}

	var languages []string
	for _, language := range slices.Sorted(maps.Keys(counts)) {
		languages = append(languages, fmt.Sprintf("%s %d", language, counts[language]))
	}

	_, _ = fmt.Fprintf(
		os.Stderr,
		"slopelint: reviewing %d declarations (%s) with %s (%s reasoning)\n",
		len(missing),
		strings.Join(languages, ", "),
		review.policy.Model,
		review.policy.Effort,
	)

	// Test groups get a second, separate ask; see combineTestGroupVotes.
	groups := slices.DeleteFunc(slices.Clone(missing), func(request codeReviewRequest) bool {
		return request.Items[0].Kind != codeReviewTestGroupItem
	})

	done := func(request codeReviewRequest) bool {
		record, ok := review.loadRecord(request)
		if ok {
			collector.reuse(request, record)
		}

		return ok
	}

	collector.total = len(missing) + len(groups)
	for _, asked := range [][]codeReviewRequest{missing, groups} {
		if len(asked) == 0 {
			continue
		}

		if err := review.reviewer.review(ctx, asked, done, collector.accept); err != nil {
			return err
		}
	}

	if second := collector.secondVotes; len(second) > 0 {
		collector.total += len(second)
		if err := review.reviewer.review(ctx, second, done, collector.accept); err != nil {
			return err
		}
	}

	if collector.accepted != collector.total {
		return fmt.Errorf(
			"codex returned %d reviews for %d declarations",
			collector.accepted,
			collector.total,
		)
	}

	return nil
}

func (review codeReview) loadRecord(request codeReviewRequest) (codeReviewRecord, bool) {
	if !review.cacheEnabled {
		return codeReviewRecord{}, false
	}

	data, err := os.ReadFile(codeReviewRecordPath(review.cacheRoot, request.ID))
	if err != nil {
		return codeReviewRecord{}, false
	}

	var record codeReviewRecord
	if json.Unmarshal(data, &record) != nil || record.Policy != review.policy.forRequest(request) {
		return codeReviewRecord{}, false
	}

	record, err = record.validated(request)

	return record, err == nil
}

func codeReviewRecordPath(root, key string) string {
	return filepath.Join(root, "reviews", key[:2], key[2:]+".json")
}

// combineFindingVotes keeps a finding only when a second, separate answer
// gives the same verdict; otherwise the item takes its kind's accepting
// verdict. One answer flips on unchanged code too often for a finding that
// cannot be overridden.
func combineFindingVotes(
	request codeReviewRequest,
	first, second codeReviewRecord,
) codeReviewRecord {
	kinds := make(map[string]string, len(request.Items))
	for _, item := range request.Items {
		kinds[item.ID] = item.Kind
	}

	confirmed := make(map[string]string, len(second.Items))
	for _, verdict := range second.Items {
		confirmed[verdict.ID] = verdict.Verdict
	}

	for index, verdict := range first.Items {
		if codeReviewFindingKinds[verdict.Verdict] != "" &&
			confirmed[verdict.ID] != verdict.Verdict {
			first.Items[index] = codeReviewVerdict{
				ID:      verdict.ID,
				Verdict: codeReviewVerdicts[kinds[verdict.ID]][0],
			}
		}
	}

	return first
}

func recordHasFinding(record codeReviewRecord) bool {
	return slices.ContainsFunc(record.Items, func(item codeReviewVerdict) bool {
		return codeReviewFindingKinds[item.Verdict] != ""
	})
}

type codeReviewCollector struct {
	sync.Mutex
	policy       codeReviewPolicy
	requested    map[string]codeReviewRequest
	records      map[string]codeReviewRecord
	firstVotes   map[string]codeReviewRecord
	secondVotes  []codeReviewRequest
	started      time.Time
	cacheRoot    string
	cacheEnabled bool
	accepted     int
	total        int
}

// reuse takes a record another run stored while this one ran. It is final:
// its findings already had their second vote.
func (collector *codeReviewCollector) reuse(request codeReviewRequest, record codeReviewRecord) {
	collector.Lock()
	defer collector.Unlock()

	collector.records[request.ID] = record
	collector.accepted++
}

// accept stores records the reviewer already validated against their
// requests; only those reach the cache.
func (collector *codeReviewCollector) accept(records []codeReviewRecord) error {
	collector.Lock()
	defer collector.Unlock()

	for _, record := range records {
		key := record.ID
		request := collector.requested[key]
		collector.accepted++

		first, voted := collector.firstVotes[key]

		switch request.Items[0].Kind {
		case codeReviewTestGroupItem:
			if !voted {
				collector.firstVotes[key] = record
				continue
			}

			record = combineTestGroupVotes(first, record)
		default:
			if voted {
				record = combineFindingVotes(request, first, record)
			} else if recordHasFinding(record) {
				collector.firstVotes[key] = record
				collector.secondVotes = append(collector.secondVotes, request)

				continue
			}
		}

		record.ID = ""
		record.Policy = collector.policy.forRequest(request)
		collector.records[key] = record

		if !collector.cacheEnabled {
			continue
		}

		data, err := json.Marshal(record)
		if err != nil {
			return err
		}

		path := codeReviewRecordPath(collector.cacheRoot, key)
		if err := writeFileAtomically(path, data); err != nil {
			return fmt.Errorf("cache review for %s: %w", request.Location, err)
		}
	}

	_, _ = fmt.Fprintf(
		os.Stderr,
		"slopelint: reviewed %d/%d declarations%s\n",
		collector.accepted,
		collector.total,
		finishedIn(collector.accepted, collector.total, collector.started),
	)

	return nil
}

func (record codeReviewRecord) validated(request codeReviewRequest) (codeReviewRecord, error) {
	if len(record.Items) != len(request.Items) {
		return codeReviewRecord{}, fmt.Errorf(
			"returned %d verdicts for %d items",
			len(record.Items),
			len(request.Items),
		)
	}

	requested := make(map[string]codeReviewItem, len(request.Items))
	for _, item := range request.Items {
		requested[item.ID] = item
	}

	items := make([]codeReviewVerdict, len(record.Items))

	for index, verdict := range record.Items {
		item, ok := requested[verdict.ID]
		if !ok {
			return codeReviewRecord{}, fmt.Errorf("unknown or duplicate item id %q", verdict.ID)
		}

		delete(requested, verdict.ID)

		if strings.ContainsFunc(verdict.Advice, unicode.IsControl) {
			return codeReviewRecord{}, fmt.Errorf(
				"item %s: advice contains control characters",
				verdict.ID,
			)
		}

		if !slices.Contains(codeReviewVerdicts[item.Kind], verdict.Verdict) {
			return codeReviewRecord{}, fmt.Errorf(
				"item %s: verdict %q is invalid for %s",
				verdict.ID,
				verdict.Verdict,
				item.Kind,
			)
		}

		var err error
		if item.Kind == codeReviewTestGroupItem {
			if verdict, err = testGroupVerdict(verdict, item); err != nil {
				return codeReviewRecord{}, err
			}
		}

		verdict.Advice = strings.Join(strings.Fields(verdict.Advice), " ")
		if codeReviewFindingKinds[verdict.Verdict] == "" {
			verdict.Advice = ""
		} else if err := validateCodeReviewAdvice(verdict); err != nil {
			return codeReviewRecord{}, err
		}

		items[index] = verdict
	}

	record.Items = items

	return record, nil
}

func validateCodeReviewAdvice(verdict codeReviewVerdict) error {
	words := len(strings.Fields(verdict.Advice))
	if len(verdict.Advice) <= codeReviewAdviceMaxChars &&
		words >= codeReviewAdviceMinWords && words <= codeReviewAdviceMaxWords {
		return nil
	}

	return fmt.Errorf(
		"item %s: advice needs %d-%d words and at most %d characters; got %q",
		verdict.ID,
		codeReviewAdviceMinWords,
		codeReviewAdviceMaxWords,
		codeReviewAdviceMaxChars,
		verdict.Advice,
	)
}

// codeReviewIssues returns the findings of records, with test group
// findings apart so that findings of other checks on their tests can drop
// them.
func codeReviewIssues(
	keys []string,
	byKey map[string][]codeReviewDeclaration,
	records map[string]codeReviewRecord,
) ([]Issue, []testGroupFinding) {
	var (
		issues []Issue
		groups []testGroupFinding
	)

	for _, key := range keys {
		verdicts := make(map[string]codeReviewVerdict, len(records[key].Items))
		for _, verdict := range records[key].Items {
			verdicts[verdict.ID] = verdict
		}

		for _, declaration := range byKey[key] {
			for index, item := range declaration.request.Items {
				verdict := verdicts[item.ID]
				kind := codeReviewFindingKinds[verdict.Verdict]

				switch {
				case kind == "":
				case item.Kind == codeReviewTestGroupItem:
					groups = append(groups, newTestGroupFinding(kind, verdict, declaration.tests))
				default:
					issues = append(issues, Issue{
						Kind:     kind,
						Message:  verdict.Advice,
						position: declaration.positions[index],
					})
				}
			}
		}
	}

	return issues, groups
}

// splitCodeReviewCandidates separates dropped-error candidates from findings.
// Repeated-fixture findings stay findings and are also candidates: a test
// group finding over the same tests would repeat them. Candidates are
// deduplicated because test package variants repeat files.
func splitCodeReviewCandidates(issues []Issue) ([]Issue, []Issue) {
	findings := make([]Issue, 0, len(issues))
	seen := make(map[string]struct{})

	var candidates []Issue

	for _, issue := range issues {
		if issue.Kind != smellcheck.DroppedErrorCandidate &&
			issue.Kind != smellcheck.SplitHelperCandidate {
			findings = append(findings, issue)
			if issue.Kind != smellcheck.RepeatedTestFixture {
				continue
			}
		}

		position := issueFilePosition(issue)

		key := position.Filename + "\x00" + strconv.Itoa(position.Offset)
		if _, ok := seen[key]; ok {
			continue
		}

		seen[key] = struct{}{}
		issue.position = position
		candidates = append(candidates, issue)
	}

	return findings, candidates
}

func collectCodeReviewDeclarations(
	files []string,
	candidates []Issue,
	policy codeReviewPolicy,
) ([]codeReviewDeclaration, error) {
	byFile := make(map[string][]Issue)

	for _, candidate := range candidates {
		name := filepath.Clean(candidate.position.Filename)
		byFile[name] = append(byFile[name], candidate)
	}

	seen := make(map[string]struct{}, len(files))

	var declarations []codeReviewDeclaration

	for _, filename := range files {
		filename = filepath.Clean(filename)
		if _, ok := seen[filename]; ok {
			continue
		}

		seen[filename] = struct{}{}

		fileDeclarations, err := collectFileCodeReviewDeclarations(
			filename,
			byFile[filename],
			policy,
		)
		if err != nil {
			return nil, err
		}

		declarations = append(declarations, fileDeclarations...)

		delete(byFile, filename)
	}

	for filename := range byFile {
		return nil, fmt.Errorf("code review candidate in unreviewed file %s", filename)
	}

	return declarations, nil
}

func collectFileCodeReviewDeclarations(
	filename string,
	candidates []Issue,
	policy codeReviewPolicy,
) ([]codeReviewDeclaration, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	if !strings.HasSuffix(filename, ".go") {
		return collectScriptCodeReviewDeclarations(filename, content, policy)
	}

	return collectGoCodeReviewDeclarations(filename, content, candidates, policy)
}

type codeReviewPositionedItem struct {
	item     codeReviewItem
	position token.Position
}

func newCodeReviewDeclaration(
	request codeReviewRequest,
	items []codeReviewPositionedItem,
	policy codeReviewPolicy,
) (codeReviewDeclaration, error) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].position.Offset < items[j].position.Offset
	})

	declaration := codeReviewDeclaration{request: request}

	for index, positioned := range items {
		positioned.item.ID = "i" + strconv.Itoa(index+1)
		declaration.request.Items = append(declaration.request.Items, positioned.item)
		declaration.positions = append(declaration.positions, positioned.position)
	}

	var err error

	declaration.request.ID, err = codeReviewKey(policy, declaration.request)

	return declaration, err
}

type codeReviewCommentGroup struct {
	position    token.Position
	lastLine    int
	suppression bool
}

// codeReviewCommentItems returns one item per judged comment group starting
// in the declaration lines. A suppression that starts on the line after a
// judged group joins it: the reason above belongs to the suppression, so
// both are judged as one item reported at the suppression.
func codeReviewCommentItems(
	groups []codeReviewCommentGroup,
	startLine, endLine int,
) []codeReviewPositionedItem {
	var (
		items       []codeReviewPositionedItem
		first, last int
	)

	for _, group := range groups {
		position := group.position
		if position.Line < startLine || position.Line > endLine {
			continue
		}

		groupFirst := position.Line - startLine + 1
		groupLast := group.lastLine - startLine + 1

		if len(items) > 0 && groupFirst == last+1 && group.suppression {
			items = items[:len(items)-1]
			groupFirst = first
		}

		items = append(items, codeReviewPositionedItem{
			item: codeReviewItem{
				Kind:  codeReviewCommentItem,
				Lines: codeReviewLines(groupFirst, groupLast),
			},
			position: position,
		})
		first, last = groupFirst, groupLast
	}

	return items
}

func codeReviewLines(first, last int) string {
	if first == last {
		return strconv.Itoa(first)
	}

	return strconv.Itoa(first) + "-" + strconv.Itoa(last)
}

func numberedCodeReviewSource(lines []string) string {
	var out bytes.Buffer
	for index, line := range lines {
		_, _ = fmt.Fprintf(&out, "%d| %s\n", index+1, line)
	}

	return out.String()
}

// codeReviewKey hashes everything the verdicts depend on, so a prompt,
// schema, or model change selects new records instead of deleting old ones.
// forRequest keeps only the version of the request's prompt family, so one
// family's change keeps the others' records.
func (policy codeReviewPolicy) forRequest(request codeReviewRequest) codeReviewPolicy {
	family := codeReviewPolicy{Model: policy.Model, Effort: policy.Effort}

	switch {
	case request.Items[0].Kind == codeReviewTestGroupItem && request.Language == outline.Go:
		family.GoTestGroups = policy.GoTestGroups
	case request.Items[0].Kind == codeReviewTestGroupItem:
		family.ScriptTestGroups = policy.ScriptTestGroups
	case request.Items[0].Kind == codeReviewSplitHelperItem && request.Language == outline.Go:
		family.GoSplitHelpers = policy.GoSplitHelpers
	case request.Items[0].Kind == codeReviewSplitHelperItem:
		family.ScriptSplitHelpers = policy.ScriptSplitHelpers
	case request.Language == outline.Go:
		family.Go = policy.Go
	case request.Language == outline.TypeScript:
		family.TypeScript = policy.TypeScript
	default:
		family.Python = policy.Python
	}

	return family
}

func codeReviewFamily(request codeReviewRequest) string {
	switch request.Items[0].Kind {
	case codeReviewTestGroupItem:
		return string(request.Language) + " test groups"
	case codeReviewSplitHelperItem:
		return string(request.Language) + " split helpers"
	}

	return string(request.Language)
}

func codeReviewKey(policy codeReviewPolicy, request codeReviewRequest) (string, error) {
	data, err := json.Marshal(struct {
		Policy codeReviewPolicy `json:"policy"`
		File   string           `json:"file,omitempty"`
		Test   bool             `json:"test"`
		Source string           `json:"source"`
		Items  []codeReviewItem `json:"items"`
	}{policy.forRequest(request), request.File, request.Test, request.Source, request.Items})
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), nil
}

type codeReviewRun struct {
	done   chan struct{}
	issues []Issue
	groups []testGroupFinding
	err    error
}

// startCodeReview reviews files in the background once structural lint sends
// its dropped-error candidates. A failure cancels the shared semantic check.
func startCodeReview(
	ctx context.Context,
	cancel context.CancelFunc,
	files []string,
	reviewer codeReviewer,
	opts SimilarityOptions,
	candidates <-chan []Issue,
) *codeReviewRun {
	run := &codeReviewRun{done: make(chan struct{})}
	if reviewer == nil {
		close(run.done)
		return run
	}

	go func() {
		defer close(run.done)

		run.issues, run.groups, run.err = reviewModuleCode(ctx, files, reviewer, opts, candidates)
		if run.err != nil {
			cancel()
		}
	}()

	return run
}

// wait returns the review findings, with test group findings apart so that
// semantic duplicates over their tests can drop them.
func (run *codeReviewRun) wait() ([]Issue, []testGroupFinding, error) {
	<-run.done

	return run.issues, run.groups, run.err
}

func reviewModuleCode(
	ctx context.Context,
	files []string,
	reviewer codeReviewer,
	opts SimilarityOptions,
	candidates <-chan []Issue,
) ([]Issue, []testGroupFinding, error) {
	var found []Issue

	if candidates != nil {
		select {
		case received, ok := <-candidates:
			if !ok {
				return nil, nil, errors.New("structural lint failed before code review")
			}

			found = received
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}

	var fixtures []Issue

	found = slices.DeleteFunc(found, func(issue Issue) bool {
		if issue.Kind == smellcheck.RepeatedTestFixture {
			fixtures = append(fixtures, issue)
			return true
		}

		return false
	})

	cacheRoot, err := similarityVectorCacheRoot(opts.cacheDir)
	if err != nil {
		return nil, nil, err
	}

	review := codeReview{
		reviewer:     reviewer,
		policy:       codeReviewCurrentPolicy,
		cacheRoot:    cacheRoot,
		cacheEnabled: opts.CacheEnabled,
	}

	issues, groups, err := review.run(ctx, files, found)

	return issues, uncoveredTestGroups(groups, fixtures), err
}
