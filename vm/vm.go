// Package vm executes the compiler's Lua 5.5.1 prototypes in Go.
package vm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/robogg133/glua/compile"
)

const MultRet = -1

func canonicalError(err error) error {
	var object *LuaError
	if errors.As(err, &object) {
		return LuaError{Value: object.Value}
	}
	return err
}

// State owns globals and the public API stack. It is not safe for concurrent
// use. Registers and PC expose the last root frame for diagnostics, not the API
// stack. Go's garbage collector manages Lua tables, closures and captured values.
type State struct {
	Registers      []any
	PC             int
	Globals        *Table
	TypeMetatables map[string]*Table
	Output         io.Writer
	Context        context.Context
	// MaxSteps limits instructions across a Run/Invoke/Call (zero is unlimited).
	MaxSteps    uint64
	stack       []any
	steps       uint64
	depth       int
	initialized bool
	cleaning    bool
}

func NewState() *State { s := &State{}; s.ensureInit(); return s }
func (s *State) ensureInit() {
	if s.initialized {
		return
	}
	if s.Output == nil {
		s.Output = os.Stdout
	}
	if s.Globals == nil {
		s.Globals = NewTable()
	}
	if err := s.OpenLibraries(); err != nil {
		panic(err)
	}
	s.initialized = true
}
func (s *State) Push(value any)       { s.stack = append(s.stack, normalizeValue(value)) }
func (s *State) PushNil()             { s.Push(nil) }
func (s *State) PushBoolean(v bool)   { s.Push(v) }
func (s *State) PushInteger(v int64)  { s.Push(v) }
func (s *State) PushNumber(v float64) { s.Push(v) }
func (s *State) PushString(v string)  { s.Push(v) }
func (s *State) GetTop() int          { return len(s.stack) }
func (s *State) index(index int) int {
	if index < 0 {
		return len(s.stack) + index
	}
	return index - 1
}

// At uses Lua's one-based indices; -1 is the top. Invalid indices return nil.
func (s *State) At(index int) any {
	i := s.index(index)
	if index == 0 || i < 0 || i >= len(s.stack) {
		return nil
	}
	return s.stack[i]
}
func (s *State) Pop(n int) error {
	if n < 0 || n > len(s.stack) {
		return fmt.Errorf("vm: invalid pop count %d", n)
	}
	return s.SetTop(len(s.stack) - n)
}

// SetTop extends with nil or discards values. Negative indices are relative to
// the current top, as in lua_settop; SetTop(-2) removes one value.
func (s *State) SetTop(index int) error {
	top := index
	if index < 0 {
		top = len(s.stack) + index + 1
	}
	if top < 0 {
		return fmt.Errorf("vm: invalid stack top %d", index)
	}
	if top < len(s.stack) {
		clear(s.stack[top:])
		s.stack = s.stack[:top]
	} else {
		s.stack = append(s.stack, make([]any, top-len(s.stack))...)
	}
	return nil
}

// Environment returns the live global table, initializing a zero-value State
// without changing its API stack. Access through the table is raw host access.
func (s *State) Environment() *Table { s.ensureInit(); return s.Globals }

func (s *State) GetGlobal(name string) error {
	s.ensureInit()
	v, err := s.get(s.Globals, name)
	if err == nil {
		s.Push(v)
	}
	return err
}
func (s *State) SetGlobal(name string) error {
	s.ensureInit()
	if len(s.stack) == 0 {
		return fmt.Errorf("vm: empty stack")
	}
	if err := s.set(s.Globals, name, s.At(-1)); err != nil {
		return err
	}
	return s.Pop(1)
}

// Call consumes the function below nargs arguments and replaces them with
// nresults results (nil-padded); MultRet requests all results. On error the
// function/arguments are removed and the preceding stack remains intact.
func (s *State) Call(nargs, nresults int) error {
	if nargs < 0 || nargs >= len(s.stack) || nresults < MultRet {
		return fmt.Errorf("vm: invalid call counts")
	}
	s.ensureInit()
	base := len(s.stack) - nargs - 1
	fn := s.stack[base]
	args := append([]any(nil), s.stack[base+1:]...)
	_ = s.SetTop(base)
	results, err := s.Invoke(fn, args...)
	if err != nil {
		return err
	}
	if nresults == MultRet {
		nresults = len(results)
	}
	for i := 0; i < nresults; i++ {
		var v any
		if i < len(results) {
			v = results[i]
		}
		s.Push(v)
	}
	return nil
}

// Invoke calls a Lua closure, native function or __call object without changing
// the public stack. Native functions return Lua results separately from errors.
func (s *State) Invoke(fn any, args ...any) ([]any, error) {
	s.ensureInit()
	if s.depth == 0 {
		s.steps = 0
	}
	arguments := make([]any, len(args))
	for i, v := range args {
		arguments[i] = normalizeValue(v)
	}
	return s.call(normalizeValue(fn), arguments)
}

