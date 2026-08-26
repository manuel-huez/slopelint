package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

func TestDownloadSimilarityModelVerifiesAndReusesExactFile(t *testing.T) {
	t.Parallel()

	content := []byte("test gguf model")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	requests := &atomic.Int64{}

	server := httptest.NewServer(
		http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			requests.Add(1)

			_, _ = writer.Write(content)
		}),
	)
	defer server.Close()

	path := filepath.Join(t.TempDir(), "model.gguf")
	if err := downloadSimilarityModel(
		path,
		server.URL,
		digest,
		int64(len(content)),
		server.Client(),
	); err != nil {
		t.Fatalf("download model: %v", err)
	}

	valid, err := validSimilarityModel(path, digest, int64(len(content)))
	if err != nil || !valid {
		t.Fatalf("valid model = %v, err = %v", valid, err)
	}

	if requests.Load() != 1 {
		t.Fatalf("model requests = %d, want 1", requests.Load())
	}
}

func TestDownloadSimilarityModelRejectsWrongDigestWithoutReplacingModel(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(path, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	content := []byte("wrong")
	server := httptest.NewServer(
		http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write(content)
		}),
	)

	defer server.Close()

	err := downloadSimilarityModel(
		path,
		server.URL,
		"0000000000000000000000000000000000000000000000000000000000000000",
		int64(len(content)),
		server.Client(),
	)
	if err == nil {
		t.Fatal("wrong model digest succeeded")
	}

	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}

	if string(got) != "existing" {
		t.Fatalf("existing model replaced with %q", got)
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
