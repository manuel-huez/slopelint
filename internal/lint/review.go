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

	codeReviewAdviceMaxChars = 240
	codeReviewAdviceMinWords = 4
	codeReviewAdviceMaxWords = 40
)

// codeReviewPolicy names the prompt version and model behind review records.
// Cache keys, records, and the stamp all carry it, so a change selects new
// records and stales the stamp.
type codeReviewPolicy struct {
	Schema int `json:"schema"`
	// TestSchema versions the test-function rules. Requests without a test
	// item omit it, so changing those rules keeps comment and dropped-error
	// records.
	TestSchema int `json:"test_schema,omitempty"`
	// TypeScriptSchema and PythonSchema version those languages' comment and
	// suppression rules. Each request keeps only its own language's version,
	// so Go keys stay as they were and one language's change keeps the
	// others' records.
	TypeScriptSchema  int `json:"typescript_schema,omitempty"`
	PythonSchema      int `json:"python_schema,omitempty"`
	ScriptErrorSchema int `json:"script_error_schema,omitempty"`
	ScriptTestSchema  int `json:"script_test_schema,omitempty"`
	// TestGroupSchema and ScriptTestGroupSchema version the repeated-steps
	// rules; only test group requests carry them, and those carry no other
	// version.
	TestGroupSchema       int    `json:"test_group_schema,omitempty"`
	ScriptTestGroupSchema int    `json:"script_test_group_schema,omitempty"`
	Model                 string `json:"model"`
	Effort                string `json:"effort"`
}

// codeReviewPolicyVersion must be bumped for any change to the comment,
// suppression, or dropped-error rules: their prompt text, their verdicts in
// the output schema, which comments are judged here, and which dropped errors
// the smells candidate pass reports.
const codeReviewPolicyVersion = 8

// codeReviewTestPolicyVersion must be bumped for any change to the test and
// serial-marker rules in the prompt or to which test functions are judged.
const codeReviewTestPolicyVersion = 2

// codeReviewScriptPolicyVersions must be bumped for any change to that
// language's prompt text, or to which comments and declaration lines the
// outline gives it.
const (
	codeReviewTypeScriptPolicyVersion = 4
	codeReviewPythonPolicyVersion     = 4
)

// codeReviewScriptErrorPolicyVersion must be bumped for any change to the
// caught-error rules in the script prompt or to which handlers the outline
// reports.
const codeReviewScriptErrorPolicyVersion = 3

// codeReviewScriptTestPolicyVersion must be bumped for any change to the test
// rules in the script prompt or to which tests and context lines the outline
// gives it.
const codeReviewScriptTestPolicyVersion = 3

// codeReviewTestGroupPolicyVersions must be bumped for any change to the
// repeated-steps prompt of that language or to how tests are grouped into
// units.
const (
	codeReviewTestGroupPolicyVersion       = 1
	codeReviewScriptTestGroupPolicyVersion = 1
)

var codeReviewCurrentPolicy = codeReviewPolicy{
	Schema:                codeReviewPolicyVersion,
	TestSchema:            codeReviewTestPolicyVersion,
	TypeScriptSchema:      codeReviewTypeScriptPolicyVersion,
	PythonSchema:          codeReviewPythonPolicyVersion,
	ScriptErrorSchema:     codeReviewScriptErrorPolicyVersion,
	ScriptTestSchema:      codeReviewScriptTestPolicyVersion,
	TestGroupSchema:       codeReviewTestGroupPolicyVersion,
	ScriptTestGroupSchema: codeReviewScriptTestGroupPolicyVersion,
	Model:                 "gpt-6.1-terra",
	Effort:                "low",
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
	codeReviewSerialItem:    {codeReviewSerialOK, codeReviewSerialExcuse},
	codeReviewTestGroupItem: {codeReviewDistinct, codeReviewRepeated},
}

// codeReviewer returns only records validated against their requests.
type codeReviewer interface {
	review(context.Context, []codeReviewRequest, func([]codeReviewRecord) error) error
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

	collector.total = len(missing)
	if err := review.reviewer.review(ctx, missing, collector.accept); err != nil {
		return err
	}

	if collector.accepted != len(missing) {
		return fmt.Errorf(
			"codex returned %d reviews for %d declarations",
			collector.accepted,
			len(missing),
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

type codeReviewCollector struct {
	sync.Mutex
	policy       codeReviewPolicy
	requested    map[string]codeReviewRequest
	records      map[string]codeReviewRecord
	cacheRoot    string
	cacheEnabled bool
	accepted     int
	total        int
}

// accept stores records the reviewer already validated against their
// requests; only those reach the cache.
func (collector *codeReviewCollector) accept(records []codeReviewRecord) error {
	collector.Lock()
	defer collector.Unlock()

	for _, record := range records {
		key := record.ID
		record.ID = ""
		record.Policy = collector.policy.forRequest(collector.requested[key])
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
			return fmt.Errorf("cache review for %s: %w", collector.requested[key].Location, err)
		}
	}

	collector.accepted += len(records)
	_, _ = fmt.Fprintf(
		os.Stderr,
		"slopelint: reviewed %d/%d declarations\n",
		collector.accepted,
		collector.total,
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
		if issue.Kind != smellcheck.DroppedErrorCandidate {
			findings = append(findings, issue)
			if issue.Kind != smellcheck.RepeatedTestFixture {
				continue
			}
		}

		// Review matches candidates against the file itself, so it needs
		// positions that ignore //line directives.
		position := issuePosition(issue)
		if issue.fset != nil {
			position = issue.fset.PositionFor(issue.Pos, false)
		}

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
		return nil, fmt.Errorf("dropped-error candidate in unreviewed file %s", filename)
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
// forRequest returns the policy parts that decide this request's verdicts:
// each version stays only for requests of its languages that hold one of
// its item kinds.
func (policy codeReviewPolicy) forRequest(request codeReviewRequest) codeReviewPolicy {
	goOnly := []outline.Language{outline.Go}
	scripts := []outline.Language{outline.TypeScript, outline.Python}
	group := []string{codeReviewTestGroupItem}
	test := []string{codeReviewTestItem}
	comment := []string{codeReviewCommentItem}

	for _, scope := range []struct {
		version   *int
		languages []outline.Language
		kinds     []string
	}{
		{&policy.Schema, goOnly, []string{
			codeReviewCommentItem, codeReviewErrorItem, codeReviewTestItem, codeReviewSerialItem,
		}},
		{&policy.TestSchema, goOnly, test},
		{&policy.TestGroupSchema, goOnly, group},
		{&policy.ScriptErrorSchema, scripts, []string{codeReviewErrorItem}},
		{&policy.ScriptTestSchema, scripts, test},
		{&policy.ScriptTestGroupSchema, scripts, group},
		{&policy.TypeScriptSchema, []outline.Language{outline.TypeScript}, comment},
		{&policy.PythonSchema, []outline.Language{outline.Python}, comment},
	} {
		if !slices.Contains(scope.languages, request.Language) ||
			!slices.ContainsFunc(request.Items, func(item codeReviewItem) bool {
				return slices.Contains(scope.kinds, item.Kind)
			}) {
			*scope.version = 0
		}
	}

	return policy
}

// codeReviewFamily names the prompt a request needs: one per language, and
// one per language for test groups.
func codeReviewFamily(request codeReviewRequest) string {
	if request.Items[0].Kind == codeReviewTestGroupItem {
		return string(request.Language) + " test groups"
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
