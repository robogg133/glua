package parser_test

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/robogg133/glua/lex"
	"github.com/robogg133/glua/parser"
	"github.com/robogg133/glua/tokens"
)

// Keep these names local to this suite rather than depending on token numbers.
var astSuiteTagNames = strings.Fields(`None Chunk Block List Pair Identifier NamePath
Parameter VarargParameter ImplicitSelf Attribute Binding Empty Assignment
LocalDeclaration GlobalDeclaration GlobalWildcardDeclaration FunctionDeclaration
MethodDeclaration LocalFunctionDeclaration GlobalFunctionDeclaration CallStatement
If Branch While RepeatUntil NumericFor GenericFor DoBlock Return Break Goto Label
NameTarget FieldTarget IndexTarget Nil Boolean Integer Float String NameReference
Vararg TableConstructor FunctionExpression Parenthesized FieldAccess IndexAccess
CallExpression MethodCallExpression TableArgumentCall StringArgumentCall ArrayField
KeyField NameField Not Negate BitwiseNot Length Or And Equal NotEqual Less LessEqual
Greater GreaterEqual BitOr BitXor BitAnd ShiftLeft ShiftRight Concat Add Subtract
Multiply Modulo Divide IntegerDivide Power`)

func astSuiteNew(source string) *parser.AST {
	return parser.NewAst(tokens.NewTokenizer(lex.NewLexer(source), "ast-suite.lua"))
}

// Failed parses may retain an incomplete arena, but never a usable root.
// Successful arenas may be DAGs (shared attributes), but must not contain cycles
// or garbage. The zero item in a numeric-for header means an omitted step.
func astSuiteInvariant(t testing.TB, a *parser.AST, err error) {
	t.Helper()
	if len(a.Nodes) == 0 || len(a.Nodes) != len(a.Values) {
		t.Fatalf("arena lengths: Nodes=%d Values=%d", len(a.Nodes), len(a.Values))
	}
	if a.Nodes[0] != (parser.Node{Kind: parser.TagNone}) || a.Values[0] != "" {
		t.Fatal("index zero is not the empty sentinel")
	}
	for i, n := range a.Nodes[1:] {
		if n.Kind <= parser.TagNone || n.Kind > parser.TagPower {
			t.Fatalf("node %d: invalid tag %d", i+1, n.Kind)
		}
		if int(n.Left) >= len(a.Nodes) || int(n.Right) >= len(a.Nodes) {
			t.Fatalf("node %d: links outside arena: %+v", i+1, n)
		}
		if n.TokenPosition[0] == 0 || n.TokenPosition[1] == 0 {
			t.Fatalf("node %d: invalid position %v", i+1, n.TokenPosition)
		}
		switch n.Kind {
		case parser.TagIdentifier, parser.TagAttribute, parser.TagNameTarget,
			parser.TagNil, parser.TagBoolean, parser.TagInteger, parser.TagFloat,
			parser.TagString, parser.TagNameReference, parser.TagVararg:
			if n.Left != 0 || n.Right != 0 {
				t.Fatalf("leaf %d has children", i+1)
			}
		default:
			if a.Values[i+1] != "" {
				t.Fatalf("structural node %d has value %q", i+1, a.Values[i+1])
			}
		}
		if n.Kind == parser.TagList && n.Right != 0 && a.Nodes[n.Right].Kind != parser.TagList {
			t.Fatalf("list %d has a non-list tail", i+1)
		}
	}
	if err != nil {
		if a.Root != 0 {
			t.Fatalf("failed parse retained root %d: %v", a.Root, err)
		}
		return
	}
	if a.Root == 0 || int(a.Root) >= len(a.Nodes) || a.Nodes[a.Root].Kind != parser.TagChunk {
		t.Fatalf("invalid chunk root %d", a.Root)
	}
	root := a.Nodes[a.Root]
	if root.Left == 0 || a.Nodes[root.Left].Kind != parser.TagBlock || root.Right != 0 {
		t.Fatalf("invalid chunk shape: %+v", root)
	}
	// Iterative DFS also handles very long, flat chunks without test recursion.
	type visit struct {
		index uint32
		exit  bool
	}
	state := make([]uint8, len(a.Nodes))
	stack := []visit{{index: a.Root}}
	for len(stack) != 0 {
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if v.index == 0 {
			continue
		}
		if v.exit {
			state[v.index] = 2
			continue
		}
		if state[v.index] == 1 {
			t.Fatalf("cycle at node %d", v.index)
		}
		if state[v.index] == 2 {
			continue
		}
		state[v.index] = 1
		n := a.Nodes[v.index]
		stack = append(stack, visit{v.index, true}, visit{index: n.Right}, visit{index: n.Left})
	}
	for i := 1; i < len(state); i++ {
		if state[i] != 2 {
			t.Errorf("unreachable node %d: %+v value=%q", i, a.Nodes[i], a.Values[i])
		}
	}
}

