package vm_test

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/robogg133/glua/compile"
	"github.com/robogg133/glua/lex"
	"github.com/robogg133/glua/parser"
	"github.com/robogg133/glua/tokens"
	"github.com/robogg133/glua/vm"
)

func libraryCompile(t *testing.T, source string) *compile.Prototype {
	t.Helper()
	ast := parser.NewAst(tokens.NewTokenizer(lex.NewLexer(source), "library-test"))
	if e := ast.Next(); e != nil {
		t.Fatal(e)
	}
	p, e := compile.Compile(ast)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func libraryRun(t *testing.T, source string) (*vm.State, []any, string) {
	t.Helper()
	var s vm.State
	var output bytes.Buffer
	s.Output = &output
	if e := s.Run(libraryCompile(t, source)); e != nil {
		t.Fatal(e)
	}
	r := make([]any, s.GetTop())
	for i := range r {
		r[i] = s.At(i + 1)
	}
	return &s, r, output.String()
}
func libraryFunction(t *testing.T, s *vm.State, name string) any {
	t.Helper()
	parts := strings.Split(name, ".")
	var v any = s.Globals
	for _, p := range parts {
		table, ok := v.(*vm.Table)
		if !ok {
			t.Fatalf("%s: non-table namespace", name)
		}
		v = table.RawGet(p)
	}
	if v == nil {
		t.Fatalf("missing function %s", name)
	}
	return v
}
func libraryWant(t *testing.T, got, want []any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("results = %#v, want %#v", got, want)
	}
}

