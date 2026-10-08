package vm

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/robogg133/glua/compile"
	"github.com/robogg133/glua/lex"
	"github.com/robogg133/glua/parser"
	"github.com/robogg133/glua/tokens"
)

func patternTestLibrary() (*State, *Table) {
	s := NewState()
	lib := NewTable()
	s.openPatternLibrary(lib)
	return s, lib
}
func patternTestCall(t *testing.T, s *State, lib *Table, name string, want []any, args ...any) {
	t.Helper()
	got, err := s.Invoke(lib.RawGet(name), args...)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s(%#v) = %#v; want %#v", name, args, got, want)
	}
}
func TestPatternMatching(t *testing.T) {
	s, lib := patternTestLibrary()
	cases := []struct {
		name       string
		args, want []any
	}{
		{"find", []any{"abc123xyz", "(%a+)(%d+)"}, []any{int64(1), int64(6), "abc", "123"}},
		{"match", []any{"abc", "()(%a+)()"}, []any{int64(1), "abc", int64(4)}},
		{"find", []any{"abc", "", 4}, []any{int64(4), int64(3)}},
		{"match", []any{"abc", "^b", 2}, []any{"b"}},
		{"find", []any{"a.b", ".", 1, true}, []any{int64(2), int64(2)}},
		{"match", []any{"x(a(b)c)y", "%b()"}, []any{"(a(b)c)"}},
		{"match", []any{"(x)", "%b(("}, []any{nil}},
		{"match", []any{"cat scatter cat", "%f[%a]cat%f[%A]"}, []any{"cat"}},
		{"match", []any{"abc", "%f[%z]"}, []any{""}},
		{"match", []any{"aabb", "(%a)%1"}, []any{"a"}},
		{"match", []any{"abc abc", "(%a+)%s%1$"}, []any{"abc"}},
		{"match", []any{"abc123def", "%a-"}, []any{""}},
		{"match", []any{"abc123def", "%a-%d+"}, []any{"abc123"}},
		{"match", []any{"ac", "ab?c"}, []any{"ac"}},
		{"match", []any{"aaab", "a+b"}, []any{"aaab"}},
		{"match", []any{"123abc", "[^%d]+"}, []any{"abc"}},
		{"match", []any{"]-", "[]-]+"}, []any{"]-"}},
		{"match", []any{"A_f9", "[%w_]+"}, []any{"A_f9"}},
		{"match", []any{"\x00\xff", "%z%Z"}, []any{"\x00\xff"}},
		{"match", []any{"é", ".."}, []any{"é"}},
		{"match", []any{"abc", "(%a*)%d"}, []any{nil}},
		{"find", []any{"abc", "b", -2}, []any{int64(2), int64(2)}},
		{"find", []any{"abc", "", 5}, []any{nil}},
		{"match", []any{"a", "((a))"}, []any{"a", "a"}},
		{"match", []any{"abc", "()%1"}, []any{nil}},
	}
	for _, c := range cases {
		t.Run(c.name+"/"+luaString(c.args[1]), func(t *testing.T) { patternTestCall(t, s, lib, c.name, c.want, c.args...) })
	}
	for _, c := range []struct {
		cl      string
		yes, no byte
	}{{"a", 'A', '1'}, {"c", 0, 'a'}, {"d", '3', 'a'}, {"g", '!', ' '}, {"l", 'a', 'A'}, {"p", '_', 'a'}, {"s", '\v', 'a'}, {"u", 'A', 'a'}, {"w", '3', '_'}, {"x", 'F', 'g'}, {"z", 0, 1}} {
		if !patternClass(c.yes, c.cl[0]) || patternClass(c.no, c.cl[0]) || patternClass(c.yes, c.cl[0]-32) || !patternClass(c.no, c.cl[0]-32) {
			t.Errorf("class %s", c.cl)
		}
	}
}
func TestPatternSubstitution(t *testing.T) {
	s, lib := patternTestLibrary()
	patternTestCall(t, s, lib, "gsub", []any{"[a1] [b2]", int64(2)}, "a1 b2", "(%a)(%d)", "[%1%2]")
	patternTestCall(t, s, lib, "gsub", []any{"%abcabc", int64(1)}, "abc", "%a+", "%%%0%1")
	patternTestCall(t, s, lib, "gsub", []any{"-a-b-c-", int64(4)}, "abc", "", "-")
	patternTestCall(t, s, lib, "gsub", []any{"X", int64(1)}, "abc", ".*", "X")
	patternTestCall(t, s, lib, "gsub", []any{"XbXcX", int64(3)}, "abc", "a*", "X")
	patternTestCall(t, s, lib, "gsub", []any{"XaX", int64(2)}, "a", "(", "X")
	patternTestCall(t, s, lib, "gsub", []any{"1a2", int64(2)}, "a", "()(", "%1")
	patternTestCall(t, s, lib, "gsub", []any{"a", int64(2)}, "a", "(", "%0")
	unused := NewTable()
	_ = unused.RawSet(1, "X")
	_ = unused.RawSet(2, "Y")
	patternTestCall(t, s, lib, "gsub", []any{"XaY", int64(2)}, "a", "()(", unused)
}
func TestPatternReplacementValues(t *testing.T) {
	s, lib := patternTestLibrary()
	table := NewTable()
	_ = table.RawSet("cat", "dog")
	_ = table.RawSet("rat", false)
	patternTestCall(t, s, lib, "gsub", []any{"dog rat bat", int64(3)}, "cat rat bat", "%a+", table)
	seen := 0
	fn := NativeFunction(func(_ *State, a []any) ([]any, error) {
		seen++
		if a[0] == "b" {
			return []any{false}, nil
		}
		return []any{strings.ToUpper(a[0].(string))}, nil
	})
	patternTestCall(t, s, lib, "gsub", []any{"AbC", int64(3)}, "abc", "(.)", fn)
	if seen != 3 {
		t.Fatal(seen)
	}
	patternTestCall(t, s, lib, "gsub", []any{"Xbc", int64(1)}, "abc", ".", "X", 1)
	patternTestCall(t, s, lib, "gsub", []any{"abc", int64(0)}, "abc", ".", "X", -1)
	patternTestCall(t, s, lib, "gsub", []any{"Xbc", int64(1)}, "abc", "^.", "X")
	patternTestCall(t, s, lib, "gsub", []any{"1a2b3", int64(3)}, "ab", "()", "%1")
	positions := NewTable()
	_ = positions.RawSet(1, "X")
	patternTestCall(t, s, lib, "gsub", []any{"Xab", int64(3)}, "ab", "()", positions)
	meta := NewTable()
	_ = meta.RawSet("__index", NativeFunction(func(_ *State, a []any) ([]any, error) { return []any{"!" + a[1].(string)}, nil }))
	table.Metatable = meta
	patternTestCall(t, s, lib, "gsub", []any{"!fox", int64(1)}, "fox", "%a+", table)
}
func TestPatternIterators(t *testing.T) {
	s, lib := patternTestLibrary()
	for _, tc := range []struct {
		text, pat string
		want      [][]any
	}{{"ab", "", [][]any{{""}, {""}, {""}}}, {"abc", "a*", [][]any{{"a"}, {""}, {""}}}, {"a1 b2", "(%a)(%d)", [][]any{{"a", "1"}, {"b", "2"}}}, {"abc", "^.", nil}, {"ab", "()", [][]any{{int64(1)}, {int64(2)}, {int64(3)}}}} {
		out, err := s.Invoke(lib.RawGet("gmatch"), tc.text, tc.pat)
		if err != nil {
			t.Fatal(err)
		}
		var got [][]any
		for i := 0; i < 10; i++ {
			v, e := s.Invoke(out[0])
			if e != nil {
				t.Fatal(e)
			}
			if len(v) == 0 {
				break
			}
			got = append(got, v)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("gmatch(%q,%q) = %#v; want %#v", tc.text, tc.pat, got, tc.want)
		}
	}
}
func TestPatternErrorsAndBounds(t *testing.T) {
	s, lib := patternTestLibrary()
	for _, pat := range []string{"%", "[abc", "[]", "%b(", "%f.", "%1", "(", ")", strings.Repeat("()", 33)} {
		if _, e := s.Invoke(lib.RawGet("match"), "abc", pat); e == nil {
			t.Errorf("accepted %q", pat)
		}
	}
	for _, r := range []any{"%", "%a", "%2", true, NativeFunction(func(*State, []any) ([]any, error) { return []any{true}, nil })} {
		if _, e := s.Invoke(lib.RawGet("gsub"), "a", "a", r); e == nil {
			t.Errorf("accepted replacement %#v", r)
		}
	}
	sentinel := errors.New("replacement failed")
	_, err := s.Invoke(lib.RawGet("gsub"), "a", "a", NativeFunction(func(*State, []any) ([]any, error) { return nil, sentinel }))
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	s.MaxSteps = 100
	_, err = s.Invoke(lib.RawGet("match"), strings.Repeat("a", 100), "a*a*a*a*a*b")
	if err == nil || !strings.Contains(err.Error(), "step limit") {
		t.Fatal(err)
	}
	s.MaxSteps = 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Context = ctx
	_, err = s.Invoke(lib.RawGet("match"), "a", "a")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.Context = nil
	_, err = s.Invoke(lib.RawGet("match"), strings.Repeat("a", 250), strings.Repeat("a?", 250)+"b")
	if err == nil || !strings.Contains(err.Error(), "complex") {
		t.Fatal(err)
	}
}
func TestPatternFormat(t *testing.T) {
	s, lib := patternTestLibrary()
	patternTestCall(t, s, lib, "format", []any{"-0007 ff 010 18446744073709551615 A abc 1.25 %"}, "%05d %x %#o %u %c %.3s %.2f %%", -7, 255, 8, -1, 65, "abcdef", 1.25)
	patternTestCall(t, s, lib, "format", []any{"\"a\\0b\\0012\\\"\\\\\\\n\" nil true 42"}, "%q %q %q %q", "a\x00b\x012\"\\\n", nil, true, 42)
	patternTestCall(t, s, lib, "format", []any{"  é|\xc3|é  "}, "%4s|%.1s|%-4s", "é", "é", "é")
	for _, f := range []string{"%", "%v", "%1000s", "%.1000f", "%2q", "%*s"} {
		if _, e := s.Invoke(lib.RawGet("format"), f, "abc"); e == nil {
			t.Errorf("accepted format %q", f)
		}
	}
}
func TestPatternLuaSource(t *testing.T) {
	s, lib := patternTestLibrary()
	_ = s.Globals.RawSet("patterns", lib)
	source := `local out = {}; for word, pos in patterns.gmatch("one two", "(%a+)()") do out[#out+1] = word .. pos end
 local changed, n = patterns.gsub("a1 b2", "(%a)(%d)", function(a,b) return b..a end)
 return out[1], out[2], changed, n, patterns.find("xy", "()y()")`
	ast := parser.NewAst(tokens.NewTokenizer(lex.NewLexer(source), "pattern-test"))
	if err := ast.Next(); err != nil {
		t.Fatal(err)
	}
	proto, err := compile.Compile(ast)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Run(proto); err != nil {
		t.Fatal(err)
	}
	got := make([]any, s.GetTop())
	for i := range got {
		got[i] = s.At(i + 1)
	}
	want := []any{"one4", "two8", "1a 2b", int64(2), int64(2), int64(2), int64(2), int64(3)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%#v; want %#v", got, want)
	}
}

