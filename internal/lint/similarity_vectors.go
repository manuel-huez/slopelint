package lint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func populateSimilarityScanVectors(
	ctx context.Context,
	scanBlocks []*similarityBlock,
	descriptionBlocks []*similarityBlock,
	descriptionRuntime codexRuntime,
	cacheRoot string,
	opts SimilarityOptions,
) (vectors similarityVectorMatrices, err error) {
	embeddings := &similarityEmbeddingRuntime{
		embedder:  opts.embedder,
		model:     opts.embeddingModel(),
		cacheRoot: cacheRoot,
		cache:     opts.CacheEnabled,
	}

	defer func() { err = errors.Join(err, embeddings.close()) }()

	if !descriptionRuntime.enabled {
		vectors.Source, err = embeddings.populate(scanBlocks, similaritySourceVector)
		return vectors, err
	}

	changed := make(map[*similarityBlock]struct{}, len(descriptionBlocks))
	for _, block := range descriptionBlocks {
		changed[block] = struct{}{}
	}

	var restored []*similarityBlock

	for _, block := range scanBlocks {
		if _, ok := changed[block]; !ok {
			restored = append(restored, block)
		}
	}

	// Queue at most one delivery per changed block plus restored metadata. These
	// pointers already belong to the scan; source inference must not stall Codex.
	// The buffer also lets embedding stop reading without blocking Codex workers.
	ready := make(chan []*similarityBlock, len(descriptionBlocks)+1)
	descriptionRuntime.ready = func(blocks []*similarityBlock) { ready <- blocks }

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var descriptionErr error

	go func() {
		defer close(ready)

		if len(restored) > 0 {
			ready <- restored
		}

		_, descriptionErr = populateSimilarityDescriptions(
			ctx,
			descriptionBlocks, descriptionRuntime, cacheRoot, opts.CacheEnabled,
		)
	}()

	vectors.Source, err = embeddings.populate(scanBlocks, similaritySourceVector)
	if err == nil {
		vectors.Description, err = embeddings.populateDescriptions(scanBlocks, ready)
	}

	if err != nil {
		// A failed scan cannot use more descriptions: stop Codex spend now and
		// wait for workers to exit. Accepted batches are already cached.
		cancel()

		for range ready {
		}

		if errors.Is(descriptionErr, context.Canceled) {
			descriptionErr = nil
		}
	}

	err = errors.Join(err, descriptionErr)
	if err != nil {
		return similarityVectorMatrices{}, err
	}

	return vectors, nil
}

func similarityEmbeddingInputs(content string) []string {
	if len(content) <= similarityEmbeddingChunkBytes {
		return []string{content}
	}

	chunks := make([]string, 0, 1+len(content)/similarityEmbeddingChunkBytes)

	for start := 0; start < len(content); {
		end := min(start+similarityEmbeddingChunkBytes, len(content))
		if end < len(content) {
			// Prefer a nearby line boundary so each fragment remains useful code context.
			searchStart := max(
				start+similarityEmbeddingChunkBytes/2,
				end-similarityEmbeddingLineSearch,
			)
			if newline := strings.LastIndexByte(content[searchStart:end], '\n'); newline >= 0 {
				end = searchStart + newline + 1
			}

			for end > start && !utf8.RuneStart(content[end]) {
				end--
			}
		}

		chunks = append(chunks, content[start:end])
		if end == len(content) {
			break
		}

		start = max(start+1, end-similarityEmbeddingChunkOverlap)
		for start < end && !utf8.RuneStart(content[start]) {
			start++
		}
	}

	return chunks
}

type similarityEmbeddingRuntime struct {
	embedder     similarityEmbedder
	model        string
	cacheRoot    string
	cache        bool
	ownsEmbedder bool
}

func (runtime *similarityEmbeddingRuntime) populate(
	blocks []*similarityBlock,
	kind similarityVectorKind,
) (similarityVectorMatrix, error) {
	blockInputs, inputs := similarityVectorInputs(blocks, kind, runtime.model, nil)
	if err := runtime.populateInputs(inputs); err != nil {
		return similarityVectorMatrix{}, err
	}

	return packSimilarityVectors(blocks, blockInputs, kind)
}

