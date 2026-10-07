package ir

import (
	"math"
	"strconv"
	"strings"

	"github.com/robogg133/glua/parser"
)

// constant holds decoded string bytes or numeric spelling, never source quotes.
// Synthesized AST Values may contain signed integers/floats (including -0.0),
// even though the parser represents a source unary minus as a separate node.
// The tag, not the spelling alone, determines the numeric kind.
type constant struct {
	kind  parser.Tag
	value string
}

// Bound compile-time allocation, e.g. a chain of s = previous .. previous.
const maxFoldedString = 64 * 1024

// constantOf only unwraps literals, not expressions or identity-bearing values.
func constantOf(a *parser.AST, index uint32) (constant, bool) {
	if a == nil {
		return constant{}, false
	}
	// A chain cannot visit more nodes than the arena without a cycle. Iteration
	// also avoids consuming the Go stack on deeply nested synthesized ASTs.
	for remaining := len(a.Nodes); remaining > 0; remaining-- {
		if index == 0 || uint64(index) >= uint64(len(a.Nodes)) {
			break
		}
		n := a.Nodes[index]
		if n.Kind == parser.TagParenthesized {
			index = n.Left
			continue
		}
		if uint64(index) >= uint64(len(a.Values)) {
			break
		}
		c := constant{n.Kind, a.Values[index]}
		if validConstant(c) {
			return c, true
		}
		break
	}
	return constant{}, false
}

func truth(c constant) bool {
	return c.kind != parser.TagNil && (c.kind != parser.TagBoolean || c.value != "false")
}

func validConstant(c constant) bool {
	switch c.kind {
	case parser.TagNil, parser.TagString:
		return true
	case parser.TagBoolean:
		return c.value == "true" || c.value == "false"
	case parser.TagInteger:
		_, ok := constantInteger(c)
		return ok
	case parser.TagFloat:
		_, ok := constantFloat(c)
		return ok
	}
	return false
}

func integerConstant(i int64) constant {
	return constant{parser.TagInteger, strconv.FormatInt(i, 10)}
}

func booleanConstant(b bool) constant {
	return constant{parser.TagBoolean, strconv.FormatBool(b)}
}

func floatConstant(f float64) (constant, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return constant{}, false
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return constant{parser.TagFloat, s}, true
}

func constantInteger(c constant) (int64, bool) {
	if c.kind == parser.TagFloat {
		f, ok := constantFloat(c)
		if !ok || f < -0x1p63 || f >= 0x1p63 || math.Trunc(f) != f {
			return 0, false
		}
		return int64(f), true
	}
	if c.kind != parser.TagInteger {
		return 0, false
	}
	s := c.value
	negative := false
	if len(s) > 0 && (s[0] == '-' || s[0] == '+') {
		negative = s[0] == '-'
		s = s[1:]
	}
	if !strings.HasPrefix(s, "0x") && !strings.HasPrefix(s, "0X") {
		// Only hexadecimal integer literals wrap. Decimal overflow is a float
		// in Lua and must not be silently accepted under an integer tag.
		i, err := strconv.ParseInt(c.value, 10, 64)
		return i, err == nil
	}
	const base = uint64(16)
	s = s[2:]
	if s == "" {
		return 0, false
	}
	var n uint64
	for i := 0; i < len(s); i++ {
		var digit uint64
		switch ch := s[i]; {
		case ch >= '0' && ch <= '9':
			digit = uint64(ch - '0')
		case ch >= 'a' && ch <= 'f':
			digit = uint64(ch - 'a' + 10)
		case ch >= 'A' && ch <= 'F':
			digit = uint64(ch - 'A' + 10)
		default:
			return 0, false
		}
		if digit >= base {
			return 0, false
		}
		n = n*base + digit // Lua integer arithmetic wraps modulo 2^64.
	}
	if negative {
		n = -n
	}
	return int64(n), true
}

