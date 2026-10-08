package vm

import (
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/robogg133/glua/compile"
)

func TestValueNilTables(t *testing.T) {
	var tab *Table
	if normalizeValue(tab) != nil {
		t.Fatal("nil table not normalized")
	}
	if err := tab.RawSet("key", true); err == nil {
		t.Fatal("nil receiver write accepted")
	}
	if tab.RawGet("key") != nil || tab.Len() != 0 || len(tab.Keys()) != 0 {
		t.Fatal("nil receiver reads")
	}
	s := NewState()
	s.Push(tab)
	if s.At(-1) != nil {
		t.Fatal("pushed nil table not Lua nil")
	}
	table := NewTable()
	_ = table.RawSet("key", true)
	if err := table.RawSet("key", tab); err != nil || table.RawGet("key") != nil {
		t.Fatal("nil table value", err)
	}
	if err := table.RawSet(tab, true); err == nil {
		t.Fatal("nil table key accepted")
	}
	if err := s.set(tab, "key", true); err == nil {
		t.Fatal("nil table set accepted")
	}
	var raw NativeFunction
	wrapped := normalizeValue(raw)
	if _, ok := wrapped.(*nativeFunction); !ok {
		t.Fatal("nil native normalization changed")
	}
	for _, fn := range []any{raw, wrapped, (*Closure)(nil)} {
		if _, err := s.call(fn, nil); err == nil {
			t.Fatal("nil callable accepted")
		}
	}
}

func TestValueUserdataSafety(t *testing.T) {
	s := new(State)
	for _, v := range []any{[]int{1}, map[string]int{"x": 1}, struct{ Value any }{[]int{1}}} {
		if !truth(v) {
			t.Fatal("userdata is false")
		}
		if equal, err := s.compare(compile.OpEQ, v, v); err != nil || equal {
			t.Fatal("unsupported userdata equality", equal, err)
		}
		tab := NewTable()
		if tab.RawGet(v) != nil {
			t.Fatal("unsupported key read")
		}
		if err := tab.RawSet(v, true); err == nil {
			t.Fatal("unsupported key accepted")
		}
	}
	object := []int{1, 2}
	original := LuaError{Value: object}
	var retained LuaError
	if !errors.As(error(original), &retained) {
		t.Fatal("LuaError not retained")
	}
	retained.Value.([]int)[0] = 42
	if object[0] != 42 {
		t.Fatal("LuaError copied error object")
	}
}

func TestValueNumeralGrammar(t *testing.T) {
	for _, s := range []string{"1_0", "0x1_0", "0x_1", "0b10", "0o10", "+-1", "--1", "1e", "0x1p", "0x.p1", "0x", "NaN", "Infinity", "1\x00", "\u00a01\u00a0"} {
		if v, ok := parseNumber(s); ok {
			t.Fatalf("accepted %q as %v", s, v)
		}
	}
	for _, tc := range []struct {
		s    string
		want any
	}{
		{"0x10000000000000000", int64(0)}, {"0x10000000000000001", int64(1)},
		{"-0x10000000000000001", int64(-1)}, {"0xffffffffffffffffffffffff", int64(-1)},
		{".5", float64(.5)}, {"1.", float64(1)}, {"0x.8", float64(.5)},
		{"+0X1.P+2", float64(4)}, {"\v42\f", int64(42)},
	} {
		v, ok := parseNumber(tc.s)
		if !ok || v != tc.want {
			t.Fatalf("%q: %v %v; want %v", tc.s, v, ok, tc.want)
		}
	}
	if v, ok := parseNumber("1e9999"); !ok || !math.IsInf(v.(float64), 1) {
		t.Fatal("overflow", v, ok)
	}
}

func TestValueNativeIdentity(t *testing.T) {
	raw := NativeFunction(func(_ *State, args []any) ([]any, error) { return args, nil })
	fn := normalizeValue(raw).(*nativeFunction)
	other := normalizeValue(raw).(*nativeFunction)
	if fn == other || normalizeValue(fn) != fn {
		t.Fatal("normalization identity")
	}
	if typeName(fn) != "function" || !isFunction(fn) || !isFunction(raw) || !strings.HasPrefix(luaString(fn), "function: ") {
		t.Fatal("native classification")
	}
	tab := NewTable()
	if err := tab.RawSet(fn, "key"); err != nil {
		t.Fatal(err)
	}
	if tab.RawGet(fn) != "key" || tab.RawGet(other) != nil || tab.Keys()[0] != fn {
		t.Fatal("native key identity")
	}
	_ = tab.RawSet("first", raw)
	_ = tab.RawSet("second", raw)
	a, b := tab.RawGet("first"), tab.RawGet("second")
	s := new(State)
	for _, tc := range []struct {
		a, b any
		want bool
	}{{fn, fn, true}, {fn, other, false}, {a, b, false}} {
		got, err := s.compare(compile.OpEQ, tc.a, tc.b)
		if err != nil || got != tc.want {
			t.Fatal("native equality", got, err)
		}
	}
	for _, callable := range []any{raw, fn} {
		results, err := s.call(callable, []any{int64(7)})
		if err != nil || len(results) != 1 || results[0] != int64(7) {
			t.Fatal("native call", results, err)
		}
	}
}

