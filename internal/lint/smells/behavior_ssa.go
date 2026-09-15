package smells

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ssa"
)

const (
	behaviorMinFunctionInstructions = 6
	behaviorMinimumCloneCount       = 2
	behaviorConditionalSuccessors   = 2
)

type behaviorSSAFunction struct {
	fn        *ssa.Function
	obj       *types.Func
	name      string
	signature string
	pos       tokenRange
	syntax    ast.Node
}

type behaviorSSAAnalysis struct {
	candidates    map[*Package][]behaviorSSAFunction
	functions     []*ssa.Function
	interfaces    []*types.Interface
	aliases       map[*ssa.Function]*ssa.Function
	objectTargets map[string]*ssa.Function
	summaries     map[*ssa.Function]behaviorSummary
	objectSummary map[string]behaviorSummary
}

type tokenRange struct {
	start token.Pos
	end   token.Pos
}

func (l *Runner) functionBehaviorCandidatesFrom(
	functions []behaviorSSAFunction,
	comparableSignatures map[string]struct{},
	analysis *behaviorSSAAnalysis,
) []behaviorCandidate {
	if comparableSignatures == nil {
		comparableSignatures = behaviorComparableSignatures(functions)
	}

	if len(comparableSignatures) == 0 {
		return nil
	}

	if len(functions) == 0 {
		return nil
	}

	candidates := make([]behaviorCandidate, 0, len(functions))
	for _, function := range functions {
		if _, comparable := comparableSignatures[function.signature]; !comparable {
			continue
		}

		if l.behaviorCloneSuppressed(function.syntax) {
			continue
		}

		if l.behaviorInterfaceForwarder(function, analysis.interfaces) {
			continue
		}

		key, effects, weight, ok := newBehaviorSSAEncoder(
			function.fn,
			analysis.summaries,
			analysis.aliases,
			analysis.objectTargets,
		).encode()
		if !ok || weight < behaviorMinFunctionInstructions {
			continue
		}

		candidates = append(candidates, newBehaviorCandidate(
			l.pkg,
			"function|"+key,
			function.name,
			function.pos.start,
			function.pos.end,
			behaviorCandidateFunction,
			weight,
			effects,
		))
	}

	return candidates
}

func behaviorComparableSignatures(functions []behaviorSSAFunction) map[string]struct{} {
	counts := make(map[string]int)
	comparable := make(map[string]struct{})

	for _, function := range functions {
		counts[function.signature]++
		if counts[function.signature] == behaviorMinimumCloneCount {
			comparable[function.signature] = struct{}{}
		}
	}

	return comparable
}

func behaviorSSAFunctionClosure(roots []behaviorSSAFunction) []*ssa.Function {
	all := make([]*ssa.Function, 0, len(roots))
	seen := make(map[*ssa.Function]struct{}, len(roots))

	queue := make([]*ssa.Function, 0, len(roots))
	for _, function := range roots {
		queue = append(queue, function.fn)
	}

	for len(queue) > 0 {
		fn := queue[0]
		queue = queue[1:]

		if _, exists := seen[fn]; exists || len(fn.Blocks) == 0 {
			continue
		}

		seen[fn] = struct{}{}
		all = append(all, fn)
		queue = append(queue, fn.AnonFuncs...)

		for _, callee := range behaviorSSAFunctionOperands(fn) {
			if len(callee.Blocks) > 0 {
				queue = append(queue, callee)
			}
		}
	}

	return all
}

