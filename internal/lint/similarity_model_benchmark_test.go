//go:build linux && amd64 && cgo

package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	llama "github.com/tcpipuk/llama-go"
)

const (
	modelBenchmarkSource        = "source"
	modelBenchmarkDescription   = "description"
	modelBenchmarkNormTolerance = 1e-5
)

type modelBenchmarkPair struct {
	Name                string `json:"name"`
	Left                string `json:"left"`
	Right               string `json:"right"`
	LeftDescription     string `json:"left_description"`
	RightDescription    string `json:"right_description"`
	LeftNormalizedHash  string `json:"left_normalized_sha256"`
	RightNormalizedHash string `json:"right_normalized_sha256"`
	Test                bool   `json:"test"`
	Tier                int    `json:"tier"`
}

type modelBenchmarkConfig struct {
	Corpus            string `json:"corpus"`
	Output            string `json:"output"`
	ModelPath         string `json:"model_path"`
	Vectors           string `json:"vectors"`
	SourcePrefix      string `json:"source_prefix"`
	DescriptionPrefix string `json:"description_prefix"`
	MaxTokens         int    `json:"max_tokens"`
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

	switch {
	case config.Vectors != "":
		result = modelBenchmarkImportedVectors(t, config, pairs, blocks, corpusDigest)
	case config.ModelPath == "":
		modelBenchmarkExport(t, pairs, blocks, result)
	default:
		modelBenchmarkScore(t, config, pairs, blocks, result)
	}

	if config.Vectors == "" {
		var usage syscall.Rusage
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
			t.Fatal(err)
		}

		result["peak_rss_kib"] = usage.Maxrss
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(config.Output, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func modelBenchmarkExport(
	t *testing.T,
	pairs []modelBenchmarkPair,
	blocks []*similarityBlock,
	result map[string]any,
) {
	t.Helper()

	start := time.Now()

	modelBenchmarkDescriptions(t, blocks)

	result["signature_seconds"] = time.Since(start).Seconds()
	result["records"] = blocks
	// Reference runtimes consume the owner's chunks and thresholds, not copies
	// of production normalization or scoring-policy constants.
	for label, kind := range map[string]similarityVectorKind{
		modelBenchmarkSource:      similaritySourceVector,
		modelBenchmarkDescription: similarityDescriptionVector,
	} {
		byBlock, _ := similarityVectorInputs(blocks, kind, nil)
		chunks := make([][]string, len(blocks))

		for index, inputs := range byBlock {
			for _, input := range inputs {
				chunks[index] = append(chunks[index], input.Content)
			}
		}

		result[label+"_chunks"] = chunks
	}

	thresholds := make([]map[string]any, 0, len(pairs))
	for _, pair := range pairs {
		thresholds = append(thresholds, map[string]any{
			"name":        pair.Name,
			"source":      similarityEmbeddingThreshold(pair.Tier, pair.Test),
			"description": similarityDescriptionThreshold(pair.Tier, pair.Test),
		})
	}

	result["thresholds"] = thresholds
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
			contentHash := hex.EncodeToString(hash[:])

			expectedHash := pair.LeftNormalizedHash
			if side != 0 {
				expectedHash = pair.RightNormalizedHash
			}

			if contentHash != expectedHash {
				t.Fatalf("normalized source changed for %s side %d", pair.Name, side)
			}

			description := pair.LeftDescription
			if side != 0 {
				description = pair.RightDescription
			}

			blocks = append(blocks, &similarityBlock{
				Identity: pair.Name + string(rune('a'+side)), Content: content,
				ContentHash: contentHash, IsTest: pair.Test,
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

	result["scores"] = modelBenchmarkPairScores(pairs, blocks, source, descriptions)
}

func modelBenchmarkPairScores(
	pairs []modelBenchmarkPair,
	blocks []*similarityBlock,
	source, descriptions similarityVectorMatrix,
) []map[string]any {
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

	return scores
}

func modelBenchmarkImportedVectors(
	t *testing.T,
	config modelBenchmarkConfig,
	pairs []modelBenchmarkPair,
	blocks []*similarityBlock,
	digest string,
) map[string]any {
	t.Helper()

	data, err := os.ReadFile(config.Vectors)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}

	if result["corpus_sha256"] != digest {
		t.Fatal("vectors belong to another corpus")
	}

	var vectors struct {
		Source      map[string][]float32 `json:"source_vectors"`
		Description map[string][]float32 `json:"description_vectors"`
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}

	var matrices []similarityVectorMatrix

	for _, channel := range []struct {
		Kind    similarityVectorKind
		Prefix  string
		Vectors map[string][]float32
	}{
		{similaritySourceVector, config.SourcePrefix, vectors.Source},
		{similarityDescriptionVector, config.DescriptionPrefix, vectors.Description},
	} {
		byBlock, inputs := similarityVectorInputs(blocks, channel.Kind, nil)
		for _, input := range inputs {
			hash := sha256.Sum256([]byte(channel.Prefix + input.Content))

			vector, ok := channel.Vectors[hex.EncodeToString(hash[:])]
			if !ok {
				t.Fatal("missing vector for exported input")
			}

			var norm float64
			for _, value := range vector {
				norm += float64(value) * float64(value)
			}
			// Engines normalize once. Validate without changing stored float32 values.
			if math.IsNaN(norm) || math.Abs(norm-1) > modelBenchmarkNormTolerance {
				t.Fatal("imported vector must be finite and normalized")
			}

			input.Vector = vector
		}

		matrix, err := packSimilarityVectors(blocks, byBlock, channel.Kind)
		if err != nil {
			t.Fatal(err)
		}

		matrices = append(matrices, matrix)
	}

	result["scores"] = modelBenchmarkPairScores(pairs, blocks, matrices[0], matrices[1])

	return result
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

	prefix := config.SourcePrefix
	if kind == similarityDescriptionVector {
		prefix = config.DescriptionPrefix
	}

	for _, input := range inputs {
		input.Content = prefix + input.Content

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
