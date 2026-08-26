package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

const (
	similarityTestPurpose      = "Returns normalized active names in stable sorted order."
	similarityPurposeJSONField = `"purpose"`
)

type similarityTestDescriber struct {
	calls    int
	requests int
}

func (describer *similarityTestDescriber) describe(
	requests []similarityDescriptionRequest,
	accept func([]similarityDescription) error,
) error {
	describer.calls++
	describer.requests += len(requests)

	out := make([]similarityDescription, len(requests))
	for index, request := range requests {
		out[index] = similarityDescriptionForTest(request)
	}

	return accept(out)
}

func similarityDescriptionForTest(request similarityDescriptionRequest) similarityDescription {
	if request.Detail {
		if request.Kind == similarityDescriptionTest {
			return similarityDescription{
				ID:         request.ID,
				Subject:    "blank-name validation",
				Scenario:   "input contains only whitespace",
				Setup:      []string{"provide one whitespace-only name"},
				Action:     []string{"collect active names"},
				Assertions: []string{"an error is returned"},
				Contract:   "Whitespace-only names always return an error before any result.",
			}
		}

		return similarityDescription{
			ID:         request.ID,
			Purpose:    similarityTestPurpose,
			Inputs:     []string{"caller-provided row collection"},
			Outputs:    []string{"sorted normalized names"},
			Processing: []string{"filter disabled rows", "normalize names", "sort accepted names"},
		}
	}

	if request.Kind == similarityDescriptionTest {
		return similarityDescription{
			ID:                request.ID,
			ContractSignature: "Whitespace-only names produce an error before returning any result.",
			ScenarioSignature: "One supplied name contains only whitespace characters.",
			OracleSignature:   "Collecting active names returns an error for whitespace-only input.",
		}
	}

	if strings.Contains(request.Content, "func third") {
		return similarityDescription{
			ID:                request.ID,
			IntentSignature:   "Aggregates indexed numeric values into one deterministic total result.",
			FlowSignature:     "Caller numbers pass indexed arithmetic branches and produce one total.",
			BoundarySignature: "Zero values alter accumulation by index.",
		}
	}

	return similarityDescription{
		ID:              request.ID,
		IntentSignature: similarityTestPurpose,
		FlowSignature:   "Caller rows are filtered and normalized into stably sorted active names.",
	}
}

func requireNoSimilarityIssues(t *testing.T, issues []Issue, err error) {
	t.Helper()

	if err != nil {
		t.Fatal(err)
	}

	if len(issues) != 0 {
		t.Fatalf("similarity issues = %v", issues)
	}
}

func requireSimilarityCount(t *testing.T, name string, got, want int) {
	t.Helper()

	if got != want {
		t.Fatalf("%s = %d, want %d", name, got, want)
	}
}

func requireSimilarityTextFields(
	t *testing.T,
	name string,
	value string,
	required []string,
	forbidden []string,
) {
	t.Helper()

	for _, field := range required {
		if !strings.Contains(value, field) {
			t.Fatalf("%s lacks %q: %s", name, field, value)
		}
	}

	for _, field := range forbidden {
		if strings.Contains(value, field) {
			t.Fatalf("%s contains %q: %s", name, field, value)
		}
	}
}

type similarityPartialTestDescriber struct{}

func (*similarityPartialTestDescriber) describe(
	requests []similarityDescriptionRequest,
	accept func([]similarityDescription) error,
) error {
	descriptions := []similarityDescription{similarityDescriptionForTest(requests[0])}
	if err := accept(descriptions); err != nil {
		return err
	}

	return errors.New("planned batch failure")
}

