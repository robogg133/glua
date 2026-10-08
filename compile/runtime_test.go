package compile

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robogg133/glua/ir"
	"github.com/robogg133/glua/lex"
	"github.com/robogg133/glua/parser"
	"github.com/robogg133/glua/tokens"
)

// Run with LUA55_SRC=/path/lua-5.5.1/src go test ./compile -run TestRuntime -v.
// No production dumper, luac, installed Lua, or network access is used here.
// Compile must provide the public Compile(*parser.AST) (*Prototype, error) API.
func runtimeRunner(t *testing.T) string {
	t.Helper()
	src := os.Getenv("LUA55_SRC")
	if src == "" {
		t.Skip("runtime tests require LUA55_SRC=/path/lua-5.5.1/src (official sources)")
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Fatal("LUA55_SRC is set but cc is not on PATH")
	}
	src, err = filepath.Abs(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(src, "lua.h")); err != nil {
		t.Fatalf("invalid LUA55_SRC: %v", err)
	}
	sources, err := filepath.Glob(filepath.Join(src, "*.c"))
	if err != nil {
		t.Fatal(err)
	}
	runner := filepath.Join(t.TempDir(), "runner")
	args := []string{"-std=c11", "-O0", "-g", "-DLUA_USE_APICHECK", "-DLUAI_ASSERT", "-I", src, "testdata/runner.c"}
	for _, source := range sources {
		switch filepath.Base(source) {
		case "lua.c", "luac.c", "onelua.c":
			continue
		}
		// Include linit and all standard libraries, not just the VM core.
		args = append(args, source)
	}
	args = append(args, "-lm", "-o", runner)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, cc, args...).CombinedOutput(); err != nil {
		t.Fatalf("build Lua 5.5.1 runner (%v): %v\n%s", ctx.Err(), err, out)
	}
	return runner
}

// runtimeWire is deliberately private to tests, with no official chunk header,
// debug tables, alignment, or Lua dump tags. See runner.c for the wire layout.
func runtimeWire(t *testing.T, p *Prototype) []byte {
	t.Helper()
	var b bytes.Buffer
	b.WriteByte('p')
	u32 := func(v uint32) { b.Write(binary.LittleEndian.AppendUint32(nil, v)) }
	u64 := func(v uint64) { b.Write(binary.LittleEndian.AppendUint64(nil, v)) }
	count := func(n int) {
		if n > 16*1024*1024 {
			t.Fatal("test wire count exceeds 16 MiB limit")
		}
		u32(uint32(n))
	}
	str := func(s string) { count(len(s)); b.WriteString(s) }
	boolean := func(v bool) {
		if v {
			b.WriteByte(1)
		} else {
			b.WriteByte(0)
		}
	}
	var write func(*Prototype, int)
	write = func(p *Prototype, depth int) {
		if p == nil || depth > 200 {
			t.Fatal("nil or excessively nested test prototype")
		}
		b.WriteByte(p.NumParams)
		boolean(p.IsVararg)
		b.WriteByte(p.MaxStackSize)
		boolean(p.VarargTable)
		count(len(p.Code))
		for _, i := range p.Code {
			u32(uint32(i))
		}
		count(len(p.Constants))
		for _, k := range p.Constants {
			if k.Kind > ConstantString {
				t.Fatalf("unknown constant kind %d", k.Kind)
			}
			b.WriteByte(byte(k.Kind))
			u64(k.Bits)
			str(k.String)
		}
		count(len(p.Upvalues))
		for _, u := range p.Upvalues {
			boolean(u.InStack)
			b.WriteByte(u.Index)
			b.WriteByte(u.Kind)
			str(u.Name)
		}
		count(len(p.Children))
		for i := range p.Children {
			write(&p.Children[i], depth+1)
		}
	}
	write(p, 0)
	return b.Bytes()
}

func runtimeSource(source string) []byte {
	b := []byte{'s'}
	b = binary.LittleEndian.AppendUint32(b, uint32(len(source)))
	return append(b, source...)
}

type runtimeResult struct {
	status int
	stdout string
	stderr string
}

