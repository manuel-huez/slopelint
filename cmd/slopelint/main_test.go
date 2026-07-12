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
	var stderr strings.Builder

	if code := runStandalone([]string{"-cache=false", "."}, &stderr); code != 0 {
		t.Fatalf("runStandalone exit = %d, want 0:\n%s", code, stderr.String())
	}
}