func astSuiteStable(t testing.TB, a *parser.AST, err error) {
	t.Helper()
	nodes := append([]parser.Node(nil), a.Nodes...)
	values := append([]string(nil), a.Values...)
	root := a.Root
	for i := 0; i < 3; i++ {
		if again := a.Next(); again != err {
			t.Fatalf("Next changed error: %v -> %v", err, again)
		}
		if a.Root != root || !reflect.DeepEqual(a.Nodes, nodes) || !reflect.DeepEqual(a.Values, values) {
			t.Fatal("Next mutated the completed arena")
		}
	}
}

func astSuiteParse(t testing.TB, source string) *parser.AST {
	t.Helper()
	a := astSuiteNew(source)
	if err := a.Next(); err != nil {
		t.Fatalf("parse %q: %v", source, err)
	}
	astSuiteInvariant(t, a, nil)
	astSuiteStable(t, a, nil)
	return a
}

func astSuiteList(t testing.TB, a *parser.AST, index uint32) []uint32 {
	t.Helper()
	var items []uint32
	for index != 0 {
		if len(items) >= len(a.Nodes) || int(index) >= len(a.Nodes) || a.Nodes[index].Kind != parser.TagList {
			t.Fatalf("invalid list at %d", index)
		}
		items = append(items, a.Nodes[index].Left)
		index = a.Nodes[index].Right
	}
	return items
}

func astSuiteStatements(t testing.TB, a *parser.AST) []uint32 {
	t.Helper()
	return astSuiteList(t, a, a.Nodes[a.Nodes[a.Root].Left].Left)
}

// An index-independent tree representation; zero children remain explicit.
func astSuiteShape(a *parser.AST, index uint32) string {
	if index == 0 {
		return "_"
	}
	n := a.Nodes[index]
	name := astSuiteTagNames[n.Kind]
	if n.Left != 0 || n.Right != 0 {
		return name + "(" + astSuiteShape(a, n.Left) + "," + astSuiteShape(a, n.Right) + ")"
	}
	if a.Values[index] != "" || n.Kind == parser.TagString {
		return name + "(" + strconv.Quote(a.Values[index]) + ")"
	}
	return name
}

func astSuiteWantShape(t testing.TB, a *parser.AST, index uint32, want string) {
	t.Helper()
	if got := astSuiteShape(a, index); got != want {
		t.Errorf("tree mismatch\n got: %s\nwant: %s", got, want)
	}
}

func astSuiteExpression(t testing.TB, source string) (*parser.AST, uint32) {
	t.Helper()
	a := astSuiteParse(t, "return "+source)
	statements := astSuiteStatements(t, a)
	if len(statements) != 1 || a.Nodes[statements[0]].Kind != parser.TagReturn {
		t.Fatal("expected one return statement")
	}
	values := astSuiteList(t, a, a.Nodes[statements[0]].Left)
	if len(values) != 1 {
		t.Fatalf("expected one expression, got %d", len(values))
	}
	return a, values[0]
}

func TestASTSuiteEmptyAndInitialState(t *testing.T) {
	a := astSuiteNew("")
	if a.Root != 0 || !reflect.DeepEqual(a.Nodes, []parser.Node{{Kind: parser.TagNone}}) || !reflect.DeepEqual(a.Values, []string{""}) {
		t.Fatalf("unexpected initial arena: %+v", a)
	}
	for _, source := range []string{"", " \t\r\n", "-- comment", "--[=[long\ncomment]=]"} {
		t.Run(strconv.Quote(source), func(t *testing.T) {
			a := astSuiteParse(t, source)
			astSuiteWantShape(t, a, a.Root, "Chunk(Block,_)")
			if len(a.Nodes) != 3 {
				t.Errorf("empty chunk allocated %d nodes; want 3", len(a.Nodes))
			}
		})
	}
}

var astSuiteBinaryOperators = []struct {
	text string
	kind parser.Tag
}{
	{"or", parser.TagOr}, {"and", parser.TagAnd}, {"==", parser.TagEqual},
	{"~=", parser.TagNotEqual}, {"<", parser.TagLess}, {"<=", parser.TagLessEqual},
	{">", parser.TagGreater}, {">=", parser.TagGreaterEqual}, {"|", parser.TagBitOr},
	{"~", parser.TagBitXor}, {"&", parser.TagBitAnd}, {"<<", parser.TagShiftLeft},
	{">>", parser.TagShiftRight}, {"..", parser.TagConcat}, {"+", parser.TagAdd},
	{"-", parser.TagSubtract}, {"*", parser.TagMultiply}, {"%", parser.TagModulo},
	{"/", parser.TagDivide}, {"//", parser.TagIntegerDivide}, {"^", parser.TagPower},
}

