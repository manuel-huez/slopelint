package lint

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // Git SHA-1 object IDs are content identities, not security proofs.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
)

var repoAnalysisSourcePathspecs = []string{
	"*.go",
	"go.mod",
	"go.sum",
	"go.work",
	"go.work.sum",
	"*.c",
	"*.cc",
	"*.cpp",
	"*.cxx",
	"*.h",
	"*.hh",
	"*.hpp",
	"*.m",
	"*.mm",
	"*.s",
	"*.S",
	"*.f",
	"*.F",
	"*.for",
	"*.f90",
	similarityStampName,
}

func repoAnalysisCacheKey(
	patterns []string,
	dir string,
	opts Options,
	similarity *SimilarityOptions,
) (string, string, error) {
	if similarity != nil && (similarity.embedder != nil || similarity.describer != nil) {
		return "", "", errAnalysisCacheDisabled
	}

	maxStates := opts.MaxStates
	if maxStates <= 0 {
		maxStates = 32
	}

	if len(patterns) == 0 {
		patterns = []string{allPackagesPattern}
	}

	absoluteDir, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}

	location, err := repositoryCacheLocationForDir(absoluteDir)
	if err != nil {
		return "", "", err
	}

	goEnvironment, goWork, err := repoAnalysisGoEnvironment(
		absoluteDir,
		location.sourceRoot,
	)
	if err != nil {
		return "", "", err
	}

	goPath, _ := exec.LookPath("go")

	sourceDigest, err := repoAnalysisSourceDigest(absoluteDir, patterns, goWork)
	if err != nil {
		return "", "", err
	}

	fingerprint := struct {
		Schema      int                                `json:"schema"`
		Repository  string                             `json:"repository"`
		Dir         string                             `json:"dir"`
		Patterns    []string                           `json:"patterns"`
		MaxStates   int                                `json:"max_states"`
		ClosedWorld bool                               `json:"closed_world"`
		Go          analysisCacheExecutable            `json:"go"`
		GoRuntime   string                             `json:"go_runtime"`
		GoEnv       string                             `json:"go_env"`
		Source      string                             `json:"source"`
		Similarity  *repoAnalysisSimilarityFingerprint `json:"similarity,omitempty"`
	}{
		Schema:      analysisCacheSchema,
		Repository:  location.identity,
		Dir:         location.relativeDir,
		Patterns:    append([]string(nil), patterns...),
		MaxStates:   maxStates,
		ClosedWorld: opts.ClosedWorld,
		Go:          analysisCacheExecutableForPath(goPath),
		GoRuntime:   runtime.GOOS + "/" + runtime.GOARCH,
		GoEnv:       goEnvironment,
		Source:      sourceDigest,
		Similarity:  repoAnalysisSimilarityKey(similarity),
	}

	key, err := analysisCacheFingerprintKey(fingerprint)

	return key, location.sourceRoot, err
}

type repositoryCacheLocation struct {
	sourceRoot  string
	identity    string
	relativeDir string
}

func repositoryCacheLocationForDir(dir string) (repositoryCacheLocation, error) {
	absoluteDir, err := filepath.Abs(dir)
	if err != nil {
		return repositoryCacheLocation{}, err
	}

	sourceRoot, _, commonDir, gitErr := repoAnalysisGitInfo(absoluteDir)

	var identity string

	if gitErr == nil {
		identity = commonDir
	} else {
		sourceRoot, err = findGoModuleRoot(absoluteDir)
		if err != nil {
			return repositoryCacheLocation{}, err
		}

		identity = sourceRoot
	}

	relativeDir, err := filepath.Rel(sourceRoot, absoluteDir)
	if err != nil || !filepath.IsLocal(relativeDir) {
		return repositoryCacheLocation{}, errAnalysisCacheDisabled
	}

	return repositoryCacheLocation{
		sourceRoot:  filepath.Clean(sourceRoot),
		identity:    filepath.Clean(identity),
		relativeDir: filepath.ToSlash(relativeDir),
	}, nil
}

