package lint

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type similarityTestEmbedder struct {
	vector func(string) []float32
	calls  int
}

type similarityOrderingTestEmbedder struct {
	lengths    []int
	batchSizes []int
}

func (embedder *similarityOrderingTestEmbedder) embed(inputs []string) ([][]float32, error) {
	embedder.batchSizes = append(embedder.batchSizes, len(inputs))

	vectors := make([][]float32, len(inputs))
	for index, input := range inputs {
		embedder.lengths = append(embedder.lengths, len(input))
		vectors[index] = []float32{1, float32(len(input))}
	}

	return vectors, nil
}

func (*similarityOrderingTestEmbedder) close() error {
	return nil
}

func TestSimilarityEmbeddingOrdersMissingInputsByLength(t *testing.T) {
	t.Parallel()

	lengths := []int{900, 10, 500, 200}

	blocks := make([]*similarityBlock, len(lengths))
	for index, length := range lengths {
		blocks[index] = &similarityBlock{
			Identity: string(rune('a' + index)),
			Content:  strings.Repeat("x", length),
		}
	}

	embedder := new(similarityOrderingTestEmbedder)

	runtime := newSimilarityEmbeddingRuntime(embedder, t.TempDir(), false)
	if _, err := runtime.populate(blocks, similaritySourceVector); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(embedder.lengths, []int{10, 200, 500, 900}) {
		t.Fatalf("embedding input lengths = %v", embedder.lengths)
	}

	if !slices.Equal(embedder.batchSizes, []int{4}) {
		t.Fatalf("embedding batch sizes = %v", embedder.batchSizes)
	}
}

func TestSimilarityEmbeddingCapsLongBatchesByBytes(t *testing.T) {
	t.Parallel()

	const blocksCount = 40

	blocks := make([]*similarityBlock, blocksCount)
	for index := range blocks {
		prefix := fmt.Sprintf("%04d", index)
		blocks[index] = &similarityBlock{
			Identity: prefix,
			Content:  prefix + strings.Repeat("x", similarityEmbeddingChunkBytes-len(prefix)),
		}
	}

	embedder := new(similarityOrderingTestEmbedder)

	runtime := newSimilarityEmbeddingRuntime(embedder, t.TempDir(), false)
	if _, err := runtime.populate(blocks, similaritySourceVector); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(embedder.batchSizes, []int{32, 8}) {
		t.Fatalf("embedding batch sizes = %v", embedder.batchSizes)
	}
}

func (embedder *similarityTestEmbedder) embed(inputs []string) ([][]float32, error) {
	embedder.calls++

	vectors := make([][]float32, len(inputs))
	for i, input := range inputs {
		vectors[i] = embedder.vector(input)
	}

	return vectors, nil
}

func (*similarityTestEmbedder) close() error {
	return nil
}

func similarityHTTPTestVector(first float64) []float64 {
	vector := make([]float64, similarityEmbeddingDimensions)
	vector[0] = first

	return vector
}

type recordedSimilarityEmbeddingRequest struct {
	method      string
	path        string
	contentType string
	body        similarityEmbeddingRequest
}

func newSimilarityHTTPTestServer(
	t *testing.T,
	response similarityEmbeddingResponse,
) (*httptest.Server, <-chan recordedSimilarityEmbeddingRequest) {
	t.Helper()

	requests := make(chan recordedSimilarityEmbeddingRequest, 1)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body similarityEmbeddingRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}

		requests <- recordedSimilarityEmbeddingRequest{
			method:      request.Method,
			path:        request.URL.Path,
			contentType: request.Header.Get("Content-Type"),
			body:        body,
		}

		if err := json.NewEncoder(writer).Encode(response); err != nil {
			t.Errorf("encode response: %v", err)
		}
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return server, requests
}

func requireSimilarityHTTPRequest(
	t *testing.T,
	request recordedSimilarityEmbeddingRequest,
	inputs []string,
) {
	t.Helper()

	if request.method != http.MethodPost ||
		request.path != similarityEmbeddingsPath ||
		request.contentType != "application/json" ||
		request.body.Model != similarityModelName ||
		request.body.EncodingFormat != "float" ||
		!slices.Equal(request.body.Input, inputs) {
		t.Fatalf("embedding request = %+v", request)
	}
}

