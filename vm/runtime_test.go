package vm_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/robogg133/glua/compile"
	"github.com/robogg133/glua/ir"
	"github.com/robogg133/glua/lex"
	"github.com/robogg133/glua/parser"
	"github.com/robogg133/glua/tokens"
	"github.com/robogg133/glua/vm"
)

func vmCompile(t *testing.T, source string, optimized bool) *compile.Prototype {
	t.Helper()
	ast := parser.NewAst(tokens.NewTokenizer(lex.NewLexer(source), "vm-runtime"))
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
	p, err := compile.Compile(ast)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return p
}

func vmStack(s *vm.State) []any {
	result := make([]any, s.GetTop())
	for i := range result {
		result[i] = s.At(i + 1)
	}
	return result
}

func vmWant(t *testing.T, got, want []any) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("results = %#v, want %#v", got, want)
	}
	for i := range want {
		if f, ok := want[i].(float64); ok {
			g, ok := got[i].(float64)
			if ok && ((math.IsNaN(f) && math.IsNaN(g)) || (g == f && (f != 0 || math.Signbit(g) == math.Signbit(f)))) {
				continue
			}
		} else if reflect.DeepEqual(got[i], want[i]) {
			continue
		}
		t.Errorf("result %d = %#v (%T), want %#v (%T)", i+1, got[i], got[i], want[i], want[i])
	}
}

func vmVariants(t *testing.T, source string, want []any, stdout, wantError string) {
	t.Helper()
	for _, optimized := range []bool{false, true} {
		name := "raw"
		if optimized {
			name = "IR"
		}
		t.Run(name, func(t *testing.T) {
			s := vm.NewState()
			s.MaxSteps = 1_000_000
			var output bytes.Buffer
			s.Output = &output
			s.Push("old stack must be replaced")
			err := s.Run(vmCompile(t, source, optimized))
			if wantError != "" {
				if err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("Run error = %v, want %q", err, wantError)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				vmWant(t, vmStack(s), want)
			}
			if output.String() != stdout {
				t.Errorf("stdout = %q, want %q", output.String(), stdout)
			}
		})
	}
}

// Sources mirror compile/runtime_test.go; expectations below are independent
// of the compiler's private C runner and require no installed Lua or network.
var vmRuntimeCases = []struct{ name, source, wantError string }{
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

var vmRuntimeReturns = map[string][]any{
	"primitives":                                 {nil, true, false, int64(42), int64(-17), 1.25, "a\x00b", int64(math.MaxInt64)},
	"arithmetic":                                 {int64(22), 3.4, 125.0},
	"comparisons_short_circuit":                  {int64(1), nil, false},
	"strings_tables":                             {int64(3), "a1b", int64(31)},
	"branches_loops_goto":                        {int64(4), int64(28)},
	"numeric_for":                                {26.5},
	"numeric_for_captures":                       {int64(4), int64(40)},
	"generic_for":                                {int64(46), int64(3)},
	"generic_for_captures":                       {int64(2), int64(20)},
	"generic_for_close":                          {"12C"},
	"closures_upvalues":                          {int64(15), int64(720)},
	"close_break_goto_repeat":                    {int64(7), int64(8), int64(2), int64(99)},
	"to_be_closed":                               {"BA"},
	"close_on_error":                             {false, "closed"},
	"multireturn":                                {int64(3), int64(2), nil, int64(4)},
	"vararg_tailcall":                            {int64(3), int64(1), nil, int64(3)},
	"named_vararg_read":                          {int64(7), int64(3), int64(10), nil, int64(30)},
	"named_vararg_mutation":                      {int64(99), int64(2), int64(33)},
	"named_vararg_capture":                       {int64(2), int64(12), int64(20)},
	"functions_methods_long_keys":                {int64(17)},
	"assignment_conflicts":                       {int64(2), int64(1), int64(99), int64(88)},
	"assignment_evaluation":                      {"OKROKV", int64(8), int64(9)},
	"metamethods":                                {int64(7), int64(12), "concat"},
	"environment":                                {int64(8)},
	"globals_initialized":                        {int64(10)},
	"globals_uninitialized":                      {int64(6)},
	"empty":                                      {},
	"call_sugar_and_self":                        {int64(10)},
	"shadowing_and_attributes":                   {int64(17), int64(4), int64(4)},
	"operand_evaluation":                         {int64(23), false},
	"discarded_results_side_effects":             {"12345", int64(3)},
	"constructor_evaluation":                     {"AKVBX", "A", "B", "V", "X"},
	"close_before_tail_return":                   {"TC", int64(1), nil, int64(3)},
	"close_goto_and_repeat":                      {"GRRB"},
	"nil_false_close":                            {true},
	"named_vararg_escape":                        {true, int64(9), nil, int64(3)},
	"named_vararg_late_materialization":          {int64(7), int64(2), int64(99), int64(8)},
	"named_vararg_dynamic_keys":                  {int64(2), int64(8), nil, nil, nil, nil, nil, nil, int64(7), int64(8)},
	"named_vararg_parenthesized":                 {int64(11), int64(11)},
	"named_vararg_materialized_tail":             {int64(3), int64(9), nil, int64(3)},
	"global_check_order":                         {"R?b!b?a!a", int64(10), int64(20)},
	"global_partial_initialization":              {false, false, int64(20), "R"},
	"global_rhs_precedes_check":                  {int64(7)},
	"global_environment_change":                  {nil, nil, int64(7), int64(8)},
	"global_nil_and_adjustment":                  {int64(3), int64(9), int64(5), nil},
	"global_recursive_function":                  {int64(720)},
	"global_collective_const":                    {int64(42)},
	"numeric_edges":                              {int64(-1), int64(0), float64(9223372036854775808.0), 7.75, 1.5, math.Inf(1), math.Copysign(0, -1), int64(math.MinInt64), int64(-3), int64(2), int64(0), int64(4)},
	"arithmetic_fallback_order":                  {"table:number;number:table;table:number;", int64(17), int64(17), int64(17), int64(19), int64(23)},
	"comparison_metamethods":                     {true, true, true, true, true, true},
	"named_vararg_env_read":                      {int64(2)},
	"named_vararg_env_write":                     {int64(7)},
	"named_vararg_env_capture":                   {int64(2)},
	"upvalue_table_literal_read":                 {int64(1), int64(1), int64(2), int64(2)},
	"upvalue_table_dynamic_read":                 {int64(2)},
	"upvalue_table_parenthesized_read":           {int64(1)},
	"upvalue_table_field_assignment":             {nil, int64(7)},
	"upvalue_table_literal_assignment":           {nil, int64(7)},
	"upvalue_table_dynamic_key_assignment":       {nil, int64(7)},
	"upvalue_table_dynamic_rhs_assignment":       {int64(7), nil},
	"upvalue_table_parenthesized_assignment":     {int64(7), nil},
	"upvalue_table_parenthesized_key_assignment": {int64(7), nil},
	"upvalue_table_assignment_conflict":          {int64(7), nil},
	"constructor_collision":                      {int64(10)},
	"constructor_buffered_fields":                {int64(10), int64(30), int64(60), nil, int64(80), int64(90), int64(50)},
	"global_long_key_environment_check":          {nil, int64(3), false},
	"goto_scope_closing":                         {"CCC", int64(1), int64(2), int64(3)},
	"goto_trailing_label":                        {""},
	"implicit_environment_loop_capture":          {int64(1), int64(2), int64(3)},
}

var vmRuntimeStdout = map[string]string{
	"generic_for_close":         "12C\n",
	"to_be_closed":              "BA\n",
	"assignment_evaluation":     "OKROKV\n",
	"environment":               "8\n",
	"globals_initialized":       "7\t11\n",
	"global_regular_reinit":     "before\n",
	"runtime_error":             "before-error\n",
	"global_rhs_precedes_check": "rhs-before-check\n",
}

func TestVMCompilerFixtures(t *testing.T) {
	for _, tc := range vmRuntimeCases {
		t.Run(tc.name, func(t *testing.T) {
			want, ok := vmRuntimeReturns[tc.name]
			if tc.wantError == "" && !ok {
				t.Fatal("missing explicit return expectation")
			}
			vmVariants(t, tc.source, want, vmRuntimeStdout[tc.name], tc.wantError)
		})
	}
}

func TestVMGeneratedCompilerFixtures(t *testing.T) {
	t.Run("wide_constants", func(t *testing.T) {
		var source strings.Builder
		source.WriteString("local t={};")
		for i := 0; i < 300; i++ {
			fmt.Fprintf(&source, "t.k%d=\"value%d\";", i, i)
		}
		source.WriteString("function t:method() return self.k299 end; return t.k0,t.k255,t.k299,t:method()")
		vmVariants(t, source.String(), []any{"value0", "value255", "value299", "value299"}, "", "")
	})
	t.Run("constructor_pressure", func(t *testing.T) {
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
		vmVariants(t, source.String(), []any{int64(64), int64(99), int64(64)}, "", "")
	})
	t.Run("large_table", func(t *testing.T) {
		var source strings.Builder
		source.WriteString("local function last() return 1201,1202 end; local t={")
		for i := 1; i <= 1200; i++ {
			fmt.Fprintf(&source, "%d,", i)
		}
		source.WriteString("last()}; assert(#t==1202); for i=1,1202 do assert(t[i]==i) end; return #t,t[1024],t[1202]")
		vmVariants(t, source.String(), []any{int64(1202), int64(1024), int64(1202)}, "", "")
	})
	t.Run("IR_indexing_timing", func(t *testing.T) {
		const source = `local t={}; local old=t
local function f()
 local key="x"
 local function change() t={}; return 7 end
 t[key]=change()
end
f(); return old.x,t.x`
		// IR substitutes the literal key, deliberately changing discharge timing.
		for _, optimized := range []bool{false, true} {
			name := "raw"
			want := []any{int64(7), nil}
			if optimized {
				name = "IR"
				want = []any{nil, int64(7)}
			}
			t.Run(name, func(t *testing.T) {
				s := vm.NewState()
				s.MaxSteps = 1_000_000
				if err := s.Run(vmCompile(t, source, optimized)); err != nil {
					t.Fatal(err)
				}
				vmWant(t, vmStack(s), want)
			})
		}
	})
}

func TestVMStackAPI(t *testing.T) {
	s := vm.NewState()
	s.MaxSteps = 1_000_000
	if s.GetTop() != 0 {
		t.Fatalf("new stack top = %d", s.GetTop())
	}
	s.Push(int64(10))
	s.Push(nil)
	s.Push("top")
	for _, tc := range []struct {
		index int
		want  any
	}{
		{1, int64(10)}, {2, nil}, {3, "top"}, {-1, "top"}, {-2, nil}, {-3, int64(10)},
		{0, nil}, {4, nil}, {-4, nil},
	} {
		if got := s.At(tc.index); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("At(%d) = %#v, want %#v", tc.index, got, tc.want)
		}
	}
	for _, n := range []int{-1, 4} {
		if err := s.Pop(n); err == nil {
			t.Errorf("Pop(%d) accepted invalid count", n)
		}
		vmWant(t, vmStack(s), []any{int64(10), nil, "top"})
	}
	if err := s.Pop(0); err != nil {
		t.Fatal(err)
	}
	if err := s.Pop(2); err != nil {
		t.Fatal(err)
	}
	vmWant(t, vmStack(s), []any{int64(10)})
	if err := s.Pop(1); err != nil {
		t.Fatal(err)
	}
	if err := s.Pop(1); err == nil {
		t.Fatal("Pop accepted empty stack")
	}
	s.Push("stale")
	if err := s.Run(vmCompile(t, "return 1,nil,3", false)); err != nil {
		t.Fatal(err)
	}
	vmWant(t, vmStack(s), []any{int64(1), nil, int64(3)})
	if err := s.Run(vmCompile(t, "return", false)); err != nil {
		t.Fatal(err)
	}
	vmWant(t, vmStack(s), nil)
}

