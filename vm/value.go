package vm

import (
	"cmp"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/robogg133/glua/compile"
)

type NativeFunction func(*State, []any) ([]any, error)

// nativeFunction gives a Go callable stable Lua identity. Each normalization of
// a raw NativeFunction creates a distinct Lua function; reuse the wrapper (or
// a value retrieved from a table) when the same identity is needed.
type nativeFunction struct{ function NativeFunction }

// LuaError preserves the original Lua object across protected calls.
type LuaError struct{ Value any }

func (e LuaError) Error() string { return luaString(e.Value) }

type Table struct {
	values    map[any]any
	order     []any
	Metatable *Table
}

func NewTable() *Table { return &Table{values: make(map[any]any)} }
func tableKey(key any) (any, bool) {
	key = normalizeValue(key)
	if key == nil {
		return nil, false
	}
	if f, ok := key.(float64); ok {
		if math.IsNaN(f) {
			return nil, false
		}
		if i, ok := toInteger(f); ok {
			return i, true
		}
	}
	if !reflect.ValueOf(key).Comparable() {
		return nil, false
	}
	return key, true
}
func (t *Table) RawGet(key any) any {
	key, ok := tableKey(key)
	if !ok || t == nil {
		return nil
	}
	return t.values[key]
}
func (t *Table) RawSet(key, value any) error {
	if t == nil {
		return valueError("attempt to index a nil table")
	}
	k, ok := tableKey(key)
	if !ok {
		return LuaError{Value: "table index is nil, NaN, or unsupported"}
	}
	if t.values == nil {
		t.values = make(map[any]any)
	}
	value = normalizeValue(value)
	if value == nil {
		if _, exists := t.values[k]; exists {
			delete(t.values, k)
			for i, v := range t.order {
				if v == k {
					t.order = append(t.order[:i], t.order[i+1:]...)
					break
				}
			}
		}
	} else {
		if _, exists := t.values[k]; !exists {
			t.order = append(t.order, k)
		}
		t.values[k] = value
	}
	return nil
}

