package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

func populateSimilarityScanVectors(
	scanBlocks []*similarityBlock,
	descriptionBlocks []*similarityBlock,
	allBlocks []*similarityBlock,
	descriptionRuntime similarityDescriptionRuntime,
	cacheRoot string,
	opts SimilarityOptions,
) (vectors similarityVectorMatrices, descriptionDigest string, err error) {
	embeddings := newSimilarityEmbeddingRuntime(opts.embedder, cacheRoot, opts.CacheEnabled)
	defer func() { err = errors.Join(err, embeddings.close()) }()

	if !descriptionRuntime.enabled {
		vectors.Source, err = embeddings.populate(scanBlocks, similaritySourceVector)
		return vectors, "", err
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
	ready := make(chan []*similarityBlock, len(descriptionBlocks)+1)
	descriptionRuntime.ready = func(blocks []*similarityBlock) { ready <- blocks }

	var descriptionErr error

	go func() {
		defer close(ready)

		if len(restored) > 0 {
			ready <- restored
		}

		_, descriptionErr = populateSimilarityDescriptions(
			descriptionBlocks, descriptionRuntime, cacheRoot, opts.CacheEnabled,
		)
		if descriptionErr == nil {
			descriptionDigest, descriptionErr = similarityDescriptionsDigest(allBlocks)
		}
	}()

	vectors.Source, err = embeddings.populate(scanBlocks, similaritySourceVector)
	if err != nil {
		// Drain even on failure so producers can finish persisting valid records.
		for range ready {
		}
	} else {
		vectors.Description, err = embeddings.populateDescriptions(scanBlocks, ready)
	}

	err = errors.Join(err, descriptionErr)
	if err != nil {
		return similarityVectorMatrices{}, "", err
	}

	return vectors, descriptionDigest, nil
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
	embedder   similarityEmbedder
	cacheRoot  string
	cache      bool
	ownsEngine bool
}

func newSimilarityEmbeddingRuntime(
	embedder similarityEmbedder,
	cacheRoot string,
	cacheEnabled bool,
) *similarityEmbeddingRuntime {
	return &similarityEmbeddingRuntime{
		embedder:  embedder,
		cacheRoot: cacheRoot,
		cache:     cacheEnabled,
	}
}

func (runtime *similarityEmbeddingRuntime) populate(
	blocks []*similarityBlock,
	kind similarityVectorKind,
) (similarityVectorMatrix, error) {
	blockInputs, inputs := similarityVectorInputs(blocks, kind, nil)
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

	var err error
	for batch := range ready {
		if err != nil {
			continue
		}

		blockInputs, inputs := similarityVectorInputs(batch, similarityDescriptionVector, byKey)
		err = runtime.populateInputs(inputs)

		for index, block := range batch {
			byBlock[block] = blockInputs[index]
		}
	}

	if err != nil {
		return similarityVectorMatrix{}, err
	}

	// Deduplicate across deliveries, then pack once in scan order. Completion
	// order must not change vector indices or re-embed shared signatures.
	blockInputs := make([][]*similarityVectorInput, len(blocks))
	for index, block := range blocks {
		blockInputs[index] = byBlock[block]
		if len(blockInputs[index]) == 0 {
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

		if err := runtime.ensureEngine(); err != nil {
			return err
		}

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
	}

	return nil
}

func (runtime *similarityEmbeddingRuntime) ensureEngine() error {
	if runtime.embedder != nil {
		return nil
	}

	// Type graphs were released before inference. Reclaim them only when model
	// allocation is necessary; fully cached incremental scans skip this pause.
	debug.FreeOSMemory()

	embedder, err := newNativeSimilarityEmbedder(runtime.cacheRoot)
	if err != nil {
		return err
	}

	if embedder == nil {
		return errors.New("embedding engine is unavailable")
	}

	runtime.embedder = embedder
	runtime.ownsEngine = true

	return nil
}

func (runtime *similarityEmbeddingRuntime) close() error {
	if !runtime.ownsEngine {
		return nil
	}

	return runtime.embedder.close()
}

func similarityVectorInputs(
	blocks []*similarityBlock,
	kind similarityVectorKind,
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
			key := similarityVectorCacheKey(content, kind, chunked)

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
) string {
	cacheContent := content
	if chunked {
		cacheContent = similarityChunkCachePrefix + content
	}

	if kind == similarityDescriptionVector {
		cacheContent = similarityDescriptionVectorPrefix + cacheContent
	}

	fingerprint := similarityModelDigest + "\x00" + strconv.Itoa(
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
