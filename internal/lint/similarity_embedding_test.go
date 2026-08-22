package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

type similarityTestEmbedder struct {
	vector func(string) []float32
	calls  int
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
