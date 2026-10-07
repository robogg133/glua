package ir

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/robogg133/glua/lex"
	"github.com/robogg133/glua/parser"
	"github.com/robogg133/glua/tokens"
)

func optimizeSource(t *testing.T, source string) (*parser.AST, Report) {
	t.Helper()
	input := parseAudit(t, source)
	before := cloneArena(input)
	output, report, err := Optimize(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, cloneArena(input)) {
		t.Fatal("input mutated")
	}
	if _, err := Audit(output); err != nil {
		t.Fatal(err)
	}
	if output == input || &output.Nodes[0] == &input.Nodes[0] || &output.Values[0] == &input.Values[0] {
		t.Fatal("output aliases input")
	}
	if report.BeforeNodes != len(input.Nodes)-1 || report.AfterNodes != len(output.Nodes)-1 {
		t.Fatal("invalid counts")
	}
	for _, c := range report.Changes {
		if c.Node == 0 || int(c.Node) >= len(input.Nodes) || c.Position != input.Nodes[c.Node].TokenPosition || c.Kind == "" || c.Message == "" {
			t.Fatalf("invalid change: %+v", c)
		}
	}
	again, r2, err := Optimize(input)
	if err != nil || !reflect.DeepEqual(output, again) || !reflect.DeepEqual(report, r2) {
		t.Fatalf("nondeterministic optimization: %v", err)
	}
	// Re-optimization may discover more constants after removal of dead writes.
	if _, _, err := Optimize(output); err != nil {
		t.Fatalf("output cannot be optimized again: %v", err)
	}
	return output, report
}
func optItems(a *parser.AST, list uint32) []uint32 {
	var items []uint32
	for list != 0 {
		items = append(items, a.Nodes[list].Left)
		list = a.Nodes[list].Right
	}
	return items
}
func optStatements(a *parser.AST) []uint32 { return optItems(a, a.Nodes[a.Nodes[a.Root].Left].Left) }
func optReturns(t *testing.T, a *parser.AST) []uint32 {
	t.Helper()
	statements := optStatements(a)
	n := a.Nodes[statements[len(statements)-1]]
	if n.Kind != parser.TagReturn {
		t.Fatalf("last root statement is %d", n.Kind)
	}
	return optItems(a, n.Left)
}
func optCount(a *parser.AST, kind parser.Tag) int {
	count := 0
	for _, n := range a.Nodes {
		if n.Kind == kind {
			count++
		}
	}
	return count
}
func optHasValue(a *parser.AST, value string) bool {
	for _, v := range a.Values {
		if v == value {
			return true
		}
	}
	return false
}
func optChanged(r Report, kind string) bool {
	for _, c := range r.Changes {
		if c.Kind == kind {
			return true
		}
	}
	return false
}

func TestOptimizeConstantsAndInlining(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         []constant
		inline       bool
	}{
		{"fold arithmetic", "return 1+2*3, -7//3, 7%-3, ~0, 0xffffffffffffffff >> 1", []constant{integerConstant(7), integerConstant(-3), integerConstant(-2), integerConstant(-1), integerConstant(9223372036854775807)}, false},
		{"short circuit", "return false and effect(), true or effect(), false or 9, true and 0", []constant{booleanConstant(false), booleanConstant(true), integerConstant(9), integerConstant(0)}, false},
		{"propagation", "local a=20; local b<const> = a+1; return b*2", []constant{integerConstant(42)}, false},
		{"local function", "local function twice(x) return x*2 end; return twice(21)", []constant{integerConstant(42)}, true},
		{"closure capture", "local k=2; local function twice(x) return x*k end; return twice(21)", []constant{integerConstant(42)}, true},
		{"direct function", "return (function(x) return x+1 end)(4)", []constant{integerConstant(5)}, true},
		{"literal function binding", "local f=(function(x) return x+1 end); return f(4)", []constant{integerConstant(5)}, true},
		{"unused constant extras", "local function f(x) return x end; return f(4,2+3)", []constant{integerConstant(4)}, true},
		{"missing argument", "local function f(x,y) return y end; return f(4)", []constant{{parser.TagNil, "nil"}}, true},
		{"duplicate parameters", "local function f(x,x) return x end; return f(1,2)", []constant{integerConstant(2)}, true},
		{"string shorthand", "local function size(x) return #x end; return size 'é'", []constant{integerConstant(2)}, true},
		{"nested constant call", "local function one() return 1 end; local function inc(x) return x+1 end; return inc(one())", []constant{integerConstant(2)}, true},
		{"exact numeric compare", "return 9007199254740993 == 9007199254740992.0, 0x7fffffffffffffff < 0x1p63", []constant{booleanConstant(false), booleanConstant(true)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, r := optimizeSource(t, tc.source)
			returns := optReturns(t, a)
			if len(returns) != len(tc.want) {
				t.Fatal("return count changed")
			}
			for i, id := range returns {
				if got, ok := constantOf(a, id); !ok || got != tc.want[i] {
					t.Errorf("return %d: %v,%v want %v", i, got, ok, tc.want[i])
				}
			}
			if tc.inline && !optChanged(r, "inline") {
				t.Fatal("missing inline report")
			}
		})
	}
}