type repoAnalysisSimilarityFingerprint struct {
	CI                bool     `json:"ci"`
	AcceptedPairIDs   []string `json:"accepted_pair_ids,omitempty"`
	Schema            int      `json:"schema"`
	Model             string   `json:"model"`
	ModelDigest       string   `json:"model_digest"`
	Descriptions      bool     `json:"descriptions"`
	DescriptionSchema int      `json:"description_schema,omitempty"`
	DescriptionModel  string   `json:"description_model,omitempty"`
	DescriptionEffort string   `json:"description_effort,omitempty"`
	DescriptionMode   string   `json:"description_mode,omitempty"`
}

func repoAnalysisSimilarityKey(
	opts *SimilarityOptions,
) *repoAnalysisSimilarityFingerprint {
	if opts == nil {
		return nil
	}

	accepted := append([]string(nil), opts.AcceptedPairIDs...)
	sort.Strings(accepted)

	fingerprint := &repoAnalysisSimilarityFingerprint{
		CI:              opts.CI,
		AcceptedPairIDs: accepted,
		Schema:          similaritySchema,
		Model:           similarityModelName,
		ModelDigest:     similarityModelDigest,
	}

	mode := strings.ToLower(strings.TrimSpace(os.Getenv(similarityDescriptionEnv)))
	fingerprint.DescriptionMode = mode
	codexPath, _ := exec.LookPath("codex")

	if !opts.descriptionDisabled && mode != offText &&
		(opts.describer != nil || codexPath != "") {
		fingerprint.Descriptions = true
		fingerprint.DescriptionSchema = similarityDescriptionPromptSchema
		fingerprint.DescriptionModel = similarityDescriptionModel
		fingerprint.DescriptionEffort = similarityDescriptionEffort
	}

	return fingerprint
}

func repoAnalysisGoEnvironment(dir, sourceRoot string) (string, string, error) {
	names := []string{
		"AR",
		"CC",
		"CGO_ENABLED",
		"CGO_CFLAGS",
		"CGO_CPPFLAGS",
		"CGO_CXXFLAGS",
		"CGO_FFLAGS",
		"CGO_LDFLAGS",
		"CXX",
		"FC",
		"GO111MODULE",
		"GOARCH",
		"GOEXPERIMENT",
		"GOFLAGS",
		"GOMODCACHE",
		"GOOS",
		"GOPATH",
		"GOROOT",
		"GOTOOLCHAIN",
		"GOWORK",
		"PKG_CONFIG",
	}

	cmd := exec.Command("go", append([]string{"env", "-json"}, names...)...)
	cmd.Dir = dir

	output, err := cmd.Output()
	if err != nil {
		return "", "", err
	}

	var environment map[string]string
	if err := json.Unmarshal(output, &environment); err != nil {
		return "", "", err
	}

	// Git worktrees relocate checkout-owned paths without changing build semantics.
	// Normalize those paths so identical linked worktrees share exact-result cache keys.
	goWork := environment["GOWORK"]
	for name, value := range environment {
		environment[name] = strings.ReplaceAll(value, sourceRoot, "$WORKTREE")
	}

	normalized, err := json.Marshal(environment)
	if err != nil {
		return "", "", err
	}

	return string(normalized), goWork, nil
}

func repoAnalysisSourceDigest(
	dir string,
	patterns []string,
	goWork string,
) (string, error) {
	if !repoAnalysisLocalPatterns(patterns) {
		return "", errAnalysisCacheDisabled
	}

	root, objectFormat, _, err := repoAnalysisGitInfo(dir)
	if err == nil {
		submodulesSupported, submoduleErr := repoAnalysisSubmodulesSupported(root)
		if submoduleErr != nil {
			return "", submoduleErr
		}

		if !submodulesSupported {
			return "", errAnalysisCacheDisabled
		}

		if err := repoAnalysisExternalModuleCheck(dir, root, goWork); err != nil {
			return "", err
		}

		return repoAnalysisGitDigest(root, objectFormat)
	}

	root, err = findGoModuleRoot(dir)
	if err != nil {
		return "", err
	}

	if err := repoAnalysisExternalModuleCheck(dir, root, goWork); err != nil {
		return "", err
	}

	return repoAnalysisWalkDigest(root)
}

