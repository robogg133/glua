package compile

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/robogg133/glua/ir"
	"github.com/robogg133/glua/lex"
	"github.com/robogg133/glua/parser"
	"github.com/robogg133/glua/tokens"
)

func parseCompile(t *testing.T, source string) *parser.AST {
	t.Helper()
	a := parser.NewAst(tokens.NewTokenizer(lex.NewLexer(source), "compile.lua"))
	if err := a.Next(); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}
	if _, err := ir.Audit(a); err != nil {
		t.Fatalf("fixture audit: %v", err)
	}
	return a
}

func compileChecked(t *testing.T, a *parser.AST) *Prototype {
	t.Helper()
	nodes := append([]parser.Node(nil), a.Nodes...)
	values := append([]string(nil), a.Values...)
	root := a.Root
	p, err := Compile(a)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !reflect.DeepEqual(nodes, a.Nodes) || !reflect.DeepEqual(values, a.Values) || root != a.Root {
		t.Fatal("Compile modified its input AST")
	}
	checkPrototype(t, p, nil)
	return p
}

// Check format-independent invariants and VM-required companion words, not an
// exact instruction listing. This is a test assertion, not a bytecode verifier.
func checkPrototype(t *testing.T, p, parent *Prototype) {
	t.Helper()
	if p == nil {
		t.Fatal("nil prototype without error")
	}
	if len(p.Code) == 0 || p.MaxStackSize < 2 || p.NumParams > p.MaxStackSize || (p.VarargTable && !p.IsVararg) {
		t.Fatalf("invalid prototype metadata: params=%d stack=%d vararg=%v table=%v code=%d", p.NumParams, p.MaxStackSize, p.IsVararg, p.VarargTable, len(p.Code))
	}
	if len(p.Lines) != 0 && len(p.Lines) != len(p.Code) {
		t.Fatalf("%d lines for %d instructions", len(p.Lines), len(p.Code))
	}
	if len(p.Upvalues) > 255 {
		t.Fatal("upvalue count cannot fit closure header")
	}
	if parent == nil {
		if p.NumParams != 0 || !p.IsVararg || len(p.Upvalues) != 1 || p.Upvalues[0].Name != "_ENV" {
			t.Fatal("chunk must be vararg with one _ENV upvalue and no fixed parameters")
		}
	}
	for _, u := range p.Upvalues {
		if u.Kind > UpvalueGlobalConst {
			t.Fatalf("invalid upvalue kind: %+v", u)
		}
		if parent != nil {
			limit := len(parent.Upvalues)
			if u.InStack {
				limit = int(parent.MaxStackSize)
			}
			if int(u.Index) >= limit {
				t.Fatalf("capture outside parent: %+v (limit %d)", u, limit)
			}
		}
	}
	for _, c := range p.Constants {
		if c.Kind > ConstantString || (c.Kind == ConstantBoolean && c.Bits > 1) ||
			(c.Kind == ConstantNil && c.Bits != 0) || (c.Kind == ConstantString && c.Bits != 0) ||
			(c.Kind != ConstantString && c.String != "") {
			t.Fatalf("invalid constant: %+v", c)
		}
	}
	aux := make(map[int]bool)
	for pc, i := range p.Code {
		if i.Op() >= NUM_OPCODES {
			t.Fatalf("pc %d: unknown opcode %d", pc, i.Op())
		}
		next := func(op OpCode) {
			t.Helper()
			if pc+1 >= len(p.Code) || p.Code[pc+1].Op() != op {
				t.Fatalf("pc %d: opcode %d requires following opcode %d", pc, i.Op(), op)
			}
		}
		switch op := i.Op(); {
		case op == OpNEWTABLE || op == OpLOADKX || (op == OpSETLIST && i.K() != 0):
			next(OpEXTRAARG)
			aux[pc+1] = true
		case op >= OpADD && op <= OpSHR:
			next(OpMMBIN)
		case op >= OpADDK && op <= OpBXORK:
			next(OpMMBINK)
		case op == OpADDI || op == OpSHLI || op == OpSHRI:
			next(OpMMBINI)
		case op >= OpEQ && op <= OpTESTSET:
			next(OpJMP)
		}
		switch i.Op() {
		case OpVARARGPREP:
			if !p.IsVararg || pc != 0 || uint32(i)>>7 != 0 {
				t.Fatalf("pc %d: invalid operand-free VARARGPREP", pc)
			}
		case OpGETVARG:
			if !p.IsVararg || p.VarargTable {
				t.Fatalf("pc %d: GETVARG requires hidden arguments", pc)
			}
		case OpRETURN, OpTAILCALL:
			want := 0
			if p.IsVararg && !p.VarargTable {
				want = int(p.NumParams) + 1
			}
			if int(i.C()) != want {
				t.Fatalf("pc %d: return/tailcall C=%d, want %d", pc, i.C(), want)
			}
		case OpRETURN0, OpRETURN1:
			if p.IsVararg && !p.VarargTable {
				t.Fatalf("pc %d: short return cannot restore hidden-vararg frame", pc)
			}
		case OpGETUPVAL, OpSETUPVAL, OpGETTABUP:
			if int(i.B()) >= len(p.Upvalues) {
				t.Fatalf("pc %d: upvalue index out of range", pc)
			}
		case OpSETTABUP:
			if int(i.A()) >= len(p.Upvalues) {
				t.Fatalf("pc %d: upvalue index out of range", pc)
			}
		case OpTFORCALL:
			next(OpTFORLOOP)
			if p.Code[pc+1].A() != i.A() || int(i.A())+6 > int(p.MaxStackSize) {
				t.Fatalf("pc %d: invalid iterator frame", pc)
			}
		}
		if i.Op() == OpLOADK && int(i.Bx()) >= len(p.Constants) {
			t.Fatalf("pc %d: LOADK constant out of range", pc)
		}
		if i.Op() == OpLOADKX && int(p.Code[pc+1].Ax()) >= len(p.Constants) {
			t.Fatalf("pc %d: LOADKX constant out of range", pc)
		}
		if i.Op() == OpCLOSURE && int(i.Bx()) >= len(p.Children) {
			t.Fatalf("pc %d: child index out of range", pc)
		}
		if i.Op() == OpERRNNIL && i.Bx() != 0 {
			k := int(i.Bx()) - 1
			if k >= len(p.Constants) || p.Constants[k].Kind != ConstantString {
				t.Fatalf("pc %d: ERRNNIL name must be a string constant (index plus one)", pc)
			}
		}
	}
	for pc, i := range p.Code {
		if i.Op() == OpEXTRAARG && !aux[pc] {
			t.Fatalf("pc %d: orphan EXTRAARG", pc)
		}
		target := -1
		switch i.Op() {
		case OpJMP:
			target = pc + 1 + int(i.SJ())
		case OpFORPREP:
			target = pc + 2 + int(i.Bx())
		case OpTFORPREP:
			target = pc + 1 + int(i.Bx())
		case OpFORLOOP, OpTFORLOOP:
			target = pc + 1 - int(i.Bx())
		default:
			continue
		}
		if target < 0 || target >= len(p.Code) || aux[target] {
			t.Fatalf("pc %d: invalid jump target %d", pc, target)
		}
	}
	for n := range p.Children {
		checkPrototype(t, &p.Children[n], p)
	}
}

