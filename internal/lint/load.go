package lint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

type packageMeta struct {
	Dir             string   `json:"Dir"`
	ImportPath      string   `json:"ImportPath"`
	Name            string   `json:"Name"`
	ForTest         string   `json:"ForTest"`
	Export          string   `json:"Export"`
	GoFiles         []string `json:"GoFiles"`
	CgoFiles        []string `json:"CgoFiles"`
	CompiledGoFiles []string `json:"CompiledGoFiles"`
	Match           []string `json:"Match"`
	Error           *struct {
		Err string `json:"Err"`
	} `json:"Error"`
}

func resolvePackageMetadata(
	patterns []string,
	dir string,
) ([]*packageMeta, map[string]*packageMeta, error) {
	if len(patterns) == 0 {
		patterns = []string{allPackagesPattern}
	}

	metas, err := goList(patterns, dir)
	if err != nil {
		return nil, nil, err
	}

	if len(metas) == 0 {
		return nil, nil, errors.New("no packages matched")
	}

	byImportPath := make(map[string]*packageMeta, len(metas))

	for _, meta := range metas {
		byImportPath[meta.ImportPath] = meta
	}

	targets := matchedPackageTargets(metas, byImportPath)
	if len(targets) == 0 {
		return nil, nil, errors.New("no matched packages were returned by go list")
	}

	sort.Slice(targets, func(i, j int) bool {
		return targets[i].targetImportPath() < targets[j].targetImportPath()
	})

	return targets, byImportPath, nil
}

func goList(patterns []string, dir string) ([]*packageMeta, error) {
	args := append(
		[]string{"list", "-buildvcs=false", "-deps", "-test", "-export", "-compiled", "-json"},
		patterns...,
	)
	cmd := exec.Command("go", args...)
	cmd.Dir = dir

	var (
		stdout bytes.Buffer
		stderr bytes.Buffer
	)

	cmd.Stdout = &stdout

	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("go %v failed: %w\n%s", args, err, stderr.String())
		}

		return nil, fmt.Errorf("go %v failed: %w", args, err)
	}

	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))

	var metas []*packageMeta

	for {
		var meta packageMeta
		if err := dec.Decode(&meta); err != nil {
			if err == io.EOF {
				break
			}

			return nil, fmt.Errorf("decode go list output: %w", err)
		}

		if meta.Error != nil {
			return nil, fmt.Errorf(
				"go list reported an error for %s: %s",
				meta.ImportPath,
				meta.Error.Err,
			)
		}

		metas = append(metas, &meta)
	}

	return metas, nil
}

func matchedPackageTargets(
	metas []*packageMeta,
	byImportPath map[string]*packageMeta,
) []*packageMeta {
	targetsByImportPath := make(map[string]*packageMeta)

	for _, meta := range metas {
		if len(meta.Match) == 0 {
			continue
		}

		targetImportPath := meta.ImportPath
		if meta.ForTest != "" {
			base := byImportPath[meta.ForTest]
			if base == nil || meta.Name != base.Name {
				continue
			}

			targetImportPath = meta.ForTest
		}

		if existing := targetsByImportPath[targetImportPath]; existing != nil &&
			existing.ForTest != "" {
			continue
		}

		targetsByImportPath[targetImportPath] = meta
	}

	targets := make([]*packageMeta, 0, len(targetsByImportPath))
	for _, target := range targetsByImportPath {
		targets = append(targets, target)
	}

	return targets
}

func loadPackageTargets(
	targets []*packageMeta,
	byImportPath map[string]*packageMeta,
) ([]*LoadedPackage, error) {
	loaded := make([]*LoadedPackage, len(targets))

	err := runPackageJobs(len(targets), func(index int) error {
		pkg, err := loadOne(targets[index], byImportPath)
		if err == nil {
			loaded[index] = pkg
		}

		return err
	})
	if err != nil {
		return nil, err
	}

	return loaded, nil
}

func loadPackageSyntax(pkgs []*LoadedPackage) error {
	return runPackageJobs(len(pkgs), func(index int) error {
		pkg := pkgs[index]
		if pkg == nil || len(pkg.Files) > 0 {
			return nil
		}

		fset := token.NewFileSet()

		files := make([]*ast.File, 0, len(pkg.repoFiles))
		for _, filename := range pkg.repoFiles {
			file, err := parser.ParseFile(fset, filename, nil, parser.ParseComments)
			if err != nil {
				return fmt.Errorf("parse %s: %w", filename, err)
			}

			files = append(files, file)
		}

		pkg.FSet = fset
		pkg.Files = files

		return nil
	})
}

