package lint

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	similarityStampName         = ".slopelint-similarity.json"
	similarityCacheSchema       = 1
	similarityVectorMagic       = "SLOPEV01"
	similarityVectorValueBytes  = 4
	similarityVectorInputSchema = 2

	// Benchmark-selected digest pins the exact GGUF weights used by native inference.
	similarityModelName   = "jina-embeddings-v2-base-code"
	similarityModelDigest = "33a8a1b6a1cbba662f292d32bb55f8d109c0e6cb02de2d243a1b70705ea20986"
)

type similarityStamp struct {
	Schema            int                    `json:"schema"`
	SourceDigest      string                 `json:"source_digest"`
	RepositoryDigest  string                 `json:"repository_digest,omitempty"`
	Model             string                 `json:"model"`
	ModelDigest       string                 `json:"model_digest"`
	DescriptionSchema int                    `json:"description_schema,omitempty"`
	DescriptionModel  string                 `json:"description_model,omitempty"`
	DescriptionEffort string                 `json:"description_effort,omitempty"`
	DescriptionDigest string                 `json:"description_digest,omitempty"`
	CheckedBlocks     int                    `json:"checked_blocks"`
	Accepted          []similarityAcceptance `json:"accepted,omitempty"`
}

type similarityAcceptance struct {
	ID        string `json:"id"`
	Left      string `json:"left"`
	LeftHash  string `json:"left_hash"`
	Right     string `json:"right"`
	RightHash string `json:"right_hash"`
}