func TestLibrariesBase(t *testing.T) {
	_, r, out := libraryRun(t, `print("hello", 42, false, nil)
 local obj = {}
 local ok, value = pcall(error, obj)
 local ok2, value2 = xpcall(function() error(obj) end, function(e) return e end)
 return _G == _ENV, _VERSION, type(print), type(nil), tostring(1.0),
 tonumber(" 0xff "), tonumber("ff", 16), tonumber("no"), select("#",1,nil,3),
 select(-1,1,2,3), ok, value == obj, ok2, value2 == obj, assert(0)`)
	libraryWant(t, r, []any{true, "Lua 5.5", "function", "nil", "1.0", int64(255), int64(255), nil, int64(3), int64(3), false, true, false, true, int64(0)})
	if out != "hello\t42\tfalse\tnil\n" {
		t.Fatalf("print = %q", out)
	}
}
func TestLibrariesMetatablesAndIteration(t *testing.T) {
	_, r, out := libraryRun(t, `local t = setmetatable({10,20}, {
 __tostring=function() return "custom" end,
 __len=function() return 99 end,
 __metatable="locked"
 })
 print(t)
 local sum=0
 for i,v in ipairs(t) do sum=sum+i+v end
 local count=0
 for k,v in pairs(t) do count=count+1 end
 local ok=pcall(setmetatable,t,{})
 rawset(t,"x",7)
 return getmetatable(t),rawlen(t),#t,sum,count,ok,rawget(t,"x"),rawequal(t,t),rawequal(1,1.0)`)
	libraryWant(t, r, []any{"locked", int64(2), int64(99), int64(33), int64(2), false, int64(7), true, true})
	if out != "custom\n" {
		t.Fatalf("print = %q", out)
	}
	_, r, _ = libraryRun(t, `local t=setmetatable({}, {__pairs=function(x) return next,{answer=42},nil end})
 for k,v in pairs(t) do return k,v end`)
	libraryWant(t, r, []any{"answer", int64(42)})
}
func TestLibrariesMath(t *testing.T) {
	_, r, _ := libraryRun(t, `math.randomseed(1,2)
 local r=math.random(-2,2)
 local i,f=math.modf(-3.5)
 return math.abs(-3),math.abs(-3.5),math.floor(2.9),math.ceil(-2.9),
 math.type(1),math.type(1.0),math.type("1"),math.tointeger("2.0"),math.tointeger(2.5),
 math.min(3,1,2),math.max(3,1,2),math.fmod(-7,3),i,f,math.ult(-1,1),r>=-2 and r<=2,
 math.sqrt(9),math.log(8,2),math.mininteger,math.maxinteger`)
	libraryWant(t, r, []any{int64(3), float64(3.5), int64(2), int64(-2), "integer", "float", nil, int64(2), nil, int64(1), int64(3), int64(-1), int64(-3), float64(-0.5), false, true, float64(3), float64(3), int64(-9223372036854775808), int64(9223372036854775807)})
	s := vm.NewState()
	seed := libraryFunction(t, s, "math.randomseed")
	random := libraryFunction(t, s, "math.random")
	_, e := s.Invoke(seed, int64(123), int64(456))
	if e != nil {
		t.Fatal(e)
	}
	first, e := s.Invoke(random)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = s.Invoke(seed, int64(123), int64(456))
	second, e := s.Invoke(random)
	if e != nil {
		t.Fatal(e)
	}
	libraryWant(t, second, first)
}
func TestLibrariesTables(t *testing.T) {
	_, r, _ := libraryRun(t, `local t={3,1,2}
 table.sort(t)
 table.insert(t,2,9)
 local removed=table.remove(t,2)
 table.move(t,1,2,2)
 local p=table.pack(1,nil,3)
 local a,b,c=table.unpack(p,1,p.n)
 local desc={1,3,2};table.sort(desc,function(a,b)return a>b end)
 return table.concat(t,","),removed,p.n,a,b,c,table.concat(desc,":")`)
	libraryWant(t, r, []any{"1,1,2", int64(9), int64(3), int64(1), nil, int64(3), "3:2:1"})
	_, r, _ = libraryRun(t, `local backing={3,2,1}
 local t=setmetatable({}, {__len=function()return #backing end,__index=backing,__newindex=backing})
 table.sort(t);table.insert(t,4);return table.concat(t,",")`)
	libraryWant(t, r, []any{"1,2,3,4"})
}
func TestLibrariesStrings(t *testing.T) {
	_, r, _ := libraryRun(t, `local a,b=string.find("a.b.c",".",1,true)
 local c,d=string.find("abcdef","cd",-4)
 local repl,n=string.gsub("aba","a","[%0]")
 local repl2,n2=string.gsub("aba","a",function(x)return false end)
 local parts={};for x in string.gmatch("ababa","ab") do table.insert(parts,x) end
 return string.len("é"),string.sub("abcdef",-3,-1),string.byte("abc",2,3),
 string.char(65,0,255),string.lower("ÉABC"),string.upper("éabc"),
 string.reverse("abc"),string.rep("x",3,":"),a,b,c,d,string.match("abc","b"),
 repl,n,repl2,n2,table.concat(parts,",")`)
	// Only the first value of a non-final multiple-return expression is retained.
	libraryWant(t, r, []any{int64(2), "def", int64(98), "A\x00\xff", "Éabc", "éABC", "cba", "x:x:x", int64(2), int64(2), int64(3), int64(4), "b", "[a]b[a]", int64(2), "aba", int64(2), "ab,ab"})
	_, r, _ = libraryRun(t, `local a,n=string.gsub("ab","","-");local b,m=string.gsub("aba","a",{a="x"});return a,n,b,m,string.find("ab","",3,true)`)
	libraryWant(t, r, []any{"-a-b-", int64(3), "xbx", int64(2), int64(3), int64(2)})
}
func TestLibrariesUTF8(t *testing.T) {
	_, r, _ := libraryRun(t, `local s=utf8.char(65,233,128512)
 local positions={};for p,c in utf8.codes(s) do table.insert(positions,p) end
 local n,bad=utf8.len("a"..string.char(255))
 return s,utf8.len(s),utf8.codepoint(s,2),utf8.offset(s,2),utf8.offset(s,-1),
 utf8.offset(s,0,3),table.concat(positions,","),n,bad`)
	libraryWant(t, r, []any{"Aé😀", int64(3), int64(233), int64(2), int64(4), int64(2), "1,2,4", nil, int64(2)})
}
func TestLibrariesErrors(t *testing.T) {
	s := vm.NewState()
	for _, tc := range []struct {
		name string
		args []any
		want string
	}{
		{"type", nil, "argument #1"}, {"assert", []any{false}, "assertion failed"},
		{"tonumber", []any{"1", int64(1)}, "base out of range"},
		{"rawset", []any{vm.NewTable(), nil, int64(1)}, "table index"},
		{"next", []any{vm.NewTable(), "missing"}, "invalid key"},

		{"string.gsub", []any{"abc", "a", "%2"}, "invalid capture"},
		{"string.char", []any{int64(256)}, "out of range"},
		{"string.sub", []any{"abc"}, "argument #2"},
		{"math.random", []any{int64(2), int64(1)}, "interval is empty"},
		{"math.fmod", []any{int64(2), int64(0)}, "zero"},
		{"utf8.codepoint", []any{"\xff"}, "invalid UTF-8"},
		{"utf8.offset", []any{"é", int64(1), int64(2)}, "continuation byte"},
		{"utf8.len", []any{"abc", int64(1), int64(-1), true}, "lax decoding unsupported"},
		{"collectgarbage", []any{"stop"}, "unsupported by Go GC"},
		{"string.pack", []any{"i", int64(1)}, "unsupported"},
		{"load", []any{false}, "argument #1"},
		{"load", []any{"return 1", false}, "argument #2"},
		{"load", []any{"return 1", "name", false}, "argument #3"},
		{"loadfile", []any{nil, false}, "argument #2"},
		{"package.searchpath", []any{"module"}, "argument #2"},
	} {
		t.Run(tc.name+fmt.Sprint(tc.args), func(t *testing.T) {
			_, e := s.Invoke(libraryFunction(t, s, tc.name), tc.args...)
			if e == nil || !strings.Contains(e.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", e, tc.want)
			}
		})
	}
	obj := vm.NewTable()
	_, e := s.Invoke(libraryFunction(t, s, "error"), obj)
	var pointer *vm.LuaError
	var value vm.LuaError
	if errors.As(e, &pointer) {
		if pointer.Value != obj {
			t.Fatal("error object changed")
		}
	} else if errors.As(e, &value) {
		if value.Value != obj {
			t.Fatal("error object changed")
		}
	} else {
		t.Fatalf("not a LuaError: %v", e)
	}
	for _, original := range []error{vm.LuaError{Value: obj}, &vm.LuaError{Value: obj}} {
		fn := vm.NativeFunction(func(*vm.State, []any) ([]any, error) { return nil, fmt.Errorf("wrapped: %w", original) })
		r, e := s.Invoke(libraryFunction(t, s, "pcall"), fn)
		if e != nil {
			t.Fatal(e)
		}
		libraryWant(t, r, []any{false, obj})
	}
}
func TestLibrariesStringMethodsAndFunctionKeys(t *testing.T) {
	_, r, _ := libraryRun(t, `local f,g=print,assert
 local t={[f]="print",[g]="assert"}
 local count=0;local seen=false
 for k,v in pairs(t) do count=count+1;if rawequal(k,f) then seen=v=="print" end end
 local p=pairs(t)
 local ok,value=xpcall(function()error(42)end,tostring)
 return ("Hello"):lower():sub(2),getmetatable("").__index==string,
 rawequal(f,print),rawequal(f,g),t[f],count,seen,rawequal(p,next),ok,value,
 ("id=123"):match("%d+"),("%s:%03d"):format("n",7)`)
	libraryWant(t, r, []any{"ello", true, true, false, "print", int64(2), true, true, false, "42", "123", "n:007"})
}

