package lint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	smellcheck "github.com/manuel-huez/slopelint/internal/lint/smells"
)

const (
	codeReviewCommentItem = "comment"
	codeReviewErrorItem   = "dropped_error"
	codeReviewTestItem    = "test"

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
	TestSchema int    `json:"test_schema,omitempty"`
	Model      string `json:"model"`
	Effort     string `json:"effort"`
}

// codeReviewPolicyVersion must be bumped for any change to the comment,
// suppression, or dropped-error rules: their prompt text, the output schema,
// which comments are judged here, and which dropped errors the smells
// candidate pass reports.
const codeReviewPolicyVersion = 8

// codeReviewTestPolicyVersion must be bumped for any change to the test rules
// in the prompt or to which test functions are judged.
const codeReviewTestPolicyVersion = 1

var codeReviewCurrentPolicy = codeReviewPolicy{
	Schema:     codeReviewPolicyVersion,
	TestSchema: codeReviewTestPolicyVersion,
	Model:      "gpt-6.1-terra",
	Effort:     "low",
}

const (
	codeReviewKeep      = "keep"
	codeReviewRestates  = "restates"
	codeReviewExcuse    = "excuse"
	codeReviewCrutch    = "crutch"
	codeReviewHandled   = "handled"
	codeReviewSwallowed = "swallowed"
	codeReviewSound     = "sound"
	codeReviewTrivial   = "trivial"
	codeReviewMockOnly  = "mock_only"
	codeReviewWasteful  = "wasteful"
)

// codeReviewFindingKinds maps each verdict to its finding kind. Verdicts
// without a kind accept the item.
var codeReviewFindingKinds = map[string]string{
	codeReviewKeep:      "",
	codeReviewRestates:  "comment_noise",
	codeReviewExcuse:    "comment_excuse",
	codeReviewCrutch:    "comment_crutch",
	codeReviewHandled:   "",
	codeReviewSwallowed: "swallowed_error",
	codeReviewSound:     "",
	codeReviewTrivial:   "test_trivial",
	codeReviewMockOnly:  "test_mock_only",
	codeReviewWasteful:  "test_wasteful",
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
}

// codeReviewer returns only records validated against their requests.
type codeReviewer interface {
	review(context.Context, []codeReviewRequest, func([]codeReviewRecord) error) error
}

// codeReviewItem is one judged comment group, dropped-error check, or test
// function. Lines are relative to the declaration, so moving it in its file
// keeps its cache key.
type codeReviewItem struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Lines string `json:"lines"`
	Error string `json:"error,omitempty"`
}

// codeReviewRequest is one top-level declaration with numbered source lines.
// ID is the cache key.
type codeReviewRequest struct {
	ID       string
	Location string
	Test     bool
	Source   string
	Items    []codeReviewItem
}

type codeReviewVerdict struct {
	ID      string `json:"id"`
	Verdict string `json:"verdict"`
	Advice  string `json:"advice"`
}

// codeReviewRecord is one validated model answer for a declaration, as cached.
type codeReviewRecord struct {
	ID     string              `json:"id,omitempty"`
	Policy codeReviewPolicy    `json:"policy,omitzero"`
	Items  []codeReviewVerdict `json:"items"`
}

// codeReviewDeclaration is one occurrence of a request; positions follow
// request.Items. Identical declarations in several files share one request.
type codeReviewDeclaration struct {
	request   codeReviewRequest
	positions []token.Position
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
) ([]Issue, error) {
	declarations, err := collectCodeReviewDeclarations(files, candidates, review.policy)
	if err != nil {
		return nil, err
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
			return nil, err
		}
	}

	return codeReviewIssues(keys, byKey, collector.records), nil
}

