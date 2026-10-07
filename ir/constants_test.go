package ir

import (
	"math"
	"reflect"
	"testing"

	"github.com/robogg133/glua/lex"
	"github.com/robogg133/glua/parser"
	"github.com/robogg133/glua/tokens"
)

func TestConstantOf(t *testing.T) {
	a := parser.NewAst(tokens.NewTokenizer(lex.NewLexer(`return nil, false, true, 00042, 0xffffffffffffffff, 9223372036854775808, 0x1.8, "a\x00\255", (((""))), {}, function() end, -1, 1+2, unknown, 1e9999`), "constants.lua"))
	if err := a.Next(); err != nil {
		t.Fatal(err)
	}
	want := []constant{
		{parser.TagNil, "nil"}, {parser.TagBoolean, "false"}, {parser.TagBoolean, "true"},
		{parser.TagInteger, "00042"}, {parser.TagInteger, "0xffffffffffffffff"},
		{parser.TagFloat, "9223372036854775808"}, {parser.TagFloat, "0x1.8"},
		{parser.TagString, "a\x00\xff"}, {parser.TagString, ""},
		{}, {}, {}, {}, {}, {},
	}
	nodes := append([]parser.Node(nil), a.Nodes...)
	values := append([]string(nil), a.Values...)
	block := a.Nodes[a.Nodes[a.Root].Left]
	ret := a.Nodes[a.Nodes[block.Left].Left]
	cell := ret.Left
	for i, expected := range want {
		if cell == 0 {
			t.Fatalf("missing expression %d", i)
		}
		got, ok := constantOf(a, a.Nodes[cell].Left)
		if ok != (expected.kind != parser.TagNone) || got != expected {
			t.Errorf("expression %d: (%#v, %v), want %#v", i, got, ok, expected)
		}
		cell = a.Nodes[cell].Right
	}
	if cell != 0 || !reflect.DeepEqual(nodes, a.Nodes) || !reflect.DeepEqual(values, a.Values) {
		t.Fatal("unexpected expressions or AST mutation")
	}
}

func TestConstantOfInvalidArena(t *testing.T) {
	for _, a := range []*parser.AST{
		nil, {},
		{Nodes: []parser.Node{{}, {Kind: parser.TagInteger}}},
		{Nodes: []parser.Node{{}, {Kind: parser.TagParenthesized, Left: 1}}},
		{Nodes: []parser.Node{{}, {Kind: parser.TagParenthesized, Left: 2}, {Kind: parser.TagParenthesized, Left: 1}}},
		{Nodes: []parser.Node{{}, {Kind: parser.TagParenthesized, Left: math.MaxUint32}}},
	} {
		for _, index := range []uint32{0, 1, math.MaxUint32} {
			if c, ok := constantOf(a, index); ok || c != (constant{}) {
				t.Fatalf("invalid arena accepted: %#v, %d", a, index)
			}
		}
	}
	a := &parser.AST{Nodes: make([]parser.Node, 10002), Values: make([]string, 10002)}
	for i := 1; i < len(a.Nodes)-1; i++ {
		a.Nodes[i] = parser.Node{Kind: parser.TagParenthesized, Left: uint32(i + 1)}
	}
	a.Nodes[10001].Kind, a.Values[10001] = parser.TagInteger, "-42"
	if got, ok := constantOf(a, 1); !ok || got != (constant{parser.TagInteger, "-42"}) {
		t.Fatalf("deep synthesized chain: %v, %v", got, ok)
	}
}

func TestTruth(t *testing.T) {
	for _, c := range []constant{{parser.TagNil, "nil"}, {parser.TagBoolean, "false"}, {parser.TagBoolean, "true"}, {parser.TagInteger, "0"}, {parser.TagFloat, "-0.0"}, {parser.TagString, ""}, {parser.TagString, "false"}} {
		want := c.kind != parser.TagNil && c != (constant{parser.TagBoolean, "false"})
		if truth(c) != want {
			t.Errorf("truth(%v) != %v", c, want)
		}
	}
}

