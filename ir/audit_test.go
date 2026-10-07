package ir

import (
	"reflect"
	"strings"
	"testing"

	"github.com/robogg133/glua/lex"
	"github.com/robogg133/glua/parser"
	"github.com/robogg133/glua/tokens"
)

func parseAudit(t *testing.T, source string) *parser.AST {
	t.Helper()
	a := parser.NewAst(tokens.NewTokenizer(lex.NewLexer(source), "audit.lua"))
	if err := a.Next(); err != nil {
		t.Fatal(err)
	}
	return a
}

func auditSource(t *testing.T, source string) (*parser.AST, Analysis) {
	t.Helper()
	a := parseAudit(t, source)
	nodes, values := append([]parser.Node(nil), a.Nodes...), append([]string(nil), a.Values...)
	root := a.Root
	r, err := Audit(a)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Nodes, nodes) || !reflect.DeepEqual(a.Values, values) || a.Root != root {
		t.Fatal("Audit modified its input")
	}
	if len(r.References) != len(a.Nodes) || r.Bindings[0] != (Binding{}) || r.References[0] != 0 {
		t.Fatal("invalid result sentinels/length")
	}
	for id, b := range r.Bindings[1:] {
		if b.Node != 0 && (a.Nodes[b.Node].Kind != parser.TagIdentifier || r.References[b.Node] != uint32(id+1) || a.Values[b.Node] != b.Name) {
			t.Fatalf("invalid declaration: %+v", b)
		}
		if b.Function != a.Root && a.Nodes[b.Function].Kind != parser.TagFunctionExpression {
			t.Fatalf("invalid owner: %+v", b)
		}
	}
	return a, r
}

func bindingsNamed(r Analysis, name string) []uint32 {
	var ids []uint32
	for id, b := range r.Bindings {
		if id != 0 && b.Name == name {
			ids = append(ids, uint32(id))
		}
	}
	return ids
}

func refsNamed(a *parser.AST, r Analysis, name string) []uint32 {
	var ids []uint32
	for i, n := range a.Nodes {
		if a.Values[i] == name && (n.Kind == parser.TagNameReference || n.Kind == parser.TagNameTarget) {
			ids = append(ids, r.References[i])
		}
	}
	return ids
}

func TestAuditScopesAndInitializers(t *testing.T) {
	a, r := auditSource(t, `
local x = 1
local x, y = x, x
do local x = y; use(x) end
use(x, y)
local a, b, c = call()
local d, e, f = call(), 7
local g, h = (call())
local i, j = ...
local missing
local explicit = nil
local same, same = x, y
use(same)
`)
	x := bindingsNamed(r, "x")
	y := bindingsNamed(r, "y")[0]
	if got, want := refsNamed(a, r, "x"), []uint32{x[0], x[0], x[2], x[1], x[1]}; !reflect.DeepEqual(got, want) {
		t.Fatalf("x references: %v want %v", got, want)
	}
	if r.References[r.Bindings[x[1]].Initializer] != x[0] || r.References[r.Bindings[y].Initializer] != x[0] {
		t.Fatal("initializers entered new scope prematurely")
	}
	for _, name := range []string{"b", "c", "f", "h", "j", "missing"} {
		if b := r.Bindings[bindingsNamed(r, name)[0]]; b.Initializer != 0 {
			t.Fatalf("%s should have unknown initializer: %+v", name, b)
		}
	}
	for name, tag := range map[string]parser.Tag{"a": parser.TagCallExpression, "d": parser.TagCallExpression, "e": parser.TagInteger, "g": parser.TagParenthesized, "i": parser.TagVararg, "explicit": parser.TagNil} {
		b := r.Bindings[bindingsNamed(r, name)[0]]
		if b.Initializer == 0 || a.Nodes[b.Initializer].Kind != tag {
			t.Fatalf("%s initializer: %+v", name, b)
		}
	}
	same := bindingsNamed(r, "same")
	if got := refsNamed(a, r, "same"); !reflect.DeepEqual(got, []uint32{same[1]}) {
		t.Fatalf("duplicate locals: %v", got)
	}
}