func runtimeRun(t *testing.T, runner string, input []byte) runtimeResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, runner)
	cmd.Stdin = bytes.NewReader(input)
	var out, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("runner timed out: %v\n%s", ctx.Err(), diagnostic.String())
	}
	status := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 1 {
			t.Fatalf("runner failed (not a Lua runtime error): %v\nstdout: %s\nstderr: %s", err, out.String(), diagnostic.String())
		}
		status = 1
	}
	return runtimeResult{status, out.String(), diagnostic.String()}
}

func runtimeCompare(t *testing.T, reference, compiled runtimeResult, wantError string) {
	t.Helper()
	wantStatus := 0
	if wantError != "" {
		wantStatus = 1
	}
	for name, result := range map[string]runtimeResult{"source": reference, "prototype": compiled} {
		if result.status != wantStatus || (wantError != "" && !strings.Contains(result.stderr, wantError)) {
			t.Errorf("%s: want status %d, error containing %q; got %+v", name, wantStatus, wantError, result)
		}
	}
	// stdout includes user prints (side effects), Lua status, result count and
	// typed primitive values. Error locations differ because debug info is omitted.
	if reference.status != compiled.status || reference.stdout != compiled.stdout {
		t.Errorf("source/prototype mismatch\nsource: %+v\nprototype: %+v", reference, compiled)
	}
}

func TestRuntimeBridge(t *testing.T) {
	runner := runtimeRunner(t)
	t.Run("constants_and_gc", func(t *testing.T) {
		// Independent of Compile: exercise all constant tags, binary strings,
		// signed integer/float bits, and GC traversal of an imported closure.
		p := &Prototype{
			MaxStackSize: 8,
			Upvalues:     []Upvalue{{Name: "_ENV", InStack: true}},
			Constants: []Constant{NilConstant(), BoolConstant(false), BoolConstant(true),
				IntConstant(math.MinInt64), FloatConstant(1.25), FloatConstant(math.Copysign(0, -1)), StringConstant("a\x00b"),
				StringConstant(strings.Repeat("long", 30))},
		}
		for i := range p.Constants {
			p.Code = append(p.Code, ABx(OpLOADK, i, i))
		}
		p.Code = append(p.Code, ABC(OpRETURN, 0, 9, 0, false))
		reference := runtimeRun(t, runner, runtimeSource(`return nil,false,true,-9223372036854775807-1,1.25,-0.0,"a\0b",string.rep("long",30)`))
		runtimeCompare(t, reference, runtimeRun(t, runner, runtimeWire(t, p)), "")
	})
	t.Run("child_capture", func(t *testing.T) {
		p := &Prototype{
			MaxStackSize: 2,
			Upvalues:     []Upvalue{{Name: "_ENV", InStack: true}},
			Code:         []Instruction{AsBx(OpLOADI, 0, 41), ABx(OpCLOSURE, 1, 0), ABC(OpCALL, 1, 1, 2, false), ABC(OpRETURN, 1, 2, 0, true)},
			Children: []Prototype{{
				MaxStackSize: 2,
				Upvalues:     []Upvalue{{Name: "x", InStack: true, Index: 0}},
				Code:         []Instruction{ABC(OpGETUPVAL, 0, 0, 0, false), ABC(OpRETURN1, 0, 0, 0, false)},
			}},
		}
		reference := runtimeRun(t, runner, runtimeSource(`local x=41; return (function() return x end)()`))
		runtimeCompare(t, reference, runtimeRun(t, runner, runtimeWire(t, p)), "")
	})
}

func TestRuntime(t *testing.T) {
	runner := runtimeRunner(t)
	for _, tc := range runtimeCases {
		t.Run(tc.name, func(t *testing.T) {
			reference := runtimeRun(t, runner, runtimeSource(tc.source))
			// Check the oracle first: a broken fixture must not masquerade as a
			// compiler bug, nor may two failing implementations pass a test.
			if (tc.wantError == "" && reference.status != 0) ||
				(tc.wantError != "" && (reference.status != 1 || !strings.Contains(reference.stderr, tc.wantError))) {
				t.Fatalf("invalid reference fixture: %+v", reference)
			}
			runtimeVariants(t, runner, tc.source, reference, tc.wantError)
		})
	}
}

