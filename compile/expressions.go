package compile

import (
	"math/bits"
	"strconv"
	"strings"

	"github.com/robogg133/glua/parser"
)

// literalConstant also accepts signed numerals produced by IR passes. Hex
// integers wrap modulo 2^64; decimal integer overflow falls back to float.
func literalConstant(ast *parser.AST, node uint32) (Constant, bool) {
	if node == 0 || int(node) >= len(ast.Nodes) {
		return Constant{}, false
	}
	kind := ast.Nodes[node].Kind
	if kind == parser.TagNil {
		return NilConstant(), true
	}
	if int(node) >= len(ast.Values) {
		return Constant{}, false
	}
	s := ast.Values[node]
	switch kind {
	case parser.TagBoolean:
		return BoolConstant(s == "true"), s == "true" || s == "false"
	case parser.TagString:
		return StringConstant(s), true
	case parser.TagInteger, parser.TagFloat:
	default:
		return Constant{}, false
	}
	unsigned := s
	negative := false
	if len(unsigned) > 0 && (unsigned[0] == '+' || unsigned[0] == '-') {
		negative = unsigned[0] == '-'
		unsigned = unsigned[1:]
	}
	if len(unsigned) == 0 {
		return Constant{}, false
	}
	hex := len(unsigned) >= 2 && unsigned[0] == '0' && (unsigned[1] == 'x' || unsigned[1] == 'X')
	// ParseFloat accepts Go-only spellings (underscores, Inf, NaN).
	for _, c := range unsigned {
		if !strings.ContainsRune("0123456789abcdefABCDEFxXpP.+-", c) {
			return Constant{}, false
		}
	}
	if kind == parser.TagInteger {
		if hex {
			var value uint64
			digits := unsigned[2:]
			if len(digits) == 0 {
				return Constant{}, false
			}
			for _, c := range digits {
				var digit uint64
				switch {
				case c >= '0' && c <= '9':
					digit = uint64(c - '0')
				case c >= 'a' && c <= 'f':
					digit = uint64(c-'a') + 10
				case c >= 'A' && c <= 'F':
					digit = uint64(c-'A') + 10
				default:
					return Constant{}, false
				}
				value = value*16 + digit
			}
			if negative {
				value = 0 - value
			}
			return IntConstant(int64(value)), true
		}
		for _, c := range unsigned {
			if c < '0' || c > '9' {
				return Constant{}, false
			}
		}
		if value, err := strconv.ParseInt(s, 10, 64); err == nil {
			return IntConstant(value), true
		}
	}
	if hex && !strings.ContainsAny(s, "pP") {
		s += "p0" // Lua permits a hexadecimal fraction without an exponent.
	}
	value, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if e, ok := err.(*strconv.NumError); !ok || e.Err != strconv.ErrRange {
			return Constant{}, false
		}
	}
	return FloatConstant(value), true
}

func expressionIsOpen(kind parser.Tag) bool {
	switch kind {
	case parser.TagCallExpression, parser.TagMethodCallExpression,
		parser.TagTableArgumentCall, parser.TagStringArgumentCall, parser.TagVararg:
		return true
	}
	return false
}

// expressionRegister borrows a current local or evaluates into a fresh temporary.
// Borrowed registers are read-only here: later operand evaluation may mutate
// them through a closure, and Lua reads their value when the opcode executes.
// The enclosing expr releases only temporaries, never the borrowed locals.
func (f *functionCompiler) expressionRegister(node uint32) int {
	if reg, ok := f.localRegister(node); ok {
		return reg
	}
	reg := f.free
	f.expr(node, reg, 1)
	return reg
}