func TestASTSuiteOperators(t *testing.T) {
	for _, op := range astSuiteBinaryOperators {
		t.Run(op.text, func(t *testing.T) {
			a, index := astSuiteExpression(t, "a "+op.text+" b "+op.text+" c")
			name := astSuiteTagNames[op.kind]
			want := name + "(" + name + "(NameReference(\"a\"),NameReference(\"b\")),NameReference(\"c\"))"
			if op.kind == parser.TagConcat || op.kind == parser.TagPower {
				want = name + "(NameReference(\"a\")," + name + "(NameReference(\"b\"),NameReference(\"c\")))"
			}
			astSuiteWantShape(t, a, index, want)
		})
	}
	for _, op := range []struct {
		text string
		kind parser.Tag
	}{{"not", parser.TagNot}, {"-", parser.TagNegate}, {"~", parser.TagBitwiseNot}, {"#", parser.TagLength}} {
		t.Run("unary/"+op.text, func(t *testing.T) {
			a, index := astSuiteExpression(t, op.text+" a ^ b * c")
			astSuiteWantShape(t, a, index, "Multiply("+astSuiteTagNames[op.kind]+"(Power(NameReference(\"a\"),NameReference(\"b\")),_),NameReference(\"c\"))")
		})
	}
}

func TestASTSuitePrecedence(t *testing.T) {
	// Each adjacent precedence tier is tested in both token orders.
	tiers := []string{"or", "and", "<", "|", "~", "&", "<<", "..", "+", "*", "^"}
	kinds := []parser.Tag{parser.TagOr, parser.TagAnd, parser.TagLess, parser.TagBitOr, parser.TagBitXor, parser.TagBitAnd, parser.TagShiftLeft, parser.TagConcat, parser.TagAdd, parser.TagMultiply, parser.TagPower}
	for i := 0; i+1 < len(tiers); i++ {
		for _, reverse := range []bool{false, true} {
			lo, hi := tiers[i], tiers[i+1]
			source := "a " + lo + " b " + hi + " c"
			if reverse {
				source = "a " + hi + " b " + lo + " c"
			}
			t.Run(source, func(t *testing.T) {
				a, index := astSuiteExpression(t, source)
				low, high := astSuiteTagNames[kinds[i]], astSuiteTagNames[kinds[i+1]]
				want := low + "(NameReference(\"a\")," + high + "(NameReference(\"b\"),NameReference(\"c\")))"
				if reverse {
					want = low + "(" + high + "(NameReference(\"a\"),NameReference(\"b\")),NameReference(\"c\"))"
				}
				astSuiteWantShape(t, a, index, want)
			})
		}
	}
	for _, tc := range []struct{ source, want string }{
		{"(a + b) * c", `Multiply(Parenthesized(Add(NameReference("a"),NameReference("b")),_),NameReference("c"))`},
		{"a ^ -b", `Power(NameReference("a"),Negate(NameReference("b"),_))`},
		{"not - ~ #a", `Not(Negate(BitwiseNot(Length(NameReference("a"),_),_),_),_)`},
		{"a / b * c", `Multiply(Divide(NameReference("a"),NameReference("b")),NameReference("c"))`},
		{"a >> b << c", `ShiftLeft(ShiftRight(NameReference("a"),NameReference("b")),NameReference("c"))`},
		{"a == b ~= c", `NotEqual(Equal(NameReference("a"),NameReference("b")),NameReference("c"))`},
	} {
		t.Run(tc.source, func(t *testing.T) {
			a, index := astSuiteExpression(t, tc.source)
			astSuiteWantShape(t, a, index, tc.want)
		})
	}
}