func TestFoldUnary(t *testing.T) {
	for _, tt := range []struct {
		name     string
		op       parser.Tag
		in, want constant
	}{
		{"not nil", parser.TagNot, constant{parser.TagNil, "nil"}, booleanConstant(true)},
		{"not zero", parser.TagNot, integerConstant(0), booleanConstant(false)},
		{"not empty", parser.TagNot, constant{parser.TagString, ""}, booleanConstant(false)},
		{"byte length", parser.TagLength, constant{parser.TagString, "é\x00\xff"}, integerConstant(4)},
		{"negate min", parser.TagNegate, integerConstant(math.MinInt64), integerConstant(math.MinInt64)},
		{"integer zero", parser.TagNegate, integerConstant(0), integerConstant(0)},
		{"negative zero", parser.TagNegate, constant{parser.TagFloat, "0.0"}, constant{parser.TagFloat, "-0.0"}},
		{"positive zero", parser.TagNegate, constant{parser.TagFloat, "-0.0"}, constant{parser.TagFloat, "0.0"}},
		{"hex implicit exponent", parser.TagNegate, constant{parser.TagFloat, "0x1.8"}, constant{parser.TagFloat, "-1.5"}},
		{"hex exponent", parser.TagNegate, constant{parser.TagFloat, "0X.8P-1"}, constant{parser.TagFloat, "-0.25"}},
		{"hex wrapping", parser.TagNegate, constant{parser.TagInteger, "0xffffffffffffffff"}, integerConstant(1)},
		{"large hex wrapping", parser.TagNegate, constant{parser.TagInteger, "0x10000000000000000"}, integerConstant(0)},
		{"invalid overflowing decimal tag", parser.TagNegate, constant{parser.TagInteger, "18446744073709551617"}, constant{}},
		{"decimal not octal", parser.TagNegate, constant{parser.TagInteger, "00042"}, integerConstant(-42)},
		{"bit not", parser.TagBitwiseNot, integerConstant(0), integerConstant(-1)},
		{"integral float", parser.TagBitwiseNot, constant{parser.TagFloat, "-0x1p63"}, integerConstant(math.MaxInt64)},
		{"fractional bit not", parser.TagBitwiseNot, constant{parser.TagFloat, "1.5"}, constant{}},
		{"out of range bit not", parser.TagBitwiseNot, constant{parser.TagFloat, "0x1p63"}, constant{}},
		{"no string coercion", parser.TagNegate, constant{parser.TagString, "42"}, constant{}},
		{"no numeric length", parser.TagLength, integerConstant(1), constant{}},
		{"unknown operator", parser.TagAdd, integerConstant(1), constant{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := foldUnary(tt.op, tt.in)
			if got != tt.want || ok != (tt.want.kind != parser.TagNone) {
				t.Fatalf("got (%v, %v), want %v", got, ok, tt.want)
			}
		})
	}
}

