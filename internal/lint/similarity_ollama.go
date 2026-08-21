package lint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	similarityOllamaTimeout = 10 * time.Minute
	maxOllamaErrorBytes     = 4096
)

type ollamaEmbeddingClient struct {
	baseURL string
	http    *http.Client
}

type ollamaTagsResponse struct {
	Models []struct {
		Name   string `json:"name"`
		Model  string `json:"model"`
		Digest string `json:"digest"`
	} `json:"models"`
}

type ollamaEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

func newOllamaEmbeddingClient() *ollamaEmbeddingClient {
	host := strings.TrimSpace(os.Getenv("OLLAMA_HOST"))
	if host == "" {
		host = "http://127.0.0.1:11434"
	} else if !strings.Contains(host, "://") {
		host = "http://" + host
	}

	return &ollamaEmbeddingClient{
		baseURL: strings.TrimRight(host, "/"),
		http:    &http.Client{Timeout: similarityOllamaTimeout},
	}
}

func (client *ollamaEmbeddingClient) verifyModel() error {
	var response ollamaTagsResponse
	if err := client.getJSON("/api/tags", &response); err != nil {
		return fmt.Errorf(
			"semantic similarity requires local Ollama at %s: %w",
			client.baseURL,
			err,
		)
	}

	for _, model := range response.Models {
		if model.Name != similarityModelName && model.Model != similarityModelName {
			continue
		}

		if model.Digest != similarityModelDigest {
			return fmt.Errorf(
				"ollama model %s has digest %s, want %s",
				similarityModelName,
				model.Digest,
				similarityModelDigest,
			)
		}

		return nil
	}

	return fmt.Errorf(
		"ollama model %s is missing; run `ollama pull %s`",
		similarityModelName,
		similarityModelName,
	)
}

func (client *ollamaEmbeddingClient) embed(inputs []string) ([][]float32, error) {
	request := struct {
		Model     string   `json:"model"`
		Input     []string `json:"input"`
		Truncate  bool     `json:"truncate"`
		KeepAlive string   `json:"keep_alive"`
		Options   struct {
			NumThread int `json:"num_thread"`
		} `json:"options"`
	}{
		Model:     similarityModelName,
		Input:     inputs,
		Truncate:  false,
		KeepAlive: "5m",
	}
	request.Options.NumThread = runtime.GOMAXPROCS(0)

	var response ollamaEmbedResponse
	if err := client.postJSON("/api/embed", request, &response); err != nil {
		return nil, fmt.Errorf("embed code with Ollama: %w", err)
	}

	return response.Embeddings, nil
}

func (client *ollamaEmbeddingClient) getJSON(path string, output any) (err error) {
	endpoint, err := client.endpoint(path)
	if err != nil {
		return err
	}

	response, err := client.http.Get(endpoint)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, response.Body.Close())
	}()

	return decodeOllamaResponse(response, output)
}

func (client *ollamaEmbeddingClient) postJSON(path string, input, output any) (err error) {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}

	endpoint, err := client.endpoint(path)
	if err != nil {
		return err
	}

	response, err := client.http.Post(endpoint, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, response.Body.Close())
	}()

	return decodeOllamaResponse(response, output)
}

func (client *ollamaEmbeddingClient) endpoint(path string) (string, error) {
	base, err := url.Parse(client.baseURL)
	if err != nil {
		return "", err
	}

	if base.Scheme != "http" && base.Scheme != "https" {
		return "", fmt.Errorf("unsupported Ollama URL scheme %q", base.Scheme)
	}

	if base.Host == "" {
		return "", fmt.Errorf("invalid Ollama URL %q", client.baseURL)
	}

	base.Path = strings.TrimRight(base.Path, "/") + path

	return base.String(), nil
}

func decodeOllamaResponse(response *http.Response, output any) error {
	const maxResponseBytes = 64 << 20

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxOllamaErrorBytes))
		return fmt.Errorf("%s: %s", response.Status, strings.TrimSpace(string(body)))
	}

	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes))
	if err := decoder.Decode(output); err != nil {
		return err
	}

	return nil
}
