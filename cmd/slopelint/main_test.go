package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	allPackages   = "./..."
	cacheDisabled = "-cache=false"
	jsonFlag      = "-json"
)

func TestAnalysisDriverRequested(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "json", args: []string{jsonFlag, allPackages}, want: false},
		{name: "double dash json", args: []string{"--json", allPackages}, want: false},
		{name: "test value", args: []string{"-test=false", allPackages}, want: true},
		{name: "context", args: []string{"-c=2", allPackages}, want: true},
		{name: "fix", args: []string{"-fix", allPackages}, want: true},
		{name: "diff", args: []string{"-diff", allPackages}, want: true},
		{name: "legacy source", args: []string{"-source", allPackages}, want: true},
		{name: "legacy tags", args: []string{"-tags=integration", allPackages}, want: true},
		{name: "profile", args: []string{"-cpuprofile=cpu.out", allPackages}, want: true},
		{name: "debug", args: []string{"-debug=f", allPackages}, want: true},
		{name: "unitchecker config", args: []string{"vet.cfg"}, want: true},
		{name: "standalone", args: []string{"-closed-world", allPackages}, want: false},
		{name: "standalone cache", args: []string{cacheDisabled, allPackages}, want: false},
		{name: "standalone version", args: []string{"-version"}, want: false},
		{name: "after separator", args: []string{"--", jsonFlag}, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := analysisDriverRequested(test.args); got != test.want {
				t.Fatalf("analysisDriverRequested(%q) = %t, want %t", test.args, got, test.want)
			}
		})
	}
}

func TestRunStandaloneRejectsUsageErrors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no patterns", want: "usage: slopelint"},
		{
			name: "partial closed world",
			args: []string{"-closed-world", "./internal/..."},
			want: "-closed-world needs the whole module",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var stderr strings.Builder

			if code := runStandalone(
				t.Context(),
				test.args,
				io.Discard,
				&stderr,
			); code != exitUsage {
				t.Fatalf("runStandalone exit = %d, want %d", code, exitUsage)
			}

			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.want)
			}
		})
	}
}

func TestRunStandalonePrintsVersionWithoutPackages(t *testing.T) {
	t.Parallel()

	var stdout strings.Builder

	if code := runStandalone(t.Context(), []string{"-version"}, &stdout, io.Discard); code != 0 {
		t.Fatalf("runStandalone exit = %d, want 0", code)
	}

	if !strings.HasPrefix(stdout.String(), "slopelint ") {
		t.Fatalf("version output = %q, want slopelint prefix", stdout.String())
	}
}

func TestRunStandaloneChecksCurrentPackage(t *testing.T) {
	t.Setenv("SLOPELINT_SIMILARITY", "off")

	var stderr strings.Builder

	if code := runStandalone(
		t.Context(),
		[]string{cacheDisabled, "."},
		io.Discard,
		&stderr,
	); code != 0 {
		t.Fatalf("runStandalone exit = %d, want 0:\n%s", code, stderr.String())
	}
}

