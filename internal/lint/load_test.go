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
)

type packageMeta struct {
	Dir             string   `json:"Dir"`
	ImportPath      string   `json:"ImportPath"`
	Name            string   `json:"Name"`
	Export          string   `json:"Export"`
	GoFiles         []string `json:"GoFiles"`
	CompiledGoFiles []string `json:"CompiledGoFiles"`
	Match           []string `json:"Match"`
	Error           *struct {
		Err string `json:"Err"`
	} `json:"Error"`
}

// LoadPackages resolves Go package patterns using `go list`, parses the matched packages,
// and type-checks them using export data for imports.
func LoadPackages(patterns []string) ([]*LoadedPackage, error) {
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

	metas, err := goList(patterns)
	if err != nil {
		return nil, err
	}

	if len(metas) == 0 {
		return nil, errors.New("no packages matched")
	}

	byImportPath := make(map[string]*packageMeta, len(metas))

	var targets []*packageMeta

	for _, meta := range metas {
		byImportPath[meta.ImportPath] = meta
		if len(meta.Match) > 0 {
			targets = append(targets, meta)
		}
	}

	if len(targets) == 0 {
		return nil, errors.New("no matched packages were returned by go list")
	}

	sort.Slice(targets, func(i, j int) bool {
		return targets[i].ImportPath < targets[j].ImportPath
	})

	loaded := make([]*LoadedPackage, 0, len(targets))
	for _, meta := range targets {
		pkg, err := loadOne(meta, byImportPath)
		if err != nil {
			return nil, err
		}

		loaded = append(loaded, pkg)
	}

	return loaded, nil
}

func goList(patterns []string) ([]*packageMeta, error) {
	args := append([]string{"list", "-deps", "-export", "-compiled", "-json"}, patterns...)
	cmd := exec.Command("go", args...)

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

func loadOne(meta *packageMeta, byImportPath map[string]*packageMeta) (*LoadedPackage, error) {
	filesOnDisk := meta.CompiledGoFiles
	if len(filesOnDisk) == 0 {
		filesOnDisk = meta.GoFiles
	}

	if len(filesOnDisk) == 0 {
		return nil, fmt.Errorf("package %s has no Go files to analyze", meta.ImportPath)
	}

	fset := token.NewFileSet()

	files := make([]*ast.File, 0, len(filesOnDisk))
	for _, name := range filesOnDisk {
		filename := name
		if !filepath.IsAbs(filename) {
			filename = filepath.Join(meta.Dir, filename)
		}

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

	typesPkg, err := conf.Check(meta.ImportPath, fset, files, info)
	if err != nil {
		return nil, fmt.Errorf("type-check %s: %w", meta.ImportPath, err)
	}

	return &LoadedPackage{
		ImportPath: meta.ImportPath,
		Name:       meta.Name,
		Dir:        meta.Dir,
		FSet:       fset,
		Files:      files,
		TypesPkg:   typesPkg,
		TypesInfo:  info,
	}, nil
}