func TestValueNumericCompareExact(t *testing.T) {
	values := []any{int64(math.MinInt64), int64(math.MaxInt64), int64(-9007199254740993), int64(9007199254740993), int64(-1), int64(0), int64(1), float64(-0x1p63), float64(0x1p63), float64(-9007199254740992), float64(9007199254740992), float64(-1.5), float64(-.5), float64(.5), float64(1.5), math.SmallestNonzeroFloat64, math.Inf(-1), math.Inf(1)}
	exact := func(v any) *big.Float {
		if i, ok := v.(int64); ok {
			return new(big.Float).SetInt64(i)
		}
		return new(big.Float).SetFloat64(v.(float64))
	}
	for _, a := range values {
		for _, b := range values {
			got, ok := numericCompare(a, b)
			want := exact(a).Cmp(exact(b))
			if !ok || got != want {
				t.Fatalf("%v versus %v: %d %v; want %d", a, b, got, ok, want)
			}
		}
	}
	for _, a := range []any{int64(1), float64(1), math.NaN()} {
		if _, ok := numericCompare(a, math.NaN()); ok {
			t.Fatal("NaN ordered")
		}
	}
	if n := testing.AllocsPerRun(100, func() {
		numericCompare(int64(math.MaxInt64), float64(0x1p63))
		numericCompare(int64(1), int64(2))
		numericCompare(float64(1), float64(2))
	}); n != 0 {
		t.Fatalf("comparison allocations: %v", n)
	}
}

func TestValueTypeMetatables(t *testing.T) {
	s := new(State)
	mt, methods := NewTable(), NewTable()
	_ = methods.RawSet("method", int64(42))
	_ = mt.RawSet("__index", methods)
	s.TypeMetatables = map[string]*Table{"string": mt, "table": mt}
	if got, err := s.get("text", "method"); err != nil || got != int64(42) {
		t.Fatal(got, err)
	}
	if s.metamethod(NewTable(), "__index") != nil {
		t.Fatal("table used shared type metatable")
	}
}