func repoAnalysisLocalPatterns(patterns []string) bool {
	for _, pattern := range patterns {
		if pattern == "." || pattern == allPackagesPattern {
			continue
		}

		if !strings.HasPrefix(pattern, "./") {
			return false
		}

		path := strings.TrimSuffix(strings.TrimPrefix(pattern, "./"), "/...")
		for component := range strings.SplitSeq(filepath.ToSlash(path), "/") {
			if component == ".." || repoAnalysisIgnoredDirectory(component) {
				return false
			}
		}
	}

	return true
}

func repoAnalysisGitInfo(dir string) (string, string, string, error) {
	const gitInfoFieldCount = 3

	cmd := exec.Command(
		"git",
		"rev-parse",
		"--show-toplevel",
		"--show-object-format",
		"--git-common-dir",
	)
	cmd.Dir = dir

	output, err := cmd.Output()
	if err != nil {
		return "", "", "", err
	}

	fields := bytes.Split(bytes.TrimSpace(output), []byte{'\n'})
	if len(fields) != gitInfoFieldCount {
		return "", "", "", errors.New(
			"git did not return repository root, object format, and common directory",
		)
	}

	root := filepath.Clean(string(fields[0]))
	commonDir := filepath.Clean(string(fields[2]))

	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(root, commonDir)
	}

	if resolved, resolveErr := filepath.EvalSymlinks(commonDir); resolveErr == nil {
		commonDir = resolved
	}

	return root, string(fields[1]), commonDir, nil
}

func repoAnalysisGitDigest(root, objectFormat string) (string, error) {
	files, err := repoAnalysisGitIndex(root)
	if err != nil {
		return "", err
	}

	modified, err := repoAnalysisGitOutput(
		root,
		"modified files",
		[]string{"diff-files", "--name-only", "-z", "--"},
		repoAnalysisSourcePathspecs...,
	)
	if err != nil {
		return "", err
	}

	if err := repoAnalysisApplyWorktree(files, modified, root, objectFormat); err != nil {
		return "", err
	}

	untracked, err := repoAnalysisGitOutput(
		root,
		"untracked files",
		[]string{"ls-files", "--others", "--exclude-standard", "-z", "--"},
		repoAnalysisSourcePathspecs...,
	)
	if err != nil {
		return "", err
	}

	if err := repoAnalysisApplyWorktree(files, untracked, root, objectFormat); err != nil {
		return "", err
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}

	sort.Strings(names)

	digest := sha256.New()
	for _, name := range names {
		_, _ = digest.Write([]byte(name))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(files[name]))
		_, _ = digest.Write([]byte{0})
	}

	return hex.EncodeToString(digest.Sum(nil)), nil
}

func repoAnalysisGitIndex(root string) (map[string]string, error) {
	index, err := repoAnalysisGitOutput(
		root,
		"tracked files",
		[]string{"ls-files", "--stage", "-z", "--"},
		repoAnalysisSourcePathspecs...,
	)
	if err != nil {
		return nil, err
	}

	files := make(map[string]string)

	for record := range bytes.SplitSeq(index, []byte{0}) {
		if len(record) == 0 {
			continue
		}

		metadata, path, found := bytes.Cut(record, []byte{'\t'})

		fields := bytes.Fields(metadata)
		if !found || len(fields) != 3 || string(fields[2]) != "0" {
			return nil, errAnalysisCacheDisabled
		}

		files[string(path)] = string(fields[1])
	}

	return files, nil
}

func repoAnalysisGitOutput(
	root string,
	subject string,
	args []string,
	pathspecs ...string,
) ([]byte, error) {
	cmd := exec.Command("git", append(args, pathspecs...)...)
	cmd.Dir = root

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("fingerprint %s: %w", subject, err)
	}

	return output, nil
}

