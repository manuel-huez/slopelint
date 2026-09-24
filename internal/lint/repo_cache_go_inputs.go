package lint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
)

// Go's package list includes ignored source files that Git's cheap untracked
// listing omits. Query Go instead of traversing every ignored tool cache.
func repoAnalysisApplyListedInputs(
	files map[string]string,
	root string,
	moduleRoot string,
	objectFormat string,
) (bool, error) {
	replacements, err := repoAnalysisReplacementRoots(moduleRoot, root)
	if err != nil {
		return false, err
	}

	needsIgnoredScan := false

	for _, module := range append([]string{moduleRoot}, replacements...) {
		cmd := exec.Command(
			"go",
			"list",
			"-find",
			"-buildvcs=false",
			"-json=Dir,Error,GoFiles,CgoFiles,IgnoredGoFiles,TestGoFiles,XTestGoFiles,OtherFiles,IgnoredOtherFiles,CFiles,CXXFiles,MFiles,HFiles,FFiles,SFiles,SwigFiles,SwigCXXFiles,SysoFiles,EmbedFiles,TestEmbedFiles,XTestEmbedFiles",
			"./...",
		)
		cmd.Dir = module

		output, err := cmd.Output()
		if err != nil {
			return false, fmt.Errorf("list source files in %s: %w", module, err)
		}

		decoder := json.NewDecoder(bytes.NewReader(output))

		for {
			var pkg repoAnalysisListedPackage
			if err := decoder.Decode(&pkg); err != nil {
				if err == io.EOF {
					break
				}

				return false, fmt.Errorf("decode Go source list: %w", err)
			}

			needsScan, err := repoAnalysisApplyListedPackage(files, root, objectFormat, pkg)
			if err != nil {
				return false, err
			}

			if needsScan {
				needsIgnoredScan = true
			}
		}

		for _, file := range []string{goModFilename, goSumFilename} {
			if err := repoAnalysisAddListedFile(
				files,
				root,
				filepath.Join(module, file),
				objectFormat,
			); err != nil {
				return false, err
			}
		}
	}

	if err := repoAnalysisAddListedFile(
		files, root, filepath.Join(moduleRoot, similarityStampName), objectFormat,
	); err != nil {
		return false, err
	}

	return needsIgnoredScan, nil
}

func repoAnalysisApplyListedPackage(
	files map[string]string,
	root string,
	objectFormat string,
	pkg repoAnalysisListedPackage,
) (bool, error) {
	if pkg.Error != nil {
		return false, fmt.Errorf("list Go source in %s: %s", pkg.Dir, pkg.Error.Err)
	}

	// Cgo can include headers in subdirectories not listed by go list.
	needsIgnoredScan := len(pkg.CgoFiles) > 0 || len(pkg.SwigFiles) > 0 ||
		len(pkg.SwigCXXFiles) > 0
	groups := [][]string{
		pkg.GoFiles, pkg.CgoFiles, pkg.IgnoredGoFiles,
		pkg.TestGoFiles, pkg.XTestGoFiles,
		pkg.OtherFiles, pkg.IgnoredOtherFiles,
		pkg.CFiles, pkg.CXXFiles, pkg.MFiles, pkg.HFiles,
		pkg.FFiles, pkg.SFiles, pkg.SwigFiles, pkg.SwigCXXFiles,
		pkg.SysoFiles, pkg.EmbedFiles, pkg.TestEmbedFiles, pkg.XTestEmbedFiles,
	}

	for _, group := range groups {
		for _, file := range group {
			if err := repoAnalysisAddListedFile(
				files,
				root,
				filepath.Join(pkg.Dir, file),
				objectFormat,
			); err != nil {
				return false, err
			}
		}
	}

	return needsIgnoredScan, nil
}

func repoAnalysisAddListedFile(files map[string]string, root, path, objectFormat string) error {
	name, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(name) {
		return errAnalysisCacheDisabled
	}

	name = filepath.ToSlash(name)

	objectID, err := repoAnalysisBlobID(root, name, objectFormat)
	if err != nil {
		return err
	}

	delete(files, name)

	if objectID != "" {
		files[name] = objectID
	}

	return nil
}

type repoAnalysisListedPackage struct {
	Dir               string                `json:"Dir"`
	Error             *struct{ Err string } `json:"Error"`
	GoFiles           []string              `json:"GoFiles"`
	CgoFiles          []string              `json:"CgoFiles"`
	IgnoredGoFiles    []string              `json:"IgnoredGoFiles"`
	TestGoFiles       []string              `json:"TestGoFiles"`
	XTestGoFiles      []string              `json:"XTestGoFiles"`
	OtherFiles        []string              `json:"OtherFiles"`
	IgnoredOtherFiles []string              `json:"IgnoredOtherFiles"`
	CFiles            []string              `json:"CFiles"`
	CXXFiles          []string              `json:"CXXFiles"`
	MFiles            []string              `json:"MFiles"`
	HFiles            []string              `json:"HFiles"`
	FFiles            []string              `json:"FFiles"`
	SFiles            []string              `json:"SFiles"`
	SwigFiles         []string              `json:"SwigFiles"`
	SwigCXXFiles      []string              `json:"SwigCXXFiles"`
	SysoFiles         []string              `json:"SysoFiles"`
	EmbedFiles        []string              `json:"EmbedFiles"`
	TestEmbedFiles    []string              `json:"TestEmbedFiles"`
	XTestEmbedFiles   []string              `json:"XTestEmbedFiles"`
}
