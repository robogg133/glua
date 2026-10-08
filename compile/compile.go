package compile

import (
	"fmt"
	"math"

	"github.com/robogg133/glua/ir"
	"github.com/robogg133/glua/parser"
)

// Compile lowers a successfully parsed (optionally ir.Optimize'd) arena to Lua
// 5.5.1 instructions. It audits the arena, does not mutate it, and does not run
// IR rewrites implicitly. The result is an in-memory prototype, not a dump file.
func Compile(ast *parser.AST) (proto *Prototype, err error) {
	analysis, err := ir.Audit(ast)
	if err != nil {
		return nil, err
	}
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(compileFailure); ok {
				proto, err = nil, e.err
			} else {
				panic(r)
			}
		}
	}()
	f := newFunction(ast, &analysis, nil, ast.Root)
	f.proto.IsVararg = true
	f.proto.Upvalues = append(f.proto.Upvalues, Upvalue{Name: "_ENV", InStack: true})
	f.ensure(1, ast.Root) // Hidden vararg slot exists even for an unnamed '...'.
	f.live = f.free
	f.emit(ABC(OpVARARGPREP, 0, 0, 0, false), ast.Root)
	f.block(ast.Nodes[ast.Root].Left)
	f.finish(ast.Root)
	return f.proto, nil
}

type compileFailure struct{ err error }
type localVariable struct {
	name             string
	reg              int
	kind             uint8
	global, captured bool
	id               uint32
}
type variable struct {
	index      int
	up, global bool
	kind       uint8
}
type label struct{ pc, live int }
type compilerScope struct {
	parent       *compilerScope
	labels       map[string]label
	live, locals int
}
type pendingGoto struct {
	node              uint32
	scope             *compilerScope
	close, jump, live int
}
type compilerLoop struct {
	base   int
	breaks []int
}
type functionCompiler struct {
	ast          *parser.AST
	analysis     *ir.Analysis
	parent       *functionCompiler
	proto        *Prototype
	node         uint32
	free, live   int
	locals       []localVariable
	constants    map[Constant]int
	scope        *compilerScope
	gotos        []pendingGoto
	loops        []*compilerLoop
	needClose    bool
	varargID     uint32
	varargTables []bool
}