func buildBehaviorSSAAnalysis(pkgs []*Package) *behaviorSSAAnalysis {
	analysis := &behaviorSSAAnalysis{
		candidates:    make(map[*Package][]behaviorSSAFunction, len(pkgs)),
		interfaces:    behaviorInterfaces(pkgs),
		aliases:       make(map[*ssa.Function]*ssa.Function),
		objectTargets: make(map[string]*ssa.Function),
		objectSummary: make(map[string]behaviorSummary),
	}
	ssaPkgs := buildBehaviorSSAPackages(pkgs)

	for _, pkg := range pkgs {
		r := newRunner(pkg)
		roots := r.behaviorSSAFunctions(ssaPkgs[pkg])
		functions := behaviorSSAFunctionClosure(roots)
		analysis.functions = append(analysis.functions, functions...)
		analysis.candidates[pkg] = append(roots, r.behaviorClosureCandidates(functions)...)

		for _, root := range roots {
			if root.obj != nil {
				analysis.objectTargets[funcObjectKey(root.obj)] = root.fn
			}
		}
	}

	known := make(map[*ssa.Function]struct{}, len(analysis.functions))
	for _, fn := range analysis.functions {
		known[fn] = struct{}{}
		analysis.aliases[fn] = fn
	}

	for _, fn := range analysis.functions {
		for _, callee := range behaviorSSAFunctionOperands(fn) {
			if _, local := known[callee]; local {
				analysis.aliases[callee] = callee
				continue
			}

			if obj, ok := callee.Object().(*types.Func); ok {
				if target := analysis.objectTargets[funcObjectKey(obj)]; target != nil {
					analysis.aliases[callee] = target
				}
			}
		}
	}

	analysis.summaries = behaviorCalleeSummaries(
		analysis.functions,
		analysis.aliases,
		analysis.objectTargets,
	)
	for key, target := range analysis.objectTargets {
		if summary, ok := analysis.summaries[target]; ok {
			analysis.objectSummary[key] = summary
		}
	}

	return analysis
}

func behaviorSSAFunctionOperands(fn *ssa.Function) []*ssa.Function {
	var functions []*ssa.Function

	for _, block := range fn.Blocks {
		for _, instruction := range block.Instrs {
			for _, operand := range instruction.Operands(nil) {
				if operand == nil || *operand == nil {
					continue
				}

				if callee, ok := (*operand).(*ssa.Function); ok {
					functions = append(functions, callee)
				}
			}
		}
	}

	return functions
}

func (l *Runner) behaviorClosureCandidates(functions []*ssa.Function) []behaviorSSAFunction {
	candidates := make([]behaviorSSAFunction, 0)
	autoSuppressed := l.behaviorAutoSuppressedClosures()

	for _, fn := range functions {
		syntax, ok := fn.Syntax().(*ast.FuncLit)
		if !ok {
			continue
		}

		if _, suppressed := autoSuppressed[syntax]; suppressed {
			continue
		}

		owner := "function literal"

		if parent := fn.Parent(); parent != nil {
			if obj, ok := parent.Object().(*types.Func); ok {
				owner = behaviorFunctionName(obj) + " closure"
			} else if parent.Name() != "" {
				owner = parent.Name() + " closure"
			}
		}

		candidates = append(candidates, behaviorSSAFunction{
			fn:        fn,
			name:      owner,
			signature: newBehaviorTypeEncoder(fn.Signature).signatureKey(fn.Signature),
			pos:       tokenRange{start: syntax.Type.Func, end: syntax.End()},
			syntax:    syntax,
		})
	}

	return candidates
}

func buildBehaviorSSAPackages(pkgs []*Package) map[*Package]*ssa.Package {
	ssaPkgs := make(map[*Package]*ssa.Package, len(pkgs))
	if len(pkgs) == 0 {
		return ssaPkgs
	}

	program := ssa.NewProgram(pkgs[0].FSet, ssa.InstantiateGenerics)
	for _, pkg := range pkgs {
		ssaPkgs[pkg] = program.CreatePackage(pkg.TypesPkg, pkg.Files, pkg.TypesInfo, true)
	}

	for _, pkg := range pkgs {
		for _, imported := range pkg.TypesPkg.Imports() {
			if program.Package(imported) == nil {
				program.CreatePackage(imported, nil, nil, true)
			}
		}
	}

	program.Build()

	return ssaPkgs
}