func TestAuditCapturesWritesAndFunctionPaths(t *testing.T) {
	a, r := auditSource(t, `
local x, obj = 1, {}
local function recursive(p, ... args)
  x = x + p
  recursive()
  return function() x = 3; return p, args, obj end
end
x = 4
function x() return x end
function obj.field() return obj end
function obj:method(q) self.field = q; return self, x end
obj.field = 2
obj[x] = 3
function unknown() end
local anon = function(...) return ... end
`)
	x := r.Bindings[bindingsNamed(r, "x")[0]]
	if x.Writes != 4 || !x.Captured || x.Function != a.Root {
		t.Fatalf("x: %+v", x)
	}
	obj := r.Bindings[bindingsNamed(r, "obj")[0]]
	if obj.Writes != 0 || !obj.Captured {
		t.Fatalf("object mutations must not write binding: %+v", obj)
	}
	rec := r.Bindings[bindingsNamed(r, "recursive")[0]]
	if !rec.Captured || rec.Writes != 0 || a.Nodes[rec.Initializer].Kind != parser.TagFunctionExpression {
		t.Fatalf("recursive local function: %+v", rec)
	}
	for _, name := range []string{"p", "args"} {
		b := r.Bindings[bindingsNamed(r, name)[0]]
		if !b.Captured || b.Function != rec.Initializer || b.Initializer != 0 {
			t.Fatalf("parameter: %+v", b)
		}
	}
	for _, name := range []string{"self", "q", "..."} {
		b := r.Bindings[bindingsNamed(r, name)[0]]
		if b.Function == a.Root || b.Initializer != 0 || b.Writes != 0 || b.Captured {
			t.Fatalf("parameter: %+v", b)
		}
		if name == "..." && b.Node != 0 {
			t.Fatal("unnamed vararg has no identifier")
		}
	}
	for i, n := range a.Nodes {
		if n.Kind == parser.TagFunctionDeclaration || n.Kind == parser.TagMethodDeclaration {
			base := a.Nodes[n.Left].Left
			want := uint32(0)
			if ids := bindingsNamed(r, a.Values[base]); len(ids) != 0 {
				want = ids[0]
			}
			if r.References[base] != want {
				t.Fatalf("function base %d: %d want %d", i, r.References[base], want)
			}
		}
		if n.Kind == parser.TagIdentifier && (a.Values[i] == "field" || a.Values[i] == "method") && r.References[i] != 0 {
			t.Fatal("field identifier resolved as lexical variable")
		}
	}
}

func TestAuditLoopBranchAndRepeatScopes(t *testing.T) {
	a, r := auditSource(t, `
local x = 10
for x = x, x, x do use(x); local fn = function() return x end end
for x, y in iterator(x) do y = x; use(y) end
repeat local x = x; x = x + 1 until x > 5
if x then local x = x; use(x) elseif x then local x = x; use(x) else local x = x; use(x) end
while x do local x = x; use(x); break end
use(x, y)
`)
	x := bindingsNamed(r, "x")
	if got, want := refsNamed(a, r, "x"), []uint32{
		x[0], x[0], x[0], x[1], x[1], x[0], x[2],
		x[0], x[3], x[3], x[3],
		x[0], x[0], x[4], x[0], x[0], x[5], x[0], x[6],
		x[0], x[0], x[7], x[0],
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scope refs:\n%v\nwant\n%v", got, want)
	}
	if !r.Bindings[x[1]].Captured || r.Bindings[x[1]].Initializer != 0 || r.Bindings[x[2]].Initializer != 0 || r.Bindings[x[3]].Writes != 1 {
		t.Fatal("loop/repeat metadata")
	}
	y := bindingsNamed(r, "y")[0]
	if r.Bindings[y].Writes != 1 || r.Bindings[y].Initializer != 0 {
		t.Fatalf("generic loop binding: %+v", r.Bindings[y])
	}
	if got := refsNamed(a, r, "y"); !reflect.DeepEqual(got, []uint32{y, y, 0}) {
		t.Fatalf("y scope: %v", got)
	}
}

