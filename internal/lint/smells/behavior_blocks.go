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

// behaviorPackage extracts behavior clone candidates from one package. calls
// maps function keys to SSA behavior summaries, so blocks that call
// equivalent functions compare equal.
type behaviorPackage struct {
	pkg   *Package
	calls map[string]behaviorSummary
}

type behaviorStmtList struct {
	owner    string
	stmts    []ast.Stmt
	bindings map[types.Object]string
	typeKeys *behaviorTypeEncoder
}

func (b behaviorPackage) blockBehaviorCandidates() []behaviorCandidate {
	candidates := make([]behaviorCandidate, 0)

	for _, fn := range b.pkg.ProductionFuncs {
		if fn.Body == nil || b.behaviorGenerated(fn) {
			continue
		}

		for _, list := range b.behaviorStmtLists(fn) {
			if len(list.stmts) == 0 {
				continue
			}

			candidates = append(candidates, b.behaviorWindowCandidates(list)...)
			candidates = append(candidates, b.behaviorStructuredCandidates(list)...)
		}
	}

	return candidates
}

func (b behaviorPackage) behaviorStmtLists(fn *ast.FuncDecl) []behaviorStmtList {
	owner := fn.Name.Name
	typeKeys := &behaviorTypeEncoder{
		params:   make(map[*types.TypeParam]string),
		visiting: make(map[types.Type]bool),
	}

	if obj, ok := b.pkg.TypesInfo.Defs[fn.Name].(*types.Func); ok {
		owner = behaviorFunctionName(obj)
		if signature, ok := obj.Type().(*types.Signature); ok {
			typeKeys = newBehaviorTypeEncoder(signature)
		}
	}

	bindings := behaviorFunctionBindings(b.pkg.TypesInfo, fn)
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

func behaviorFunctionBindings(info *types.Info, fn *ast.FuncDecl) map[types.Object]string {
	bindings := make(map[types.Object]string)

	ast.Inspect(fn, func(node ast.Node) bool {
		if _, isFuncLit := node.(*ast.FuncLit); isFuncLit {
			return false
		}

		id, ok := node.(*ast.Ident)
		if !ok {
			return true
		}

		obj := info.ObjectOf(id)
		if behaviorNormalizableObject(obj) {
			if _, exists := bindings[obj]; !exists {
				bindings[obj] = "slopelintF" + strconv.Itoa(len(bindings))
			}
		}

		return true
	})

	return bindings
}

func (b behaviorPackage) behaviorWindowCandidates(list behaviorStmtList) []behaviorCandidate {
	candidates := make([]behaviorCandidate, 0)
	maxWindow := min(len(list.stmts), behaviorMaxBlockStatements)

	for window := behaviorMinBlockStatements; window <= maxWindow; window++ {
		for start := 0; start+window <= len(list.stmts); start++ {
			stmts := list.stmts[start : start+window]
			if candidate, ok := b.blockBehaviorCandidate(
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

func (b behaviorPackage) behaviorStructuredCandidates(list behaviorStmtList) []behaviorCandidate {
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

		if candidate, ok := b.blockBehaviorCandidate(
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

func (b behaviorPackage) blockBehaviorCandidate(
	owner string,
	bindings map[types.Object]string,
	typeKeys *behaviorTypeEncoder,
	stmts []ast.Stmt,
) (behaviorCandidate, bool) {
	// Check size before normalization: renaming and formatting every window
	// dominates repo runs, and most short windows fail this cheap check.
	weight := 0
	for _, stmt := range stmts {
		weight += behaviorNodeCount(stmt)
	}

	if len(stmts) > 1 && weight < behaviorMinBlockNodes && !behaviorValidationWindow(stmts) {
		return behaviorCandidate{}, false
	}

	key, ok := b.normalizedBehaviorBlock(bindings, typeKeys, stmts)
	if !ok {
		return behaviorCandidate{}, false
	}

	return newBehaviorCandidate(behaviorCandidate{
		pkg:     b.pkg,
		key:     "block|" + key,
		name:    owner,
		pos:     stmts[0].Pos(),
		end:     stmts[len(stmts)-1].End(),
		kind:    behaviorCandidateBlock,
		weight:  weight,
		effects: b.blockBehaviorEffects(stmts),
	}), true
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

func (b behaviorPackage) normalizedBehaviorBlock(
	functionBindings map[types.Object]string,
	typeKeys *behaviorTypeEncoder,
	stmts []ast.Stmt,
) (key string, ok bool) {
	block := &ast.BlockStmt{List: stmts}
	defined := behaviorBlockDefinitions(b.pkg.TypesInfo, block)

	renamer := &behaviorBlockRenamer{
		owner:            b,
		functionBindings: functionBindings,
		typeKeys:         typeKeys,
		defined:          defined,
		objects:          make(map[types.Object]string),
	}

	ast.Inspect(block, renamer.visit)

	defer func() {
		for _, item := range renamer.renamed {
			item.ident.Name = item.original
		}
	}()

	if renamer.unsupported {
		return "", false
	}

	var rendered bytes.Buffer
	if err := format.Node(&rendered, b.pkg.FSet, block); err != nil {
		return "", false
	}

	return strings.Join(renamer.typesByPlaceholder, ",") + "|" + rendered.String(), true
}

func behaviorBlockDefinitions(info *types.Info, block *ast.BlockStmt) map[types.Object]struct{} {
	defined := make(map[types.Object]struct{})

	ast.Inspect(block, func(node ast.Node) bool {
		id, isIdent := node.(*ast.Ident)
		if !isIdent {
			return true
		}

		if obj := info.Defs[id]; behaviorNormalizableObject(obj) {
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

// behaviorBlockRenamer replaces block identifiers with stable placeholders in
// place; callers restore renamed after rendering.
type behaviorBlockRenamer struct {
	owner              behaviorPackage
	functionBindings   map[types.Object]string
	typeKeys           *behaviorTypeEncoder
	defined            map[types.Object]struct{}
	objects            map[types.Object]string
	nextDefined        int
	typesByPlaceholder []string
	renamed            []behaviorRenamedIdent
	unsupported        bool
}

func (r *behaviorBlockRenamer) visit(node ast.Node) bool {
	if call, isCall := node.(*ast.CallExpr); isCall &&
		!behaviorBlockCallSupported(r.owner.pkg.TypesInfo, call, r.defined) {
		r.unsupported = true
		return false
	}

	id, isIdent := node.(*ast.Ident)
	if !isIdent || id.Name == "_" {
		return true
	}

	obj := r.owner.pkg.TypesInfo.ObjectOf(id)
	if obj == nil {
		return true
	}

	placeholder, exists := r.objects[obj]
	if !exists {
		var objectKey string

		placeholder, objectKey = r.placeholder(obj)
		if placeholder == "" {
			return true
		}

		r.objects[obj] = placeholder
		r.typesByPlaceholder = append(r.typesByPlaceholder, objectKey)
	}

	r.renamed = append(r.renamed, behaviorRenamedIdent{ident: id, original: id.Name})
	id.Name = placeholder

	return true
}

func (r *behaviorBlockRenamer) placeholder(obj types.Object) (string, string) {
	if !behaviorNormalizableObject(obj) {
		objectKey, ok := r.owner.behaviorCanonicalObjectKey(obj, r.typeKeys)
		if !ok {
			return "", ""
		}

		return "slopelintO" + strconv.Itoa(len(r.objects)), objectKey
	}

	if _, isDefined := r.defined[obj]; isDefined {
		placeholder := "slopelintD" + strconv.Itoa(r.nextDefined)
		r.nextDefined++

		return placeholder, r.typeKeys.typeKey(obj.Type())
	}

	return r.functionBindings[obj], r.typeKeys.typeKey(obj.Type())
}

func behaviorBlockCallSupported(
	info *types.Info,
	call *ast.CallExpr,
	defined map[types.Object]struct{},
) bool {
	if info.Types[call.Fun].IsType() {
		return true
	}

	if id, ok := ast.Unparen(call.Fun).(*ast.Ident); ok {
		if _, isBuiltin := info.ObjectOf(id).(*types.Builtin); isBuiltin {
			return true
		}

		if _, isDefined := defined[info.ObjectOf(id)]; isDefined {
			return true
		}
	}

	_, _, ok := calledFunc(info, call)

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

func (b behaviorPackage) behaviorCanonicalObjectKey(
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
		if summary, ok := b.calls[FuncObjectKey(obj)]; ok {
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

func (b behaviorPackage) blockBehaviorEffects(stmts []ast.Stmt) behaviorEffects {
	var effects behaviorEffects

	for _, stmt := range stmts {
		ast.Inspect(stmt, func(node ast.Node) bool {
			nodeEffects, descend := b.behaviorEffectsForNode(node)
			effects |= nodeEffects

			return descend
		})
	}

	return effects
}

func (b behaviorPackage) behaviorEffectsForNode(node ast.Node) (behaviorEffects, bool) {
	switch node := node.(type) {
	case *ast.FuncLit:
		return 0, false
	case *ast.AssignStmt:
		if node.Tok.String() != ":=" && slices.ContainsFunc(node.Lhs, func(lhs ast.Expr) bool {
			return exprIsBehaviorWrite(b.pkg.TypesInfo, lhs)
		}) {
			return behaviorEffectWrite, true
		}
	case *ast.IncDecStmt:
		if exprIsBehaviorWrite(b.pkg.TypesInfo, node.X) {
			return behaviorEffectWrite, true
		}
	case *ast.CallExpr:
		return b.callBehaviorEffects(node), true
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

func exprIsBehaviorWrite(info *types.Info, expr ast.Expr) bool {
	id, ok := ast.Unparen(expr).(*ast.Ident)

	return !ok || !behaviorNormalizableObject(info.ObjectOf(id))
}

func (b behaviorPackage) callBehaviorEffects(call *ast.CallExpr) behaviorEffects {
	if b.pkg.TypesInfo.Types[call.Fun].IsType() {
		return 0
	}

	if id, ok := ast.Unparen(call.Fun).(*ast.Ident); ok {
		if builtin, ok := b.pkg.TypesInfo.ObjectOf(id).(*types.Builtin); ok {
			return behaviorBuiltinEffects(builtin.Name())
		}
	}

	if fn, _, ok := calledFunc(b.pkg.TypesInfo, call); ok {
		if summary, exists := b.calls[FuncObjectKey(fn)]; exists {
			return behaviorEffectCall | summary.effects
		}
	}

	return behaviorEffectCall
}
