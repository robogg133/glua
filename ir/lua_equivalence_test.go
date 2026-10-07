package ir

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/robogg133/glua/parser"
)

// This emitter is deliberately test-only: compare both arenas in a real Lua
// runtime without implementing another evaluator with the optimizer's biases.
// These fixtures use the Lua 5.4/5.5 common subset; new 5.5 syntax is tested in Go.
func emitTestLua(t *testing.T, a *parser.AST, root uint32) string {
	t.Helper()
	var emit func(uint32) string
	list := func(index uint32, separator string) string {
		var parts []string
		for _, item := range optItems(a, index) {
			parts = append(parts, emit(item))
		}
		return strings.Join(parts, separator)
	}
	function := func(index uint32) string {
		n := a.Nodes[index]
		return "(" + list(n.Left, ",") + ")\n" + emit(n.Right) + "end"
	}
	operators := map[parser.Tag]string{
		parser.TagOr: "or", parser.TagAnd: "and", parser.TagEqual: "==", parser.TagNotEqual: "~=", parser.TagLess: "<", parser.TagLessEqual: "<=", parser.TagGreater: ">", parser.TagGreaterEqual: ">=",
		parser.TagBitOr: "|", parser.TagBitXor: "~", parser.TagBitAnd: "&", parser.TagShiftLeft: "<<", parser.TagShiftRight: ">>", parser.TagConcat: "..", parser.TagAdd: "+", parser.TagSubtract: "-", parser.TagMultiply: "*", parser.TagModulo: "%", parser.TagDivide: "/", parser.TagIntegerDivide: "//", parser.TagPower: "^",
	}
	emit = func(index uint32) string {
		if index == 0 {
			return ""
		}
		n := a.Nodes[index]
		switch n.Kind {
		case parser.TagChunk:
			return emit(n.Left)
		case parser.TagBlock:
			return list(n.Left, "\n") + "\n"
		case parser.TagNil:
			return "nil"
		case parser.TagBoolean, parser.TagNameReference, parser.TagNameTarget, parser.TagIdentifier:
			return a.Values[index]
		case parser.TagInteger:
			value, ok := constantInteger(constant{n.Kind, a.Values[index]})
			if !ok {
				t.Fatal("bad integer")
			}
			// Hex spelling round-trips MinInt64, unlike its overflowing decimal token.
			return fmt.Sprintf("0x%x", uint64(value))
		case parser.TagFloat:
			return "(" + a.Values[index] + ")"
		case parser.TagString:
			var s strings.Builder
			s.WriteByte('"')
			for _, b := range []byte(a.Values[index]) {
				fmt.Fprintf(&s, "\\%03d", b)
			}
			s.WriteByte('"')
			return s.String()
		case parser.TagVararg:
			return "..."
		case parser.TagParenthesized:
			return "(" + emit(n.Left) + ")"
		case parser.TagNot:
			return "(not " + emit(n.Left) + ")"
		case parser.TagNegate:
			return "(- " + emit(n.Left) + ")"
		case parser.TagBitwiseNot:
			return "(~ " + emit(n.Left) + ")"
		case parser.TagLength:
			return "(# " + emit(n.Left) + ")"
		case parser.TagTableConstructor:
			return "{" + list(n.Left, ",") + "}"
		case parser.TagArrayField:
			return emit(n.Left)
		case parser.TagNameField:
			return emit(n.Left) + "=" + emit(n.Right)
		case parser.TagKeyField:
			return "[" + emit(n.Left) + "]=" + emit(n.Right)
		case parser.TagFieldAccess, parser.TagFieldTarget:
			return "(" + emit(n.Left) + ")." + emit(n.Right)
		case parser.TagIndexAccess, parser.TagIndexTarget:
			return "(" + emit(n.Left) + ")[" + emit(n.Right) + "]"
		case parser.TagCallExpression, parser.TagTableArgumentCall, parser.TagStringArgumentCall:
			return "(" + emit(n.Left) + ")(" + list(n.Right, ",") + ")"
		case parser.TagMethodCallExpression:
			pair := a.Nodes[n.Left]
			return "(" + emit(pair.Left) + "):" + emit(pair.Right) + "(" + list(n.Right, ",") + ")"
		case parser.TagFunctionExpression:
			return "function" + function(index)
		case parser.TagParameter:
			return emit(n.Left)
		case parser.TagVarargParameter:
			if n.Left != 0 {
				t.Fatal("named varargs require Lua 5.5")
			}
			return "..."
		case parser.TagLocalFunctionDeclaration:
			return "local function " + emit(n.Left) + function(n.Right)
		case parser.TagFunctionDeclaration:
			return "function " + emit(n.Left) + function(n.Right)
		case parser.TagNamePath:
			pair := a.Nodes[n.Right]
			name := emit(n.Left)
			for _, field := range optItems(a, pair.Left) {
				name += "." + emit(field)
			}
			if pair.Right != 0 {
				name += ":" + emit(pair.Right)
			}
			return name
		case parser.TagBinding:
			name := emit(n.Left)
			if n.Right != 0 {
				name += "<" + a.Values[n.Right] + ">"
			}
			return name
		case parser.TagLocalDeclaration:
			s := "local " + list(n.Left, ",")
			if n.Right != 0 {
				s += " = " + list(n.Right, ",")
			}
			return s + ";"
		case parser.TagAssignment:
			return list(n.Left, ",") + " = " + list(n.Right, ",") + ";"
		case parser.TagCallStatement:
			return emit(n.Left) + ";"
		case parser.TagReturn:
			return "return " + list(n.Left, ",") + ";"
		case parser.TagEmpty:
			return ";"
		case parser.TagBreak:
			return "break;"
		case parser.TagGoto:
			return "goto " + emit(n.Left) + ";"
		case parser.TagLabel:
			return "::" + emit(n.Left) + "::"
		case parser.TagDoBlock:
			return "do\n" + emit(n.Left) + "end"
		case parser.TagWhile:
			return "while " + emit(n.Left) + " do\n" + emit(n.Right) + "end"
		case parser.TagRepeatUntil:
			return "repeat\n" + emit(n.Left) + "until " + emit(n.Right)
		case parser.TagNumericFor:
			header := a.Nodes[n.Left]
			bounds := optItems(a, header.Right)
			s := "for " + emit(header.Left) + "=" + emit(bounds[0]) + "," + emit(bounds[1])
			if bounds[2] != 0 {
				s += "," + emit(bounds[2])
			}
			return s + " do\n" + emit(n.Right) + "end"
		case parser.TagIf:
			s := ""
			for i, branch := range optItems(a, n.Left) {
				if i == 0 {
					s += "if "
				} else {
					s += "elseif "
				}
				b := a.Nodes[branch]
				s += emit(b.Left) + " then\n" + emit(b.Right)
			}
			if n.Right != 0 {
				s += "else\n" + emit(n.Right)
			}
			return s + "end"
		}
		if op, ok := operators[n.Kind]; ok {
			return "(" + emit(n.Left) + " " + op + " " + emit(n.Right) + ")"
		}
		t.Fatalf("test emitter does not handle tag %d", n.Kind)
		return ""
	}
	return emit(root)
}

