package lint

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	similarityCompletedRecordID = "completed"
	similarityPendingRecordID   = "pending"
)

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

func TestSimilarityDescriptionBatchesBalanceAndPreserveInputs(t *testing.T) {
	t.Parallel()

	requests := make([]similarityDescriptionRequest, 2*similarityDescriptionBatchBlocks)
	for index := range requests {
		length := 10
		if index < similarityDescriptionBatchBlocks {
			length = 1000
		}

		requests[index] = similarityDescriptionRequest{
			ID: fmt.Sprintf("%04d", index), Kind: similarityDescriptionProduction,
			Content: strings.Repeat("x", length),
		}
	}

	original := slices.Clone(requests)

	batches := similarityDescriptionBatches(requests)
	if !slices.Equal(original, requests) {
		t.Fatal("batching mutated caller requests")
	}

	if len(batches) != 2 {
		t.Fatalf("batch count = %d", len(batches))
	}

	lengths := make([]int, len(batches))
	seen := make(map[string]bool)

	for index, batch := range batches {
		for _, request := range batch.requests {
			if seen[request.ID] {
				t.Fatalf("duplicate request %s", request.ID)
			}

			seen[request.ID] = true
			lengths[index] += len(request.Content)
		}
	}

	if len(seen) != len(requests) || lengths[0] != lengths[1] {
		t.Fatalf("unbalanced or missing inputs: lengths=%v, unique=%d", lengths, len(seen))
	}

	slices.Reverse(requests)

	if !reflect.DeepEqual(batches, similarityDescriptionBatches(requests)) {
		t.Fatal("batch layout depends on input order")
	}
}

func TestSimilarityDescriptionBatchesRespectBytesWithoutTruncation(t *testing.T) {
	t.Parallel()

	requests := []similarityDescriptionRequest{
		{
			ID:      "a",
			Kind:    similarityDescriptionProduction,
			Content: strings.Repeat("a", similarityDescriptionBatchBytes+1),
		},
		{
			ID:      "b",
			Kind:    similarityDescriptionProduction,
			Content: strings.Repeat("b", similarityDescriptionBatchBytes/2),
		},
		{
			ID:      "c",
			Kind:    similarityDescriptionProduction,
			Content: strings.Repeat("c", similarityDescriptionBatchBytes/2),
		},
	}

	var got []similarityDescriptionRequest

	for _, batch := range similarityDescriptionBatches(requests) {
		size := 0
		for _, request := range batch.requests {
			size += len(request.Content) + len(request.ID) + similarityDescriptionBatchOverhead
		}

		if len(batch.requests) != 1 ||
			(size > similarityDescriptionBatchBytes && batch.requests[0].ID != "a") {
			t.Fatalf("invalid bounded batch: count=%d, bytes=%d", len(batch.requests), size)
		}

		got = append(got, batch.requests...)
	}

	if !slices.Equal(got, requests) {
		t.Fatal("oversized input was changed or dropped")
	}
}

// The child process exercises the real CLI transport without provider access.
func TestSimilarityCodexProcess(t *testing.T) {
	endpoint := os.Getenv("SLOPELINT_TEST_CODEX_ENDPOINT")
	if endpoint == "" {
		return
	}

	response, err := http.Post(endpoint, "text/plain", os.Stdin)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = response.Body.Close() }()

	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}

	if response.StatusCode != http.StatusOK {
		t.Fatalf("fake CLI server: %s", data)
	}

	for index, arg := range os.Args {
		if arg == "--output-last-message" && index+1 < len(os.Args) {
			if err := os.WriteFile(os.Args[index+1], data, 0o600); err != nil {
				t.Fatal(err)
			}

			return
		}
	}

	t.Fatal("CLI did not specify an output file")
}