func countOp(p *Prototype, op OpCode) int {
	n := 0
	for _, i := range p.Code {
		if i.Op() == op {
			n++
		}
	}
	for j := range p.Children {
		n += countOp(&p.Children[j], op)
	}
	return n
}

func TestCompileFeatures(t *testing.T) {
	// Includes runtime-error programs: they must compile successfully. Running
	// this structural suite does not require Lua sources or a C compiler.
	for _, tc := range runtimeCases {
		t.Run(tc.name, func(t *testing.T) {
			a := parseCompile(t, tc.source)
			compileChecked(t, a)
			optimized, _, err := ir.Optimize(a)
			if err != nil {
				t.Fatal(err)
			}
			compileChecked(t, optimized)
		})
	}
}

func TestCompileOwnershipAndDeterminism(t *testing.T) {
	a := parseCompile(t, `local x=...; return function(y) x=x+y; return x,"owned" end`)
	first := compileChecked(t, a)
	second := compileChecked(t, a)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("compilation is not deterministic")
	}
	first.Code[0] = 0
	first.Upvalues[0].Name = "changed"
	first.Children[0].Code[0] = 0
	first.Children[0].Upvalues[0].Name = "changed"
	third := compileChecked(t, a)
	if !reflect.DeepEqual(second, third) {
		t.Fatal("Compile results share mutable storage")
	}
	for i := range a.Nodes {
		a.Nodes[i] = parser.Node{}
		a.Values[i] = "changed"
	}
	if !reflect.DeepEqual(second, third) {
		t.Fatal("prototype retained mutable AST storage")
	}
}

