package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestConstValueTestLintRequiresBehavior(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "go.mod"), `module example.com/sample

go 1.22

require github.com/stretchr/testify v0.0.0

replace github.com/stretchr/testify => ./testify
`)
	writeTestGoMod(t, filepath.Join(tmp, "testify"), "github.com/stretchr/testify")
	writeFile(t, filepath.Join(tmp, "testify", "assert", "assert.go"), `package assert

import "testing"

func Equal(t *testing.T, expected, actual any) bool { return true }
`)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "fmt"

const defaultLimit = 10
const defaultTitle = "alpha"
var runtimeLimit = 10
var defaultBanner = "alpha,beta"
var mutableBanner = "alpha,beta"

type Policy struct {
	Limit    int
	Disabled bool
}

var defaultPolicy = Policy{Limit: defaultLimit}
var mutablePolicy = Policy{Limit: defaultLimit}

func DefaultLimit() int { return defaultLimit }

func DefaultPolicy() Policy { return defaultPolicy }

func MutablePolicy() Policy { return mutablePolicy }

func (policy *Policy) Enable() { policy.Disabled = true }

func Panic() { panic("boom") }

func SetMutableBanner(value string) { mutableBanner = value }

func MutatePolicy() { mutablePolicy.Enable() }

func BannerFor(value string) string { return fmt.Sprintf("banner:%s", value) }

func ClampLimit(value int) int {
	if value > defaultLimit {
		return defaultLimit
	}

	return value
}
`)
	writeFile(t, filepath.Join(tmp, "sample_test.go"), `package sample

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultLimitConst(t *testing.T) {
	if got, want := defaultLimit, 10; got != want {
		t.Fatalf("defaultLimit = %d, want %d", got, want)
	}
}

func TestDefaultLimitAssertion(t *testing.T) {
	assert.Equal(t, 10, defaultLimit)
}

func TestLiteralArithmetic(t *testing.T) {
	if 2+2 != 4 {
		t.Fatal("two plus two should equal four")
	}
}

func TestDefaultLimitAccessor(t *testing.T) {
	if got := DefaultLimit(); got != defaultLimit {
		t.Fatalf("DefaultLimit() = %d, want %d", got, defaultLimit)
	}
}

func TestDefaultPolicyConst(t *testing.T) {
	policy := DefaultPolicy()
	if policy.Limit != defaultLimit {
		t.Fatalf("Limit = %d, want %d", policy.Limit, defaultLimit)
	}

	if policy.Disabled {
		t.Fatal("Disabled = true, want false")
	}
}

func TestDefaultTitleContainsFixedValue(t *testing.T) {
	if !strings.Contains(defaultTitle, "alp") {
		t.Fatal("defaultTitle must contain alp")
	}
}

func TestDefaultBannerContainsFixedValue(t *testing.T) {
	if !strings.Contains(defaultBanner, "alpha") {
		t.Fatal("defaultBanner must contain alpha")
	}
}

func TestClampLimitBehavior(t *testing.T) {
	if got := ClampLimit(defaultLimit + 1); got != defaultLimit {
		t.Fatalf("ClampLimit() = %d, want %d", got, defaultLimit)
	}
}

func TestPolicyMutationBehavior(t *testing.T) {
	policy := DefaultPolicy()
	policy.Enable()
	if !policy.Disabled {
		t.Fatal("Disabled = false, want true")
	}
}

func TestPanicBehavior(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Panic() did not panic")
		}
	}()

	Panic()
}

func TestRuntimeLimit(t *testing.T) {
	if runtimeLimit != defaultLimit {
		t.Fatalf("runtimeLimit = %d, want %d", runtimeLimit, defaultLimit)
	}
}

func TestMutableBanner(t *testing.T) {
	if !strings.Contains(mutableBanner, "alpha") {
		t.Fatal("mutableBanner must contain alpha")
	}
}

func TestBannerBehavior(t *testing.T) {
	if !strings.Contains(BannerFor("live"), "live") {
		t.Fatal("BannerFor() must include its input")
	}
}

func TestStaticBannerMutation(t *testing.T) {
	defaultBanner = "live"
	if !strings.Contains(defaultBanner, "live") {
		t.Fatal("defaultBanner mutation missing")
	}
}

func TestStaticPolicyMutation(t *testing.T) {
	defaultPolicy.Enable()
	if !defaultPolicy.Disabled {
		t.Fatal("defaultPolicy mutation missing")
	}
}

func TestMutablePackagePolicy(t *testing.T) {
	if MutablePolicy().Limit != defaultLimit {
		t.Fatal("MutablePolicy() must use defaultLimit")
	}
}
`)

	issues := lintInDir(t, tmp)
	count := 0
	joined := joinMessages(issues)

	for _, issue := range issues {
		if issue.Kind != "const_value_test" {
			continue
		}

		count++
	}

	if count != 8 ||
		!strings.Contains(
			joined,
			`test "TestDefaultLimitConst" only checks fixed package values`,
		) ||
		!strings.Contains(
			joined,
			`test "TestDefaultLimitAssertion" only checks fixed package values`,
		) ||
		!strings.Contains(
			joined,
			`test "TestLiteralArithmetic" only checks fixed package values`,
		) ||
		!strings.Contains(
			joined,
			`test "TestDefaultLimitAccessor" only checks fixed package values`,
		) ||
		!strings.Contains(
			joined,
			`test "TestDefaultPolicyConst" only checks fixed package values`,
		) ||
		!strings.Contains(
			joined,
			`test "TestDefaultTitleContainsFixedValue" only checks fixed package values`,
		) ||
		!strings.Contains(
			joined,
			`test "TestDefaultBannerContainsFixedValue" only checks fixed package values`,
		) ||
		!strings.Contains(joined, `test "TestRuntimeLimit" only checks fixed package values`) {
		t.Fatalf("const_value_test findings = %d, want 8 findings; got:\n%s", count, joined)
	}
}
