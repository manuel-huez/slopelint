package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestContractCommentsEstablishFactsAfterCall(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

//slopelint:ensures req != nil
//slopelint:ensures req.Name != ""
func requireReq(req *Req) {}

func f(req *Req) {
	requireReq(req)
	if req == nil { println("bad") }
	if req.Name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "req == nil" is always false here`) {
		t.Fatalf("expected contract-backed nil check, got:\n%s", joined)
	}

	if !strings.Contains(joined, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("expected contract-backed field check, got:\n%s", joined)
	}
}

func TestInfersGuardHelperFactsAcrossCallBoundary(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func mustReq(req *Req) {
	if req == nil { panic("bad") }
	if req.Name == "" { panic("bad") }
}

func f(req *Req) {
	mustReq(req)
	if req == nil { println("bad") }
	if req.Name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "req == nil" is always false here`) {
		t.Fatalf("expected inferred helper nil check, got:\n%s", joined)
	}

	if !strings.Contains(joined, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("expected inferred helper field check, got:\n%s", joined)
	}
}

func TestInfersPredicateFactsAcrossCallBoundary(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func valid(req *Req) bool {
	if req == nil { return false }
	if req.Name == "" { return false }
	return true
}

func f(req *Req) {
	if !valid(req) { return }
	if req == nil { println("bad") }
	if req.Name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "req == nil" is always false here`) {
		t.Fatalf("expected predicate-backed nil check, got:\n%s", joined)
	}

	if !strings.Contains(joined, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("expected predicate-backed field check, got:\n%s", joined)
	}
}

func TestInfersPredicateFactsAcrossMultipleCalls(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func valid(req *Req) bool {
	if req == nil { return false }
	if req.Name == "" { return false }
	return true
}

func valid2(req *Req) bool {
	return valid(req)
}

func f(req *Req) {
	if !valid2(req) { return }
	if req == nil { println("bad") }
	if req.Name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "req == nil" is always false here`) {
		t.Fatalf("expected multi-hop nil check, got:\n%s", joined)
	}

	if !strings.Contains(joined, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("expected multi-hop field check, got:\n%s", joined)
	}
}

func TestPropagatesPredicateFactsViaBoolVariable(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func valid(req *Req) bool {
	if req == nil { return false }
	if req.Name == "" { return false }
	return true
}

func f(req *Req) {
	ok := valid(req)
	if !ok { return }
	if req == nil { println("bad") }
	if req.Name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "req == nil" is always false here`) {
		t.Fatalf("expected bool-var nil check, got:\n%s", joined)
	}

	if !strings.Contains(joined, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("expected bool-var field check, got:\n%s", joined)
	}
}

func TestPropagatesPredicateFactsViaCopiedBoolVariable(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type Req struct { Name string }

func valid(req *Req) bool {
	if req == nil { return false }
	if req.Name == "" { return false }
	return true
}

func f(req *Req) {
	ok := valid(req)
	yes := ok
	if !yes { return }
	if req == nil { println("bad") }
	if req.Name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "req == nil" is always false here`) {
		t.Fatalf("expected copied bool-var nil check, got:\n%s", joined)
	}

	if !strings.Contains(joined, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("expected copied bool-var field check, got:\n%s", joined)
	}
}

func TestPropagatesNilFactsViaErrorVariable(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "errors"

type Req struct { Name string }

func validate(req *Req) error {
	if req == nil { return errors.New("bad") }
	if req.Name == "" { return errors.New("bad") }
	return nil
}

func f(req *Req) {
	err := validate(req)
	if err != nil { return }
	if req == nil { println("bad") }
	if req.Name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "req == nil" is always false here`) {
		t.Fatalf("expected error-var nil check, got:\n%s", joined)
	}

	if !strings.Contains(joined, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("expected error-var field check, got:\n%s", joined)
	}
}

func TestInfersFactsAcrossPackages(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), "module example.com/sample\n\ngo 1.22\n")
	writeFile(t, filepath.Join(tmp, "guard", "guard.go"), `package guard

import "errors"

type Req struct { Name string }

func Valid(req *Req) bool {
	if req == nil { return false }
	if req.Name == "" { return false }
	return true
}

func Check(req *Req) error {
	if req == nil { return errors.New("bad") }
	if req.Name == "" { return errors.New("bad") }
	return nil
}
`)
	writeFile(t, filepath.Join(tmp, "use", "use.go"), `package use

import "example.com/sample/guard"

func f(req *guard.Req) {
	ok := guard.Valid(req)
	if !ok { return }
	if req == nil { println("bad") }
	if req.Name == "" { println("bad") }
}

func g(req *guard.Req) {
	err := guard.Check(req)
	if err != nil { return }
	if req == nil { println("bad") }
	if req.Name == "" { println("bad") }
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if !strings.Contains(joined, `condition "req == nil" is always false here`) {
		t.Fatalf("expected cross-package nil check, got:\n%s", joined)
	}

	if !strings.Contains(joined, `condition "req.Name == \"\"" is always false here`) {
		t.Fatalf("expected cross-package field check, got:\n%s", joined)
	}
}