func TestHTTPSimilarityEmbedderRequestsAndOrdersEmbeddings(t *testing.T) {
	t.Parallel()

	inputs := []string{"first function", "second function"}

	for _, tt := range []struct {
		name     string
		basePath string
	}{
		{name: "server root", basePath: ""},
		{name: "v1 root", basePath: "/v1/"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server, requests := newSimilarityHTTPTestServer(t, similarityEmbeddingResponse{
				Model: similarityModelName,
				Data: []similarityEmbeddingData{
					{Index: 1, Embedding: similarityHTTPTestVector(2)},
					{Index: 0, Embedding: similarityHTTPTestVector(1)},
				},
			})

			embedder, err := newHTTPSimilarityEmbedder(server.URL + tt.basePath)
			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() {
				if err := embedder.close(); err != nil {
					t.Error(err)
				}
			})

			vectors, err := embedder.embed(inputs)
			if err != nil {
				t.Fatal(err)
			}

			if len(vectors) != 2 || vectors[0][0] != 1 || vectors[1][0] != 2 {
				t.Fatalf("ordered vectors = %v", vectors)
			}

			requireSimilarityHTTPRequest(t, <-requests, inputs)
		})
	}
}

func TestSimilarityEmbeddingRuntimeUsesConfiguredServer(t *testing.T) {
	server, requests := newSimilarityHTTPTestServer(t, similarityEmbeddingResponse{
		Model: similarityModelName,
		Data: []similarityEmbeddingData{{
			Index:     0,
			Embedding: similarityHTTPTestVector(1),
		}},
	})

	t.Setenv(similarityLlamaURLEnv, server.URL+"/v1")

	runtime := newSimilarityEmbeddingRuntime(nil, t.TempDir(), false)
	t.Cleanup(func() {
		if err := runtime.close(); err != nil {
			t.Error(err)
		}
	})

	matrix, err := runtime.populate(
		[]*similarityBlock{{Identity: "configured", Content: "func configured() {}"}},
		similaritySourceVector,
	)
	if err != nil {
		t.Fatal(err)
	}

	if matrix.Dimensions != similarityEmbeddingDimensions {
		t.Fatalf(
			"matrix dimensions = %d, want %d",
			matrix.Dimensions,
			similarityEmbeddingDimensions,
		)
	}

	if path := (<-requests).path; path != similarityEmbeddingsPath {
		t.Fatalf("request path = %s, want %s", path, similarityEmbeddingsPath)
	}
}

func TestNewHTTPSimilarityEmbedderRejectsInvalidURL(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		url  string
	}{
		{name: "unsupported scheme", url: "unix:///tmp/llama.sock"},
		{name: "endpoint path", url: "http://127.0.0.1:8080/v1/embeddings"},
		{name: "query", url: "http://127.0.0.1:8080?token=secret"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newHTTPSimilarityEmbedder(tt.url)
			if err == nil || !strings.Contains(err.Error(), similarityLlamaURLEnv) {
				t.Fatalf("URL error = %v", err)
			}
		})
	}
}