func TestOptimizeConstantReportAndScopes(t *testing.T) {
	a, r := optimizeSource(t, `local x=1; local y<const> = x+2; do local x=9; use(x) end; local function f() return y end; return x,y,f()`)
	if len(r.Constants) != 3 {
		t.Fatalf("constants: %+v", r.Constants)
	}
	explicit := 0
	for _, c := range r.Constants {
		if c.Explicit {
			explicit++
		}
		if c.Declaration != r.Analysis.Bindings[c.Binding].Node {
			t.Fatal("wrong declaration")
		}
	}
	if explicit != 1 {
		t.Fatal("explicit attributes not reported")
	}
	returns := optReturns(t, a)
	for i, want := range []constant{integerConstant(1), integerConstant(3), integerConstant(3)} {
		if got, ok := constantOf(a, returns[i]); !ok || got != want {
			t.Fatalf("incorrect shadow resolution: %v", got)
		}
	}
	for _, source := range []string{
		`local x=1; x=2; return x`,
		`local x=1; local function f() x=2 end; f(); return x`,
		`local x<close> = nil; return x`,
		`global <const> x=1; return x`,
		`global *; local x=1; do global x; use(x) end; return x`,
		`local x=1; function x() end; return x`,
		`local x=1; if unknown then x=2 end; return x`,
	} {
		t.Run(source, func(t *testing.T) {
			out, _ := optimizeSource(t, source)
			if optCount(out, parser.TagNameReference) == 0 {
				t.Fatal("unsafe propagation")
			}
		})
	}
}

func TestOptimizeRefusesUnsafeInlining(t *testing.T) {
	for _, source := range []string{
		`local function f(x) return x end; return f(effect())`,
		`local function f(x) return x end; return f(1,effect())`,
		`local function f() effect(); return 1 end; return f()`,
		`local function f() return 1,2 end; return f()`,
		`local function f() end; return f()`,
		`local function f(...) return 1 end; return f()`,
		`local function f(... args) return 1 end; return f()`,
		`local function f() return f() end; return f()`,
		`local function f() return 1 end; f=other; return f()`,
		`local function f() return 1 end; local function change() f=other end; return f()`,
		`local function f() return globalValue end; return f()`,
		`local x=1; local function f() return x end; x=2; return f()`,
		`function f() return 1 end; return f()`,
		`local t={}; function t:f() return 1 end; return t:f()`,
		`local function f(x) return x end; return f {}`,
		`local function f(a,b,c,d,e,f,g,h,i) return 1 end; return f()`,
		`local function f(x) return x+x+x+x+x+x+x end; return f(2)`,
		`local function f(x) return x+1 end; return f('not a number')`,
	} {
		t.Run(source, func(t *testing.T) {
			a, r := optimizeSource(t, source)
			if optChanged(r, "inline") {
				t.Fatal("unsafe call inlined")
			}
			last := optReturns(t, a)
			if !isCall(a.Nodes[last[0]].Kind) {
				t.Fatal("call removed")
			}
		})
	}
	a, r := optimizeSource(t, `local function f() return 1 end; f()`)
	if optChanged(r, "inline") || optCount(a, parser.TagCallStatement) != 1 {
		t.Fatal("call statement turned into invalid expression statement")
	}
}