// Run executes a chunk with this State's _ENV and replaces the public stack with
// its return values. Globals persist across runs. The prototype must remain
// immutable while it or closures returned from it are in use.
func (s *State) Run(proto *compile.Prototype) error {
	s.ensureInit()
	if s.depth != 0 {
		return fmt.Errorf("vm: Run cannot replace an active root frame; use Invoke")
	}
	_ = s.SetTop(0)
	s.steps = 0
	s.PC = 0
	s.Registers = nil
	closure, err := s.rootClosure(proto)
	if err != nil {
		return err
	}
	results, err := s.execute(closure, nil, true)
	if err == nil {
		for _, v := range results {
			s.Push(v)
		}
	}
	return err
}

// Load pushes a compiled chunk as a callable closure without executing it.
func (s *State) Load(proto *compile.Prototype) error {
	s.ensureInit()
	closure, err := s.rootClosure(proto)
	if err != nil {
		return err
	}
	s.Push(closure)
	return nil
}
func (s *State) rootClosure(proto *compile.Prototype) (*Closure, error) {
	if err := checkPrototype(proto); err != nil {
		return nil, err
	}
	closure := &Closure{proto: proto, upvalues: make([]*upvalue, len(proto.Upvalues))}
	for i, u := range proto.Upvalues {
		if i != 0 || u.Name != "_ENV" {
			return nil, fmt.Errorf("vm: root upvalue %d is not _ENV", i)
		}
		closure.upvalues[i] = &upvalue{value: s.Globals}
	}
	return closure, nil
}

// Closure binds a prototype to shared mutable upvalues.
type Closure struct {
	proto    *compile.Prototype
	upvalues []*upvalue
}
type upvalue struct {
	frame *frame
	index int
	value any
}

func (u *upvalue) get() any {
	if u.frame != nil {
		return u.frame.regs[u.index]
	}
	return u.value
}
func (u *upvalue) set(v any) {
	if u.frame != nil {
		u.frame.regs[u.index] = v
	} else {
		u.value = v
	}
}

type frame struct {
	closure          *Closure
	regs             []any
	pc, current, top int
	varargs          []any
	open             map[int]*upvalue
	closing          []int
}

func (s *State) call(fn any, args []any) ([]any, error) {
	if s.Context != nil {
		if err := s.Context.Err(); err != nil {
			return nil, err
		}
	}
	for chain := 0; chain < 200; chain++ {
		switch f := fn.(type) {
		case *nativeFunction:
			if f == nil {
				return nil, valueError("attempt to call a nil function")
			}
			fn = f.function
			continue
		case NativeFunction:
			if f == nil {
				return nil, valueError("attempt to call a nil function")
			}
			if s.depth >= 1000 {
				return nil, valueError("stack overflow")
			}
			return s.callNative(f, args)
		case *Closure:
			if f == nil {
				return nil, valueError("attempt to call a nil function")
			}
			return s.execute(f, args, false)
		default:
			mm := s.metamethod(fn, "__call")
			if mm == nil {
				return nil, valueError("attempt to call a %s value", typeName(fn))
			}
			args = append([]any{fn}, args...)
			fn = mm
		}
	}
	return nil, valueError("'__call' chain too long; possible loop")
}

func (s *State) callNative(fn NativeFunction, args []any) (results []any, err error) {
	callerStack := s.stack
	// Give native code its own Lua API frame; SetTop/Pop must not clear the
	// caller's backing array, including when the native function returns an error.
	s.stack = append([]any(nil), args...)
	s.depth++
	defer func() { s.stack = callerStack; s.depth-- }()
	results, err = fn(s, args)
	for i, v := range results {
		results[i] = normalizeValue(v)
	}
	return results, canonicalError(err)
}

type vmFault struct{ err error }

