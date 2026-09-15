package smells

import (
	"crypto/sha256"
	"fmt"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
)

const (
	behaviorCloneKind     = "behavior_clone"
	behaviorEffectCount   = 7
	behaviorMatchCapacity = 1024
)

type behaviorCandidateKind uint8

const (
	behaviorCandidateFunction behaviorCandidateKind = iota + 1
	behaviorCandidateBlock
)

type behaviorEffects uint16

const (
	behaviorEffectRead behaviorEffects = 1 << iota
	behaviorEffectWrite
	behaviorEffectCall
	behaviorEffectAllocate
	behaviorEffectPanic
	behaviorEffectDefer
	behaviorEffectConcurrent
)

type behaviorCandidate struct {
	pkg     *Package
	key     string
	digest  [sha256.Size]byte
	name    string
	pos     token.Pos
	end     token.Pos
	kind    behaviorCandidateKind
	weight  int
	effects behaviorEffects
}

type behaviorMatch struct {
	source *behaviorCandidate
	target *behaviorCandidate
}

type behaviorSpan struct {
	start token.Pos
	end   token.Pos
}

type behaviorSources struct {
	first         int
	secondPackage int
	firstPackage  *Package
	byPackage     map[*Package]behaviorPackageSources
}

type behaviorPackageSources struct {
	minimumEnd   int
	maximumStart int
}

func (l *Runner) checkBehaviorClones(comparableSignatures map[string]struct{}) {
	analysis := buildBehaviorSSAAnalysis([]*Package{l.pkg})
	l.behaviorCalls = analysis.objectSummary

	if comparableSignatures == nil {
		comparableSignatures = behaviorComparableSignatures(analysis.candidates[l.pkg])
	}

	candidates := append(
		l.functionBehaviorCandidatesFrom(
			analysis.candidates[l.pkg],
			comparableSignatures,
			analysis,
		),
		l.blockBehaviorCandidates()...,
	)
	matches := exactBehaviorMatches(candidates)
	covered := make([]behaviorSpan, 0, len(matches))

	for _, match := range matches {
		if behaviorSpanCovered(match.target, covered) {
			continue
		}

		l.report(
			match.target.pos,
			behaviorCloneKind,
			l.behaviorCloneMessage(match),
		)
		covered = append(covered, behaviorSpan{start: match.target.pos, end: match.target.end})
	}
}

// RunBehaviorRepo reports behavior clones across every supplied package.
func RunBehaviorRepo(pkgs []*Package) map[*Package][]Finding {
	pkgs = append([]*Package(nil), pkgs...)
	sort.Slice(pkgs, func(i, j int) bool {
		return pkgs[i].TypesPkg.Path() < pkgs[j].TypesPkg.Path()
	})

	analysis := buildBehaviorSSAAnalysis(pkgs)
	allCandidateFunctions := make([]behaviorSSAFunction, 0)

	for _, pkg := range pkgs {
		allCandidateFunctions = append(allCandidateFunctions, analysis.candidates[pkg]...)
	}

	comparableSignatures := behaviorComparableSignatures(allCandidateFunctions)
	candidates := make([]behaviorCandidate, 0)

	for _, pkg := range pkgs {
		r := newRunner(pkg)
		r.behaviorCalls = analysis.objectSummary
		candidates = append(candidates, r.functionBehaviorCandidatesFrom(
			analysis.candidates[pkg],
			comparableSignatures,
			analysis,
		)...)
		candidates = append(candidates, r.blockBehaviorCandidates()...)
	}

	matches := exactBehaviorMatches(candidates)
	covered := make(map[*Package][]behaviorSpan)
	runners := make(map[*Package]*Runner)

	for _, match := range matches {
		pkg := match.target.pkg
		if behaviorSpanCovered(match.target, covered[pkg]) {
			continue
		}

		r := runners[pkg]
		if r == nil {
			r = newRunner(pkg)
			runners[pkg] = r
		}

		r.report(match.target.pos, behaviorCloneKind, r.behaviorCloneMessage(match))
		covered[pkg] = append(
			covered[pkg],
			behaviorSpan{start: match.target.pos, end: match.target.end},
		)
	}

	findings := make(map[*Package][]Finding, len(runners))
	for pkg, r := range runners {
		findings[pkg] = r.findings
	}

	return findings
}

