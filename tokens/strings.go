package tokens

import (
	"fmt"
	"strings"

	"github.com/robogg133/glua/lex"
)

func readShortString(l *lex.Lexer) (string, error) {
	quote := l.Peek()
	if quote != '\'' && quote != '"' {
		return "", fmt.Errorf("expected string quote")
	}
	l.Next()
	start := l.ByteIndex()
	var value strings.Builder
	changed := false
	for {
		pos := l.ByteIndex()
		ch := l.Next()
		switch ch {
		case 0:
			return "", fmt.Errorf("unfinished string")
		case '\r', '\n':
			return "", fmt.Errorf("unescaped newline in string")
		case quote:
			if !changed {
				return l.Slice(start, pos), nil
			}
			value.WriteString(l.Slice(start, pos))
			return value.String(), nil
		case '\\':
			changed = true
			value.WriteString(l.Slice(start, pos))
			ch = l.Next()
			switch ch {
			case 'a':
				value.WriteByte('\a')
			case 'b':
				value.WriteByte('\b')
			case 'f':
				value.WriteByte('\f')
			case 'n':
				value.WriteByte('\n')
			case 'r':
				value.WriteByte('\r')
			case 't':
				value.WriteByte('\t')
			case 'v':
				value.WriteByte('\v')
			case '\\', '"', '\'':
				value.WriteByte(ch)
			case '\r', '\n':
				consumeNewlinePair(l, ch)
				value.WriteByte('\n')
			case 'z':
				for ch = l.Peek(); ch == ' ' || (ch >= '\t' && ch <= '\r'); ch = l.Peek() {
					l.Next()
				}
			case 'x':
				n := 0
				for i := 0; i < 2; i++ {
					digit := stringHexDigit(l.Peek())
					if digit < 0 {
						return "", fmt.Errorf("expected two hexadecimal digits after \\x")
					}
					l.Next()
					n = n*16 + digit
				}
				value.WriteByte(byte(n))
			case 'u':
				if l.Peek() != '{' {
					return "", fmt.Errorf("expected '{' after \\u")
				}
				l.Next()
				var n uint32
				digits := 0
				for {
					digit := stringHexDigit(l.Peek())
					if digit < 0 {
						break
					}
					if n > (0x7fffffff-uint32(digit))/16 {
						return "", fmt.Errorf("Unicode escape exceeds 0x7fffffff")
					}
					l.Next()
					n = n*16 + uint32(digit)
					digits++
				}
				if digits == 0 || l.Peek() != '}' {
					return "", fmt.Errorf("invalid Unicode escape")
				}
				l.Next()
				writeLuaUTF8(&value, n)
			default:
				if ch < '0' || ch > '9' {
					return "", fmt.Errorf("invalid string escape")
				}
				n := int(ch - '0')
				for i := 1; i < 3 && l.Peek() >= '0' && l.Peek() <= '9'; i++ {
					n = n*10 + int(l.Next()-'0')
				}
				if n > 255 {
					return "", fmt.Errorf("decimal escape exceeds 255")
				}
				value.WriteByte(byte(n))
			}
			start = l.ByteIndex()

		}
	}
}

func longDelimiter(l *lex.Lexer, closing bool) int {
	bracket := int('[')
	if closing {
		bracket = ']'
	}
	if l.PeekByte(0) != bracket {
		return -1
	}
	level := 0
	for l.PeekByte(level+1) == '=' {
		level++
	}
	if l.PeekByte(level+1) != bracket {
		return -1
	}
	return level
}

func readLongString(l *lex.Lexer, level int) (string, error) {
	return scanLongString(l, level, false)
}

func skipLongComment(l *lex.Lexer, level int) error {
	_, err := scanLongString(l, level, true)
	return err
}

func scanLongString(l *lex.Lexer, level int, discard bool) (string, error) {
	if level < 0 || longDelimiter(l, false) != level {
		return "", fmt.Errorf("invalid long string delimiter")
	}
	for i := 0; i < level+2; i++ {
		l.Next()
	}
	if ch := l.Peek(); ch == '\r' || ch == '\n' {
		l.Next()
		consumeNewlinePair(l, ch)
	}
	start := l.ByteIndex()
	var value strings.Builder
	changed := false
	for {
		pos := l.ByteIndex()
		ch := l.Peek()
		if ch == 0 {
			return "", fmt.Errorf("unfinished long string")
		}
		if ch == ']' && longDelimiter(l, true) == level {
			for i := 0; i < level+2; i++ {
				l.Next()
			}
			if discard {
				return "", nil
			}
			if !changed {
				return l.Slice(start, pos), nil
			}
			value.WriteString(l.Slice(start, pos))
			return value.String(), nil
		}
		l.Next()
		if discard {
			continue
		}
		if ch == '\r' || (ch == '\n' && l.PeekByte(0) == '\r') {
			consumeNewlinePair(l, ch)
			changed = true
			value.WriteString(l.Slice(start, pos))
			value.WriteByte('\n')
			start = l.ByteIndex()

		}
	}
}

func consumeNewlinePair(l *lex.Lexer, first byte) {
	if next := l.Peek(); (first == '\r' && next == '\n') || (first == '\n' && next == '\r') {
		l.Next()
	}
}

func stringHexDigit(ch byte) int {
	switch {
	case ch >= '0' && ch <= '9':
		return int(ch - '0')
	case ch >= 'a' && ch <= 'f':
		return int(ch-'a') + 10
	case ch >= 'A' && ch <= 'F':
		return int(ch-'A') + 10
	default:
		return -1
	}
}

// Lua accepts surrogates and up to six UTF-8 bytes, unlike unicode/utf8.
func writeLuaUTF8(value *strings.Builder, n uint32) {
	if n < 0x80 {
		value.WriteByte(byte(n))
		return
	}
	var encoded [6]byte
	i := len(encoded)
	limit := uint32(0x3f)
	for {
		i--
		encoded[i] = 0x80 | byte(n&0x3f)
		n >>= 6
		limit >>= 1
		if n <= limit {
			break
		}
	}
	i--
	encoded[i] = byte((^limit << 1) | n)
	value.Write(encoded[i:])
}