func runPackageJobs(count int, work func(int) error) error {
	if count == 0 {
		return nil
	}

	errs := make([]error, count)
	jobs := make(chan int)

	var wg sync.WaitGroup
	for range loadWorkerCount(count) {
		wg.Go(func() {
			for index := range jobs {
				errs[index] = work(index)
			}
		})
	}

	for index := range count {
		jobs <- index
	}

	close(jobs)
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	return nil
}

func loadWorkerCount(targetCount int) int {
	if targetCount <= 1 {
		return 1
	}

	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		return 1
	}

	if workers > targetCount {
		return targetCount
	}

	return workers
}

func loadOne(meta *packageMeta, byImportPath map[string]*packageMeta) (*LoadedPackage, error) {
	compiledFiles, err := packageCompiledFiles(meta)
	if err != nil {
		return nil, err
	}

	repoFiles, err := packageRepoFiles(meta)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()

	files := make([]*ast.File, 0, len(compiledFiles))
	for _, filename := range compiledFiles {
		file, err := parser.ParseFile(fset, filename, nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", filename, err)
		}

		files = append(files, file)
	}

	lookup := func(path string) (io.ReadCloser, error) {
		dep := byImportPath[path]
		if dep == nil {
			return nil, fmt.Errorf("missing package metadata for import %q", path)
		}

		if dep.Export == "" {
			return nil, fmt.Errorf("missing export data for import %q", path)
		}

		return os.Open(dep.Export)
	}

	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
		Scopes:     make(map[ast.Node]*types.Scope),
		Instances:  make(map[*ast.Ident]types.Instance),
	}

	conf := types.Config{
		Importer: importer.ForCompiler(fset, runtime.Compiler, lookup),
		Error: func(err error) {
			// The type checker will keep going, and Check will return a summarized error.
		},
	}

	importPath := meta.targetImportPath()

	typesPkg, err := conf.Check(importPath, fset, files, info)
	if err != nil {
		return nil, fmt.Errorf("type-check %s: %w", importPath, err)
	}

	files = packageAnalysisFiles(files, fset, meta.Dir)

	return &LoadedPackage{
		ImportPath: importPath,
		Name:       meta.Name,
		Dir:        meta.Dir,
		repoFiles:  repoFiles,
		FSet:       fset,
		Files:      files,
		TypesPkg:   typesPkg,
		TypesInfo:  info,
	}, nil
}

func packageAnalysisFiles(
	files []*ast.File,
	fset *token.FileSet,
	packageDir string,
) []*ast.File {
	kept := files[:0]

	for _, file := range files {
		if file == nil {
			continue
		}

		if !ast.IsGenerated(file) {
			kept = append(kept, file)
			continue
		}

		filename := fset.PositionFor(file.Package, true).Filename

		relative, err := filepath.Rel(packageDir, filename)
		if err == nil && relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			kept = append(kept, file)
		}
	}

	return kept
}

func packageCompiledFiles(meta *packageMeta) ([]string, error) {
	filesOnDisk := meta.CompiledGoFiles
	if len(filesOnDisk) == 0 {
		filesOnDisk = meta.GoFiles
	}

	return resolvePackageFiles(meta, filesOnDisk)
}

func packageRepoFiles(meta *packageMeta) ([]string, error) {
	// CGo compiler files live in the build cache. Original GoFiles plus CgoFiles
	// are the stable source set for cache keys and semantic similarity stamps.
	filesOnDisk := append(append([]string(nil), meta.GoFiles...), meta.CgoFiles...)
	if len(filesOnDisk) == 0 {
		filesOnDisk = meta.CompiledGoFiles
	}

	return resolvePackageFiles(meta, filesOnDisk)
}

func resolvePackageFiles(meta *packageMeta, filesOnDisk []string) ([]string, error) {
	if len(filesOnDisk) == 0 {
		return nil, fmt.Errorf("package %s has no Go files to analyze", meta.ImportPath)
	}

	files := make([]string, len(filesOnDisk))
	for index, name := range filesOnDisk {
		if filepath.IsAbs(name) {
			files[index] = name
			continue
		}

		files[index] = filepath.Join(meta.Dir, name)
	}

	return files, nil
}

func (meta *packageMeta) targetImportPath() string {
	if meta.ForTest != "" {
		return meta.ForTest
	}

	return meta.ImportPath
}
