package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoDeadCodeKeepsBranchAssignedGenericCallbackDecodeFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Load(nil, true)
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "encoding/json"

type Payload struct {
	Name    string `+"`json:\"name\"`"+`
	Ignored string `+"`json:\"-\"`"+`
}

func Load(body []byte, primary bool) error {
	return consume[Payload](body, primary)
}

func consume[T any](body []byte, primary bool) error {
	var visit func([]byte) error
	if primary {
		visit = func(data []byte) error {
			var value T

			return json.Unmarshal(data, &value)
		}
	} else {
		visit = func(data []byte) error {
			var value T

			return json.Unmarshal(data, &value)
		}
	}

	return runVisit(body, visit)
}

func runVisit(body []byte, visit func([]byte) error) error {
	return visit(body)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, wantFieldPayloadName) {
		t.Fatalf("branch-assigned generic callback decode field reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Payload.Ignored" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected ignored JSON field finding, got:\n%s", joined)
	}
}

func TestRepoDeadCodeKeepsSwitchAssignedGenericCallbackDecodeFields(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "cmd", "app", "main.go"), `package main

import "example.com/sample/lib"

func main() {
	_ = lib.Load(nil, "primary")
}
`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "encoding/json"

type Payload struct {
	Name    string `+"`json:\"name\"`"+`
	Ignored string `+"`json:\"-\"`"+`
}

func Load(body []byte, mode string) error {
	return consume[Payload](body, mode)
}

func consume[T any](body []byte, mode string) error {
	var visit func([]byte) error
	switch mode {
	case "primary":
		visit = func(data []byte) error {
			var value T

			return json.Unmarshal(data, &value)
		}
	default:
		visit = func(data []byte) error {
			var value T

			return json.Unmarshal(data, &value)
		}
	}

	return runVisit(body, visit)
}

func runVisit(body []byte, visit func([]byte) error) error {
	return visit(body)
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, wantFieldPayloadName) {
		t.Fatalf("switch-assigned generic callback decode field reported dead, got:\n%s", joined)
	}

	if !strings.Contains(
		joined,
		`exported field "Payload.Ignored" is unreachable from repo entrypoints; remove it`,
	) {
		t.Fatalf("expected ignored JSON field finding, got:\n%s", joined)
	}
}