func TestASTSuiteStatementTrees(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{";", "Empty"},
		{"return;", "Return"},
		{"do end", "DoBlock(Block,_)"},
		{"local a,b", `LocalDeclaration(List(Binding(Identifier("a"),_),List(Binding(Identifier("b"),_),_)),_)`},
		{"global x=1", `GlobalDeclaration(List(Binding(Identifier("x"),_),_),List(Integer("1"),_))`},
		{"global *", "GlobalWildcardDeclaration"},
		{"global<const> *", `GlobalWildcardDeclaration(Attribute("const"),_)`},
		{"local x<close> = nil", `LocalDeclaration(List(Binding(Identifier("x"),Attribute("close")),_),List(Nil("nil"),_))`},
		{"a,t.x,t[i]=1,2,f()", `Assignment(List(NameTarget("a"),List(FieldTarget(NameReference("t"),Identifier("x")),List(IndexTarget(NameReference("t"),NameReference("i")),_))),List(Integer("1"),List(Integer("2"),List(CallExpression(NameReference("f"),_),_))))`},
		{"f()", `CallStatement(CallExpression(NameReference("f"),_),_)`},
		{"while a do break end", `While(NameReference("a"),Block(List(Break,_),_))`},
		{"repeat local x=true until x", `RepeatUntil(Block(List(LocalDeclaration(List(Binding(Identifier("x"),_),_),List(Boolean("true"),_)),_),_),NameReference("x"))`},
		{"for i=1,9 do end", `NumericFor(Pair(Identifier("i"),List(Integer("1"),List(Integer("9"),List))),Block)`},
		{"for i=9,1,-2 do break end", `NumericFor(Pair(Identifier("i"),List(Integer("9"),List(Integer("1"),List(Negate(Integer("2"),_),_)))),Block(List(Break,_),_))`},
		{"for k,v in iter(),state,0 do end", `GenericFor(Pair(List(Identifier("k"),List(Identifier("v"),_)),List(CallExpression(NameReference("iter"),_),List(NameReference("state"),List(Integer("0"),_)))),Block)`},
		{"if a then elseif b then else end", `If(List(Branch(NameReference("a"),Block),List(Branch(NameReference("b"),Block),_)),Block)`},
		{"if a then end", `If(List(Branch(NameReference("a"),Block),_),_)`},
		{"local function f() end", `LocalFunctionDeclaration(Identifier("f"),FunctionExpression(_,Block))`},
		{"global function f() end", `GlobalFunctionDeclaration(Identifier("f"),FunctionExpression(_,Block))`},
		{"function f() end", `FunctionDeclaration(NamePath(Identifier("f"),Pair),FunctionExpression(_,Block))`},
	} {
		t.Run(tc.source, func(t *testing.T) {
			a := astSuiteParse(t, tc.source)
			items := astSuiteStatements(t, a)
			if len(items) != 1 {
				t.Fatalf("got %d statements, want 1", len(items))
			}
			astSuiteWantShape(t, a, items[0], tc.want)
		})
	}
	a := astSuiteParse(t, "::again:: goto again")
	items := astSuiteStatements(t, a)
	if len(items) != 2 {
		t.Fatalf("got %d statements", len(items))
	}
	astSuiteWantShape(t, a, items[0], `Label(Identifier("again"),_)`)
	astSuiteWantShape(t, a, items[1], `Goto(Identifier("again"),_)`)
}

func TestASTSuiteCallsTablesAndMultipleReturns(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"{}", "TableConstructor"},
		{`{x=1; [k]=2, a, f(),}`, `TableConstructor(List(NameField(Identifier("x"),Integer("1")),List(KeyField(NameReference("k"),Integer("2")),List(ArrayField(NameReference("a"),_),List(ArrayField(CallExpression(NameReference("f"),_),_),_)))),_)`},
		{`{x + 1}`, `TableConstructor(List(ArrayField(Add(NameReference("x"),Integer("1")),_),_),_)`},
		{`f{}`, `TableArgumentCall(NameReference("f"),List(TableConstructor,_))`},
		{`f"x"`, `StringArgumentCall(NameReference("f"),List(String("x"),_))`},
		{`t:m(1,...)`, `MethodCallExpression(Pair(NameReference("t"),Identifier("m")),List(Integer("1"),List(Vararg("..."),_)))`},
		{`t:m{}`, `MethodCallExpression(Pair(NameReference("t"),Identifier("m")),List(TableConstructor,_))`},
		{`t:m"x"`, `MethodCallExpression(Pair(NameReference("t"),Identifier("m")),List(String("x"),_))`},
		{`(f)().x[k](2)`, `CallExpression(IndexAccess(FieldAccess(CallExpression(Parenthesized(NameReference("f"),_),_),Identifier("x")),NameReference("k")),List(Integer("2"),_))`},
		{`f(g(),(h()),...)`, `CallExpression(NameReference("f"),List(CallExpression(NameReference("g"),_),List(Parenthesized(CallExpression(NameReference("h"),_),_),List(Vararg("..."),_))))`},
		{`function() end`, `FunctionExpression(_,Block)`},
	} {
		t.Run(tc.source, func(t *testing.T) {
			a, index := astSuiteExpression(t, tc.source)
			astSuiteWantShape(t, a, index, tc.want)
		})
	}
	for _, source := range []string{
		"return f(),(g()),...,(...)",
		"local a,b,c,d = f(),(g()),...,(...)",
		"a,b,c,d = f(),(g()),...,(...)",
	} {
		t.Run(source, func(t *testing.T) {
			a := astSuiteParse(t, source)
			n := a.Nodes[astSuiteStatements(t, a)[0]]
			list := n.Right
			if n.Kind == parser.TagReturn {
				list = n.Left
			}
			astSuiteWantShape(t, a, list, `List(CallExpression(NameReference("f"),_),List(Parenthesized(CallExpression(NameReference("g"),_),_),List(Vararg("..."),List(Parenthesized(Vararg("..."),_),_))))`)
		})
	}
}