func (l *Runner) behaviorSSAFunctions(ssaPkg *ssa.Package) []behaviorSSAFunction {
	functions := make([]behaviorSSAFunction, 0, len(l.pkg.ProductionFuncs))
	for _, decl := range l.pkg.ProductionFuncs {
		if decl.Body == nil || l.behaviorGenerated(decl) {
			continue
		}

		obj, ok := l.pkg.TypesInfo.Defs[decl.Name].(*types.Func)
		if !ok {
			continue
		}

		fn := ssaPkg.Prog.FuncValue(obj)
		if fn == nil || len(fn.Blocks) == 0 || fn.Synthetic != "" {
			continue
		}

		functions = append(functions, behaviorSSAFunction{
			fn:        fn,
			obj:       obj,
			name:      behaviorFunctionName(obj),
			signature: newBehaviorTypeEncoder(fn.Signature).signatureKey(fn.Signature),
			pos:       tokenRange{start: decl.Name.Pos(), end: decl.Body.End()},
			syntax:    decl,
		})
	}

	return functions
}

func behaviorFunctionName(fn *types.Func) string {
	signature, _ := fn.Type().(*types.Signature)
	if signature == nil || signature.Recv() == nil {
		return fn.Name()
	}

	return "(" + behaviorReceiverName(signature.Recv().Type()) + ")." + fn.Name()
}

func behaviorReceiverName(typ types.Type) string {
	pointer := ""
	if ptr, ok := types.Unalias(typ).(*types.Pointer); ok {
		pointer = "*"
		typ = ptr.Elem()
	}

	if named, ok := types.Unalias(typ).(*types.Named); ok {
		return pointer + named.Obj().Name()
	}

	return pointer + (&behaviorTypeEncoder{
		params:   make(map[*types.TypeParam]string),
		visiting: make(map[types.Type]bool),
	}).typeKey(typ)
}

type behaviorSSAEncoder struct {
	fn            *ssa.Function
	summaries     map[*ssa.Function]behaviorSummary
	aliases       map[*ssa.Function]*ssa.Function
	objectTargets map[string]*ssa.Function
	typeKeys      *behaviorTypeEncoder
	blocks        []*ssa.BasicBlock
	blockIDs      map[*ssa.BasicBlock]int
	valueIDs      map[ssa.Value]string
}

func newBehaviorSSAEncoder(
	fn *ssa.Function,
	summaries map[*ssa.Function]behaviorSummary,
	aliases map[*ssa.Function]*ssa.Function,
	objectTargets map[string]*ssa.Function,
) *behaviorSSAEncoder {
	blocks := behaviorBlockOrder(fn)
	encoder := &behaviorSSAEncoder{
		fn:            fn,
		summaries:     summaries,
		aliases:       aliases,
		objectTargets: objectTargets,
		typeKeys:      newBehaviorTypeEncoder(fn.Signature),
		blocks:        blocks,
		blockIDs:      make(map[*ssa.BasicBlock]int, len(blocks)),
		valueIDs:      make(map[ssa.Value]string),
	}

	for idx, block := range blocks {
		encoder.blockIDs[block] = idx
	}

	for idx, param := range fn.Params {
		encoder.valueIDs[param] = "p" + strconv.Itoa(idx)
	}

	for idx, free := range fn.FreeVars {
		encoder.valueIDs[free] = "f" + strconv.Itoa(idx)
	}

	nextValue := 0

	for _, block := range blocks {
		for _, instruction := range block.Instrs {
			value, isValue := instruction.(ssa.Value)
			if !isValue {
				continue
			}

			encoder.valueIDs[value] = "v" + strconv.Itoa(nextValue)
			nextValue++
		}
	}

	return encoder
}