func TestLibrariesLoad(t *testing.T) {
	_, r, _ := libraryRun(t, `local env={x=40}
 local f=assert(load("x=x+1;return x,...","test-chunk","t",env))
 local a,b,c=f(7,nil)
 local nested=assert(load("return assert(load('return 19'))()"))()
 local parts={"return ","23, nil, ","24"};local i=0
 local reader=function()i=i+1;return parts[i]end
 local readerfn=assert(load(reader,"reader","t"))
 local d,e,h=readerfn()
 local n,msg=load("local = broken","syntax-name")
 local bin,binmsg=load(string.char(27).."Lua")
 local text,textmsg=load("return 1",nil,"b")
 local noenv=assert(load("return 3",nil,"t",nil))()
 local object={};local badReader,readerError=load(function()error(object)end)
 return a,b,c,env.x,nested,d,e,h,n,type(msg),bin,type(binmsg),text,type(textmsg),noenv,
 badReader,readerError==object`)
	libraryWant(t, r, []any{int64(41), int64(7), nil, int64(41), int64(19), int64(23), nil, int64(24), nil, "string", nil, "string", nil, "string", int64(3), nil, true})
	s := vm.NewState()
	s.Push("keep")
	loaded, e := s.Invoke(libraryFunction(t, s, "load"), "return 9")
	if e != nil {
		t.Fatal(e)
	}
	if s.GetTop() != 1 || s.At(1) != "keep" {
		t.Fatal("load modified public stack")
	}
	values, e := s.Invoke(loaded[0])
	if e != nil {
		t.Fatal(e)
	}
	libraryWant(t, values, []any{int64(9)})
	for _, source := range []string{"return 1\x00garbage", "return )"} {
		r, e := s.Invoke(libraryFunction(t, s, "load"), source, "bad-source")
		if e != nil {
			t.Fatal(e)
		}
		if len(r) != 2 || r[0] != nil || !strings.Contains(r[1].(string), "bad-source") {
			t.Fatalf("load error results: %#v", r)
		}
	}
}

