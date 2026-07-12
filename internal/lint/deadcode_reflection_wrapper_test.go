package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoDeadCodeKeepsCyclicLocalWrapperFields(t *testing.T) {
	tmp := newTestModule(t)
	writeTestMain(t, tmp, `	_, _ = lib.Save()`)
	writeFile(t, filepath.Join(tmp, "lib", "lib.go"), `package lib

import "encoding/json"

type Payload struct {
	Name string `+"`json:\"name\"`"+`
}

type dummy struct {
	Other string `+"`json:\"other\"`"+`
}

var warm = func() any {
	_, _ = encodeA(dummy{})
	return nil
}()

func encodeA(value any) ([]byte, error) {
	_, _ = encodeB(value)
	return json.Marshal(value)
}

func encodeB(value any) ([]byte, error) {
	return encodeA(value)
}

func Save() ([]byte, error) {
	return encodeB(Payload{})
}
`)

	issues := lintInDir(t, tmp)
	joined := joinMessages(issues)

	if strings.Contains(joined, wantFieldPayloadName) {
		t.Fatalf("cyclic local wrapper field reported dead, got:\n%s", joined)
	}
}
