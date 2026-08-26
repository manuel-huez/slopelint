//go:build linux && amd64 && cgo

package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	llama "github.com/tcpipuk/llama-go"
)

const (
	modelBenchmarkSource      = "source"
	modelBenchmarkDescription = "description"
)

type modelBenchmarkPair struct {
	Name             string `json:"name"`
	Left             string `json:"left"`
	Right            string `json:"right"`
	LeftDescription  string `json:"left_description"`
	RightDescription string `json:"right_description"`
	Test             bool   `json:"test"`
	Tier             int    `json:"tier"`
}

type modelBenchmarkConfig struct {
	Corpus    string `json:"corpus"`
	Output    string `json:"output"`
	ModelPath string `json:"model_path"`
	Prefix    string `json:"prefix"`
	MaxTokens int    `json:"max_tokens"`
}

// TestSimilarityModelBenchmark is opt-in: ordinary tests never download models
// or generate signatures. Candidate vectors must never enter the Jina cache.
func TestSimilarityModelBenchmark(t *testing.T) {
	configPath := os.Getenv("SLOPELINT_BENCHMARK_CONFIG")
	if configPath == "" {
		t.Skip("set SLOPELINT_BENCHMARK_CONFIG for the isolated model benchmark")
	}

	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	var config modelBenchmarkConfig
	if err := json.Unmarshal(configData, &config); err != nil {
		t.Fatal(err)
	}

	pairs, blocks, tokenCounts, corpusDigest := modelBenchmarkBlocks(t, config.Corpus)
	result := map[string]any{"tokens": tokenCounts, "corpus_sha256": corpusDigest}
	start := time.Now()

	if config.ModelPath == "" {
		modelBenchmarkDescriptions(t, blocks)

		result["signature_seconds"] = time.Since(start).Seconds()
		result["records"] = blocks
	} else {
		modelBenchmarkScore(t, config, pairs, blocks, result)
	}

	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}

	result["peak_rss_kib"] = usage.Maxrss

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(config.Output, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func modelBenchmarkDescriptions(t *testing.T, blocks []*similarityBlock) {
	t.Helper()

	var missing []*similarityBlock

	for _, block := range blocks {
		if block.Description == "" {
			missing = append(missing, block)
		}
	}

	if len(missing) == 0 {
		return
	}

	path, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}

	_, err = populateSimilarityDescriptions(missing, similarityDescriptionRuntime{
		describer: &codexSimilarityDescriber{path: path}, enabled: true,
	}, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
}

func modelBenchmarkBlocks(
	t *testing.T,
	path string,
) ([]modelBenchmarkPair, []*similarityBlock, []int, string) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var pairs []modelBenchmarkPair
	if err := json.Unmarshal(data, &pairs); err != nil {
		t.Fatal(err)
	}

	var (
		blocks      []*similarityBlock
		tokenCounts []int
	)

	for _, pair := range pairs {
		for side, source := range []string{pair.Left, pair.Right} {
			fset := token.NewFileSet()

			file, err := parser.ParseFile(fset, "sample.go", "package fixture\n"+source, 0)
			if err != nil {
				t.Fatal(err)
			}

			content, tokens, err := renderSimilarityFunction(fset, file.Decls[0].(*ast.FuncDecl))
			if err != nil {
				t.Fatal(err)
			}

			hash := sha256.Sum256([]byte(content))

			description := pair.LeftDescription
			if side != 0 {
				description = pair.RightDescription
			}

			blocks = append(blocks, &similarityBlock{
				Identity: pair.Name + string(rune('a'+side)), Content: content,
				ContentHash: hex.EncodeToString(hash[:]), IsTest: pair.Test,
				Description: description,
			})
			tokenCounts = append(tokenCounts, len(tokens))
		}
	}

	digest := sha256.Sum256(data)

	return pairs, blocks, tokenCounts, hex.EncodeToString(digest[:])
}