func repoAnalysisApplyWorktree(
	files map[string]string,
	paths []byte,
	root string,
	objectFormat string,
) error {
	for path := range bytes.SplitSeq(paths, []byte{0}) {
		name := string(path)
		if len(name) == 0 {
			continue
		}

		delete(files, name)

		objectID, blobErr := repoAnalysisBlobID(root, name, objectFormat)
		if blobErr != nil {
			return blobErr
		}

		if objectID != "" {
			files[name] = objectID
		}
	}

	return nil
}

func repoAnalysisBlobID(root, name, objectFormat string) (string, error) {
	path := filepath.Join(root, filepath.FromSlash(name))

	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}

	if err != nil {
		return "", err
	}

	if !info.Mode().IsRegular() {
		return "", errAnalysisCacheDisabled
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var objectHash hash.Hash

	switch objectFormat {
	case "sha1":
		objectHash = sha1.New() //nolint:gosec // Must match Git's configured object format.
	case "sha256":
		objectHash = sha256.New()
	default:
		return "", errAnalysisCacheDisabled
	}

	_, _ = objectHash.Write([]byte("blob " + strconv.Itoa(len(content)) + "\x00"))
	_, _ = objectHash.Write(content)

	return hex.EncodeToString(objectHash.Sum(nil)), nil
}

func repoAnalysisWalkDigest(root string) (string, error) {
	hash := sha256.New()

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			name := entry.Name()
			if path != root && repoAnalysisIgnoredDirectory(name) {
				return filepath.SkipDir
			}

			return nil
		}

		if !repoAnalysisSourceFile(entry.Name()) {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		_, _ = hash.Write([]byte(filepath.ToSlash(relative)))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(content)
		_, _ = hash.Write([]byte{0})

		return nil
	})
	if err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func repoAnalysisSubmodulesSupported(root string) (bool, error) {
	if _, err := os.Stat(filepath.Join(root, ".gitmodules")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}

		return false, err
	}

	cmd := exec.Command("git", "ls-files", "--stage", "-z")
	cmd.Dir = root

	output, err := cmd.Output()
	if err != nil {
		return false, err
	}

	for record := range bytes.SplitSeq(output, []byte{0}) {
		metadata, path, found := bytes.Cut(record, []byte{'\t'})
		if !found || !bytes.HasPrefix(metadata, []byte("160000 ")) {
			continue
		}

		ignored := false

		for component := range strings.SplitSeq(filepath.ToSlash(string(path)), "/") {
			if repoAnalysisIgnoredDirectory(component) {
				ignored = true
				break
			}
		}

		if !ignored {
			return false, nil
		}
	}

	return true, nil
}

func repoAnalysisIgnoredDirectory(name string) bool {
	return name == "testdata" || name == "vendor" ||
		strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

func repoAnalysisSourceFile(name string) bool {
	for _, pattern := range repoAnalysisSourcePathspecs {
		if strings.HasPrefix(pattern, "*.") && strings.HasSuffix(name, pattern[1:]) {
			return true
		}

		if name == pattern {
			return true
		}
	}

	return false
}

func repoAnalysisExternalModuleCheck(dir, cacheRoot, goWork string) error {
	if goWork != "" && goWork != offText {
		return errAnalysisCacheDisabled
	}

	moduleRoot, err := findGoModuleRoot(dir)
	if err != nil {
		return err
	}

	return repoAnalysisReplacementCheck(moduleRoot, cacheRoot)
}

func repoAnalysisReplacementCheck(moduleRoot, cacheRoot string) error {
	data, err := os.ReadFile(filepath.Join(moduleRoot, "go.mod"))
	if err != nil {
		return err
	}

	parsed, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		return err
	}

	for _, replacement := range parsed.Replace {
		if replacement.New.Version != "" || !modfile.IsDirectoryPath(replacement.New.Path) {
			continue
		}

		path := replacement.New.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(moduleRoot, path)
		}

		if relative, relErr := filepath.Rel(cacheRoot, path); relErr != nil ||
			relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errAnalysisCacheDisabled
		}
	}

	return nil
}