func TestLibrariesLoadFileAndPackage(t *testing.T) {
	dir := t.TempDir()
	chunk := filepath.Join(dir, "chunk.lua")
	if e := os.WriteFile(chunk, []byte("\xef\xbb\xbf#!/usr/bin/env lua\nx=x+1; return x,nil,7"), 0600); e != nil {
		t.Fatal(e)
	}
	s := vm.NewState()
	env := vm.NewTable()
	_ = env.RawSet("x", int64(4))
	fn, e := s.Invoke(libraryFunction(t, s, "loadfile"), chunk, "t", env)
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.Invoke(fn[0])
	if e != nil {
		t.Fatal(e)
	}
	libraryWant(t, r, []any{int64(5), nil, int64(7)})
	_ = s.Globals.RawSet("x", int64(9))
	r, e = s.Invoke(libraryFunction(t, s, "dofile"), chunk)
	if e != nil {
		t.Fatal(e)
	}
	libraryWant(t, r, []any{int64(10), nil, int64(7)})
	r, e = s.Invoke(libraryFunction(t, s, "loadfile"), filepath.Join(dir, "absent.lua"))
	if e != nil || len(r) != 2 || r[0] != nil {
		t.Fatalf("loadfile missing: %#v, %v", r, e)
	}
	_, e = s.Invoke(libraryFunction(t, s, "dofile"), filepath.Join(dir, "absent.lua"))
	if e == nil {
		t.Fatal("dofile did not propagate file error")
	}
	stdinPath := filepath.Join(dir, "stdin.lua")
	if e = os.WriteFile(stdinPath, []byte("return 31,nil"), 0600); e != nil {
		t.Fatal(e)
	}
	input, e := os.Open(stdinPath)
	if e != nil {
		t.Fatal(e)
	}
	previousStdin := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = previousStdin; input.Close() }()
	r, e = s.Invoke(libraryFunction(t, s, "loadfile"))
	if e != nil {
		t.Fatal(e)
	}
	if r[0] == nil {
		t.Fatalf("stdin load: %#v", r)
	}
	r, e = s.Invoke(r[0])
	if e != nil {
		t.Fatal(e)
	}
	libraryWant(t, r, []any{int64(31), nil})
	os.Stdin = previousStdin
	if e = os.Mkdir(filepath.Join(dir, "module"), 0700); e != nil {
		t.Fatal(e)
	}
	module := filepath.Join(dir, "module", "answer.lua")
	if e = os.WriteFile(module, []byte(`local name,path=...;moduleCalls=(moduleCalls or 0)+1;return {name=name,path=path,value=42}`), 0600); e != nil {
		t.Fatal(e)
	}
	pkg := s.Globals.RawGet("package").(*vm.Table)
	_ = pkg.RawSet("path", filepath.Join(dir, "?.lua"))
	if e = s.Run(libraryCompile(t, `local calls=0
 package.preload.pre=function(name,data) calls=calls+1;return {name=name,data=data} end
 local a,data=require("pre");local b=require("pre")
 local c,path=require("module.answer");local d=require("module.answer")
 package.preload.noresult=function()end
 package.preload.selfset=function()package.loaded.selfset=17 end
 local noresult=require("noresult");local selfset=require("selfset")
 return a==b,c==d,c.value,c.name,c.path==path,data,a.data,calls,moduleCalls,
 noresult,selfset,require("string")==string`)); e != nil {
		t.Fatal(e)
	}
	r = make([]any, s.GetTop())
	for i := range r {
		r[i] = s.At(i + 1)
	}
	libraryWant(t, r, []any{true, true, int64(42), "module.answer", true, ":preload:", ":preload:", int64(1), int64(1), true, int64(17), true})
	r, e = s.Invoke(libraryFunction(t, s, "package.searchpath"), "module.answer", filepath.Join(dir, "?.lua"))
	if e != nil {
		t.Fatal(e)
	}
	libraryWant(t, r, []any{module})
	_, e = s.Invoke(libraryFunction(t, s, "require"), "not-found")
	if e == nil || !strings.Contains(e.Error(), "no file") {
		t.Fatalf("require missing: %v", e)
	}
	broken := filepath.Join(dir, "broken.lua")
	if e = os.WriteFile(broken, []byte("local = error"), 0600); e != nil {
		t.Fatal(e)
	}
	_, e = s.Invoke(libraryFunction(t, s, "require"), "broken")
	if e == nil || !strings.Contains(e.Error(), "error loading module") {
		t.Fatalf("require broken: %v", e)
	}
}