func TestOptimizeBranchesAndDeadCode(t *testing.T) {
	for _, tc := range []struct {
		source  string
		absent  []string
		present []string
	}{
		{`if true then yes() else no() end; after()`, []string{"no"}, []string{"yes", "after"}},
		{`if false then no() elseif nil then never() else yes() end`, []string{"no", "never"}, []string{"yes"}},
		{`if 0 then yes() else no() end; if '' then yes2() end`, []string{"no"}, []string{"yes", "yes2"}},
		{`if cond() then yes() elseif true then other() elseif effect() then no() else never() end`, []string{"effect", "no", "never"}, []string{"cond", "yes", "other"}},
		{`while false do no() end; after()`, []string{"no"}, []string{"after"}},
		{`while true do end; no()`, []string{"no"}, nil},
		{`while true do while true do break end end; no()`, []string{"no"}, nil},
		{`while true do if cond() then break end end; after()`, nil, []string{"after"}},
		{`repeat step() until false; no()`, []string{"no"}, []string{"step"}},
		{`repeat break until false; after()`, nil, []string{"after"}},
		{`while true do goto L end; no(); ::L:: yes()`, []string{"no"}, []string{"L", "yes"}},
		{`while true do local f=function() while true do break end end end; no()`, []string{"no"}, nil},
		{`while cond() do break; no() end; after()`, []string{"no"}, []string{"cond", "after"}},
		{`goto L; no(); ::L:: yes()`, []string{"no"}, []string{"L", "yes"}},
		{`do return 1 end; no()`, []string{"no"}, nil},
		{`if cond() then return 1 else return 2 end; no()`, []string{"no"}, []string{"cond"}},
		{`if cond() then return 1 end; after()`, nil, []string{"after"}},
		{`do goto L; no() end; never(); ::L:: yes()`, []string{"no", "never"}, []string{"L", "yes"}},
		{`goto outer; do ::inner:: no() end; ::outer:: yes()`, []string{"inner", "no"}, []string{"outer", "yes"}},
		{`if effect() or true then yes() end`, nil, []string{"effect", "yes"}},
	} {
		t.Run(tc.source, func(t *testing.T) {
			a, _ := optimizeSource(t, tc.source)
			for _, v := range tc.absent {
				if optHasValue(a, v) {
					t.Errorf("unreachable %q retained", v)
				}
			}
			for _, v := range tc.present {
				if !optHasValue(a, v) {
					t.Errorf("reachable %q removed", v)
				}
			}
		})
	}
	a, _ := optimizeSource(t, `local x=1; if true then local x<close> = resource(); use(x) end; return x`)
	if optCount(a, parser.TagDoBlock) != 1 || !optHasValue(a, "close") {
		t.Fatal("selected branch lost its lexical/close scope")
	}
	out, _ := optimizeSource(t, `if condition() then a() elseif true then b() end`)
	n := out.Nodes[optStatements(out)[0]]
	if n.Kind != parser.TagIf || len(optItems(out, n.Left)) != 1 || n.Right == 0 {
		t.Fatal("constant elseif not converted to else")
	}
}

func TestOptimizePreservesMultipleResults(t *testing.T) {
	a, _ := optimizeSource(t, `return true and f(), false or g(), (h()), f(), ...`)
	results := optReturns(t, a)
	for _, i := range []int{0, 1, 2} {
		if a.Nodes[results[i]].Kind != parser.TagParenthesized || !isCall(a.Nodes[a.Nodes[results[i]].Left].Kind) {
			t.Fatal("single-result adjustment lost")
		}
	}
	if a.Nodes[results[3]].Kind != parser.TagCallExpression || a.Nodes[results[4]].Kind != parser.TagVararg {
		t.Fatal("expandable expressions changed")
	}
	a, _ = optimizeSource(t, `local function f() return 1,2 end; local a,b=f(); return a,b`)
	for _, n := range optReturns(t, a) {
		if a.Nodes[n].Kind != parser.TagNameReference {
			t.Fatal("expanded return incorrectly inferred constant")
		}
	}
	a, _ = optimizeSource(t, `local function f() return 1 end; local a,b=f(); return a,b`)
	r := optReturns(t, a)
	if c, ok := constantOf(a, r[0]); !ok || c != integerConstant(1) || a.Nodes[r[1]].Kind != parser.TagNameReference {
		t.Fatal("single-result initializer mapping incorrect")
	}
}