func TestCompileMalformedAST(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*parser.AST)
	}{
		{"zero_root", func(a *parser.AST) { a.Root = 0 }},
		{"root_out_of_range", func(a *parser.AST) { a.Root = uint32(len(a.Nodes)) }},
		{"missing_values", func(a *parser.AST) { a.Values = a.Values[:1] }},
		{"bad_sentinel", func(a *parser.AST) { a.Nodes[0].Kind = parser.TagNil }},
		{"wrong_root_kind", func(a *parser.AST) { a.Nodes[a.Root].Kind = parser.TagBlock }},
		{"missing_body", func(a *parser.AST) { a.Nodes[a.Root].Left = 0 }},
		{"dangling_child", func(a *parser.AST) { a.Nodes[a.Root].Left = ^uint32(0) }},
		{"cycle", func(a *parser.AST) { a.Nodes[a.Root].Left = a.Root }},
		{"list_cycle", func(a *parser.AST) {
			for i := range a.Nodes {
				if a.Nodes[i].Kind == parser.TagList {
					a.Nodes[i].Right = uint32(i)
					return
				}
			}
		}},
		{"unknown_tag", func(a *parser.AST) { a.Nodes[a.Root].Kind = parser.Tag(255) }},
		{"expression_as_statement", func(a *parser.AST) {
			for i := range a.Nodes {
				if a.Nodes[i].Kind == parser.TagReturn {
					a.Nodes[i].Kind = parser.TagInteger
					return
				}
			}
		}},
		{"unreachable_bad_node", func(a *parser.AST) {
			a.Nodes = append(a.Nodes, parser.Node{Kind: parser.Tag(255)})
			a.Values = append(a.Values, "")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := parseCompile(t, `return 1`)
			tc.mutate(a)
			if _, err := ir.Audit(a); err == nil {
				t.Fatal("malformed fixture unexpectedly passes audit")
			}
			if p, err := Compile(a); err == nil || p != nil {
				t.Fatalf("want nil prototype and error, got %v, %v", p, err)
			}
		})
	}
	for name, a := range map[string]*parser.AST{"nil": nil, "empty": {}, "unparsed": parser.NewAst(tokens.NewTokenizer(lex.NewLexer(""), "test"))} {
		t.Run(name, func(t *testing.T) {
			if p, err := Compile(a); err == nil || p != nil {
				t.Fatalf("got %v, %v", p, err)
			}
		})
	}
	t.Run("failed_parse", func(t *testing.T) {
		a := parser.NewAst(tokens.NewTokenizer(lex.NewLexer("local ="), "test"))
		if a.Next() == nil {
			t.Fatal("expected parser error")
		}
		if p, err := Compile(a); err == nil || p != nil {
			t.Fatalf("got %v, %v", p, err)
		}
	})
}

func TestCompileMalformedNumerals(t *testing.T) {
	for _, spelling := range []string{"", "0x", "1_000", "NaN", "Inf", "1e+", "0xg", "--1"} {
		t.Run(spelling, func(t *testing.T) {
			a := parseCompile(t, `return 1`)
			for i, n := range a.Nodes {
				if n.Kind == parser.TagInteger {
					a.Values[i] = spelling
				}
			}
			if p, err := Compile(a); err == nil || p != nil {
				t.Fatalf("malformed numeral accepted: %v, %v", p, err)
			}
		})
	}
}

