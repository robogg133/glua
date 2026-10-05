package lex

import "testing"

func FuzzLexerLookahead(f *testing.F) {
	for _, input := range []string{"", "abc", "á+\nβ", "\r\n\r", "a\xff\xc0\xafβ", "\ufffd"} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		want := []byte(input)
		l := NewLexer(input)
		for i := 0; i <= len(want); i++ {
			if l.Index() != i || l.ByteIndex() != i {
				t.Fatal("incorrect source offset")
			}
			for offset := -3; offset <= 3; offset++ {
				expected := byte(0)
				if at := i + offset; at >= 0 && at < len(want) {
					expected = want[at]
				}
				if got := l.PeekN(offset); got != expected {
					t.Fatalf("PeekN(%d) at %d = %U, want %U", offset, i, got, expected)
				}
			}
			if i < len(want) {
				if l.Peek() != want[i] || l.Next() != want[i] {
					t.Fatal("source byte changed")
				}
			} else if l.Next() != 0 {
				t.Fatal("missing EOF")
			}
		}
	})
}

func TestAllByteValues(t *testing.T) {
	var input [256]byte
	for i := range input {
		input[i] = byte(i)
	}
	l := NewLexer(string(input[:]))
	for i := range input {
		if l.Peek() != byte(i) || l.Next() != byte(i) {
			t.Fatalf("byte %d changed", i)
		}
	}
	if l.Next() != 0 || l.Index() != 256 {
		t.Fatal("incorrect EOF")
	}
}

func TestByteAccess(t *testing.T) {
	l := NewLexer("á+")
	if l.PeekByte(0) != 0xc3 || l.PeekByte(1) != 0xa1 || l.PeekByte(3) != 0 || l.PeekByte(-1) != 0 {
		t.Fatal("incorrect byte lookahead")
	}
	if l.Next() != 0xc3 || l.Index() != 1 || l.Col() != 2 {
		t.Fatal("first UTF-8 byte not consumed separately")
	}
	if l.Next() != 0xa1 || l.ByteIndex() != 2 || l.Index() != 2 || l.Slice(0, l.Index()) != "á" {
		t.Fatal("incorrect byte offsets")
	}
}

func TestNextPositions(t *testing.T) {
	for _, tc := range []struct {
		input string
		lines int
	}{
		{"", 1}, {"x", 1}, {"\n", 2}, {"\r", 2}, {"\r\n", 2}, {"\n\r", 2},
		{"\r\n\r", 3}, {"\n\r\n", 3}, {"\r\r", 3}, {"\n\n", 3},
		{"\r\n\r\n", 3}, {"\n\r\n\r", 3},
	} {
		l := NewLexer(tc.input)
		for l.Next() != 0 {
		}
		wantCol := 1
		if tc.input == "x" {
			wantCol = 2
		}
		if l.Line() != tc.lines || l.Col() != wantCol {
			t.Errorf("%q: position %d:%d, want %d:%d", tc.input, l.Line(), l.Col(), tc.lines, wantCol)
		}
		index, line, col := l.Index(), l.Line(), l.Col()
		for i := 0; i < 3; i++ {
			if l.Next() != 0 || l.Peek() != 0 || l.PeekN(1) != 0 {
				t.Errorf("%q: EOF unstable", tc.input)
			}
		}
		if l.Index() != index || l.Line() != line || l.Col() != col {
			t.Error("EOF changed position")
		}
	}
}

func TestByteColumnsAndLookahead(t *testing.T) {
	l := NewLexer("á+\nβ")
	if l.Peek() != 0xc3 || l.PeekN(1) != 0xa1 || l.PeekN(2) != '+' || l.Index() != 0 {
		t.Fatal("incorrect byte lookahead")
	}
	l.Next()
	l.Next()
	if l.Col() != 3 || l.Index() != 2 {
		t.Fatal("column is not byte-based")
	}
	l.Next()
	l.Next()
	if l.Line() != 2 || l.Col() != 1 || l.Peek() != 0xce {
		t.Fatal("incorrect newline position")
	}
}
