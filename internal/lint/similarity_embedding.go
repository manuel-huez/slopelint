package lint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	similarityLlamaURLEnv     = "SLOPELINT_LLAMA_URL"
	similarityLlamaDefaultURL = "http://127.0.0.1:18080"
	// One full batch takes about 20s on an 8-core VPS CPU. The limit leaves room
	// for a server shared by parallel runs and fails a hung server in minutes.
	similarityEmbeddingRequestTimeout   = 3 * time.Minute
	similarityEmbeddingMaxErrorBytes    = 4096
	similarityEmbeddingMaxResponseBytes = 16 << 20
	similarityEmbeddingsPath            = "/v1/embeddings"
)

type similarityEmbedder interface {
	embed([]string) ([][]float32, error)
	close() error
}

type httpSimilarityEmbedder struct {
	endpoint string
	model    string
	client   *http.Client
	timeout  time.Duration
}

type similarityEmbeddingRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	EncodingFormat string   `json:"encoding_format"`
}

type similarityEmbeddingResponse struct {
	Model string                    `json:"model"`
	Data  []similarityEmbeddingData `json:"data"`
}

type similarityEmbeddingData struct {
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

func newHTTPSimilarityEmbedder(rawURL, model string) (*httpSimilarityEmbedder, error) {
	endpoint, err := similarityEmbeddingEndpoint(rawURL)
	if err != nil {
		return nil, err
	}

	transport := http.DefaultTransport
	if defaultTransport, ok := transport.(*http.Transport); ok {
		transport = defaultTransport.Clone()
	}

	return &httpSimilarityEmbedder{
		endpoint: endpoint,
		model:    model,
		client:   &http.Client{Transport: transport},
		timeout:  similarityEmbeddingRequestTimeout,
	}, nil
}

func similarityEmbeddingEndpoint(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		rawURL = similarityLlamaDefaultURL
	}

	serverURL, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", similarityLlamaURLEnv, err)
	}

	if (serverURL.Scheme != "http" && serverURL.Scheme != "https") ||
		serverURL.Host == "" || serverURL.User != nil || serverURL.RawQuery != "" ||
		serverURL.Fragment != "" {
		return "", fmt.Errorf(
			"%s must be an HTTP server root or /v1 URL, got %q",
			similarityLlamaURLEnv,
			rawURL,
		)
	}

	switch strings.TrimRight(serverURL.Path, "/") {
	case "", "/v1":
		serverURL.Path = similarityEmbeddingsPath
	default:
		return "", fmt.Errorf(
			"%s must be an HTTP server root or /v1 URL, got %q",
			similarityLlamaURLEnv,
			rawURL,
		)
	}

	serverURL.RawPath = ""

	return serverURL.String(), nil
}

func (embedder *httpSimilarityEmbedder) embed(inputs []string) (vectors [][]float32, err error) {
	if len(inputs) == 0 {
		return [][]float32{}, nil
	}

	body, err := json.Marshal(similarityEmbeddingRequest{
		Model:          embedder.model,
		Input:          inputs,
		EncodingFormat: "float",
	})
	if err != nil {
		return nil, fmt.Errorf("encode llama-server embedding request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), embedder.timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		embedder.endpoint,
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("create llama-server embedding request: %w", err)
	}

	request.Header.Set("Content-Type", "application/json")

	response, err := embedder.client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf(
				"llama-server embedding request to %s timed out after %s: %w",
				embedder.endpoint,
				embedder.timeout,
				err,
			)
		}

		return nil, fmt.Errorf(
			"connect to llama-server at %s (check %s): %w",
			embedder.endpoint,
			similarityLlamaURLEnv,
			err,
		)
	}

	defer func() { err = errors.Join(err, response.Body.Close()) }()

	return embedder.readResponse(response, len(inputs))
}