// expr owns only its destination range and registers at/above entryFree.
// An open result must start at the free top or the last reserved register;
// its caller must immediately emit the instruction consuming the VM top.
func (f *functionCompiler) expr(node uint32, dst int, want int) {
	entryFree := f.free
	if node == 0 || int(node) >= len(f.ast.Nodes) {
		f.fail(node, "missing expression")
	}
	if want < -1 || want > MaxArgC-1 || dst < 0 {
		f.fail(node, "invalid expression result range")
	}
	n := f.ast.Nodes[node]
	if want == -1 && (!expressionIsOpen(n.Kind) || dst < entryFree-1) {
		f.fail(node, "open expression requires a call or vararg at the register top")
	}
	top := max(entryFree, dst+max(want, 1))
	f.ensure(top, node)

	switch n.Kind {
	case parser.TagNil, parser.TagBoolean, parser.TagInteger, parser.TagFloat, parser.TagString:
		c, ok := literalConstant(f.ast, node)
		if !ok {
			f.fail(node, "malformed literal")
		}
		f.load(dst, c, node)
	case parser.TagNameReference:
		f.readName(node, dst)
	case parser.TagFunctionExpression:
		f.closure(node, dst)
	case parser.TagParenthesized:
		f.expr(n.Left, dst, 1)
	case parser.TagVararg:
		if !f.proto.IsVararg {
			f.fail(node, "cannot use '...' outside a vararg function")
		}
		f.emit(ABC(OpVARARG, dst, int(f.proto.NumParams), want+1, f.proto.VarargTable), node)
	case parser.TagCallExpression, parser.TagMethodCallExpression,
		parser.TagTableArgumentCall, parser.TagStringArgumentCall:
		f.expressionCall(node, dst, want, entryFree)
	case parser.TagFieldAccess, parser.TagIndexAccess:
		if f.hiddenVararg(n.Left) {
			var key int
			if n.Kind == parser.TagFieldAccess {
				key = f.reserve(1, node)
				f.load(key, StringConstant(f.ast.Values[n.Right]), node)
			} else {
				key = f.expressionRegister(n.Right)
			}
			// GETVARG ignores B; retain the named slot as in Lua's compiler.
			f.emit(ABC(OpGETVARG, dst, int(f.proto.NumParams), key, false), node)
		} else if up, ok := f.directUpvalue(n.Left); ok {
			var key int
			c, literal := literalConstant(f.ast, n.Right)
			if n.Kind == parser.TagFieldAccess {
				c, literal = StringConstant(f.ast.Values[n.Right]), true
			}
			if literal && c.Kind == ConstantString && len(c.String) <= 40 {
				if index := f.constant(c, node); index <= MaxArgC {
					f.emit(ABC(OpGETTABUP, dst, up.index, index, false), node)
					break
				}
			}
			if n.Kind == parser.TagFieldAccess {
				key = f.reserve(1, node)
				f.load(key, c, node)
			} else {
				key = f.expressionRegister(n.Right)
			}
			// Lua retains the upvalue descriptor until after key evaluation.
			object := f.reserve(1, node)
			f.readVariable(up, object, node)
			f.emit(ABC(OpGETTABLE, dst, object, key, false), node)
		} else {
			object := f.expressionRegister(n.Left)
			if n.Kind == parser.TagFieldAccess {
				f.expressionField(dst, object, f.ast.Values[n.Right], node)
			} else {
				key := f.expressionRegister(n.Right)
				f.emit(ABC(OpGETTABLE, dst, object, key, false), node)
			}
		}
	case parser.TagTableConstructor:
		f.expressionTable(node, dst)
	case parser.TagNot, parser.TagNegate, parser.TagBitwiseNot, parser.TagLength:
		operand := f.expressionRegister(n.Left)
		op := OpNOT
		switch n.Kind {
		case parser.TagNegate:
			op = OpUNM
		case parser.TagBitwiseNot:
			op = OpBNOT
		case parser.TagLength:
			op = OpLEN
		}
		f.emit(ABC(op, dst, operand, 0, false), node)
	case parser.TagAnd, parser.TagOr:
		// Do not write dst before the right operand: it may read/capture dst.
		value := f.reserve(1, node)
		f.expr(n.Left, value, 1)
		f.emit(ABC(OpTEST, value, 0, 0, n.Kind == parser.TagOr), node)
		done := f.jump(node)
		f.expr(n.Right, value, 1)
		f.patch(done, len(f.proto.Code), node)
		f.move(dst, value, node)
	default:
		f.expressionBinary(node, dst)
	}
	if !expressionIsOpen(n.Kind) && want > 1 {
		f.emit(ABC(OpLOADNIL, dst+1, want-2, 0, false), node)
	}
	f.free = top
}

// condition returns an unpatched jump taken iff the value's truthiness equals
// truth. No expression temporaries remain reserved on return.
func (f *functionCompiler) condition(node uint32, truth bool) int {
	entryFree := f.free
	f.expr(node, entryFree, 1)
	f.emit(ABC(OpTEST, entryFree, 0, 0, truth), node)
	pc := f.jump(node)
	f.free = entryFree
	return pc
}