func TestLibrariesAllocationAndOverflowLimits(t *testing.T) {
	s := vm.NewState()
	table := vm.NewTable()
	for _, tc := range []struct {
		name string
		args []any
	}{
		{"string.rep", []any{"x", int64(math.MaxInt64)}},
		{"string.rep", []any{"", int64(math.MaxInt64), "-"}},
		{"table.unpack", []any{table, int64(math.MinInt64), int64(math.MaxInt64)}},
		{"table.move", []any{table, int64(math.MinInt64), int64(math.MaxInt64), int64(1)}},
		{"table.move", []any{table, int64(1), int64(2), int64(math.MaxInt64)}},
		{"table.concat", []any{table, ",", int64(math.MinInt64), int64(math.MaxInt64)}},
	} {
		t.Run(tc.name+fmt.Sprint(tc.args[1:]), func(t *testing.T) {
			_, e := s.Invoke(libraryFunction(t, s, tc.name), tc.args...)
			if e == nil {
				t.Fatal("oversized operation succeeded")
			}
		})
	}
	// A tiny table with an enormous __len must not start a huge native loop.
	mt := vm.NewTable()
	_ = mt.RawSet("__len", vm.NativeFunction(func(*vm.State, []any) ([]any, error) { return []any{int64(math.MaxInt64)}, nil }))
	table.Metatable = mt
	for _, name := range []string{"table.insert", "table.remove", "table.sort"} {
		args := []any{table}
		if name == "table.insert" {
			args = append(args, int64(1))
		}
		_, e := s.Invoke(libraryFunction(t, s, name), args...)
		if e == nil {
			t.Fatalf("%s accepted excessive length", name)
		}
	}
	big := strings.Repeat("a", (16<<20)+1)
	for _, name := range []string{"string.upper", "string.reverse", "string.lower"} {
		_, e := s.Invoke(libraryFunction(t, s, name), big)
		if e == nil {
			t.Fatalf("%s accepted oversized result", name)
		}
	}
	r, e := s.Invoke(libraryFunction(t, s, "load"), big, "large")
	if e != nil || r[0] != nil {
		t.Fatalf("oversized load: %#v, %v", r, e)
	}
	_, e = s.Invoke(libraryFunction(t, s, "string.byte"), strings.Repeat("a", (16<<20)/16+1), int64(1), int64(-1))
	if e == nil {
		t.Fatal("string.byte accepted too many results")
	}
	// A separator is unused for a single repetition, even if it is enormous.
	r, e = s.Invoke(libraryFunction(t, s, "string.rep"), "x", int64(1), big)
	if e != nil {
		t.Fatal(e)
	}
	libraryWant(t, r, []any{"x"})
	r, e = s.Invoke(libraryFunction(t, s, "table.unpack"), table, int64(math.MaxInt64), int64(math.MaxInt64))
	if e != nil {
		t.Fatal(e)
	}
	libraryWant(t, r, []any{nil})
}

