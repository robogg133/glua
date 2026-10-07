package tokens

import "testing"

func TestGlobalKeyword(t *testing.T) {
	got, err := TokenizeChecked("global globalx _global Global")
	if err != nil {
		t.Fatal(err)
	}
	want := []Token{
		{TkGlobal, "global", 6, [2]uint32{1, 1}},
		{TkName, "globalx", 7, [2]uint32{1, 8}},
		{TkName, "_global", 7, [2]uint32{1, 16}},
		{TkName, "Global", 6, [2]uint32{1, 24}},
		{TkEos, "", 0, [2]uint32{1, 30}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d tokens, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestTokenNumbersAndStrings(t *testing.T) {
	punctuation := []struct {
		kind TokenType
		text byte
	}{
		{TkPlus, '+'}, {TkMinus, '-'}, {TkMul, '*'}, {TkDiv, '/'},
		{TkMod, '%'}, {TkPow, '^'}, {TkLen, '#'}, {TkBand, '&'},
		{TkBor, '|'}, {TkBxor, '~'}, {TkLt, '<'}, {TkGt, '>'},
		{TkAssign, '='}, {TkLParen, '('}, {TkRParen, ')'},
		{TkLBrace, '{'}, {TkRBrace, '}'}, {TkLBracket, '['},
		{TkRBracket, ']'}, {TkSemi, ';'}, {TkColon, ':'},
		{TkComma, ','}, {TkDot, '.'},
	}
	for _, test := range punctuation {
		if byte(test.kind) != test.text || test.kind.String() != string(test.text) {
			t.Errorf("token %d = %q, want %d (%q)", test.kind, test.kind.String(), test.text, string(test.text))
		}
	}

	// This order fixes every pre-existing non-ASCII token at its original number.
	named := []struct {
		kind TokenType
		text string
	}{
		{TkAnd, "and"}, {TkBreak, "break"}, {TkDo, "do"},
		{TkElse, "else"}, {TkElseIf, "elseif"}, {TkEnd, "end"},
		{TkFalse, "false"}, {TkFor, "for"}, {TkFunction, "function"},
		{TkGoto, "goto"}, {TkIf, "if"}, {TkIn, "in"},
		{TkLocal, "local"}, {TkNil, "nil"}, {TkNot, "not"},
		{TkOr, "or"}, {TkRepeat, "repeat"}, {TkReturn, "return"},
		{TkThen, "then"}, {TkTrue, "true"}, {TkUntil, "until"},
		{TkWhile, "while"}, {TkIDiv, "//"}, {TkShl, "<<"},
		{TkShr, ">>"}, {TkEq, "=="}, {TkLe, "<="},
		{TkGe, ">="}, {TkNe, "~="}, {TkDbColon, "::"},
		{TkDots, "..."}, {TkConcat, ".."}, {TkEos, "<eof>"},
		{TkFloat, "float"}, {TkInt, "integer"}, {TkName, "name"},
		{TkString, "string"}, {TkGlobal, "global"},
	}
	for i, test := range named {
		if int(test.kind) != 128+i {
			t.Errorf("%s number = %d, want %d", test.text, test.kind, 128+i)
		}
		if got := test.kind.String(); got != test.text {
			t.Errorf("token %d string = %q, want %q", test.kind, got, test.text)
		}
	}
	for kind, want := range map[TokenType]string{0: "TokenType(0)", 255: "TokenType(255)"} {
		if got := kind.String(); got != want {
			t.Errorf("unknown token %d string = %q, want %q", kind, got, want)
		}
	}
}