func TestHTTPSimilarityEmbedderRejectsInvalidResponses(t *testing.T) {
	t.Parallel()

	const input = "embedding input"

	validVector := similarityHTTPTestVector(1)
	shortVector := make([]float64, similarityEmbeddingDimensions-1)
	overflowVector := similarityHTTPTestVector(1)
	overflowVector[10] = float64(2) * math.MaxFloat32

	tests := []struct {
		name     string
		inputs   []string
		response similarityEmbeddingResponse
		wantErr  string
	}{
		{
			name:   "wrong model",
			inputs: []string{input},
			response: similarityEmbeddingResponse{
				Model: "other",
				Data:  []similarityEmbeddingData{{Index: 0, Embedding: validVector}},
			},
			wantErr: "returned model",
		},
		{
			name:     "wrong count",
			inputs:   []string{input},
			response: similarityEmbeddingResponse{Model: similarityModelName},
			wantErr:  "returned 0 embeddings for 1 inputs",
		},
		{
			name:   "index out of range",
			inputs: []string{input},
			response: similarityEmbeddingResponse{
				Model: similarityModelName,
				Data:  []similarityEmbeddingData{{Index: 1, Embedding: validVector}},
			},
			wantErr: "embedding index 1 for 1 inputs",
		},
		{
			name:   "duplicate index",
			inputs: []string{input, input + " second"},
			response: similarityEmbeddingResponse{
				Model: similarityModelName,
				Data: []similarityEmbeddingData{
					{Index: 0, Embedding: validVector},
					{Index: 0, Embedding: validVector},
				},
			},
			wantErr: "duplicate embedding index 0",
		},
		{
			name:   "wrong dimensions",
			inputs: []string{input},
			response: similarityEmbeddingResponse{
				Model: similarityModelName,
				Data:  []similarityEmbeddingData{{Index: 0, Embedding: shortVector}},
			},
			wantErr: "has 767 dimensions, want 768",
		},
		{
			name:   "float32 overflow",
			inputs: []string{input},
			response: similarityEmbeddingResponse{
				Model: similarityModelName,
				Data:  []similarityEmbeddingData{{Index: 0, Embedding: overflowVector}},
			},
			wantErr: "dimension 10 is not a finite float32",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _ := newSimilarityHTTPTestServer(t, tt.response)

			embedder, err := newHTTPSimilarityEmbedder(server.URL)
			if err != nil {
				t.Fatal(err)
			}

			defer func() {
				if err := embedder.close(); err != nil {
					t.Error(err)
				}
			}()

			_, err = embedder.embed(tt.inputs)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("embed error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestHTTPSimilarityEmbedderBoundsServerError(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(
			strings.Repeat("x", similarityEmbeddingMaxErrorBytes+100) + "secret-tail",
		))
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	embedder, err := newHTTPSimilarityEmbedder(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := embedder.close(); err != nil {
			t.Error(err)
		}
	})

	_, err = embedder.embed([]string{"one"})
	if err == nil ||
		!strings.Contains(err.Error(), "503 Service Unavailable") ||
		strings.Contains(err.Error(), "secret-tail") ||
		!strings.HasSuffix(err.Error(), "...") {
		t.Fatalf("bounded server error = %v", err)
	}
}

func TestHTTPSimilarityEmbedderReportsConnectionFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	serverURL := server.URL
	server.Close()

	embedder, err := newHTTPSimilarityEmbedder(serverURL)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := embedder.close(); err != nil {
			t.Error(err)
		}
	})

	_, err = embedder.embed([]string{"one"})
	if err == nil ||
		!strings.Contains(err.Error(), "connect to llama-server") ||
		!strings.Contains(err.Error(), similarityLlamaURLEnv) ||
		!strings.Contains(err.Error(), serverURL) {
		t.Fatalf("connection error = %v", err)
	}
}

func TestSimilarityDescriptionVectorsStreamInScanOrder(t *testing.T) {
	t.Parallel()

	blocks := []*similarityBlock{
		{Identity: "slot-a", Description: "shared signature"},
		{Identity: "slot-b", Description: "different signature"},
		{Identity: "shared", Description: "shared signature"},
	}

	ready := make(chan []*similarityBlock, len(blocks))
	ready <- blocks[2:]

	ready <- blocks[1:2]

	ready <- blocks[:1]

	close(ready)

	embedder := new(similarityOrderingTestEmbedder)
	runtime := newSimilarityEmbeddingRuntime(embedder, t.TempDir(), false)

	got, err := runtime.populateDescriptions(blocks, ready)
	if err != nil {
		t.Fatal(err)
	}

	if len(embedder.lengths) != 2 {
		t.Fatalf("embedded shared signature more than once: %v", embedder.lengths)
	}

	want, err := runtime.populate(blocks, similarityDescriptionVector)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("streamed matrix differs from scan-order matrix: %v vs %v", got, want)
	}
}

