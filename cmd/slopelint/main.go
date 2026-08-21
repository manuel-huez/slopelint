package main

import (
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

type similarityMode uint8

const (
	similarityLocal similarityMode = iota
	similarityCI
	similarityOff
)

func main() {
	if analysisDriverRequested(os.Args[1:]) {
		singlechecker.Main(slopelint.Analyzer)
		return
	}

	os.Exit(runStandalone(os.Args[1:], os.Stderr))
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
		"json":       {},
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

func runStandalone(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("slopelint", flag.ContinueOnError)
	flags.SetOutput(stderr)

	maxStates := flags.Int(
		"max-states",
		defaultMaxStates,
		"maximum number of symbolic states before widening",
	)
	cacheEnabled := flags.Bool("cache", true, "reuse cached analysis for unchanged packages")
	cacheDir := flags.String("cache-dir", "", "directory for persistent analysis cache")
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

	mode, err := similarityModeFromEnv()
	if err != nil {
		return reportStandaloneError(stderr, err)
	}

	pkgs, err := lint.LoadPackages(patterns)
	if err != nil {
		return reportStandaloneError(stderr, err)
	}

	issues := lint.LintPackages(pkgs, lint.Options{
		MaxStates:    *maxStates,
		CacheEnabled: *cacheEnabled && lint.CacheEnabledFromEnv(),
		CacheDir:     lint.ResolveCacheDir(*cacheDir),
		ClosedWorld:  *closedWorld,
	})

	if mode != similarityOff {
		similarityIssues, similarityErr := lint.CheckSimilarCode(pkgs, lint.SimilarityOptions{
			CI:              mode == similarityCI,
			CacheEnabled:    *cacheEnabled && lint.CacheEnabledFromEnv(),
			CacheDir:        lint.ResolveCacheDir(*cacheDir),
			AcceptedPairIDs: similarityAcceptedPairIDs(),
		})
		if similarityErr != nil {
			return reportStandaloneError(stderr, similarityErr)
		}

		issues = append(issues, similarityIssues...)
	}

	if len(issues) == 0 {
		return 0
	}

	if err := writeStandaloneIssues(stderr, issues); err != nil {
		return exitFailure
	}

	return exitIssues
}

func reportStandaloneError(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintf(stderr, "slopelint: %v\n", err)
	return exitFailure
}

func writeStandaloneIssues(stderr io.Writer, issues []lint.Issue) error {
	for _, issue := range issues {
		if _, err := fmt.Fprintf(
			stderr,
			"%s: %s\n",
			lint.FormatIssuePosition(issue),
			issue.Message,
		); err != nil {
			return err
		}
	}

	return nil
}

func similarityModeFromEnv() (similarityMode, error) {
	// Explicit override keeps local reproductions possible inside CI environments.
	value := strings.ToLower(strings.TrimSpace(os.Getenv("SLOPELINT_SIMILARITY")))
	switch value {
	case "local":
		return similarityLocal, nil
	case "ci":
		return similarityCI, nil
	case "off":
		return similarityOff, nil
	case "":
		// Cloudflare Workers Builds and Pages expose provider-specific markers in
		// addition to CI. Check all markers because build variables can be overridden.
		for _, name := range []string{"CI", "WORKERS_CI", "CF_PAGES"} {
			signal := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
			if signal != "" && signal != "0" && signal != "false" &&
				signal != "off" && signal != "no" {
				return similarityCI, nil
			}
		}

		return similarityLocal, nil
	default:
		return similarityLocal, fmt.Errorf(
			"SLOPELINT_SIMILARITY must be local, ci, or off; got %q",
			value,
		)
	}
}

func similarityAcceptedPairIDs() []string {
	return strings.FieldsFunc(os.Getenv("SLOPELINT_SIMILARITY_ACCEPT"), func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	})
}