func TestAuditGlobalBarriersAndAttributes(t *testing.T) {
	a, r := auditSource(t, `
local x = 1
local <const> a, b = 2, 3
local resource <close> = acquire()
do
  global x = x
  x = 2
  function x() return x end
  local x = x
  x = 3
end
use(x)
do global function x() return x end end
do global *; use(x) end
`)
	x := bindingsNamed(r, "x")
	if len(x) != 2 || r.Bindings[x[0]].Writes != 0 || r.Bindings[x[0]].Captured || r.Bindings[x[1]].Writes != 1 {
		t.Fatalf("global barrier leaked: %+v", r.Bindings)
	}
	if got, want := refsNamed(a, r, "x"), []uint32{x[0], 0, 0, 0, x[1], x[0], 0, x[0]}; !reflect.DeepEqual(got, want) {
		t.Fatalf("global refs: %v want %v", got, want)
	}
	for name, attr := range map[string]string{"a": "const", "b": "const", "resource": "close"} {
		if r.Bindings[bindingsNamed(r, name)[0]].Attribute != attr {
			t.Fatalf("lost attribute of %s", name)
		}
	}
}

// Together these sources exercise every current parser tag, including all
// operator families, optional list shapes, and shared default attributes.
var allTagSources = []string{`
;
local <const> a, b = nil, true
local x, t = 1, {2, [3] = 4, key = 5}
local f = function(p, ... rest) return p, rest, ... end
local function recurse(...) return recurse(...) end
global g
x, t.key, t[x] = false, 1.5, "str"
function t.a() return (x) end
function t:m(p) return self, p end
global function g() end
f(); f{}; f"s"; t:m()
if x then do end elseif false then ; else ; end
while x do break end
repeat local q = x until q
for i = 1, 2 do end
for i = 1, 2, 1 do end
for k, v in f() do end
goto done
::done::
return not x, -x, ~x, #t, x or x, x and x, x == x, x ~= x,
 x < x, x <= x, x > x, x >= x, x | x, x ~ x, x & x,
 x << x, x >> x, x .. x, x + x, x - x, x * x, x % x,
 x / x, x // x, x ^ x, t.key, t[x]
`, `global <const> *`, `local f = function() end; return`, `global x = 1`}

func TestAuditAllTags(t *testing.T) {
	seen := make(map[parser.Tag]bool)
	for _, source := range allTagSources {
		a, _ := auditSource(t, source)
		for _, n := range a.Nodes {
			seen[n.Kind] = true
		}
	}
	for tag := parser.TagNone; tag <= parser.TagPower; tag++ {
		if !seen[tag] {
			t.Errorf("tag %d was not exercised", tag)
		}
	}
}

func cloneArena(a *parser.AST) *parser.AST {
	return &parser.AST{Nodes: append([]parser.Node(nil), a.Nodes...), Values: append([]string(nil), a.Values...), Root: a.Root}
}