func behaviorBlockOrder(fn *ssa.Function) []*ssa.BasicBlock {
	if len(fn.Blocks) == 0 {
		return nil
	}

	blocks := make([]*ssa.BasicBlock, 0, len(fn.Blocks))
	seen := make(map[*ssa.BasicBlock]struct{}, len(fn.Blocks))
	queue := []*ssa.BasicBlock{fn.Blocks[0]}

	for len(queue) > 0 {
		block := queue[0]
		queue = queue[1:]

		if _, exists := seen[block]; exists {
			continue
		}

		seen[block] = struct{}{}
		blocks = append(blocks, block)
		queue = append(queue, block.Succs...)
	}

	remaining := make([]*ssa.BasicBlock, 0)

	for _, block := range fn.Blocks {
		if _, exists := seen[block]; !exists {
			remaining = append(remaining, block)
		}
	}

	sort.Slice(remaining, func(i, j int) bool {
		return remaining[i].Index < remaining[j].Index
	})

	return append(blocks, remaining...)
}

func (e *behaviorSSAEncoder) encode() (string, behaviorEffects, int, bool) {
	var out strings.Builder
	out.WriteString(e.typeKeys.signatureKey(e.fn.Signature))

	var effects behaviorEffects

	weight := 0

	for _, block := range e.blocks {
		fmt.Fprintf(&out, "\nb%d{", e.blockIDs[block])

		for _, instruction := range block.Instrs {
			encoded, instructionEffects, meaningful, ok := e.encodeInstruction(instruction)
			if !ok {
				return "", 0, 0, false
			}

			if encoded == "" {
				continue
			}

			out.WriteString(encoded)
			out.WriteByte(';')

			effects |= instructionEffects

			if meaningful {
				weight++
			}
		}

		out.WriteByte('}')
	}

	return out.String(), effects, weight, true
}