func TestOptimizeLuaEquivalence(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua executable unavailable; Go structural tests still run")
	}
	version, err := exec.Command(lua, "-v").CombinedOutput()
	if err != nil || (!strings.Contains(string(version), "Lua 5.4") && !strings.Contains(string(version), "Lua 5.5")) {
		t.Skip("equivalence tests require Lua 5.4 or 5.5")
	}
	run := func(t *testing.T, body string) string {
		t.Helper()
		script := `local r=table.pack((function()
` + body + `
end)())
io.write(r.n, ":")
for i=1,r.n do
 local v=r[i]
 if type(v)=="number" then
  if math.type(v)=="integer" then io.write("i",tostring(v),";")
  else
   io.write("f")
   for c in string.gmatch(string.pack(">d",v),".") do io.write(string.format("%02x",string.byte(c))) end
   io.write(";")
  end
 elseif type(v)=="string" then io.write("s",#v,":",v,";")
 else io.write(type(v),":",tostring(v),";") end
end`
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, lua, "-E", "-")
		cmd.Stdin = strings.NewReader(script)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Lua execution failed: %v\n%s\n%s", err, output, script)
		}
		return string(output)
	}
	fixtures := []string{
		`local a=20; local b=a+1; local function twice(x) return x*2 end; return twice(b)`,
		`local x=1; do local x=2; print(x) end; return x`,
		`local x=1; local function change() x=2 end; change(); return x`,
		`local n=0; local function effect() n=n+1; return 7,8 end; local function id(x) return x end; return id(effect()),n`,
		`local n=0; local function effect() n=n+1; return 7,8 end; local function id(x) return x end; return id(1,effect()),n`,
		`local function f() return 1,2 end; return true and f()`,
		`local function f() return 1,2 end; return false or f()`,
		`local function f() return end; return true and f()`,
		`local function f(...) return true and ... end; return f(1,2)`,
		`local function f() return 1,2 end; return (f()),f()`,
		`local function f() return 1,2 end; local a,b=f(); return a,b`,
		`local function f() return 1 end; local a,b=f(); return a,b`,
		`local a,b=1,2; a,b=b,a; return a,b`,
		`local i=1; local a={}; i,a[i]=i+1,20; return i,a[1],a[2]`,
		`local s=''; local function mark(x) s=s..x end; if false then mark('no') elseif true then mark('yes') else mark('no') end; return s`,
		`local x=0; if true then local y<close> = setmetatable({},{__close=function() x=x+1 end}) end; return x`,
		`local x=0; while false do x=x+100 end; while true do x=x+1; break; x=x+100 end; return x`,
		`local x=0; goto L; x=100; ::L:: x=x+1; return x`,
		`local x=0; while true do x=3; goto L end; x=100; ::L:: return x`,
		`local x=0; repeat x=x+1; break until false; return x`,
		`local x=0; for i=1,3 do x=x+i end; return x`,
		`local x=0; local t=setmetatable({},{__add=function(a,b) x=x+1; return b end}); local function f(a) return a+1 end; return f(t),x`,
		`local a,b={},{}; return a==b,a==a`,
		`return 9223372036854775807+1, -7//3, 7%-3, 0xffffffffffffffff >> 1, 1<<63`,
		`return 9007199254740993==9007199254740992.0, 0x7fffffffffffffff<0x1p63`,
		`return -0.0, -6.0%3.0, 1.0/2, 10^0.1, 3.1^2.7`,
		`return 'é'..'\0x', #'é', 0 and 2, '' or 3, nil or false`,
		`local function f(x) return x end; return f(),f(9,10)`,
		`local function f(x) return 1/x end; local ok=pcall(f,'bad'); return ok`,
	}
	for i, source := range fixtures {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			input := parseAudit(t, source)
			output, _, err := Optimize(input)
			if err != nil {
				t.Fatal(err)
			}
			before := run(t, source)
			if emitted := run(t, emitTestLua(t, input, input.Root)); emitted != before {
				t.Fatalf("test emitter changed the input: source %q, emitted %q", before, emitted)
			}
			after := run(t, emitTestLua(t, output, output.Root))
			if before != after {
				t.Fatalf("behavior changed:\nbefore %q\nafter %q\nsource:\n%s\noptimized:\n%s", before, after, source, emitTestLua(t, output, output.Root))
			}
		})
	}
}