func newBehaviorCandidate(
	pkg *Package,
	key string,
	name string,
	pos token.Pos,
	end token.Pos,
	kind behaviorCandidateKind,
	weight int,
	effects behaviorEffects,
) behaviorCandidate {
	return behaviorCandidate{
		pkg:     pkg,
		key:     key,
		digest:  sha256.Sum256([]byte(key)),
		name:    name,
		pos:     pos,
		end:     end,
		kind:    kind,
		weight:  weight,
		effects: effects,
	}
}

func exactBehaviorMatches(candidates []behaviorCandidate) []behaviorMatch {
	firstByDigest := make(map[[sha256.Size]byte]int, len(candidates))
	sourcesByDigest := make(map[[sha256.Size]byte]*behaviorSources)
	collisionsByDigest := make(map[[sha256.Size]byte]map[string]*behaviorSources)
	matches := make([]behaviorMatch, 0, min(len(candidates), behaviorMatchCapacity))

	for targetIndex, target := range candidates {
		firstIndex, exists := firstByDigest[target.digest]
		if !exists {
			firstByDigest[target.digest] = targetIndex
			continue
		}

		sources := behaviorSourcesForCandidate(
			candidates,
			firstIndex,
			target,
			sourcesByDigest,
			collisionsByDigest,
		)

		selectedSource := sources.matchingSource(candidates, target)
		if selectedSource >= 0 {
			matches = append(matches, behaviorMatch{
				source: &candidates[selectedSource],
				target: &candidates[targetIndex],
			})
		}

		sources.add(candidates, targetIndex)
	}

	sort.Slice(matches, func(i, j int) bool {
		leftFunctions := matches[i].source.kind == behaviorCandidateFunction &&
			matches[i].target.kind == behaviorCandidateFunction

		rightFunctions := matches[j].source.kind == behaviorCandidateFunction &&
			matches[j].target.kind == behaviorCandidateFunction
		if leftFunctions != rightFunctions {
			return leftFunctions
		}

		if matches[i].target.weight != matches[j].target.weight {
			return matches[i].target.weight > matches[j].target.weight
		}

		return matches[i].target.pos < matches[j].target.pos
	})

	return matches
}

func behaviorSourcesForCandidate(
	candidates []behaviorCandidate,
	firstIndex int,
	target behaviorCandidate,
	sourcesByDigest map[[sha256.Size]byte]*behaviorSources,
	collisionsByDigest map[[sha256.Size]byte]map[string]*behaviorSources,
) *behaviorSources {
	if collisions := collisionsByDigest[target.digest]; collisions != nil {
		if sources := collisions[target.key]; sources != nil {
			return sources
		}

		sources := newBehaviorSources()
		collisions[target.key] = sources

		return sources
	}

	first := candidates[firstIndex]
	if first.key == target.key {
		sources := sourcesByDigest[target.digest]
		if sources == nil {
			sources = newBehaviorSources()
			sources.add(candidates, firstIndex)
			sourcesByDigest[target.digest] = sources
		}

		return sources
	}

	collisions := map[string]*behaviorSources{
		first.key: sourcesByDigest[target.digest],
	}
	if collisions[first.key] == nil {
		collisions[first.key] = newBehaviorSources()
		collisions[first.key].add(candidates, firstIndex)
	}

	sources := newBehaviorSources()
	collisions[target.key] = sources
	collisionsByDigest[target.digest] = collisions
	delete(sourcesByDigest, target.digest)

	return sources
}

func newBehaviorSources() *behaviorSources {
	return &behaviorSources{
		first:         -1,
		secondPackage: -1,
		byPackage:     make(map[*Package]behaviorPackageSources),
	}
}