func patternReferenceLua(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("GLUA_PATTERN_LUA"); path != "" {
		return path
	}
	for _, name := range []string{"lua5.5", "lua5.4"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	t.Skip("reference Lua interpreter not installed")
	return ""
}

func TestPatternOutputBounds(t *testing.T) {
	s, lib := patternTestLibrary()
	exact := strings.Repeat("x", libMaxBytes)
	for _, tc := range []struct {
		name string
		args []any
	}{
		{"format", []any{"%s", exact}},
		{"gsub", []any{"a", "a", exact}},
		{"gsub", []any{exact, "", "", 0}},
	} {
		out, err := s.Invoke(lib.RawGet(tc.name), tc.args...)
		if err != nil {
			t.Fatal(err)
		}
		if len(out[0].(string)) != libMaxBytes {
			t.Fatal("exact boundary rejected")
		}
	}
	tooBig := exact + "x"
	hugeTable := NewTable()
	_ = hugeTable.RawSet("a", tooBig)
	hugeFn := NativeFunction(func(*State, []any) ([]any, error) { return []any{tooBig}, nil })
	capture := strings.Repeat("x", libMaxBytes/3+1)
	for _, tc := range []struct {
		name string
		args []any
	}{
		{"format", []any{"%s", tooBig}}, {"format", []any{tooBig}},
		{"format", []any{"%s%s", exact, "x"}},
		{"format", []any{"%q", strings.Repeat("\x011", libMaxBytes/5+1)}},
		{"gsub", []any{tooBig, "a", "x", 0}},
		{"gsub", []any{"aa", "a", exact}},
		{"gsub", []any{"a", "a", tooBig}},
		{"gsub", []any{"a", "a", hugeFn}},
		{"gsub", []any{"a", "a", hugeTable}},
		{"gsub", []any{capture, "(.+)", "%1%1%1"}},
		{"gsub", []any{"ab", "a", exact}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Invoke(lib.RawGet(tc.name), tc.args...); err == nil || !strings.Contains(err.Error(), "16 MiB") {
				t.Fatalf("expected output-size error, got %v", err)
			}
		})
	}
	// Many individually bounded conversions must also respect the total limit.
	args := make([]any, 45001)
	args[0] = strings.Repeat("%99.99f", len(args)-1)
	for i := 1; i < len(args); i++ {
		args[i] = math.MaxFloat64
	}
	if _, err := s.Invoke(lib.RawGet("format"), args...); err == nil || !strings.Contains(err.Error(), "16 MiB") {
		t.Fatal(err)
	}
}