func (f *functionCompiler) expressionBinary(node uint32, dst int) {
	n := f.ast.Nodes[node]
	var op OpCode
	switch n.Kind {
	case parser.TagAdd:
		op = OpADD
	case parser.TagSubtract:
		op = OpSUB
	case parser.TagMultiply:
		op = OpMUL
	case parser.TagModulo:
		op = OpMOD
	case parser.TagPower:
		op = OpPOW
	case parser.TagDivide:
		op = OpDIV
	case parser.TagIntegerDivide:
		op = OpIDIV
	case parser.TagBitAnd:
		op = OpBAND
	case parser.TagBitOr:
		op = OpBOR
	case parser.TagBitXor:
		op = OpBXOR
	case parser.TagShiftLeft:
		op = OpSHL
	case parser.TagShiftRight:
		op = OpSHR
	case parser.TagEqual, parser.TagNotEqual:
		op = OpEQ
	case parser.TagLess, parser.TagGreater:
		op = OpLT
	case parser.TagLessEqual, parser.TagGreaterEqual:
		op = OpLE
	case parser.TagConcat:
		op = OpCONCAT
	default:
		f.fail(node, "unsupported expression kind")
		return
	}
	if op == OpCONCAT {
		// Unlike arithmetic, CONCAT consumes contiguous, writable temporaries.
		// Lua snapshots the left operand before evaluating the right here.
		left := f.free
		f.expr(n.Left, left, 1)
		f.expr(n.Right, left+1, 1)
		f.emit(ABC(OpCONCAT, left, 2, 0, false), node)
		f.move(dst, left, node)
		return
	}
	if f.expressionArithmeticConstant(node, dst, op) {
		return
	}
	left := f.expressionRegister(n.Left)
	right := f.expressionRegister(n.Right)
	switch op {
	case OpEQ, OpLT, OpLE:
		if n.Kind == parser.TagGreater || n.Kind == parser.TagGreaterEqual {
			left, right = right, left
		}
		f.emit(ABC(op, left, right, 0, n.Kind != parser.TagNotEqual), node)
		isTrue := f.jump(node)
		f.emit(ABC(OpLFALSESKIP, dst, 0, 0, false), node)
		f.patch(isTrue, len(f.proto.Code), node)
		f.emit(ABC(OpLOADTRUE, dst, 0, 0, false), node)
	default:
		f.emit(ABC(op, dst, left, right, false), node)
		// ltm.h: TM_ADD=6; arithmetic opcodes and events share their order.
		f.emit(ABC(OpMMBIN, left, right, 6+int(op-OpADD), false), node)
	}
}

// Only specialize a literal on the right: swapping operands would change
// metamethod argument order. Do not coerce float constants to integers (even
// integral floats), since both the numeric result and fallback can expose it.
func (f *functionCompiler) expressionArithmeticConstant(node uint32, dst int, op OpCode) bool {
	if op < OpADD || op > OpBXOR {
		return false
	}
	n := f.ast.Nodes[node]
	c, ok := literalConstant(f.ast, n.Right)
	if !ok || (c.Kind != ConstantInteger && c.Kind != ConstantFloat) {
		return false
	}
	if op >= OpBAND && c.Kind != ConstantInteger {
		return false // The bitwise K instructions require an integer constant.
	}
	event := 6 + int(op-OpADD)
	if value := int64(c.Bits); op == OpADD && c.Kind == ConstantInteger && value >= -127 && value <= 128 {
		left := f.expressionRegister(n.Left)
		immediate := int(value) + 127
		f.emit(ABC(OpADDI, dst, left, immediate, false), node)
		f.emit(ABC(OpMMBINI, left, immediate, event, false), node)
		return true
	}
	index := f.constant(c, node)
	if index > MaxArgC {
		return false
	}
	left := f.expressionRegister(n.Left)
	f.emit(ABC(OpADDK+(op-OpADD), dst, left, index, false), node)
	f.emit(ABC(OpMMBINK, left, index, event, false), node)
	return true
}

func (f *functionCompiler) expressionField(dst, object int, name string, node uint32) {
	c := StringConstant(name)
	if len(name) <= 40 {
		if index := f.constant(c, node); index <= MaxArgC {
			f.emit(ABC(OpGETFIELD, dst, object, index, false), node)
			return
		}
	}
	key := f.reserve(1, node)
	f.load(key, c, node)
	f.emit(ABC(OpGETTABLE, dst, object, key, false), node)
}

