package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	similarityDescriptionPromptSchema = 2
	similarityDescriptionModel        = "gpt-5.6-luna"
	similarityDescriptionEffort       = "medium"
	similarityDescriptionEnv          = "SLOPELINT_CODEX_DESCRIPTIONS"

	similarityDescriptionProduction = "production"
	similarityDescriptionTest       = "test"

	similarityDescriptionSummaryMaxChars = 200
	similarityDescriptionSubjectMaxChars = 160
	similarityDescriptionItemMaxChars    = 180
	similarityDescriptionMinimumWords    = 6
	similarityDescriptionMaximumWords    = 32
	similarityDescriptionMaximumInputs   = 5
	similarityDescriptionMaximumOutputs  = 4
	similarityDescriptionMaximumSteps    = 6
	similarityDescriptionMaximumEffects  = 4
	similarityDescriptionMaximumErrors   = 4

	similarityDescriptionMaximumActions  = 3
	similarityDescriptionMaximumFixtures = 4

	similarityDescriptionSignatureMaxChars = 180
)

type similarityDescriptionRuntime struct {
	describer similarityDescriber
	enabled   bool
}

type similarityDescriptionRequest struct {
	ID       string
	Kind     string
	Detail   bool
	Content  string
	Location string
}

type similarityDescriber interface {
	describe(
		[]similarityDescriptionRequest,
		func([]similarityDescription) error,
	) error
}

type similarityDescription struct {
	Schema            int      `json:"schema,omitempty"`
	Model             string   `json:"model,omitempty"`
	Effort            string   `json:"effort,omitempty"`
	Kind              string   `json:"kind,omitempty"`
	Detail            bool     `json:"detail,omitempty"`
	ID                string   `json:"id,omitempty"`
	Purpose           string   `json:"purpose,omitempty"`
	Inputs            []string `json:"inputs,omitempty"`
	Outputs           []string `json:"outputs,omitempty"`
	Processing        []string `json:"processing,omitempty"`
	Effects           []string `json:"effects,omitempty"`
	Errors            []string `json:"errors,omitempty"`
	IntentSignature   string   `json:"intent_signature,omitempty"`
	FlowSignature     string   `json:"flow_signature,omitempty"`
	BoundarySignature string   `json:"boundary_signature"`
	Subject           string   `json:"subject,omitempty"`
	Scenario          string   `json:"scenario,omitempty"`
	Setup             []string `json:"setup,omitempty"`
	Action            []string `json:"action,omitempty"`
	Assertions        []string `json:"assertions,omitempty"`
	Fixtures          []string `json:"fixtures,omitempty"`
	Contract          string   `json:"contract,omitempty"`
	ContractSignature string   `json:"contract_signature,omitempty"`
	ScenarioSignature string   `json:"scenario_signature,omitempty"`
	OracleSignature   string   `json:"oracle_signature,omitempty"`
}

type similarityDescriptionInput struct {
	kind       string
	recordKind similarityDescriptionRecordKind
	blocks     []*similarityBlock
}

type similarityDescriptionRecordKind uint8

const (
	similarityDescriptionSignatures similarityDescriptionRecordKind = iota
	similarityDescriptionDetails
)

type similarityDescriptionCollector struct {
	sync.Mutex
	requested    map[string]similarityDescriptionRequest
	inputs       map[string]*similarityDescriptionInput
	seen         map[string]struct{}
	cacheRoot    string
	cacheEnabled bool
	accepted     int
	total        int
}

type similarityDescriptionListRule struct {
	name    string
	values  []string
	minimum int
	maximum int
}

func similarityDescriptionRuntimeForOptions(
	opts SimilarityOptions,
) (similarityDescriptionRuntime, error) {
	if opts.descriptionDisabled {
		return similarityDescriptionRuntime{}, nil
	}

	mode := strings.ToLower(strings.TrimSpace(os.Getenv(similarityDescriptionEnv)))
	if mode != "" && mode != "auto" && mode != offText {
		return similarityDescriptionRuntime{}, fmt.Errorf(
			"%s must be auto or off; got %q",
			similarityDescriptionEnv,
			mode,
		)
	}

	if mode == offText {
		return similarityDescriptionRuntime{}, nil
	}

	if opts.describer != nil {
		return similarityDescriptionRuntime{describer: opts.describer, enabled: true}, nil
	}

	path, _ := exec.LookPath("codex")
	if path == "" {
		return similarityDescriptionRuntime{}, nil
	}

	return similarityDescriptionRuntime{
		describer: &codexSimilarityDescriber{path: path},
		enabled:   true,
	}, nil
}