func TestASTSuiteFunctionsAndContextRestoration(t *testing.T) {
	a := astSuiteParse(t, "function a.b.c:m(x,... args) return self,x,args,... end")
	n := a.Nodes[astSuiteStatements(t, a)[0]]
	if n.Kind != parser.TagMethodDeclaration {
		t.Fatalf("got declaration tag %d", n.Kind)
	}
	astSuiteWantShape(t, a, n.Left, `NamePath(Identifier("a"),Pair(List(Identifier("b"),List(Identifier("c"),_)),Identifier("m")))`)
	fn := a.Nodes[n.Right]
	if fn.Kind != parser.TagFunctionExpression {
		t.Fatalf("got function tag %d", fn.Kind)
	}
	astSuiteWantShape(t, a, fn.Left, `List(ImplicitSelf(Identifier("self"),_),List(Parameter(Identifier("x"),_),List(VarargParameter(Identifier("args"),_),_)))`)
	astSuiteWantShape(t, a, fn.Right, `Block(List(Return(List(NameReference("self"),List(NameReference("x"),List(NameReference("args"),List(Vararg("..."),_)))),_),_),_)`)
	params := astSuiteList(t, a, fn.Left)
	self := a.Nodes[params[0]]
	if self.TokenPosition != [2]uint32{1, 1} || a.Nodes[self.Left].TokenPosition != self.TokenPosition {
		t.Errorf("synthetic self position: %+v", self)
	}

	a = astSuiteParse(t, "function a.b(x,...) return ... end")
	n = a.Nodes[astSuiteStatements(t, a)[0]]
	if n.Kind != parser.TagFunctionDeclaration {
		t.Fatalf("got declaration tag %d", n.Kind)
	}
	astSuiteWantShape(t, a, n.Left, `NamePath(Identifier("a"),Pair(List(Identifier("b"),_),_))`)
	astSuiteWantShape(t, a, a.Nodes[n.Right].Left, `List(Parameter(Identifier("x"),_),List(VarargParameter,_))`)

	a = astSuiteParse(t, "while true do local function f() while true do break end end break end return ...")
	items := astSuiteStatements(t, a)
	if len(items) != 2 || a.Nodes[items[0]].Kind != parser.TagWhile || a.Nodes[items[1]].Kind != parser.TagReturn {
		t.Fatalf("loop/vararg context not restored: %v", items)
	}
	body := astSuiteList(t, a, a.Nodes[a.Nodes[items[0]].Right].Left)
	if len(body) != 2 || a.Nodes[body[1]].Kind != parser.TagBreak {
		t.Fatal("outer break missing after nested function")
	}
	astSuiteWantShape(t, a, items[1], `Return(List(Vararg("..."),_),_)`)

	a, index := astSuiteExpression(t, "function(...) local function inner() end return ... end")
	body = astSuiteList(t, a, a.Nodes[a.Nodes[index].Right].Left)
	if len(body) != 2 {
		t.Fatalf("function body length %d", len(body))
	}
	astSuiteWantShape(t, a, body[1], `Return(List(Vararg("..."),_),_)`)
}

func TestASTSuiteDeclarationAttributes(t *testing.T) {
	for _, prefix := range []string{"local", "global"} {
		t.Run(prefix, func(t *testing.T) {
			a := astSuiteParse(t, prefix+"<const> a,b,c<const> = 1,2,3")
			n := a.Nodes[astSuiteStatements(t, a)[0]]
			bindings := astSuiteList(t, a, n.Left)
			if len(bindings) != 3 || len(astSuiteList(t, a, n.Right)) != 3 {
				t.Fatal("declaration lists lost entries")
			}
			first, second, third := a.Nodes[bindings[0]], a.Nodes[bindings[1]], a.Nodes[bindings[2]]
			if first.Right == 0 || first.Right != second.Right || first.Right == third.Right {
				t.Fatal("inherited attributes must share a node; explicit attribute must have its own node")
			}
			for i, b := range bindings {
				astSuiteWantShape(t, a, b, fmt.Sprintf("Binding(Identifier(%q),Attribute(\"const\"))", string(rune('a'+i))))
			}
		})
	}
	// Overriding every inherited attribute must not leave dead arena entries.
	t.Run("fully overridden prefix", func(t *testing.T) {
		a := astSuiteParse(t, "local<const> resource<close> = nil")
		astSuiteWantShape(t, a, astSuiteStatements(t, a)[0], `LocalDeclaration(List(Binding(Identifier("resource"),Attribute("close")),_),List(Nil("nil"),_))`)
	})
}