func TestRunStandaloneWritesRepositoryJSON(t *testing.T) {
	t.Setenv("SLOPELINT_SIMILARITY", "off")

	var (
		stdout strings.Builder
		stderr strings.Builder
	)

	code := runStandalone(t.Context(), []string{cacheDisabled, jsonFlag, "."}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runStandalone exit = %d, want 0:\n%s", code, stderr.String())
	}

	var output struct {
		Issues []standaloneJSONIssue `json:"issues"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &output); err != nil {
		t.Fatalf("decode JSON output: %v\n%s", err, stdout.String())
	}

	if output.Issues == nil {
		t.Fatal("JSON issues must encode as an array")
	}
}

func TestSimilarityModeAutoDetectsCI(t *testing.T) {
	tests := []struct {
		name     string
		variable string
		value    string
	}{
		{name: "generic", variable: "CI", value: "true"},
		{name: "Cloudflare Workers Builds", variable: "WORKERS_CI", value: "1"},
		{name: "Cloudflare Pages", variable: "CF_PAGES", value: "1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("SLOPELINT_SIMILARITY", "")
			t.Setenv("CI", "")
			t.Setenv("WORKERS_CI", "")
			t.Setenv("CF_PAGES", "")
			t.Setenv(test.variable, test.value)

			mode, err := similarityModeFromEnv()
			if err != nil {
				t.Fatal(err)
			}

			if mode != similarityCI {
				t.Fatalf("similarity mode = %d, want CI", mode)
			}
		})
	}
}

func TestSimilarityModeIgnoresFalseCIMarkers(t *testing.T) {
	t.Setenv("SLOPELINT_SIMILARITY", "")
	t.Setenv("CI", "false")
	t.Setenv("WORKERS_CI", "0")
	t.Setenv("CF_PAGES", "off")

	mode, err := similarityModeFromEnv()
	if err != nil {
		t.Fatal(err)
	}

	if mode != similarityLocal {
		t.Fatalf("similarity mode = %d, want local", mode)
	}
}

func TestSimilarityModeExplicitLocalOverridesCI(t *testing.T) {
	t.Setenv("SLOPELINT_SIMILARITY", "local")
	t.Setenv("WORKERS_CI", "1")

	mode, err := similarityModeFromEnv()
	if err != nil {
		t.Fatal(err)
	}

	if mode != similarityLocal {
		t.Fatalf("similarity mode = %d, want local", mode)
	}
}

func TestSimilarityOptionsReadModelAndThreshold(t *testing.T) {
	t.Setenv("SLOPELINT_SIMILARITY", "local")
	t.Setenv("SLOPELINT_EMBEDDING_MODEL", " custom-model ")
	t.Setenv("SLOPELINT_SIMILARITY_THRESHOLD", "0.95")

	options, err := similarityOptionsFromEnv(false)
	if err != nil || options.EmbeddingModel != "custom-model" || options.Threshold != 0.95 {
		t.Fatalf("options = %+v, err = %v", options, err)
	}

	for _, value := range []string{"high", "0", "1", "NaN"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("SLOPELINT_SIMILARITY_THRESHOLD", value)

			var stderr strings.Builder

			code := runStandalone(t.Context(), []string{cacheDisabled, "."}, io.Discard, &stderr)
			if code != exitFailure ||
				!strings.Contains(stderr.String(), "SLOPELINT_SIMILARITY_THRESHOLD must be") {
				t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
			}
		})
	}
}

// TestRunStandaloneGroupsFindingsByRule checks that a rule's guide prints
// once above its findings, in text and JSON output.
func TestRunStandaloneGroupsFindingsByRule(t *testing.T) {
	t.Setenv("SLOPELINT_SIMILARITY", "off")

	dir := t.TempDir()
	for name, content := range map[string]string{
		"go.mod": "module example.com/sample\n\ngo 1.22\n",
		"sample.go": `package sample

func First(a, b, c, d, e, f, g int) int { return a + b + c + d + e + f + g }

func Second(a, b, c, d, e, f, g int) int { return a * b * c * d * e * f * g }
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Chdir(dir)

	var stdout, stderr strings.Builder

	if code := runStandalone(
		t.Context(),
		[]string{cacheDisabled, "./..."},
		&stdout,
		&stderr,
	); code != exitIssues {
		t.Fatalf("exit = %d, want %d:\n%s", code, exitIssues, stderr.String())
	}

	text := stderr.String()
	if strings.Count(text, "long_param_list: Functions with over 6 parameters (2)\n  Fix: ") != 1 ||
		!strings.Contains(text, `sample.go:3:6: function "First" has 7 parameters`) ||
		!strings.Contains(text, `sample.go:5:6: function "Second" has 7 parameters`) {
		t.Fatalf("grouped output:\n%s", text)
	}

	stdout.Reset()
	_ = runStandalone(t.Context(), []string{cacheDisabled, jsonFlag, "./..."}, &stdout, io.Discard)

	var output struct {
		Rules map[string]standaloneJSONRule `json:"rules"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &output); err != nil ||
		output.Rules["long_param_list"].Fix == "" {
		t.Fatalf("JSON rules = %v, err = %v", output.Rules, err)
	}
}
