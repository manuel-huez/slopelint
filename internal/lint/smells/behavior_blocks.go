package smells

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/types"
	"slices"
	"strconv"
	"strings"
)

const (
	behaviorMinBlockStatements = 3
	behaviorMaxBlockStatements = 12
	behaviorMinBlockNodes      = 35
	behaviorMinStructuredNodes = 15
	behaviorMinAdjacentNodes   = 9
)

type behaviorStmtList struct {
	owner    string
	stmts    []ast.Stmt
	bindings map[types.Object]string
	typeKeys *behaviorTypeEncoder
}

func (l *Runner) blockBehaviorCandidates() []behaviorCandidate {
	candidates := make([]behaviorCandidate, 0)

	for _, fn := range l.pkg.ProductionFuncs {
		if fn.Body == nil || l.behaviorGenerated(fn) {
			continue
		}

		for _, list := range l.behaviorStmtLists(fn) {
			candidates = append(candidates, l.behaviorCandidatesForStmtList(list)...)
		}
	}

	return candidates
}

func (l *Runner) behaviorStmtLists(fn *ast.FuncDecl) []behaviorStmtList {
	if l.behaviorCloneSuppressed(fn) {
		return nil
	}

	owner := fn.Name.Name
	typeKeys := &behaviorTypeEncoder{
		params:   make(map[*types.TypeParam]string),
		visiting: make(map[types.Type]bool),
	}

	if obj, ok := l.pkg.TypesInfo.Defs[fn.Name].(*types.Func); ok {
		owner = behaviorFunctionName(obj)
		if signature, ok := obj.Type().(*types.Signature); ok {
			typeKeys = newBehaviorTypeEncoder(signature)
		}
	}

	bindings := l.behaviorFunctionBindings(fn)
	lists := []behaviorStmtList{{
		owner: owner, stmts: fn.Body.List, bindings: bindings, typeKeys: typeKeys,
	}}

	ast.Inspect(fn.Body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.FuncLit:
			return false
		case *ast.BlockStmt:
			if node != fn.Body {
				lists = append(
					lists,
					behaviorStmtList{
						owner: owner, stmts: node.List, bindings: bindings, typeKeys: typeKeys,
					},
				)
			}
		case *ast.CaseClause:
			lists = append(
				lists,
				behaviorStmtList{
					owner: owner, stmts: node.Body, bindings: bindings, typeKeys: typeKeys,
				},
			)
		case *ast.CommClause:
			lists = append(
				lists,
				behaviorStmtList{
					owner: owner, stmts: node.Body, bindings: bindings, typeKeys: typeKeys,
				},
			)
		}

		return true
	})

	return lists
}

func (l *Runner) behaviorFunctionBindings(fn *ast.FuncDecl) map[types.Object]string {
	bindings := make(map[types.Object]string)

	ast.Inspect(fn, func(node ast.Node) bool {
		if _, isFuncLit := node.(*ast.FuncLit); isFuncLit {
			return false
		}

		id, ok := node.(*ast.Ident)
		if !ok {
			return true
		}

		obj := l.pkg.TypesInfo.ObjectOf(id)
		if behaviorNormalizableObject(obj) {
			if _, exists := bindings[obj]; !exists {
				bindings[obj] = "slopelintF" + strconv.Itoa(len(bindings))
			}
		}

		return true
	})

	return bindings
}

func (l *Runner) behaviorCandidatesForStmtList(list behaviorStmtList) []behaviorCandidate {
	if len(list.stmts) == 0 {
		return nil
	}

	candidates := l.behaviorWindowCandidates(list)

	return append(candidates, l.behaviorStructuredCandidates(list)...)
}