func TestVMNativeCalls(t *testing.T) {
	for _, tc := range []struct {
		name     string
		nresults int
		want     []any
	}{
		{"all", -1, []any{"prefix", int64(7), nil, "end"}},
		{"discard", 0, []any{"prefix"}},
		{"truncate", 1, []any{"prefix", int64(7)}},
		{"pad", 5, []any{"prefix", int64(7), nil, "end", nil, nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := vm.NewState()
			s.MaxSteps = 1_000_000
			called := false
			fn := vm.NativeFunction(func(state *vm.State, args []any) ([]any, error) {
				if state != s {
					t.Error("native received another State")
				}
				vmWant(t, args, []any{int64(7), nil})
				called = true
				return []any{args[0], nil, "end"}, nil
			})
			s.Push("prefix")
			s.Push(fn)
			s.Push(int64(7))
			s.Push(nil)
			if err := s.Call(2, tc.nresults); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("native was not called")
			}
			vmWant(t, vmStack(s), tc.want)
		})
	}
	t.Run("invoke_preserves_stack", func(t *testing.T) {
		s := vm.NewState()
		s.MaxSteps = 1_000_000
		s.Push("sentinel")
		fn := vm.NativeFunction(func(_ *vm.State, args []any) ([]any, error) { return args, nil })
		got, err := s.Invoke(fn, int64(9), nil, "x")
		if err != nil {
			t.Fatal(err)
		}
		vmWant(t, got, []any{int64(9), nil, "x"})
		vmWant(t, vmStack(s), []any{"sentinel"})
	})
	t.Run("native_error", func(t *testing.T) {
		s := vm.NewState()
		s.MaxSteps = 1_000_000
		sentinel := errors.New("native sentinel")
		fn := vm.NativeFunction(func(_ *vm.State, _ []any) ([]any, error) { return nil, sentinel })
		if _, err := s.Invoke(fn); err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
			t.Fatalf("Invoke error = %v", err)
		}
		s.Push(fn)
		if err := s.Call(0, -1); err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
			t.Fatalf("Call error = %v", err)
		}
	})
	t.Run("from_lua_and_reentrant", func(t *testing.T) {
		for _, optimized := range []bool{false, true} {
			s := vm.NewState()
			s.MaxSteps = 1_000_000
			s.Globals.RawSet("native", vm.NativeFunction(func(state *vm.State, args []any) ([]any, error) {
				if len(args) != 3 {
					return nil, fmt.Errorf("got %d arguments", len(args))
				}
				return state.Invoke(args[0], args[1], args[2])
			}))
			if err := s.Run(vmCompile(t, `return native(function(a,b) return a+b,nil,a end,4,5)`, optimized)); err != nil {
				t.Fatal(err)
			}
			vmWant(t, vmStack(s), []any{int64(9), nil, int64(4)})
		}
	})
	t.Run("invalid_call", func(t *testing.T) {
		for _, tc := range []struct{ nargs, nresults int }{{-1, 0}, {1, 0}, {0, -2}} {
			s := vm.NewState()
			s.MaxSteps = 1_000_000
			s.Push(vm.NativeFunction(func(_ *vm.State, _ []any) ([]any, error) { return nil, nil }))
			if err := s.Call(tc.nargs, tc.nresults); err == nil {
				t.Errorf("Call(%d,%d) accepted invalid counts", tc.nargs, tc.nresults)
			}
		}
		s := vm.NewState()
		s.MaxSteps = 1_000_000
		if err := s.Call(0, 0); err == nil {
			t.Fatal("Call accepted no function")
		}
		s.Push(int64(3))
		if err := s.Call(0, 0); err == nil {
			t.Fatal("Call accepted a number")
		}
		if _, err := s.Invoke(nil); err == nil {
			t.Fatal("Invoke accepted nil")
		}
	})
}

