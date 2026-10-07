package parser_test

import (
	"strings"
	"testing"

	"github.com/robogg133/glua/lex"
	"github.com/robogg133/glua/parser"
	"github.com/robogg133/glua/tokens"
)

func TestPostASTValidation(t *testing.T) {
	cases := []struct{ name, source, want string }{
		{"forward", "goto L; ::L::", ""},
		{"backward", "::L:: local x; goto L", ""},
		{"missing", "goto missing", "no visible label"},
		{"child invisible", "goto L; do ::L:: end", "no visible label"},
		{"sibling invisible", "do goto L end; do ::L:: end", "no visible label"},
		{"function boundary", "::L:: local function f() goto L end", "no visible label"},
		{"inner label invisible", "local function f() ::L:: end; goto L", "no visible label"},
		{"duplicate", "::L:: ; ::L::", "already defined"},
		{"previous outer label", "::L:: do ::L:: end", "already defined"},
		{"sibling labels", "do ::L:: end; do ::L:: end", ""},
		{"function labels", "::L:: local function f() ::L:: end", ""},
		{"enter local", "goto L; local x; ::L:: x=1", "scope of variable 'x'"},
		{"same count different locals", "do local a; goto L end; local b; ::L:: b=1", "scope of variable 'b'"},
		{"terminal labels", "goto L; local x; ::L:: ; ::M:: ;", ""},
		{"outer terminal label", "do goto L end; local x; ::L::", ""},
		{"repeat label", "repeat goto L; local x; ::L:: until true", "scope of variable 'x'"},
		{"repeat condition scope", "repeat local x<const> = 1 until (function() x=2 end)()", "const variable 'x'"},
		{"repeat condition local", "global none; repeat local x=true until x", ""},
		{"const", "local x<const> = 1; x=2", "const variable 'x'"},
		{"close capture", "local x<close> = nil; local function f() local function g() x=nil end end", "const variable 'x'"},
		{"initializer outer", "local x<const> = 1; local x=(function() x=2 end)()", "const variable 'x'"},
		{"parallel initializer", "local x<const> = 1; local x,y=2,(function() x=3 end)()", "const variable 'x'"},
		{"shadow", "local x<const> = 1; do local x; x=2 end", ""},
		{"scope restored", "local x<const> = 1; do local x end; x=2", "const variable 'x'"},
		{"recursive local function", "local f<const> = 1; local function f() f=function() end end", ""},
		{"nonrecursive initializer", "local f<const> = 1; local f=function() f=2 end", "const variable 'f'"},
		{"parameter shadow", "local x<const> = 1; local function f(x) x=2 end", ""},
		{"implicit self", "local self<const> = 1; function t:m() self=2 end", ""},
		{"named vararg", "local function f(... args) args={} end", "const variable 'args'"},
		{"vararg fields", "local function f(... args) args[1]=2 end", ""},
		{"numeric control", "for i=1,2 do i=2 end", "const variable 'i'"},
		{"numeric capture", "for i=1,2 do local function f() i=2 end end", "const variable 'i'"},
		{"numeric shadow", "for i=1,2 do local i; i=2 end", ""},
		{"generic control", "for k,v in iter() do k=2 end", "const variable 'k'"},
		{"generic other variable", "local v<const> = 1; for k,v in iter() do v=2 end", ""},
		{"for initializer scope", "local i<const> = 1; for i=(function() i=2 end)(),2 do end", "const variable 'i'"},
		{"field assignment", "local x<const> = {}; x.a=1; x[1]=2; function x:f() end; function x.a.b() end", ""},
		{"function assignment", "local f<const> = 1; function f() end", "const variable 'f'"},
		{"target index closure", "local x<const> = 1; t[(function() x=2 end)()]=1", "const variable 'x'"},
		{"table closure", "local x<const> = 1; local t={f=function() x=2 end}", "const variable 'x'"},
		{"global const", "global x<const> = 1; x=2", "const variable 'x'"},
		{"global const capture", "global x<const>; local function f() x=2 end", "const variable 'x'"},
		{"global restriction", "global x; y=1", "variable 'y' not declared"},
		{"global read", "global x; local y=z", "variable 'z' not declared"},
		{"global initializer old scope", "global x=x", ""},
		{"global initializer restricted", "global none; global x=x", "variable 'x' not declared"},
		{"explicit wildcard", "global *; global x; y=1", ""},
		{"const wildcard", "global<const> *; x=1", "const variable 'x'"},
		{"wildcard preserves explicit", "global x; global<const> *; x=1; local y=math.pi", ""},
		{"wildcard preserves local", "local x; global<const> *; x=1", ""},
		{"global shadows local", "local x<const> = 1; global x; x=2", ""},
		{"local shadows global", "global x<const>; local x; x=2", ""},
		{"global redeclaration", "global x<const>; global x=2; x=3", ""},
		{"global function recursion", "global none; global function f() return f() end", ""},
		{"global function const shadow", "global f<const>; global function f() f=nil end", ""},
		{"global scope restored", "do global x end; y=1", ""},
		{"global function isolation", "local function f() global x end; y=1", ""},
		{"global captured restriction", "global x; local function f() return y end", "variable 'y' not declared"},
		{"global goto", "goto L; global x; ::L:: x=1", "scope of variable 'x'"},
		{"wildcard goto", "goto L; global *; ::L:: x=1", "scope of variable '*'"},
		{"explicit environment escape", "global x<const>; _ENV.x=2", ""},
		{"const environment fields", "local _ENV<const> = {}; x=2", ""},
		{"global environment unused", "global _ENV", ""},
		{"global environment access", "global _ENV; global x; local y=x", "_ENV is global"},
		{"global environment initialization", "global _ENV; global x=1", "_ENV is global"},
		{"all branches", "local x<const> = 1; if true then elseif false then else x=2 end", "const variable 'x'"},
		{"while body", "local x<const> = 1; while true do x=2 end", "const variable 'x'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ast := parser.NewAst(tokens.NewTokenizer(lex.NewLexer(tc.source), "validate.lua"))
			err := ast.Next()
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if ast.Root == 0 {
					t.Fatal("successful parse has no root")
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("got %v; want %q", err, tc.want)
				}
				if !strings.HasPrefix(err.Error(), "validate.lua:1:") {
					t.Fatalf("missing source position: %v", err)
				}
				if ast.Root != 0 {
					t.Fatal("failed parse retained root")
				}
			}
			if again := ast.Next(); again != err {
				t.Fatalf("Next changed result: %v -> %v", err, again)
			}
		})
	}
}
