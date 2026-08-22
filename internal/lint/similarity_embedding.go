package lint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	similarityModelBytes = 322997312
	similarityModelURL   = "https://registry.ollama.ai/v2/unclemusclez/jina-embeddings-v2-base-code/blobs/sha256:" + similarityModelDigest
	modelDownloadTimeout = 30 * time.Minute
	maxModelErrorBytes   = 4096
	bytesPerMiB          = 1024 * 1024
	modelDirectoryMode   = 0o755
	modelFileMode        = 0o644
)

type similarityEmbedder interface {
	embed([]string) ([][]float32, error)
	close() error
}

func acquireSimilarityModel(cacheRoot string) (string, error) {
	modelDir := filepath.Join(filepath.Dir(cacheRoot), "models")
	modelPath := filepath.Join(modelDir, similarityModelDigest+".gguf")

	valid, err := validSimilarityModel(
		modelPath,
		similarityModelDigest,
		similarityModelBytes,
	)
	if err != nil {
		return "", err
	}

	if valid {
		return modelPath, nil
	}

	if err := os.MkdirAll(modelDir, modelDirectoryMode); err != nil {
		return "", fmt.Errorf("create similarity model cache: %w", err)
	}

	_, _ = fmt.Fprintf(
		os.Stderr,
		"slopelint: downloading semantic model once (%d MiB)\n",
		similarityModelBytes/bytesPerMiB,
	)

	client := &http.Client{Timeout: modelDownloadTimeout}
	if err := downloadSimilarityModel(
		modelPath,
		similarityModelURL,
		similarityModelDigest,
		similarityModelBytes,
		client,
	); err != nil {
		return "", err
	}

	return modelPath, nil
}

func validSimilarityModel(path, digest string, size int64) (valid bool, err error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	if !info.Mode().IsRegular() || info.Size() != size {
		return false, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false, err
	}

	return hex.EncodeToString(hash.Sum(nil)) == digest, nil
}

func downloadSimilarityModel(
	path string,
	modelURL string,
	digest string,
	size int64,
	client *http.Client,
) (err error) {
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		modelURL,
		nil,
	)
	if err != nil {
		return err
	}

	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download similarity model: %w", err)
	}
	defer func() {
		err = errors.Join(err, response.Body.Close())
	}()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxModelErrorBytes))

		return fmt.Errorf(
			"download similarity model: %s: %s",
			response.Status,
			string(body),
		)
	}

	if response.ContentLength >= 0 && response.ContentLength != size {
		return fmt.Errorf(
			"download similarity model: received %d bytes, want %d",
			response.ContentLength,
			size,
		)
	}

	return installSimilarityModel(path, response.Body, digest, size)
}

func installSimilarityModel(
	path string,
	source io.Reader,
	digest string,
	size int64,
) (err error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".model-*.tmp")
	if err != nil {
		return err
	}

	keepTemporary := false
	temporaryClosed := false

	defer func() {
		if !temporaryClosed {
			err = errors.Join(err, temporary.Close())
		}

		if !keepTemporary {
			_ = os.Remove(temporary.Name())
		}
	}()

	hash := sha256.New()

	written, err := io.Copy(
		io.MultiWriter(temporary, hash),
		io.LimitReader(source, size+1),
	)
	if err != nil {
		return fmt.Errorf("download similarity model: %w", err)
	}

	if written != size {
		return fmt.Errorf(
			"download similarity model: received %d bytes, want %d",
			written,
			size,
		)
	}

	if got := hex.EncodeToString(hash.Sum(nil)); got != digest {
		return fmt.Errorf("download similarity model: digest %s, want %s", got, digest)
	}

	if err := temporary.Sync(); err != nil {
		return err
	}

	if err := temporary.Close(); err != nil {
		return err
	}

	temporaryClosed = true

	if err := os.Chmod(temporary.Name(), modelFileMode); err != nil {
		return err
	}

	if err := os.Rename(temporary.Name(), path); err != nil {
		return fmt.Errorf("install similarity model: %w", err)
	}

	keepTemporary = true

	return nil
}