func (review codeReview) generate(
	ctx context.Context,
	missing []codeReviewRequest,
	collector *codeReviewCollector,
) error {
	_, _ = fmt.Fprintf(
		os.Stderr,
		"slopelint: reviewing %d declarations with %s (%s reasoning)\n",
		len(missing),
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

	kinds := make(map[string]string, len(request.Items))
	for _, item := range request.Items {
		kinds[item.ID] = item.Kind
	}

	items := make([]codeReviewVerdict, len(record.Items))

	for index, verdict := range record.Items {
		kind, ok := kinds[verdict.ID]
		if !ok {
			return codeReviewRecord{}, fmt.Errorf("unknown or duplicate item id %q", verdict.ID)
		}

		delete(kinds, verdict.ID)

		if strings.ContainsFunc(verdict.Advice, unicode.IsControl) {
			return codeReviewRecord{}, fmt.Errorf(
				"item %s: advice contains control characters",
				verdict.ID,
			)
		}

		if !slices.Contains(codeReviewVerdicts[kind], verdict.Verdict) {
			return codeReviewRecord{}, fmt.Errorf(
				"item %s: verdict %q is invalid for %s",
				verdict.ID,
				verdict.Verdict,
				kind,
			)
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

func codeReviewIssues(
	keys []string,
	byKey map[string][]codeReviewDeclaration,
	records map[string]codeReviewRecord,
) []Issue {
	var issues []Issue

	for _, key := range keys {
		verdicts := make(map[string]codeReviewVerdict, len(records[key].Items))
		for _, verdict := range records[key].Items {
			verdicts[verdict.ID] = verdict
		}

		for _, declaration := range byKey[key] {
			for index, item := range declaration.request.Items {
				verdict := verdicts[item.ID]
				if kind := codeReviewFindingKinds[verdict.Verdict]; kind != "" {
					issues = append(issues, Issue{
						Kind:     kind,
						Message:  verdict.Advice,
						position: declaration.positions[index],
					})
				}
			}
		}
	}

	return issues
}

// splitCodeReviewCandidates separates dropped-error candidates from findings.
// Candidates are deduplicated because test package variants repeat files.
func splitCodeReviewCandidates(issues []Issue) ([]Issue, []Issue) {
	findings := make([]Issue, 0, len(issues))
	seen := make(map[string]struct{})

	var candidates []Issue

	for _, issue := range issues {
		if issue.Kind != smellcheck.DroppedErrorCandidate {
			findings = append(findings, issue)
			continue
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

	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, filename, content, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	if ast.IsGenerated(file) {
		return nil, nil
	}

	source := codeReviewSource{
		fset:       fset,
		file:       file,
		lines:      strings.Split(string(content), "\n"),
		test:       strings.HasSuffix(filename, "_test.go"),
		candidates: candidates,
		ifs:        make(map[int]*ast.IfStmt),
		policy:     policy,
	}

	ast.Inspect(file, func(node ast.Node) bool {
		if stmt, ok := node.(*ast.IfStmt); ok {
			source.ifs[fset.PositionFor(stmt.Pos(), false).Offset] = stmt
		}

		return true
	})

	for _, candidate := range candidates {
		if source.ifs[candidate.position.Offset] == nil {
			return nil, fmt.Errorf("%s changed during the scan; run slopelint again", filename)
		}
	}

	var declarations []codeReviewDeclaration

	for _, decl := range file.Decls {
		declaration, err := source.declaration(decl)
		if err != nil {
			return nil, err
		}

		if len(declaration.request.Items) > 0 {
			declarations = append(declarations, declaration)
		}
	}

	return declarations, nil
}

type codeReviewSource struct {
	fset       *token.FileSet
	file       *ast.File
	lines      []string
	test       bool
	candidates []Issue
	ifs        map[int]*ast.IfStmt
	policy     codeReviewPolicy
}

type codeReviewPositionedItem struct {
	item     codeReviewItem
	position token.Position
}

// position ignores //line directives: review reads the file's own lines.
func (source codeReviewSource) position(pos token.Pos) token.Position {
	return source.fset.PositionFor(pos, false)
}

func (source codeReviewSource) declaration(decl ast.Decl) (codeReviewDeclaration, error) {
	start, doc := decl.Pos(), declDoc(decl)
	if doc != nil {
		start = doc.Pos()
	}

	startLine := source.position(start).Line
	endLine := source.position(decl.End()).Line

	var items []codeReviewPositionedItem

	// A cgo preamble is C source, not a Go comment.
	if !importsC(decl) {
		items = source.commentItems(startLine, endLine)
	}

	items = append(items, source.droppedErrorItems(decl, startLine)...)

	if source.test && runnableGoTest(decl) {
		position := source.position(decl.Pos())
		items = append(items, codeReviewPositionedItem{
			item: codeReviewItem{
				Kind:  codeReviewTestItem,
				Lines: codeReviewLines(position.Line-startLine+1, endLine-startLine+1),
			},
			position: position,
		})
	}

	if len(items) == 0 {
		return codeReviewDeclaration{}, nil
	}

	sort.SliceStable(items, func(i, j int) bool {
		return items[i].position.Offset < items[j].position.Offset
	})

	declaration := codeReviewDeclaration{
		request: codeReviewRequest{
			Location: fmt.Sprintf("%s:%d", source.position(start).Filename, startLine),
			Test:     source.test,
			Source:   numberedCodeReviewSource(source.lines[startLine-1 : endLine]),
		},
	}

	for index, positioned := range items {
		positioned.item.ID = "i" + strconv.Itoa(index+1)
		declaration.request.Items = append(declaration.request.Items, positioned.item)
		declaration.positions = append(declaration.positions, positioned.position)
	}

	var err error

	declaration.request.ID, err = codeReviewKey(source.policy, declaration.request)

	return declaration, err
}

// commentItems returns one item per judged comment group. A suppression that
// starts on the line after a judged group joins it: the reason above belongs
// to the suppression, so both are judged as one item reported at the
// suppression.
func (source codeReviewSource) commentItems(startLine, endLine int) []codeReviewPositionedItem {
	var (
		items       []codeReviewPositionedItem
		first, last int
	)

	for _, group := range source.file.Comments {
		position := source.position(group.Pos())
		if position.Line < startLine || position.Line > endLine || !codeReviewHasItem(group) {
			continue
		}

		groupFirst := position.Line - startLine + 1
		groupLast := source.position(group.End()).Line - startLine + 1

		if len(items) > 0 && groupFirst == last+1 && codeReviewSuppression(group.List[0].Text) {
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

func (source codeReviewSource) droppedErrorItems(
	decl ast.Decl,
	startLine int,
) []codeReviewPositionedItem {
	startOffset := source.position(decl.Pos()).Offset
	endOffset := source.position(decl.End()).Offset

	var items []codeReviewPositionedItem

	for _, candidate := range source.candidates {
		offset := candidate.position.Offset
		if offset < startOffset || offset > endOffset {
			continue
		}

		ifStmt := source.ifs[offset]
		items = append(items, codeReviewPositionedItem{
			item: codeReviewItem{
				Kind: codeReviewErrorItem,
				Lines: codeReviewLines(
					source.position(ifStmt.Pos()).Line-startLine+1,
					source.position(ifStmt.End()).Line-startLine+1,
				),
				Error: candidate.Message,
			},
			position: candidate.position,
		})
	}

	return items
}

// runnableGoTest matches func TestXxx(t *testing.T) as go test names tests.
// It is syntactic, so a renamed testing import is not matched.
func runnableGoTest(decl ast.Decl) bool {
	fn, ok := decl.(*ast.FuncDecl)
	if !ok || fn.Recv != nil {
		return false
	}

	rest, ok := strings.CutPrefix(fn.Name.Name, "Test")
	if !ok || rest != "" && unicode.IsLower([]rune(rest)[0]) {
		return false
	}

	params := fn.Type.Params.List

	return len(params) == 1 && len(params[0].Names) <= 1 &&
		types.ExprString(params[0].Type) == "*testing.T"
}

func declDoc(decl ast.Decl) *ast.CommentGroup {
	switch decl := decl.(type) {
	case *ast.FuncDecl:
		return decl.Doc
	case *ast.GenDecl:
		return decl.Doc
	default:
		return nil
	}
}

func importsC(decl ast.Decl) bool {
	gen, ok := decl.(*ast.GenDecl)
	if !ok || gen.Tok != token.IMPORT {
		return false
	}

	for _, spec := range gen.Specs {
		if spec, ok := spec.(*ast.ImportSpec); ok && spec.Path.Value == `"C"` {
			return true
		}
	}

	return false
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
// forRequest returns the policy parts that decide this request's verdicts.
func (policy codeReviewPolicy) forRequest(request codeReviewRequest) codeReviewPolicy {
	if !slices.ContainsFunc(request.Items, func(item codeReviewItem) bool {
		return item.Kind == codeReviewTestItem
	}) {
		policy.TestSchema = 0
	}

	return policy
}

func codeReviewKey(policy codeReviewPolicy, request codeReviewRequest) (string, error) {
	data, err := json.Marshal(struct {
		Policy codeReviewPolicy `json:"policy"`
		Test   bool             `json:"test"`
		Source string           `json:"source"`
		Items  []codeReviewItem `json:"items"`
	}{policy.forRequest(request), request.Test, request.Source, request.Items})
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), nil
}

// codeReviewSuppressions are directives that silence a check. They are always
// judged, with or without a reason, so a bare suppression is no way around
// review.
var codeReviewSuppressions = []string{
	"nolint",
	"exhaustive:ignore",
	"slopelint:ignore",
	"lint:ignore",
	"lint:file-ignore",
}

func codeReviewHasItem(group *ast.CommentGroup) bool {
	return slices.ContainsFunc(group.List, func(comment *ast.Comment) bool {
		return codeReviewJudged(comment.Text)
	})
}

func codeReviewJudged(text string) bool {
	if body, ok := strings.CutPrefix(text, "/*"); ok {
		return strings.TrimSpace(strings.TrimSuffix(body, "*/")) != ""
	}

	if codeReviewSuppression(text) {
		return true
	}

	body := strings.TrimPrefix(text, "//")

	return strings.TrimSpace(body) != "" && !strings.HasPrefix(body, " +build") &&
		!commentDirective(body)
}

func codeReviewSuppression(text string) bool {
	body := strings.TrimPrefix(text, "//")

	return slices.ContainsFunc(codeReviewSuppressions, func(prefix string) bool {
		return strings.HasPrefix(body, prefix)
	})
}

// commentDirective matches Go's directive form: `//name:arg` with no space,
// plus the `//line` and `//export`-style forms.
func commentDirective(body string) bool {
	for _, prefix := range []string{"line ", "export ", "extern "} {
		if strings.HasPrefix(body, prefix) {
			return true
		}
	}

	name, _, ok := strings.Cut(body, ":")
	if !ok || name == "" {
		return false
	}

	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}

	return true
}

type codeReviewRun struct {
	done   chan struct{}
	issues []Issue
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

		run.issues, run.err = reviewModuleCode(ctx, files, reviewer, opts, candidates)
		if run.err != nil {
			cancel()
		}
	}()

	return run
}

func (run *codeReviewRun) wait() ([]Issue, error) {
	<-run.done

	return run.issues, run.err
}

func reviewModuleCode(
	ctx context.Context,
	files []string,
	reviewer codeReviewer,
	opts SimilarityOptions,
	candidates <-chan []Issue,
) ([]Issue, error) {
	var found []Issue

	if candidates != nil {
		select {
		case received, ok := <-candidates:
			if !ok {
				return nil, errors.New("structural lint failed before code review")
			}

			found = received
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	cacheRoot, err := similarityVectorCacheRoot(opts.cacheDir)
	if err != nil {
		return nil, err
	}

	review := codeReview{
		reviewer:     reviewer,
		policy:       codeReviewCurrentPolicy,
		cacheRoot:    cacheRoot,
		cacheEnabled: opts.CacheEnabled,
	}

	return review.run(ctx, files, found)
}