func (runtime *similarityEmbeddingRuntime) populateDescriptions(
	blocks []*similarityBlock,
	ready <-chan []*similarityBlock,
) (similarityVectorMatrix, error) {
	byKey := make(map[string]*similarityVectorInput)
	byBlock := make(map[*similarityBlock][]*similarityVectorInput, len(blocks))

	for batch := range ready {
		blockInputs, inputs := similarityVectorInputs(
			batch,
			similarityDescriptionVector,
			runtime.model,
			byKey,
		)
		if err := runtime.populateInputs(inputs); err != nil {
			return similarityVectorMatrix{}, err
		}

		for index, block := range batch {
			byBlock[block] = blockInputs[index]
		}
	}

	// Deduplicate across deliveries, then pack once in scan order. Completion
	// order must not change vector indices or re-embed shared signatures.
	blockInputs := make([][]*similarityVectorInput, len(blocks))
	for index, block := range blocks {
		blockInputs[index] = byBlock[block]
		if len(blockInputs[index]) == 0 && similarityDescribed(block.RelativePath) {
			return similarityVectorMatrix{}, fmt.Errorf(
				"description missing for %s",
				block.Identity,
			)
		}
	}

	return packSimilarityVectors(blocks, blockInputs, similarityDescriptionVector)
}

func (runtime *similarityEmbeddingRuntime) populateInputs(inputs []*similarityVectorInput) error {
	missing := loadCachedSimilarityVectors(inputs, runtime.cacheRoot, runtime.cache)

	if len(missing) > 0 {
		// Encoder batches share token work with their longest sequence. Length order
		// keeps large functions from padding batches of short functions.
		sort.Slice(missing, func(i, j int) bool {
			if len(missing[i].Content) != len(missing[j].Content) {
				return len(missing[i].Content) < len(missing[j].Content)
			}

			return missing[i].CacheKey < missing[j].CacheKey
		})

		if err := runtime.ensureEmbedder(); err != nil {
			return err
		}

		started := time.Now()

		for start := 0; start < len(missing); {
			end := start
			bytes := 0

			for end < len(missing) && end-start < similarityEmbeddingBatchSize {
				nextBytes := len(missing[end].Content)
				if end > start && bytes+nextBytes > similarityEmbeddingBatchBytes {
					break
				}

				bytes += nextBytes
				end++
			}

			if err := populateSimilarityVectorBatch(
				missing[start:end],
				runtime.embedder,
				runtime.cacheRoot,
				runtime.cache,
			); err != nil {
				return err
			}

			start = end
		}

		_, _ = fmt.Fprintf(
			os.Stderr,
			"slopelint: embedded %d code blocks with %s%s\n",
			len(missing),
			runtime.model,
			" in "+time.Since(started).Round(time.Second).String(),
		)
	}

	return nil
}

func (runtime *similarityEmbeddingRuntime) ensureEmbedder() error {
	if runtime.embedder != nil {
		return nil
	}

	embedder, err := newHTTPSimilarityEmbedder(os.Getenv(similarityLlamaURLEnv), runtime.model)
	if err != nil {
		return err
	}

	runtime.embedder = embedder
	runtime.ownsEmbedder = true

	return nil
}

func (runtime *similarityEmbeddingRuntime) close() error {
	if !runtime.ownsEmbedder {
		return nil
	}

	return runtime.embedder.close()
}

func similarityVectorInputs(
	blocks []*similarityBlock,
	kind similarityVectorKind,
	model string,
	byKey map[string]*similarityVectorInput,
) ([][]*similarityVectorInput, []*similarityVectorInput) {
	if byKey == nil {
		byKey = make(map[string]*similarityVectorInput, len(blocks))
	}

	var inputs []*similarityVectorInput

	blockInputs := make([][]*similarityVectorInput, len(blocks))

	for blockIndex, block := range blocks {
		content := block.Content
		locationKind := "source"

		if kind == similarityDescriptionVector {
			content = block.Description
			locationKind = "description"
		}

		if content == "" {
			continue
		}

		chunks := similarityEmbeddingInputs(content)
		blockInputs[blockIndex] = make([]*similarityVectorInput, 0, len(chunks))
		chunked := len(chunks) > 1

		for index, content := range chunks {
			key := similarityVectorCacheKey(content, kind, chunked, model)

			input := byKey[key]
			if input == nil {
				input = &similarityVectorInput{
					Content:  content,
					CacheKey: key,
					Location: fmt.Sprintf(
						"%s %s chunk %d",
						block.Identity,
						locationKind,
						index+1,
					),
				}
				byKey[key] = input
				inputs = append(inputs, input)
			}

			blockInputs[blockIndex] = append(blockInputs[blockIndex], input)
		}
	}

	sort.Slice(inputs, func(i, j int) bool { return inputs[i].CacheKey < inputs[j].CacheKey })

	return blockInputs, inputs
}