func modelBenchmarkScore(
	t *testing.T,
	config modelBenchmarkConfig,
	pairs []modelBenchmarkPair,
	blocks []*similarityBlock,
	result map[string]any,
) {
	t.Helper()

	if config.MaxTokens < 1 || config.MaxTokens > nativeEmbeddingBatch {
		t.Fatal("max_tokens must fit the native micro-batch")
	}

	start := time.Now()

	model, err := llama.LoadModel(
		config.ModelPath,
		llama.WithGPULayers(0),
		llama.WithMMap(true),
		llama.WithSilentLoading(),
	)
	if err != nil {
		t.Fatal(err)
	}

	context, err := model.NewContext(
		llama.WithContext(
			nativeEmbeddingContext,
		),
		llama.WithBatch(nativeEmbeddingBatch),
		llama.WithUBatch(nativeEmbeddingBatch),
		llama.WithThreads(physicalCPUCount()),
		llama.WithThreadsBatch(physicalCPUCount()),
		llama.WithParallel(nativeEmbeddingParallel),
		llama.WithEmbeddings(),
	)
	if err != nil {
		_ = model.Close()

		t.Fatal(err)
	}

	engine := &nativeSimilarityEmbedder{model: model, context: context}

	t.Cleanup(func() {
		if err := engine.close(); err != nil {
			t.Error(err)
		}
	})

	result["load_seconds"] = time.Since(start).Seconds()
	runtime := newSimilarityEmbeddingRuntime(engine, t.TempDir(), false)
	source := modelBenchmarkVectors(t, config, blocks, similaritySourceVector, runtime, result)
	descriptions := modelBenchmarkVectors(
		t,
		config,
		blocks,
		similarityDescriptionVector,
		runtime,
		result,
	)

	scores := make([]map[string]any, 0, len(pairs))
	for i, pair := range pairs {
		left, right := blocks[2*i], blocks[2*i+1]
		a, _, _ := maximumDotSimilarity(source, left, right)
		d := maximumDescriptionDotSimilarity(descriptions, left, right)
		scores = append(scores, map[string]any{
			"name": pair.Name, modelBenchmarkSource: a, modelBenchmarkDescription: d,
			"source_match":      a >= similarityEmbeddingThreshold(pair.Tier, pair.Test),
			"description_match": d >= similarityDescriptionThreshold(pair.Tier, pair.Test),
		})
	}

	result["scores"] = scores
}

func modelBenchmarkVectors(
	t *testing.T,
	config modelBenchmarkConfig,
	blocks []*similarityBlock,
	kind similarityVectorKind,
	runtime *similarityEmbeddingRuntime,
	result map[string]any,
) similarityVectorMatrix {
	t.Helper()

	start := time.Now()
	blockInputs, inputs := similarityVectorInputs(blocks, kind, nil)
	engine := runtime.embedder.(*nativeSimilarityEmbedder)
	maxTokens := 0
	inputLimit := min(config.MaxTokens, nativeEmbeddingContext/nativeEmbeddingParallel)

	for _, input := range inputs {
		input.Content = config.Prefix + input.Content

		tokens, err := engine.context.Tokenize(input.Content)
		if err != nil {
			t.Fatal(err)
		}
		// Keep identical chunks across candidates; reject instead of truncating or
		// silently changing the scoring policy for a short-context model.
		if len(tokens) > inputLimit {
			t.Fatalf("%s: %d tokens exceed limit %d", input.Location, len(tokens), inputLimit)
		}

		maxTokens = max(maxTokens, len(tokens))
	}

	label := modelBenchmarkSource
	if kind == similarityDescriptionVector {
		label = modelBenchmarkDescription
	}

	result[label+"_prepare_seconds"] = time.Since(start).Seconds()
	result[label+"_inputs"] = len(inputs)
	result[label+"_max_tokens"] = maxTokens
	// Repeat inference on the same loaded model; clear vectors to avoid cache hits.
	var timings []float64

	for range 2 {
		for _, input := range inputs {
			input.Vector = nil
		}

		start = time.Now()

		if err := runtime.populateInputs(inputs); err != nil {
			t.Fatal(err)
		}

		elapsed := time.Since(start).Seconds()
		timings = append(timings, elapsed)
		t.Logf("%s pass %d: %.3fs, %d inputs", label, len(timings), elapsed, len(inputs))
	}

	result[label+"_seconds"] = timings

	matrix, err := packSimilarityVectors(blocks, blockInputs, kind)
	if err != nil {
		t.Fatal(err)
	}

	result[label+"_dimensions"] = matrix.Dimensions

	return matrix
}