func (l *Runner) behaviorWindowCandidates(list behaviorStmtList) []behaviorCandidate {
	candidates := make([]behaviorCandidate, 0)
	maxWindow := min(len(list.stmts), behaviorMaxBlockStatements)

	for window := behaviorMinBlockStatements; window <= maxWindow; window++ {
		for start := 0; start+window <= len(list.stmts); start++ {
			stmts := list.stmts[start : start+window]
			if l.behaviorCloneSuppressedStatements(list.stmts, start, start+window) {
				continue
			}

			if candidate, ok := l.blockBehaviorCandidate(
				list.owner,
				list.bindings,
				list.typeKeys,
				stmts,
			); ok {
				candidates = append(candidates, candidate)
			}
		}
	}

	return candidates
}

func (l *Runner) behaviorStructuredCandidates(list behaviorStmtList) []behaviorCandidate {
	candidates := make([]behaviorCandidate, 0)

	for idx, stmt := range list.stmts {
		minimumNodes := behaviorMinStructuredNodes
		if idx > 0 && behaviorStructuredStmt(list.stmts[idx-1]) ||
			idx+1 < len(list.stmts) && behaviorStructuredStmt(list.stmts[idx+1]) {
			minimumNodes = behaviorMinAdjacentNodes
		}

		if !behaviorStructuredStmt(stmt) || behaviorNodeCount(stmt) < minimumNodes {
			continue
		}

		if l.behaviorCloneSuppressedStatements(list.stmts, idx, idx+1) {
			continue
		}

		if candidate, ok := l.blockBehaviorCandidate(
			list.owner,
			list.bindings,
			list.typeKeys,
			[]ast.Stmt{stmt},
		); ok {
			candidates = append(candidates, candidate)
		}
	}

	return candidates
}

func (l *Runner) blockBehaviorCandidate(
	owner string,
	bindings map[types.Object]string,
	typeKeys *behaviorTypeEncoder,
	stmts []ast.Stmt,
) (behaviorCandidate, bool) {
	key, ok := l.normalizedBehaviorBlock(bindings, typeKeys, stmts)
	if !ok {
		return behaviorCandidate{}, false
	}

	weight := 0
	for _, stmt := range stmts {
		weight += behaviorNodeCount(stmt)
	}

	if len(stmts) > 1 && weight < behaviorMinBlockNodes && !behaviorValidationWindow(stmts) {
		return behaviorCandidate{}, false
	}

	return newBehaviorCandidate(
		l.pkg,
		"block|"+key,
		owner,
		stmts[0].Pos(),
		stmts[len(stmts)-1].End(),
		behaviorCandidateBlock,
		weight,
		l.blockBehaviorEffects(stmts),
	), true
}

func behaviorValidationWindow(stmts []ast.Stmt) bool {
	for _, stmt := range stmts {
		guard, ok := stmt.(*ast.IfStmt)
		if !ok || guard.Init != nil || guard.Else != nil || len(guard.Body.List) != 1 {
			return false
		}

		if _, ok := guard.Body.List[0].(*ast.ReturnStmt); !ok {
			return false
		}
	}

	return true
}

func (l *Runner) normalizedBehaviorBlock(
	functionBindings map[types.Object]string,
	typeKeys *behaviorTypeEncoder,
	stmts []ast.Stmt,
) (key string, ok bool) {
	block := &ast.BlockStmt{List: stmts}
	defined := l.behaviorBlockDefinitions(block)

	typesByPlaceholder, renamed, unsupported := l.renameBehaviorBlock(
		block,
		functionBindings,
		typeKeys,
		defined,
	)
	defer func() {
		for _, item := range renamed {
			item.ident.Name = item.original
		}
	}()

	if unsupported {
		return "", false
	}

	var rendered bytes.Buffer
	if err := format.Node(&rendered, l.pkg.FSet, block); err != nil {
		return "", false
	}

	return strings.Join(typesByPlaceholder, ",") + "|" + rendered.String(), true
}