// Len selects the first border; Lua permits any border for tables with holes.
// ponytail: linear scan of the contiguous prefix; add an array part if profiling warrants it.
func (t *Table) Len() int64 {
	var n int64
	for n < math.MaxInt64 && t.RawGet(n+1) != nil {
		n++
	}
	return n
}
func (t *Table) Keys() []any {
	if t == nil {
		return nil
	}
	return append([]any(nil), t.order...)
}
func truth(v any) bool { return v != nil && v != false }
func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "nil"
	case bool:
		return "boolean"
	case int64, float64:
		return "number"
	case string:
		return "string"
	case *Table:
		return "table"
	case *Closure, NativeFunction, *nativeFunction:
		return "function"
	default:
		return "userdata"
	}
}
func luaString(v any) string {
	switch x := v.(type) {
	case nil:
		return "nil"
	case string:
		return x
	case float64:
		s := strconv.FormatFloat(x, 'g', 14, 64)
		if !strings.ContainsAny(s, ".eE") && !math.IsInf(x, 0) && !math.IsNaN(x) {
			s += ".0"
		}
		return s
	case *Table:
		return fmt.Sprintf("table: %p", x)
	case *Closure:
		return fmt.Sprintf("function: %p", x)
	case NativeFunction:
		return fmt.Sprintf("function: %p", x)
	case *nativeFunction:
		return fmt.Sprintf("function: %p", x)
	default:
		return fmt.Sprint(v)
	}
}
func parseNumber(s string) (any, bool) {
	s = strings.Trim(s, " \t\n\r\v\f")
	if s == "" {
		return nil, false
	}
	start := 0
	negative := s[0] == '-'
	if s[0] == '+' || negative {
		start++
	}
	hex := len(s)-start >= 2 && s[start] == '0' && (s[start+1] == 'x' || s[start+1] == 'X')
	if hex {
		start += 2
	}
	digit := func(c byte) (uint64, bool) {
		if c >= '0' && c <= '9' {
			return uint64(c - '0'), true
		}
		if hex && c >= 'a' && c <= 'f' {
			return uint64(c - 'a' + 10), true
		}
		if hex && c >= 'A' && c <= 'F' {
			return uint64(c - 'A' + 10), true
		}
		return 0, false
	}
	pos, digits := start, 0
	var integer uint64
	base := uint64(10)
	if hex {
		base = 16
	}
	for pos < len(s) {
		d, ok := digit(s[pos])
		if !ok {
			break
		}
		integer = integer*base + d
		pos++
		digits++
	}
	integral := true
	if pos < len(s) && s[pos] == '.' {
		integral = false
		pos++
		for pos < len(s) {
			_, ok := digit(s[pos])
			if !ok {
				break
			}
			pos++
			digits++
		}
	}
	if digits == 0 {
		return nil, false
	}
	hasExponent := false
	if pos < len(s) && ((!hex && (s[pos] == 'e' || s[pos] == 'E')) || (hex && (s[pos] == 'p' || s[pos] == 'P'))) {
		integral = false
		hasExponent = true
		pos++
		if pos < len(s) && (s[pos] == '+' || s[pos] == '-') {
			pos++
		}
		expStart := pos
		for pos < len(s) && s[pos] >= '0' && s[pos] <= '9' {
			pos++
		}
		if pos == expStart {
			return nil, false
		}
	}
	if pos != len(s) {
		return nil, false
	}
	if integral {
		if hex {
			i := int64(integer)
			if negative {
				i = -i
			}
			return i, true
		}
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i, true
		}
	}
	if hex && !hasExponent {
		s += "p0"
	}
	f, err := strconv.ParseFloat(s, 64)
	if err == nil {
		return f, true
	}
	if e, ok := err.(*strconv.NumError); ok && e.Err == strconv.ErrRange {
		return f, true
	}
	return nil, false
}
func toInteger(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case float64:
		if x >= -0x1p63 && x < 0x1p63 && math.Trunc(x) == x {
			return int64(x), true
		}
	case string:
		if n, ok := parseNumber(x); ok {
			return toInteger(n)
		}
	}
	return 0, false
}
func toNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case string:
		if n, ok := parseNumber(x); ok {
			return toNumber(n)
		}
	}
	return 0, false
}
func normalizeValue(v any) any {
	switch x := v.(type) {
	case *Table:
		if x == nil {
			return nil
		}
	case NativeFunction:
		return &nativeFunction{function: x}
	case int:
		return int64(x)
	case int8:
		return int64(x)
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case uint:
		return int64(x)
	case uint8:
		return int64(x)
	case uint16:
		return int64(x)
	case uint32:
		return int64(x)
	case uint64:
		return int64(x)
	case float32:
		return float64(x)
	}
	return v
}
func (s *State) metamethod(v any, name string) any {
	if t, ok := v.(*Table); ok {
		if t != nil && t.Metatable != nil {
			return t.Metatable.RawGet(name)
		}
		return nil
	}
	if mt := s.TypeMetatables[typeName(v)]; mt != nil {
		return mt.RawGet(name)
	}
	return nil
}
func valueError(format string, args ...any) error {
	return LuaError{Value: fmt.Sprintf(format, args...)}
}
func isFunction(v any) bool {
	switch v.(type) {
	case NativeFunction, *nativeFunction, *Closure:
		return true
	}
	return false
}
func (s *State) metaResult(fn any, args ...any) (any, error) {
	values, err := s.call(fn, args)
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, nil
	}
	return values[0], nil
}
func (s *State) get(object, key any) (any, error) {
	for i := 0; i < 200; i++ {
		t, ok := object.(*Table)
		if ok {
			if v := t.RawGet(key); v != nil {
				return v, nil
			}
		}
		mm := s.metamethod(object, "__index")
		if mm == nil {
			if ok {
				return nil, nil
			}
			return nil, valueError("attempt to index a %s value", typeName(object))
		}
		if isFunction(mm) {
			return s.metaResult(mm, object, key)
		}
		object = mm
	}
	return nil, valueError("'__index' chain too long; possible loop")
}
func (s *State) set(object, key, value any) error {
	for i := 0; i < 200; i++ {
		t, ok := object.(*Table)
		if ok && t.RawGet(key) != nil {
			return t.RawSet(key, value)
		}
		mm := s.metamethod(object, "__newindex")
		if mm == nil {
			if ok {
				return t.RawSet(key, value)
			}
			return valueError("attempt to index a %s value", typeName(object))
		}
		if isFunction(mm) {
			_, err := s.call(mm, []any{object, key, value})
			return err
		}
		object = mm
	}
	return valueError("'__newindex' chain too long; possible loop")
}
func (s *State) length(v any) (any, error) {
	if x, ok := v.(string); ok {
		return int64(len(x)), nil
	}
	if mm := s.metamethod(v, "__len"); mm != nil {
		return s.metaResult(mm, v, v)
	}
	if t, ok := v.(*Table); ok {
		return t.Len(), nil
	}
	return nil, valueError("attempt to get length of a %s value", typeName(v))
}
func shift(x, n int64) int64 {
	if n >= 64 || n <= -64 {
		return 0
	}
	if n < 0 {
		return int64(uint64(x) >> uint64(-n))
	}
	return int64(uint64(x) << uint64(n))
}
func (s *State) binaryMeta(name string, left, right any) (any, error) {
	mm := s.metamethod(left, name)
	if mm == nil {
		mm = s.metamethod(right, name)
	}
	if mm != nil {
		return s.metaResult(mm, left, right)
	}
	return nil, valueError("attempt to perform %s on a %s and a %s", name, typeName(left), typeName(right))
}
func (s *State) arithmetic(op compile.OpCode, left, right any) (any, error) {
	names := []string{"__add", "__sub", "__mul", "__mod", "__pow", "__div", "__idiv", "__band", "__bor", "__bxor", "__shl", "__shr"}
	if op < compile.OpADD || op > compile.OpSHR {
		return nil, valueError("invalid arithmetic opcode %d", op)
	}
	if op >= compile.OpBAND {
		a, ok := toInteger(left)
		b, ok2 := toInteger(right)
		if ok && ok2 {
			switch op {
			case compile.OpBAND:
				return a & b, nil
			case compile.OpBOR:
				return a | b, nil
			case compile.OpBXOR:
				return a ^ b, nil
			case compile.OpSHL:
				return shift(a, b), nil
			case compile.OpSHR:
				if b == math.MinInt64 {
					return int64(0), nil
				}
				return shift(a, -b), nil
			}
		}
	} else {
		// Lua arithmetic coerces numeric strings to floats.
		l, r := left, right
		if x, ok := l.(string); ok {
			if n, ok := toNumber(x); ok {
				l = n
			}
		}
		if x, ok := r.(string); ok {
			if n, ok := toNumber(x); ok {
				r = n
			}
		}
		a, ai := l.(int64)
		b, bi := r.(int64)
		if ai && bi {
			switch op {
			case compile.OpADD:
				return a + b, nil
			case compile.OpSUB:
				return a - b, nil
			case compile.OpMUL:
				return a * b, nil
			case compile.OpIDIV, compile.OpMOD:
				if b == 0 {
					if op == compile.OpMOD {
						return nil, valueError("attempt to perform 'n%%0'")
					}
					return nil, valueError("attempt to divide by zero")
				}
				if b == -1 {
					if op == compile.OpMOD {
						return int64(0), nil
					}
					return -a, nil
				}
				q, rem := a/b, a%b
				if rem != 0 && (rem < 0) != (b < 0) {
					q--
					rem += b
				}
				if op == compile.OpMOD {
					return rem, nil
				}
				return q, nil
			}
		}
		x, ok := toNumber(l)
		y, ok2 := toNumber(r)
		if ok && ok2 {
			switch op {
			case compile.OpADD:
				return x + y, nil
			case compile.OpSUB:
				return x - y, nil
			case compile.OpMUL:
				return x * y, nil
			case compile.OpDIV:
				return x / y, nil
			case compile.OpPOW:
				return math.Pow(x, y), nil
			case compile.OpIDIV:
				return math.Floor(x / y), nil
			case compile.OpMOD:
				m := math.Mod(x, y)
				if m != 0 && (m < 0) != (y < 0) {
					m += y
				}
				return m, nil
			}
		}
	}
	return s.binaryMeta(names[int(op-compile.OpADD)], left, right)
}
func (s *State) unary(op compile.OpCode, v any) (any, error) {
	switch op {
	case compile.OpNOT:
		return !truth(v), nil
	case compile.OpLEN:
		return s.length(v)
	case compile.OpUNM:
		n := v
		if x, ok := v.(string); ok {
			if parsed, ok := toNumber(x); ok {
				n = parsed
			}
		}
		if i, ok := n.(int64); ok {
			return -i, nil
		}
		if f, ok := toNumber(n); ok {
			return -f, nil
		}
		return s.binaryMeta("__unm", v, v)
	case compile.OpBNOT:
		if i, ok := toInteger(v); ok {
			return ^i, nil
		}
		return s.binaryMeta("__bnot", v, v)
	}
	return nil, valueError("invalid unary opcode %d", op)
}
func (s *State) concat(left, right any) (any, error) {
	text := func(v any) (string, bool) {
		switch v.(type) {
		case string, int64, float64:
			return luaString(v), true
		}
		return "", false
	}
	a, ok := text(left)
	b, ok2 := text(right)
	if ok && ok2 {
		return a + b, nil
	}
	return s.binaryMeta("__concat", left, right)
}

