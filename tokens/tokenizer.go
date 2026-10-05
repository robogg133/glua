package tokens

import (
	"fmt"

	"strings"

	"github.com/robogg133/glua/lex"
)

type Tokenizer struct {
	l         *lex.Lexer
	filename  string
	lookahead Token
	err       error
	ready     bool
}

func NewTokenizer(l *lex.Lexer, filename string) *Tokenizer {
	return &Tokenizer{
		l:        l,
		filename: filename,
	}
}

// Next consumes one token and panics on a lexical error.
func (t *Tokenizer) Next() Token {
	token, err := t.NextChecked()
	if err != nil {
		panic(err)
	}
	return token
}

// Peek returns the next token without consuming it and panics on a lexical error.
func (t *Tokenizer) Peek() Token {
	token, err := t.PeekChecked()
	if err != nil {
		panic(err)
	}
	return token
}

// NextChecked consumes one token. EOF and lexical errors remain stable.
func (t *Tokenizer) NextChecked() (Token, error) {
	token, err := t.PeekChecked()
	if err == nil && token.Type != TkEos {
		t.ready = false
	}
	return token, err
}

func (t *Tokenizer) PeekChecked() (Token, error) {
	if !t.ready {
		t.lookahead, t.err = t.readNext()
		t.ready = true
	}
	return t.lookahead, t.err
}

// Tokenize panics on invalid input. Use TokenizeChecked to handle lexical errors.
func Tokenize(input string) []Token {
	result, err := TokenizeChecked(input)
	if err != nil {
		panic(err)
	}
	return result
}

func TokenizeChecked(input string) ([]Token, error) {
	t := NewTokenizer(lex.NewLexer(input), "")
	var result []Token
	for {
		token, err := t.NextChecked()
		if err != nil {
			return result, err
		}
		result = append(result, token)
		if token.Type == TkEos {
			return result, nil
		}
	}
}

func (t *Tokenizer) readNext() (Token, error) {
	l := t.l
	for {
		ch := l.Peek()
		if isSpace(ch) {
			l.Next()
			continue
		}
		pos := [2]int{l.Line(), l.Col()}
		start := l.Index()
		if ch == 0 {
			return Token{Type: TkEos, Pos: pos}, nil
		}
		var kind TokenType
		var value string
		var err error
		switch {
		case isNameStart(ch):
			byteStart := l.ByteIndex()
			for isNameStart(l.Peek()) || isDigit(l.Peek()) {
				l.Next()
			}
			value = l.Slice(byteStart, l.ByteIndex())
			kind = wordToTokenType(value)
		case isDigit(ch) || (ch == '.' && isDigit(l.PeekN(1))):
			value, kind, err = readNumber(l)
		case ch == '\'' || ch == '"':
			value, err = readShortString(l)
			kind = TkString
		case ch == '[' && longDelimiter(l, false) >= 0:
			value, err = readLongString(l, longDelimiter(l, false))
			kind = TkString
		case ch == '[' && l.PeekN(1) == '=':
			err = fmt.Errorf("invalid long string delimiter")
		case ch == '-' && l.PeekN(1) == '-':
			l.Next()
			l.Next()
			if level := longDelimiter(l, false); level >= 0 {
				err = skipLongComment(l, level)
			} else {
				for l.Peek() != 0 && l.Peek() != '\n' && l.Peek() != '\r' {
					l.Next()
				}
			}
			if err == nil {
				continue
			}
		default:
			value, kind, err = readOperator(l)
		}
		if err != nil {
			if t.filename != "" {
				return Token{}, fmt.Errorf("%s:%d:%d: %w", t.filename, pos[0], pos[1], err)
			}
			return Token{}, fmt.Errorf("%d:%d: %w", pos[0], pos[1], err)
		}
		return Token{Type: kind, Value: value, Len: l.Index() - start, Pos: pos}, nil
	}
}

func isDigit(ch byte) bool { return ch >= '0' && ch <= '9' }
func isNameStart(ch byte) bool {
	return ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}
func isSpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\v' || ch == '\f'
}

func readNumber(l *lex.Lexer) (string, TokenType, error) {
	start := l.ByteIndex()
	hexadecimal := l.Peek() == '0' && (l.PeekN(1) == 'x' || l.PeekN(1) == 'X')
	exponent := byte('e')
	if hexadecimal {
		exponent = 'p'
	}
	for {
		ch := l.Peek()
		if ch == exponent || ch == exponent-('a'-'A') {
			l.Next()
			if l.Peek() == '+' || l.Peek() == '-' {
				l.Next()
			}
		} else if isNameStart(ch) || isDigit(ch) || ch == '.' {
			l.Next()
		} else {
			break
		}
	}
	value := l.Slice(start, l.ByteIndex())
	kind, valid := numberType(value, hexadecimal)
	if !valid {
		return value, 0, fmt.Errorf("malformed number %q", value)
	}
	return value, kind, nil
}

func numberType(value string, hexadecimal bool) (TokenType, bool) {
	i := 0
	exponent := byte('e')
	if hexadecimal {
		i = 2
		exponent = 'p'
	}
	start := i
	for i < len(value) && numberDigit(value[i], hexadecimal) {
		i++
	}
	digits := i - start
	floating := false
	if i < len(value) && value[i] == '.' {
		floating = true
		i++
		start = i
		for i < len(value) && numberDigit(value[i], hexadecimal) {
			i++
		}
		digits += i - start
	}
	if digits == 0 {
		return 0, false
	}
	if i < len(value) && (value[i] == exponent || value[i] == exponent-('a'-'A')) {
		floating = true
		i++
		if i < len(value) && (value[i] == '+' || value[i] == '-') {
			i++
		}
		start = i
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
		if i == start {
			return 0, false
		}
	}
	if i != len(value) {
		return 0, false
	}
	if !floating && !hexadecimal {
		// Lua promotes overflowing decimal integers; hexadecimal integers wrap.
		digits := strings.TrimLeft(value, "0")
		floating = len(digits) > 19 || len(digits) == 19 && digits > "9223372036854775807"
	}
	if floating {
		return TkFloat, true
	}
	return TkInt, true
}

func numberDigit(ch byte, hexadecimal bool) bool {
	return ch >= '0' && ch <= '9' || hexadecimal && (ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F')
}

func readOperator(l *lex.Lexer) (string, TokenType, error) {
	start := l.ByteIndex()
	ch, next := l.Peek(), l.PeekByte(1)
	kind := TokenType(ch)
	width := 1
	switch ch {
	case '/':
		if next == '/' {
			kind, width = TkIDiv, 2
		}
	case '<':
		if next == '<' {
			kind, width = TkShl, 2
		} else if next == '=' {
			kind, width = TkLe, 2
		}
	case '>':
		if next == '>' {
			kind, width = TkShr, 2
		} else if next == '=' {
			kind, width = TkGe, 2
		}
	case '=':
		if next == '=' {
			kind, width = TkEq, 2
		}
	case '~':
		if next == '=' {
			kind, width = TkNe, 2
		}
	case ':':
		if next == ':' {
			kind, width = TkDbColon, 2
		}
	case '.':
		if next == '.' {
			kind, width = TkConcat, 2
			if l.PeekByte(2) == '.' {
				kind, width = TkDots, 3
			}
		}
	case '+', '-', '*', '%', '^', '#', '&', '|', '(', ')', '{', '}', '[', ']', ';', ',':
	default:
		return "", 0, fmt.Errorf("unexpected character %q", ch)
	}
	for i := 0; i < width; i++ {
		l.Next()
	}
	return l.Slice(start, l.ByteIndex()), kind, nil
}
