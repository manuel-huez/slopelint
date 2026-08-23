package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
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