func TestOptimizePreservesErrorsAndIdentity(t *testing.T) {
	a, _ := optimizeSource(t, `return 1//0, 1%0, 'x'+1, {}=={}, function() end, 'a'<'b', 10^0.1`)
	results := optReturns(t, a)
	for i, want := range []parser.Tag{parser.TagIntegerDivide, parser.TagModulo, parser.TagAdd, parser.TagEqual, parser.TagFunctionExpression, parser.TagLess, parser.TagPower} {
		if a.Nodes[results[i]].Kind != want {
			t.Errorf("expression %d folded unsafely", i)
		}
	}
	out, _ := optimizeSource(t, `local a,b=1,2; a,b=b,a; return a,b`)
	if optCount(out, parser.TagAssignment) != 1 || optCount(out, parser.TagNameTarget) != 2 {
		t.Fatal("multiple assignment changed")
	}
}

func TestOptimizeAllTagsAndInvalidInput(t *testing.T) {
	for _, source := range allTagSources {
		optimizeSource(t, source)
	}
	for _, input := range []*parser.AST{nil, {}, {Nodes: []parser.Node{{}}, Values: []string{""}}} {
		a, r, err := Optimize(input)
		if err == nil || a != nil || !reflect.DeepEqual(r, Report{}) {
			t.Fatal("accepted invalid input")
		}
	}
	a := parseAudit(t, `return 1`)
	a.Nodes[a.Root].Left = a.Root
	if _, _, err := Optimize(a); err == nil {
		t.Fatal("accepted cycle")
	}
}

func TestOptimizeAllocationBudget(t *testing.T) {
	var source strings.Builder
	source.WriteString("local s0='a';")
	for i := 1; i < 30; i++ {
		fmt.Fprintf(&source, "local s%d=s%d..s%d;", i, i-1, i-1)
	}
	source.WriteString("return s29")
	a, r := optimizeSource(t, source.String())
	if a.Nodes[optReturns(t, a)[0]].Kind != parser.TagNameReference {
		t.Fatal("unbounded string folding")
	}
	for _, c := range r.Constants {
		if len(c.Value) > maxFoldedString {
			t.Fatal("constant allocation exceeds budget")
		}
	}
}

func FuzzOptimize(f *testing.F) {
	for _, source := range append([]string{
		`local x=1; if x==1 then return x+2 end`,
		`local function twice(x) return x*2 end; return twice(21)`,
		`return true and f(),false or ...`,
		`goto L; no(); ::L:: yes()`,
		`while true do break; no() end`,
		`local x=1; local function f() x=2 end; return x`,
	}, allTagSources...) {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 4096 {
			t.Skip()
		}
		input := parser.NewAst(tokens.NewTokenizer(lex.NewLexer(source), "fuzz.lua"))
		if input.Next() != nil {
			return
		}
		before := cloneArena(input)
		// Audit can intentionally reject excessive nesting beyond the parser limit
		// for long left-associated expressions; Optimize must return that error.
		_, auditErr := Audit(input)
		output, report, err := Optimize(input)
		if auditErr != nil {
			if err == nil {
				t.Fatal("audit error ignored")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, cloneArena(input)) {
			t.Fatal("input mutated")
		}
		if _, err := Audit(output); err != nil {
			t.Fatal(err)
		}
		again, r, err := Optimize(input)
		if err != nil || !reflect.DeepEqual(output, again) || !reflect.DeepEqual(report, r) {
			t.Fatal("nondeterministic output")
		}
		if _, _, err := Optimize(output); err != nil {
			t.Fatal(err)
		}
	})
}
