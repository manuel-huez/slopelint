package main

import (
	"strings"
	"testing"
)

const allPackages = "./..."

func TestAnalysisDriverRequested(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "json", args: []string{"-json", allPackages}, want: true},
		{name: "double dash json", args: []string{"--json", allPackages}, want: true},
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
		{name: "standalone cache", args: []string{"-cache=false", allPackages}, want: false},
		{name: "after separator", args: []string{"--", "-json"}, want: false},
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

func TestRunStandaloneRequiresPackagePattern(t *testing.T) {
	t.Parallel()

	var stderr strings.Builder

	if code := runStandalone(nil, &stderr); code != exitUsage {
		t.Fatalf("runStandalone exit = %d, want %d", code, exitUsage)
	}

	if !strings.Contains(stderr.String(), "usage: slopelint") {
		t.Fatalf("missing usage output: %q", stderr.String())
	}
}

func TestRunStandaloneChecksCurrentPackage(t *testing.T) {
	t.Setenv("SLOPELINT_SIMILARITY", "off")

	var stderr strings.Builder

	if code := runStandalone([]string{"-cache=false", "."}, &stderr); code != 0 {
		t.Fatalf("runStandalone exit = %d, want 0:\n%s", code, stderr.String())
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