func populateSimilarityDescriptions(
	blocks []*similarityBlock,
	runtime similarityDescriptionRuntime,
	cacheRoot string,
	cacheEnabled bool,
) (string, error) {
	if !runtime.enabled {
		return "", nil
	}

	inputs, keys := indexSimilarityDescriptionInputs(blocks, similarityDescriptionSignatures)

	missing := loadCachedSimilarityDescriptions(inputs, keys, cacheRoot, cacheEnabled)
	if len(missing) > 0 {
		if err := generateSimilarityDescriptions(
			missing,
			inputs,
			runtime.describer,
			cacheRoot,
			cacheEnabled,
		); err != nil {
			return "", err
		}
	}

	return similarityDescriptionsDigest(blocks)
}

func populateSimilarityDetails(
	blocks []*similarityBlock,
	runtime similarityDescriptionRuntime,
	cacheRoot string,
	cacheEnabled bool,
) error {
	if !runtime.enabled || len(blocks) == 0 {
		return nil
	}

	inputs, keys := indexSimilarityDescriptionInputs(blocks, similarityDescriptionDetails)

	missing := loadCachedSimilarityDescriptions(inputs, keys, cacheRoot, cacheEnabled)
	if len(missing) == 0 {
		return nil
	}

	return generateSimilarityDescriptions(
		missing,
		inputs,
		runtime.describer,
		cacheRoot,
		cacheEnabled,
	)
}

func populateSimilarityMatchDetails(
	matches []similarityMatch,
	runtime similarityDescriptionRuntime,
	cacheRoot string,
	cacheEnabled bool,
) error {
	if !runtime.enabled || len(matches) == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	detailBlocks := make([]*similarityBlock, 0)

	for _, match := range matches {
		for _, block := range match.Members {
			if _, ok := seen[block.Identity]; ok {
				continue
			}

			seen[block.Identity] = struct{}{}
			detailBlocks = append(detailBlocks, block)
		}
	}

	return populateSimilarityDetails(detailBlocks, runtime, cacheRoot, cacheEnabled)
}