func (l *Runner) behaviorBlockDefinitions(block *ast.BlockStmt) map[types.Object]struct{} {
	defined := make(map[types.Object]struct{})

	ast.Inspect(block, func(node ast.Node) bool {
		id, isIdent := node.(*ast.Ident)
		if !isIdent {
			return true
		}

		if obj := l.pkg.TypesInfo.Defs[id]; behaviorNormalizableObject(obj) {
			defined[obj] = struct{}{}
		}

		return true
	})

	return defined
}

type behaviorRenamedIdent struct {
	ident    *ast.Ident
	original string
}

func (l *Runner) renameBehaviorBlock(
	block *ast.BlockStmt,
	functionBindings map[types.Object]string,
	typeKeys *behaviorTypeEncoder,
	defined map[types.Object]struct{},
) ([]string, []behaviorRenamedIdent, bool) {
	objects := make(map[types.Object]string)
	nextDefined := 0
	typesByPlaceholder := make([]string, 0)
	renamed := make([]behaviorRenamedIdent, 0)
	unsupported := false

	ast.Inspect(block, func(node ast.Node) bool {
		if call, isCall := node.(*ast.CallExpr); isCall &&
			!l.behaviorBlockCallSupported(call, defined) {
			unsupported = true
			return false
		}

		id, isIdent := node.(*ast.Ident)
		if !isIdent || id.Name == "_" {
			return true
		}

		obj := l.pkg.TypesInfo.ObjectOf(id)
		if obj == nil {
			return true
		}

		placeholder, exists := objects[obj]
		if !exists {
			var objectKey string

			placeholder, objectKey = l.behaviorIdentifierPlaceholder(
				obj,
				functionBindings,
				typeKeys,
				defined,
				len(objects),
				&nextDefined,
			)
			if placeholder == "" {
				return true
			}

			objects[obj] = placeholder

			typesByPlaceholder = append(typesByPlaceholder, objectKey)
		}

		renamed = append(renamed, behaviorRenamedIdent{ident: id, original: id.Name})
		id.Name = placeholder

		return true
	})

	return typesByPlaceholder, renamed, unsupported
}

func (l *Runner) behaviorIdentifierPlaceholder(
	obj types.Object,
	functionBindings map[types.Object]string,
	typeKeys *behaviorTypeEncoder,
	defined map[types.Object]struct{},
	objectCount int,
	nextDefined *int,
) (string, string) {
	if !behaviorNormalizableObject(obj) {
		objectKey, ok := l.behaviorCanonicalObjectKey(obj, typeKeys)
		if !ok {
			return "", ""
		}

		return "slopelintO" + strconv.Itoa(objectCount), objectKey
	}

	if _, isDefined := defined[obj]; isDefined {
		placeholder := "slopelintD" + strconv.Itoa(*nextDefined)
		*nextDefined++

		return placeholder, typeKeys.typeKey(obj.Type())
	}

	return functionBindings[obj], typeKeys.typeKey(obj.Type())
}

func (l *Runner) behaviorBlockCallSupported(
	call *ast.CallExpr,
	defined map[types.Object]struct{},
) bool {
	if l.pkg.TypesInfo.Types[call.Fun].IsType() {
		return true
	}

	if id, ok := ast.Unparen(call.Fun).(*ast.Ident); ok {
		if _, isBuiltin := l.pkg.TypesInfo.ObjectOf(id).(*types.Builtin); isBuiltin {
			return true
		}

		if _, isDefined := defined[l.pkg.TypesInfo.ObjectOf(id)]; isDefined {
			return true
		}
	}

	_, _, ok := l.calledFunc(call)

	return ok
}

func behaviorNormalizableObject(obj types.Object) bool {
	if _, ok := obj.(*types.Label); ok {
		return true
	}

	variable, ok := obj.(*types.Var)
	if !ok || variable.IsField() {
		return false
	}

	pkg := obj.Pkg()

	return pkg == nil || obj.Parent() != pkg.Scope()
}

