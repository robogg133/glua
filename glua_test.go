package glua_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/robogg133/glua"
)

func ExampleExecString() {
	result, err := glua.ExecString(`answer=initial+2; return answer,nil,"done"`, glua.Globals{"initial": 40})
	if err != nil {
		fmt.Println(err)
		return
	}
	answer, found := result.GetGlobal("answer")
	fmt.Println(answer, found)
	fmt.Println(result.Returns)
	// Output:
	// 42 true
	// [42 <nil> done]
}

func ExampleRuntime_SetGlobal() {
	lua := glua.New()
	err := lua.SetGlobal("double", glua.NativeFunction(func(_ *glua.State, args []any) ([]any, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("double: expected one argument")
		}
		x, ok := args[0].(int64)
		if !ok {
			return nil, fmt.Errorf("double: expected an integer")
		}
		return []any{x * 2}, nil
	}))
	if err != nil {
		fmt.Println(err)
		return
	}
	result, err := lua.ExecString(`config={enabled=false}; function greet(name) return "hello "..name end; return double(21),config`)
	if err != nil {
		fmt.Println(err)
		return
	}
	config := result.Returns[1].(*glua.Table)
	if err := config.Set("name", "Go"); err != nil {
		fmt.Println(err)
		return
	}
	name, _ := config.Get("name")
	greeting, err := result.CallGlobal("greet", name)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(result.Returns[0], greeting[0])
	enabled, found := config.Get("enabled")
	fmt.Println(enabled, found)
	// Output:
	// 42 hello Go
	// false true
}

func TestExecFile(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "script.lua")
	if err := os.WriteFile(filename, []byte(`answer=input+2; return answer,nil`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, session := range []bool{false, true} {
		var result *glua.Result
		var err error
		if session {
			lua := glua.New()
			if err = lua.SetGlobal("input", 40); err != nil {
				t.Fatal(err)
			}
			result, err = lua.ExecFile(filename)
		} else {
			result, err = glua.ExecFile(filename, glua.Globals{"input": 40})
		}
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(result.Returns, []any{int64(42), nil}) {
			t.Fatal(result.Returns)
		}
		if v, found := result.Globals.Get("answer"); !found || v != int64(42) {
			t.Fatal(v, found)
		}
	}
	if result, err := glua.ExecFile(filename + ".missing"); result != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v %v", result, err)
	}
	if err := os.WriteFile(filename, []byte("local = broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := glua.ExecFile(filename); result != nil || err == nil || !strings.Contains(err.Error(), filename) {
		t.Fatalf("syntax error: %v %v", result, err)
	}
}

func TestPersistentRuntimeAndResults(t *testing.T) {
	for _, optimize := range []bool{false, true} {
		lua := glua.New()
		lua.Optimize = optimize
		var output bytes.Buffer
		lua.State.Output = &output
		first, err := lua.ExecString(`counter=10; data={x=1}; function add(n) counter=counter+n;return counter,nil end; return data,function()return counter end,nil`)
		if err != nil {
			t.Fatal(err)
		}
		if len(first.Returns) != 3 || first.Returns[2] != nil {
			t.Fatal(first.Returns)
		}
		data := first.Returns[0].(*glua.Table)
		if err := data.Set("x", 42); err != nil {
			t.Fatal(err)
		}
		if err := lua.SetGlobal("host", "Go"); err != nil {
			t.Fatal(err)
		}
		second, err := lua.ExecString(`print(host); return add(data.x)`)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(second.Returns, []any{int64(52), nil}) || output.String() != "Go\n" {
			t.Fatal(second.Returns, output.String())
		}
		values, err := first.Call(first.Returns[1])
		if err != nil || !reflect.DeepEqual(values, []any{int64(52)}) {
			t.Fatal(values, err)
		}
		if first.Globals != second.Globals || first.Globals != lua.Globals() {
			t.Fatal("globals lost identity")
		}
		if v, found := first.GetGlobal("counter"); !found || v != int64(52) {
			t.Fatal(v, found)
		}
		if len(first.Returns) != 3 || first.Returns[0] != data {
			t.Fatal("old returns overwritten")
		}
		lua.State.Push("sentinel")
		values, err = lua.CallGlobal("add", 1)
		if err != nil || !reflect.DeepEqual(values, []any{int64(53), nil}) {
			t.Fatal(values, err)
		}
		if lua.State.At(-1) != "sentinel" {
			t.Fatal("Call changed API stack")
		}
		if _, err := lua.CallGlobal("absent"); err == nil {
			t.Fatal("missing function accepted")
		}
		if _, err := lua.Call(int64(1)); err == nil {
			t.Fatal("number is callable")
		}
	}
}

func TestTablesAndHostGlobals(t *testing.T) {
	table := glua.NewTable()
	for key, value := range map[string]any{"enabled": false, "empty": "", "zero": 0} {
		if err := table.Set(key, value); err != nil {
			t.Fatal(err)
		}
		if v, found := table.Get(key); !found || v == nil {
			t.Fatal(key, v, found)
		}
	}
	if err := table.Set(1, "first"); err != nil {
		t.Fatal(err)
	}
	if v, found := table.Get(float64(1)); !found || v != "first" {
		t.Fatal(v, found)
	}
	if err := table.Set("deleted", true); err != nil {
		t.Fatal(err)
	}
	if err := table.Set("deleted", nil); err != nil {
		t.Fatal(err)
	}
	if _, found := table.Get("deleted"); found {
		t.Fatal("nil retained")
	}
	if err := table.Set(nil, true); err == nil {
		t.Fatal("nil key accepted")
	}
	predeclared := glua.Globals{"config": table, "input": 2}
	result, err := glua.ExecString(`config.input=input;input=9;return config,config.enabled,config[1]`, predeclared)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Returns, []any{table, false, "first"}) {
		t.Fatal(result.Returns)
	}
	if predeclared["input"] != 2 {
		t.Fatal("predeclared map mutated")
	}
	if v, found := table.Get("input"); !found || v != int64(2) {
		t.Fatal(v, found)
	}
	lua := glua.New()
	if err := lua.SetGlobal("flag", false); err != nil {
		t.Fatal(err)
	}
	if v, found := lua.GetGlobal("flag"); !found || v != false {
		t.Fatal(v, found)
	}
	if err := lua.SetGlobal("flag", nil); err != nil {
		t.Fatal(err)
	}
	if _, found := lua.GetGlobal("flag"); found {
		t.Fatal("global not removed")
	}
}

