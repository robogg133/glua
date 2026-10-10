// Package glua embeds Lua scripts through the parser, optional IR optimizer,
// compiler and Go VM. See vm/README.md for the VM's compatibility limits.
package glua

import (
	"fmt"
	"os"

	"github.com/robogg133/glua/compile"
	"github.com/robogg133/glua/ir"
	"github.com/robogg133/glua/lex"
	"github.com/robogg133/glua/parser"
	"github.com/robogg133/glua/tokens"
	"github.com/robogg133/glua/vm"
)

// Globals supplies predeclared values, including NativeFunction callbacks.
// Numeric Go values are normalized by the VM to int64/float64.
type Globals map[string]any

type State = vm.State
type Table = vm.Table
type NativeFunction = vm.NativeFunction
type LuaError = vm.LuaError

func NewTable() *Table { return vm.NewTable() }

// Runtime retains globals and closures across executions. It is not safe for
// concurrent use. The zero value works without IR optimization; New enables it.
// State exposes output, context, instruction limits and the low-level stack API.
type Runtime struct {
	State    *State
	Optimize bool
}

func New() *Runtime { return &Runtime{State: vm.NewState(), Optimize: true} }

func (r *Runtime) state() *State {
	if r.State == nil {
		r.State = vm.NewState()
	}
	return r.State
}

// Result owns a copy of the returned value list, preserving nils and order.
// Globals is the runtime's live environment, not a snapshot. Tables and
// functions in Returns retain their identity and can be used by the host.
type Result struct {
	Globals *Table
	Returns []any
	runtime *Runtime
}

// ExecFile executes a UTF-8/byte source file in a fresh runtime. The optional
// globals map predeclares host values. For persistent globals use Runtime.ExecFile.
func ExecFile(filename string, globals ...Globals) (*Result, error) {
	r, err := withGlobals(globals)
	if err != nil {
		return nil, err
	}
	return r.ExecFile(filename)
}

// ExecString executes source in a fresh runtime, optionally with host globals.
func ExecString(source string, globals ...Globals) (*Result, error) {
	r, err := withGlobals(globals)
	if err != nil {
		return nil, err
	}
	return r.ExecString(source)
}

func withGlobals(globals []Globals) (*Runtime, error) {
	if len(globals) > 1 {
		return nil, fmt.Errorf("glua: expected at most one globals map")
	}
	r := New()
	if len(globals) == 1 {
		for name, value := range globals[0] {
			if err := r.SetGlobal(name, value); err != nil {
				return nil, err
			}
		}
	}
	return r, nil
}

func (r *Runtime) ExecFile(filename string) (*Result, error) {
	source, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	return r.exec(filename, string(source))
}

func (r *Runtime) ExecString(source string) (*Result, error) {
	return r.exec("<string>", source)
}

func (r *Runtime) exec(filename, source string) (*Result, error) {
	ast := parser.NewAst(tokens.NewTokenizer(lex.NewLexer(source), filename))
	if err := ast.Next(); err != nil {
		return nil, err
	}
	if r.Optimize {
		optimized, _, err := ir.Optimize(ast)
		if err != nil {
			return nil, err
		}
		ast = optimized
	}
	proto, err := compile.Compile(ast)
	if err != nil {
		return nil, err
	}
	s := r.state()
	if err := s.Run(proto); err != nil {
		return nil, fmt.Errorf("%s: %w", filename, err)
	}
	values := make([]any, s.GetTop())
	for i := range values {
		values[i] = s.At(i + 1)
	}
	return &Result{Globals: s.Globals, Returns: values, runtime: r}, nil
}

func (r *Runtime) Globals() *Table { return r.state().Environment() }

// GetGlobal and SetGlobal are raw host access: they do not run Lua metatables
// or change the API stack. Setting nil removes the binding.
func (r *Runtime) GetGlobal(name string) (any, bool) { return r.Globals().Get(name) }
func (r *Runtime) SetGlobal(name string, value any) error {
	return r.Globals().Set(name, value)
}

// Call accepts a Lua function, NativeFunction or __call object and returns all
// results without changing the API stack. Arguments may use normal Go numerics.
func (r *Runtime) Call(function any, args ...any) ([]any, error) {
	return r.state().Invoke(function, args...)
}

func (r *Runtime) CallGlobal(name string, args ...any) ([]any, error) {
	function, found := r.GetGlobal(name)
	if !found {
		return nil, fmt.Errorf("glua: global %q not found", name)
	}
	return r.Call(function, args...)
}

func (result *Result) GetGlobal(name string) (any, bool) {
	return result.Globals.Get(name)
}
func (result *Result) Call(function any, args ...any) ([]any, error) {
	return result.runtime.Call(function, args...)
}
func (result *Result) CallGlobal(name string, args ...any) ([]any, error) {
	return result.runtime.CallGlobal(name, args...)
}