func TestVMLuaCallsAndTables(t *testing.T) {
	for _, optimized := range []bool{false, true} {
		s := vm.NewState()
		s.MaxSteps = 1_000_000
		if err := s.Run(vmCompile(t, `local x=0; return function(d) x=x+d; return x,nil end`, optimized)); err != nil {
			t.Fatal(err)
		}
		fn := s.At(1)
		for _, want := range []int64{3, 6} {
			got, err := s.Invoke(fn, int64(3))
			if err != nil {
				t.Fatal(err)
			}
			vmWant(t, got, []any{want, nil})
		}
		if err := s.Pop(1); err != nil {
			t.Fatal(err)
		}
		s.Push(fn)
		s.Push(int64(4))
		if err := s.Call(1, 3); err != nil {
			t.Fatal(err)
		}
		vmWant(t, vmStack(s), []any{int64(10), nil, nil})
	}
	t.Run("table_globals_and_call_metamethod", func(t *testing.T) {
		s := vm.NewState()
		s.MaxSteps = 1_000_000
		table := vm.NewTable()
		table.RawSet(int64(1), "first")
		table.RawSet("x", int64(7))
		if table.Len() != 1 || table.RawGet(int64(1)) != "first" {
			t.Fatal("raw table array access failed")
		}
		mt := vm.NewTable()
		mt.RawSet("__call", vm.NativeFunction(func(_ *vm.State, args []any) ([]any, error) {
			if len(args) != 2 || args[0] != table {
				return nil, fmt.Errorf("missing __call receiver: %#v", args)
			}
			return []any{table.RawGet("x"), args[1]}, nil
		}))
		table.Metatable = mt
		s.Globals.RawSet("host", table)
		if err := s.Run(vmCompile(t, `return host(9)`, false)); err != nil {
			t.Fatal(err)
		}
		vmWant(t, vmStack(s), []any{int64(7), int64(9)})
		table.RawSet("x", nil)
		if table.RawGet("x") != nil {
			t.Fatal("raw nil assignment did not delete key")
		}
	})
}

func TestVMExecutionLimits(t *testing.T) {
	p := vmCompile(t, `while true do end`, false)
	t.Run("step_budget", func(t *testing.T) {
		s := vm.NewState()
		s.MaxSteps = 100
		if err := s.Run(p); err == nil {
			t.Fatal("unbounded loop ignored MaxSteps")
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s := vm.NewState()
		s.Context = ctx
		s.MaxSteps = 1_000_000
		if err := s.Run(p); err == nil {
			t.Fatal("Run ignored cancelled context")
		}
	})
	t.Run("cancel_during_native", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s := vm.NewState()
		s.Context = ctx
		s.MaxSteps = 1_000_000
		s.Globals.RawSet("cancel", vm.NativeFunction(func(_ *vm.State, _ []any) ([]any, error) { cancel(); return nil, nil }))
		if err := s.Run(vmCompile(t, `cancel(); while true do end`, false)); err == nil {
			t.Fatal("Run ignored cancellation during execution")
		}
	})
}

func TestVMMalformedBytecode(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    *compile.Prototype
	}{
		{"nil", nil},
		{"frame", &compile.Prototype{}},
		{"unknown_opcode", &compile.Prototype{MaxStackSize: 2, Code: []compile.Instruction{compile.Instruction(127)}}},
		{"constant", &compile.Prototype{MaxStackSize: 2, Code: []compile.Instruction{compile.ABx(compile.OpLOADK, 0, 0)}}},
		{"loadkx_extraarg", &compile.Prototype{MaxStackSize: 2, Code: []compile.Instruction{compile.ABx(compile.OpLOADKX, 0, 0)}}},
		{"child", &compile.Prototype{MaxStackSize: 2, Code: []compile.Instruction{compile.ABx(compile.OpCLOSURE, 0, 0)}}},
		{"upvalue", &compile.Prototype{MaxStackSize: 2, Code: []compile.Instruction{compile.ABC(compile.OpGETUPVAL, 0, 0, 0, false)}}},
		{"destination", &compile.Prototype{MaxStackSize: 2, Code: []compile.Instruction{compile.AsBx(compile.OpLOADI, 2, 1)}}},
		{"source", &compile.Prototype{MaxStackSize: 2, Code: []compile.Instruction{compile.ABC(compile.OpMOVE, 0, 2, 0, false)}}},
		{"nil_range", &compile.Prototype{MaxStackSize: 2, Code: []compile.Instruction{compile.ABC(compile.OpLOADNIL, 1, 2, 0, false)}}},
		{"return_range", &compile.Prototype{MaxStackSize: 2, Code: []compile.Instruction{compile.ABC(compile.OpRETURN, 1, 4, 0, false)}}},
		{"newtable_extraarg", &compile.Prototype{MaxStackSize: 2, Code: []compile.Instruction{compile.ABC(compile.OpNEWTABLE, 0, 0, 0, false)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("malformed prototype panicked: %v", r)
				}
			}()
			s := vm.NewState()
			s.MaxSteps = 1_000_000
			if err := s.Run(tc.p); err == nil {
				t.Fatal("malformed prototype accepted")
			}
		})
	}
}

func TestVMDeepTailRecursion(t *testing.T) {
	const source = `local function loop(n,acc,...) if n==0 then return acc,... end return loop(n-1,acc+1,...) end
return loop(100000,0,7,nil,9)`
	for _, optimized := range []bool{false, true} {
		name := "raw"
		if optimized {
			name = "IR"
		}
		t.Run(name, func(t *testing.T) {
			s := vm.NewState()
			s.MaxSteps = 10_000_000
			if err := s.Run(vmCompile(t, source, optimized)); err != nil {
				t.Fatal(err)
			}
			vmWant(t, vmStack(s), []any{int64(100000), int64(7), nil, int64(9)})
		})
	}
}