func TestPatternNumericFormatReference(t *testing.T) {
	lua := patternReferenceLua(t)
	s, lib := patternTestLibrary()
	cases := []struct {
		format, expression string
		value              any
	}{
		{"%q", "1.5", 1.5}, {"%q", "0.0", 0.0}, {"%q", "-0.0", math.Copysign(0, -1)},
		{"%q", "math.huge", math.Inf(1)}, {"%q", "-math.huge", math.Inf(-1)}, {"%q", "0/0", math.NaN()},
		{"%q", "math.mininteger", int64(math.MinInt64)},
		{"%q", "0x0.0000000000001p-1022", math.SmallestNonzeroFloat64},
		{"%a", "1.5", 1.5}, {"%A", "1.5", 1.5}, {"%.0a", "1.5", 1.5}, {"%.1a", "1.96875", 1.96875},
		{"%#a", "0.0", 0.0}, {"%020a", "-1.5", -1.5}, {"%.20a", "1.5", 1.5},
		{"%a", "0x0.0000000000001p-1022", math.SmallestNonzeroFloat64},
		{"%010f", "math.huge", math.Inf(1)}, {"%+10E", "math.huge", math.Inf(1)},
		{"%g", "math.abs(0/0)", math.NaN()}, {"%G", "-math.abs(0/0)", math.Copysign(math.NaN(), -1)},
		{"%.0g", "1.5", 1.5}, {"%#g", "1.5", 1.5},
		{"%#x", "0", int64(0)}, {"%#.0o", "0", int64(0)}, {"%#.0x", "0", int64(0)},
		{"%#08x", "255", int64(255)}, {"%08.4d", "42", int64(42)}, {"%--8s", "'abc'", "abc"},
		{"%0s", "'abc'", "abc"}, {"%+u", "1", int64(1)}, {"%#d", "1", int64(1)},
		{"% c", "65", int64(65)}, {"%.1c", "65", int64(65)}, {"%F", "1.0", 1.0}, {"%p", "nil", nil},
	}
	for _, tc := range cases {
		t.Run(tc.format+"/"+tc.expression, func(t *testing.T) {
			out, refErr := exec.Command(lua, "-e", "local ok,v=pcall(string.format,"+patternQuote(tc.format)+","+tc.expression+"); if ok then io.write(v) else os.exit(2) end").CombinedOutput()
			got, err := s.Invoke(lib.RawGet("format"), tc.format, tc.value)
			if tc.format == "%p" {
				if err == nil {
					t.Fatal("pointer format should be explicitly unsupported")
				}
				return
			}
			if refErr != nil {
				if err == nil {
					t.Fatalf("reference rejects format, Go returned %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got[0] != string(out) {
				t.Fatalf("Go %q; Lua %q", got[0], out)
			}
		})
	}
}

// Compare shared byte-pattern semantics with the installed reference interpreter.
func TestPatternLua54Reference(t *testing.T) {
	lua := patternReferenceLua(t)
	s, lib := patternTestLibrary()
	cases := []struct{ name, text, pat string }{{"match", "abc", "()(%a+)()"}, {"match", "]-", "[]-]+"}, {"match", "aabb", "(%a)%1"}, {"match", "abc", "%f[%z]"}, {"match", "x(a(b)c)y", "%b()"}, {"match", "x(a(b)c", "%b()"}, {"match", "((x))", "(%b())()"}, {"match", "((", "%b(("}, {"match", "abc", "%f[%a]%a+%f[%z]"}, {"match", "\x00a\x00", "%f[%a]a%f[%z]"}, {"match", "abc abc", "(%a+)%s%1$"}, {"match", "abc", "()%1"}, {"match", "aac", "(a*)a%1"}, {"match", "aba", "((a)b)%2"}, {"gsub", "a", "("}, {"gsub", "a", "()("}, {"gsub", "", ""}, {"gsub", "aaa", "a*"}, {"gsub", "abc", "a*"}, {"gsub", "abc", ".*"}, {"gsub", "abc", ""}, {"gsub", "abc", ".-"}, {"gsub", "abc", "^"}, {"gsub", "abc", "$"}}
	for _, tc := range cases {
		args := []any{tc.text, tc.pat}
		extra := ""
		if tc.name == "gsub" {
			args = append(args, "X")
			extra = ", 'X'"
		}
		got, e := s.Invoke(lib.RawGet(tc.name), args...)
		if e != nil {
			t.Fatal(e)
		}
		var want strings.Builder
		for _, v := range got {
			fmtType := typeName(v)
			want.WriteString(fmtType + ":" + luaString(v) + "\n")
		}
		source := "local function emit(...) for i=1,select('#',...) do local v=select(i,...); print(type(v)..':'..tostring(v)) end end; emit(string." + tc.name + "(" + patternQuote(tc.text) + "," + patternQuote(tc.pat) + extra + "))"
		out, e := exec.Command(lua, "-e", source).CombinedOutput()
		if e != nil {
			t.Fatalf("reference: %v: %s", e, out)
		}
		if string(out) != want.String() {
			t.Errorf("%s %q: Go %q Lua %q", tc.name, tc.pat, want.String(), out)
		}
	}
}
