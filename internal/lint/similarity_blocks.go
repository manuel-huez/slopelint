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

	reuse := newSimilarityBlockReuse(previous, root, files)
	cachedFiles := make([]similarityCachedFile, 0, len(files))
	blocks := make([]*similarityBlock, 0, len(previous.Blocks))

	for _, file := range files {
		cachedFiles = append(cachedFiles, similarityCachedFile{
			RelativePath: file.relativePath,
			ContentHash:  file.contentHash,
		})

		if reused, ok := reuse.blocks(file); ok {
			blocks = append(blocks, reused...)

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

// similarityBlockReuse hands out the previous scan's blocks for unchanged
// files, also when a file moved within its package. Each previous file is
// reused once, and never while a current file still has its path and content.
type similarityBlockReuse struct {
	previous  similarityScanCache
	root      string
	byPath    map[string]string
	byContent map[string][]string
	current   map[string]string
	used      map[string]struct{}
}

func newSimilarityBlockReuse(
	previous similarityScanCache,
	root string,
	files []similarityBlockSourceFile,
) similarityBlockReuse {
	reuse := similarityBlockReuse{
		previous:  previous,
		root:      root,
		byPath:    make(map[string]string, len(previous.Files)),
		byContent: make(map[string][]string, len(previous.Files)),
		current:   make(map[string]string, len(files)),
		used:      make(map[string]struct{}, len(previous.Files)),
	}

	for _, file := range previous.Files {
		reuse.byPath[file.RelativePath] = file.ContentHash
		key := similarityCachedFileReuseKey(file.RelativePath, file.ContentHash)
		reuse.byContent[key] = append(reuse.byContent[key], file.RelativePath)
	}

	for _, file := range files {
		reuse.current[file.relativePath] = file.contentHash
	}

	return reuse
}

func (reuse similarityBlockReuse) blocks(
	file similarityBlockSourceFile,
) ([]*similarityBlock, bool) {
	if reuse.byPath[file.relativePath] == file.contentHash {
		reuse.used[file.relativePath] = struct{}{}

		return reuse.previous.blocksForFile(reuse.root, file.relativePath, file.relativePath), true
	}

	for _, previousPath := range reuse.byContent[similarityCachedFileReuseKey(file.relativePath, file.contentHash)] {
		if _, used := reuse.used[previousPath]; used ||
			previousPath != file.relativePath && reuse.current[previousPath] == file.contentHash {
			continue
		}

		reuse.used[previousPath] = struct{}{}

		return reuse.previous.blocksForFile(reuse.root, previousPath, file.relativePath), true
	}

	return nil, false
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

	file, err := parseReadableScript(source.absolutePath, content)
	if err != nil {
		return nil, err
	}

	var blocks []*similarityBlock

	for _, block := range file.Blocks {
		if each := newSimilarityBlock(
			source.relativePath,
			block.Symbol,
			block.Content,
			scriptPosition(source.absolutePath, block.Pos),
			file.Test,
		); each != nil {
			blocks = append(blocks, each)
		}
	}

	return blocks, nil
}