func (f *functionCompiler) expressionCall(node uint32, dst, want, entryFree int) {
	n := f.ast.Nodes[node]
	base := dst
	// Fixed calls may clobber their entire frame, not just their results.
	// A previously reserved dst can also be a local read by an argument.
	if want != -1 && dst < entryFree {
		base = f.reserve(max(want, 1), node)
	}
	f.ensure(base+max(want, 1), node)
	// The result window is ours; arguments reuse it before CALL fills it.
	f.free = base + 1
	next := base + 1
	if n.Kind == parser.TagMethodCallExpression {
		pair := f.ast.Nodes[n.Left]
		f.ensure(base+2, node)
		f.expr(pair.Left, base+1, 1)
		name := f.ast.Values[pair.Right]
		index := -1
		if len(name) <= 40 {
			index = f.constant(StringConstant(name), node)
		}
		if index >= 0 && index <= MaxArgC {
			f.emit(ABC(OpSELF, base, base+1, index, false), node)
		} else {
			f.expressionField(base, base+1, name, node)
		}
		next++
		f.free = next
	} else {
		f.expr(n.Left, base, 1)
	}
	open := false
	for cell := n.Right; cell != 0; cell = f.ast.Nodes[cell].Right {
		item := f.ast.Nodes[cell]
		open = item.Right == 0 && expressionIsOpen(f.ast.Nodes[item.Left].Kind)
		count := 1
		if open {
			count = -1
		}
		f.expr(item.Left, next, count)
		next++
	}
	argc := next - base
	if open {
		argc = 0
	}
	if argc > MaxArgB {
		f.fail(node, "too many function arguments")
	}
	f.emit(ABC(OpCALL, base, argc, want+1, false), node)
	if base != dst {
		for i := 0; i < want; i++ {
			f.move(dst+i, base+i, node)
		}
	}
}

func (f *functionCompiler) expressionTable(node uint32, dst int) {
	fields := f.ast.Nodes[node].Left
	arrays, keys := 0, 0
	for cell := fields; cell != 0; cell = f.ast.Nodes[cell].Right {
		if f.ast.Nodes[f.ast.Nodes[cell].Left].Kind == parser.TagArrayField {
			arrays++
		} else {
			keys++
		}
	}
	base := f.reserve(1, node)
	hash := 0
	if keys > 0 {
		hash = bits.Len(uint(keys-1)) + 1
	}
	extra := arrays / (MaxArgVC + 1)
	if uint64(arrays) > uint64(1<<32-1) || extra > MaxArgAx || hash > 32 {
		f.fail(node, "table constructor too large")
	}
	f.emit(VABC(OpNEWTABLE, base, hash, arrays%(MaxArgVC+1), extra != 0), node)
	f.emit(Ax(OpEXTRAARG, extra), node) // NEWTABLE always consumes this word.
	// Match Lua's maxtostore policy, leaving room for nested field expressions.
	batch := 1
	available := MaxStack - f.free
	if available >= 160 {
		batch = available / 5
	} else if available >= 80 {
		batch = 10
	}
	pending, offset := 0, 0
	for cell := fields; cell != 0; cell = f.ast.Nodes[cell].Right {
		fieldNode := f.ast.Nodes[cell].Left
		field := f.ast.Nodes[fieldNode]
		if field.Kind == parser.TagArrayField {
			open := f.ast.Nodes[cell].Right == 0 && expressionIsOpen(f.ast.Nodes[field.Left].Kind)
			want := 1
			if open {
				want = -1
			}
			f.expr(field.Left, base+1+pending, want)
			pending++
			if open {
				f.expressionSetList(base, 0, offset, fieldNode)
				pending = 0
			} else if pending == batch {
				f.expressionSetList(base, pending, offset, fieldNode)
				offset += pending
				pending = 0
			}
			if pending == 0 {
				f.free = base + 1
			}
			continue
		}
		// Keyed fields execute before the pending SETLIST. Keep its values
		// reserved while evaluating the key and value above the list buffer.
		f.free = base + 1 + pending
		var key int
		switch field.Kind {
		case parser.TagNameField:
			key = f.reserve(1, fieldNode)
			f.load(key, StringConstant(f.ast.Values[field.Left]), fieldNode)
		case parser.TagKeyField:
			key = f.expressionRegister(field.Left)
		default:
			f.fail(fieldNode, "invalid table constructor field")
		}
		value := f.expressionRegister(field.Right)
		f.emit(ABC(OpSETTABLE, base, key, value, false), fieldNode)
		f.free = base + 1 + pending
	}
	if pending != 0 {
		f.expressionSetList(base, pending, offset, node)
	}
	f.move(dst, base, node)
}

func (f *functionCompiler) expressionSetList(base, count, offset int, node uint32) {
	extra := offset / (MaxArgVC + 1)
	if extra > MaxArgAx {
		f.fail(node, "table constructor offset too large")
	}
	f.emit(VABC(OpSETLIST, base, count, offset%(MaxArgVC+1), extra != 0), node)
	if extra != 0 {
		f.emit(Ax(OpEXTRAARG, extra), node)
	}
}
