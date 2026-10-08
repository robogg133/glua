package vm

import (
	"fmt"
	"math"

	"github.com/robogg133/glua/compile"
)

func floatBits(bits uint64) float64 { return math.Float64frombits(bits) }

func (s *State) execute(closure *Closure, args []any, root bool) (results []any, err error) {
	if s.depth >= 1000 {
		return nil, valueError("stack overflow")
	}
	s.depth++
	var f *frame
	defer func() {
		if r := recover(); r != nil {
			if failure, ok := r.(vmFault); ok {
				err = failure.err
			} else {
				panic(r)
			}
		}
		if f != nil {
			if err != nil {
				err = s.closeFrame(f, 0, err)
			}
			if root {
				s.Registers = f.regs[:int(f.closure.proto.MaxStackSize)]
				s.PC = f.pc
				if err != nil {
					s.PC = f.current
				}
			}
			if err != nil {
				err = fmt.Errorf("vm: pc %d: %w", f.current, canonicalError(err))
			}
		}
		s.depth--
	}()
frames:
	for {
		require(checkPrototype(closure.proto))
		p := closure.proto
		f = &frame{closure: closure, regs: make([]any, int(p.MaxStackSize)), open: make(map[int]*upvalue)}
		for i := 0; i < int(p.NumParams) && i < len(args); i++ {
			f.regs[i] = args[i]
		}
		if p.IsVararg && len(args) > int(p.NumParams) {
			f.varargs = append([]any(nil), args[int(p.NumParams):]...)
		}
		f.top = int(p.NumParams)
		if p.IsVararg {
			f.top++
		}
		for f.pc < len(p.Code) {
			f.current = f.pc
			if s.Context != nil {
				require(s.Context.Err())
			}
			if s.MaxSteps != 0 && s.steps >= s.MaxSteps {
				fault("instruction limit exceeded")
			}
			s.steps++
			i := p.Code[f.pc]
			f.pc++
			a, b, c := int(i.A()), int(i.B()), int(i.C())
			rk := func() any {
				if i.K() != 0 {
					return f.constant(c)
				}
				return f.reg(c)
			}
			switch op := i.Op(); op {
			case compile.OpMOVE:
				f.put(a, f.reg(b))
			case compile.OpLOADI:
				f.put(a, int64(i.SBx()))
			case compile.OpLOADF:
				f.put(a, float64(i.SBx()))
			case compile.OpLOADK:
				f.put(a, f.constant(int(i.Bx())))
			case compile.OpLOADKX:
				f.put(a, f.constant(f.extra()))
			case compile.OpLOADFALSE:
				f.put(a, false)
			case compile.OpLFALSESKIP:
				f.put(a, false)
				f.pc++
			case compile.OpLOADTRUE:
				f.put(a, true)
			case compile.OpLOADNIL:
				for index := a; index <= a+b; index++ {
					f.put(index, nil)
				}
			case compile.OpGETUPVAL:
				f.put(a, f.up(b).get())
			case compile.OpSETUPVAL:
				f.up(b).set(f.reg(a))
			case compile.OpGETTABUP:
				v, e := s.get(f.up(b).get(), f.constant(c))
				require(e)
				f.put(a, v)
			case compile.OpGETTABLE:
				v, e := s.get(f.reg(b), f.reg(c))
				require(e)
				f.put(a, v)
			case compile.OpGETI:
				v, e := s.get(f.reg(b), int64(c))
				require(e)
				f.put(a, v)
			case compile.OpGETFIELD:
				v, e := s.get(f.reg(b), f.constant(c))
				require(e)
				f.put(a, v)
			case compile.OpSETTABUP:
				require(s.set(f.up(a).get(), f.constant(b), rk()))
			case compile.OpSETTABLE:
				require(s.set(f.reg(a), f.reg(b), rk()))
			case compile.OpSETI:
				require(s.set(f.reg(a), int64(b), rk()))
			case compile.OpSETFIELD:
				require(s.set(f.reg(a), f.constant(b), rk()))
			case compile.OpNEWTABLE:
				// Go maps grow on demand; consume Lua's mandatory size word without
				// allocating attacker-controlled hints. Size hints do not affect values.
				f.extra()
				f.put(a, NewTable())
			case compile.OpSELF:
				receiver := f.reg(b)
				f.put(a+1, receiver)
				v, e := s.get(receiver, f.constant(c))
				require(e)
				f.put(a, v)
			case compile.OpADDI, compile.OpADDK, compile.OpSUBK, compile.OpMULK, compile.OpMODK, compile.OpPOWK, compile.OpDIVK, compile.OpIDIVK, compile.OpBANDK, compile.OpBORK, compile.OpBXORK, compile.OpSHLI, compile.OpSHRI,
				compile.OpADD, compile.OpSUB, compile.OpMUL, compile.OpMOD, compile.OpPOW, compile.OpDIV, compile.OpIDIV, compile.OpBAND, compile.OpBOR, compile.OpBXOR, compile.OpSHL, compile.OpSHR:
				operation := op
				left := f.reg(b)
				var right any
				companion := compile.OpMMBIN
				switch {
				case op == compile.OpADDI:
					operation = compile.OpADD
					right = int64(c - 127)
					companion = compile.OpMMBINI
				case op == compile.OpSHLI:
					operation = compile.OpSHL
					right = left
					left = int64(c - 127)
					companion = compile.OpMMBINI
				case op == compile.OpSHRI:
					operation = compile.OpSHR
					right = int64(c - 127)
					companion = compile.OpMMBINI
				case op >= compile.OpADDK && op <= compile.OpBXORK:
					operation = compile.OpADD + (op - compile.OpADDK)
					right = f.constant(c)
					companion = compile.OpMMBINK
				default:
					right = f.reg(c)
				}
				if f.pc >= len(p.Code) || p.Code[f.pc].Op() != companion {
					fault("missing arithmetic metamethod fallback")
				}
				fallback := p.Code[f.pc]
				// Preserve the fallback's original operands/event (e.g. x - n optimized
				// as ADDI with -n, or swapped operands in a commutative fast path).
				if !primitiveNumber(left) || !primitiveNumber(right) {
					left, right, operation = f.fallback(fallback)
				}
				v, e := s.arithmetic(operation, left, right)
				require(e)
				f.put(a, v)
				f.pc++
			case compile.OpMMBIN, compile.OpMMBINI, compile.OpMMBINK:
				if f.current == 0 {
					fault("orphan metamethod fallback")
				}
				original := p.Code[f.current-1]
				if original.Op() < compile.OpADDI || original.Op() > compile.OpSHR {
					fault("orphan metamethod fallback")
				}
				left, right, operation := f.fallback(i)
				v, e := s.arithmetic(operation, left, right)
				require(e)
				f.put(int(original.A()), v)
			case compile.OpUNM, compile.OpBNOT, compile.OpNOT, compile.OpLEN:
				v, e := s.unary(op, f.reg(b))
				require(e)
				f.put(a, v)
			case compile.OpCONCAT:
				if b < 2 {
					fault("invalid CONCAT count")
				}
				value := f.reg(a + b - 1)
				for index := a + b - 2; index >= a; index-- {
					v, e := s.concat(f.reg(index), value)
					require(e)
					value = v
				}
				f.put(a, value)
			case compile.OpCLOSE:
				require(s.closeFrame(f, a, nil))
			case compile.OpTBC:
				require(s.markClose(f, a))
			case compile.OpJMP:
				f.jump(f.pc + int(i.SJ()))
			case compile.OpEQ, compile.OpLT, compile.OpLE, compile.OpEQK, compile.OpEQI, compile.OpLTI, compile.OpLEI, compile.OpGTI, compile.OpGEI:
				if f.pc >= len(p.Code) || p.Code[f.pc].Op() != compile.OpJMP {
					fault("test requires JMP")
				}
				left := f.reg(a)
				var right any
				operation := op
				switch op {
				case compile.OpEQ, compile.OpLT, compile.OpLE:
					right = f.reg(b)
				case compile.OpEQK:
					operation = compile.OpEQ
					right = f.constant(b)
				case compile.OpEQI:
					operation = compile.OpEQ
					right = int64(b - 127)
				case compile.OpLTI:
					operation = compile.OpLT
					right = immediateComparison(b, c)
				case compile.OpLEI:
					operation = compile.OpLE
					right = immediateComparison(b, c)
				case compile.OpGTI:
					operation = compile.OpLT
					right = left
					left = immediateComparison(b, c)
				case compile.OpGEI:
					operation = compile.OpLE
					right = left
					left = immediateComparison(b, c)
				}
				v, e := s.compare(operation, left, right)
				require(e)
				if v != (i.K() != 0) {
					f.pc++
				}
			case compile.OpTEST, compile.OpTESTSET:
				if f.pc >= len(p.Code) || p.Code[f.pc].Op() != compile.OpJMP {
					fault("test requires JMP")
				}
				index := a
				if op == compile.OpTESTSET {
					index = b
				}
				if truth(f.reg(index)) != (i.K() != 0) {
					f.pc++
				} else if op == compile.OpTESTSET {
					f.put(a, f.reg(b))
				}
			case compile.OpCALL, compile.OpTAILCALL:
				fn := f.reg(a)
				argc := b - 1
				if b == 0 {
					argc = -1
				}
				arguments := f.results(a+1, argc)
				if op == compile.OpTAILCALL {
					require(s.closeFrame(f, 0, nil))
					if next, ok := fn.(*Closure); ok && next != nil {
						closure = next
						args = arguments
						continue frames
					}
					results, err = s.call(fn, arguments)
					return results, err
				}
				returned, e := s.call(fn, arguments)
				require(e)
				f.storeResults(a, c-1, returned)
			case compile.OpRETURN:
				results = f.results(a, b-1)
				if b == 0 {
					results = f.results(a, -1)
				}
				require(s.closeFrame(f, 0, nil))
				return results, nil
			case compile.OpRETURN0:
				require(s.closeFrame(f, 0, nil))
				return nil, nil
			case compile.OpRETURN1:
				results = []any{f.reg(a)}
				require(s.closeFrame(f, 0, nil))
				return results, nil
			case compile.OpFORPREP:
				skip, e := s.forPrep(f, a)
				require(e)
				if skip {
					f.jump(f.pc + int(i.Bx()) + 1)
				}
			case compile.OpFORLOOP:
				if step, ok := f.reg(a + 1).(int64); ok {
					count, ok := f.reg(a).(int64)
					if !ok {
						fault("invalid integer for counter")
					}
					index, ok := f.reg(a + 2).(int64)
					if !ok {
						fault("invalid integer for index")
					}
					if uint64(count) > 0 {
						f.put(a, int64(uint64(count)-1))
						f.put(a+2, index+step)
						f.jump(f.pc - int(i.Bx()))
					}
				} else {
					limit, ok := f.reg(a).(float64)
					if !ok {
						fault("invalid float for limit")
					}
					step, ok := f.reg(a + 1).(float64)
					if !ok {
						fault("invalid float for step")
					}
					index, ok := f.reg(a + 2).(float64)
					if !ok {
						fault("invalid float for index")
					}
					index += step
					if step > 0 && index <= limit || step <= 0 && limit <= index {
						f.put(a+2, index)
						f.jump(f.pc - int(i.Bx()))
					}
				}
			case compile.OpTFORPREP:
				control, closing := f.reg(a+2), f.reg(a+3)
				f.put(a+2, closing)
				f.put(a+3, control)
				require(s.markClose(f, a+2))
				f.jump(f.pc + int(i.Bx()))
			case compile.OpTFORCALL:
				if f.pc >= len(p.Code) || p.Code[f.pc].Op() != compile.OpTFORLOOP || p.Code[f.pc].A() != i.A() {
					fault("TFORCALL requires matching TFORLOOP")
				}
				returned, e := s.call(f.reg(a), []any{f.reg(a + 1), f.reg(a + 3)})
				require(e)
				f.storeResults(a+3, c, returned)
			case compile.OpTFORLOOP:
				if f.reg(a+3) != nil {
					f.jump(f.pc - int(i.Bx()))
				}
			case compile.OpSETLIST:
				table, ok := f.reg(a).(*Table)
				if !ok || table == nil {
					fault("SETLIST expects table")
				}
				count := int(i.VB())
				if count == 0 {
					count = f.top - a - 1
				}
				offset := int(i.VC())
				if i.K() != 0 {
					offset += f.extra() * (compile.MaxArgVC + 1)
				}
				values := f.results(a+1, count)
				for index, v := range values {
					require(table.RawSet(int64(offset+index+1), v))
				}
			case compile.OpCLOSURE:
				index := int(i.Bx())
				if index >= len(p.Children) {
					fault("child %d outside prototype", index)
				}
				child := &Closure{proto: &p.Children[index], upvalues: make([]*upvalue, len(p.Children[index].Upvalues))}
				require(checkPrototype(child.proto))
				for index, u := range child.proto.Upvalues {
					if u.InStack {
						register := int(u.Index)
						f.reg(register)
						if existing, ok := f.open[register]; ok {
							child.upvalues[index] = existing
						} else {
							captured := &upvalue{frame: f, index: register}
							f.open[register] = captured
							child.upvalues[index] = captured
						}
					} else {
						child.upvalues[index] = f.up(int(u.Index))
					}
				}
				f.put(a, child)
			case compile.OpVARARG:
				if !p.IsVararg {
					fault("VARARG in non-vararg function")
				}
				values := f.varargs
				if i.K() != 0 {
					table, ok := f.reg(b).(*Table)
					if !ok || table == nil {
						fault("missing vararg table")
					}
					n, e := s.get(table, "n")
					require(e)
					size, ok := toInteger(n)
					if !ok || size < 0 || size > 1<<24 {
						fault("invalid vararg count")
					}
					values = make([]any, int(size))
					for index := range values {
						v, e := s.get(table, int64(index+1))
						require(e)
						values[index] = v
					}
				}
				f.storeResults(a, c-1, values)
			case compile.OpGETVARG:
				if !p.IsVararg || p.VarargTable {
					fault("GETVARG requires hidden varargs")
				}
				key := f.reg(c)
				var value any
				if key == "n" {
					value = int64(len(f.varargs))
				} else {
					var index int64
					var ok bool
					switch x := key.(type) {
					case int64:
						index, ok = x, true
					case float64:
						index, ok = toInteger(x)
					}
					if ok && index > 0 && index <= int64(len(f.varargs)) {
						value = f.varargs[index-1]
					}
				}
				f.put(a, value)
			case compile.OpERRNNIL:
				if f.reg(a) != nil {
					name := "?"
					if i.Bx() != 0 {
						name = luaString(f.constant(int(i.Bx()) - 1))
					}
					require(valueError("global '%s' already defined", name))
				}
			case compile.OpVARARGPREP:
				if !p.IsVararg || f.current != 0 || uint32(i)>>7 != 0 {
					fault("invalid VARARGPREP")
				}
				if p.VarargTable {
					table := NewTable()
					for index, v := range f.varargs {
						require(table.RawSet(int64(index+1), v))
					}
					require(table.RawSet("n", int64(len(f.varargs))))
					f.put(int(p.NumParams), table)
				}
			case compile.OpEXTRAARG:
				fault("orphan EXTRAARG")
			default:
				fault("unknown opcode %d", op)
			}
			if f.pc > len(p.Code) {
				fault("instruction skips outside code")
			}
		}
		// Permit small hand-built instruction fragments without a RETURN, while
		// compiled chunks use explicit returns and always close their frame.
		require(s.closeFrame(f, 0, nil))
		return nil, nil
	}
}
func primitiveNumber(v any) bool {
	switch v.(type) {
	case int64, float64:
		return true
	}
	return false
}
func immediateComparison(b, c int) any {
	if c != 0 {
		return float64(b - 127)
	}
	return int64(b - 127)
}
func (f *frame) fallback(i compile.Instruction) (any, any, compile.OpCode) {
	event := int(i.C())
	if event < 6 || event > 17 {
		fault("invalid arithmetic metamethod event %d", event)
	}
	left := f.reg(int(i.A()))
	var right any
	switch i.Op() {
	case compile.OpMMBIN:
		right = f.reg(int(i.B()))
	case compile.OpMMBINI:
		right = int64(int(i.B()) - 127)
	case compile.OpMMBINK:
		right = f.constant(int(i.B()))
	default:
		fault("invalid arithmetic fallback")
	}
	if i.K() != 0 {
		left, right = right, left
	}
	return left, right, compile.OpADD + compile.OpCode(event-6)
}
func (s *State) forPrep(f *frame, a int) (bool, error) {
	initial, limitValue, stepValue := f.reg(a), f.reg(a+1), f.reg(a+2)
	init, integer := initial.(int64)
	step, integerStep := stepValue.(int64)
	if integer && integerStep {
		if step == 0 {
			return false, valueError("'for' step is zero")
		}
		limit, ok := limitValue.(int64)
		if !ok {
			n, ok := toNumber(limitValue)
			if !ok {
				return false, valueError("'for' limit must be a number")
			}
			if math.IsNaN(n) {
				return true, nil
			}
			if step > 0 {
				n = math.Floor(n)
			} else {
				n = math.Ceil(n)
			}
			switch {
			case n >= 0x1p63:
				if step < 0 {
					return true, nil
				}
				limit = math.MaxInt64
			case n < -0x1p63:
				if step > 0 {
					return true, nil
				}
				limit = math.MinInt64
			default:
				limit = int64(n)
			}
		}
		if step > 0 && init > limit || step < 0 && init < limit {
			return true, nil
		}
		var count uint64
		if step > 0 {
			count = (uint64(limit) - uint64(init)) / uint64(step)
		} else {
			count = (uint64(init) - uint64(limit)) / (uint64(-(step + 1)) + 1)
		}
		f.put(a, int64(count))
		f.put(a+1, step)
		f.put(a+2, init)
		return false, nil
	}
	limit, ok := toNumber(limitValue)
	if !ok {
		return false, valueError("'for' limit must be a number")
	}
	stepFloat, ok := toNumber(stepValue)
	if !ok {
		return false, valueError("'for' step must be a number")
	}
	initFloat, ok := toNumber(initial)
	if !ok {
		return false, valueError("'for' initial value must be a number")
	}
	if stepFloat == 0 {
		return false, valueError("'for' step is zero")
	}
	if stepFloat > 0 && limit < initFloat || stepFloat <= 0 && initFloat < limit {
		return true, nil
	}
	f.put(a, limit)
	f.put(a+1, stepFloat)
	f.put(a+2, initFloat)
	return false, nil
}