func TestVMCloseAndErrorObjects(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         []any
	}{
		{"error_identity", `local object={}; local seen; local saved
local ok,err=pcall(function()
 local x=41; saved=function() return x end
 local a<close> = setmetatable({}, {__close=function(_,e) seen=e end})
 error(object)
end)
local x=99
return ok,err==object,seen==object,saved()`, []any{false, true, true, int64(41)}},
		{"reverse_close_order", `local log=""; local object={}
local function r(n) return setmetatable({}, {__close=function(_,e) assert(e==object); log=log..n end}) end
local ok,err=pcall(function() local a<close> = r("A"); local b<close> = r("B"); error(object) end)
return ok,err==object,log`, []any{false, true, "BA"}},
		{"close_replaces_error", `local first,second={},{}; local seen
local ok,err=pcall(function()
 local a<close> = setmetatable({}, {__close=function(_,e) seen=e end})
 local b<close> = setmetatable({}, {__close=function(_,e) assert(e==first); error(second) end})
 error(first)
end)
return ok,err==second,seen==second`, []any{false, true, true}},
		{"xpcall_object", `local object={}; local handled={}
local ok,err=xpcall(function() error(object) end,function(e) assert(e==object); return handled end)
return ok,err==handled`, []any{false, true}},
	} {
		t.Run(tc.name, func(t *testing.T) { vmVariants(t, tc.source, tc.want, "", "") })
	}
}

func TestVMNumericLoopEdges(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         []any
		wantError    string
	}{
		{"max_integer", `local n=0; for i=0x7ffffffffffffffe,0x7fffffffffffffff do n=n+1 end; return n`, []any{int64(2)}, ""},
		{"min_integer", `local n=0; for i=-9223372036854775807,-9223372036854775807-1,-1 do n=n+1 end; return n`, []any{int64(2)}, ""},
		{"fractional_limit", `local n=0; for i=1,3.9 do n=n+i end; local m=0; for i=3,0.1,-1 do m=m+i end; return n,m`, []any{int64(6), int64(6)}, ""},
		{"float_control", `local n=0; local last; for i=1.0,2.0,0.5 do n=n+1; last=i end; return n,last,math.type(last)`, []any{int64(3), 2.0, "float"}, ""},
		{"empty_reverse", `local n=0; for i=1,5,-1 do n=n+1 end; return n`, []any{int64(0)}, ""},
		{"nan_limit", `local n=0; for i=1,0/0 do n=n+1 end; return n`, []any{int64(0)}, ""},
		{"string_coercion", `local n=0; for i="1","3","1" do n=n+i end; return n`, []any{6.0}, ""},
		{"zero_float_step", `for i=1.0,3.0,0.0 do end`, nil, "step is zero"},
		{"non_numeric_initial", `for i={},3 do end`, nil, "number"},
		{"non_numeric_limit", `for i=1,{} do end`, nil, "number"},
		{"non_numeric_step", `for i=1,3,{} do end`, nil, "number"},
	} {
		t.Run(tc.name, func(t *testing.T) { vmVariants(t, tc.source, tc.want, "", tc.wantError) })
	}
}

func TestVMManualPrototypeFixtures(t *testing.T) {
	t.Run("constants", func(t *testing.T) {
		p := &compile.Prototype{
			MaxStackSize: 8,
			Upvalues:     []compile.Upvalue{{Name: "_ENV", InStack: true}},
			Constants: []compile.Constant{compile.NilConstant(), compile.BoolConstant(false), compile.BoolConstant(true),
				compile.IntConstant(math.MinInt64), compile.FloatConstant(1.25), compile.FloatConstant(math.Copysign(0, -1)),
				compile.StringConstant("a\x00b"), compile.StringConstant(strings.Repeat("long", 30))},
		}
		for i := range p.Constants {
			p.Code = append(p.Code, compile.ABx(compile.OpLOADK, i, i))
		}
		p.Code = append(p.Code, compile.ABC(compile.OpRETURN, 0, 9, 0, false))
		s := vm.NewState()
		s.MaxSteps = 1_000_000
		if err := s.Run(p); err != nil {
			t.Fatal(err)
		}
		vmWant(t, vmStack(s), []any{nil, false, true, int64(math.MinInt64), 1.25, math.Copysign(0, -1), "a\x00b", strings.Repeat("long", 30)})
	})
	t.Run("child_capture", func(t *testing.T) {
		p := &compile.Prototype{
			MaxStackSize: 2,
			Upvalues:     []compile.Upvalue{{Name: "_ENV", InStack: true}},
			Code:         []compile.Instruction{compile.AsBx(compile.OpLOADI, 0, 41), compile.ABx(compile.OpCLOSURE, 1, 0), compile.ABC(compile.OpCALL, 1, 1, 2, false), compile.ABC(compile.OpRETURN, 1, 2, 0, true)},
			Children: []compile.Prototype{{MaxStackSize: 2, Upvalues: []compile.Upvalue{{Name: "x", InStack: true, Index: 0}},
				Code: []compile.Instruction{compile.ABC(compile.OpGETUPVAL, 0, 0, 0, false), compile.ABC(compile.OpRETURN1, 0, 0, 0, false)}}},
		}
		s := vm.NewState()
		s.MaxSteps = 1_000_000
		if err := s.Run(p); err != nil {
			t.Fatal(err)
		}
		vmWant(t, vmStack(s), []any{int64(41)})
	})
}

func TestVMHostErrorObjects(t *testing.T) {
	s := vm.NewState()
	s.MaxSteps = 1_000_000
	object := vm.NewTable()
	s.Globals.RawSet("object", object)
	if err := s.Run(vmCompile(t, `error(object)`, false)); err == nil {
		t.Fatal("Run accepted error(object)")
	} else {
		var luaErr vm.LuaError
		if !errors.As(err, &luaErr) || luaErr.Value != object {
			t.Fatalf("Run lost Lua error object: %T %v", err, err)
		}
	}
	s.Globals.RawSet("fail", vm.NativeFunction(func(_ *vm.State, _ []any) ([]any, error) {
		return nil, vm.LuaError{Value: object}
	}))
	if err := s.Run(vmCompile(t, `local ok,err=pcall(fail); return ok,err==object`, false)); err != nil {
		t.Fatal(err)
	}
	vmWant(t, vmStack(s), []any{false, true})
}

func TestVMMalformedJumps(t *testing.T) {
	for _, offset := range []int{-2, 10} {
		t.Run(fmt.Sprint(offset), func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("invalid jump panicked: %v", r)
				}
			}()
			s := vm.NewState()
			s.MaxSteps = 1_000_000
			p := &compile.Prototype{MaxStackSize: 2, Code: []compile.Instruction{
				compile.SJ(compile.OpJMP, offset), compile.ABC(compile.OpRETURN0, 0, 0, 0, false),
			}}
			if err := s.Run(p); err == nil {
				t.Fatal("out-of-range jump accepted")
			}
		})
	}
}

// Keep hand-built programs small: these exercise forms the compiler normally
// lowers to other opcodes, independently of its instruction selection.
func vmRunPrototype(t *testing.T, p *compile.Prototype) []any {
	t.Helper()
	s := vm.NewState()
	s.MaxSteps = 1_000_000
	s.Output = io.Discard
	if err := s.Run(p); err != nil {
		t.Fatal(err)
	}
	return vmStack(s)
}

