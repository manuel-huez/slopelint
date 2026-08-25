package lint

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
	root string,
	previous similarityScanCache,
) ([]similarityCachedFile, []*similarityBlock, error) {
	files, err := similaritySourceFiles(pkgs, root)
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
		strings.HasSuffix(relativePath, "_test.go"),
	) + "\x00" + contentHash
}