type similaritySourceFile struct {
	Package string `json:"package"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
}

func similarityModuleRoot(pkgs []*LoadedPackage) (string, error) {
	var root string

	for _, pkg := range pkgs {
		if pkg == nil || pkg.Dir == "" {
			continue
		}

		candidate, err := findGoModuleRoot(pkg.Dir)
		if err != nil {
			return "", err
		}

		if root == "" {
			root = candidate
			continue
		}

		if root != candidate {
			return "", fmt.Errorf(
				"semantic similarity requires one Go module, found %s and %s",
				root,
				candidate,
			)
		}
	}

	if root == "" {
		return "", errors.New("cannot find Go module for semantic similarity")
	}

	return root, nil
}

func findGoModuleRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}

	for {
		_, err := os.Stat(filepath.Join(dir, "go.mod"))
		if err == nil {
			return dir, nil
		}

		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("cannot find go.mod above %s", start)
		}

		dir = parent
	}
}

func similaritySourceDigest(pkgs []*LoadedPackage, root string) (string, error) {
	filesByPath := make(map[string]similaritySourceFile)

	for _, pkg := range pkgs {
		if pkg == nil {
			continue
		}

		for _, filename := range pkg.repoFiles {
			key, sourceFile, err := similaritySourceFileForPath(pkg.ImportPath, filename, root)
			if err != nil {
				return "", err
			}

			filesByPath[key] = sourceFile
		}
	}

	files := make([]similaritySourceFile, 0, len(filesByPath))
	for _, file := range filesByPath {
		files = append(files, file)
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].Package != files[j].Package {
			return files[i].Package < files[j].Package
		}

		return files[i].Path < files[j].Path
	})

	fingerprint := struct {
		Schema int                    `json:"schema"`
		Files  []similaritySourceFile `json:"files"`
	}{
		Schema: similaritySchema,
		Files:  files,
	}

	data, err := json.Marshal(fingerprint)
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), nil
}

func similaritySourceFileForPath(
	importPath string,
	filename string,
	root string,
) (string, similaritySourceFile, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", similaritySourceFile{}, fmt.Errorf(
			"hash similarity source %s: %w",
			filename,
			err,
		)
	}

	relativePath, err := filepath.Rel(root, filename)
	if err != nil {
		return "", similaritySourceFile{}, err
	}

	if relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return "", similaritySourceFile{}, fmt.Errorf(
			"similarity source %s is outside module root %s",
			filename,
			root,
		)
	}

	relativePath = filepath.ToSlash(relativePath)
	sum := sha256.Sum256(data)

	return importPath + "\x00" + relativePath, similaritySourceFile{
		Package: importPath,
		Path:    relativePath,
		SHA256:  hex.EncodeToString(sum[:]),
	}, nil
}

func loadSimilarityStamp(root string) (similarityStamp, error) {
	path := filepath.Join(root, similarityStampName)

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return similarityStamp{}, nil
	}

	if err != nil {
		return similarityStamp{}, err
	}

	var stamp similarityStamp
	if err := json.Unmarshal(data, &stamp); err != nil {
		return similarityStamp{}, fmt.Errorf("decode %s: %w", similarityStampName, err)
	}

	if stamp.Schema == 0 {
		return similarityStamp{}, fmt.Errorf("decode %s: missing schema", similarityStampName)
	}

	return stamp, nil
}

func newSimilarityStamp(
	sourceDigest string,
	checkedBlocks int,
	accepted []similarityAcceptance,
	descriptionsEnabled bool,
	descriptionDigest string,
) similarityStamp {
	stamp := similarityStamp{
		Schema:        similaritySchema,
		SourceDigest:  sourceDigest,
		Model:         similarityModelName,
		ModelDigest:   similarityModelDigest,
		CheckedBlocks: checkedBlocks,
		Accepted:      accepted,
	}

	if descriptionsEnabled {
		stamp.DescriptionSchema = similarityDescriptionPromptSchema
		stamp.DescriptionModel = similarityDescriptionModel
		stamp.DescriptionEffort = similarityDescriptionEffort
		stamp.DescriptionDigest = descriptionDigest
	}

	return stamp
}

func (stamp similarityStamp) policyMatches() bool {
	if stamp.Schema != similaritySchema ||
		stamp.Model != similarityModelName ||
		stamp.ModelDigest != similarityModelDigest {
		return false
	}

	if stamp.DescriptionModel == "" {
		return stamp.DescriptionSchema == 0 &&
			stamp.DescriptionEffort == "" &&
			stamp.DescriptionDigest == ""
	}

	return stamp.DescriptionSchema == similarityDescriptionPromptSchema &&
		stamp.DescriptionModel == similarityDescriptionModel &&
		stamp.DescriptionEffort == similarityDescriptionEffort &&
		len(stamp.DescriptionDigest) == sha256.Size*2
}

func (stamp similarityStamp) covers(sourceDigest string, descriptionsEnabled bool) bool {
	return stamp.policyMatches() &&
		stamp.SourceDigest == sourceDigest &&
		(!descriptionsEnabled || stamp.DescriptionModel != "")
}

func verifySimilarityStamp(
	stamp similarityStamp,
	exists bool,
	sourceDigest string,
) error {
	if !exists {
		return fmt.Errorf(
			"%s is missing; run slopelint locally and commit the generated stamp",
			similarityStampName,
		)
	}

	if !stamp.policyMatches() {
		return fmt.Errorf(
			"%s uses an obsolete semantic similarity policy; run slopelint locally",
			similarityStampName,
		)
	}

	if stamp.SourceDigest != sourceDigest {
		return fmt.Errorf(
			"%s is stale; run slopelint locally and commit the updated stamp",
			similarityStampName,
		)
	}

	return nil
}

func storeSimilarityStamp(root string, stamp similarityStamp) error {
	if repositoryDigest, err := similarityRepositoryDigest(root); err == nil {
		stamp.RepositoryDigest = repositoryDigest
	}

	data, err := json.MarshalIndent(stamp, "", "  ")
	if err != nil {
		return err
	}

	data = append(data, '\n')

	path := filepath.Join(root, similarityStampName)
	if err := writeFileAtomically(path, data); err != nil {
		return fmt.Errorf("write %s: %w", similarityStampName, err)
	}

	return nil
}

func similarityRepositoryDigest(root string) (string, error) {
	location, err := repositoryCacheLocationForDir(root)
	if err != nil || !location.git {
		return "", errAnalysisCacheDisabled
	}

	return repoAnalysisGitDigest(
		location.sourceRoot,
		location.objectFormat,
		similarityRepositoryPathspecs,
	)
}

func similarityVectorCacheRoot(dir string) (string, error) {
	root, err := slopelintCacheRoot(dir)
	if err != nil {
		return "", err
	}

	return filepath.Join(root, fmt.Sprintf("similarity-v%d", similarityCacheSchema)), nil
}

func loadSimilarityVector(root, key string) ([]float32, bool) {
	data, err := os.ReadFile(similarityVectorPath(root, key))
	if err != nil || len(data) < len(similarityVectorMagic)+similarityVectorValueBytes {
		return nil, false
	}

	if string(data[:len(similarityVectorMagic)]) != similarityVectorMagic {
		return nil, false
	}

	dimensions := int(binary.LittleEndian.Uint32(data[len(similarityVectorMagic):]))

	expected := len(similarityVectorMagic) +
		similarityVectorValueBytes +
		dimensions*similarityVectorValueBytes
	if dimensions <= 0 || dimensions > 65536 || len(data) != expected {
		return nil, false
	}

	vector := make([]float32, dimensions)

	offset := len(similarityVectorMagic) + similarityVectorValueBytes
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(
			data[offset+i*similarityVectorValueBytes:],
		))
	}

	if normalizeSimilarityVector(vector) != nil {
		return nil, false
	}

	return vector, true
}

func storeSimilarityVector(root, key string, vector []float32) error {
	normalized := append([]float32(nil), vector...)
	if err := normalizeSimilarityVector(normalized); err != nil {
		return err
	}

	data := make(
		[]byte,
		len(similarityVectorMagic)+similarityVectorValueBytes+
			len(normalized)*similarityVectorValueBytes,
	)
	copy(data, similarityVectorMagic)
	binary.LittleEndian.PutUint32(data[len(similarityVectorMagic):], uint32(len(normalized)))

	offset := len(similarityVectorMagic) + similarityVectorValueBytes
	for i, value := range normalized {
		binary.LittleEndian.PutUint32(
			data[offset+i*similarityVectorValueBytes:],
			math.Float32bits(value),
		)
	}

	return writeFileAtomically(similarityVectorPath(root, key), data)
}

func similarityVectorPath(root, key string) string {
	return filepath.Join(root, "vectors", key[:2], key[2:]+".bin")
}

func carrySimilarityAcceptances(
	stamp similarityStamp,
	exists bool,
	current map[string]string,
) []similarityAcceptance {
	if !exists {
		return nil
	}

	out := make([]similarityAcceptance, 0, len(stamp.Accepted))
	for _, accepted := range stamp.Accepted {
		if current[accepted.Left] != accepted.LeftHash ||
			current[accepted.Right] != accepted.RightHash {
			continue
		}

		if accepted.ID != similarityPairID(
			&similarityBlock{Identity: accepted.Left, ContentHash: accepted.LeftHash},
			&similarityBlock{Identity: accepted.Right, ContentHash: accepted.RightHash},
		) {
			continue
		}

		out = append(out, accepted)
	}

	return out
}