func TestVMImmediateTableOpcodes(t *testing.T) {
	for _, key := range []int{0, 1, 255} {
		for _, constant := range []bool{false, true} {
			t.Run(fmt.Sprintf("index_%d/constant_%t", key, constant), func(t *testing.T) {
				value := 2
				if constant {
					value = 0
				}
				p := &compile.Prototype{MaxStackSize: 3, Constants: []compile.Constant{compile.StringConstant("value")}, Code: []compile.Instruction{
					compile.VABC(compile.OpNEWTABLE, 0, 0, 0, false), compile.Ax(compile.OpEXTRAARG, 0),
					compile.ABx(compile.OpLOADK, 2, 0), compile.ABC(compile.OpSETI, 0, key, value, constant),
					compile.ABC(compile.OpGETI, 1, 0, key, false), compile.ABC(compile.OpRETURN, 0, 3, 0, false),
				}}
				got := vmRunPrototype(t, p)
				table, ok := got[0].(*vm.Table)
				if !ok {
					t.Fatalf("table = %T", got[0])
				}
				if table.RawGet(int64(key)) != "value" {
					t.Errorf("RawGet(%d) = %#v", key, table.RawGet(int64(key)))
				}
				vmWant(t, got[1:], []any{"value"})
			})
		}
	}
	t.Run("metamethod_integer_keys", func(t *testing.T) {
		s := vm.NewState()
		s.MaxSteps = 1_000_000
		target := vm.NewTable()
		mt := vm.NewTable()
		calls := 0
		_ = mt.RawSet("__newindex", vm.NativeFunction(func(_ *vm.State, args []any) ([]any, error) {
			vmWant(t, args, []any{target, int64(255), int64(42)})
			calls++
			return nil, nil
		}))
		_ = mt.RawSet("__index", vm.NativeFunction(func(_ *vm.State, args []any) ([]any, error) {
			vmWant(t, args, []any{target, int64(0)})
			calls++
			return []any{"missing"}, nil
		}))
		target.Metatable = mt
		_ = s.Globals.RawSet("target", target)
		p := &compile.Prototype{MaxStackSize: 2, Upvalues: []compile.Upvalue{{Name: "_ENV"}}, Constants: []compile.Constant{compile.StringConstant("target"), compile.IntConstant(42)}, Code: []compile.Instruction{
			compile.ABC(compile.OpGETTABUP, 0, 0, 0, false), compile.ABC(compile.OpSETI, 0, 255, 1, true),
			compile.ABC(compile.OpGETI, 1, 0, 0, false), compile.ABC(compile.OpRETURN1, 1, 0, 0, false),
		}}
		if err := s.Run(p); err != nil {
			t.Fatal(err)
		}
		vmWant(t, vmStack(s), []any{"missing"})
		if calls != 2 {
			t.Fatalf("metamethod calls = %d, want 2", calls)
		}
	})
}

func TestVMTestSetOpcode(t *testing.T) {
	for _, value := range []compile.Constant{compile.NilConstant(), compile.BoolConstant(false), compile.BoolConstant(true), compile.IntConstant(0)} {
		for _, k := range []bool{false, true} {
			t.Run(fmt.Sprintf("kind_%d/bits_%d/k_%t", value.Kind, value.Bits, k), func(t *testing.T) {
				p := &compile.Prototype{MaxStackSize: 2, Constants: []compile.Constant{value}, Code: []compile.Instruction{
					compile.AsBx(compile.OpLOADI, 0, 99), compile.ABx(compile.OpLOADK, 1, 0),
					compile.ABC(compile.OpTESTSET, 0, 1, 0, k), compile.SJ(compile.OpJMP, 1),
					compile.AsBx(compile.OpLOADI, 1, 7), compile.ABC(compile.OpRETURN, 0, 3, 0, false),
				}}
				var source any
				switch value.Kind {
				case compile.ConstantBoolean:
					source = value.Bits != 0
				case compile.ConstantInteger:
					source = int64(value.Bits)
				}
				truthy := source != nil && source != false
				want := []any{int64(99), int64(7)}
				if truthy == k {
					want = []any{source, source}
				}
				vmWant(t, vmRunPrototype(t, p), want)
			})
		}
	}
}

func TestVMImmediateComparisons(t *testing.T) {
	for _, op := range []compile.OpCode{compile.OpEQI, compile.OpLTI, compile.OpLEI, compile.OpGTI, compile.OpGEI} {
		for _, floatImmediate := range []bool{false, true} {
			for _, value := range []int{-128, -1, 0, 128} {
				for _, k := range []bool{false, true} {
					t.Run(fmt.Sprintf("op_%d/float_%t/value_%d/k_%t", op, floatImmediate, value, k), func(t *testing.T) {
						c := 0
						if floatImmediate {
							c = 1
						}
						p := &compile.Prototype{MaxStackSize: 2, Code: []compile.Instruction{
							compile.AsBx(compile.OpLOADI, 0, value), compile.ABC(op, 0, 126, c, k),
							compile.SJ(compile.OpJMP, 2), compile.ABC(compile.OpLOADFALSE, 1, 0, 0, false), compile.SJ(compile.OpJMP, 1),
							compile.ABC(compile.OpLOADTRUE, 1, 0, 0, false), compile.ABC(compile.OpRETURN1, 1, 0, 0, false),
						}}
						condition := false
						switch op {
						case compile.OpEQI:
							condition = value == -1
						case compile.OpLTI:
							condition = value < -1
						case compile.OpLEI:
							condition = value <= -1
						case compile.OpGTI:
							condition = value > -1
						case compile.OpGEI:
							condition = value >= -1
						}
						vmWant(t, vmRunPrototype(t, p), []any{condition == k})
					})
				}
			}
		}
	}
	t.Run("float_metamethod_operand_and_order", func(t *testing.T) {
		for _, op := range []compile.OpCode{compile.OpLTI, compile.OpLEI, compile.OpGTI, compile.OpGEI} {
			s := vm.NewState()
			s.MaxSteps = 1_000_000
			target := vm.NewTable()
			mt := vm.NewTable()
			called := false
			event := "__lt"
			if op == compile.OpLEI || op == compile.OpGEI {
				event = "__le"
			}
			_ = mt.RawSet(event, vm.NativeFunction(func(_ *vm.State, args []any) ([]any, error) {
				want := []any{target, -1.0}
				if op == compile.OpGTI || op == compile.OpGEI {
					want = []any{-1.0, target}
				}
				vmWant(t, args, want)
				called = true
				return []any{true}, nil
			}))
			target.Metatable = mt
			_ = s.Globals.RawSet("target", target)
			p := &compile.Prototype{MaxStackSize: 2, Upvalues: []compile.Upvalue{{Name: "_ENV"}}, Constants: []compile.Constant{compile.StringConstant("target")}, Code: []compile.Instruction{
				compile.ABC(compile.OpGETTABUP, 0, 0, 0, false), compile.ABC(op, 0, 126, 1, true), compile.SJ(compile.OpJMP, 1),
				compile.ABC(compile.OpRETURN0, 0, 0, 0, false), compile.ABC(compile.OpRETURN1, 0, 0, 0, false),
			}}
			if err := s.Run(p); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatalf("op %d did not call %s", op, event)
			}
		}
	})
}