func (embedder *httpSimilarityEmbedder) readResponse(
	response *http.Response,
	inputCount int,
) ([][]float32, error) {
	endpoint := embedder.endpoint

	if response.StatusCode != http.StatusOK {
		message, readErr := io.ReadAll(
			io.LimitReader(response.Body, similarityEmbeddingMaxErrorBytes+1),
		)
		if readErr != nil {
			return nil, fmt.Errorf(
				"read llama-server error response from %s: %w",
				endpoint,
				readErr,
			)
		}

		truncated := len(message) > similarityEmbeddingMaxErrorBytes
		if truncated {
			message = message[:similarityEmbeddingMaxErrorBytes]
		}

		detail := strings.TrimSpace(string(message))
		if truncated {
			detail += "..."
		}

		if detail == "" {
			detail = "empty response body"
		}

		return nil, fmt.Errorf(
			"llama-server embedding request to %s returned %s: %s",
			endpoint,
			response.Status,
			detail,
		)
	}

	encoded, err := io.ReadAll(
		io.LimitReader(response.Body, similarityEmbeddingMaxResponseBytes+1),
	)
	if err != nil {
		return nil, fmt.Errorf("read llama-server embedding response from %s: %w", endpoint, err)
	}

	if len(encoded) > similarityEmbeddingMaxResponseBytes {
		return nil, fmt.Errorf(
			"llama-server embedding response exceeds %d bytes",
			similarityEmbeddingMaxResponseBytes,
		)
	}

	var decoded similarityEmbeddingResponse
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, fmt.Errorf("decode llama-server embedding response: %w", err)
	}

	return validateSimilarityEmbeddingResponse(decoded, inputCount, embedder.model)
}

func validateSimilarityEmbeddingResponse(
	decoded similarityEmbeddingResponse,
	inputCount int,
	model string,
) ([][]float32, error) {
	if decoded.Model != model {
		return nil, fmt.Errorf(
			"llama-server returned model %q, want %q from %s",
			decoded.Model,
			model,
			similarityEmbeddingModelEnv,
		)
	}

	if len(decoded.Data) != inputCount {
		return nil, fmt.Errorf(
			"llama-server returned %d embeddings for %d inputs",
			len(decoded.Data),
			inputCount,
		)
	}

	vectors := make([][]float32, inputCount)
	// Models differ in width; one response must still use one width.
	dimensions := 0
	if len(decoded.Data) > 0 {
		dimensions = len(decoded.Data[0].Embedding)
	}

	seen := make([]bool, inputCount)
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= inputCount {
			return nil, fmt.Errorf(
				"llama-server returned embedding index %d for %d inputs",
				item.Index,
				inputCount,
			)
		}

		if seen[item.Index] {
			return nil, fmt.Errorf("llama-server returned duplicate embedding index %d", item.Index)
		}

		vector, err := validatedSimilarityEmbeddingVector(item, dimensions)
		if err != nil {
			return nil, err
		}

		vectors[item.Index] = vector
		seen[item.Index] = true
	}

	return vectors, nil
}

func validatedSimilarityEmbeddingVector(
	item similarityEmbeddingData,
	dimensions int,
) ([]float32, error) {
	if len(item.Embedding) == 0 {
		return nil, fmt.Errorf("llama-server embedding %d is empty", item.Index)
	}

	if len(item.Embedding) != dimensions {
		return nil, fmt.Errorf(
			"llama-server embedding %d has %d dimensions, want %d",
			item.Index,
			len(item.Embedding),
			dimensions,
		)
	}

	vector := make([]float32, dimensions)

	for dimension, value := range item.Embedding {
		if math.IsNaN(value) || math.IsInf(value, 0) ||
			value > math.MaxFloat32 || value < -math.MaxFloat32 {
			return nil, fmt.Errorf(
				"llama-server embedding %d dimension %d is not a finite float32",
				item.Index,
				dimension,
			)
		}

		vector[dimension] = float32(value)
	}

	return vector, nil
}

func (embedder *httpSimilarityEmbedder) close() error {
	embedder.client.CloseIdleConnections()

	return nil
}