func TestASTSuitePositionsAndValues(t *testing.T) {
	source := "-- header\r\nlocal x = 0X2a\nreturn x + 1.50e+2, \"a\\n\\000\\xFF\", [=[\nlong\r\ntext]=], nil, true, false, ..."
	a := astSuiteParse(t, source)
	items := astSuiteStatements(t, a)
	if len(items) != 2 {
		t.Fatalf("got %d statements", len(items))
	}
	for _, tc := range []struct {
		kind  parser.Tag
		value string
		pos   [2]uint32
	}{
		{parser.TagIdentifier, "x", [2]uint32{2, 7}},
		{parser.TagInteger, "0X2a", [2]uint32{2, 11}},
		{parser.TagNameReference, "x", [2]uint32{3, 8}},
		{parser.TagFloat, "1.50e+2", [2]uint32{3, 12}},
		{parser.TagString, "a\n\x00\xff", [2]uint32{3, 21}},
		{parser.TagString, "long\ntext", [2]uint32{3, 36}},
		{parser.TagNil, "nil", [2]uint32{5, 10}},
		{parser.TagBoolean, "true", [2]uint32{5, 15}},
		{parser.TagBoolean, "false", [2]uint32{5, 21}},
		{parser.TagVararg, "...", [2]uint32{5, 28}},
		{parser.TagAdd, "", [2]uint32{3, 10}},
		{parser.TagLocalDeclaration, "", [2]uint32{2, 1}},
		{parser.TagReturn, "", [2]uint32{3, 1}},
	} {
		matches := 0
		for i, n := range a.Nodes {
			if n.Kind == tc.kind && a.Values[i] == tc.value {
				matches++
				if n.TokenPosition != tc.pos {
					t.Errorf("%s %q position=%v, want %v", astSuiteTagNames[tc.kind], tc.value, n.TokenPosition, tc.pos)
				}
			}
		}
		if matches != 1 {
			t.Errorf("%s %q: got %d matching nodes, want 1", astSuiteTagNames[tc.kind], tc.value, matches)
		}
	}
	if a.Nodes[a.Root].TokenPosition != [2]uint32{2, 1} || a.Nodes[a.Nodes[a.Root].Left].TokenPosition != [2]uint32{2, 1} {
		t.Error("chunk/block position must start at first token after comments")
	}
	for _, literal := range []struct{ source, want string }{
		{`0xffffffffffffffff`, `Integer("0xffffffffffffffff")`},
		{`9223372036854775808`, `Float("9223372036854775808")`},
		{`0x1.8p+1`, `Float("0x1.8p+1")`},
		{`.5`, `Float(".5")`},
		{`''`, `String("")`},
		{`"\u{E9}\z  x"`, `String("éx")`},
	} {
		t.Run(literal.source, func(t *testing.T) {
			a, index := astSuiteExpression(t, literal.source)
			astSuiteWantShape(t, a, index, literal.want)
		})
	}
}