func TestValueTableKeys(t *testing.T) {
	tab := NewTable()
	for _, key := range []any{nil, math.NaN()} {
		if tab.RawGet(key) != nil {
			t.Fatal("invalid read")
		}
		if tab.RawSet(key, true) == nil {
			t.Fatal("invalid write accepted")
		}
	}
	if err := tab.RawSet(float64(1), "one"); err != nil {
		t.Fatal(err)
	}
	if tab.RawGet(int64(1)) != "one" {
		t.Fatal("integral float key not canonical")
	}
	const large = int64(9007199254740993)
	_ = tab.RawSet(large, "integer")
	_ = tab.RawSet(float64(large), "float")
	if tab.RawGet(large) != "integer" || tab.RawGet(float64(large)) != "float" {
		t.Fatal("large integer rounded")
	}
	_ = tab.RawSet(int64(2), false)
	if tab.Len() != 2 {
		t.Fatalf("length %d", tab.Len())
	}
	keys := tab.Keys()
	keys[0] = "changed"
	if tab.Keys()[0] != int64(1) {
		t.Fatal("Keys aliases table storage")
	}
	_ = tab.RawSet(int64(1), nil)
	_ = tab.RawSet(int64(1), "new")
	if tab.Keys()[len(tab.Keys())-1] != int64(1) {
		t.Fatal("reinsertion order")
	}
}
func TestValueConversions(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int64
	}{{" 42 ", 42}, {"0xff", 255}, {"-0x10", -16}, {"0x1.8p1", 3}, {"3.0", 3}, {"0xffffffffffffffff", -1}} {
		got, ok := toInteger(tc.text)
		if !ok || got != tc.want {
			t.Fatalf("%q: %d %v", tc.text, got, ok)
		}
	}
	for _, v := range []any{math.NaN(), math.Inf(1), float64(0x1p63), "3.5", "nan", "inf", "not a numeral"} {
		if _, ok := toInteger(v); ok {
			t.Fatalf("integer accepted %v", v)
		}
	}
	if i, ok := toInteger(float64(-0x1p63)); !ok || i != math.MinInt64 {
		t.Fatal("minimum integer rejected")
	}
	if luaString(float64(3)) != "3.0" || luaString(int64(3)) != "3" {
		t.Fatal("number formatting")
	}
	for _, v := range []any{int64(0), "", NewTable(), []int{1}, map[string]int{"x": 1}} {
		if !truth(v) {
			t.Fatal("truthiness", v)
		}
	}
	if truth(nil) || truth(false) {
		t.Fatal("false truthiness")
	}
	if normalizeValue(int(7)) != int64(7) || normalizeValue(float32(2)) != float64(2) {
		t.Fatal("normalization")
	}
	original := NewTable()
	var e LuaError
	if !errors.As(error(LuaError{Value: original}), &e) || e.Value != original {
		t.Fatal("error object lost")
	}
}
func TestValueArithmetic(t *testing.T) {
	s := new(State)
	cases := []struct {
		op         compile.OpCode
		a, b, want any
	}{
		{compile.OpADD, int64(math.MaxInt64), int64(1), int64(math.MinInt64)},
		{compile.OpIDIV, int64(-7), int64(3), int64(-3)},
		{compile.OpMOD, int64(-7), int64(3), int64(2)},
		{compile.OpMOD, int64(7), int64(-3), int64(-2)},
		{compile.OpIDIV, int64(math.MinInt64), int64(-1), int64(math.MinInt64)},
		{compile.OpMOD, int64(math.MinInt64), int64(-1), int64(0)},
		{compile.OpSHR, int64(-1), int64(1), int64(math.MaxInt64)},
		{compile.OpSHL, int64(4), int64(-1), int64(2)},
		{compile.OpSHR, int64(4), int64(math.MinInt64), int64(0)},
		{compile.OpSHL, int64(1), int64(64), int64(0)},
		{compile.OpADD, "0x10", int64(2), float64(18)},
		{compile.OpMOD, float64(-7), float64(3), float64(2)},
	}
	for _, tc := range cases {
		got, err := s.arithmetic(tc.op, tc.a, tc.b)
		if err != nil || got != tc.want {
			t.Fatalf("op %d (%v,%v): %v, %v; want %v", tc.op, tc.a, tc.b, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		op      compile.OpCode
		message string
	}{{compile.OpIDIV, "attempt to divide by zero"}, {compile.OpMOD, "attempt to perform 'n%0'"}} {
		if _, err := s.arithmetic(tc.op, int64(1), int64(0)); err == nil || err.Error() != tc.message {
			t.Fatalf("zero divisor: %v; want %q", err, tc.message)
		}
	}
	if got, err := s.arithmetic(compile.OpDIV, float64(1), float64(0)); err != nil || !math.IsInf(got.(float64), 1) {
		t.Fatal("floating division", got, err)
	}
}
func TestValueComparisons(t *testing.T) {
	s := new(State)
	for _, tc := range []struct {
		op   compile.OpCode
		a, b any
		want bool
	}{
		{compile.OpEQ, int64(9007199254740993), float64(9007199254740992), false},
		{compile.OpLT, float64(9007199254740992), int64(9007199254740993), true},
		{compile.OpLT, int64(math.MaxInt64), float64(0x1p63), true},
		{compile.OpEQ, int64(3), float64(3), true},
		{compile.OpLE, math.NaN(), math.NaN(), false},
		{compile.OpEQ, math.NaN(), math.NaN(), false},
		{compile.OpEQ, "3", int64(3), false},
	} {
		got, err := s.compare(tc.op, tc.a, tc.b)
		if err != nil || got != tc.want {
			t.Fatalf("compare %v %v: %v %v", tc.a, tc.b, got, err)
		}
	}
}
func TestValueMetamethods(t *testing.T) {
	s := new(State)
	left, right := NewTable(), NewTable()
	left.Metatable = NewTable()
	right.Metatable = NewTable()
	_ = right.Metatable.RawSet("__sub", NativeFunction(func(_ *State, args []any) ([]any, error) {
		if args[0] != left || args[1] != right {
			t.Fatal("arithmetic argument order")
		}
		return []any{int64(17)}, nil
	}))
	if got, err := s.arithmetic(compile.OpSUB, left, right); err != nil || got != int64(17) {
		t.Fatal(got, err)
	}
	_ = left.Metatable.RawSet("__lt", NativeFunction(func(_ *State, args []any) ([]any, error) {
		if args[0] != right || args[1] != left {
			t.Fatal("__le fallback argument order")
		}
		return []any{false}, nil
	}))
	if got, err := s.compare(compile.OpLE, left, right); err != nil || !got {
		t.Fatal(got, err)
	}
	index := NewTable()
	_ = index.RawSet("answer", int64(42))
	_ = left.Metatable.RawSet("__index", index)
	if got, err := s.get(left, "answer"); err != nil || got != int64(42) {
		t.Fatal(got, err)
	}
	_ = left.Metatable.RawSet("__newindex", index)
	if err := s.set(left, "write", true); err != nil || index.RawGet("write") != true {
		t.Fatal(err)
	}
	_ = left.Metatable.RawSet("__index", left)
	if _, err := s.get(left, "missing"); err == nil {
		t.Fatal("index loop accepted")
	}
}