func (s *behaviorSources) matchingSource(
	candidates []behaviorCandidate,
	target behaviorCandidate,
) int {
	if local, ok := s.byPackage[target.pkg]; ok {
		if local.minimumEnd >= 0 && candidates[local.minimumEnd].end < target.pos {
			return local.minimumEnd
		}

		if local.maximumStart >= 0 && candidates[local.maximumStart].pos > target.end {
			return local.maximumStart
		}
	}

	if s.first >= 0 && s.firstPackage != target.pkg {
		return s.first
	}

	if s.secondPackage >= 0 {
		return s.secondPackage
	}

	return -1
}

func (s *behaviorSources) add(candidates []behaviorCandidate, index int) {
	candidate := candidates[index]
	if s.first < 0 {
		s.first = index
		s.firstPackage = candidate.pkg
	} else if s.secondPackage < 0 && candidate.pkg != s.firstPackage {
		s.secondPackage = index
	}

	local, ok := s.byPackage[candidate.pkg]
	if !ok {
		s.byPackage[candidate.pkg] = behaviorPackageSources{
			minimumEnd:   index,
			maximumStart: index,
		}

		return
	}

	if candidate.end < candidates[local.minimumEnd].end {
		local.minimumEnd = index
	}

	if candidate.pos > candidates[local.maximumStart].pos {
		local.maximumStart = index
	}

	s.byPackage[candidate.pkg] = local
}

func behaviorSpanCovered(candidate *behaviorCandidate, covered []behaviorSpan) bool {
	for _, span := range covered {
		if candidate.pos >= span.start && candidate.end <= span.end {
			return true
		}
	}

	return false
}

func (l *Runner) behaviorCloneMessage(match behaviorMatch) string {
	effects := match.target.effects.String()
	if match.target.kind == behaviorCandidateFunction &&
		match.source.kind == behaviorCandidateFunction {
		if match.source.pkg != match.target.pkg {
			return fmt.Sprintf(
				"function %q has same supported behavior as %q in package %q at %s (effects: %s); merge shared implementation",
				match.target.name,
				match.source.name,
				match.source.pkg.TypesPkg.Path(),
				behaviorPositionText(match.source),
				effects,
			)
		}

		return fmt.Sprintf(
			"function %q has same supported behavior as %q (effects: %s); merge shared implementation",
			match.target.name,
			match.source.name,
			effects,
		)
	}

	if match.source.pkg != match.target.pkg {
		return fmt.Sprintf(
			"behavior block in %q duplicates block in %q in package %q at %s (effects: %s); extract shared behavior",
			match.target.name,
			match.source.name,
			match.source.pkg.TypesPkg.Path(),
			behaviorPositionText(match.source),
			effects,
		)
	}

	return fmt.Sprintf(
		"behavior block in %q duplicates block in %q at %s (effects: %s); extract shared behavior",
		match.target.name,
		match.source.name,
		behaviorPositionText(match.source),
		effects,
	)
}

func behaviorPositionText(candidate *behaviorCandidate) string {
	position := candidate.pkg.FSet.Position(candidate.pos)
	if !position.IsValid() {
		return unknownPos
	}

	return fmt.Sprintf("%s:%d", filepath.Base(position.Filename), position.Line)
}

func (effects behaviorEffects) String() string {
	labels := make([]string, 0, behaviorEffectCount)

	for _, item := range []struct {
		flag  behaviorEffects
		label string
	}{
		{flag: behaviorEffectRead, label: "reads"},
		{flag: behaviorEffectWrite, label: "writes"},
		{flag: behaviorEffectCall, label: "calls"},
		{flag: behaviorEffectAllocate, label: "allocates"},
		{flag: behaviorEffectPanic, label: "may panic"},
		{flag: behaviorEffectDefer, label: "defers"},
		{flag: behaviorEffectConcurrent, label: "concurrent"},
	} {
		if effects&item.flag != 0 {
			labels = append(labels, item.label)
		}
	}

	if len(labels) == 0 {
		return "none"
	}

	return strings.Join(labels, ", ")
}