func indexSimilarityDescriptionInputs(
	blocks []*similarityBlock,
	recordKind similarityDescriptionRecordKind,
) (map[string]*similarityDescriptionInput, []string) {
	inputs := make(map[string]*similarityDescriptionInput, len(blocks))

	for _, block := range blocks {
		kind, key := similarityDescriptionCacheKey(
			block.ContentHash,
			block.IsTest,
			recordKind,
		)

		input := inputs[key]
		if input == nil {
			input = &similarityDescriptionInput{kind: kind, recordKind: recordKind}
			inputs[key] = input
		}

		input.blocks = append(input.blocks, block)
	}

	keys := make([]string, 0, len(inputs))
	for key := range inputs {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return inputs, keys
}

func similarityDescriptionCacheKey(
	contentHash string,
	isTest bool,
	recordKind similarityDescriptionRecordKind,
) (string, string) {
	kind := similarityDescriptionProduction
	if isTest {
		kind = similarityDescriptionTest
	}

	fingerprint := strings.Join([]string{
		strconv.Itoa(similarityDescriptionPromptSchema),
		similarityDescriptionModel,
		similarityDescriptionEffort,
		kind,
		strconv.FormatBool(recordKind == similarityDescriptionDetails),
		contentHash,
	}, "\x00")
	sum := sha256.Sum256([]byte(fingerprint))

	return kind, hex.EncodeToString(sum[:])
}

func loadCachedSimilarityDescriptions(
	inputs map[string]*similarityDescriptionInput,
	keys []string,
	cacheRoot string,
	cacheEnabled bool,
) []similarityDescriptionRequest {
	missing := make([]similarityDescriptionRequest, 0)

	for _, key := range keys {
		input := inputs[key]
		if cacheEnabled {
			if description, ok := loadSimilarityDescription(
				cacheRoot,
				key,
				input.kind,
				input.recordKind,
			); ok {
				applySimilarityDescription(input.blocks, description)
				continue
			}
		}

		missing = append(missing, similarityDescriptionRequest{
			ID:       key,
			Kind:     input.kind,
			Detail:   input.recordKind == similarityDescriptionDetails,
			Content:  input.blocks[0].Content,
			Location: input.blocks[0].Identity,
		})
	}

	return missing
}

func generateSimilarityDescriptions(
	missing []similarityDescriptionRequest,
	inputs map[string]*similarityDescriptionInput,
	describer similarityDescriber,
	cacheRoot string,
	cacheEnabled bool,
) error {
	if describer == nil {
		return errors.New("codex description engine is unavailable")
	}

	outputKind := "signatures"
	if missing[0].Detail {
		outputKind = "details"
	}

	_, _ = fmt.Fprintf(
		os.Stderr,
		"slopelint: generating %s for %d code blocks with %s (%s reasoning)\n",
		outputKind,
		len(missing),
		similarityDescriptionModel,
		similarityDescriptionEffort,
	)

	requested := make(map[string]similarityDescriptionRequest, len(missing))
	for _, request := range missing {
		requested[request.ID] = request
	}

	collector := similarityDescriptionCollector{
		requested:    requested,
		inputs:       inputs,
		seen:         make(map[string]struct{}, len(missing)),
		cacheRoot:    cacheRoot,
		cacheEnabled: cacheEnabled,
		total:        len(missing),
	}
	if err := describer.describe(missing, collector.accept); err != nil {
		return err
	}

	if len(collector.seen) != len(missing) {
		return fmt.Errorf(
			"codex returned %d descriptions for %d code blocks",
			len(collector.seen),
			len(missing),
		)
	}

	return nil
}

func (collector *similarityDescriptionCollector) accept(
	descriptions []similarityDescription,
) error {
	collector.Lock()
	defer collector.Unlock()

	validated, err := collector.validateBatch(descriptions)
	if err != nil {
		return err
	}

	for _, description := range validated {
		request := collector.requested[description.ID]
		collector.seen[description.ID] = struct{}{}
		description.ID = ""
		applySimilarityDescription(collector.inputs[request.ID].blocks, description)

		if !collector.cacheEnabled {
			continue
		}

		if err := storeSimilarityDescription(
			collector.cacheRoot,
			request.ID,
			description,
		); err != nil {
			return fmt.Errorf("cache description for %s: %w", request.Location, err)
		}
	}

	collector.accepted += len(validated)
	_, _ = fmt.Fprintf(
		os.Stderr,
		"slopelint: generated %d/%d code records\n",
		collector.accepted,
		collector.total,
	)

	return nil
}

func (collector *similarityDescriptionCollector) validateBatch(
	descriptions []similarityDescription,
) ([]similarityDescription, error) {
	validated := make([]similarityDescription, len(descriptions))
	batchSeen := make(map[string]struct{}, len(descriptions))

	for index, description := range descriptions {
		request, ok := collector.requested[description.ID]
		if !ok {
			return nil, fmt.Errorf("codex returned unknown description id %q", description.ID)
		}

		if _, duplicate := collector.seen[description.ID]; duplicate {
			return nil, fmt.Errorf("codex returned duplicate description id %q", description.ID)
		}

		if _, duplicate := batchSeen[description.ID]; duplicate {
			return nil, fmt.Errorf("codex returned duplicate description id %q", description.ID)
		}

		batchSeen[description.ID] = struct{}{}
		description.Schema = similarityDescriptionPromptSchema
		description.Model = similarityDescriptionModel
		description.Effort = similarityDescriptionEffort
		description.Kind = request.Kind
		description.Detail = request.Detail
		description.normalize()

		if err := description.validate(); err != nil {
			return nil, fmt.Errorf("describe %s: %w", request.Location, err)
		}

		validated[index] = description
	}

	return validated, nil
}

func similarityDescriptionsDigest(blocks []*similarityBlock) (string, error) {
	hash := sha256.New()

	for _, block := range blocks {
		if block.DescriptionHash == "" {
			return "", fmt.Errorf("description missing for %s", block.Identity)
		}

		_, _ = hash.Write([]byte(block.Identity))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(block.ContentHash))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(block.DescriptionHash))
		_, _ = hash.Write([]byte{0})
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func loadSimilarityDescription(
	root string,
	key string,
	kind string,
	recordKind similarityDescriptionRecordKind,
) (similarityDescription, bool) {
	data, err := os.ReadFile(similarityDescriptionPath(root, key, recordKind))
	if err != nil {
		return similarityDescription{}, false
	}

	var description similarityDescription
	if json.Unmarshal(data, &description) != nil ||
		description.Kind != kind ||
		description.Detail != (recordKind == similarityDescriptionDetails) {
		return similarityDescription{}, false
	}

	description.normalize()

	if description.validate() != nil {
		return similarityDescription{}, false
	}

	return description, true
}

func storeSimilarityDescription(
	root string,
	key string,
	description similarityDescription,
) error {
	data, err := json.Marshal(description)
	if err != nil {
		return err
	}

	recordKind := similarityDescriptionSignatures
	if description.Detail {
		recordKind = similarityDescriptionDetails
	}

	return writeFileAtomically(similarityDescriptionPath(root, key, recordKind), data)
}

func similarityDescriptionPath(
	root string,
	key string,
	recordKind similarityDescriptionRecordKind,
) string {
	kind := "signatures"
	if recordKind == similarityDescriptionDetails {
		kind = "details"
	}

	return filepath.Join(root, "descriptions", kind, key[:2], key[2:]+".json")
}

func applySimilarityDescription(
	blocks []*similarityBlock,
	description similarityDescription,
) {
	if description.Detail {
		for _, block := range blocks {
			block.DescriptionDetail = description.canonical()
		}

		return
	}

	canonical := description.canonical()
	sum := sha256.Sum256([]byte(canonical))

	for _, block := range blocks {
		block.Description = description.embeddingText()
		block.DescriptionHash = hex.EncodeToString(sum[:])
	}
}