type similarityStreamingTestDescriber struct {
	embedded <-chan struct{}
	finished chan struct{}
}

func (describer similarityStreamingTestDescriber) describe(
	requests []similarityDescriptionRequest,
	accept func([]similarityDescription) error,
) error {
	if describer.finished != nil {
		defer close(describer.finished)
	}

	if err := accept(
		[]similarityDescription{similarityDescriptionForTest(requests[0])},
	); err != nil {
		return err
	}

	select {
	case <-describer.embedded:
	case <-time.After(5 * time.Second):
		return errors.New("signature embedding waited for all descriptions")
	}

	for _, request := range requests[1:] {
		if err := accept(
			[]similarityDescription{similarityDescriptionForTest(request)},
		); err != nil {
			return err
		}
	}

	return nil
}

func TestSimilarityScanEmbedsBeforeDescriptionsFinish(t *testing.T) {
	t.Parallel()

	for _, cacheEnabled := range []bool{false, true} {
		t.Run(strconv.FormatBool(cacheEnabled), func(t *testing.T) {
			embedded := make(chan struct{})

			var signal sync.Once

			embedder := &similarityTestEmbedder{vector: func(input string) []float32 {
				if strings.HasPrefix(input, "KIND PRODUCTION") {
					signal.Do(func() { close(embedded) })
				}

				return []float32{1, float32(len(input))}
			}}
			blocks := []*similarityBlock{
				{
					Identity:    "slot-a",
					Content:     "func first() int { return 1 }",
					ContentHash: "hash-a",
				},
				{Identity: "different", Content: "func third() {}", ContentHash: "different"},
			}
			runtime := similarityDescriptionRuntime{
				describer: similarityStreamingTestDescriber{embedded: embedded}, enabled: true,
			}

			vectors, digest, err := populateSimilarityScanVectors(
				blocks, blocks, blocks, runtime, t.TempDir(),
				SimilarityOptions{embedder: embedder, CacheEnabled: cacheEnabled},
			)
			if err != nil {
				t.Fatal(err)
			}

			if digest == "" || len(vectors.Description.Values) != 2*vectors.Description.Dimensions {
				t.Fatalf("incomplete scan: digest=%q, vectors=%+v", digest, vectors)
			}
		})
	}
}

type similarityFailureTestEmbedder struct {
	calls  int
	failAt int
}

func (embedder *similarityFailureTestEmbedder) embed(inputs []string) ([][]float32, error) {
	embedder.calls++
	if embedder.calls == embedder.failAt {
		return nil, errors.New("planned embedding failure")
	}

	vectors := make([][]float32, len(inputs))
	for index := range vectors {
		vectors[index] = []float32{1, 0}
	}

	return vectors, nil
}

func (*similarityFailureTestEmbedder) close() error { return nil }

func TestSimilarityScanDrainsDescriptionsAfterEmbeddingFailure(t *testing.T) {
	t.Parallel()

	for _, failAt := range []int{1, 2} {
		t.Run(strconv.Itoa(failAt), func(t *testing.T) {
			// Both source and signature failures must wait for completed cache work.
			blocks := make([]*similarityBlock, similarityDescriptionWorkers+2)
			for index := range blocks {
				key := strconv.Itoa(index)
				blocks[index] = &similarityBlock{Identity: key, Content: key, ContentHash: key}
			}

			embedded := make(chan struct{})
			close(embedded)
			runtime := similarityDescriptionRuntime{
				describer: similarityStreamingTestDescriber{embedded: embedded}, enabled: true,
			}
			cacheRoot := t.TempDir()
			done := make(chan error, 1)

			go func() {
				_, _, err := populateSimilarityScanVectors(
					blocks,
					blocks,
					blocks,
					runtime,
					cacheRoot,
					SimilarityOptions{
						embedder:     &similarityFailureTestEmbedder{failAt: failAt},
						CacheEnabled: true,
					},
				)
				done <- err
			}()

			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "planned embedding failure") {
					t.Fatalf("scan error = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("embedding failure blocked description producer")
			}

			inputs, keys := indexSimilarityDescriptionInputs(
				blocks,
				similarityDescriptionSignatures,
			)

			missing, _ := loadCachedSimilarityDescriptions(inputs, keys, cacheRoot, true)
			if len(missing) != 0 {
				t.Fatalf("lost %d completed signatures", len(missing))
			}
		})
	}
}