func TestFoldBinary(t *testing.T) {
	i := integerConstant
	f := func(s string) constant { return constant{parser.TagFloat, s} }
	s := func(s string) constant { return constant{parser.TagString, s} }
	b := booleanConstant
	for _, tt := range []struct {
		name              string
		op                parser.Tag
		left, right, want constant
	}{
		{"add wrap", parser.TagAdd, i(math.MaxInt64), i(1), i(math.MinInt64)},
		{"subtract wrap", parser.TagSubtract, i(math.MinInt64), i(1), i(math.MaxInt64)},
		{"multiply wrap", parser.TagMultiply, i(math.MaxInt64), i(2), i(-2)},
		{"mixed add", parser.TagAdd, i(2), f("0x1.8"), f("3.5")},
		{"float subtract", parser.TagSubtract, f("2.5"), i(1), f("1.5")},
		{"float multiply", parser.TagMultiply, f("-0.0"), i(2), f("-0.0")},
		{"division is float", parser.TagDivide, i(4), i(2), f("2.0")},
		{"libm-dependent power refused", parser.TagPower, i(2), i(3), constant{}},
		{"zero exponent", parser.TagPower, i(2), i(0), f("1.0")},
		{"one exponent", parser.TagPower, i(2), i(1), f("2.0")},
		{"unit base", parser.TagPower, i(1), f("0.5"), f("1.0")},
		{"floor negative dividend", parser.TagIntegerDivide, i(-7), i(3), i(-3)},
		{"floor negative divisor", parser.TagIntegerDivide, i(7), i(-3), i(-3)},
		{"floor both negative", parser.TagIntegerDivide, i(-7), i(-3), i(2)},
		{"min divided by minus one", parser.TagIntegerDivide, i(math.MinInt64), i(-1), i(math.MinInt64)},
		{"min modulo minus one", parser.TagModulo, i(math.MinInt64), i(-1), i(0)},
		{"mod negative dividend", parser.TagModulo, i(-7), i(3), i(2)},
		{"mod negative divisor", parser.TagModulo, i(7), i(-3), i(-2)},
		{"mod both negative", parser.TagModulo, i(-7), i(-3), i(-1)},
		{"mod min divisor", parser.TagModulo, i(1), i(math.MinInt64), i(math.MinInt64 + 1)},
		{"floor min divisor", parser.TagIntegerDivide, i(1), i(math.MinInt64), i(-1)},
		{"float floor", parser.TagIntegerDivide, f("-7.0"), i(3), f("-3.0")},
		{"float mod", parser.TagModulo, f("7.0"), i(-3), f("-2.0")},
		{"float mod negative", parser.TagModulo, f("-7.0"), i(3), f("2.0")},
		{"float mod signed zero", parser.TagModulo, f("-6.0"), i(3), f("-0.0")},
		{"mod avoids quotient overflow", parser.TagModulo, f("1e308"), f("1e-308"), f("3.498445546245627e-309")},
		{"logical right shift", parser.TagShiftRight, i(-1), i(1), i(math.MaxInt64)},
		{"left shift sign bit", parser.TagShiftLeft, i(1), i(63), i(math.MinInt64)},
		{"reverse left", parser.TagShiftLeft, i(-1), i(-1), i(math.MaxInt64)},
		{"reverse right", parser.TagShiftRight, i(1), i(-1), i(2)},
		{"shift 64", parser.TagShiftLeft, i(1), i(64), i(0)},
		{"shift -64", parser.TagShiftRight, i(-1), i(-64), i(0)},
		{"shift min", parser.TagShiftLeft, i(-1), i(math.MinInt64), i(0)},
		{"shift max", parser.TagShiftRight, i(-1), i(math.MaxInt64), i(0)},
		{"shift zero", parser.TagShiftRight, i(-1), i(0), i(-1)},
		{"bit and float", parser.TagBitAnd, f("3.0"), i(6), i(2)},
		{"bit or", parser.TagBitOr, i(3), i(6), i(7)},
		{"bit xor", parser.TagBitXor, i(3), i(6), i(5)},
		{"fractional shift", parser.TagShiftLeft, i(1), f("1.5"), constant{}},
		{"or returns zero", parser.TagOr, i(0), i(2), i(0)},
		{"and returns empty string", parser.TagAnd, b(true), s(""), s("")},
		{"and returns nil", parser.TagAnd, constant{parser.TagNil, "nil"}, i(2), constant{parser.TagNil, "nil"}},
		{"or returns right", parser.TagOr, b(false), i(2), i(2)},
		{"concat bytes", parser.TagConcat, s("\xff"), s("\x00"), s("\xff\x00")},
		{"concat number refused", parser.TagConcat, s("x"), f("1.0"), constant{}},
		{"string order refused", parser.TagLess, s("a"), s("b"), constant{}},
		{"string equality", parser.TagEqual, s("\xff\x00"), s("\xff\x00"), b(true)},
		{"distinct types", parser.TagNotEqual, s("1"), i(1), b(true)},
		{"nil equality", parser.TagEqual, constant{parser.TagNil, ""}, constant{parser.TagNil, "nil"}, b(true)},
		{"no arithmetic coercion", parser.TagAdd, s("1"), i(1), constant{}},
		{"no relational coercion", parser.TagLessEqual, s("1"), i(1), constant{}},
		{"nan power", parser.TagPower, i(-1), f("0.5"), constant{}},
		{"infinite power", parser.TagPower, i(0), i(-1), constant{}},
		{"overflow float", parser.TagMultiply, f("1e308"), i(2), constant{}},
		{"underflow signed zero", parser.TagMultiply, f("-1e-300"), f("1e-300"), f("-0.0")},
		{"unknown operator", parser.TagCallExpression, i(1), i(2), constant{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := foldBinary(tt.op, tt.left, tt.right)
			if got != tt.want || ok != (tt.want.kind != parser.TagNone) {
				t.Fatalf("got (%v, %v), want %v", got, ok, tt.want)
			}
		})
	}
	for _, op := range []parser.Tag{parser.TagDivide, parser.TagIntegerDivide, parser.TagModulo} {
		for _, zero := range []constant{i(0), f("0.0"), f("-0.0")} {
			if got, ok := foldBinary(op, i(1), zero); ok || got != (constant{}) {
				t.Errorf("zero divisor accepted: %v, %v", op, zero)
			}
		}
	}
}