func TestLibrariesPairsClosingAndStableIterator(t *testing.T) {
	_, r, _ := libraryRun(t, `local original=next
 local normalCount=select("#",pairs({}))
 next=function()error("replaced next must not be called")end
 local sum=0;for k,v in pairs({4,5})do sum=sum+v end
 local iterator=pairs({})
 local closed=0
 local closer=setmetatable({}, {__close=function()closed=closed+1 end})
 local t=setmetatable({}, {__pairs=function()return original,{8},nil,closer,"discard" end})
 local customCount=select("#",pairs(t))
 for k,v in pairs(t)do sum=sum+v;break end
 local ok,p=pcall(pairs,42)
 local short=select("#",pairs(setmetatable({}, {__pairs=function()return original end})))
 return normalCount,customCount,short,sum,closed,rawequal(iterator,original),ok,type(p)`)
	libraryWant(t, r, []any{int64(4), int64(4), int64(4), int64(17), int64(1), true, true, "function"})
}

func TestLibrariesRequireFailuresAndCycles(t *testing.T) {
	_, r, _ := libraryRun(t, `local object={};local attempts=0
 package.preload.retry=function()attempts=attempts+1;if attempts==1 then error(object)end;return 12 end
 local ok,err=pcall(require,"retry")
 local before=package.loaded.retry
 local retry=require("retry")
 package.preload.cycle=function()return require("cycle")end
 local cycle,cycleError=pcall(require,"cycle")
 local cycleBefore=package.loaded.cycle
 package.preload.cycle=function()return 34 end
 local recovered=require("cycle")
 package.preload.published=function()package.loaded.published=56;error(object)end
 local published,err2=pcall(require,"published")
 local cached=require("published")
 package.preload.partial=function()local t={};package.loaded.partial=t;t.same=require("partial")==t;return t end
 local partial=require("partial")
 return ok,err==object,before,retry,attempts,cycle,type(cycleError),cycleBefore,recovered,
 published,err2==object,cached,partial.same`)
	// Lua does not roll back assignments explicitly made to package.loaded by a loader.
	libraryWant(t, r, []any{false, true, nil, int64(12), int64(2), false, "string", nil, int64(34), false, true, int64(56), true})
}

func TestLibrariesPackageMetatables(t *testing.T) {
	_, r, _ := libraryRun(t, `setmetatable(package.loaded,{__index={virtual=19}})
 setmetatable(package.preload,{__index={dynamic=function()return 23 end}})
 local stored={}
 setmetatable(package.loaded,{__index={virtual=19},__newindex=function(t,k,v)stored[k]=v;rawset(t,k,v)end})
 local a=require("virtual");local b=require("dynamic")
 local c=require("dynamic")
 return a,b,c,stored.dynamic`)
	libraryWant(t, r, []any{int64(19), int64(23), int64(23), int64(23)})
}

