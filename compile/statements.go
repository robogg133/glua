package compile

import "github.com/robogg133/glua/parser"

func (f *functionCompiler) list(list uint32) []uint32 {
	var items []uint32
	for ; list != 0; list = f.ast.Nodes[list].Right {
		items = append(items, f.ast.Nodes[list].Left)
	}
	return items
}

// values adjusts the last expression only; surplus expressions still run.
func (f *functionCompiler) values(list uint32, base, count int, node uint32) {
	f.ensure(base+count, node)
	f.free = base
	used := 0
	for cell := list; cell != 0; cell = f.ast.Nodes[cell].Right {
		item := f.ast.Nodes[cell]
		want := 1
		if item.Right == 0 && expressionIsOpen(f.ast.Nodes[item.Left].Kind) {
			want = max(0, count-used)
		}
		dst := base + used
		if used >= count {
			dst = f.free
			want = 0
		}
		f.expr(item.Left, dst, want)
		if used >= count {
			f.free = base + count // Discarded results do not accumulate registers.
		}
		used += max(1, want)
	}
	if used < count {
		f.emit(ABC(OpLOADNIL, base+used, count-used-1, 0, false), node)
	}
	f.free = base + count
}

func (f *functionCompiler) block(node uint32) {
	s := f.enterScope()
	f.blockBody(node, false)
	f.leaveScope(s, node)
}
func (f *functionCompiler) blockBody(node uint32, repeat bool) {
	items := f.list(f.ast.Nodes[node].Left)
	last := -1
	for i, item := range items {
		k := f.ast.Nodes[item].Kind
		if k != parser.TagLabel && k != parser.TagEmpty {
			last = i
		}
	}
	for i, item := range items {
		if f.ast.Nodes[item].Kind == parser.TagLabel {
			live := f.live
			if i > last && !repeat {
				live = f.scope.live
			}
			name := f.ast.Values[f.ast.Nodes[item].Left]
			if _, ok := f.scope.labels[name]; ok {
				f.fail(item, "duplicate label '"+name+"'")
			}
			f.scope.labels[name] = label{pc: len(f.proto.Code), live: live}
		} else {
			f.statement(item)
		}
		f.free = f.live
	}
}
func (f *functionCompiler) statement(node uint32) {
	n := f.ast.Nodes[node]
	switch n.Kind {
	case parser.TagEmpty, parser.TagGlobalWildcardDeclaration:
	case parser.TagDoBlock:
		f.block(n.Left)
	case parser.TagCallStatement:
		f.expr(n.Left, f.free, 0)
	case parser.TagLocalDeclaration:
		bindings := f.list(n.Left)
		base := f.live
		f.values(n.Right, base, len(bindings), node)
		for i, item := range bindings {
			b := f.ast.Nodes[item]
			f.bind(b.Left, base+i, UpvalueRegular, false)
			if f.ast.Values[b.Right] == "close" {
				f.emit(ABC(OpTBC, base+i, 0, 0, false), item)
			}
		}
	case parser.TagLocalFunctionDeclaration:
		dst := f.live
		f.bind(n.Left, dst, UpvalueRegular, false)
		f.closure(n.Right, dst)
	case parser.TagGlobalDeclaration:
		f.globalDeclaration(node)
	case parser.TagGlobalFunctionDeclaration:
		f.bind(n.Left, 0, UpvalueGlobal, true)
		target := f.globalTarget(f.ast.Values[n.Left], node)
		dst := f.reserve(1, node)
		f.closure(n.Right, dst)
		f.checkGlobal(f.ast.Values[n.Left], node)
		f.store(target, dst, node)
	case parser.TagFunctionDeclaration, parser.TagMethodDeclaration:
		f.functionDeclaration(node)
	case parser.TagAssignment:
		f.assignment(node)
	case parser.TagReturn:
		f.returnStatement(node)
	case parser.TagIf:
		var exits []int
		for cell := n.Left; cell != 0; cell = f.ast.Nodes[cell].Right {
			branch := f.ast.Nodes[f.ast.Nodes[cell].Left]
			next := f.condition(branch.Left, false)
			f.block(branch.Right)
			exits = append(exits, f.jump(node))
			f.patch(next, len(f.proto.Code), node)
		}
		if n.Right != 0 {
			f.block(n.Right)
		}
		for _, pc := range exits {
			f.patch(pc, len(f.proto.Code), node)
		}
	case parser.TagWhile:
		start := len(f.proto.Code)
		exit := f.condition(n.Left, false)
		loop := f.pushLoop(f.live)
		f.block(n.Right)
		back := f.jump(node)
		f.patch(back, start, node)
		f.patch(exit, len(f.proto.Code), node)
		f.popLoop(loop, node)
	case parser.TagRepeatUntil:
		start := len(f.proto.Code)
		loop := f.pushLoop(f.live)
		s := f.enterScope()
		f.blockBody(n.Left, true)
		retry := f.condition(n.Right, false)
		close := f.closeLevel(s.locals)
		f.leaveScope(s, node)
		done := f.jump(node)
		f.patch(retry, len(f.proto.Code), node)
		f.closeFrom(close, node)
		back := f.jump(node)
		f.patch(back, start, node)
		f.patch(done, len(f.proto.Code), node)
		f.popLoop(loop, node)
	case parser.TagNumericFor:
		f.numericFor(node)
	case parser.TagGenericFor:
		f.genericFor(node)
	case parser.TagBreak:
		if len(f.loops) == 0 {
			f.fail(node, "break outside loop")
		}
		loop := f.loops[len(f.loops)-1]
		level := MaxStack
		for _, v := range f.locals {
			if !v.global && v.reg >= loop.base && (v.captured || v.kind == UpvalueToClose) {
				level = min(level, v.reg)
			}
		}
		f.closeFrom(level, node)
		loop.breaks = append(loop.breaks, f.jump(node))
	case parser.TagGoto:
		close := f.emit(ABC(OpCLOSE, 0, 0, 0, false), node)
		f.gotos = append(f.gotos, pendingGoto{node: node, scope: f.scope, close: close, jump: f.jump(node), live: f.live})
	default:
		f.fail(node, "unsupported statement kind")
	}
}
func (f *functionCompiler) pushLoop(base int) *compilerLoop {
	loop := &compilerLoop{base: base}
	f.loops = append(f.loops, loop)
	return loop
}
func (f *functionCompiler) popLoop(loop *compilerLoop, node uint32) {
	for _, pc := range loop.breaks {
		f.patch(pc, len(f.proto.Code), node)
	}
	f.loops = f.loops[:len(f.loops)-1]
}
func (f *functionCompiler) numericFor(node uint32) {
	n := f.ast.Nodes[node]
	header := f.ast.Nodes[n.Left]
	s := f.enterScope()
	base := f.live
	values := f.list(header.Right)
	f.ensure(base+3, node)
	for i, value := range values {
		if value == 0 {
			f.load(base+i, IntConstant(1), node)
		} else {
			f.expr(value, base+i, 1)
		}
	}
	f.free = base + 3
	f.live = base + 3
	f.bind(header.Left, base+2, UpvalueConst, false)
	prep := f.emit(ABx(OpFORPREP, base, 0), node)
	body := len(f.proto.Code)
	loop := f.pushLoop(base)
	f.block(n.Right)
	f.closeFrom(f.closeLevel(s.locals), node)
	step := f.emit(ABx(OpFORLOOP, base, 0), node)
	f.patchFor(step, step+1-body, node)
	f.patchFor(prep, step-prep-1, node)
	f.leaveScope(s, node)
	f.popLoop(loop, node)
}
func (f *functionCompiler) genericFor(node uint32) {
	n := f.ast.Nodes[node]
	header := f.ast.Nodes[n.Left]
	s := f.enterScope()
	base := f.live
	names := f.list(header.Left)
	f.values(header.Right, base, 4, node)
	f.ensure(base+max(6, 3+len(names)), node)
	f.live = base + 3 + len(names)
	// The VM swaps the fourth initializer into this closing slot in TFORPREP.
	f.locals = append(f.locals, localVariable{reg: base + 2, kind: UpvalueToClose})
	f.needClose = true
	for i, name := range names {
		kind := UpvalueRegular
		if i == 0 {
			kind = UpvalueConst
		}
		f.bind(name, base+3+i, kind, false)
	}
	f.free = f.live
	prep := f.emit(ABx(OpTFORPREP, base, 0), node)
	body := len(f.proto.Code)
	loop := f.pushLoop(base)
	f.block(n.Right)
	// Close each iteration's visible bindings, not the iterator resource.
	f.closeFrom(f.closeLevel(s.locals+1), node)
	call := f.emit(ABC(OpTFORCALL, base, 0, len(names), false), node)
	step := f.emit(ABx(OpTFORLOOP, base, 0), node)
	f.patchFor(prep, call-prep-1, node)
	f.patchFor(step, step+1-body, node)
	f.leaveScope(s, node)
	f.popLoop(loop, node)
}
func (f *functionCompiler) returnStatement(node uint32) {
	values := f.list(f.ast.Nodes[node].Left)
	base := f.free
	open := false
	for i, value := range values {
		open = i == len(values)-1 && expressionIsOpen(f.ast.Nodes[value].Kind)
		want := 1
		if open {
			want = -1
		}
		f.expr(value, base+i, want)
	}
	count := len(values) + 1
	if open {
		count = 0
		if len(values) == 1 && f.ast.Nodes[values[0]].Kind != parser.TagVararg {
			tbc := false
			for _, v := range f.locals {
				tbc = tbc || v.kind == UpvalueToClose
			}
			if !tbc {
				pc := len(f.proto.Code) - 1
				f.proto.Code[pc] = f.proto.Code[pc].SetOp(OpTAILCALL).SetC(uint8(f.returnC()))
			}
		}
	}
	if count > MaxArgB {
		f.fail(node, "too many return values")
	}
	f.emit(ABC(OpRETURN, base, count, f.returnC(), false), node)
}