func (l *Runner) behaviorCanonicalObjectKey(
	obj types.Object,
	typeKeys *behaviorTypeEncoder,
) (string, bool) {
	switch obj := obj.(type) {
	case *types.Builtin:
		return "builtin:" + obj.Name(), true
	case *types.Const:
		return "const:" + obj.Val().ExactString() + ":" + typeKeys.typeKey(obj.Type()), true
	case *types.PkgName:
		return "package:" + obj.Imported().Path(), true
	case *types.Func:
		if summary, ok := l.behaviorCalls[funcObjectKey(obj)]; ok {
			return "function-behavior:" + summary.digest, true
		}

		return "function:" + typeKeys.objectKey(obj), true
	case *types.TypeName:
		return "type:" + typeKeys.typeKey(obj.Type()), true
	case *types.Var:
		return "variable:" + typeKeys.objectKey(obj), true
	default:
		return "", false
	}
}

func behaviorStructuredStmt(stmt ast.Stmt) bool {
	switch stmt.(type) {
	case *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
		return true
	default:
		return false
	}
}

func behaviorNodeCount(node ast.Node) int {
	count := 0

	ast.Inspect(node, func(node ast.Node) bool {
		if _, isFuncLit := node.(*ast.FuncLit); isFuncLit {
			return false
		}

		if node != nil {
			count++
		}

		return true
	})

	return count
}

func (l *Runner) blockBehaviorEffects(stmts []ast.Stmt) behaviorEffects {
	var effects behaviorEffects

	for _, stmt := range stmts {
		ast.Inspect(stmt, func(node ast.Node) bool {
			nodeEffects, descend := l.behaviorEffectsForNode(node)
			effects |= nodeEffects

			return descend
		})
	}

	return effects
}

func (l *Runner) behaviorEffectsForNode(node ast.Node) (behaviorEffects, bool) {
	switch node := node.(type) {
	case *ast.FuncLit:
		return 0, false
	case *ast.AssignStmt:
		if node.Tok.String() != ":=" && l.assignmentHasBehaviorWrite(node.Lhs) {
			return behaviorEffectWrite, true
		}
	case *ast.IncDecStmt:
		if l.exprIsBehaviorWrite(node.X) {
			return behaviorEffectWrite, true
		}
	case *ast.CallExpr:
		return l.callBehaviorEffects(node), true
	default:
		return behaviorNonMutationEffects(node), true
	}

	return 0, true
}

func behaviorNonMutationEffects(node ast.Node) behaviorEffects {
	switch node := node.(type) {
	case *ast.CompositeLit:
		return behaviorEffectAllocate
	case *ast.GoStmt, *ast.SendStmt, *ast.SelectStmt:
		return behaviorEffectConcurrent
	case *ast.DeferStmt:
		return behaviorEffectDefer
	case *ast.UnaryExpr:
		if node.Op.String() == "<-" {
			return behaviorEffectConcurrent
		}
	}

	return 0
}

func (l *Runner) assignmentHasBehaviorWrite(exprs []ast.Expr) bool {
	return slices.ContainsFunc(exprs, l.exprIsBehaviorWrite)
}

func (l *Runner) exprIsBehaviorWrite(expr ast.Expr) bool {
	id, ok := ast.Unparen(expr).(*ast.Ident)
	if !ok {
		return true
	}

	obj := l.pkg.TypesInfo.ObjectOf(id)

	return !behaviorNormalizableObject(obj)
}

func (l *Runner) callBehaviorEffects(call *ast.CallExpr) behaviorEffects {
	if l.pkg.TypesInfo.Types[call.Fun].IsType() {
		return 0
	}

	if id, ok := ast.Unparen(call.Fun).(*ast.Ident); ok {
		if builtin, ok := l.pkg.TypesInfo.ObjectOf(id).(*types.Builtin); ok {
			return behaviorBuiltinEffects(builtin.Name())
		}
	}

	if fn, _, ok := l.calledFunc(call); ok {
		if summary, exists := l.behaviorCalls[funcObjectKey(fn)]; exists {
			return behaviorEffectCall | summary.effects
		}
	}

	return behaviorEffectCall
}