//nolint:cyclop,maintidx // SSA exposes an exhaustive type set; unknown types fail closed.
func (e *behaviorSSAEncoder) encodeInstruction(
	instruction ssa.Instruction,
) (string, behaviorEffects, bool, bool) {
	if _, isDebug := instruction.(*ssa.DebugRef); isDebug {
		return "", 0, false, true
	}

	prefix := ""
	if value, isValue := instruction.(ssa.Value); isValue {
		prefix = e.valueIDs[value] + ":" + e.typeKeys.typeKey(value.Type()) + "="
	}

	operands := func(values ...ssa.Value) (string, bool) {
		parts := make([]string, len(values))
		for idx, value := range values {
			encoded, ok := e.valueRef(value)
			if !ok {
				return "", false
			}

			parts[idx] = encoded
		}

		return strings.Join(parts, ","), true
	}

	switch instruction := instruction.(type) {
	case *ssa.Alloc:
		effects := behaviorEffects(0)
		if instruction.Heap {
			effects = behaviorEffectAllocate
		}

		return prefix + fmt.Sprintf("alloc(heap=%t)", instruction.Heap), effects, true, true
	case *ssa.Phi:
		parts := make([]string, 0, len(instruction.Edges))
		for idx, edge := range instruction.Edges {
			value, ok := e.valueRef(edge)
			if !ok || idx >= len(instruction.Block().Preds) {
				return "", 0, false, false
			}

			parts = append(
				parts,
				fmt.Sprintf("b%d:%s", e.blockIDs[instruction.Block().Preds[idx]], value),
			)
		}

		sort.Strings(parts)

		return prefix + "phi(" + strings.Join(parts, ",") + ")", 0, true, true
	case *ssa.Call:
		call, effects, ok := e.encodeCall(&instruction.Call)
		return prefix + call, effects, true, ok
	case *ssa.BinOp:
		args, ok := operands(instruction.X, instruction.Y)
		return prefix + "bin(" + instruction.Op.String() + "," + args + ")", 0, true, ok
	case *ssa.UnOp:
		arg, ok := operands(instruction.X)

		effects := behaviorEffects(0)
		if instruction.Op.String() == "*" && !behaviorLocalMemory(instruction.X) {
			effects |= behaviorEffectRead
		}

		if instruction.Op.String() == "<-" {
			effects |= behaviorEffectConcurrent
		}

		return prefix + fmt.Sprintf(
			"unary(%s,ok=%t,%s)",
			instruction.Op,
			instruction.CommaOk,
			arg,
		), effects, true, ok
	case *ssa.ChangeType:
		arg, ok := operands(instruction.X)
		return prefix + "changetype(" + arg + ")", 0, true, ok
	case *ssa.Convert:
		arg, ok := operands(instruction.X)
		return prefix + "convert(" + arg + ")", 0, true, ok
	case *ssa.MultiConvert:
		arg, ok := operands(instruction.X)
		return prefix + "multiconvert(" + arg + ")", 0, true, ok
	case *ssa.ChangeInterface:
		arg, ok := operands(instruction.X)
		return prefix + "changeinterface(" + arg + ")", 0, true, ok
	case *ssa.SliceToArrayPointer:
		arg, ok := operands(instruction.X)
		return prefix + "slicetoarray(" + arg + ")", behaviorEffectPanic, true, ok
	case *ssa.MakeInterface:
		arg, ok := operands(instruction.X)
		return prefix + "makeinterface(" + arg + ")", 0, true, ok
	case *ssa.MakeClosure:
		values := append([]ssa.Value{instruction.Fn}, instruction.Bindings...)
		args, ok := operands(values...)

		return prefix + "closure(" + args + ")", behaviorEffectAllocate, true, ok
	case *ssa.MakeMap:
		arg, ok := operands(instruction.Reserve)
		return prefix + "makemap(" + arg + ")", behaviorEffectAllocate, true, ok
	case *ssa.MakeChan:
		arg, ok := operands(instruction.Size)
		return prefix + "makechan(" + arg + ")", behaviorEffectAllocate, true, ok
	case *ssa.MakeSlice:
		args, ok := operands(instruction.Len, instruction.Cap)
		return prefix + "makeslice(" + args + ")", behaviorEffectAllocate, true, ok
	case *ssa.Slice:
		args, ok := operands(instruction.X, instruction.Low, instruction.High, instruction.Max)
		return prefix + "slice(" + args + ")", behaviorEffectPanic, true, ok
	case *ssa.FieldAddr:
		arg, ok := operands(instruction.X)

		effects := behaviorEffects(0)
		if !behaviorLocalMemory(instruction.X) {
			effects = behaviorEffectPanic
		}

		return prefix + fmt.Sprintf("fieldaddr(%d,%s)", instruction.Field, arg), effects, true, ok
	case *ssa.Field:
		arg, ok := operands(instruction.X)
		return prefix + fmt.Sprintf("field(%d,%s)", instruction.Field, arg), 0, true, ok
	case *ssa.IndexAddr:
		args, ok := operands(instruction.X, instruction.Index)
		return prefix + "indexaddr(" + args + ")", behaviorEffectPanic, true, ok
	case *ssa.Index:
		args, ok := operands(instruction.X, instruction.Index)
		return prefix + "index(" + args + ")", behaviorEffectRead | behaviorEffectPanic, true, ok
	case *ssa.Lookup:
		args, ok := operands(instruction.X, instruction.Index)

		return prefix + fmt.Sprintf(
			"lookup(ok=%t,%s)",
			instruction.CommaOk,
			args,
		), behaviorEffectRead, true, ok
	case *ssa.Select:
		parts := make([]string, 0, len(instruction.States))
		for _, state := range instruction.States {
			args, ok := operands(state.Chan, state.Send)
			if !ok {
				return "", 0, false, false
			}

			parts = append(parts, fmt.Sprintf("%d:%s", state.Dir, args))
		}

		return prefix + fmt.Sprintf(
			"select(block=%t,%s)",
			instruction.Blocking,
			strings.Join(parts, "|"),
		), behaviorEffectConcurrent, true, true
	case *ssa.Range:
		arg, ok := operands(instruction.X)
		return prefix + "range(" + arg + ")", behaviorEffectRead, true, ok
	case *ssa.Next:
		arg, ok := operands(instruction.Iter)

		return prefix + fmt.Sprintf(
			"next(string=%t,%s)",
			instruction.IsString,
			arg,
		), behaviorEffectRead, true, ok
	case *ssa.TypeAssert:
		arg, ok := operands(instruction.X)

		effects := behaviorEffects(0)
		if !instruction.CommaOk {
			effects = behaviorEffectPanic
		}

		return prefix + fmt.Sprintf(
			"assert(%s,ok=%t,%s)",
			e.typeKeys.typeKey(instruction.AssertedType),
			instruction.CommaOk,
			arg,
		), effects, true, ok
	case *ssa.Extract:
		arg, ok := operands(instruction.Tuple)
		return prefix + fmt.Sprintf("extract(%d,%s)", instruction.Index, arg), 0, true, ok
	default:
		return e.encodeControlInstruction(instruction, operands)
	}
}