func TestCompileGlobalChecks(t *testing.T) {
	for _, tc := range []struct {
		source string
		checks int
	}{
		{`global a`, 0}, {`global *`, 0}, {`global <const> *`, 0},
		{`global a=1`, 1}, {`global a<const> = 1`, 1},
		{`global a,b=1`, 2}, {`global a=nil`, 1}, {`global function f() end`, 1},
	} {
		t.Run(tc.source, func(t *testing.T) {
			p := compileChecked(t, parseCompile(t, tc.source))
			if got := countOp(p, OpERRNNIL); got != tc.checks {
				t.Fatalf("ERRNNIL count %d, want %d", got, tc.checks)
			}
		})
	}
}

func TestCompileVarargModes(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		table      bool
	}{
		{"hidden", `return args.n,args[1],...`, false},
		{"mutated", `args[1]=9; return ...`, true},
		{"escaped", `local t=args; return t,...`, true},
		{"captured", `return function() return args[1] end`, true},
		{"late_materialization", `local x=args[1]; local t=args; return x,t,...`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := compileChecked(t, parseCompile(t, `return function(x,... args) `+tc.body+` end`))
			if len(p.Children) != 1 {
				t.Fatal("missing function prototype")
			}
			f := &p.Children[0]
			if f.NumParams != 1 || !f.IsVararg || f.VarargTable != tc.table {
				t.Fatalf("incorrect vararg metadata: %+v", f)
			}
			if f.Code[0].Op() != OpVARARGPREP {
				t.Fatal("vararg function must start with VARARGPREP")
			}
			if !tc.table && countOp(f, OpGETVARG) == 0 {
				t.Fatal("read-only named varargs should use GETVARG")
			}
			for _, i := range f.Code {
				if i.Op() == OpVARARG && ((i.K() != 0) != tc.table || i.B() != f.NumParams) {
					t.Fatal("incorrect VARARG table mode/slot")
				}
			}
		})
	}
}

func TestCompileExplicitOptimization(t *testing.T) {
	a := parseCompile(t, `local x=2+3; if false then print("dead") end; return x*4`)
	raw := compileChecked(t, a)
	optimized, report, err := ir.Optimize(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Changes) == 0 {
		t.Fatal("fixture did not exercise IR rewrites")
	}
	folded := compileChecked(t, optimized)
	arithmetic := func(p *Prototype) int { return countOp(p, OpMMBIN) + countOp(p, OpMMBINI) + countOp(p, OpMMBINK) }
	if arithmetic(raw) == 0 {
		t.Fatal("Compile unexpectedly folded the input without explicit ir.Optimize")
	}
	if arithmetic(folded) != 0 || len(folded.Code) >= len(raw.Code) {
		t.Fatal("optimized AST did not eliminate constant arithmetic/dead branch")
	}
}