func similarityCodexForTest(
	t *testing.T,
	respond func([]similarityCodexInput) ([]similarityDescription, error),
) *codexSimilarityDescriber {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		_, encoded, _ := strings.Cut(string(data), "\n\nINPUT_BLOCKS_JSON\n")

		var inputs []similarityCodexInput
		if err := json.Unmarshal([]byte(encoded), &inputs); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		result, err := respond(inputs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := json.NewEncoder(w).
			Encode(similarityCodexResponse{Descriptions: result}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("SLOPELINT_TEST_CODEX_ENDPOINT", server.URL)

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "codex")

	command := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") +
		"' -test.run='^TestSimilarityCodexProcess$' -- \"$@\"\n"
	if err := os.WriteFile(path, []byte(command), 0o700); err != nil {
		t.Fatal(err)
	}

	return &codexSimilarityDescriber{path: path}
}

func TestSimilarityCodexPersistsPartialResponseBeforeRetry(t *testing.T) {
	var (
		accepted atomic.Bool
		attempts atomic.Int32
	)

	describer := similarityCodexForTest(
		t,
		func(inputs []similarityCodexInput) ([]similarityDescription, error) {
			attempt := attempts.Add(1)
			if attempt == 2 &&
				(!accepted.Load() || len(inputs) != 1 || inputs[0].Code != similarityPendingRecordID) {
				return nil, errors.New(
					"retry started before acceptance or included completed input",
				)
			}

			return []similarityDescription{
				similarityDescriptionForTest(similarityDescriptionRequest{ID: inputs[0].ID}),
			}, nil
		},
	)
	batch := similarityDescriptionBatch{
		kind: similarityDescriptionProduction,
		requests: []similarityDescriptionRequest{
			{
				ID:      similarityCompletedRecordID,
				Content: similarityCompletedRecordID,
			},
			{ID: similarityPendingRecordID, Content: similarityPendingRecordID},
		},
	}

	var ids []string

	err := describer.describeBatch(batch, func(records []similarityDescription) error {
		accepted.Store(true)

		for _, record := range records {
			ids = append(ids, record.ID)
		}

		return nil
	})
	if err != nil || attempts.Load() != 2 ||
		!slices.Equal(ids, []string{similarityCompletedRecordID, similarityPendingRecordID}) {
		t.Fatalf("partial retry: attempts=%d, ids=%v, err=%v", attempts.Load(), ids, err)
	}
}

func TestSimilarityCodexStopsOnAcceptanceFailure(t *testing.T) {
	var attempts atomic.Int32

	describer := similarityCodexForTest(
		t,
		func(inputs []similarityCodexInput) ([]similarityDescription, error) {
			attempts.Add(1)

			return []similarityDescription{
				similarityDescriptionForTest(similarityDescriptionRequest{ID: inputs[0].ID}),
			}, nil
		},
	)
	want := errors.New("cache write failed")

	err := describer.describeBatch(similarityDescriptionBatch{
		kind: similarityDescriptionProduction,
		requests: []similarityDescriptionRequest{
			{ID: similarityCompletedRecordID},
			{ID: similarityPendingRecordID},
		},
	}, func([]similarityDescription) error { return want })
	if !errors.Is(err, want) || attempts.Load() != 1 {
		t.Fatalf("acceptance failure retried: attempts=%d, err=%v", attempts.Load(), err)
	}
}

func TestSimilarityCodexConcurrencyIndependentOfCPULimit(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)

	t.Cleanup(func() { runtime.GOMAXPROCS(previous) })

	started := make(chan struct{}, similarityDescriptionWorkers+1)
	release := make(chan struct{})

	var active, peak atomic.Int32

	describer := similarityCodexForTest(
		t,
		func(inputs []similarityCodexInput) ([]similarityDescription, error) {
			count := active.Add(1)
			defer active.Add(-1)

			for old := peak.Load(); count > old && !peak.CompareAndSwap(old, count); old = peak.Load() {
			}

			started <- struct{}{}

			select {
			case <-release:
			case <-time.After(5 * time.Second):
				return nil, errors.New("CPU cap serialized Codex requests")
			}

			records := make([]similarityDescription, len(inputs))
			for index, input := range inputs {
				records[index] = similarityDescriptionForTest(
					similarityDescriptionRequest{ID: input.ID},
				)
			}

			return records, nil
		},
	)

	requests := make(
		[]similarityDescriptionRequest,
		(similarityDescriptionWorkers+1)*similarityDescriptionBatchBlocks,
	)
	for index := range requests {
		requests[index] = similarityDescriptionRequest{
			ID:   strconv.Itoa(index),
			Kind: similarityDescriptionProduction,
		}
	}

	done := make(chan error, 1)
	go func() { done <- describer.describe(requests, func([]similarityDescription) error { return nil }) }()

	for range similarityDescriptionWorkers {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			close(release)
			<-done
			t.Fatal("Codex requests did not overlap under one CPU")
		}
	}

	close(release)

	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if peak.Load() != int32(similarityDescriptionWorkers) {
		t.Fatalf("unbounded request concurrency: %d", peak.Load())
	}
}