func newFunction(ast *parser.AST, analysis *ir.Analysis, parent *functionCompiler, node uint32) *functionCompiler {
	f := &functionCompiler{ast: ast, analysis: analysis, parent: parent, node: node,
		proto: &Prototype{MaxStackSize: 2, LineDefined: ast.Nodes[node].TokenPosition[0]}, constants: make(map[Constant]int)}
	if parent == nil {
		f.varargTables = varargTableModes(ast, analysis)
	} else {
		f.varargTables = parent.varargTables
	}
	return f
}
func (f *functionCompiler) fail(node uint32, message string) {
	pos := f.ast.Nodes[node].TokenPosition
	panic(compileFailure{fmt.Errorf("compile:%d:%d: %s", pos[0], pos[1], message)})
}
func (f *functionCompiler) ensure(top int, node uint32) {
	if top < 0 || top > MaxStack {
		f.fail(node, "too many registers (limit 255)")
	}
	f.free = max(f.free, top)
	f.proto.MaxStackSize = uint8(max(int(f.proto.MaxStackSize), top))
}
func (f *functionCompiler) reserve(count int, node uint32) int {
	base := f.free
	f.ensure(base+count, node)
	return base
}
func (f *functionCompiler) emit(i Instruction, node uint32) int {
	pc := len(f.proto.Code)
	if pc >= math.MaxInt32 {
		f.fail(node, "too many instructions")
	}
	f.proto.Code = append(f.proto.Code, i)
	line := f.ast.Nodes[node].TokenPosition[0]
	f.proto.Lines = append(f.proto.Lines, line)
	f.proto.LastLineDefined = max(f.proto.LastLineDefined, line)
	return pc
}
func (f *functionCompiler) move(dst, src int, node uint32) {
	if dst != src {
		f.emit(ABC(OpMOVE, dst, src, 0, false), node)
	}
}
func (f *functionCompiler) constant(c Constant, node uint32) int {
	if index, ok := f.constants[c]; ok {
		return index
	}
	index := len(f.proto.Constants)
	if index > MaxArgAx {
		f.fail(node, "too many constants")
	}
	f.constants[c] = index
	f.proto.Constants = append(f.proto.Constants, c)
	return index
}
func (f *functionCompiler) load(dst int, c Constant, node uint32) {
	switch c.Kind {
	case ConstantNil:
		f.emit(ABC(OpLOADNIL, dst, 0, 0, false), node)
		return
	case ConstantBoolean:
		op := OpLOADFALSE
		if c.Bits != 0 {
			op = OpLOADTRUE
		}
		f.emit(ABC(op, dst, 0, 0, false), node)
		return
	case ConstantInteger:
		v := int64(c.Bits)
		if v >= MinArgSBx && v <= MaxArgSBx {
			f.emit(AsBx(OpLOADI, dst, int(v)), node)
			return
		}
	case ConstantFloat:
		v := math.Float64frombits(c.Bits)
		if v >= MinArgSBx && v <= MaxArgSBx && math.Trunc(v) == v && !(v == 0 && math.Signbit(v)) {
			f.emit(AsBx(OpLOADF, dst, int(v)), node)
			return
		}
	}
	index := f.constant(c, node)
	if index <= MaxArgBx {
		f.emit(ABx(OpLOADK, dst, index), node)
	} else {
		f.emit(ABx(OpLOADKX, dst, 0), node)
		f.emit(Ax(OpEXTRAARG, index), node)
	}
}
func (f *functionCompiler) jump(node uint32) int { return f.emit(SJ(OpJMP, 0), node) }
func (f *functionCompiler) patch(pc, target int, node uint32) {
	delta := target - pc - 1
	if delta < MinArgSJ || delta > MaxArgSJ {
		f.fail(node, "jump too long")
	}
	f.proto.Code[pc] = SJ(OpJMP, delta)
}
func (f *functionCompiler) patchFor(pc, distance int, node uint32) {
	if distance < 0 || distance > MaxArgBx {
		f.fail(node, "for loop too long")
	}
	f.proto.Code[pc] = f.proto.Code[pc].SetBx(uint32(distance))
}
func (f *functionCompiler) resolve(name string, node uint32) variable {
	for i := len(f.locals) - 1; i >= 0; i-- {
		v := f.locals[i]
		if v.name == name {
			return variable{index: v.reg, global: v.global, kind: v.kind}
		}
	}
	if f.parent == nil {
		if name == "_ENV" {
			return variable{index: 0, up: true}
		}
		return variable{global: true}
	}
	v := f.parent.resolve(name, node)
	if v.global {
		return v
	}
	u := Upvalue{Name: name, InStack: !v.up, Index: uint8(v.index), Kind: v.kind}
	for i, previous := range f.proto.Upvalues {
		if previous == u {
			return variable{index: i, up: true, kind: v.kind}
		}
	}
	if len(f.proto.Upvalues) >= 255 {
		f.fail(node, "too many upvalues")
	}
	index := len(f.proto.Upvalues)
	f.proto.Upvalues = append(f.proto.Upvalues, u)
	return variable{index: index, up: true, kind: v.kind}
}
func (f *functionCompiler) localRegister(node uint32) (int, bool) {
	for f.ast.Nodes[node].Kind == parser.TagParenthesized {
		node = f.ast.Nodes[node].Left
	}
	if f.ast.Nodes[node].Kind != parser.TagNameReference {
		return 0, false
	}
	name := f.ast.Values[node]
	for i := len(f.locals) - 1; i >= 0; i-- {
		v := f.locals[i]
		if v.name == name {
			return v.reg, !v.global
		}
	}
	return 0, false
}
func (f *functionCompiler) directUpvalue(node uint32) (variable, bool) {
	if f.ast.Nodes[node].Kind != parser.TagNameReference {
		return variable{}, false
	}
	v := f.resolve(f.ast.Values[node], node)
	return v, v.up
}
func (f *functionCompiler) hiddenVararg(node uint32) bool {
	// Parentheses discharge the special vararg descriptor in Lua's parser.
	return f.varargID != 0 && !f.proto.VarargTable && f.ast.Nodes[node].Kind == parser.TagNameReference && f.analysis.References[node] == f.varargID
}
func (f *functionCompiler) readVariable(v variable, dst int, node uint32) {
	if v.up {
		f.emit(ABC(OpGETUPVAL, dst, v.index, 0, false), node)
	} else {
		f.move(dst, v.index, node)
	}
}
func (f *functionCompiler) readName(node uint32, dst int) {
	name := f.ast.Values[node]
	v := f.resolve(name, node)
	if !v.global {
		f.readVariable(v, dst, node)
		return
	}
	env := f.resolve("_ENV", node)
	if env.global {
		f.fail(node, "_ENV cannot be global")
	}
	k := f.constant(StringConstant(name), node)
	if env.up && len(name) <= 40 && k <= MaxArgC {
		f.emit(ABC(OpGETTABUP, dst, env.index, k, false), node)
		return
	}
	object := env.index
	if env.up {
		object = f.reserve(1, node)
		f.readVariable(env, object, node)
	}
	f.expressionField(dst, object, name, node)
}
func (f *functionCompiler) bind(node uint32, reg int, kind uint8, global bool) {
	id := f.analysis.References[node]
	// Audit records explicit name captures; global accesses also implicitly
	// capture a lexical _ENV, including accesses in later loop iterations.
	captured := !global && (f.ast.Values[node] == "_ENV" || id != 0 && f.analysis.Bindings[id].Captured)
	if id != 0 {
		switch f.analysis.Bindings[id].Attribute {
		case "const":
			kind = UpvalueConst
		case "close":
			kind = UpvalueToClose
		}
	}
	f.locals = append(f.locals, localVariable{name: f.ast.Values[node], reg: reg, kind: kind, global: global, captured: captured, id: id})
	if !global {
		f.ensure(reg+1, node)
		f.live = max(f.live, reg+1)
	}
	f.needClose = f.needClose || captured || kind == UpvalueToClose
}
func (f *functionCompiler) enterScope() *compilerScope {
	s := &compilerScope{parent: f.scope, labels: make(map[string]label), live: f.live, locals: len(f.locals)}
	f.scope = s
	return s
}
func (f *functionCompiler) closeLevel(first int) int {
	level := MaxStack
	for _, v := range f.locals[first:] {
		if !v.global && (v.captured || v.kind == UpvalueToClose) {
			level = min(level, v.reg)
		}
	}
	return level
}
func (f *functionCompiler) closeFrom(level int, node uint32) {
	if level < MaxStack {
		f.emit(ABC(OpCLOSE, level, 0, 0, false), node)
	}
}
func (f *functionCompiler) leaveScope(s *compilerScope, node uint32) {
	f.closeFrom(f.closeLevel(s.locals), node)
	f.locals = f.locals[:s.locals]
	f.live = s.live
	f.free = s.live
	f.scope = s.parent
}
func (f *functionCompiler) closure(node uint32, dst int) {
	if len(f.proto.Children) > MaxArgBx {
		f.fail(node, "too many nested functions")
	}
	child := newFunction(f.ast, f.analysis, f, node)
	n := f.ast.Nodes[node]
	for cell := n.Left; cell != 0; cell = f.ast.Nodes[cell].Right {
		p := f.ast.Nodes[f.ast.Nodes[cell].Left]
		if p.Kind == parser.TagVarargParameter {
			child.proto.IsVararg = true
			if p.Left != 0 {
				child.varargID = f.analysis.References[p.Left]
				child.proto.VarargTable = child.varargTables[child.varargID]
				child.bind(p.Left, child.live, UpvalueVararg, false)
			} else {
				child.ensure(child.live+1, node)
				child.live = child.free
			}
		} else {
			if child.proto.NumParams == 254 {
				f.fail(node, "too many parameters")
			}
			child.bind(p.Left, child.live, UpvalueRegular, false)
			child.proto.NumParams++
		}
	}
	if child.proto.IsVararg {
		child.emit(ABC(OpVARARGPREP, 0, 0, 0, false), node)
	}
	child.block(n.Right)
	child.finish(node)
	index := len(f.proto.Children)
	f.proto.Children = append(f.proto.Children, *child.proto)
	f.emit(ABx(OpCLOSURE, dst, index), node)
}
func varargTableModes(ast *parser.AST, analysis *ir.Analysis) []bool {
	// Analyze once for the entire arena, not once per nested function. Only
	// direct indexing retains the hidden descriptor. _ENV can also escape
	// implicitly through global accesses and captures, absent from References.
	tables := make([]bool, len(analysis.Bindings))
	for id, b := range analysis.Bindings {
		tables[id] = b.Captured || b.Name == "_ENV"
	}
	indexed := make([]bool, len(ast.Nodes))
	for _, n := range ast.Nodes {
		if n.Kind == parser.TagFieldAccess || n.Kind == parser.TagIndexAccess {
			indexed[n.Left] = true
		}
	}
	for i, n := range ast.Nodes {
		if n.Kind == parser.TagNameReference && !indexed[i] {
			tables[analysis.References[i]] = true
		}
	}
	return tables
}
func (f *functionCompiler) returnC() int {
	if f.proto.IsVararg && !f.proto.VarargTable {
		return int(f.proto.NumParams) + 1
	}
	return 0
}
func (f *functionCompiler) finish(node uint32) {
	f.emit(ABC(OpRETURN, f.live, 1, f.returnC(), false), node)
	for _, g := range f.gotos {
		name := f.ast.Values[f.ast.Nodes[g.node].Left]
		found := false
		for s := g.scope; s != nil; s = s.parent {
			if l, ok := s.labels[name]; ok {
				if l.live > g.live {
					f.fail(g.node, "goto enters a local scope")
				}
				f.proto.Code[g.close] = ABC(OpCLOSE, l.live, 0, 0, false)
				f.patch(g.jump, l.pc, g.node)
				found = true
				break
			}
		}
		if !found {
			f.fail(g.node, "no visible label '"+name+"'")
		}
	}
	for pc, i := range f.proto.Code {
		if i.Op() == OpRETURN || i.Op() == OpTAILCALL {
			if f.needClose {
				i = i.SetK(1)
			}
			if i.Op() == OpRETURN && !f.needClose && f.returnC() == 0 {
				if i.B() == 1 {
					i = ABC(OpRETURN0, 0, 0, 0, false)
				} else if i.B() == 2 {
					i = ABC(OpRETURN1, int(i.A()), 0, 0, false)
				}
			}
			f.proto.Code[pc] = i
		}
	}
}