func TestSimilarityDescriptionsCacheSeparateProductionAndTestShapes(t *testing.T) {
	t.Parallel()

	const sharedHash = "same"

	cacheRoot := t.TempDir()
	describer := new(similarityTestDescriber)
	blocks := []*similarityBlock{
		{Identity: "sample.go::load", Content: "func load() {}", ContentHash: sharedHash},
		{
			Identity:    "sample_test.go::TestLoad",
			Content:     "func TestLoad(t *testing.T) {}",
			ContentHash: sharedHash,
			IsTest:      true,
		},
	}

	digest, err := populateSimilarityDescriptions(
		blocks,
		similarityDescriptionRuntime{describer: describer, enabled: true},
		cacheRoot,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(digest) != 64 || describer.calls != 1 {
		t.Fatalf("digest=%q calls=%d", digest, describer.calls)
	}

	requireSimilarityTextFields(
		t,
		"production signature",
		blocks[0].Description,
		[]string{"INTENT ", "FLOW "},
		[]string{"PURPOSE "},
	)
	requireSimilarityTextFields(
		t,
		"test signature",
		blocks[1].Description,
		[]string{"CONTRACT ", "SCENARIO ", "ORACLE "},
		[]string{"SUBJECT "},
	)

	reloaded := []*similarityBlock{
		{Identity: blocks[0].Identity, Content: blocks[0].Content, ContentHash: sharedHash},
		{
			Identity:    blocks[1].Identity,
			Content:     blocks[1].Content,
			ContentHash: sharedHash,
			IsTest:      true,
		},
	}

	reloadedDigest, err := populateSimilarityDescriptions(
		reloaded,
		similarityDescriptionRuntime{describer: describer, enabled: true},
		cacheRoot,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}

	if reloadedDigest != digest || describer.calls != 1 {
		t.Fatalf("cached digest=%q calls=%d, want %q/1", reloadedDigest, describer.calls, digest)
	}
}

func TestSimilarityDescriptionsPersistCompletedBatchesBeforeFailure(t *testing.T) {
	t.Parallel()

	cacheRoot := t.TempDir()
	blocks := []*similarityBlock{
		{Identity: "sample.go::first", Content: "func first() {}", ContentHash: "hash-a"},
		{Identity: "sample.go::second", Content: "func second() {}", ContentHash: "hash-b"},
	}

	_, err := populateSimilarityDescriptions(
		blocks,
		similarityDescriptionRuntime{
			describer: new(similarityPartialTestDescriber),
			enabled:   true,
		},
		cacheRoot,
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "planned batch failure") {
		t.Fatalf("partial run error = %v", err)
	}

	retry := new(similarityTestDescriber)

	digest, err := populateSimilarityDescriptions(
		blocks,
		similarityDescriptionRuntime{describer: retry, enabled: true},
		cacheRoot,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(digest) != 64 || retry.requests != 1 {
		t.Fatalf("digest=%q retried requests=%d, want one missing block", digest, retry.requests)
	}
}

func TestSimilarityDescriptionsEmbedAndStampThroughExistingLintPath(t *testing.T) {
	tmp := newTestModule(t)
	cacheDir := t.TempDir()
	writeSimilarityTestSource(t, tmp)

	describer := new(similarityTestDescriber)
	embedder := &similarityTestEmbedder{vector: func(input string) []float32 {
		if strings.Contains(input, "func first") || strings.HasPrefix(input, "KIND ") {
			return []float32{1, 0}
		}

		if strings.Contains(input, "func second") {
			return []float32{0.96, 0.28}
		}

		return []float32{0, 1}
	}}
	options := SimilarityOptions{
		CacheEnabled:    true,
		cacheDir:        cacheDir,
		AcceptedPairIDs: []string{similarityAcceptAllID},
		describer:       describer,
		embedder:        embedder,
	}

	issues, err := CheckSimilarCode(loadPackagesForTest(t, tmp), options)
	requireNoSimilarityIssues(t, issues, err)

	requireSimilarityCount(t, "description calls", describer.calls, 2)
	requireSimilarityCount(t, "description requests", describer.requests, 4)
	requireSimilarityCount(t, "embedding batches", embedder.calls, 2)

	stamp, err := loadSimilarityStamp(tmp)
	if err != nil {
		t.Fatal(err)
	}

	if !stamp.policyMatches() {
		t.Fatalf("enriched stamp policy = %#v", stamp)
	}

	if stamp.DescriptionModel != similarityDescriptionModel {
		t.Fatalf("description model = %q", stamp.DescriptionModel)
	}

	if len(stamp.DescriptionDigest) != 64 {
		t.Fatalf("enriched stamp = %#v", stamp)
	}

	issues, err = CheckSimilarCode(loadPackagesForTest(t, tmp), options)
	requireNoSimilarityIssues(t, issues, err)

	requireSimilarityCount(t, "hot description calls", describer.calls, 2)
	requireSimilarityCount(t, "hot embedding batches", embedder.calls, 2)

	withoutCodex := options
	withoutCodex.describer = nil
	withoutCodex.descriptionDisabled = true
	issues, err = CheckSimilarCode(loadPackagesForTest(t, tmp), withoutCodex)
	requireNoSimilarityIssues(t, issues, err)

	requireSimilarityCount(t, "embedding batches without Codex", embedder.calls, 2)

	renamed := strings.NewReplacer("first", "renamedFirst", "values", "inputs", "total", "sum").
		Replace(similarityTestSource)
	writeFile(t, filepath.Join(tmp, similarityTestFilename), renamed)

	if _, err := CheckSimilarCode(
		loadPackagesForTest(t, tmp),
		SimilarityOptions{CI: true},
	); err == nil {
		t.Fatal("CI accepted stale source attestation after rename")
	}

	options.AcceptedPairIDs = nil
	issues, err = CheckSimilarCode(loadPackagesForTest(t, tmp), options)
	requireSingleSimilarityIssue(t, issues, err)
	requireSimilarityCount(t, "description requests after rename", describer.requests, 4)
	requireSimilarityCount(t, "source embedding batches after rename", embedder.calls, 3)
}

func TestSimilarityDescriptionsCoverAllBlocksIndependentlyOfSource(t *testing.T) {
	tmp := newTestModule(t)
	cacheDir := t.TempDir()
	writeFile(t, filepath.Join(tmp, similarityTestFilename), similarityTestSource+`

func third(values []int) int {
	total := 7
	for index, value := range values {
		if index%5 == 0 {
			total *= value + 3
			continue
		}
		if value == 0 {
			total += index
		} else {
			total -= value * index
		}
	}
	return total
}
`)

	describer := new(similarityTestDescriber)
	embedder := &similarityTestEmbedder{vector: func(input string) []float32 {
		switch {
		case strings.Contains(input, "Aggregates indexed"):
			return []float32{0, 0, 1}
		case strings.HasPrefix(input, "KIND "), strings.Contains(input, "func first"):
			return []float32{1, 0, 0}
		case strings.Contains(input, "func second"):
			return []float32{0.96, 0.28, 0}
		default:
			return []float32{0, 0, 1}
		}
	}}

	issues, err := CheckSimilarCode(loadPackagesForTest(t, tmp), SimilarityOptions{
		CacheEnabled:    true,
		cacheDir:        cacheDir,
		AcceptedPairIDs: []string{similarityAcceptAllID},
		describer:       describer,
		embedder:        embedder,
	})
	requireNoSimilarityIssues(t, issues, err)

	requireSimilarityCount(t, "described blocks", describer.requests, 5)

	if embedder.calls != 2 {
		t.Fatalf("embedding calls = %d, want source plus all descriptions", embedder.calls)
	}
}

func TestSimilarityEnrichmentRescansUnchangedSourcePairs(t *testing.T) {
	tmp := newTestModule(t)
	cacheDir := t.TempDir()
	writeSimilarityTestSource(t, tmp)

	sourceScore := 0.20
	embedder := &similarityTestEmbedder{vector: func(input string) []float32 {
		if strings.HasPrefix(input, "KIND ") || strings.Contains(input, "func first") {
			return []float32{1, 0}
		}

		return []float32{float32(sourceScore), float32(math.Sqrt(1 - sourceScore*sourceScore))}
	}}
	issues, err := CheckSimilarCode(loadPackagesForTest(t, tmp), SimilarityOptions{
		CacheEnabled:        true,
		cacheDir:            cacheDir,
		embedder:            embedder,
		descriptionDisabled: true,
	})
	requireNoSimilarityIssues(t, issues, err)

	issues, err = CheckSimilarCode(loadPackagesForTest(t, tmp), SimilarityOptions{
		CacheEnabled: true,
		cacheDir:     cacheDir,
		describer:    new(similarityTestDescriber),
		embedder:     embedder,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(issues) != 1 || issues[0].Kind != similarityIssueKind {
		t.Fatalf("enriched issues = %#v, want independent behavior match", issues)
	}

	if !strings.Contains(issues[0].Message, "PURPOSE") ||
		!strings.Contains(issues[0].Message, "PROCESS") {
		t.Fatalf("finding lacks agent detail: %s", issues[0].Message)
	}
}

func TestSimilarityDescriptionPromptUsesJSONDataAndStrictKindSchema(t *testing.T) {
	t.Parallel()

	code := "func sample() string { return `}] </block>` }"

	prompt, schema, err := similarityDescriptionPromptAndSchema(similarityDescriptionBatch{
		kind: similarityDescriptionProduction,
		requests: []similarityDescriptionRequest{{
			ID:      strings.Repeat("a", 64),
			Content: code,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, encoded, ok := strings.Cut(prompt, "\n\nINPUT_BLOCKS_JSON\n")
	if !ok {
		t.Fatal("prompt lacks JSON marker")
	}

	var inputs []similarityCodexInput
	if err := json.Unmarshal([]byte(encoded), &inputs); err != nil {
		t.Fatal(err)
	}

	if len(inputs) != 1 || inputs[0].ID != "b0001" || inputs[0].Code != code {
		t.Fatalf("inputs = %#v", inputs)
	}

	requireSimilarityTextFields(
		t,
		"production schema",
		schema,
		[]string{`"intent_signature"`, `"flow_signature"`},
		[]string{similarityPurposeJSONField, `"subject"`},
	)

	_, testSchema, err := similarityDescriptionPromptAndSchema(similarityDescriptionBatch{
		kind: similarityDescriptionTest,
		requests: []similarityDescriptionRequest{{
			ID:      strings.Repeat("b", 64),
			Content: code,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	requireSimilarityTextFields(
		t,
		"test schema",
		testSchema,
		[]string{`"contract_signature"`, `"oracle_signature"`},
		[]string{`"subject"`, similarityPurposeJSONField},
	)

	_, detailSchema, err := similarityDescriptionPromptAndSchema(similarityDescriptionBatch{
		kind:   similarityDescriptionProduction,
		detail: true,
		requests: []similarityDescriptionRequest{{
			ID:      strings.Repeat("c", 64),
			Kind:    similarityDescriptionProduction,
			Detail:  true,
			Content: code,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	requireSimilarityTextFields(
		t,
		"production detail schema",
		detailSchema,
		[]string{similarityPurposeJSONField},
		[]string{`"intent_signature"`},
	)
}

func TestDecodeSimilarityDescriptionBatchReturnsValidSubset(t *testing.T) {
	t.Parallel()

	validID := strings.Repeat("a", 64)
	invalidID := strings.Repeat("b", 64)
	batch := similarityDescriptionBatch{
		kind: similarityDescriptionProduction,
		requests: []similarityDescriptionRequest{
			{ID: validID, Kind: similarityDescriptionProduction},
			{ID: invalidID, Kind: similarityDescriptionProduction},
		},
	}
	data := []byte(`{"descriptions":[` +
		`{"id":"b0001","intent_signature":"Returns normalized values in stable sorted order",` +
		`"flow_signature":"Reads input values, normalizes each value, then returns sorted results",` +
		`"boundary_signature":""},` +
		`{"id":"b0002","intent_signature":"Too short",` +
		`"flow_signature":"Reads each value and returns the accepted result",` +
		`"boundary_signature":""}]}`)

	descriptions, err := decodeSimilarityDescriptionBatch(data, batch)
	if err == nil || !strings.Contains(err.Error(), "intent_signature has 2 words") {
		t.Fatalf("partial validation error = %v", err)
	}

	if len(descriptions) != 1 || descriptions[0].ID != validID {
		t.Fatalf("valid descriptions = %#v", descriptions)
	}
}

func TestSimilarityDescriptionBatchesNeverMixShapes(t *testing.T) {
	t.Parallel()

	requests := make([]similarityDescriptionRequest, 0, 67)
	for index := range 65 {
		requests = append(requests, similarityDescriptionRequest{
			ID: strings.Repeat(
				"a",
				62,
			) + string(
				rune('a'+index/26),
			) + string(
				rune('a'+index%26),
			),
			Kind:    similarityDescriptionProduction,
			Content: "func production() {}",
		})
	}

	requests = append(requests, similarityDescriptionRequest{
		ID:      strings.Repeat("b", 64),
		Kind:    similarityDescriptionTest,
		Content: "func TestSample(t *testing.T) {}",
	})
	requests = append(requests, similarityDescriptionRequest{
		ID:      strings.Repeat("c", 64),
		Kind:    similarityDescriptionProduction,
		Detail:  true,
		Content: "func productionDetail() {}",
	})

	batches := similarityDescriptionBatches(requests)

	wantBatches := (65+similarityDescriptionBatchBlocks-1)/similarityDescriptionBatchBlocks + 2
	if len(batches) != wantBatches {
		t.Fatalf("batches = %d, want %d", len(batches), wantBatches)
	}

	for _, batch := range batches {
		for _, request := range batch.requests {
			if request.Kind != batch.kind || request.Detail != batch.detail {
				t.Fatalf("batch %q contains %q", batch.kind, request.Kind)
			}
		}
	}
}

func TestSimilarityDescriptionValidationRejectsMixedShape(t *testing.T) {
	t.Parallel()

	description := similarityDescription{
		Schema:     similarityDescriptionPromptSchema,
		Model:      similarityDescriptionModel,
		Effort:     similarityDescriptionEffort,
		Kind:       similarityDescriptionProduction,
		Detail:     true,
		Purpose:    similarityTestPurpose,
		Processing: []string{"normalize names"},
		Subject:    "unexpected test subject",
	}
	if err := description.validate(); err == nil ||
		!strings.Contains(err.Error(), "contains test fields") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestSimilarityDescriptionStampPolicyTracksEnrichment(t *testing.T) {
	t.Parallel()

	digest := strings.Repeat("a", 64)

	enriched := newSimilarityStamp("source", 2, nil, true, digest)
	if !enriched.policyMatches() || !enriched.covers("source", true) ||
		!enriched.covers("source", false) {
		t.Fatalf("enriched stamp = %#v", enriched)
	}

	sourceOnly := newSimilarityStamp("source", 2, nil, false, "")
	if !sourceOnly.policyMatches() || !sourceOnly.covers("source", false) ||
		sourceOnly.covers("source", true) {
		t.Fatalf("source-only stamp = %#v", sourceOnly)
	}

	enriched.DescriptionDigest = "short"
	if enriched.policyMatches() {
		t.Fatal("short description digest accepted")
	}
}

func TestSimilarityRenameFingerprint(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, before, after string
		equal               bool
	}{
		{"private and locals", `func first(x int) int { y := x + 1; return y }`, `func second(input int) int { output := input + 1; return output }`, true},
		{"shadowed locals", `func f(x int) int { if x > 0 { x := x + 1; println(x) }; return x }`, `func f(input int) int { if input > 0 { inner := input + 1; println(inner) }; return input }`, true},
		{"shadow changes binding", `func f(x int) int { if x > 0 { x := x + 1; println(x) }; return x }`, `func f(x int) int { if x > 0 { y := x + 1; println(x) }; return x }`, false},
		{"API", `func f(x string) string { return strings.TrimSpace(x) }`, `func f(x string) string { return strings.ToUpper(x) }`, false},
		{"literal", `func f(x int) int { return x + 1 }`, `func f(x int) int { return x + 2 }`, false},
		{"flow", `func f(x int) int { return x + 1 }`, `func f(x int) int { return x - 1 }`, false},
		{"callee", `func f(x int) int { return helper(x) }`, `func f(x int) int { return other(x) }`, false},
		{"type", `func f(x First) { consume(x) }`, `func f(x Second) { consume(x) }`, false},
		{"public function", `func First(x int) int { return x }`, `func Second(x int) int { return x }`, false},
		{"method", `func (x T) first() int { return x.n }`, `func (x T) second() int { return x.n }`, false},
		{"receiver", `func (x T) method() int { return x.n }`, `func (value T) method() int { return value.n }`, true},
		{"literal key", `func f(x int) S { return S{x:x} }`, `func f(y int) S { return S{y:y} }`, false},
		{"literal value", `func f(x int) S { return S{field:x} }`, `func f(y int) S { return S{field:y} }`, true},
		{"closure", `func f(x int) func(int) int { return func(y int) int { return x + y } }`, `func f(input int) func(int) int { return func(other int) int { return input + other } }`, true},
		{"entrypoint", `func first() { println(1) }`, `func init() { println(1) }`, false},
		{"named result", `func f(x int) (out int) { out = x; return }`, `func f(in int) (result int) { result = in; return }`, true},
		{"range", `func f(xs []int) int { out := 0; for _, x := range xs { out += x }; return out }`, `func f(values []int) int { sum := 0; for _, value := range values { sum += value }; return sum }`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := similarityRenameFingerprint(test.before)

			after := similarityRenameFingerprint(test.after)
			if before == "" || after == "" || (before == after) != test.equal {
				t.Fatalf("rename fingerprints %q / %q, want equal=%t", before, after, test.equal)
			}
		})
	}

	if got := similarityRenameFingerprint("not Go code"); got != "" {
		t.Fatalf("invalid source fingerprint = %q", got)
	}
}

func TestSimilarityDescriptionsReuseRenames(t *testing.T) {
	t.Parallel()

	makeBlock := func(content string) *similarityBlock {
		hash := sha256.Sum256([]byte(content))

		return &similarityBlock{
			Identity: "sample.go::function", RelativePath: "sample.go",
			Content: content, ContentHash: hex.EncodeToString(hash[:]),
		}
	}
	before := makeBlock(`func first(x int) int { y := x + 1; return y }`)
	runtime := similarityDescriptionRuntime{enabled: true, describer: new(similarityTestDescriber)}

	root := t.TempDir()
	if _, err := populateSimilarityDescriptions(
		[]*similarityBlock{before},
		runtime,
		root,
		true,
	); err != nil {
		t.Fatal(err)
	}

	if err := populateSimilarityDetails(
		[]*similarityBlock{before},
		runtime,
		root,
		true,
	); err != nil {
		t.Fatal(err)
	}

	previous := similarityScanCache{
		Schema:            similarityScanCacheSchema,
		SimilaritySchema:  similaritySchema,
		SourceDigest:      before.ContentHash,
		DescriptionDigest: similarityModelDigest,
		Model:             similarityModelName,
		ModelDigest:       similarityModelDigest,
		Descriptions:      true,
		DescriptionSchema: similarityDescriptionPromptSchema,
		DescriptionModel:  similarityDescriptionModel,
		DescriptionEffort: similarityDescriptionEffort,
		Blocks:            cacheSimilarityBlocks([]*similarityBlock{before}),
	}

	for _, test := range []struct {
		name, content, path string
		stale               bool
		want                int
	}{
		{name: "rename", content: `func second(input int) int { output := input + 1; return output }`},
		{name: "API change", content: `func second(input int) int { output := other(input); return output }`, want: 2},
		{name: "literal change", content: `func second(input int) int { output := input + 2; return output }`, want: 2},
		{name: "different file", content: `func third(input int) int { output := input + 1; return output }`, path: "other.go", want: 2},
		{name: "stale policy", content: `func fourth(input int) int { output := input + 1; return output }`, stale: true, want: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			after := makeBlock(test.content)
			if test.path != "" {
				after.RelativePath = test.path
			}

			cache := previous
			if test.stale {
				cache.DescriptionSchema--
			}

			if err := reuseRenamedSimilarityDescriptions(
				[]*similarityBlock{after},
				cache,
				root,
			); err != nil {
				t.Fatal(err)
			}

			describer := new(similarityTestDescriber)

			runtime := similarityDescriptionRuntime{enabled: true, describer: describer}
			if _, err := populateSimilarityDescriptions(
				[]*similarityBlock{after},
				runtime,
				root,
				true,
			); err != nil {
				t.Fatal(err)
			}

			if err := populateSimilarityDetails(
				[]*similarityBlock{after},
				runtime,
				root,
				true,
			); err != nil {
				t.Fatal(err)
			}

			if describer.requests != test.want {
				t.Fatalf("requests = %d, want %d", describer.requests, test.want)
			}

			if after.ContentHash == before.ContentHash {
				t.Fatal("exact source hash erased rename")
			}

			if len(previous.changedBlocks([]*similarityBlock{after})) != 1 {
				t.Fatal("rename skipped source rescan")
			}
		})
	}
}