func TestNumericComparisons(t *testing.T) {
	for _, tt := range []struct {
		left, right constant
		cmp         int
	}{
		{integerConstant(9007199254740993), constant{parser.TagFloat, "9007199254740992.0"}, 1},
		{integerConstant(-9007199254740993), constant{parser.TagFloat, "-9007199254740992.0"}, -1},
		{integerConstant(math.MaxInt64), constant{parser.TagFloat, "0x1p63"}, -1},
		{integerConstant(math.MinInt64), constant{parser.TagFloat, "-0x1p63"}, 0},
		{integerConstant(math.MinInt64), constant{parser.TagFloat, "-0x1.0000000000001p63"}, 1},
		{integerConstant(math.MinInt64 + 1), constant{parser.TagFloat, "-0x1p63"}, 1},
		{integerConstant(0), constant{parser.TagFloat, "-0.0"}, 0},
		{integerConstant(0), constant{parser.TagFloat, "0.5"}, -1},
		{integerConstant(0), constant{parser.TagFloat, "-0.5"}, 1},
		{integerConstant(9007199254740993), integerConstant(9007199254740992), 1},
		{constant{parser.TagInteger, "0xffffffffffffffff"}, integerConstant(-1), 0},
		{constant{parser.TagFloat, "1.5"}, constant{parser.TagFloat, "1.25"}, 1},
	} {
		for range 2 {
			for _, op := range []parser.Tag{parser.TagEqual, parser.TagNotEqual, parser.TagLess, parser.TagLessEqual, parser.TagGreater, parser.TagGreaterEqual} {
				want := op == parser.TagEqual && tt.cmp == 0 || op == parser.TagNotEqual && tt.cmp != 0 || op == parser.TagLess && tt.cmp < 0 || op == parser.TagLessEqual && tt.cmp <= 0 || op == parser.TagGreater && tt.cmp > 0 || op == parser.TagGreaterEqual && tt.cmp >= 0
				got, ok := foldBinary(op, tt.left, tt.right)
				if !ok || got != booleanConstant(want) {
					t.Errorf("%v op %v %v = (%v, %v), want %v", tt.left, op, tt.right, got, ok, want)
				}
			}
			tt.left, tt.right, tt.cmp = tt.right, tt.left, -tt.cmp
		}
	}
}

func TestInvalidConstants(t *testing.T) {
	for _, c := range []constant{
		{}, {parser.TagFunctionExpression, ""}, {parser.TagTableConstructor, ""},
		{parser.TagInteger, ""}, {parser.TagInteger, "0x"}, {parser.TagInteger, "1.0"}, {parser.TagInteger, "1_0"}, {parser.TagInteger, "0b1"},
		{parser.TagFloat, "NaN"}, {parser.TagFloat, "+Inf"}, {parser.TagFloat, "1e9999"}, {parser.TagFloat, "1_0.0"}, {parser.TagFloat, "bad"},
		{parser.TagBoolean, "yes"},
	} {
		a := &parser.AST{Nodes: []parser.Node{{}, {Kind: c.kind}}, Values: []string{"", c.value}}
		if _, ok := constantOf(a, 1); ok {
			t.Errorf("constantOf accepted %v", c)
		}
		if _, ok := foldUnary(parser.TagNot, c); ok {
			t.Errorf("unary accepted %v", c)
		}
		for _, op := range []parser.Tag{parser.TagOr, parser.TagAnd, parser.TagEqual, parser.TagAdd} {
			if _, ok := foldBinary(op, c, integerConstant(1)); ok {
				t.Errorf("binary accepted left %v", c)
			}
			if _, ok := foldBinary(op, integerConstant(1), c); ok {
				t.Errorf("binary accepted right %v", c)
			}
		}
	}
}