//nolint:cyclop // Control instructions use the same exhaustive, fail-closed dispatch.
func (e *behaviorSSAEncoder) encodeControlInstruction(
	instruction ssa.Instruction,
	operands func(...ssa.Value) (string, bool),
) (string, behaviorEffects, bool, bool) {
	switch instruction := instruction.(type) {
	case *ssa.Jump:
		if len(instruction.Block().Succs) != 1 {
			return "", 0, false, false
		}

		return fmt.Sprintf("jump(b%d)", e.blockIDs[instruction.Block().Succs[0]]), 0, false, true
	case *ssa.If:
		cond, ok := operands(instruction.Cond)
		if len(instruction.Block().Succs) != behaviorConditionalSuccessors {
			return "", 0, false, false
		}

		return fmt.Sprintf(
			"if(%s,b%d,b%d)",
			cond,
			e.blockIDs[instruction.Block().Succs[0]],
			e.blockIDs[instruction.Block().Succs[1]],
		), 0, true, ok
	case *ssa.Return:
		args, ok := operands(instruction.Results...)
		return "return(" + args + ")", 0, true, ok
	case *ssa.RunDefers:
		return "rundefers", behaviorEffectDefer, true, true
	case *ssa.Panic:
		arg, ok := operands(instruction.X)
		return "panic(" + arg + ")", behaviorEffectPanic, true, ok
	case *ssa.Go:
		call, callEffects, ok := e.encodeCall(&instruction.Call)
		return "go(" + call + ")", callEffects | behaviorEffectConcurrent, true, ok
	case *ssa.Defer:
		call, callEffects, ok := e.encodeCall(&instruction.Call)
		stack, stackOK := e.valueRef(instruction.DeferStack)

		return "defer(" + call + "," + stack + ")", callEffects | behaviorEffectDefer,
			true, ok && stackOK
	case *ssa.Send:
		args, ok := operands(instruction.Chan, instruction.X)
		return "send(" + args + ")", behaviorEffectConcurrent, true, ok
	case *ssa.Store:
		args, ok := operands(instruction.Addr, instruction.Val)

		effects := behaviorEffects(0)
		if !behaviorLocalMemory(instruction.Addr) {
			effects = behaviorEffectWrite
		}

		return "store(" + args + ")", effects, true, ok
	case *ssa.MapUpdate:
		args, ok := operands(instruction.Map, instruction.Key, instruction.Value)
		return "mapupdate(" + args + ")", behaviorEffectWrite, true, ok
	default:
		return "", 0, false, false
	}
}

