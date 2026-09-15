package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/manuel-huez/slopelint"
	"github.com/manuel-huez/slopelint/internal/lint"
	"golang.org/x/tools/go/analysis/singlechecker"
)

const (
	defaultMaxStates = 32
	exitFailure      = 1
	exitUsage        = 2
	exitIssues       = 3
)

func main() {
	if analysisDriverRequested(os.Args[1:]) {
		singlechecker.Main(slopelint.Analyzer)
		return
	}

	os.Exit(runStandalone(os.Args[1:], os.Stdout, os.Stderr))
}

func analysisDriverRequested(args []string) bool {
	driverFlags := map[string]struct{}{
		"V":          {},
		"all":        {},
		"c":          {},
		"cpuprofile": {},
		"debug":      {},
		"diff":       {},
		"fix":        {},
		"flags":      {},
		"memprofile": {},
		"source":     {},
		"tags":       {},
		"test":       {},
		"trace":      {},
		"v":          {},
	}

	for _, arg := range args {
		if arg == "help" {
			return true
		}

		if arg == "--" {
			break
		}

		if strings.HasPrefix(arg, "-") {
			name := strings.TrimLeft(arg, "-")
			name, _, _ = strings.Cut(name, "=")

			if _, ok := driverFlags[name]; ok {
				return true
			}
		}

		if strings.HasSuffix(arg, ".cfg") {
			return true
		}
	}

	return false
}

func runStandalone(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("slopelint", flag.ContinueOnError)
	flags.SetOutput(stderr)

	maxStates := flags.Int(
		"max-states",
		defaultMaxStates,
		"maximum number of symbolic states before widening",
	)
	cacheEnabled := flags.Bool("cache", true, "reuse cached analysis for unchanged packages")
	cacheDir := flags.String("cache-dir", "", "directory for persistent analysis cache")
	jsonOutput := flags.Bool("json", false, "emit repository-aware JSON diagnostics")
	closedWorld := flags.Bool(
		"closed-world",
		false,
		"treat matched main packages as the complete set of production entrypoints",
	)

	if err := flags.Parse(args); err != nil {
		return exitUsage
	}

	patterns := flags.Args()
	if len(patterns) == 0 {
		if _, err := fmt.Fprintln(
			stderr,
			"usage: slopelint [flags] [package patterns]",
		); err != nil {
			return exitFailure
		}

		return exitUsage
	}

	pkgs, err := lint.LoadPackages(patterns)
	if err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "slopelint: %v\n", err); writeErr != nil {
			return exitFailure
		}

		return exitFailure
	}

	issues := lint.LintPackages(pkgs, lint.Options{
		MaxStates:    *maxStates,
		CacheEnabled: *cacheEnabled && lint.CacheEnabledFromEnv(),
		CacheDir:     lint.ResolveCacheDir(*cacheDir),
		ClosedWorld:  *closedWorld,
	})

	return reportStandaloneIssues(stdout, stderr, issues, *jsonOutput)
}

func reportStandaloneIssues(stdout, stderr io.Writer, issues []lint.Issue, jsonOutput bool) int {
	if jsonOutput {
		if err := writeJSONIssues(stdout, issues); err != nil {
			if _, writeErr := fmt.Fprintf(
				stderr,
				"slopelint: encode json: %v\n",
				err,
			); writeErr != nil {
				return exitFailure
			}

			return exitFailure
		}

		if len(issues) > 0 {
			return exitIssues
		}

		return 0
	}

	if len(issues) == 0 {
		return 0
	}

	for _, issue := range issues {
		if _, err := fmt.Fprintf(
			stderr,
			"%s: %s\n",
			lint.FormatIssuePosition(issue),
			issue.Message,
		); err != nil {
			return exitFailure
		}
	}

	return exitIssues
}

type standaloneJSONIssue struct {
	Position string `json:"position"`
	Kind     string `json:"kind"`
	Message  string `json:"message"`
}

func writeJSONIssues(out io.Writer, issues []lint.Issue) error {
	encoded := struct {
		Issues []standaloneJSONIssue `json:"issues"`
	}{
		Issues: make([]standaloneJSONIssue, 0, len(issues)),
	}

	for _, issue := range issues {
		encoded.Issues = append(encoded.Issues, standaloneJSONIssue{
			Position: lint.FormatIssuePosition(issue),
			Kind:     issue.Kind,
			Message:  issue.Message,
		})
	}

	return json.NewEncoder(out).Encode(encoded)
}
