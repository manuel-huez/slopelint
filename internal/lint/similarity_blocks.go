package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/manuel-huez/slopelint/internal/lint/outline"
)

type similarityBlockSourceFile struct {
	pkg          *LoadedPackage
	fset         *token.FileSet
	file         *ast.File
	absolutePath string
	relativePath string
	contentHash  string
}

func collectSimilarityBlocks(
	pkgs []*LoadedPackage,
	scripts []string,
	root string,
	previous similarityScanCache,
) ([]similarityCachedFile, []*similarityBlock, error) {
	files, err := similaritySourceFiles(pkgs, scripts, root)
	if err != nil {
		return nil, nil, err
	}

	previousFiles := make(map[string]string, len(previous.Files))
	previousByContent := make(map[string][]string, len(previous.Files))

	for _, file := range previous.Files {
		previousFiles[file.RelativePath] = file.ContentHash
		key := similarityCachedFileReuseKey(file.RelativePath, file.ContentHash)
		previousByContent[key] = append(previousByContent[key], file.RelativePath)
	}

	usedPreviousFiles := make(map[string]struct{}, len(previous.Files))
	currentFiles := make(map[string]string, len(files))

	for _, file := range files {
		currentFiles[file.relativePath] = file.contentHash
	}

	cachedFiles := make([]similarityCachedFile, 0, len(files))
	blocks := make([]*similarityBlock, 0, len(previous.Blocks))

	for _, file := range files {
		cachedFiles = append(cachedFiles, similarityCachedFile{
			RelativePath: file.relativePath,
			ContentHash:  file.contentHash,
		})

		if previousFiles[file.relativePath] == file.contentHash {
			usedPreviousFiles[file.relativePath] = struct{}{}
			blocks = append(
				blocks,
				previous.blocksForFile(root, file.relativePath, file.relativePath)...,
			)

			continue
		}

		reuseKey := similarityCachedFileReuseKey(file.relativePath, file.contentHash)
		reusedByContent := false

		for _, previousPath := range previousByContent[reuseKey] {
			if _, used := usedPreviousFiles[previousPath]; used {
				continue
			}

			if previousPath != file.relativePath &&
				currentFiles[previousPath] == file.contentHash {
				continue
			}

			usedPreviousFiles[previousPath] = struct{}{}
			blocks = append(
				blocks,
				previous.blocksForFile(root, previousPath, file.relativePath)...,
			)
			reusedByContent = true

			break
		}

		if reusedByContent {
			continue
		}

		fileBlocks, err := collectSimilaritySourceFileBlocks(file, root)
		if err != nil {
			return nil, nil, err
		}

		blocks = append(blocks, fileBlocks...)
	}

	sort.Slice(blocks, func(i, j int) bool { return blocks[i].Identity < blocks[j].Identity })

	return cachedFiles, blocks, nil
}

func similarityCachedFileReuseKey(relativePath, contentHash string) string {
	packageDir := filepath.ToSlash(filepath.Dir(relativePath))

	return packageDir + "\x00" + strconv.FormatBool(
		outline.TestFile(relativePath),
	) + "\x00" + contentHash
}

func similaritySourceFiles(
	pkgs []*LoadedPackage,
	scripts []string,
	root string,
) ([]similarityBlockSourceFile, error) {
	filesByPath := make(map[string]similarityBlockSourceFile)

	scriptFiles, err := similarityFileSources(nil, scripts, root)
	if err != nil {
		return nil, err
	}

	for _, file := range scriptFiles {
		filesByPath[file.relativePath] = file
	}

	for _, pkg := range pkgs {
		if pkg == nil {
			continue
		}

		packageFiles, err := similarityPackageSourceFiles(pkg, root)
		if err != nil {
			return nil, err
		}

		for _, file := range packageFiles {
			if _, duplicate := filesByPath[file.relativePath]; !duplicate {
				filesByPath[file.relativePath] = file
			}
		}
	}

	paths := make([]string, 0, len(filesByPath))
	for path := range filesByPath {
		paths = append(paths, path)
	}

	sort.Strings(paths)

	files := make([]similarityBlockSourceFile, 0, len(paths))
	for _, path := range paths {
		files = append(files, filesByPath[path])
	}

	return files, nil
}