func runtimeVariants(t *testing.T, runner, source string, reference runtimeResult, wantError string) {
	t.Helper()
	for _, optimized := range []bool{false, true} {
		name := "parsed"
		if optimized {
			name = "optimized"
		}
		t.Run(name, func(t *testing.T) {
			ast := parser.NewAst(tokens.NewTokenizer(lex.NewLexer(source), "runtime"))
			if err := ast.Next(); err != nil {
				t.Fatalf("parse: %v", err)
			}
			if optimized {
				var err error
				ast, _, err = ir.Optimize(ast)
				if err != nil {
					t.Fatalf("optimize: %v", err)
				}
			}
			p, err := Compile(ast)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			checkPrototype(t, p, nil)
			runtimeCompare(t, reference, runtimeRun(t, runner, runtimeWire(t, p)), wantError)
		})
	}
}

var runtimeCases = []struct{ name, source, wantError string }{
	{"primitives", `return nil,true,false,42,-17,1.25,"a\0b",0x7fffffffffffffff`, ""},
	{"arithmetic", `local a,b=17,5
assert(a+b==22 and a-b==12 and a*b==85 and a//b==3 and a%b==2)
assert(a/b==3.4 and b^3==125 and -b==-5)
assert((a&b)==1 and (a|b)==21 and (a~b)==20 and (b<<2)==20 and (a>>2)==4 and ~0==-1)
assert(-2^2==-4 and 2^3^2==512 and -7//3==-3 and -7%3==2)
return a+b,a/b,b^3`, ""},
	{"comparisons_short_circuit", `local n=0
local function hit() n=n+1; return "hit" end
assert((false and hit())==false and (true or hit())==true and n==0)
assert((nil or hit())=="hit" and n==1 and (0 and 7)==7)
local a,b=3,4
assert(a<b and a<=b and b>a and b>=a and a~=b and a==3)
assert(not nil and not false and not not 0 and "a"<"b")
return n,false or nil,true and false`, ""},
	{"strings_tables", `local t={10,20,30,x=30,[4]=40}
t.x=t.x+1; t[2]=22
assert(#t==4 and t[1]==10 and t.x==31 and t[2]==22)
local s="a" .. 1 .. "b"
return #"a\0b",s,t.x`, ""},
	{"branches_loops_goto", `local n,s=0,0
while n<8 do n=n+1; if n==2 then s=s+20 elseif n==5 then break else s=s+n end end
assert(s==28)
repeat local x=n; n=n-1 until x==2
assert(n==1)
::again:: n=n+1; if n<4 then goto again end
return n,s`, ""},
	{"numeric_for", `local sum=0
for i=1,5 do sum=sum+i end
for i=5,1,-2 do sum=sum+i end
for i=1,0 do error("empty") end
for i=0,1,0.25 do sum=sum+i end
assert(sum==26.5)
return sum`, ""},
	{"numeric_for_captures", `local f={}
for i=1,4 do local x=i*10; f[i]=function() return i,x end end
collectgarbage("collect")
for i=1,4 do local a,b=f[i](); assert(a==i and b==i*10) end
return f[4]()`, ""},
	{"generic_for", `local s=0
for k,v in ipairs({3,5,7}) do s=s+k*v end
local function iter(state,k) k=k+1; if k<=state then return k,k*2 end end
for k,v in iter,3,0 do s=s+v end
local n=0; for k,v in pairs({a=1,b=2}) do n=n+v end
assert(s==46 and n==3)
return s,n`, ""},
	{"generic_for_captures", `local f={}
for k,v in ipairs({10,20,30}) do f[k]=function() return k,v end end
for i=1,3 do local k,v=f[i](); assert(k==i and v==i*10) end
return f[2]()`, ""},
	{"generic_for_close", `local log=""
local close=setmetatable({}, {__close=function() log=log.."C" end})
local function iter(s,k) if k<3 then return k+1 end end
for k in iter,nil,0,close do log=log..k; if k==2 then break end end
assert(log=="12C"); print(log); return log`, ""},
	{"closures_upvalues", `local function counter(x)
 return function(d) x=x+d; return function() return x end end
end
local c=counter(10); local a=c(2); local b=c(3)
collectgarbage("collect"); assert(a()==15 and b()==15)
local function fact(n) if n<=1 then return 1 end return n*fact(n-1) end
return a(),fact(6)`, ""},
	{"close_break_goto_repeat", `local f,g,h
while true do local x=7; f=function() return x end; break end
do local x=8; g=function() return x end; goto out end
::out::
local n=0
repeat local x=n; h=function() return x end; n=n+1 until n==3
local x=99; collectgarbage("collect")
assert(f()==7 and g()==8 and h()==2); return f(),g(),h(),x`, ""},
	{"to_be_closed", `local log=""
local function resource(s) return setmetatable({}, {__close=function(_,err) log=log..s; assert(err==nil) end}) end
local function f() local a<close> = resource("A"); do local b<close> = resource("B") end; return 7 end
assert(f()==7 and log=="BA"); print(log); return log`, ""},
	{"close_on_error", `local log=""
local ok=pcall(function() local x<close> = setmetatable({}, {__close=function(_,err) assert(err~=nil); log="closed" end}); error("boom") end)
assert(not ok and log=="closed"); return ok,log`, ""},
	{"multireturn", `local function f() return 2,nil,4 end
local function count(...) return select("#",...),... end
local a,b,c,d=1,f(); assert(a==1 and b==2 and c==nil and d==4)
local x,y=(f()); assert(x==2 and y==nil)
local t={1,f()}; assert(t[1]==1 and t[2]==2 and t[3]==nil and t[4]==4)
local u={f(),9}; assert(#u==2 and u[1]==2 and u[2]==9)
local n=count(1,f()); assert(n==4)
return count(f())`, ""},
	{"vararg_tailcall", `local function target(...) return select("#",...),... end
local function forward(x,...) local z=x; local function keep() return z end; assert(keep()==x); return target(...) end
return forward(99,1,nil,3)`, ""},
	{"named_vararg_read", `local function f(x,... args)
 assert(args.n==3 and args[1]==10 and args[2]==nil and args[3]==30 and args[9]==nil)
 return x,args.n,...
end
return f(7,10,nil,30)`, ""},
	{"named_vararg_mutation", `local function f(... args)
 args[1]=99; args[3]=33; args.n=3
 assert(args[1]==99 and args[3]==33)
 return ...
end
local a,b,c=f(1,2); assert(a==99 and b==2 and c==33); return a,b,c`, ""},
	{"named_vararg_capture", `local function f(... args)
 return function() args[1]=args[1]+1; return args.n,args[1],args[2] end
end
local g=f(10,20); collectgarbage("collect")
local n,a,b=g(); assert(n==2 and a==11 and b==20); return g()`, ""},
	{"functions_methods_long_keys", `local t={base=10,nested={}}
function t.nested.add(x,y) return x+y end
function t:method_with_a_name_longer_than_lua_short_string_limit_123456789(x) return self.base+x end
local key="field_with_a_name_longer_than_lua_short_string_limit_123456789"
t[key]=8; assert(t[key]==8)
assert(t.nested.add(2,3)==5)
return t:method_with_a_name_longer_than_lua_short_string_limit_123456789(7)`, ""},
	{"assignment_conflicts", `local i,t=1,{10,20}
i,t[i]=2,99; assert(i==2 and t[1]==99 and t[2]==20)
local old=t; t,t[2]={7},88; assert(t[1]==7 and old[2]==88)
local function f() local a={x=1}; local function get() return a end; a,a.x={x=2},3; return a,get() end
local a,b=f(); assert(a==b and a.x==2)
local x,y=1,2; x,y=y,x; return x,y,old[1],old[2]`, ""},
	{"assignment_evaluation", `local log=""; local t={}
local function obj() log=log.."O"; return t end
local function key() log=log.."K"; return 1 end
local function rhs() log=log.."R"; return 42 end
obj()[key()]=rhs(); assert(log=="OKR" and t[1]==42)
local function values() log=log.."V"; return 8,9 end
obj()[key()],t[2]=values(); assert(log=="OKROKV" and t[1]==8 and t[2]==9)
print(log); return log,t[1],t[2]`, ""},
	{"metamethods", `local mt={__add=function(a,b) return a.x+b.x end,__len=function() return 12 end,__concat=function() return "concat" end}
local a,b=setmetatable({x=3},mt),setmetatable({x=4},mt)
assert(a+b==7 and #a==12 and a..b=="concat"); return a+b,#a,a..b`, ""},
	{"environment", `local outer=_ENV
local _ENV={assert=assert,print=print,x=5}
x=x+2; local function f() x=x+1; return x end
assert(f()==8 and outer.x==nil); print(x); return x`, ""},
	{"globals_initialized", `global *; global runtime_g=3; runtime_g=runtime_g+4
global runtime_c<const> = 11
global function runtime_fn(x) return x+runtime_g end
assert(runtime_g==7 and runtime_c==11 and runtime_fn(2)==9)
print(runtime_g,runtime_c); return runtime_fn(3)`, ""},
	{"globals_uninitialized", `global *; _ENV.runtime_existing=5; global runtime_existing
assert(runtime_existing==5); runtime_existing=6; return runtime_existing`, ""},
	{"global_regular_reinit", `global *; _ENV.runtime_g=1; print("before"); global runtime_g=2; error("missing ERRNNIL")`, "already defined"},
	{"global_false_reinit", `global *; _ENV.runtime_g=false; global runtime_g=2; error("missing ERRNNIL")`, "already defined"},
	{"global_const_reinit", `global *; _ENV.runtime_g=1; global runtime_g<const> = 2; error("missing ERRNNIL")`, "already defined"},
	{"global_function_reinit", `global *; _ENV.runtime_fn=false; global function runtime_fn() end; error("missing ERRNNIL")`, "already defined"},
	{"global_multiple_reinit", `global *; _ENV.runtime_b=4; global runtime_a,runtime_b=1,2; error("missing ERRNNIL")`, "already defined"},
	{"runtime_error", `print("before-error"); local t=nil; return t.x`, "attempt to index"},
	{"empty", `; do end; return`, ""},
	{"call_sugar_and_self", `local function id(x) return x end
local t={x=7}; function t:get(y) return self.x+y end
assert(id"abc"=="abc" and id{9}[1]==9)
return t:get(3)`, ""},
	{"shadowing_and_attributes", `local x=4
local x,y=x,x; local <const> a,b=2,3
local function f() local x=8; return function() return x+y+a+b end end
return f()(),x,y`, ""},
	{"operand_evaluation", `local x=10
local function change() x=20; return 3 end
local y=x+change(); assert(x==20)
local function replace() x=30; return false end
x=x and replace(); assert(x==false); return y,x`, ""},
	{"discarded_results_side_effects", `local log=""
local function f(n) log=log..n; return n,n+1 end
local a=f(1),f(2); a=f(3),f(4); f(5)
assert(log=="12345" and a==3); return log,a`, ""},
	{"constructor_evaluation", `local log=""
local function f(s) log=log..s; return s end
local t={f("A"),[f("K")]=f("V"),f("B"),x=f("X")}
assert(log=="AKVBX"); return log,t[1],t[2],t.K,t.x`, ""},
	{"close_before_tail_return", `local log=""
local function target() log=log.."T"; return 1,nil,3 end
local function f()
 local x<close> = setmetatable({}, {__close=function() log=log.."C" end})
 return target()
end
local a,b,c=f(); assert(log=="TC"); return log,a,b,c`, ""},
	{"close_goto_and_repeat", `local log=""
local function resource(s) return setmetatable({}, {__close=function() log=log..s end}) end
do local x<close> = resource("G"); goto out end
::out::
local n=0
repeat local x<close> = resource("R"); n=n+1 until n==2
while true do local x<close> = resource("B"); break end
assert(log=="GRRB"); return log`, ""},
	{"nil_false_close", `do local a<close> = nil; local b<close> = false end; return true`, ""},
	{"named_vararg_escape", `local function f(... args)
 local t=args; t[1]=9; t.n=3; return args==t,...
end
return f(1,nil,3)`, ""},
	{"named_vararg_late_materialization", `local function f(... args)
 local first=args[1]; local n=args.n
 local alias=args; alias[1]=99
 assert(first==7 and n==2 and args[1]==99)
 return first,n,...
end
return f(7,8)`, ""},
	{"named_vararg_dynamic_keys", `local function f(... args)
 local key="n"; local i=2
 return args[key],args[i],args[0],args[-1],args[1.5],args["1"],args[false],args[nil],...
end
return f(7,8)`, ""},
	{"named_vararg_parenthesized", `local function f(... args) return (args)[1],(...) end
return f(11,12)`, ""},
	{"named_vararg_materialized_tail", `local function target(...) return select("#",...),... end
local function f(x,... args) args[1]=x; return target(...) end
return f(9,1,nil,3)`, ""},
	{"global_check_order", `global *
local log=""
local function rhs() log=log.."R"; return 10,20 end
local env=setmetatable({}, {
 __index=function(_,k) log=log.."?"..k; return nil end,
 __newindex=function(t,k,v) log=log.."!"..k; rawset(t,k,v) end})
do local _ENV=env; global a,b=rhs() end
assert(log=="R?b!b?a!a"); return log,env.a,env.b`, ""},
	{"global_partial_initialization", `global *
local env={a=false}; local log=""
local function rhs() log=log.."R"; return 10,20 end
local ok,err=pcall(function() local _ENV=env; global a,b=rhs() end)
assert(not ok and string.find(err,"already defined",1,true))
assert(env.a==false and env.b==20 and log=="R"); return ok,env.a,env.b,log`, ""},
	{"global_rhs_precedes_check", `global *; _ENV.runtime_g=1
local function rhs() print("rhs-before-check"); _ENV.runtime_g=nil; return 7 end
global runtime_g=rhs(); assert(runtime_g==7); return runtime_g`, ""},
	{"global_environment_change", `global *
local old,new={},{}
local _ENV=old
local function rhs() _ENV=new; return 7,8 end
global a,b=rhs()
return old.a,old.b,new.a,new.b`, ""},
	{"global_nil_and_adjustment", `global *
local function f() return 3,nil,5 end
global runtime_a,runtime_b,runtime_c,runtime_d=f()
assert(runtime_a==3 and runtime_b==nil and runtime_c==5 and runtime_d==nil)
global runtime_b=9; return runtime_a,runtime_b,runtime_c,runtime_d`, ""},
	{"global_recursive_function", `global *
global function runtime_fact(n) if n==0 then return 1 end return n*runtime_fact(n-1) end
return runtime_fact(6)`, ""},
	{"global_collective_const", `local env={answer=42}; local _ENV=env; global <const> *; return answer`, ""},
	{"numeric_edges", `return 0xffffffffffffffff,0x10000000000000000,9223372036854775808,
0x1.fp2,0x1.8,1e309,-0.0,0x7fffffffffffffff+1,(-7)//3,(-7)%3,1<<64,1>>-2`, ""},
	{"arithmetic_fallback_order", `local log=""
local mt={__add=function(a,b) log=log..type(a)..":"..type(b)..";"; return 17 end,
 __sub=function(a,b) return 19 end, __band=function(a,b) return 23 end}
local x=setmetatable({},mt)
local a,b,c,d,e=x+1,1+x,x+1.0,x-300,x&3
assert(log=="table:number;number:table;table:number;")
return log,a,b,c,d,e`, ""},
	{"comparison_metamethods", `local mt={__eq=function(a,b) return a.x==b.x end,
 __lt=function(a,b) return a.x<b.x end,__le=function(a,b) return a.x<=b.x end}
local a,b,c=setmetatable({x=2},mt),setmetatable({x=3},mt),setmetatable({x=2},mt)
return a==c,a~=b,a<b,a<=c,b>a,c>=a`, ""},
	{"named_vararg_env_read", `local function f(... _ENV) return n end
return f(7,8)`, ""},
	{"named_vararg_env_write", `local function f(... _ENV) n=1; return ... end
return f(7,8)`, ""},
	{"named_vararg_env_capture", `local function f(... _ENV) return function() return n end end
local g=f(7,8); collectgarbage("collect"); return g()`, ""},
	{"upvalue_table_literal_read", `local t={x=1,field_with_a_name_longer_than_lua_short_string_limit_123456789=2}
local function f()
 return t.x,t["x"],t.field_with_a_name_longer_than_lua_short_string_limit_123456789,
 t["field_with_a_name_longer_than_lua_short_string_limit_123456789"]
end
return f()`, ""},
	{"upvalue_table_dynamic_read", `local t={x=1}
local function f()
 local function change() t={x=2}; return "x" end
 return t[change()]
end
return f()`, ""},
	{"upvalue_table_parenthesized_read", `local t={x=1}
local function f()
 local function change() t={x=2}; return "x" end
 return (t)[change()]
end
return f()`, ""},
	{"upvalue_table_field_assignment", `local t={}; local old=t
local function f()
 local function change() t={}; return 7 end
 t.x=change()
end
f(); return old.x,t.x`, ""},
	{"upvalue_table_literal_assignment", `local t={}; local old=t
local function f()
 local function change() t={}; return 7 end
 t["x"]=change()
end
f(); return old.x,t.x`, ""},
	{"upvalue_table_dynamic_key_assignment", `local t={}; local old=t
local function f()
 local function change() t={}; return "x" end
 t[change()]=7
end
f(); return old.x,t.x`, ""},
	{"upvalue_table_dynamic_rhs_assignment", `local t={}; local old=t
local function f(key)
 local function change() t={}; return 7 end
 t[key]=change()
end
f("x"); return old.x,t.x`, ""},
	{"upvalue_table_parenthesized_assignment", `local t={}; local old=t
local function f()
 local function change() t={}; return 7 end
 (t).x=change()
end
f(); return old.x,t.x`, ""},
	{"upvalue_table_parenthesized_key_assignment", `local t={}; local old=t
local function f()
 local function change() t={}; return "x" end
 (t)[change()]=7
end
f(); return old.x,t.x`, ""},
	{"upvalue_table_assignment_conflict", `local t={}; local old=t
local function f() t.x,t=7,{} end
f(); return old.x,t.x`, ""},
	{"constructor_collision", `local t={10,[1]=20}; return t[1]`, ""},
	{"constructor_buffered_fields", `local k=8
local function key() return "x" end
local function value() k=9; return {value=90} end
local function last() return 60,nil,80 end
local t={10,[k]=value(),30,[key()]={40,50},[2]=99,last()}
assert(t[1]==10 and t[2]==30 and t[3]==60 and t[4]==nil and t[5]==80)
assert(t[8]==nil and t[9].value==90 and t.x[1]==40 and t.x[2]==50)
return t[1],t[2],t[3],t[4],t[5],t[9].value,t.x[2]`, ""},
	{"global_long_key_environment_check", `global *
local key="global_name_longer_than_the_lua_short_string_limit_123456789"
local old,new={},{}
local function f()
 local _ENV=old
 local function rhs() _ENV=new; new[key]=3; return 7 end
 local function init() global global_name_longer_than_the_lua_short_string_limit_123456789=rhs() end
 return init()
end
local ok,err=pcall(f)
assert(not ok and string.find(err,"already defined",1,true))
return old[key],new[key],ok`, ""},
	{"goto_scope_closing", `local log=""; local functions={}; local n=0
local function resource() return setmetatable({}, {__close=function() log=log.."C" end}) end
::again::
n=n+1
do
 local x=n; local r<close> = resource()
 functions[n]=function() return x end
 if n<3 then goto again end
 goto done
end
::done::
return log,functions[1](),functions[2](),functions[3]()`, ""},
	{"goto_trailing_label", `local log=""
local function resource() return setmetatable({}, {__close=function() log=log.."C" end}) end
do
 goto done
 local x<close> = resource()
 ::done::
end
return log`, ""},
	{"implicit_environment_loop_capture", `local functions={}
for i=1,3 do
 local _ENV={value=i}
 functions[i]=function() return value end
end
return functions[1](),functions[2](),functions[3]()`, ""},
	{"numeric_for_zero_step", `for i=1,3,0 do end`, "step is zero"},
	{"integer_divide_zero", `local x=0; return 1//x`, "divide by zero"},
	{"modulo_zero", `local x=0; return 1%x`, "'n%0'"},
}