func TestASTSuiteErrors(t *testing.T) {
	for _, tc := range []struct{ name, source, message, category string }{
		{"missing name", "local = 1", "<name> expected", "expected"},
		{"missing initializer", "local a =", "unexpected symbol", "syntax"},
		{"missing end", "do", "'end' expected", "expected"},
		{"multiline close", "do\n", "to close 'do' at line 1", "syntax"},
		{"missing then", "if true end", "'then' expected", "expected"},
		{"missing do", "while true end", "'do' expected", "expected"},
		{"missing until", "repeat", "'until' expected", "expected"},
		{"missing in", "for k iter() do end", "'in' expected", "expected"},
		{"missing bound", "for i=1 do end", "',' expected", "expected"},
		{"bare name", "x", "syntax error", "syntax"},
		{"call target", "f()=1", "syntax error", "syntax"},
		{"parenthesized target", "(x)=1", "syntax error", "syntax"},
		{"literal target", "1=2", "unexpected symbol", "syntax"},
		{"missing arguments", "t:m", "function arguments expected", "syntax"},
		{"missing paren", "return (1", "')' expected", "expected"},
		{"missing bracket", "return t[1", "']' expected", "expected"},
		{"missing table separator", "return {1 2}", "'}' expected", "expected"},
		{"missing table value", "return {x=}", "unexpected symbol", "syntax"},
		{"bad table key", "return {[1] 2}", "'=' expected", "expected"},
		{"return not last", "return 1; x=2", "expected near 'x'", "expected"},
		{"trailing comma", "return 1,", "unexpected symbol", "syntax"},
		{"extra end", "end", "expected near 'end'", "expected"},
		{"break outside loop", "break", "break outside loop", "syntax"},
		{"break across function", "while true do local function f() break end end", "break outside loop", "syntax"},
		{"vararg outside function", "local function f() return ... end", "outside a vararg function", "syntax"},
		{"vararg not inherited", "local function f(...) return function() return ... end end", "outside a vararg function", "syntax"},
		{"vararg last", "local function f(...,x) end", "')' expected", "expected"},
		{"named vararg last", "local function f(... args,x) end", "')' expected", "expected"},
		{"parameter trailing comma", "function f(x,) end", "<name> expected", "expected"},
		{"unknown attribute", "local a<other>", "unknown attribute 'other'", "syntax"},
		{"global close", "global a<close>", "cannot be marked 'close'", "syntax"},
		{"global prefix close", "global<close> *", "cannot be marked 'close'", "syntax"},
		{"multiple close", "local a<close>,b<close>", "multiple to-be-closed", "syntax"},
		{"shared close", "local<close> a,b", "multiple to-be-closed", "syntax"},
		{"local wildcard", "local *", "<name> expected", "expected"},
		{"unfinished string", "return 'abc", "unfinished string", "lexical"},
		{"bad escape", `return '\q'`, "invalid string escape", "lexical"},
		{"malformed number", "return 1e+", "malformed number", "lexical"},
		{"unfinished long string", "return [=[abc", "unfinished long string", "lexical"},
		{"unfinished comment", "--[[abc", "unfinished long string", "lexical"},
		{"lookahead lexical error", `return {field '\q'}`, "invalid string escape", "lexical"},
		{"lexical after valid statement", "local a=1\nreturn 'bad", "unfinished string", "lexical"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := astSuiteNew(tc.source)
			err := a.Next()
			if err == nil {
				t.Fatal("invalid source accepted")
			}
			if !strings.Contains(err.Error(), tc.message) || !strings.HasPrefix(err.Error(), "ast-suite.lua:") {
				t.Errorf("error=%v; want filename and %q", err, tc.message)
			}
			var syntax *parser.SyntaxError
			var expected *parser.ErrExpected
			switch tc.category {
			case "syntax":
				if !errors.As(err, &syntax) || syntax.Filename != "ast-suite.lua" || syntax.Line == 0 || syntax.Column == 0 || syntax.Near == "" {
					t.Errorf("invalid SyntaxError: %#v", err)
				}
			case "expected":
				if !errors.As(err, &expected) {
					t.Errorf("want ErrExpected, got %T", err)
				}
			case "lexical":
				if errors.As(err, &syntax) || errors.As(err, &expected) || errors.Unwrap(err) == nil {
					t.Errorf("lexical error was not preserved: %T %v", err, err)
				}
			}
			astSuiteInvariant(t, a, err)
			astSuiteStable(t, a, err)
		})
	}
	t.Run("exact syntax position", func(t *testing.T) {
		a := astSuiteNew("-- comment\n  break")
		err := a.Next()
		var syntax *parser.SyntaxError
		if !errors.As(err, &syntax) {
			t.Fatalf("got %T %v", err, err)
		}
		if *syntax != (parser.SyntaxError{Filename: "ast-suite.lua", Line: 2, Column: 3, Message: "break outside loop", Near: "'break'"}) {
			t.Errorf("unexpected diagnostic: %+v", syntax)
		}
		if err.Error() != "ast-suite.lua:2: break outside loop near 'break'" {
			t.Errorf("unexpected formatted diagnostic: %v", err)
		}
	})
	t.Run("exact lexical position", func(t *testing.T) {
		a := astSuiteNew("return\n  'bad")
		err := a.Next()
		if err == nil || err.Error() != "ast-suite.lua:2:3: unfinished string" {
			t.Errorf("unexpected lexical diagnostic: %v", err)
		}
	})
}