func TestVMImmediateShiftOpcodes(t *testing.T) {
	for _, tc := range []struct {
		op                        compile.OpCode
		register, immediate, want int64
	}{
		{compile.OpSHLI, 2, 3, 12}, {compile.OpSHLI, -2, 16, 4},
		{compile.OpSHRI, 16, 2, 4}, {compile.OpSHRI, 3, -2, 12},
		{compile.OpSHRI, -1, 1, math.MaxInt64}, {compile.OpSHRI, 1, 64, 0},
	} {
		t.Run(fmt.Sprintf("op_%d/%d/%d", tc.op, tc.register, tc.immediate), func(t *testing.T) {
			event := 17
			flip := false
			if tc.op == compile.OpSHLI {
				event = 16
				flip = true
			}
			p := &compile.Prototype{MaxStackSize: 2, Code: []compile.Instruction{
				compile.AsBx(compile.OpLOADI, 0, int(tc.register)), compile.ABC(tc.op, 1, 0, int(tc.immediate)+127, false),
				compile.ABC(compile.OpMMBINI, 0, int(tc.immediate)+127, event, flip), compile.ABC(compile.OpRETURN1, 1, 0, 0, false),
			}}
			vmWant(t, vmRunPrototype(t, p), []any{tc.want})
		})
	}
}

func TestVMArithmeticFallbackOperands(t *testing.T) {
	for _, tc := range []struct {
		name         string
		op, fallback compile.OpCode
		event        int
		immediate    int
		flip         bool
	}{
		{"MMBIN", compile.OpADD, compile.OpMMBIN, 6, 0, false},
		{"MMBIN_flip", compile.OpSUB, compile.OpMMBIN, 7, 0, true},
		{"MMBINI", compile.OpADDI, compile.OpMMBINI, 6, 4, false},
		{"MMBINI_flip", compile.OpADDI, compile.OpMMBINI, 6, 4, true},
		{"MMBINK", compile.OpSUBK, compile.OpMMBINK, 7, 0, false},
		{"MMBINK_flip", compile.OpSUBK, compile.OpMMBINK, 7, 0, true},
		{"SHLI_flip", compile.OpSHLI, compile.OpMMBINI, 16, 4, true},
		{"SHRI", compile.OpSHRI, compile.OpMMBINI, 17, -2, false},
		// ADDI may encode subtraction by negating the immediate; the companion
		// retains the original immediate and metamethod event.
		{"ADDI_subtraction", compile.OpADDI, compile.OpMMBINI, 7, 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := vm.NewState()
			s.MaxSteps = 1_000_000
			target := vm.NewTable()
			mt := vm.NewTable()
			called := false
			event := map[int]string{6: "__add", 7: "__sub", 16: "__shl", 17: "__shr"}[tc.event]
			right := int64(4)
			if tc.fallback == compile.OpMMBINI {
				right = int64(tc.immediate)
			}
			_ = mt.RawSet(event, vm.NativeFunction(func(_ *vm.State, args []any) ([]any, error) {
				want := []any{target, right}
				if tc.flip {
					want = []any{right, target}
				}
				vmWant(t, args, want)
				called = true
				return []any{"fallback"}, nil
			}))
			target.Metatable = mt
			_ = s.Globals.RawSet("target", target)
			b, c := 0, 1
			fallbackB := 1
			switch tc.fallback {
			case compile.OpMMBINI:
				c = tc.immediate + 127
				fallbackB = c
				if tc.name == "ADDI_subtraction" {
					c = 127 - tc.immediate
				}
			case compile.OpMMBINK:
				c = 1
			}
			p := &compile.Prototype{MaxStackSize: 3, Upvalues: []compile.Upvalue{{Name: "_ENV"}}, Constants: []compile.Constant{compile.StringConstant("target"), compile.IntConstant(4)}, Code: []compile.Instruction{
				compile.ABC(compile.OpGETTABUP, 0, 0, 0, false), compile.ABx(compile.OpLOADK, 1, 1),
				compile.ABC(tc.op, 2, b, c, false), compile.ABC(tc.fallback, 0, fallbackB, tc.event, tc.flip),
				compile.ABC(compile.OpRETURN1, 2, 0, 0, false),
			}}
			if err := s.Run(p); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("arithmetic fallback not called")
			}
			vmWant(t, vmStack(s), []any{"fallback"})
		})
	}
}

func TestVMLoadKXAndMetadata(t *testing.T) {
	t.Run("wide_pool", func(t *testing.T) {
		p := &compile.Prototype{MaxStackSize: 2, Constants: make([]compile.Constant, compile.MaxArgBx+2), Code: []compile.Instruction{
			compile.ABx(compile.OpLOADKX, 0, 0), compile.Ax(compile.OpEXTRAARG, compile.MaxArgBx+1), compile.ABC(compile.OpRETURN1, 0, 0, 0, false),
		}}
		p.Constants[compile.MaxArgBx+1] = compile.StringConstant("wide")
		vmWant(t, vmRunPrototype(t, p), []any{"wide"})
	})
	for _, tc := range []struct {
		name string
		p    *compile.Prototype
	}{
		{"wrong_extraarg", &compile.Prototype{MaxStackSize: 2, Code: []compile.Instruction{compile.ABx(compile.OpLOADKX, 0, 0), compile.AsBx(compile.OpLOADI, 0, 1)}}},
		{"wide_constant_outside_pool", &compile.Prototype{MaxStackSize: 2, Code: []compile.Instruction{compile.ABx(compile.OpLOADKX, 0, 0), compile.Ax(compile.OpEXTRAARG, compile.MaxArgAx)}}},
		{"params_outside_frame", &compile.Prototype{MaxStackSize: 2, NumParams: 3}},
		{"missing_vararg_slot", &compile.Prototype{MaxStackSize: 2, NumParams: 2, IsVararg: true}},
		{"vararg_table_without_varargs", &compile.Prototype{MaxStackSize: 2, VarargTable: true}},
		{"varargprep_operands", &compile.Prototype{MaxStackSize: 2, IsVararg: true, Code: []compile.Instruction{compile.ABC(compile.OpVARARGPREP, 1, 0, 0, false)}}},
		{"bad_boolean", &compile.Prototype{MaxStackSize: 2, Constants: []compile.Constant{{Kind: compile.ConstantBoolean, Bits: 2}}, Code: []compile.Instruction{compile.ABx(compile.OpLOADK, 0, 0)}}},
		{"bad_constant_tag", &compile.Prototype{MaxStackSize: 2, Constants: []compile.Constant{{Kind: compile.ConstantKind(255)}}, Code: []compile.Instruction{compile.ABx(compile.OpLOADK, 0, 0)}}},
		{"bad_child_capture", &compile.Prototype{MaxStackSize: 2, Children: []compile.Prototype{{MaxStackSize: 2, Upvalues: []compile.Upvalue{{InStack: true, Index: 2}}}}, Code: []compile.Instruction{compile.ABx(compile.OpCLOSURE, 0, 0)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := vm.NewState()
			s.MaxSteps = 1000
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Go panic: %v", r)
				}
			}()
			if err := s.Run(tc.p); err == nil {
				t.Fatal("invalid bytecode/metadata accepted")
			}
		})
	}
}