func TestLibrariesNativeStackLoadingAndXpcall(t *testing.T) {
	s := vm.NewState()
	s.Push("caller sentinel")
	readerCalls := 0
	reader := vm.NativeFunction(func(s *vm.State, args []any) ([]any, error) {
		if len(args) != 0 || s.GetTop() != 0 {
			t.Fatal("reader did not get its own empty frame")
		}
		s.Push("reader scratch")
		if e := s.SetTop(0); e != nil {
			t.Fatal(e)
		}
		readerCalls++
		if readerCalls == 1 {
			return []any{"return ..."}, nil
		}
		return nil, nil
	})
	loaded, e := s.Invoke(libraryFunction(t, s, "load"), reader, "native reader", "t")
	if e != nil {
		t.Fatal(e)
	}
	results, e := s.Invoke(loaded[0], int64(7), nil)
	if e != nil {
		t.Fatal(e)
	}
	libraryWant(t, results, []any{int64(7), nil})
	object := vm.NewTable()
	bad := vm.NativeFunction(func(s *vm.State, args []any) ([]any, error) {
		if s.GetTop() != 1 || s.At(1) != int64(9) {
			t.Fatal("protected call args missing")
		}
		_ = s.SetTop(0)
		s.Push("callee scratch")
		return nil, vm.LuaError{Value: object}
	})
	handler := vm.NativeFunction(func(s *vm.State, args []any) ([]any, error) {
		if s.GetTop() != 1 || s.At(1) != object {
			t.Fatal("handler error object missing")
		}
		_ = s.SetTop(0)
		s.Push("handler scratch")
		return []any{object, "discard"}, nil
	})
	results, e = s.Invoke(libraryFunction(t, s, "xpcall"), bad, handler, int64(9))
	if e != nil {
		t.Fatal(e)
	}
	libraryWant(t, results, []any{false, object})
	handler = vm.NativeFunction(func(*vm.State, []any) ([]any, error) { return nil, vm.LuaError{Value: "handler failure"} })
	results, e = s.Invoke(libraryFunction(t, s, "xpcall"), bad, handler, int64(9))
	if e != nil {
		t.Fatal(e)
	}
	libraryWant(t, results, []any{false, "error in error handling"})
	if s.GetTop() != 1 || s.At(1) != "caller sentinel" {
		t.Fatal("native library callbacks corrupted caller stack")
	}
}

func TestLibrariesIntegerBoundaryRandomAndMove(t *testing.T) {
	s := vm.NewState()
	random := libraryFunction(t, s, "math.random")
	ranges := [][2]int64{{math.MinInt64, math.MaxInt64}, {math.MinInt64, -1}, {0, math.MaxInt64}, {math.MinInt64, math.MinInt64}, {math.MaxInt64, math.MaxInt64}}
	for _, bounds := range ranges {
		for i := 0; i < 64; i++ {
			r, e := s.Invoke(random, bounds[0], bounds[1])
			if e != nil {
				t.Fatal(e)
			}
			x, ok := r[0].(int64)
			if !ok || x < bounds[0] || x > bounds[1] {
				t.Fatalf("random(%d,%d) = %#v", bounds[0], bounds[1], r)
			}
		}
	}
	source, dest := vm.NewTable(), vm.NewTable()
	_ = source.RawSet(int64(math.MinInt64), "edge")
	r, e := s.Invoke(libraryFunction(t, s, "table.move"), source, int64(math.MinInt64), int64(math.MinInt64), int64(math.MaxInt64), dest)
	if e != nil {
		t.Fatal(e)
	}
	libraryWant(t, r, []any{dest})
	if dest.RawGet(int64(math.MaxInt64)) != "edge" {
		t.Fatal("table.move lost boundary value")
	}
	_, e = s.Invoke(libraryFunction(t, s, "table.move"), source, int64(1), int64((16<<20)/64+1), int64(1), dest)
	if e == nil {
		t.Fatal("table.move exceeded entry budget")
	}
	if len(dest.Keys()) != 1 {
		t.Fatal("oversized move partially wrote destination")
	}
	_ = source.RawSet(int64(math.MinInt64), "wrapped")
	iter, e := s.Invoke(libraryFunction(t, s, "ipairs"), source)
	if e != nil {
		t.Fatal(e)
	}
	r, e = s.Invoke(iter[0], source, int64(math.MaxInt64))
	if e != nil {
		t.Fatal(e)
	}
	libraryWant(t, r, []any{int64(math.MinInt64), "wrapped"})
}

