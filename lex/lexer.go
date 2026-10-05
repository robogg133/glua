package lex

type Lexer struct {
	input     string
	idx       int
	line, col int
	newline   byte
}

// 0 means EOF

func NewLexer(input string) *Lexer {
	return &Lexer{input: input, line: 1, col: 1}
}

func (l *Lexer) Next() byte {
	if l.idx >= len(l.input) {
		return 0
	}
	ch := l.input[l.idx]
	l.idx++
	if ch == '\r' || ch == '\n' {
		if l.newline != 0 && ch != l.newline {
			l.newline = 0
		} else {
			l.line++
			l.newline = ch
		}
		l.col = 1
	} else {
		l.newline = 0
		l.col++
	}
	return ch
}

func (l *Lexer) Peek() byte { return l.PeekN(0) }

// PeekN uses byte offsets, including negative offsets, in constant time.
func (l *Lexer) PeekN(offset int) byte {
	if offset < -l.idx || offset >= len(l.input)-l.idx {
		return 0
	}
	return l.input[l.idx+offset]
}

func (l *Lexer) PeekByte(offset int) int { return int(l.PeekN(offset)) }

func (l *Lexer) Slice(start, end int) string { return l.input[start:end] }
func (l *Lexer) ByteIndex() int              { return l.idx }
func (l *Lexer) Index() int                  { return l.idx }
func (l *Lexer) Line() int                   { return l.line }
func (l *Lexer) Col() int                    { return l.col }