func TestSimilarityScanReusesCachedAndRestoredDescriptions(t *testing.T) {
	t.Parallel()

	blocks := []*similarityBlock{
		{Identity: "cached", Content: "cached source", ContentHash: "cached-hash"},
		{Identity: "restored", Content: "restored source", ContentHash: "restored-hash"},
	}
	cacheRoot := t.TempDir()
	describer := new(similarityTestDescriber)
	runtime := similarityDescriptionRuntime{describer: describer, enabled: true}
	embedder := &similarityTestEmbedder{vector: func(input string) []float32 {
		return []float32{1, float32(len(input))}
	}}

	want, wantDigest, err := populateSimilarityScanVectors(
		blocks,
		blocks,
		blocks,
		runtime,
		cacheRoot,
		SimilarityOptions{embedder: embedder, CacheEnabled: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	blocks[0].Description = ""
	blocks[0].DescriptionHash = ""
	describer = new(similarityTestDescriber)
	runtime.describer = describer

	got, digest, err := populateSimilarityScanVectors(
		blocks,
		blocks[:1],
		blocks,
		runtime,
		cacheRoot,
		SimilarityOptions{embedder: &similarityFailureTestEmbedder{failAt: 1}, CacheEnabled: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	if describer.calls != 0 || digest != wantDigest || !reflect.DeepEqual(got, want) {
		t.Fatalf(
			"cache/restored replay changed results or generated descriptions: calls=%d",
			describer.calls,
		)
	}
}

func TestSimilarityScanRejectsIncompleteDescriptionStream(t *testing.T) {
	t.Parallel()

	blocks := []*similarityBlock{
		{Identity: "valid", Content: "valid source", ContentHash: "valid-hash"},
		{Identity: "failed", Content: "failed source", ContentHash: "failed-hash"},
	}
	runtime := similarityDescriptionRuntime{
		describer: new(similarityPartialTestDescriber),
		enabled:   true,
	}

	vectors, digest, err := populateSimilarityScanVectors(
		blocks,
		blocks,
		blocks,
		runtime,
		t.TempDir(),
		SimilarityOptions{embedder: new(similarityOrderingTestEmbedder), CacheEnabled: true},
	)
	if err == nil || !strings.Contains(err.Error(), "planned batch failure") {
		t.Fatalf("incomplete stream error = %v", err)
	}

	if digest != "" || len(vectors.Description.Values) != 0 || len(vectors.Source.Values) != 0 {
		t.Fatal("incomplete stream published a scan result")
	}
}

func TestSimilarityScanDoesNotStallDescriptionsDuringSourceInference(t *testing.T) {
	t.Parallel()

	blocks := make([]*similarityBlock, similarityDescriptionWorkers+2)
	for index := range blocks {
		key := strconv.Itoa(index)
		blocks[index] = &similarityBlock{Identity: key, Content: key, ContentHash: key}
	}

	embedded := make(chan struct{})
	close(embedded)

	finished := make(chan struct{})
	runtime := similarityDescriptionRuntime{
		describer: similarityStreamingTestDescriber{
			embedded: embedded,
			finished: finished,
		},
		enabled: true,
	}
	embedder := &similarityTestEmbedder{vector: func(input string) []float32 {
		select {
		case <-finished:
			return []float32{1, float32(len(input))}
		case <-time.After(5 * time.Second):
			return nil
		}
	}}

	_, _, err := populateSimilarityScanVectors(blocks, blocks, blocks, runtime, t.TempDir(),
		SimilarityOptions{embedder: embedder})
	if err != nil {
		t.Fatalf("source inference stalled description production: %v", err)
	}
}