func TestPredeclaredFunctionsAndCustomState(t *testing.T) {
	fn := glua.NativeFunction(func(_ *glua.State, args []any) ([]any, error) {
		return append([]any{"host"}, args...), nil
	})
	result, err := glua.ExecString(`return echo(1,nil,3)`, glua.Globals{"echo": fn})
	if err != nil || !reflect.DeepEqual(result.Returns, []any{"host", int64(1), nil, int64(3)}) {
		t.Fatal(result, err)
	}
	var output bytes.Buffer
	lua := &glua.Runtime{State: &glua.State{Output: &output}}
	if err := lua.SetGlobal("assert", fn); err != nil {
		t.Fatal(err)
	}
	result, err = lua.ExecString(`print("ready");return assert(42)`)
	if err != nil || output.String() != "ready\n" || !reflect.DeepEqual(result.Returns, []any{"host", int64(42)}) {
		t.Fatal(result, err, output.String())
	}
	failure := errors.New("host failure")
	if err := lua.SetGlobal("fail", glua.NativeFunction(func(_ *glua.State, _ []any) ([]any, error) { return nil, failure })); err != nil {
		t.Fatal(err)
	}
	if _, err := lua.ExecString(`fail()`); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}

func TestExecutionErrorsAndZeroValue(t *testing.T) {
	var lua glua.Runtime
	if result, err := lua.ExecString(`return 42`); err != nil || result.Returns[0] != int64(42) {
		t.Fatal(result, err)
	}
	for _, source := range []string{"local = broken", "local x=nil;return x.key"} {
		if result, err := lua.ExecString(source); result != nil || err == nil || !strings.Contains(err.Error(), "<string>") {
			t.Fatal(result, err)
		}
	}
	object := glua.NewTable()
	if err := lua.SetGlobal("object", object); err != nil {
		t.Fatal(err)
	}
	if _, err := lua.ExecString(`error(object)`); err == nil {
		t.Fatal("error accepted")
	} else {
		var luaErr glua.LuaError
		if !errors.As(err, &luaErr) || luaErr.Value != object {
			t.Fatal("lost error identity", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lua.State.Context = ctx
	if _, err := lua.ExecString(`return 1`); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	lua.State.Context = nil
	lua.State.MaxSteps = 100
	if _, err := lua.ExecString(`while true do end`); err == nil {
		t.Fatal("instruction limit ignored")
	}
	if result, err := glua.ExecString(`return`, glua.Globals{}, glua.Globals{}); result != nil || err == nil {
		t.Fatal("multiple globals accepted")
	}
	if result, err := glua.ExecString(`return`); err != nil || len(result.Returns) != 0 {
		t.Fatal(result, err)
	}
}
