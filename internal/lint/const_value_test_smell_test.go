package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnableTestLintsRequireBehavior(t *testing.T) {
	t.Parallel()

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

import (
	"fmt"
	"time"
)

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

func Pause() { time.Sleep(time.Millisecond) }

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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type limitHarness struct {
	t *testing.T
}

func checkLimit(t *testing.T, got int) {
	if got != defaultLimit {
		t.Fatalf("limit = %d, want %d", got, defaultLimit)
	}
}

func checkSubtest(t *testing.T) {
	checkLimit(t, ClampLimit(defaultLimit))
}

func TestClampWithoutFailure(t *testing.T) {
	ClampLimit(1)
}

func TestClampOnlyLogs(t *testing.T) {
	t.Log(ClampLimit(1))
	t.Run("sub", func(t *testing.T) {
		t.Logf("limit %d", ClampLimit(2))
	})
}

func TestClampHelper(t *testing.T) {
	checkLimit(t, ClampLimit(defaultLimit))
}

func TestClampNamedSubtest(t *testing.T) {
	t.Run("sub", checkSubtest)
}

func TestClampMethodValue(t *testing.T) {
	fail := t.Errorf
	if ClampLimit(1) != 1 {
		fail("ClampLimit(1) changed")
	}
}

func TestClampHarness(t *testing.T) {
	harness := limitHarness{t: t}
	_ = harness
}

func TestClampPanics(t *testing.T) {
	if ClampLimit(1) != 1 {
		panic("ClampLimit(1) changed")
	}
}

func TestClampHelperProcess(t *testing.T) {
	if os.Getenv("SAMPLE_HELPER") == "" {
		return
	}

	ClampLimit(1)
	os.Exit(0)
}

func TestClampSkipped(t *testing.T) {
	t.Skip("covered elsewhere")
	ClampLimit(1)
}

func BenchmarkClamp(b *testing.B) {
	for b.Loop() {
		ClampLimit(1)
	}
}

func TestClampAfterSleep(t *testing.T) {
	time.Sleep(time.Millisecond)
	go func() { time.Sleep(time.Millisecond) }()
	checkLimit(t, ClampLimit(defaultLimit))
}

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

	writeFile(t, filepath.Join(tmp, "serial_test.go"), `package sample

import "testing"

func parallelHelper(t *testing.T) {
	t.Parallel()
}

func useEnv(t *testing.T) {
	t.Setenv("SAMPLE", "1")
}

func TestParallelDirect(t *testing.T) {
	checkLimit(t, ClampLimit(defaultLimit))
	t.Parallel()
}

func TestParallelHelper(t *testing.T) {
	parallelHelper(t)
	checkLimit(t, ClampLimit(defaultLimit))
}

func TestParallelOnlySubtest(t *testing.T) {
	t.Run("sub", func(t *testing.T) {
		t.Parallel()
		checkLimit(t, ClampLimit(defaultLimit))
	})
}

func TestSerialSubtestSetenv(t *testing.T) {
	t.Run("sub", func(t *testing.T) {
		t.Setenv("SAMPLE", "1")
		checkLimit(t, ClampLimit(defaultLimit))
	})
}

func TestSerialHelperSetenv(t *testing.T) {
	useEnv(t)
	checkLimit(t, ClampLimit(defaultLimit))
}

//slopelint:serial SetMutableBanner writes the package-level banner
func TestSerialMarked(t *testing.T) {
	checkLimit(t, ClampLimit(defaultLimit))
}

//slopelint:serial
func TestSerialBare(t *testing.T) {
	checkLimit(t, ClampLimit(defaultLimit))
}

//slopelint:serial shared banner
func TestSerialStale(t *testing.T) {
	t.Parallel()
	checkLimit(t, ClampLimit(defaultLimit))
}

//slopelint:serial shared environment
func TestSerialStaleSetenv(t *testing.T) {
	t.Setenv("SAMPLE", "1")
	checkLimit(t, ClampLimit(defaultLimit))
}
`)

	issues := lintInDir(t, tmp)
	messages := make(map[string][]string)

	for _, issue := range issues {
		// The parallel cases live in serial_test.go; the other tests are all serial.
		if issue.Kind != "test_not_parallel" ||
			filepath.Base(issuePosition(issue).Filename) == "serial_test.go" {
			messages[issue.Kind] = append(messages[issue.Kind], issue.Message)
		}
	}

	// test_sleep must skip the production Pause helper.
	want := map[string][]string{
		"const_value_test": {
			`test "TestDefaultLimitConst" only checks fixed package values`,
			`test "TestDefaultLimitAssertion" only checks fixed package values`,
			`test "TestLiteralArithmetic" only checks fixed package values`,
			`test "TestDefaultLimitAccessor" only checks fixed package values`,
			`test "TestDefaultPolicyConst" only checks fixed package values`,
			`test "TestDefaultTitleContainsFixedValue" only checks fixed package values`,
			`test "TestDefaultBannerContainsFixedValue" only checks fixed package values`,
			`test "TestRuntimeLimit" only checks fixed package values`,
		},
		"test_no_assertion": {
			`test "TestClampWithoutFailure" has no failing path`,
			`test "TestClampOnlyLogs" has no failing path`,
		},
		"test_sleep": {"time.Sleep in test code", "time.Sleep in test code"},
		"test_not_parallel": {
			`test "TestParallelOnlySubtest" does not call t.Parallel()`,
			`test "TestSerialBare" has //slopelint:serial without a reason`,
			`test "TestSerialStale" calls t.Parallel(), so its //slopelint:serial marker is stale`,
			`test "TestSerialStaleSetenv" uses t.Setenv or t.Chdir`,
		},
	}

	for kind, fragments := range want {
		got := strings.Join(messages[kind], "\n")
		if len(messages[kind]) != len(fragments) {
			t.Fatalf(
				"%s findings = %d, want %d; got:\n%s",
				kind,
				len(messages[kind]),
				len(fragments),
				got,
			)
		}

		for _, fragment := range fragments {
			if !strings.Contains(got, fragment) {
				t.Fatalf("%s findings miss %q; got:\n%s", kind, fragment, got)
			}
		}
	}
}
