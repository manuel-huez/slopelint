package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/manuel-huez/slopelint"
	"github.com/manuel-huez/slopelint/internal/lint"
	"golang.org/x/tools/go/analysis/singlechecker"
)

const (
	defaultMaxStates = 32
	exitFailure      = 1
	exitUsage        = 2
	exitIssues       = 3
	exitInterrupted  = 130

	// Cancellation kills Codex processes at once; structural lint holds no
	// outside resources, so exit after this grace instead of waiting for it.
	interruptGrace = 2 * time.Second
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

	// Non-interactive shells start background jobs with SIGINT ignored. Handle it
	// anyway: Ctrl-C on a parallel check script must stop Codex spend, not leave
	// an orphaned slopelint generating descriptions.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	done := make(chan int, 1)

	go func() { done <- runStandalone(ctx, os.Args[1:], os.Stdout, os.Stderr) }()

	var code int

	select {
	case code = <-done:
	case <-ctx.Done():
		select {
		case <-done:
		case <-time.After(interruptGrace):
		}

		_, _ = fmt.Fprintln(os.Stderr, "slopelint: stopped by signal")
		code = exitInterrupted
	}

	stop()
	os.Exit(code)
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

func runStandalone(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("slopelint", flag.ContinueOnError)
	flags.SetOutput(stderr)

	maxStates := flags.Int(
		"max-states",
		defaultMaxStates,
		"maximum number of symbolic states before widening",
	)
	jsonOutput := flags.Bool("json", false, "emit repository-aware JSON diagnostics")
	cacheEnabled := flags.Bool("cache", true, "reuse cached analysis for unchanged packages")
	closedWorld := flags.Bool(
		"closed-world",
		false,
		"treat the module's main packages as the complete set of production entrypoints; needs ./... from the module root",
	)

	printVersion := flags.Bool("version", false, "print the slopelint version and exit")

	if err := flags.Parse(args); err != nil {
		return exitUsage
	}

	if *printVersion {
		_, err := fmt.Fprintf(stdout, "slopelint %s %s\n", buildVersion(), runtime.Version())
		if err != nil {
			return exitFailure
		}

		return 0
	}

	patterns := flags.Args()
	if message := standaloneUsageError(patterns, *closedWorld); message != "" {
		if _, err := fmt.Fprintln(stderr, message); err != nil {
			return exitFailure
		}

		return exitUsage
	}

	cacheActive := *cacheEnabled && lint.CacheEnabledFromEnv()
	options := lint.Options{
		MaxStates:    *maxStates,
		CacheEnabled: cacheActive,
		ClosedWorld:  *closedWorld,
	}

	similarityOptions, err := similarityOptionsFromEnv(cacheActive)
	if err != nil {
		return reportStandaloneError(stderr, err)
	}

	issues, err := lint.LintRepository(ctx, patterns, ".", options, similarityOptions)
	if err != nil {
		return reportStandaloneError(stderr, err)
	}

	if *jsonOutput {
		err = writeJSONIssues(stdout, issues)
	} else {
		err = writeStandaloneIssues(stderr, issues)
	}

	if err != nil {
		return reportStandaloneError(stderr, err)
	}

	if len(issues) > 0 {
		return exitIssues
	}

	return 0
}

// standaloneUsageError returns why the arguments cannot run, or "". Closed-
// world dead code treats the scan as the whole program, so a partial scan
// would report code that unscanned packages use.
func standaloneUsageError(patterns []string, closedWorld bool) string {
	if len(patterns) == 0 {
		return "usage: slopelint [flags] [package patterns]"
	}

	if !closedWorld {
		return ""
	}

	if _, err := os.Stat("go.mod"); err != nil || !slices.Equal(patterns, []string{"./..."}) {
		return "slopelint: -closed-world needs the whole module: run `slopelint -closed-world ./...` from the module root"
	}

	return ""
}

// similarityOptionsFromEnv returns nil when SLOPELINT_SIMILARITY or CI markers
// turn semantic similarity off.
func similarityOptionsFromEnv(cacheActive bool) (*lint.SimilarityOptions, error) {
	mode, err := similarityModeFromEnv()
	if err != nil || mode == similarityOff {
		return nil, err
	}

	// Empty values keep the defaults tuned in the lint package.
	var threshold float64
	if value := strings.TrimSpace(os.Getenv("SLOPELINT_SIMILARITY_THRESHOLD")); value != "" {
		threshold, err = strconv.ParseFloat(value, 64)
		if err != nil || !(threshold > 0 && threshold < 1) {
			return nil, fmt.Errorf(
				"SLOPELINT_SIMILARITY_THRESHOLD must be a number between 0 and 1; got %q",
				value,
			)
		}
	}

	return &lint.SimilarityOptions{
		CI:              mode == similarityCI,
		CacheEnabled:    cacheActive,
		AcceptedPairIDs: similarityAcceptedPairIDs(),
		EmbeddingModel:  strings.TrimSpace(os.Getenv("SLOPELINT_EMBEDDING_MODEL")),
		Threshold:       threshold,
	}, nil
}

// buildVersion returns the module version stamped by go install or go build;
// go run builds report "(devel)".
func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}

	return "(devel)"
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