func behaviorLocalMemory(value ssa.Value) bool {
	switch value := value.(type) {
	case *ssa.Alloc:
		return !value.Heap
	case *ssa.FieldAddr:
		return behaviorLocalMemory(value.X)
	case *ssa.IndexAddr:
		return behaviorLocalMemory(value.X)
	case *ssa.ChangeType:
		return behaviorLocalMemory(value.X)
	case *ssa.Convert:
		return behaviorLocalMemory(value.X)
	default:
		return false
	}
}

func (e *behaviorSSAEncoder) encodeCall(call *ssa.CallCommon) (string, behaviorEffects, bool) {
	var callee string

	if call.IsInvoke() {
		value, ok := e.valueRef(call.Value)
		if !ok {
			return "", 0, false
		}

		callee = "invoke:" + e.typeKeys.objectKey(call.Method) + ":" + value
	} else {
		value, ok := e.valueRef(call.Value)
		if !ok {
			return "", 0, false
		}

		callee = value
	}

	args := make([]string, len(call.Args))
	for idx, arg := range call.Args {
		encoded, ok := e.valueRef(arg)
		if !ok {
			return "", 0, false
		}

		args[idx] = encoded
	}

	effects := behaviorEffectCall
	if builtin, ok := call.Value.(*ssa.Builtin); ok {
		effects = behaviorBuiltinEffects(builtin.Name())
	} else if summary, ok := e.callSummary(call); ok {
		effects |= summary.effects
	}

	return "call(" + callee + "," + strings.Join(args, ",") + ")", effects, true
}

func (e *behaviorSSAEncoder) callSummary(call *ssa.CallCommon) (behaviorSummary, bool) {
	if callee, ok := call.Value.(*ssa.Function); ok {
		return e.functionSummary(callee)
	}

	if call.IsInvoke() && call.Method != nil {
		if target := e.objectTargets[funcObjectKey(call.Method)]; target != nil {
			return e.functionSummary(target)
		}
	}

	return behaviorSummary{}, false
}

func (e *behaviorSSAEncoder) functionSummary(fn *ssa.Function) (behaviorSummary, bool) {
	if alias := e.aliases[fn]; alias != nil {
		fn = alias
	}

	summary, ok := e.summaries[fn]

	return summary, ok
}

func behaviorBuiltinEffects(name string) behaviorEffects {
	switch name {
	case "len", "cap", "complex", "real", "imag", "min", "max":
		return 0
	case "make", "new", "append":
		return behaviorEffectAllocate
	case "panic":
		return behaviorEffectPanic
	case "copy", "delete", "clear":
		return behaviorEffectWrite
	case "close":
		return behaviorEffectConcurrent
	default:
		return behaviorEffectCall
	}
}

func (e *behaviorSSAEncoder) valueRef(value ssa.Value) (string, bool) {
	if value == nil {
		return "-", true
	}

	if id, ok := e.valueIDs[value]; ok {
		return id, true
	}

	switch value := value.(type) {
	case *ssa.Const:
		return behaviorConstantKey(value.Value) + ":" + e.typeKeys.typeKey(value.Type()), true
	case *ssa.Global:
		return "global:" + value.Pkg.Pkg.Path() + "." + value.Name() + ":" +
			e.typeKeys.typeKey(value.Type()), true
	case *ssa.Builtin:
		return "builtin:" + value.Name(), true
	case *ssa.Function:
		if summary, ok := e.functionSummary(value); ok {
			return "local:" + summary.digest, true
		}

		if value.Pkg == e.fn.Pkg && value.Syntax() != nil {
			return "local-signature:" +
				newBehaviorTypeEncoder(value.Signature).signatureKey(value.Signature), true
		}

		if obj := value.Object(); obj != nil {
			return "function:" + e.typeKeys.objectKey(obj), true
		}

		return "function:" + value.String() + ":" +
			newBehaviorTypeEncoder(value.Signature).signatureKey(value.Signature), true
	default:
		return "", false
	}
}

func behaviorConstantKey(value constant.Value) string {
	if value == nil {
		return "nil"
	}

	return value.ExactString()
}