func TestCompileEncodingSelections(t *testing.T) {
	for _, tc := range []struct {
		source string
		op     OpCode
	}{
		{`local x=...; return x+1`, OpADDI},
		{`local x=...; return x+300`, OpADDK},
		{`local t=...; return t.field`, OpGETFIELD},
		{`local t=...; return t:method()`, OpSELF},
		{`local f=...; return f()`, OpTAILCALL},
	} {
		t.Run(tc.source, func(t *testing.T) {
			p := compileChecked(t, parseCompile(t, tc.source))
			if countOp(p, tc.op) == 0 {
				t.Fatalf("expected opcode %d", tc.op)
			}
		})
	}
	t.Run("constant_deduplication", func(t *testing.T) {
		p := compileChecked(t, parseCompile(t, `return "repeated","repeated","repeated"`))
		n := 0
		for _, c := range p.Constants {
			if c == StringConstant("repeated") {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("duplicate string stored %d times", n)
		}
	})
}

func TestConstantRepresentations(t *testing.T) {
	for _, n := range []int64{math.MinInt64, -1, 0, math.MaxInt64} {
		c := IntConstant(n)
		if c.Kind != ConstantInteger || int64(c.Bits) != n || c.String != "" {
			t.Fatalf("integer %d: %+v", n, c)
		}
	}
	for _, bits := range []uint64{0, 1 << 63, 0x7ff0000000000000, 0xfff0000000000000, 0x7ff8000000000042} {
		c := FloatConstant(math.Float64frombits(bits))
		if c.Kind != ConstantFloat || c.Bits != bits || c.String != "" {
			t.Fatalf("float bits %x: %+v", bits, c)
		}
	}
	if StringConstant("a\x00b").String != "a\x00b" || BoolConstant(false).Bits != 0 || BoolConstant(true).Bits != 1 || NilConstant() != (Constant{}) {
		t.Fatal("constant representation mismatch")
	}
	if IntConstant(1) == FloatConstant(1) || FloatConstant(0) == FloatConstant(math.Copysign(0, -1)) {
		t.Fatal("type or signed-zero distinction lost")
	}
}

func TestEncodingBoundaries(t *testing.T) {
	if i := ABC(OpMOVE, MaxArgA, MaxArgB, MaxArgC, true); int(i.A()) != MaxArgA || int(i.B()) != MaxArgB || int(i.C()) != MaxArgC || i.K() != 1 {
		t.Fatal("ABC boundary did not round-trip")
	}
	if i := VABC(OpSETLIST, MaxArgA, MaxArgVB, MaxArgVC, true); int(i.VB()) != MaxArgVB || int(i.VC()) != MaxArgVC || i.K() != 1 {
		t.Fatal("ivABC boundary did not round-trip")
	}
	if i := ABx(OpLOADK, MaxArgA, MaxArgBx); int(i.Bx()) != MaxArgBx {
		t.Fatal("ABx boundary did not round-trip")
	}
	if i := Ax(OpEXTRAARG, MaxArgAx); int(i.Ax()) != MaxArgAx {
		t.Fatal("Ax boundary did not round-trip")
	}
	for _, n := range []int{MinArgSBx, -1, 0, MaxArgSBx} {
		if int(AsBx(OpLOADI, 0, n).SBx()) != n {
			t.Fatalf("sBx %d did not round-trip", n)
		}
	}
	for _, n := range []int{MinArgSJ, -1, 0, MaxArgSJ} {
		if int(SJ(OpJMP, n).SJ()) != n {
			t.Fatalf("sJ %d did not round-trip", n)
		}
	}
	for name, encode := range map[string]func(){
		"opcode":     func() { ABC(NUM_OPCODES, 0, 0, 0, false) },
		"negative_A": func() { ABC(OpMOVE, -1, 0, 0, false) },
		"A":          func() { ABC(OpMOVE, MaxArgA+1, 0, 0, false) },
		"B":          func() { ABC(OpMOVE, 0, MaxArgB+1, 0, false) },
		"C":          func() { ABC(OpMOVE, 0, 0, MaxArgC+1, false) },
		"vB":         func() { VABC(OpSETLIST, 0, MaxArgVB+1, 0, false) },
		"vC":         func() { VABC(OpSETLIST, 0, 0, MaxArgVC+1, false) },
		"Bx":         func() { ABx(OpLOADK, 0, MaxArgBx+1) },
		"Ax":         func() { Ax(OpEXTRAARG, MaxArgAx+1) },
		"low_sBx":    func() { AsBx(OpLOADI, 0, MinArgSBx-1) },
		"high_sBx":   func() { AsBx(OpLOADI, 0, MaxArgSBx+1) },
		"low_sJ":     func() { SJ(OpJMP, MinArgSJ-1) },
		"high_sJ":    func() { SJ(OpJMP, MaxArgSJ+1) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("out-of-range encoder argument did not panic")
				}
			}()
			encode()
		})
	}
}

func numberedNames(n int) string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("v%d", i)
	}
	return strings.Join(names, ",")
}

func FuzzCompile(f *testing.F) {
	for _, source := range []string{
		"return 1", "local function f(...) return ... end; return f(1,nil,3)",
		"local x=0; for i=1,10 do x=x+i end; return x",
		"local t={}; ::again:: do local x<close> = nil; if t.x then goto again end end",
	} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 65536 {
			t.Skip()
		}
		a := parser.NewAst(tokens.NewTokenizer(lex.NewLexer(source), "fuzz.lua"))
		if a.Next() != nil {
			return
		}
		p, err := Compile(a)
		if err != nil {
			if p != nil {
				t.Fatal("partial prototype on error")
			}
			return // Valid syntax can still exceed VM register/operand limits.
		}
		checkPrototype(t, p, nil)
	})
}