func TestVMNativeAPIStackIsolation(t *testing.T) {
	for _, mode := range []string{"Invoke", "Call", "Lua", "nested_Invoke"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/error_%t", mode, fail), func(t *testing.T) {
				s := vm.NewState()
				s.MaxSteps = 1_000_000
				s.PushString("caller")
				sentinel := errors.New("native error")
				fn := vm.NativeFunction(func(state *vm.State, args []any) ([]any, error) {
					vmWant(t, args, []any{int64(7), nil})
					if got := vmStack(state); !reflect.DeepEqual(got, args) {
						t.Errorf("native API stack = %#v, want arguments %#v", got, args)
					}
					// Discarding a native frame must never clear the caller's backing array.
					if err := state.SetTop(0); err != nil {
						t.Fatal(err)
					}
					state.PushString("scratch")
					if fail {
						return nil, sentinel
					}
					return []any{int64(42)}, nil
				})
				var got []any
				var err error
				switch mode {
				case "Invoke":
					got, err = s.Invoke(fn, int64(7), nil)
				case "Call":
					s.Push(fn)
					s.PushInteger(7)
					s.PushNil()
					err = s.Call(2, vm.MultRet)
					if !fail {
						got = []any{s.At(-1)}
						if popErr := s.Pop(1); popErr != nil {
							t.Fatal(popErr)
						}
					}
				case "Lua":
					_ = s.Globals.RawSet("native", fn)
					if loadErr := s.Load(vmCompile(t, `return native(7,nil)`, false)); loadErr != nil {
						t.Fatal(loadErr)
					}
					chunk := s.At(-1)
					if popErr := s.Pop(1); popErr != nil {
						t.Fatal(popErr)
					}
					got, err = s.Invoke(chunk)
				case "nested_Invoke":
					outer := vm.NativeFunction(func(state *vm.State, _ []any) ([]any, error) {
						state.PushString("outer scratch")
						before := vmStack(state)
						results, callErr := state.Invoke(fn, int64(7), nil)
						vmWant(t, vmStack(state), before)
						return results, callErr
					})
					got, err = s.Invoke(outer, "outer argument")
				}
				if fail {
					if err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
						t.Errorf("error = %v", err)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					vmWant(t, got, []any{int64(42)})
				}
				vmWant(t, vmStack(s), []any{"caller"})
			})
		}
	}
}

func TestVMLoadAndTypedStackAPI(t *testing.T) {
	s := vm.NewState()
	s.MaxSteps = 1_000_000
	s.PushNil()
	s.PushBoolean(true)
	s.PushInteger(7)
	s.PushNumber(1.5)
	s.PushString("s")
	vmWant(t, vmStack(s), []any{nil, true, int64(7), 1.5, "s"})
	if err := s.SetTop(-2); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTop(6); err != nil {
		t.Fatal(err)
	}
	vmWant(t, vmStack(s), []any{nil, true, int64(7), 1.5, nil, nil})
	if err := s.SetTop(-8); err == nil {
		t.Fatal("SetTop accepted negative size")
	}
	if err := s.SetTop(0); err != nil {
		t.Fatal(err)
	}
	s.PushInteger(11)
	if err := s.SetGlobal("host"); err != nil {
		t.Fatal(err)
	}
	if s.GetTop() != 0 {
		t.Fatal("SetGlobal did not pop its value")
	}
	if err := s.GetGlobal("host"); err != nil {
		t.Fatal(err)
	}
	vmWant(t, vmStack(s), []any{int64(11)})
	p := vmCompile(t, `host=host+1; return host`, false)
	if err := s.Load(p); err != nil {
		t.Fatal(err)
	}
	if s.Globals.RawGet("host") != int64(11) {
		t.Fatal("Load executed chunk")
	}
	if err := s.Call(0, 1); err != nil {
		t.Fatal(err)
	}
	vmWant(t, vmStack(s), []any{int64(11), int64(12)})
	if err := s.SetTop(0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGlobal("empty"); err == nil {
		t.Fatal("SetGlobal accepted empty stack")
	}
	if err := s.GetGlobal("missing"); err != nil {
		t.Fatal(err)
	}
	vmWant(t, vmStack(s), []any{nil})
}

func TestVMCancellationClosesResources(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := vm.NewState()
	s.MaxSteps = 1000
	s.Context = ctx
	resource := vm.NewTable()
	mt := vm.NewTable()
	closed := 0
	_ = mt.RawSet("__close", vm.NativeFunction(func(_ *vm.State, args []any) ([]any, error) {
		if len(args) != 2 || args[0] != resource || args[1] == nil {
			t.Errorf("close arguments = %#v", args)
		}
		closed++
		return nil, nil
	}))
	resource.Metatable = mt
	_ = s.Globals.RawSet("resource", resource)
	_ = s.Globals.RawSet("cancel", vm.NativeFunction(func(_ *vm.State, _ []any) ([]any, error) { cancel(); return nil, nil }))
	p := vmCompile(t, `local x=41; saved=function() return x end; local r<close> = resource; cancel(); while true do end`, false)
	if err := s.Run(p); !errors.Is(err, context.Canceled) {
		t.Errorf("Run error = %v, want context.Canceled", err)
	}
	if closed != 1 {
		t.Errorf("__close calls = %d, want 1", closed)
	}
	// Error unwinding must still detach captures and leave the State reusable.
	s.Context = context.Background()
	got, err := s.Invoke(s.Globals.RawGet("saved"))
	if err != nil {
		t.Fatal(err)
	}
	vmWant(t, got, []any{int64(41)})
	if err := s.Run(vmCompile(t, `return 9`, false)); err != nil {
		t.Fatal(err)
	}
	vmWant(t, vmStack(s), []any{int64(9)})
}

func TestVMInstructionLimitClosesResources(t *testing.T) {
	s := vm.NewState()
	s.MaxSteps = 100
	resource := vm.NewTable()
	mt := vm.NewTable()
	closed := 0
	_ = mt.RawSet("__close", vm.NativeFunction(func(_ *vm.State, _ []any) ([]any, error) { closed++; return nil, nil }))
	resource.Metatable = mt
	_ = s.Globals.RawSet("resource", resource)
	if err := s.Run(vmCompile(t, `local r<close> = resource; while true do end`, false)); err == nil || !strings.Contains(err.Error(), "instruction limit") {
		t.Fatalf("Run error = %v", err)
	}
	if closed != 1 {
		t.Errorf("__close calls = %d, want 1", closed)
	}
	if err := s.Run(vmCompile(t, `return 1`, false)); err != nil {
		t.Fatal(err)
	}
}

func TestVMCriticalNumericLoops(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         []any
	}{
		{"min_step", `local n=0; local last; for i=0x7fffffffffffffff,-9223372036854775807-1,-9223372036854775807-1 do n=n+1; last=i end; return n,last`, []any{int64(2), int64(-1)}},
		{"unsigned_counter", `local n=0; local last; for i=-9223372036854775807-1,0x7fffffffffffffff,0x7fffffffffffffff do n=n+1; last=i end; return n,last`, []any{int64(3), int64(math.MaxInt64 - 1)}},
		{"positive_infinite_limit", `local n=0; for i=0x7ffffffffffffffe,1/0 do n=n+1 end; return n`, []any{int64(2)}},
		{"negative_infinite_limit", `local n=0; for i=-9223372036854775807,-1/0,-1 do n=n+1 end; return n`, []any{int64(2)}},
		{"empty_infinite_limit", `local n=0; for i=1,-1/0 do n=n+1 end; for i=1,1/0,-1 do n=n+1 end; return n`, []any{int64(0)}},
		{"float_nan_limit", `local n=0; for i=1.0,0/0,1.0 do n=n+1 end; return n`, []any{int64(1)}},
		{"float_nan_initial", `local n=0; for i=0/0,3.0,1.0 do n=n+1 end; return n`, []any{int64(1)}},
		{"float_nan_step", `local n=0; for i=3.0,1.0,0/0 do n=n+1 end; return n`, []any{int64(1)}},
	} {
		t.Run(tc.name, func(t *testing.T) { vmVariants(t, tc.source, tc.want, "", "") })
	}
}

