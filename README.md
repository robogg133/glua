# glua

A Go API for executing Lua source through **parser → IR → compiler → VM**.
It does not require an installed Lua executable. See [`vm/README.md`](vm/README.md)
for VM compatibility and standard-library limitations.

## Execute code directly

```go
import "github.com/robogg133/glua"

result, err := glua.ExecFile("script.lua")
if err != nil {
    return err
}
fmt.Println(result.Returns) // Every result, including intermediate nil values.
answer, found := result.Globals.Get("answer")
```

Or execute a string with predeclared host values:

```go
result, err := glua.ExecString(`
    answer = initial + 2
    function greet(name) return "hello " .. name end
    return answer, nil, {enabled = false}
`, glua.Globals{"initial": 40})
if err != nil {
    return err
}

fmt.Println(result.Returns[0]) // int64(42)
config := result.Returns[2].(*glua.Table)
if err := config.Set("name", "Go"); err != nil {
    return err
}
name, found := config.Get("name")
values, err := result.CallGlobal("greet", name) // ["hello Go"]
```

`ExecFile` and `ExecString` create an independent runtime for every call. They
accept zero or one `Globals` map, copy its bindings into the environment, and do
not modify the Go map. Referenced objects, such as tables, remain shared.

## Persistent runtime and Go functions

```go
lua := glua.New()
lua.State.Output = &output // io.Writer; defaults to os.Stdout.
lua.State.Context = ctx   // Optional context.Context.
lua.State.MaxSteps = 1_000_000

err := lua.SetGlobal("double", glua.NativeFunction(func(state *glua.State, args []any) ([]any, error) {
    if len(args) != 1 {
        return nil, fmt.Errorf("double: expected one argument")
    }
    n, ok := args[0].(int64)
    if !ok {
        return nil, fmt.Errorf("double: expected an integer")
    }
    return []any{n * 2}, nil
}))
if err != nil {
    return err
}

result, err := lua.ExecString(`count = double(21); return count`)
if err != nil {
    return err
}
count, found := lua.GetGlobal("count") // int64(42), true

// Calling a Lua function returned directly also works.
result, err = lua.ExecString(`return function(n) return n+count,nil end`)
if err != nil {
    return err
}
values, err := lua.Call(result.Returns[0], 8) // [int64(50), nil]
```

These snippets are intended to run inside a Go function. Complete executable
examples are in `glua_test.go`:

```sh
go test . -run Example -v
go run ./cmd/lua script.lua
```

## Public API

| API | Purpose |
|---|---|
| `ExecFile(filename, globals...)` | Execute a file in a new runtime. |
| `ExecString(source, globals...)` | Execute a string in a new runtime. |
| `New()` | Create a `*Runtime` with libraries and IR optimization enabled. |
| `Runtime.ExecFile` / `ExecString` | Execute while preserving runtime globals. |
| `Runtime.Globals()` | Return the live global table, including libraries. |
| `Runtime.GetGlobal(name)` | Return `(value, found)`. |
| `Runtime.SetGlobal(name, value)` | Set a binding, including a Go function. |
| `Runtime.Call(function, args...)` | Call a function or `__call` object without manipulating the stack. |
| `Runtime.CallGlobal(name, args...)` | Call a global function by name. |
| `Result.Globals` / `Returns` | Live global environment and independent result list. |
| `Result.GetGlobal`, `Call`, `CallGlobal` | Inspect or call through the result's runtime. |
| `NewTable()` | Create a table shareable with Lua. |
| `Table.Get(key)` / `Set(key, value)` | Direct host access with validation for invalid keys. |
| `Table.Keys()` / `Len()` | Enumerate live keys or get a sequence border. |

`Table`, `State`, `NativeFunction`, and `LuaError` are aliases for VM types, not
another value representation. `Runtime.State` exposes the existing low-level
stack API (`Push`, `Pop`, `Load`, `Call`, and so on) when needed.

### Semantics and limits

- Host `Get`/`Set` and global access are **raw**: they do not invoke `__index` or
  `__newindex`. Writing nil removes a binding. `Get` distinguishes a missing
  entry from false, zero, and an empty string.
- Lua integers are `int64`, floats are `float64`, nil is `nil`, and tables are
  `*glua.Table`. Go numeric values are normalized at VM boundaries. Register Go
  functions with `glua.NativeFunction`; arbitrary Go signatures are not adapted
  through reflection.
- `Result.Returns` copies its slice but not contained objects, so it is not
  overwritten by the next execution. `Result.Globals` is live: subsequent
  changes in the same runtime are visible. Calling functions does not change the
  public stack; executing a chunk replaces it with chunk results.
- Errors return a nil result. Previously executed global writes and other effects
  are not rolled back. `LuaError` remains available through `errors.As`.
- `New()` and standalone execution enable IR optimization. Set
  `lua.Optimize = false` to compile the original AST; see the index-timing note
  in `compile/README.md`. The zero value of `Runtime` works with optimization
  disabled.
- Runtimes and tables are not safe for concurrent use. `Context` and `MaxSteps`
  are neither a sandbox nor a memory quota; Go callbacks must manage their own
  resources.

## Tests

```sh
go test ./...
go vet ./...
go test -race .
```