// numericCompare compares mixed integers/floats without rounding integers.
func numericCompare(a, b any) (int, bool) {
	switch x := a.(type) {
	case int64:
		switch y := b.(type) {
		case int64:
			return cmp.Compare(x, y), true
		case float64:
			return integerFloatCompare(x, y)
		}
	case float64:
		switch y := b.(type) {
		case int64:
			c, ok := integerFloatCompare(y, x)
			return -c, ok
		case float64:
			if math.IsNaN(x) || math.IsNaN(y) {
				return 0, false
			}
			return cmp.Compare(x, y), true
		}
	}
	return 0, false
}
func integerFloatCompare(i int64, f float64) (int, bool) {
	if math.IsNaN(f) {
		return 0, false
	}
	if f >= 0x1p63 {
		return -1, true
	}
	if f < -0x1p63 {
		return 1, true
	}
	truncated := int64(f)
	if c := cmp.Compare(i, truncated); c != 0 {
		return c, true
	}
	// Only the fractional part remains; converting the truncated float back is exact.
	return cmp.Compare(float64(truncated), f), true
}
func (s *State) compare(op compile.OpCode, left, right any) (bool, error) {
	if op != compile.OpEQ && op != compile.OpLT && op != compile.OpLE {
		return false, valueError("invalid comparison opcode %d", op)
	}
	if typeName(left) == "number" && typeName(right) == "number" {
		c, ok := numericCompare(left, right)
		if !ok {
			return false, nil
		}
		switch op {
		case compile.OpEQ:
			return c == 0, nil
		case compile.OpLT:
			return c < 0, nil
		default:
			return c <= 0, nil
		}
	}
	if op == compile.OpEQ {
		equal := false
		switch a := left.(type) {
		case nil:
			equal = right == nil
		case bool:
			b, ok := right.(bool)
			equal = ok && a == b
		case string:
			b, ok := right.(string)
			equal = ok && a == b
		case *Table:
			b, ok := right.(*Table)
			equal = ok && a == b
		case *Closure:
			b, ok := right.(*Closure)
			equal = ok && a == b
		case *nativeFunction:
			b, ok := right.(*nativeFunction)
			equal = ok && a == b
		}
		if equal {
			return true, nil
		}
		if _, ok := left.(*Table); ok {
			if _, ok := right.(*Table); ok {
				if s.metamethod(left, "__eq") != nil || s.metamethod(right, "__eq") != nil {
					v, err := s.binaryMeta("__eq", left, right)
					return truth(v), err
				}
			}
		}
		return false, nil
	}
	if a, ok := left.(string); ok {
		if b, ok := right.(string); ok {
			if op == compile.OpLT {
				return a < b, nil
			}
			return a <= b, nil
		}
	}
	name := "__lt"
	if op == compile.OpLE {
		name = "__le"
	}
	if s.metamethod(left, name) != nil || s.metamethod(right, name) != nil {
		v, err := s.binaryMeta(name, left, right)
		return truth(v), err
	}
	if op == compile.OpLE && (s.metamethod(right, "__lt") != nil || s.metamethod(left, "__lt") != nil) {
		v, err := s.binaryMeta("__lt", right, left)
		return !truth(v), err
	}
	return false, valueError("attempt to compare %s with %s", typeName(left), typeName(right))
}