func TestAuditRejectsMalformedArena(t *testing.T) {
	base := parseAudit(t, `local x = 1; x = x + 2; return x`)
	find := func(a *parser.AST, tag parser.Tag) uint32 {
		for i, n := range a.Nodes {
			if n.Kind == tag {
				return uint32(i)
			}
		}
		t.Fatalf("missing tag %d", tag)
		return 0
	}
	tests := map[string]func(*parser.AST){
		"values length":           func(a *parser.AST) { a.Values = a.Values[:len(a.Values)-1] },
		"sentinel":                func(a *parser.AST) { a.Nodes[0].Left = a.Root },
		"sentinel value":          func(a *parser.AST) { a.Values[0] = "bad" },
		"zero root":               func(a *parser.AST) { a.Root = 0 },
		"root bounds":             func(a *parser.AST) { a.Root = ^uint32(0) },
		"root kind":               func(a *parser.AST) { a.Root = find(a, parser.TagBlock) },
		"child bounds":            func(a *parser.AST) { a.Nodes[a.Root].Left = ^uint32(0) },
		"missing block":           func(a *parser.AST) { a.Nodes[a.Root].Left = 0 },
		"unknown tag":             func(a *parser.AST) { a.Nodes[find(a, parser.TagInteger)].Kind = 255 },
		"none tag":                func(a *parser.AST) { a.Nodes[find(a, parser.TagInteger)].Kind = parser.TagNone },
		"leaf child":              func(a *parser.AST) { a.Nodes[find(a, parser.TagInteger)].Left = a.Root },
		"list cycle":              func(a *parser.AST) { i := find(a, parser.TagList); a.Nodes[i].Right = i },
		"expression cycle":        func(a *parser.AST) { i := find(a, parser.TagAdd); a.Nodes[i].Left = i },
		"shared expression":       func(a *parser.AST) { i := find(a, parser.TagAdd); a.Nodes[i].Right = a.Nodes[i].Left },
		"statement as value":      func(a *parser.AST) { a.Nodes[find(a, parser.TagInteger)].Kind = parser.TagEmpty },
		"expression as statement": func(a *parser.AST) { a.Nodes[find(a, parser.TagAssignment)].Kind = parser.TagAdd },
		"wrong binding category":  func(a *parser.AST) { a.Nodes[find(a, parser.TagBinding)].Kind = parser.TagParameter },
		"wrong target category":   func(a *parser.AST) { a.Nodes[find(a, parser.TagNameTarget)].Kind = parser.TagNameReference },
		"missing name":            func(a *parser.AST) { a.Values[find(a, parser.TagIdentifier)] = "" },
		"unreachable": func(a *parser.AST) {
			a.Nodes = append(a.Nodes, parser.Node{Kind: parser.TagEmpty})
			a.Values = append(a.Values, "")
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			a := cloneArena(base)
			mutate(a)
			before := cloneArena(a)
			r, err := Audit(a)
			if err == nil || !reflect.DeepEqual(r, Analysis{}) {
				t.Fatalf("accepted malformed arena: %+v, %v", r, err)
			}
			if !reflect.DeepEqual(a, before) {
				t.Fatal("modified malformed input")
			}
		})
	}
	for _, a := range []*parser.AST{nil, {}, {Nodes: []parser.Node{{}}, Values: []string{""}}} {
		if _, err := Audit(a); err == nil {
			t.Fatal("accepted empty input")
		}
	}
}

func TestAuditRejectsWrongListCategories(t *testing.T) {
	for _, tc := range []struct {
		source   string
		from, to parser.Tag
	}{
		{`local f = function(p) end`, parser.TagParameter, parser.TagBinding},
		{`if true then end`, parser.TagBranch, parser.TagWhile},
		{`local t = {1}`, parser.TagArrayField, parser.TagReturn},
		{`function t.f() end`, parser.TagIdentifier, parser.TagNameReference},
		{`for a, b in f() do end`, parser.TagIdentifier, parser.TagNameReference},
	} {
		a := parseAudit(t, tc.source)
		for i, n := range a.Nodes {
			if n.Kind == tc.from {
				a.Nodes[i].Kind = tc.to
				break
			}
		}
		if _, err := Audit(a); err == nil {
			t.Errorf("accepted wrong category: %s", tc.source)
		}
	}
}