// These fuzz inputs have no _ENV or host functions. Frame sizes, code lengths,
// and execution budgets are bounded. Exclude exponential string growth and
// materialized-vararg allocation: this checks panics, not memory exhaustion.
func vmRunMalformedBytes(t *testing.T, data []byte) {
	t.Helper()
	if len(data) < 6 {
		return
	}
	if len(data) > 6+4*32 {
		data = data[:6+4*32]
	}
	code := make([]compile.Instruction, 0, (len(data)-6)/4)
	for offset := 6; offset+4 <= len(data); offset += 4 {
		i := compile.Instruction(binary.LittleEndian.Uint32(data[offset : offset+4]))
		if i.Op() == compile.OpCONCAT {
			i = compile.ABC(compile.OpMOVE, int(i.A()), int(i.B()), 0, false)
		}
		if i.Op() == compile.OpVARARG {
			i = i.SetK(0)
		}
		code = append(code, i)
	}
	constants := []compile.Constant{
		compile.NilConstant(), compile.BoolConstant(false), compile.BoolConstant(true), compile.IntConstant(-1),
		compile.FloatConstant(math.NaN()), compile.StringConstant("n"),
		{Kind: compile.ConstantBoolean, Bits: 2}, {Kind: compile.ConstantKind(255)},
	}
	p := &compile.Prototype{MaxStackSize: 2 + data[0]%15, NumParams: data[1], IsVararg: data[2]&1 != 0, VarargTable: data[2]&2 != 0, Code: code, Constants: constants}
	p.Children = []compile.Prototype{{MaxStackSize: 2 + data[3]%15, NumParams: data[4], IsVararg: data[5]&1 != 0, VarargTable: data[5]&2 != 0,
		Code: code, Constants: constants, Upvalues: []compile.Upvalue{{InStack: data[5]&4 != 0, Index: data[3]}}}}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("State.Run leaked Go panic: %v; input %x", r, data)
		}
	}()
	s := vm.NewState()
	s.Output = io.Discard
	s.MaxSteps = 1000
	_ = s.Run(p) // Either normal termination or a VM error is acceptable here.
}

func vmMalformedSeeds() [][]byte {
	seeds := [][]byte{make([]byte, 6)}
	// Seed valid instruction pairs and loops so mutations reach past the first
	// instruction's register/companion checks, as well as the rejection paths.
	for _, code := range [][]compile.Instruction{
		{compile.ABx(compile.OpLOADKX, 0, 0), compile.Ax(compile.OpEXTRAARG, 2), compile.ABC(compile.OpRETURN1, 0, 0, 0, false)},
		{compile.VABC(compile.OpNEWTABLE, 0, 0, 0, false), compile.Ax(compile.OpEXTRAARG, 0), compile.AsBx(compile.OpLOADI, 1, 3), compile.ABC(compile.OpSETI, 0, 0, 1, false), compile.ABC(compile.OpGETI, 2, 0, 0, false), compile.ABC(compile.OpRETURN1, 2, 0, 0, false)},
		{compile.AsBx(compile.OpLOADI, 0, 1), compile.ABC(compile.OpADDI, 1, 0, 128, false), compile.ABC(compile.OpMMBINI, 0, 128, 6, false), compile.ABC(compile.OpRETURN1, 1, 0, 0, false)},
		{compile.AsBx(compile.OpLOADI, 0, 1), compile.AsBx(compile.OpLOADI, 1, 3), compile.AsBx(compile.OpLOADI, 2, 1), compile.ABx(compile.OpFORPREP, 0, 1), compile.ABC(compile.OpMOVE, 3, 2, 0, false), compile.ABx(compile.OpFORLOOP, 0, 2), compile.ABC(compile.OpRETURN1, 3, 0, 0, false)},
		{compile.ABC(compile.OpLOADTRUE, 0, 0, 0, false), compile.ABC(compile.OpTESTSET, 1, 0, 0, true), compile.SJ(compile.OpJMP, 1), compile.ABC(compile.OpLOADFALSE, 1, 0, 0, false), compile.ABC(compile.OpRETURN1, 1, 0, 0, false)},
		{compile.ABC(compile.OpVARARGPREP, 0, 0, 0, false), compile.ABC(compile.OpVARARG, 0, 0, 4, false), compile.ABC(compile.OpRETURN, 0, 0, 0, false)},
		{compile.AsBx(compile.OpLOADI, 0, 7), compile.ABx(compile.OpCLOSURE, 1, 0), compile.ABC(compile.OpCALL, 1, 1, 2, false), compile.ABC(compile.OpRETURN1, 1, 0, 0, false)},
	} {
		data := make([]byte, 6+4*len(code))
		data[0] = 6
		data[3] = 6
		data[5] = 4
		if code[0].Op() == compile.OpVARARGPREP {
			data[2] = 1
		}
		for i, instruction := range code {
			binary.LittleEndian.PutUint32(data[6+4*i:], uint32(instruction))
		}
		seeds = append(seeds, data)
	}
	for op := 0; op < 128; op++ {
		for _, fields := range []uint32{0, 0xffffff80} {
			data := make([]byte, 6+8)
			data[0] = 6
			data[3] = 6
			binary.LittleEndian.PutUint32(data[6:], uint32(op)|fields)
			binary.LittleEndian.PutUint32(data[10:], uint32(compile.ABC(compile.OpRETURN0, 0, 0, 0, false)))
			seeds = append(seeds, data)
		}
	}
	return seeds
}

func TestVMBoundedMalformedBytecodeCorpus(t *testing.T) {
	for i, data := range vmMalformedSeeds() {
		t.Run(fmt.Sprint(i), func(t *testing.T) { vmRunMalformedBytes(t, data) })
	}
}

func FuzzVMRunMalformedBytecode(f *testing.F) {
	for _, data := range vmMalformedSeeds() {
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) { vmRunMalformedBytes(t, data) })
}

func TestVMConstantComparisonOpcode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		left, right compile.Constant
		equal       bool
	}{
		{"integer_float", compile.IntConstant(-1), compile.FloatConstant(-1), true},
		{"different_numbers", compile.IntConstant(1), compile.IntConstant(2), false},
		{"binary_strings", compile.StringConstant("a\x00b"), compile.StringConstant("a\x00b"), true},
		{"different_types", compile.StringConstant("1"), compile.IntConstant(1), false},
		{"nil", compile.NilConstant(), compile.NilConstant(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &compile.Prototype{MaxStackSize: 2, Constants: []compile.Constant{tc.left, tc.right}, Code: []compile.Instruction{
				compile.ABx(compile.OpLOADK, 0, 0), compile.ABC(compile.OpEQK, 0, 1, 0, true), compile.SJ(compile.OpJMP, 2),
				compile.ABC(compile.OpLOADFALSE, 1, 0, 0, false), compile.SJ(compile.OpJMP, 1), compile.ABC(compile.OpLOADTRUE, 1, 0, 0, false), compile.ABC(compile.OpRETURN1, 1, 0, 0, false),
			}}
			vmWant(t, vmRunPrototype(t, p), []any{tc.equal})
		})
	}
}
