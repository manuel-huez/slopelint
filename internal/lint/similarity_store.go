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
	similarityVectorInputSchema = 1

	// Benchmark-selected digest pins model weights so a mutable Ollama tag cannot alter policy.
	similarityModelName   = "unclemusclez/jina-embeddings-v2-base-code:latest"
	similarityModelDigest = "9fe680d4d58b475099d91e0d08a1eaea6cef087b0a0770f4165581a7e366afec"
)

type similarityStamp struct {
	Schema        int                    `json:"schema"`
	SourceDigest  string                 `json:"source_digest"`
	Model         string                 `json:"model"`
	ModelDigest   string                 `json:"model_digest"`
	CheckedBlocks int                    `json:"checked_blocks"`
	Accepted      []similarityAcceptance `json:"accepted,omitempty"`
}

type similarityAcceptance struct {
	ID        string `json:"id"`
	Left      string `json:"left"`
	LeftHash  string `json:"left_hash"`
	Right     string `json:"right"`
	RightHash string `json:"right_hash"`
}

type similarityManifest struct {
	Schema       int                       `json:"schema"`
	SourceDigest string                    `json:"source_digest"`
	Blocks       []similarityManifestBlock `json:"blocks"`
}

type similarityManifestBlock struct {
	Identity    string `json:"identity"`
	ContentHash string `json:"content_hash"`
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
		if pkg == nil || pkg.FSet == nil {
			continue
		}

		for _, file := range pkg.Files {
			if file == nil {
				continue
			}

			filename := pkg.FSet.Position(file.Pos()).Filename

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
) similarityStamp {
	return similarityStamp{
		Schema:        similaritySchema,
		SourceDigest:  sourceDigest,
		Model:         similarityModelName,
		ModelDigest:   similarityModelDigest,
		CheckedBlocks: checkedBlocks,
		Accepted:      accepted,
	}
}

func (stamp similarityStamp) policyMatches() bool {
	return stamp.Schema == similaritySchema &&
		stamp.Model == similarityModelName &&
		stamp.ModelDigest == similarityModelDigest
}

func (stamp similarityStamp) covers(sourceDigest string) bool {
	return stamp.policyMatches() &&
		stamp.SourceDigest == sourceDigest
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

func similarityVectorCacheRoot(dir string) (string, error) {
	if dir == "" {
		userCacheDir, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}

		dir = filepath.Join(userCacheDir, "slopelint")
	}

	return filepath.Join(dir, fmt.Sprintf("similarity-v%d", similarityCacheSchema)), nil
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

	if validateSimilarityVector(vector) != nil {
		return nil, false
	}

	return vector, true
}

func storeSimilarityVector(root, key string, vector []float32) error {
	data := make(
		[]byte,
		len(similarityVectorMagic)+similarityVectorValueBytes+
			len(vector)*similarityVectorValueBytes,
	)
	copy(data, similarityVectorMagic)
	binary.LittleEndian.PutUint32(data[len(similarityVectorMagic):], uint32(len(vector)))

	offset := len(similarityVectorMagic) + similarityVectorValueBytes
	for i, value := range vector {
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

func changedSimilarityBlocks(
	blocks []*similarityBlock,
	stamp similarityStamp,
	stampExists bool,
	cacheRoot string,
	moduleRoot string,
) map[string]struct{} {
	// A clean prior manifest proves unchanged pairs were already reviewed. Only a changed
	// endpoint can create a new duplicate; deletion alone cannot create one.
	if !stampExists || !stamp.policyMatches() {
		return nil
	}

	manifest, ok := loadSimilarityManifest(cacheRoot, moduleRoot)
	if !ok || manifest.Schema != similarityCacheSchema ||
		manifest.SourceDigest != stamp.SourceDigest {
		return nil
	}

	previous := make(map[string]string, len(manifest.Blocks))

	for _, block := range manifest.Blocks {
		previous[block.Identity] = block.ContentHash
	}

	changed := make(map[string]struct{})

	for _, block := range blocks {
		if previous[block.Identity] != block.ContentHash {
			changed[block.Identity] = struct{}{}
		}
	}

	return changed
}

func loadSimilarityManifest(root, moduleRoot string) (similarityManifest, bool) {
	data, err := os.ReadFile(similarityManifestPath(root, moduleRoot))
	if err != nil {
		return similarityManifest{}, false
	}

	var manifest similarityManifest
	if json.Unmarshal(data, &manifest) != nil {
		return similarityManifest{}, false
	}

	return manifest, true
}

func storeSimilarityManifest(
	root string,
	moduleRoot string,
	sourceDigest string,
	blocks []*similarityBlock,
) error {
	manifest := similarityManifest{
		Schema:       similarityCacheSchema,
		SourceDigest: sourceDigest,
		Blocks:       make([]similarityManifestBlock, 0, len(blocks)),
	}
	for _, block := range blocks {
		manifest.Blocks = append(manifest.Blocks, similarityManifestBlock{
			Identity:    block.Identity,
			ContentHash: block.ContentHash,
		})
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}

	return writeFileAtomically(similarityManifestPath(root, moduleRoot), data)
}

func similarityManifestPath(root, moduleRoot string) string {
	absolute, err := filepath.Abs(moduleRoot)
	if err != nil {
		absolute = moduleRoot
	}

	sum := sha256.Sum256([]byte(absolute))
	key := hex.EncodeToString(sum[:])

	return filepath.Join(root, "repos", key[:2], key[2:]+".json")
}

func carrySimilarityAcceptances(
	stamp similarityStamp,
	exists bool,
	blocks []*similarityBlock,
) []similarityAcceptance {
	if !exists || !stamp.policyMatches() {
		return nil
	}

	current := make(map[string]string, len(blocks))
	for _, block := range blocks {
		current[block.Identity] = block.ContentHash
	}

	out := make([]similarityAcceptance, 0, len(stamp.Accepted))
	for _, accepted := range stamp.Accepted {
		if current[accepted.Left] != accepted.LeftHash ||
			current[accepted.Right] != accepted.RightHash {
			continue
		}

		out = append(out, accepted)
	}

	return out
}