func TestRuntimeWideConstants(t *testing.T) {
	runner := runtimeRunner(t)
	var source strings.Builder
	source.WriteString("local t={};")
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&source, "t.k%d=\"value%d\";", i, i)
	}
	source.WriteString("function t:method() return self.k299 end; return t.k0,t.k255,t.k299,t:method()")
	text := source.String()
	reference := runtimeRun(t, runner, runtimeSource(text))
	if reference.status != 0 {
		t.Fatalf("invalid reference fixture: %+v", reference)
	}
	runtimeVariants(t, runner, text, reference, "")
}

// IR propagation can change when Lua discharges an upvalue-backed table.
// Compare each AST against its own source equivalent, not against the original
// expression's implementation-dependent timing. This is not an IR equivalence
// claim: the two source programs deliberately have different results.
func TestRuntimeIRIndexingTiming(t *testing.T) {
	runner := runtimeRunner(t)
	const source = `local t={}; local old=t
local function f()
 local key="x"
 local function change() t={}; return 7 end
 t[key]=change()
end
f(); return old.x,t.x`
	a := parseCompile(t, source)
	p := compileChecked(t, a)
	raw := runtimeRun(t, runner, runtimeSource(source))
	runtimeCompare(t, raw, runtimeRun(t, runner, runtimeWire(t, p)), "")
	optimized, _, err := ir.Optimize(a)
	if err != nil {
		t.Fatal(err)
	}
	p = compileChecked(t, optimized)
	foldedSource := strings.Replace(source, "t[key]=", `t["x"]=`, 1)
	folded := runtimeRun(t, runner, runtimeSource(foldedSource))
	if raw.stdout == folded.stdout {
		t.Fatal("fixture no longer exposes IR indexing timing")
	}
	runtimeCompare(t, folded, runtimeRun(t, runner, runtimeWire(t, p)), "")
}