func TestAuditAuxiliaryShapes(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		tag          parser.Tag
		mutate       func(*parser.AST, uint32)
	}{
		{"missing numeric limit", `for i = 1, 2 do end`, parser.TagNumericFor, func(a *parser.AST, i uint32) {
			list := a.Nodes[a.Nodes[i].Left].Right
			a.Nodes[a.Nodes[list].Right].Left = 0
		}},
		{"missing step cell", `for i = 1, 2 do end`, parser.TagNumericFor, func(a *parser.AST, i uint32) {
			list := a.Nodes[a.Nodes[i].Left].Right
			a.Nodes[a.Nodes[list].Right].Right = 0
		}},
		{"extra step cell", `for i = 1, 2 do end`, parser.TagNumericFor, func(a *parser.AST, i uint32) {
			list := a.Nodes[a.Nodes[i].Left].Right
			last := a.Nodes[a.Nodes[list].Right].Right
			a.Nodes[last].Right = list
		}},
		{"wrong method pair", `t:m()`, parser.TagMethodCallExpression, func(a *parser.AST, i uint32) {
			a.Nodes[a.Nodes[i].Left].Kind = parser.TagList
		}},
		{"missing abbreviated argument", `f{}`, parser.TagTableArgumentCall, func(a *parser.AST, i uint32) {
			a.Nodes[i].Right = 0
		}},
		{"wrong abbreviated argument", `f"s"`, parser.TagString, func(a *parser.AST, i uint32) {
			a.Nodes[i].Kind = parser.TagInteger
		}},
		{"non-call statement", `f()`, parser.TagCallExpression, func(a *parser.AST, i uint32) {
			a.Nodes[i].Kind = parser.TagIndexAccess
		}},
		{"invalid attribute", `local x <const> = 1`, parser.TagAttribute, func(a *parser.AST, i uint32) {
			a.Values[i] = "invalid"
		}},
		{"invalid boolean", `return true`, parser.TagBoolean, func(a *parser.AST, i uint32) {
			a.Values[i] = "invalid"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := parseAudit(t, tc.source)
			for i, n := range a.Nodes {
				if n.Kind == tc.tag {
					tc.mutate(a, uint32(i))
					break
				}
			}
			if _, err := Audit(a); err == nil {
				t.Fatal("accepted malformed auxiliary shape")
			}
		})
	}
}

func TestAuditChildBoundsForEveryTag(t *testing.T) {
	for _, source := range allTagSources {
		base := parseAudit(t, source)
		for i, n := range base.Nodes {
			for _, left := range []bool{true, false} {
				a := cloneArena(base)
				if left {
					a.Nodes[i].Left = ^uint32(0)
				} else {
					a.Nodes[i].Right = ^uint32(0)
				}
				if _, err := Audit(a); err == nil {
					t.Fatalf("accepted invalid link at node %d tag %d (left=%v)", i, n.Kind, left)
				}
			}
		}
	}
}

func TestAuditLongListsAndDepthBound(t *testing.T) {
	_, r := auditSource(t, strings.Repeat("local x = 1; ", 12000))
	if len(r.Bindings) != 12001 {
		t.Fatal("lost long-list declarations")
	}
	a := parseAudit(t, "return "+strings.Repeat("1 + ", maxAuditDepth+50)+"1")
	if _, err := Audit(a); err == nil || !strings.Contains(err.Error(), "nesting exceeds") {
		t.Fatalf("expected explicit nesting limit: %v", err)
	}
	// Lists do not consume the nesting budget, and validation itself is iterative.
	a = parseAudit(t, "return "+strings.Repeat("1, ", 12000)+"1")
	if _, err := Audit(a); err != nil {
		t.Fatal(err)
	}
}

func FuzzAuditArena(f *testing.F) {
	for _, source := range allTagSources {
		f.Add(source, uint32(0), uint32(0), uint32(0), uint8(0))
	}
	f.Fuzz(func(t *testing.T, source string, index, left, right uint32, tag uint8) {
		a := parser.NewAst(tokens.NewTokenizer(lex.NewLexer(source), "fuzz.lua"))
		if err := a.Next(); err != nil {
			return
		}
		if _, err := Audit(a); err != nil && !strings.Contains(err.Error(), "nesting exceeds") {
			t.Fatalf("rejected parser AST: %v", err)
		}
		i := index % uint32(len(a.Nodes))
		a.Nodes[i].Left, a.Nodes[i].Right, a.Nodes[i].Kind = left, right, parser.Tag(tag)
		before := cloneArena(a)
		_, _ = Audit(a) // Every mutation must terminate without panic or writes.
		if !reflect.DeepEqual(a.Nodes, before.Nodes) || !reflect.DeepEqual(a.Values, before.Values) || a.Root != before.Root {
			t.Fatal("modified input")
		}
	})
}