// assignmentTarget retains Lua's local-register operands until the store. A
// later conflicting LHS is the one case that must snapshot them first.
type assignmentTarget struct {
	variable              variable
	table                 bool
	object, key           int
	objectUp, keyConstant bool
}

func (f *functionCompiler) globalTarget(name string, node uint32) assignmentTarget {
	env := f.resolve("_ENV", node)
	if env.global {
		f.fail(node, "_ENV cannot be global")
	}
	return f.namedTarget(env, name, node)
}
func (f *functionCompiler) nameTarget(node uint32) assignmentTarget {
	v := f.resolve(f.ast.Values[node], node)
	if v.global {
		return f.globalTarget(f.ast.Values[node], node)
	}
	return assignmentTarget{variable: v}
}
func (f *functionCompiler) target(node uint32) assignmentTarget {
	n := f.ast.Nodes[node]
	if n.Kind == parser.TagNameTarget {
		return f.nameTarget(node)
	}
	object, up := f.directUpvalue(n.Left)
	if !up {
		object = variable{index: f.expressionRegister(n.Left)}
	}
	if n.Kind == parser.TagFieldTarget {
		return f.namedTarget(object, f.ast.Values[n.Right], node)
	}
	if c, ok := literalConstant(f.ast, n.Right); ok && c.Kind == ConstantString {
		return f.namedTarget(object, c.String, node)
	}
	key := f.expressionRegister(n.Right)
	return f.indexedTarget(object, key, node)
}
func (f *functionCompiler) namedTarget(object variable, name string, node uint32) assignmentTarget {
	index := f.constant(StringConstant(name), node)
	if len(name) <= 40 && index <= MaxArgB {
		return assignmentTarget{table: true, object: object.index, objectUp: object.up, key: index, keyConstant: true}
	}
	key := f.reserve(1, node)
	f.load(key, StringConstant(name), node)
	return f.indexedTarget(object, key, node)
}
func (f *functionCompiler) indexedTarget(object variable, key int, node uint32) assignmentTarget {
	reg := object.index
	if object.up {
		// Dynamic key evaluation may replace the table through this upvalue.
		reg = f.reserve(1, node)
		f.readVariable(object, reg, node)
	}
	return assignmentTarget{table: true, object: reg, key: key}
}
func (f *functionCompiler) store(t assignmentTarget, value int, node uint32) {
	if !t.table {
		if t.variable.up {
			f.emit(ABC(OpSETUPVAL, value, t.variable.index, 0, false), node)
		} else {
			f.move(t.variable.index, value, node)
		}
		return
	}
	op := OpSETTABLE
	if t.objectUp {
		op = OpSETTABUP
	} else if t.keyConstant {
		op = OpSETFIELD
	}
	f.emit(ABC(op, t.object, t.key, value, false), node)
}
func (f *functionCompiler) assignment(node uint32) {
	n := f.ast.Nodes[node]
	var targets []assignmentTarget
	for cell := n.Left; cell != 0; cell = f.ast.Nodes[cell].Right {
		t := f.target(f.ast.Nodes[cell].Left)
		if !t.table {
			// Match check_conflict in Lua: table/upvalue and key operands captured by
			// earlier targets must survive a later assignment to that variable.
			copied := -1
			for i := range targets {
				previous := &targets[i]
				if !previous.table {
					continue
				}
				object := previous.object == t.variable.index && previous.objectUp == t.variable.up
				key := !previous.keyConstant && !t.variable.up && previous.key == t.variable.index
				if object || key {
					if copied < 0 {
						copied = f.reserve(1, node)
						f.readVariable(t.variable, copied, node)
					}
					if object {
						previous.object = copied
						previous.objectUp = false
					}
					if key {
						previous.key = copied
					}
				}
			}
		}
		targets = append(targets, t)
	}
	base := f.free
	f.values(n.Right, base, len(targets), node)
	for i := len(targets) - 1; i >= 0; i-- {
		f.store(targets[i], base+i, node)
	}
}
func (f *functionCompiler) checkGlobal(name string, node uint32) {
	base := f.free
	// Rebuild the read after the RHS: its calls may have replaced _ENV, even
	// when the store retained an earlier snapshot for a long/wide key.
	t := f.globalTarget(name, node)
	dst := f.reserve(1, node)
	op := OpGETTABLE
	if t.objectUp {
		op = OpGETTABUP
	} else if t.keyConstant {
		op = OpGETFIELD
	}
	f.emit(ABC(op, dst, t.object, t.key, false), node)
	nameIndex := f.constant(StringConstant(name), node) + 1
	if nameIndex > MaxArgBx {
		nameIndex = 0
	}
	f.emit(ABx(OpERRNNIL, dst, nameIndex), node)
	f.free = base
}
func (f *functionCompiler) globalDeclaration(node uint32) {
	n := f.ast.Nodes[node]
	bindings := f.list(n.Left)
	targets := make([]assignmentTarget, len(bindings))
	if n.Right != 0 {
		for i, item := range bindings {
			b := f.ast.Nodes[item]
			targets[i] = f.globalTarget(f.ast.Values[b.Left], node)
		}
		base := f.free
		f.values(n.Right, base, len(bindings), node)
		for i := len(bindings) - 1; i >= 0; i-- {
			b := f.ast.Nodes[bindings[i]]
			f.checkGlobal(f.ast.Values[b.Left], node)
			f.store(targets[i], base+i, node)
		}
	}
	for _, item := range bindings {
		f.bind(f.ast.Nodes[item].Left, 0, UpvalueGlobal, true)
	}
}
func (f *functionCompiler) functionDeclaration(node uint32) {
	n := f.ast.Nodes[node]
	path := f.ast.Nodes[n.Left]
	parts := f.ast.Nodes[path.Right]
	fields := f.list(parts.Left)
	if parts.Right != 0 {
		fields = append(fields, parts.Right)
	}
	var target assignmentTarget
	if len(fields) == 0 {
		target = f.nameTarget(path.Left)
	} else {
		v := f.resolve(f.ast.Values[path.Left], node)
		object := v.index
		if v.up || v.global {
			object = f.reserve(1, node)
			f.readName(path.Left, object)
		}
		for _, field := range fields[:len(fields)-1] {
			dst := f.reserve(1, node)
			f.expressionField(dst, object, f.ast.Values[field], node)
			object = dst
		}
		target = f.namedTarget(variable{index: object}, f.ast.Values[fields[len(fields)-1]], node)
	}
	value := f.reserve(1, node)
	f.closure(n.Right, value)
	f.store(target, value, node)
}
