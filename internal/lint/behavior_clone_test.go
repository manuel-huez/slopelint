package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBehaviorCloneNormalizesEquivalentFunctionTransformations(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func expanded(input int) int {
	next := input
	next++
	if next > 10 {
		return next * 2
	}
	return next
}

func compact(value int) int {
	next := value + 1
	if next > 10 {
		return next * 2
	}
	return next
}

func changed(value int) int {
	next := value + 2
	if next > 10 {
		return next * 2
	}
	return next
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(joined, `function "compact" has same supported behavior as "expanded"`) {
		t.Fatalf("expected normalized function clone, got:\n%s", joined)
	}

	if strings.Contains(joined, `function "changed" has same supported behavior`) {
		t.Fatalf("unexpected clone after constant changed, got:\n%s", joined)
	}
}

func TestBehaviorCloneFindsSharedBlockWhenFunctionsDiffer(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import "errors"

var errBad = errors.New("bad")

type request struct {
	name string
	kind string
	size int
}

func create(value request) error {
	if value.name == "" {
		return errBad
	}
	if value.kind == "" {
		return errBad
	}
	if value.size == 0 {
		return errBad
	}
	println("create")
	return nil
}

func update(req request) error {
	if req.name == "" {
		return errBad
	}
	if req.kind == "" {
		return errBad
	}
	if req.size == 0 {
		return errBad
	}
	println("update")
	return errBad
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(
		joined,
		`behavior block in "update" duplicates block in "create" at sample.go:`,
	) || !strings.Contains(joined, `(effects: none); extract shared behavior`) {
		t.Fatalf("expected shared validation block, got:\n%s", joined)
	}

	if strings.Contains(joined, `function "update" has same supported behavior`) {
		t.Fatalf("unexpected whole-function clone, got:\n%s", joined)
	}
}

func TestBehaviorCloneDoesNotTreatOrdinaryCommentsAsSuppressions(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

var errBad error

type request struct { name, kind string; size int }

func create(value request) error {
	if value.name == "" { return errBad }
	if value.kind == "" { return errBad }
	if value.size == 0 { return errBad }
	println("create")
	return nil
}

func update(value request) error {
	// Keep validation local because update accepts partially migrated records.
	if value.name == "" { return errBad }
	if value.kind == "" { return errBad }
	if value.size == 0 { return errBad }
	println("update")
	return errBad
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(joined, `behavior block in "update" duplicates block in "create"`) {
		t.Fatalf("ordinary comment suppressed behavior clone, got:\n%s", joined)
	}
}

func TestBehaviorCloneDistinguishesClosureBehavior(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func add(values []int) int {
	total := 0
	apply := func(value int) { total += value }
	for _, value := range values {
		apply(value)
	}
	return total
}

func subtract(values []int) int {
	total := 0
	apply := func(value int) { total -= value }
	for _, value := range values {
		apply(value)
	}
	return total
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if strings.Contains(joined, `function "subtract" has same supported behavior`) ||
		strings.Contains(joined, `behavior block in "subtract" duplicates block in "add"`) {
		t.Fatalf("unexpected clone for different closure behavior, got:\n%s", joined)
	}
}

func TestBehaviorCloneIncludesWritesAndConcurrency(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type counter struct { value int }

func consume(ch chan int) { ch <- 1 }

func first(c *counter, ch chan int) int {
	go consume(ch)
	c.value += <-ch
	if c.value > 10 {
		return c.value
	}
	return 0
}

func second(target *counter, values chan int) int {
	go consume(values)
	target.value += <-values
	if target.value > 10 {
		return target.value
	}
	return 0
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(joined, `function "second" has same supported behavior as "first"`) {
		t.Fatalf("expected concurrent function clone, got:\n%s", joined)
	}

	if !strings.Contains(joined, "writes") || !strings.Contains(joined, "concurrent") {
		t.Fatalf("expected write and concurrency effects, got:\n%s", joined)
	}
}

func TestBehaviorCloneDistinguishesWrittenFields(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type counter struct { left, right int }

func updateLeft(c *counter, value int) int {
	c.left += value
	if c.left > 10 { return c.left }
	return 0
}

func updateRight(c *counter, value int) int {
	c.right += value
	if c.right > 10 { return c.right }
	return 0
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if strings.Contains(joined, `function "updateRight" has same supported behavior`) {
		t.Fatalf("unexpected clone for different field writes, got:\n%s", joined)
	}
}

func TestBehaviorCloneKeepsReceiverIdentity(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

type counter struct { value int }
type otherCounter struct { value int }

func (c *counter) first(delta int) int {
	c.value += delta
	if c.value > 10 { return c.value }
	return 0
}

func (target *counter) second(delta int) int {
	target.value += delta
	if target.value > 10 { return target.value }
	return 0
}

func (target *otherCounter) third(delta int) int {
	target.value += delta
	if target.value > 10 { return target.value }
	return 0
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(
		joined,
		`function "(*counter).second" has same supported behavior as "(*counter).first"`,
	) {
		t.Fatalf("expected same-receiver method clone, got:\n%s", joined)
	}

	if strings.Contains(joined, `function "(*otherCounter).third" has same supported behavior`) {
		t.Fatalf("unexpected clone across receiver types, got:\n%s", joined)
	}
}

func TestBehaviorCloneMatchesFunctionsAcrossPackages(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "first", "first.go"), `package first

func Expanded(input int) int {
	next := input + 1
	if next > 10 { return next * 2 }
	return next
}
`)
	writeFile(t, filepath.Join(tmp, "second", "second.go"), `package second

func Compact(value int) int {
	next := value + 1
	if next > 10 { return next * 2 }
	return next
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(
		joined,
		`function "Compact" has same supported behavior as "Expanded" in package "example.com/sample/first" at first.go:3`,
	) {
		t.Fatalf("expected cross-package function clone, got:\n%s", joined)
	}
}

func TestBehaviorCloneMatchesBlocksAcrossImportAliases(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "first", "first.go"), `package first

import text "strings"

func Create(name, kind, suffix, prefix, scope string) string {
	name = text.TrimSpace(name)
	kind = text.TrimSpace(kind)
	suffix = text.TrimSpace(suffix)
	prefix = text.TrimSpace(prefix)
	scope = text.TrimSpace(scope)
	println("create")
	return name + kind + suffix + prefix + scope
}
`)
	writeFile(t, filepath.Join(tmp, "second", "second.go"), `package second

import stringsAlias "strings"

func Update(value, category, ending, beginning, namespace string) string {
	value = stringsAlias.TrimSpace(value)
	category = stringsAlias.TrimSpace(category)
	ending = stringsAlias.TrimSpace(ending)
	beginning = stringsAlias.TrimSpace(beginning)
	namespace = stringsAlias.TrimSpace(namespace)
	println("update")
	return value + category + ending + beginning + namespace
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(
		joined,
		`behavior block in "Update" duplicates block in "Create" in package "example.com/sample/first" at first.go:6`,
	) {
		t.Fatalf("expected cross-package block clone across aliases, got:\n%s", joined)
	}
}

func TestBehaviorClonePropagatesThroughDeepLocalCalls(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func Left(value int) int {
	next := left1(value)
	if next > 10 { return next * 2 }
	return next
}
func left1(value int) int { return left2(value) + 1 }
func left2(value int) int { return left3(value) + 1 }
func left3(value int) int { return left4(value) + 1 }
func left4(value int) int { return left5(value) + 1 }
func left5(value int) int { return value + 1 }

func Right(value int) int {
	next := right1(value)
	if next > 10 { return next * 2 }
	return next
}
func right1(value int) int { return right2(value) + 1 }
func right2(value int) int { return right3(value) + 1 }
func right3(value int) int { return right4(value) + 1 }
func right4(value int) int { return right5(value) + 1 }
func right5(value int) int { return value + 1 }
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(joined, `function "Right" has same supported behavior as "Left"`) {
		t.Fatalf("expected clone through more than four local calls, got:\n%s", joined)
	}
}

func TestBehaviorCloneHandlesRecursiveFunctions(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func Left(value int) int {
	if value <= 0 { return 0 }
	return value + Left(value-1)
}

func Right(value int) int {
	if value <= 0 { return 0 }
	return value + Right(value-1)
}

func Changed(value int) int {
	if value <= 0 { return 1 }
	return value + Changed(value-1)
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(joined, `function "Right" has same supported behavior as "Left"`) {
		t.Fatalf("expected recursive function clone, got:\n%s", joined)
	}

	if strings.Contains(joined, `function "Changed" has same supported behavior`) {
		t.Fatalf("unexpected recursive clone with changed base case, got:\n%s", joined)
	}
}

func TestBehaviorCloneMatchesStaticLocalClosures(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func First(values []int) int {
	total := 0
	apply := func(value int) { total += value }
	for _, value := range values { apply(value) }
	if total > 10 { return total * 2 }
	return total
}

func Second(items []int) int {
	sum := 0
	add := func(item int) { sum += item }
	for _, item := range items { add(item) }
	if sum > 10 { return sum * 2 }
	return sum
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(joined, `function "Second" has same supported behavior as "First"`) {
		t.Fatalf("expected static closure clone, got:\n%s", joined)
	}
}

func TestBehaviorCloneMatchesDynamicFunctionParameters(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func First(apply func(int) int, value int) int {
	result := apply(value)
	if result > 10 { return result * 2 }
	return result
}

func Second(transform func(int) int, input int) int {
	result := transform(input)
	if result > 10 { return result * 2 }
	return result
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(joined, `function "Second" has same supported behavior as "First"`) {
		t.Fatalf("expected dynamic function-parameter clone, got:\n%s", joined)
	}
}

func TestBehaviorCloneMatchesReflectionAndUnsafeOperations(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

import (
	"reflect"
	"unsafe"
)

func First(value *int) uintptr {
	reflected := reflect.ValueOf(value)
	if reflected.IsNil() { return 0 }
	return uintptr(unsafe.Pointer(value))
}

func Second(input *int) uintptr {
	reflected := reflect.ValueOf(input)
	if reflected.IsNil() { return 0 }
	return uintptr(unsafe.Pointer(input))
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(joined, `function "Second" has same supported behavior as "First"`) {
		t.Fatalf("expected reflection and unsafe operation clone, got:\n%s", joined)
	}
}

func TestBehaviorCloneKeepsNamedTypeIdentityAcrossPackages(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "first", "first.go"), `package first

type Record struct { Value int }

func Transform(input Record) int {
	if input.Value > 10 { return input.Value * 2 }
	return input.Value
}
`)
	writeFile(t, filepath.Join(tmp, "second", "second.go"), `package second

type Record struct { Value int }

func Transform(input Record) int {
	if input.Value > 10 { return input.Value * 2 }
	return input.Value
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if strings.Contains(joined, `in package "example.com/sample/first"`) {
		t.Fatalf("unexpected clone across distinct named types, got:\n%s", joined)
	}
}

func TestBehaviorCloneKeepsGlobalIdentityAcrossPackages(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "first", "shared.go"), `package shared

var State int

func Read(value int) int {
	next := State + value
	if next > 10 { return next * 2 }
	return next
}
`)
	writeFile(t, filepath.Join(tmp, "second", "shared.go"), `package shared

var State int

func Read(value int) int {
	next := State + value
	if next > 10 { return next * 2 }
	return next
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if strings.Contains(joined, `in package "example.com/sample/first"`) {
		t.Fatalf("unexpected clone across distinct package globals, got:\n%s", joined)
	}
}

func TestBehaviorCloneHandlesMutualRecursion(t *testing.T) {
	tmp := newTestModule(t)
	writeFile(t, filepath.Join(tmp, "sample.go"), `package sample

func LeftEven(value int) bool {
	if value == 0 { return true }
	return LeftOdd(value - 1)
}
func LeftOdd(value int) bool {
	if value == 0 { return false }
	return LeftEven(value - 1)
}

func RightEven(value int) bool {
	if value == 0 { return true }
	return RightOdd(value - 1)
}
func RightOdd(value int) bool {
	if value == 0 { return false }
	return RightEven(value - 1)
}
`)

	joined := joinMessages(lintInDir(t, tmp))
	if !strings.Contains(joined, `function "RightEven" has same supported behavior as "LeftEven"`) {
		t.Fatalf("expected mutual-recursion clone, got:\n%s", joined)
	}
}