func TestLibrariesSourceBudgetsAndBinaryFiles(t *testing.T) {
	s := vm.NewState()
	r, e := s.Invoke(libraryFunction(t, s, "load"), strings.Repeat(";", 65537), "token flood", "t")
	if e != nil {
		t.Fatal(e)
	}
	if r[0] != nil || !strings.Contains(r[1].(string), "token limit") {
		t.Fatalf("token flood not rejected: %#v", r)
	}
	dir := t.TempDir()
	for i, source := range []string{"\x1bLua\x00malicious", "\xef\xbb\xbf\x1bLua\x00malicious", "#!/usr/bin/env lua\n\x1bLua\x00malicious"} {
		path := filepath.Join(dir, fmt.Sprintf("binary%d.lua", i))
		if e = os.WriteFile(path, []byte(source), 0600); e != nil {
			t.Fatal(e)
		}
		r, e = s.Invoke(libraryFunction(t, s, "loadfile"), path, "bt")
		if e != nil {
			t.Fatal(e)
		}
		if r[0] != nil || !strings.Contains(r[1].(string), "binary chunks unsupported") {
			t.Fatalf("binary accepted: %#v", r)
		}
	}
}

func TestLibrariesUTF8CodesMalformedAndControls(t *testing.T) {
	s := vm.NewState()
	codes := libraryFunction(t, s, "utf8.codes")
	_, e := s.Invoke(codes, "\x80")
	if e == nil {
		t.Fatal("utf8.codes accepted initial continuation")
	}
	r, e := s.Invoke(codes, "a\x80")
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Invoke(r[0], r[1], int64(0))
	if e == nil {
		t.Fatal("utf8.codes yielded character before stray continuation")
	}
	r, e = s.Invoke(codes, "AéB")
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.Invoke(r[0], r[1], int64(2))
	if e != nil {
		t.Fatal(e)
	}
	libraryWant(t, got, []any{int64(4), int64('B')})
	got, e = s.Invoke(r[0], r[1], int64(-1))
	if e != nil {
		t.Fatal(e)
	}
	if len(got) != 0 {
		t.Fatalf("negative control yielded %#v", got)
	}
}

func TestLibrariesStackCallAndGC(t *testing.T) {
	s := vm.NewState()
	s.Push(libraryFunction(t, s, "table.pack"))
	s.Push(int64(1))
	s.Push(nil)
	if e := s.Call(2, 1); e != nil {
		t.Fatal(e)
	}
	p := s.At(-1).(*vm.Table)
	if p.RawGet("n") != int64(2) {
		t.Fatal("pack lost nil")
	}
	if e := s.Pop(1); e != nil {
		t.Fatal(e)
	}
	s.Push(libraryFunction(t, s, "table.unpack"))
	s.Push(p)
	s.Push(int64(1))
	s.Push(int64(2))
	if e := s.Call(3, vm.MultRet); e != nil {
		t.Fatal(e)
	}
	if s.GetTop() != 2 || s.At(1) != int64(1) || s.At(2) != nil {
		t.Fatal("unpack lost nil")
	}
	for _, mode := range []string{"collect", "count"} {
		r, e := s.Invoke(libraryFunction(t, s, "collectgarbage"), mode)
		if e != nil {
			t.Fatal(e)
		}
		if len(r) != 1 {
			t.Fatalf("GC results: %#v", r)
		}
		switch r[0].(type) {
		case int64, float64:
		default:
			t.Fatalf("GC result type: %T", r[0])
		}
	}
}