func TestCompileExtendedOperands(t *testing.T) {
	if testing.Short() {
		t.Skip("large arenas for 17-bit operand boundaries")
	}
	t.Run("LOADKX", func(t *testing.T) {
		var s strings.Builder
		s.WriteString("return {")
		for i := 0; i < MaxArgBx+2; i++ {
			fmt.Fprintf(&s, "\"constant%d\",", i)
		}
		s.WriteString("}")
		p := compileChecked(t, parseCompile(t, s.String()))
		if len(p.Constants) <= MaxArgBx+1 || countOp(p, OpLOADKX) == 0 {
			t.Fatal("17-bit constant index overflow must use LOADKX/EXTRAARG")
		}
	})
	t.Run("for_offset_overflow", func(t *testing.T) {
		// A side-effecting call cannot disappear; this exceeds even a one-word
		// encoding per statement and cannot fit FORPREP/FORLOOP Bx.
		source := "for i=1,2 do " + strings.Repeat("sink();", MaxArgBx+1) + " end"
		a := parseCompile(t, source)
		if p, err := Compile(a); err == nil || p != nil {
			t.Fatalf("oversized for offset must return an error: %v, %v", p, err)
		}
	})
}

func TestCompileLimits(t *testing.T) {
	for _, tc := range []struct{ name, source string }{
		{"locals", "local " + numberedNames(256)},
		{"parameters", "return function(" + numberedNames(256) + ") end"},
		{"arguments", "f(" + strings.Repeat("1,", 255) + "1)"},
		{"results", "return " + strings.Repeat("1,", 255) + "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := parseCompile(t, tc.source)
			if p, err := Compile(a); err == nil || p != nil {
				t.Fatalf("overflow must return error, not wrap fields: %v, %v", p, err)
			}
		})
	}
	t.Run("upvalues", func(t *testing.T) {
		// Each enclosing frame fits; the innermost function needs 260 captures.
		var s strings.Builder
		for level := 0; level < 2; level++ {
			for i := 0; i < 130; i++ {
				fmt.Fprintf(&s, "local x%d_%d=...;", level, i)
			}
			s.WriteString("return function(...) ")
		}
		for level := 0; level < 2; level++ {
			for i := 0; i < 130; i++ {
				fmt.Fprintf(&s, "sink(x%d_%d);", level, i)
			}
		}
		s.WriteString("end end")
		if p, err := Compile(parseCompile(t, s.String())); err == nil || p != nil {
			t.Fatalf("upvalue overflow: %v, %v", p, err)
		}
	})
	t.Run("large_valid_frames", func(t *testing.T) {
		for _, source := range []string{
			"local " + numberedNames(128) + "; return v127",
			"return function(" + numberedNames(128) + ") return v127 end",
			"return " + strings.Repeat("1,", 127) + "1",
			"f(" + strings.Repeat("1,", 127) + "1)",
		} {
			compileChecked(t, parseCompile(t, source))
		}
	})
	t.Run("sequential_scopes_reuse_registers", func(t *testing.T) {
		compileChecked(t, parseCompile(t, strings.Repeat("do local x=1; consume(x) end;", 1000)))
	})
	t.Run("discarded_results_reuse_registers", func(t *testing.T) {
		compileChecked(t, parseCompile(t, "local x=1,"+strings.Repeat("sideeffect(),", 300)+"sideeffect(); return x"))
	})
	t.Run("wide_constant_pool", func(t *testing.T) {
		var s strings.Builder
		s.WriteString("local t={};")
		for i := 0; i < 300; i++ {
			fmt.Fprintf(&s, "t.k%d=\"value%d\";", i, i)
		}
		s.WriteString("return t.k299,t:method()")
		p := compileChecked(t, parseCompile(t, s.String()))
		if len(p.Constants) <= MaxArgC {
			t.Fatal("fixture did not cross 8-bit constant index boundary")
		}
	})
	t.Run("large_table", func(t *testing.T) {
		p := compileChecked(t, parseCompile(t, "return {"+strings.Repeat("1,", 1200)+"}"))
		if countOp(p, OpSETLIST) < 2 {
			t.Fatal("large constructor was not batched")
		}
		found := false
		for _, i := range p.Code {
			if i.Op() == OpSETLIST && i.K() != 0 {
				found = true
			}
		}
		if !found {
			t.Fatal("constructor did not extend its 10-bit SETLIST offset")
		}
	})
}