func similarityVectorCacheKey(
	content string,
	kind similarityVectorKind,
	chunked bool,
	model string,
) string {
	cacheContent := content
	if chunked {
		cacheContent = similarityChunkCachePrefix + content
	}

	if kind == similarityDescriptionVector {
		cacheContent = similarityDescriptionVectorPrefix + cacheContent
	}

	// The model name keys vectors so models never share cached vectors.
	fingerprint := model + "\x00" + strconv.Itoa(
		similarityVectorInputSchema,
	) + "\x00" + cacheContent
	sum := sha256.Sum256([]byte(fingerprint))

	return hex.EncodeToString(sum[:])
}

func loadCachedSimilarityVectors(
	inputs []*similarityVectorInput,
	cacheRoot string,
	cacheEnabled bool,
) []*similarityVectorInput {
	missing := make([]*similarityVectorInput, 0)

	for _, input := range inputs {
		if cacheEnabled {
			if vector, ok := loadSimilarityVector(cacheRoot, input.CacheKey); ok {
				input.Vector = vector
				continue
			}
		}

		missing = append(missing, input)
	}

	return missing
}

func populateSimilarityVectorBatch(
	batch []*similarityVectorInput,
	embedder similarityEmbedder,
	cacheRoot string,
	cacheEnabled bool,
) error {
	inputs := make([]string, len(batch))
	for index, input := range batch {
		inputs[index] = input.Content
	}

	vectors, err := embedder.embed(inputs)
	if err != nil {
		return err
	}

	if len(vectors) != len(inputs) {
		return fmt.Errorf(
			"embedding engine returned %d embeddings for %d code blocks",
			len(vectors),
			len(inputs),
		)
	}

	for index, input := range batch {
		vector := vectors[index]
		if err := normalizeSimilarityVector(vector); err != nil {
			return fmt.Errorf("embedding %s: %w", input.Location, err)
		}

		input.Vector = vector

		if cacheEnabled {
			_ = storeSimilarityVector(cacheRoot, input.CacheKey, vector)
		}
	}

	return nil
}

func packSimilarityVectors(
	blocks []*similarityBlock,
	blockInputs [][]*similarityVectorInput,
	kind similarityVectorKind,
) (similarityVectorMatrix, error) {
	dimensions := 0
	totalVectors := 0

	for _, inputs := range blockInputs {
		totalVectors += len(inputs)
		for _, input := range inputs {
			if dimensions == 0 {
				dimensions = len(input.Vector)
			}

			if len(input.Vector) != dimensions {
				return similarityVectorMatrix{}, fmt.Errorf(
					"embedding %s has %d dimensions, want %d",
					input.Location,
					len(input.Vector),
					dimensions,
				)
			}
		}
	}

	// One flat matrix gives pair workers sequential float32 reads and avoids one
	// allocation plus pointer chase for every cached chunk.
	matrix := similarityVectorMatrix{
		Values:     make([]float32, totalVectors*dimensions),
		Dimensions: dimensions,
	}
	vectorIndex := 0

	for blockIndex, inputs := range blockInputs {
		if kind == similarityDescriptionVector {
			blocks[blockIndex].DescriptionVectorStart = vectorIndex
			blocks[blockIndex].DescriptionVectorCount = len(inputs)
		} else {
			blocks[blockIndex].VectorStart = vectorIndex
			blocks[blockIndex].VectorCount = len(inputs)
		}

		for _, input := range inputs {
			copy(
				matrix.Values[vectorIndex*dimensions:(vectorIndex+1)*dimensions],
				input.Vector,
			)
			vectorIndex++
		}
	}

	return matrix, nil
}

func normalizeSimilarityVector(vector []float32) error {
	if len(vector) == 0 {
		return errors.New("empty vector")
	}

	var norm float64

	for _, value := range vector {
		floatValue := float64(value)
		if math.IsNaN(floatValue) || math.IsInf(floatValue, 0) {
			return errors.New("non-finite vector value")
		}

		norm += floatValue * floatValue
	}

	if norm == 0 {
		return errors.New("zero vector")
	}

	// Normalize once at engine boundary so cache format and threshold behavior
	// do not depend on model-runner output conventions.
	inverseNorm := 1 / math.Sqrt(norm)
	for index, value := range vector {
		vector[index] = float32(float64(value) * inverseNorm)
	}

	return nil
}