func TestASTSuiteRecursionLimit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		makeSource func(int) string
		lastOK     int
	}{
		{"parentheses", func(n int) string { return "return " + strings.Repeat("(", n) + "1" + strings.Repeat(")", n) }, 198},
		{"unary", func(n int) string { return "return " + strings.Repeat("not ", n) + "true" }, 198},
		{"right associative", func(n int) string { return "return " + strings.Repeat("a ^ ", n) + "a" }, 198},
		{"blocks", func(n int) string { return strings.Repeat("do ", n) + strings.Repeat("end ", n) }, 199},
		{"functions", func(n int) string { return strings.Repeat("local function f() ", n) + strings.Repeat("end ", n) }, 99},
		{"tables", func(n int) string { return "return " + strings.Repeat("{", n) + "1" + strings.Repeat("}", n) }, 198},
	} {
		t.Run(tc.name, func(t *testing.T) {
			astSuiteParse(t, tc.makeSource(tc.lastOK))
			a := astSuiteNew(tc.makeSource(tc.lastOK + 1))
			err := a.Next()
			var syntax *parser.SyntaxError
			if !errors.As(err, &syntax) || syntax.Message != "too many syntax levels" {
				t.Fatalf("want recursion-limit SyntaxError, got %v", err)
			}
			astSuiteInvariant(t, a, err)
			astSuiteStable(t, a, err)
		})
	}
	// Length alone must not consume the syntactic nesting budget.
	a := astSuiteParse(t, strings.Repeat(";", 1000))
	if got := len(astSuiteStatements(t, a)); got != 1000 {
		t.Errorf("flat chunk has %d statements", got)
	}
	a, index := astSuiteExpression(t, "a"+strings.Repeat(" + a", 500))
	count := 0
	for a.Nodes[index].Kind == parser.TagAdd {
		count++
		astSuiteWantShape(t, a, a.Nodes[index].Right, `NameReference("a")`)
		index = a.Nodes[index].Left
	}
	if count != 500 {
		t.Errorf("flat left-associated expression has %d additions", count)
	}
	astSuiteWantShape(t, a, index, `NameReference("a")`)
}

func TestASTSuiteAllTags(t *testing.T) {
	if len(astSuiteTagNames) != int(parser.TagPower)+1 {
		t.Fatal("tag names need updating")
	}
	seen := make([]bool, len(astSuiteTagNames))
	corpus := []string{
		`; local x<const>,y=1,2; a,t.x,t[1]=nil,true,false
function a.b:m(x,... args) return self,x,args,... end
function f() end
local function f(...) return ... end
do global function g() end end
do global x end
do global<const> * end
f(); t:m(); f{}; f"x"
if a then elseif b then else end
while a do break end
repeat until a
for i=1,2,1 do end
for k,v in iter() do end
::again:: goto again
return 1.5, (a), t.x, t[1], {a,[b]=c,x=1}, function() end,
not a, -a, ~a, #a`,
	}
	for _, op := range astSuiteBinaryOperators {
		corpus = append(corpus, "return a "+op.text+" b")
	}
	for _, source := range corpus {
		a := astSuiteParse(t, source)
		for _, n := range a.Nodes {
			seen[n.Kind] = true
		}
	}
	for kind, found := range seen {
		if !found {
			t.Errorf("tag %s has no grammar fixture", astSuiteTagNames[kind])
		}
	}
}

func FuzzASTSuiteParseInvariants(f *testing.F) {
	for _, source := range []string{
		"", "return 1 + 2 * 3", "return (f()),...", "local<const> a,b=1,2",
		"function t:m(x,... args) return self,args,... end",
		"return {x=1,[k]=2,f(),}", "for i=1,9 do break end",
		"for k,v in iter() do f(v) end", "repeat local x=true until x",
		"goto L; ::L::", "local x<const>; x=2", "return '\\q'",
		"return {field '\\q'}", "local =", "\x00\xff",
		"return " + strings.Repeat("(", 205) + "1" + strings.Repeat(")", 205),
	} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 4096 {
			t.Skip()
		}
		// Do not recover: any parser panic must fail and be minimized by fuzzing.
		a := astSuiteNew(source)
		err := a.Next()
		astSuiteInvariant(t, a, err)
		astSuiteStable(t, a, err)
		other := astSuiteNew(source)
		otherErr := other.Next()
		if (err == nil) != (otherErr == nil) || fmt.Sprint(err) != fmt.Sprint(otherErr) {
			t.Fatalf("non-deterministic errors: %v / %v", err, otherErr)
		}
		if a.Root != other.Root || !reflect.DeepEqual(a.Nodes, other.Nodes) || !reflect.DeepEqual(a.Values, other.Values) {
			t.Fatal("non-deterministic arena")
		}
	})
}
