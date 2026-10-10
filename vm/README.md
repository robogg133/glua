# VM

`vm` executes in-memory Lua 5.5.1 `compile.Prototype` values. Compile a parsed
AST with `compile.Compile`, optionally applying `ir.Optimize` first. The core
handles all 85 opcode names, including the validated `EXTRAARG` and `MMBIN*`
companion instructions. This is not a binary Lua chunk loader or a claim of
complete reference-Lua compatibility. A `State` must not be used concurrently;
prototypes must remain immutable while their closures are in use.

## Execution and stack API

`vm.NewState()` initializes globals and libraries. Globals persist between
runs. `Run(proto)` clears the API stack and replaces it with chunk results,
including nils; execution errors are returned as Go errors. `Load(proto)` instead
pushes a callable chunk without executing it.

```go
state := vm.NewState()
state.Output = os.Stdout // defaults to os.Stdout; accepts any io.Writer
state.Context = ctx     // optional context.Context
state.MaxSteps = 1_000_000 // zero means unlimited

if err := state.Load(proto); err != nil {
    return err
}
if err := state.Call(0, vm.MultRet); err != nil {
    return err
}
for i := 1; i <= state.GetTop(); i++ {
    fmt.Println(state.At(i))
}
```

The API stack is separate from bytecode registers. `Registers` and `PC` expose
root-frame diagnostics, not the API stack.

| Method | Behavior |
| --- | --- |
| `GetTop()` | Number of API stack entries. |
| `At(index)` | One-based from the bottom, negative from the top (`-1` is top); invalid indices return nil. |
| `Push(value)` | Append a normalized Go/Lua value. |
| `PushNil`, `PushBoolean`, `PushInteger`, `PushNumber`, `PushString` | Typed push helpers. |
| `Pop(n)` | Remove n entries; invalid counts return an error. |
| `SetTop(index)` | Truncate or extend with nil; `SetTop(0)` clears and `SetTop(-2)` removes one entry. |
| `GetGlobal(name)` | Push a global through normal table access; return any access error. |
| `SetGlobal(name)` | Assign the top entry through normal table access, popping it only on success. |
| `Call(nargs, nresults)` | Consume a function followed by its arguments and push its results. |
| `Invoke(fn, args...)` | Return all results directly without changing the caller's API stack. |

`Call` preserves entries below the function; on execution failure the function
and arguments are removed. Fixed result counts truncate or pad with nil;
`vm.MultRet` (`-1`) requests all results. Use `GetTop` to distinguish a missing
entry from a stored nil.

## Host functions and tables

```go
add := vm.NativeFunction(func(s *vm.State, args []any) ([]any, error) {
    if len(args) != 2 {
        return nil, fmt.Errorf("add: expected two arguments")
    }
    a, aOK := args[0].(int64)
    b, bOK := args[1].(int64)
    if !aOK || !bOK {
        return nil, fmt.Errorf("add: expected two Lua integers")
    }
    return []any{a + b}, nil
})

state.Push(add)
if err := state.SetGlobal("add"); err != nil {
    return err
}
if err := state.GetGlobal("add"); err != nil {
    return err
}
state.PushInteger(20)
state.PushInteger(22)
if err := state.Call(2, 1); err != nil {
    return err
}
fmt.Println(state.At(-1)) // 42
if err := state.Pop(1); err != nil {
    return err
}
results, err := state.Invoke(add, int64(3), int64(4))
```

Native functions have type `vm.NativeFunction func(*vm.State, []any) ([]any,
error)`. Lua nil, booleans, integers, floats, and strings use Go `nil`, `bool`,
`int64`, `float64`, and `string`. Use Lua numeric types at the embedding boundary.
Returned closures and normalized function values can be called or used as table
keys without depending on their internal representation. `vm.LuaError` preserves
Lua error objects, including tables, across protected calls.

`state.Globals` is a `*vm.Table`. Create tables with `vm.NewTable()`.
`RawGet`/`RawSet` bypass metamethods; check `RawSet`'s returned error for invalid
keys. Nil assignments remove keys. `Len` reports a raw sequence border, and
`Metatable` assigns the table's metatable. `State.TypeMetatables` provides
per-type metatables, including the string metatable used for string methods.

## Libraries and limitations

Initialization loads base, math, table, string, utf8, and package facilities.
Available functionality includes Lua patterns, string formatting, text `load`,
and `require`; availability is not a full standard-library conformance claim.
Remaining gaps include io, os, debug, coroutine, binary chunks, string packing,
Lua's lax-UTF8 mode, and a memory quota. Go manages object lifetimes rather than
the reference Lua garbage collector.

`Context` and `MaxSteps` bound VM execution, not host function time, allocations,
or general resource use. Native functions must enforce their own cancellation
and resource limits. This is not a security sandbox.

Native callbacks receive a temporary API stack containing their arguments;
`Pop`/`SetTop` and nested calls cannot overwrite the caller's stack. Explicit
returned slices determine their results. The caller's stack is restored on both
success and failure.

Cancellation and instruction-budget exhaustion still unwind `<close>` resources.
Cleanup receives a separate allowance of 10,000 instructions with cancellation
masked, so cleanup code cannot restart an unlimited Lua loop. Errors raised by
`__close` replace the current Lua error while remaining resources are closed.
Host callbacks still need their own resource and timing bounds.

Tables retain deleted iteration slots so deleting the current key during `pairs`
works. New-key insertions can compact those slots; as in Lua, inserting new keys
while traversing a table is not supported. Table length currently scans the
contiguous prefix, and Lua may choose a different valid border for tables with
holes. String decimal formatting uses Go's formatting and is not guaranteed
byte-identical to C in every numeric corner case.

## Tests

```sh
go test ./vm -run='TestVM' -count=1
go test ./vm
# Optional bounded fuzz run; -run skips ordinary regression tests.
go test ./vm -run='^$' -fuzz='^FuzzVMRunMalformedBytecode$' -fuzztime=10s -parallel=2
```

`runtime_test.go` uses the external `vm_test` package. Compiler fixture sources
run raw and after IR optimization with explicit return, stdout, and error
expectations, without an installed Lua interpreter, network access, or the
compiler's private C runner. The IR indexing-timing fixture deliberately has
separate raw and optimized expectations. Hand-built tests cover immediate table
access, comparisons, shifts, `TESTSET`, arithmetic fallback operands/flips,
`LOADKX`, metadata, native stack isolation, cancellation cleanup, and integer
loop boundaries, alongside the compiled fixtures.

The malformed-bytecode corpus and fuzz target check for leaked Go panics with
`MaxSteps=1000`, frames of at most 16 initial registers, at most 32 instructions
per prototype, and no global environment or host callbacks. To avoid testing
memory exhaustion, fuzz inputs replace `CONCAT` and disable materialized-vararg
expansion. A bounded passing fuzz run is not proof that all malformed bytecode
is safe, and these tests are not exhaustive opcode or library conformance tests.
