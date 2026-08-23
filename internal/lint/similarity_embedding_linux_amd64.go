//go:build linux && amd64 && cgo

package lint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	llama "github.com/tcpipuk/llama-go"
)

const (
	nativeEmbeddingParallel = 4
	nativeEmbeddingContext  = 4096
	// Jina is an encoder, so every llama_decode batch must fit the micro-batch.
	// One shared limit makes llama-go flush before llama.cpp can abort.
	nativeEmbeddingBatch      = 2048
	linuxCPUAllowedListPrefix = "Cpus_allowed_list:"
	linuxCPUTopologyRoot      = "/sys/devices/system/cpu"
)

var configureNativeEmbeddingLogs sync.Once

type nativeSimilarityEmbedder struct {
	model   *llama.Model
	context *llama.Context
}

func newNativeSimilarityEmbedder(cacheRoot string) (similarityEmbedder, error) {
	modelPath, err := acquireSimilarityModel(cacheRoot)
	if err != nil {
		return nil, err
	}

	configureNativeEmbeddingLogs.Do(func() {
		if _, configured := os.LookupEnv("LLAMA_LOG"); configured {
			return
		}

		_ = os.Setenv("LLAMA_LOG", "error")

		llama.InitLogging()

		_ = os.Unsetenv("LLAMA_LOG")
	})

	model, err := llama.LoadModel(
		modelPath,
		llama.WithGPULayers(0),
		llama.WithMMap(true),
		llama.WithSilentLoading(),
	)
	if err != nil {
		return nil, fmt.Errorf("load similarity model: %w", err)
	}

	threads := physicalCPUCount()

	context, err := model.NewContext(
		llama.WithContext(nativeEmbeddingContext),
		llama.WithBatch(nativeEmbeddingBatch),
		llama.WithUBatch(nativeEmbeddingBatch),
		llama.WithThreads(threads),
		llama.WithThreadsBatch(threads),
		llama.WithParallel(nativeEmbeddingParallel),
		llama.WithEmbeddings(),
	)
	if err != nil {
		_ = model.Close()

		return nil, fmt.Errorf("create similarity model context: %w", err)
	}

	return &nativeSimilarityEmbedder{model: model, context: context}, nil
}

func (embedder *nativeSimilarityEmbedder) embed(inputs []string) ([][]float32, error) {
	vectors, err := embedder.context.GetEmbeddingsBatch(inputs)
	if err != nil {
		return nil, fmt.Errorf("embed code with llama.cpp: %w", err)
	}

	return vectors, nil
}

func (embedder *nativeSimilarityEmbedder) close() error {
	return errors.Join(embedder.context.Close(), embedder.model.Close())
}

func physicalCPUCount() int {
	logicalLimit := runtime.GOMAXPROCS(0)

	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return logicalLimit
	}

	var allowed map[int]struct{}

	for line := range strings.SplitSeq(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, linuxCPUAllowedListPrefix); ok {
			allowed = parseCPUList(strings.TrimSpace(value))
			break
		}
	}

	if len(allowed) == 0 {
		return logicalLimit
	}

	cores := make(map[string]struct{}, len(allowed))
	for cpu := range allowed {
		topology := filepath.Join(
			linuxCPUTopologyRoot,
			"cpu"+strconv.Itoa(cpu),
			"topology",
		)
		packageID, packageErr := os.ReadFile(filepath.Join(topology, "physical_package_id"))

		coreID, coreErr := os.ReadFile(filepath.Join(topology, "core_id"))
		if packageErr != nil || coreErr != nil {
			return logicalLimit
		}

		cores[strings.TrimSpace(string(packageID))+":"+strings.TrimSpace(string(coreID))] = struct{}{}
	}

	return max(1, min(len(cores), logicalLimit))
}

func parseCPUList(value string) map[int]struct{} {
	result := make(map[int]struct{})

	for part := range strings.SplitSeq(value, ",") {
		firstText, lastText, ranged := strings.Cut(strings.TrimSpace(part), "-")

		first, err := strconv.Atoi(firstText)
		if err != nil || first < 0 {
			return nil
		}

		last := first
		if ranged {
			last, err = strconv.Atoi(lastText)
			if err != nil || last < first {
				return nil
			}
		}

		for cpu := first; cpu <= last; cpu++ {
			result[cpu] = struct{}{}
		}
	}

	return result
}