func constantFloat(c constant) (float64, bool) {
	if c.kind == parser.TagInteger {
		i, ok := constantInteger(c)
		return float64(i), ok
	}
	if c.kind != parser.TagFloat || strings.Contains(c.value, "_") {
		return 0, false
	}
	s := c.value
	unsigned := strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+")
	if (strings.HasPrefix(unsigned, "0x") || strings.HasPrefix(unsigned, "0X")) && !strings.ContainsAny(s, "pP") {
		s += "p0" // Lua permits hexadecimal floats without an explicit exponent.
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
}

func foldUnary(kind parser.Tag, operand constant) (constant, bool) {
	if !validConstant(operand) {
		return constant{}, false
	}
	switch kind {
	case parser.TagNot:
		return booleanConstant(!truth(operand)), true
	case parser.TagLength:
		if operand.kind == parser.TagString {
			return integerConstant(int64(len(operand.value))), true
		}
	case parser.TagNegate:
		if operand.kind == parser.TagInteger {
			i, _ := constantInteger(operand)
			return integerConstant(-i), true
		}
		if f, ok := constantFloat(operand); ok {
			return floatConstant(-f)
		}
	case parser.TagBitwiseNot:
		if i, ok := constantInteger(operand); ok {
			return integerConstant(^i), true
		}
	}
	return constant{}, false
}

// compareIntegerFloat avoids rounding the integer through float64, including
// at the int64 bounds where conversion from float64 would be out of range.
func compareIntegerFloat(i int64, f float64) int {
	if f >= 0x1p63 {
		return -1
	}
	if f < -0x1p63 {
		return 1
	}
	j := int64(f)
	if i < j {
		return -1
	}
	if i > j {
		return 1
	}
	if float64(j) < f {
		return -1
	}
	if float64(j) > f {
		return 1
	}
	return 0
}

func compareNumbers(left, right constant) (int, bool) {
	x, xok := constantFloat(left)
	y, yok := constantFloat(right)
	if !xok || !yok {
		return 0, false
	}
	if left.kind == parser.TagInteger {
		i, _ := constantInteger(left)
		if right.kind == parser.TagFloat {
			return compareIntegerFloat(i, y), true
		}
		j, _ := constantInteger(right)
		if i < j {
			return -1, true
		}
		if i > j {
			return 1, true
		}
		return 0, true
	}
	if right.kind == parser.TagInteger {
		j, _ := constantInteger(right)
		return -compareIntegerFloat(j, x), true
	}
	if x < y {
		return -1, true
	}
	if x > y {
		return 1, true
	}
	return 0, true
}

func foldBinary(kind parser.Tag, left, right constant) (constant, bool) {
	if !validConstant(left) || !validConstant(right) {
		return constant{}, false
	}
	switch kind {
	case parser.TagAnd, parser.TagOr:
		if truth(left) == (kind == parser.TagOr) {
			return left, true
		}
		return right, true
	case parser.TagEqual, parser.TagNotEqual:
		equal := left.kind == right.kind && (left.kind == parser.TagNil || left.value == right.value)
		if cmp, ok := compareNumbers(left, right); ok {
			equal = cmp == 0
		}
		return booleanConstant(equal == (kind == parser.TagEqual)), true
	case parser.TagLess, parser.TagLessEqual, parser.TagGreater, parser.TagGreaterEqual:
		cmp, ok := compareNumbers(left, right)
		if !ok {
			// Lua string ordering is locale-sensitive; bytewise equality is not.
			return constant{}, false
		}
		return booleanConstant(kind == parser.TagLess && cmp < 0 ||
			kind == parser.TagLessEqual && cmp <= 0 ||
			kind == parser.TagGreater && cmp > 0 ||
			kind == parser.TagGreaterEqual && cmp >= 0), true
	case parser.TagConcat:
		if left.kind == parser.TagString && right.kind == parser.TagString &&
			len(left.value) <= maxFoldedString && len(right.value) <= maxFoldedString-len(left.value) {
			return constant{parser.TagString, left.value + right.value}, true
		}
		return constant{}, false
	case parser.TagBitAnd, parser.TagBitOr, parser.TagBitXor, parser.TagShiftLeft, parser.TagShiftRight:
		i, iok := constantInteger(left)
		j, jok := constantInteger(right)
		if !iok || !jok {
			return constant{}, false
		}
		switch kind {
		case parser.TagBitAnd:
			return integerConstant(i & j), true
		case parser.TagBitOr:
			return integerConstant(i | j), true
		case parser.TagBitXor:
			return integerConstant(i ^ j), true
		}
		if j <= -64 || j >= 64 {
			return integerConstant(0), true
		}
		if j < 0 {
			j = -j
			if kind == parser.TagShiftLeft {
				kind = parser.TagShiftRight
			} else {
				kind = parser.TagShiftLeft
			}
		}
		if kind == parser.TagShiftLeft {
			return integerConstant(int64(uint64(i) << uint(j))), true
		}
		return integerConstant(int64(uint64(i) >> uint(j))), true
	}

	if left.kind == parser.TagInteger && right.kind == parser.TagInteger {
		i, _ := constantInteger(left)
		j, _ := constantInteger(right)
		switch kind {
		case parser.TagAdd:
			return integerConstant(i + j), true
		case parser.TagSubtract:
			return integerConstant(i - j), true
		case parser.TagMultiply:
			return integerConstant(i * j), true
		case parser.TagIntegerDivide, parser.TagModulo:
			if j == 0 {
				return constant{}, false
			}
			// Go and Lua both wrap MinInt64 / -1. Correct truncation to floor.
			q, r := i/j, i%j
			if r != 0 && (r < 0) != (j < 0) {
				q--
				r += j
			}
			if kind == parser.TagModulo {
				return integerConstant(r), true
			}
			return integerConstant(q), true
		}
	}

	x, xok := constantFloat(left)
	y, yok := constantFloat(right)
	if !xok || !yok {
		return constant{}, false
	}
	switch kind {
	case parser.TagAdd:
		return floatConstant(x + y)
	case parser.TagSubtract:
		return floatConstant(x - y)
	case parser.TagMultiply:
		return floatConstant(x * y)
	case parser.TagDivide, parser.TagIntegerDivide, parser.TagModulo:
		if y == 0 {
			return constant{}, false
		}
		if kind == parser.TagModulo {
			// Lua uses fmod plus a sign correction, not x-floor(x/y)*y.
			r := math.Mod(x, y)
			if r != 0 && (r < 0) != (y < 0) {
				r += y
			}
			return floatConstant(r)
		}
		q := x / y
		if kind == parser.TagIntegerDivide {
			q = math.Floor(q)
		}
		return floatConstant(q)
	case parser.TagPower:
		// Go math.Pow and a target's C libm need not round identically. Only
		// identities with exact, finite results are independent of that choice.
		if y == 0 || x == 1 {
			return floatConstant(1)
		}
		if y == 1 {
			return floatConstant(x)
		}
		return constant{}, false
	}
	return constant{}, false
}