func fault(format string, args ...any) { panic(vmFault{fmt.Errorf("vm: "+format, args...)}) }
func require(err error) {
	if err != nil {
		panic(vmFault{err})
	}
}
func checkPrototype(p *compile.Prototype) error {
	if p == nil {
		return fmt.Errorf("vm: nil prototype")
	}
	if p.MaxStackSize < 2 {
		return fmt.Errorf("vm: invalid stack size %d", p.MaxStackSize)
	}
	if p.NumParams > p.MaxStackSize || p.VarargTable && !p.IsVararg {
		return fmt.Errorf("vm: invalid function metadata")
	}
	if p.IsVararg && int(p.NumParams) >= int(p.MaxStackSize) {
		return fmt.Errorf("vm: missing vararg register")
	}
	return nil
}
func (f *frame) reg(index int) any {
	if index < 0 || index >= int(f.closure.proto.MaxStackSize) {
		fault("register R[%d] outside frame", index)
	}
	return f.regs[index]
}
func (f *frame) put(index int, v any) {
	if index < 0 || index >= int(f.closure.proto.MaxStackSize) {
		fault("register R[%d] outside frame", index)
	}
	f.regs[index] = v
}
func (f *frame) constant(index int) any {
	if index < 0 || index >= len(f.closure.proto.Constants) {
		fault("constant %d outside pool", index)
	}
	c := f.closure.proto.Constants[index]
	switch c.Kind {
	case compile.ConstantNil:
		return nil
	case compile.ConstantBoolean:
		if c.Bits > 1 {
			fault("invalid boolean constant")
		}
		return c.Bits != 0
	case compile.ConstantInteger:
		return int64(c.Bits)
	case compile.ConstantFloat:
		return floatBits(c.Bits)
	case compile.ConstantString:
		return c.String
	default:
		fault("invalid constant kind %d", c.Kind)
	}
	return nil
}
func (f *frame) up(index int) *upvalue {
	if index < 0 || index >= len(f.closure.upvalues) || f.closure.upvalues[index] == nil {
		fault("upvalue %d outside closure", index)
	}
	return f.closure.upvalues[index]
}
func (f *frame) extra() int {
	if f.pc >= len(f.closure.proto.Code) || f.closure.proto.Code[f.pc].Op() != compile.OpEXTRAARG {
		fault("missing EXTRAARG")
	}
	v := int(f.closure.proto.Code[f.pc].Ax())
	f.pc++
	return v
}
func (f *frame) jump(target int) {
	if target < 0 || target >= len(f.closure.proto.Code) {
		fault("jump target %d outside code", target)
	}
	if f.closure.proto.Code[target].Op() == compile.OpEXTRAARG {
		fault("jump into EXTRAARG")
	}
	f.pc = target
}
func (f *frame) results(base, count int) []any {
	if count < 0 {
		count = f.top - base
	}
	if base < 0 || count < 0 || base+count > len(f.regs) {
		fault("invalid result/argument range")
	}
	return append([]any(nil), f.regs[base:base+count]...)
}
func (f *frame) storeResults(base, count int, results []any) {
	if count < 0 {
		count = len(results)
	}
	if base < 0 || base+count < base || base+count > 1<<24 {
		fault("too many results")
	}
	if base+count > len(f.regs) {
		f.regs = append(f.regs, make([]any, base+count-len(f.regs))...)
	}
	for i := 0; i < count; i++ {
		var v any
		if i < len(results) {
			v = results[i]
		}
		f.regs[base+i] = v
	}
	f.top = base + count
}
func (s *State) markClose(f *frame, index int) error {
	v := f.reg(index)
	if v == nil || v == false {
		return nil
	}
	if s.metamethod(v, "__close") == nil {
		return valueError("variable got a non-closable value")
	}
	if len(f.closing) > 0 && index <= f.closing[len(f.closing)-1] {
		return fmt.Errorf("vm: unordered to-be-closed register")
	}
	f.closing = append(f.closing, index)
	return nil
}
func (s *State) closeFrame(f *frame, level int, cause error) error {
	savedContext, savedLimit := s.Context, s.MaxSteps
	cleanup := false
	defer func() {
		if cleanup {
			s.Context, s.MaxSteps, s.cleaning = savedContext, savedLimit, false
		}
	}()
	// Closing captured values first preserves their values while __close executes
	// Lua code; closing errors replace the active error and still close the rest.
	for index, u := range f.open {
		if index >= level {
			u.value = u.get()
			u.frame = nil
			delete(f.open, index)
		}
	}
	for len(f.closing) > 0 && f.closing[len(f.closing)-1] >= level {
		index := f.closing[len(f.closing)-1]
		f.closing = f.closing[:len(f.closing)-1]
		v := f.reg(index)
		var errorObject any
		if cause != nil {
			errorObject = libErrorValue(cause)
		}
		if !s.cleaning && (s.Context != nil && s.Context.Err() != nil || s.MaxSteps != 0 && s.steps >= s.MaxSteps) {
			// Cancellation must still release resources. Cleanup has its own
			// bounded instruction allowance; it cannot restart the user's loop.
			if s.Context != nil {
				s.Context = context.WithoutCancel(s.Context)
			}
			s.MaxSteps, s.cleaning, cleanup = s.steps+10000, true, true
		}
		if _, err := s.call(s.metamethod(v, "__close"), []any{v, errorObject}); err != nil {
			cause = err
		}
	}
	return cause
}
