package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
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
	if analyzerProtocolRequested(os.Args[1:]) {
		singlechecker.Main(slopelint.Analyzer)
		return
	}

	os.Exit(runStandalone(os.Args[1:]))
}

func analyzerProtocolRequested(args []string) bool {
	for _, arg := range args {
		if arg == "-flags" || arg == "help" || strings.HasPrefix(arg, "-V") {
			return true
		}

		if strings.HasSuffix(arg, ".cfg") {
			return true
		}
	}

	return false
}

func runStandalone(args []string) int {
	flags := flag.NewFlagSet("slopelint", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)

	maxStates := flags.Int(
		"max-states",
		defaultMaxStates,
		"maximum number of symbolic states before widening",
	)
	cacheEnabled := flags.Bool("cache", true, "reuse cached analysis for unchanged packages")
	cacheDir := flags.String("cache-dir", "", "directory for persistent analysis cache")

	if err := flags.Parse(args); err != nil {
		return exitUsage
	}

	patterns := flags.Args()
	if len(patterns) == 0 {
		fmt.Fprintln(os.Stderr, "usage: slopelint [flags] [package patterns]")
		return exitUsage
	}

	pkgs, err := lint.LoadPackages(patterns)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slopelint: %v\n", err)
		return exitFailure
	}

	issues := lint.LintPackages(pkgs, lint.Options{
		MaxStates:    *maxStates,
		CacheEnabled: *cacheEnabled && lint.CacheEnabledFromEnv(),
		CacheDir:     lint.ResolveCacheDir(*cacheDir),
	})
	if len(issues) == 0 {
		return 0
	}

	sort.Slice(issues, func(i, j int) bool {
		left := lint.FormatIssuePosition(issues[i])
		right := lint.FormatIssuePosition(issues[j])

		return left < right
	})

	for _, issue := range issues {
		fmt.Fprintf(os.Stderr, "%s: %s\n", lint.FormatIssuePosition(issue), issue.Message)
	}

	return exitIssues
}