func TestRuntimeConstructorPressure(t *testing.T) {
	runner := runtimeRunner(t)
	var source strings.Builder
	source.WriteString("local ")
	for i := 0; i < 190; i++ {
		if i != 0 {
			source.WriteByte(',')
		}
		fmt.Fprintf(&source, "x%d", i)
	}
	source.WriteString("; local t={1,[1]=99,")
	for i := 2; i <= 64; i++ {
		fmt.Fprintf(&source, "%d,", i)
	}
	source.WriteString("}; assert(#t==64 and t[1]==99 and t[64]==64); return #t,t[1],t[64]")
	text := source.String()
	reference := runtimeRun(t, runner, runtimeSource(text))
	if reference.status != 0 {
		t.Fatalf("invalid reference fixture: %+v", reference)
	}
	runtimeVariants(t, runner, text, reference, "")
}

func TestRuntimeLargeTable(t *testing.T) {
	runner := runtimeRunner(t)
	// Cross SETLIST batches and its 10-bit array-offset boundary; the final
	// call must expand, unlike calls earlier in a list constructor.
	var source strings.Builder
	source.WriteString("local function last() return 1201,1202 end; local t={")
	for i := 1; i <= 1200; i++ {
		fmt.Fprintf(&source, "%d,", i)
	}
	source.WriteString("last()}; assert(#t==1202); for i=1,1202 do assert(t[i]==i) end; return #t,t[1024],t[1202]")
	text := source.String()
	reference := runtimeRun(t, runner, runtimeSource(text))
	if reference.status != 0 {
		t.Fatalf("invalid reference fixture: %+v", reference)
	}
	runtimeVariants(t, runner, text, reference, "")
}