func similarityPackageSourceFiles(
	pkg *LoadedPackage,
	root string,
) ([]similarityBlockSourceFile, error) {
	syntax := make(map[string]*ast.File, len(pkg.Files))
	for _, file := range pkg.Files {
		if file != nil && pkg.FSet != nil {
			filename := pkg.FSet.PositionFor(file.Package, true).Filename
			syntax[filepath.Clean(filename)] = file
		}
	}

	sources, err := similarityFileSources(pkg, pkg.repoFiles, root)
	for index := range sources {
		sources[index].file = syntax[sources[index].absolutePath]
	}

	return sources, err
}

// similarityFileSources hashes files of pkg, or script files for a nil pkg.
func similarityFileSources(
	pkg *LoadedPackage,
	filenames []string,
	root string,
) ([]similarityBlockSourceFile, error) {
	files := make([]similarityBlockSourceFile, 0, len(filenames))
	for _, filename := range filenames {
		absolutePath := filepath.Clean(filename)

		relativePath, err := filepath.Rel(root, absolutePath)
		if err != nil {
			return nil, fmt.Errorf("resolve similarity path for %s: %w", filename, err)
		}

		relativePath = filepath.ToSlash(relativePath)
		if !filepath.IsLocal(relativePath) {
			return nil, fmt.Errorf("similarity source %s is outside module root", filename)
		}

		content, err := os.ReadFile(absolutePath)
		if err != nil {
			return nil, err
		}

		digest := sha256.Sum256(content)
		file := similarityBlockSourceFile{
			pkg:          pkg,
			absolutePath: absolutePath,
			relativePath: relativePath,
			contentHash:  hex.EncodeToString(digest[:]),
		}

		if pkg != nil {
			file.fset = pkg.FSet
		}

		files = append(files, file)
	}

	return files, nil
}

func collectSimilaritySourceFileBlocks(
	source similarityBlockSourceFile,
	root string,
) ([]*similarityBlock, error) {
	if source.pkg == nil {
		return collectSimilarityScriptBlocks(source)
	}

	fset := source.fset
	file := source.file

	if file == nil || fset == nil {
		fset = token.NewFileSet()

		var err error

		file, err = parser.ParseFile(fset, source.absolutePath, nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", source.absolutePath, err)
		}
	}

	if ast.IsGenerated(file) {
		return nil, nil
	}

	pkg := *source.pkg
	pkg.FSet = fset

	return collectSimilarityFileBlocks(&pkg, file, root)
}

// collectSimilarityScriptBlocks returns the blocks of a TypeScript or
// Python file. Go's scanner tokenizes their comment-free content closely
// enough for the token minimum and the reported structure score.
func collectSimilarityScriptBlocks(source similarityBlockSourceFile) ([]*similarityBlock, error) {
	content, err := os.ReadFile(source.absolutePath)
	if err != nil {
		return nil, err
	}

	file, err := outline.Parse(source.absolutePath, content)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", source.absolutePath, err)
	}

	packageDir := filepath.ToSlash(filepath.Dir(source.relativePath))
	if packageDir == "." {
		packageDir = ""
	}

	var blocks []*similarityBlock

	for _, block := range file.Blocks {
		tokens := normalizedSimilarityTokens(block.Content)
		if len(tokens) < similarityMinimumTokens {
			continue
		}

		sum := sha256.Sum256([]byte(block.Content))
		blocks = append(blocks, &similarityBlock{
			Identity:     source.relativePath + "::" + block.Symbol,
			Symbol:       block.Symbol,
			PackageDir:   packageDir,
			PackageParts: similarityPathParts(packageDir),
			RelativePath: source.relativePath,
			Content:      block.Content,
			ContentHash:  hex.EncodeToString(sum[:]),
			Position:     scriptPosition(source.absolutePath, block.Pos),
			IsTest:       file.Test,
			Structural:   structuralShingles(tokens),
		})
	}

	return blocks, nil
}
