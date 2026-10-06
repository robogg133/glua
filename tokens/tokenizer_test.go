package tokens

import (
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/robogg133/glua/lex"
)

func TestTokenizerStreaming(t *testing.T) {
	l := lex.NewLexer("local x=42 -- comment\n...")
	stream := NewTokenizer(l, "test.lua")
	if l.Index() != 0 {
		t.Fatal("constructor read input")
	}
	want := []Token{
		{TkLocal, "local", 5, [2]uint32{1, 1}},
		{TkName, "x", 1, [2]uint32{1, 7}},
		{TkAssign, "=", 1, [2]uint32{1, 8}},
		{TkInt, "42", 2, [2]uint32{1, 9}},
		{TkDots, "...", 3, [2]uint32{2, 1}},
		{TkEos, "", 0, [2]uint32{2, 4}},
	}
	for _, expected := range want {
		if got := stream.Peek(); got != expected {
			t.Fatalf("peek = %#v, want %#v", got, expected)
		}
		index := l.Index()
		if got := stream.Peek(); got != expected || l.Index() != index {
			t.Fatal("repeated peek advanced input")
		}
		if got := stream.Next(); got != expected || l.Index() != index {
			t.Fatalf("next after peek = %#v, want %#v", got, expected)
		}
	}
	for i := 0; i < 3; i++ {
		if stream.Next() != want[len(want)-1] || stream.Peek() != want[len(want)-1] {
			t.Fatal("unstable EOF")
		}
	}
}

func TestTokenizerLazyErrors(t *testing.T) {
	l := lex.NewLexer("x @")
	stream := NewTokenizer(l, "bad.lua")
	if got := stream.Next(); got.Type != TkName || got.Value != "x" || l.Index() != 1 {
		t.Fatal("next read beyond token")
	}
	_, err := stream.PeekChecked()
	if err == nil || !strings.Contains(err.Error(), "bad.lua:1:3:") {
		t.Fatalf("error = %v", err)
	}
	index := l.Index()
	for i := 0; i < 3; i++ {
		if _, nextErr := stream.NextChecked(); nextErr != err || l.Index() != index {
			t.Fatal("lexical error not stable")
		}
	}
	for _, read := range []func() Token{stream.Next, stream.Peek} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("expected panic on lexical error")
				}
			}()
			read()
		}()
	}
	partial, err := TokenizeChecked("x @")
	if err == nil || len(partial) != 1 || partial[0].Value != "x" {
		t.Fatalf("partial = %#v, err = %v", partial, err)
	}
}

func TestTokenizerNextWithoutPeek(t *testing.T) {
	stream := NewTokenizer(lex.NewLexer("a+1"), "")
	for _, kind := range []TokenType{TkName, TkPlus, TkInt, TkEos} {
		token, err := stream.NextChecked()
		if err != nil || token.Type != kind {
			t.Fatalf("got %#v, %v; want %v", token, err, kind)
		}
	}
}

func TestAllTokens(t *testing.T) {
	input := "+ - * / % ^ # & | ~ < > = ( ) { } [ ] ; : , . and break do else elseif end false for function goto if in local nil not or repeat return then true until while // << >> == <= >= ~= :: ... .. 3.14 42 identifier 'text'"
	expected := []TokenType{TkPlus, TkMinus, TkMul, TkDiv, TkMod, TkPow, TkLen, TkBand, TkBor, TkBxor, TkLt, TkGt, TkAssign, TkLParen, TkRParen, TkLBrace, TkRBrace, TkLBracket, TkRBracket, TkSemi, TkColon, TkComma, TkDot, TkAnd, TkBreak, TkDo, TkElse, TkElseIf, TkEnd, TkFalse, TkFor, TkFunction, TkGoto, TkIf, TkIn, TkLocal, TkNil, TkNot, TkOr, TkRepeat, TkReturn, TkThen, TkTrue, TkUntil, TkWhile, TkIDiv, TkShl, TkShr, TkEq, TkLe, TkGe, TkNe, TkDbColon, TkDots, TkConcat, TkFloat, TkInt, TkName, TkString, TkEos}
	stream, err := TokenizeChecked(input)
	if err != nil {
		t.Fatal(err)
	}
	var actual []TokenType
	for _, token := range stream {
		actual = append(actual, token.Type)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("types = %v, want %v", actual, expected)
	}
}

// Keep the old regexp-based validation as an independent test oracle.
func FuzzNumberType(f *testing.F) {
	decimal := regexp.MustCompile(`^(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
	hex := regexp.MustCompile(`^0[xX](?:[0-9a-fA-F]+(?:\.[0-9a-fA-F]*)?|\.[0-9a-fA-F]+)(?:[pP][+-]?[0-9]+)?$`)
	for _, input := range []string{"0", "1.", ".5", "1e-3", "1..2", "0x1.8p2", "0x", "0xE", "9223372036854775808", "00009223372036854775807", "1e999999"} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		hexadecimal := strings.HasPrefix(input, "0x") || strings.HasPrefix(input, "0X")
		valid := decimal.MatchString(input)
		floating := strings.ContainsAny(input, ".eE")
		if hexadecimal {
			valid = hex.MatchString(input)
			floating = strings.ContainsAny(input, ".pP")
		}
		kind, gotValid := numberType(input, hexadecimal)
		if gotValid != valid {
			t.Fatalf("validity of %q = %v, want %v", input, gotValid, valid)
		}
		if !valid {
			return
		}
		if !floating && !hexadecimal {
			digits := strings.TrimLeft(input, "0")
			if digits == "" {
				digits = "0"
			}
			_, err := strconv.ParseInt(digits, 10, 64)
			floating = err != nil
		}
		want := TkInt
		if floating {
			want = TkFloat
		}
		if kind != want {
			t.Fatalf("type of %q = %v, want %v", input, kind, want)
		}
	})
}

func TestNumbers(t *testing.T) {
	cases := []struct {
		input string
		kind  TokenType
	}{
		{"0", TkInt}, {"42", TkInt}, {"00042", TkInt}, {"0x2A", TkInt}, {"0XE", TkInt},
		{"0xffffffffffffffff", TkInt}, {"0x10000000000000000", TkInt},
		{"9223372036854775807", TkInt}, {"9223372036854775808", TkFloat},
		{"00009223372036854775807", TkInt},
		{"3.14", TkFloat}, {".5", TkFloat}, {"1.", TkFloat}, {"1e3", TkFloat}, {"1E-3", TkFloat}, {"1e+3", TkFloat},
		{"0x1.8p1", TkFloat}, {"0X.8P-1", TkFloat}, {"0x1p+3", TkFloat}, {"0x1.8", TkFloat},
		{"0x1.", TkFloat}, {"1e99999", TkFloat},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			stream, err := TokenizeChecked(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			want := Token{Type: tc.kind, Value: tc.input, Len: len(tc.input), Pos: [2]uint32{1, 1}}
			if len(stream) != 2 || stream[0] != want || stream[1].Type != TkEos {
				t.Fatalf("tokens = %#v, want %#v and EOF", stream, want)
			}
		})
	}
}

func TestPositionsAndComments(t *testing.T) {
	input := "local x=42-- comment\r\n\t&~x<<2\n\r--[==[ ignored ]=] ]==]\r\n'á'"
	stream, err := TokenizeChecked(input)
	if err != nil {
		t.Fatal(err)
	}
	want := []Token{
		{TkLocal, "local", 5, [2]uint32{1, 1}}, {TkName, "x", 1, [2]uint32{1, 7}},
		{TkAssign, "=", 1, [2]uint32{1, 8}}, {TkInt, "42", 2, [2]uint32{1, 9}},
		{TkBand, "&", 1, [2]uint32{2, 2}}, {TkBxor, "~", 1, [2]uint32{2, 3}},
		{TkName, "x", 1, [2]uint32{2, 4}}, {TkShl, "<<", 2, [2]uint32{2, 5}},
		{TkInt, "2", 1, [2]uint32{2, 7}}, {TkString, "á", 4, [2]uint32{4, 1}},
		{TkEos, "", 0, [2]uint32{4, 5}},
	}
	if !reflect.DeepEqual(stream, want) {
		t.Fatalf("got %#v\nwant %#v", stream, want)
	}
	for _, input := range []string{"", " ", "--", "-- comment", "--[not long", "--[=[comment]=]"} {
		stream, err := TokenizeChecked(input)
		if err != nil || len(stream) != 1 || stream[0].Type != TkEos {
			t.Errorf("%q: %#v, %v", input, stream, err)
		}
	}
}

func TestStringSourceLengths(t *testing.T) {
	input := "\"a\\n\" [==[\r\nb\r\nc]==] ''"
	stream, err := TokenizeChecked(input)
	if err != nil {
		t.Fatal(err)
	}
	if stream[0].Value != "a\n" || stream[0].Len != 5 || stream[1].Value != "b\nc" || stream[1].Len != 14 || stream[2].Value != "" || stream[2].Len != 2 {
		t.Fatalf("%#v", stream)
	}
}

func TestMalformedInput(t *testing.T) {
	for _, input := range []string{"0x", "1e", "1e+", "0x1p", "0xG", "123abc", "1..2", "0b10", "1_000", "'unfinished", "[[unfinished", "--[[unfinished", "--[=[unfinished", "[=invalid", "\"\\q\"", "\"\\256\"", "@", "Olá"} {
		t.Run(input, func(t *testing.T) {
			_, err := TokenizeChecked(input)
			if err == nil || !strings.Contains(err.Error(), ":") {
				t.Fatalf("expected positioned error for %q, got %v", input, err)
			}
		})
	}
}

func FuzzTokenizeChecked(f *testing.F) {
	for _, input := range []string{"", "local x=0x1.8p1", "-- comment", "--[=[unterminated", "'\\\\u{7fffffff}'", "1..2", "... & | ~ << >>", "[[\r\ntext]]"} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		stream, err := TokenizeChecked(input)
		for _, token := range stream {
			if token.Len < 0 || token.Pos[0] < 1 || token.Pos[1] < 1 {
				t.Fatalf("invalid token metadata: %#v", token)
			}
		}
		if err == nil && (len(stream) == 0 || stream[len(stream)-1].Type != TkEos) {
			t.Fatal("missing EOF")
		}
	})
}

func TestAdjacentTokens(t *testing.T) {
	stream, err := TokenizeChecked("f(x,1)..'s'...-2>>1<=3~=4//2::label::")
	if err != nil {
		t.Fatal(err)
	}
	want := []TokenType{TkName, TkLParen, TkName, TkComma, TkInt, TkRParen, TkConcat, TkString, TkDots, TkMinus, TkInt, TkShr, TkInt, TkLe, TkInt, TkNe, TkInt, TkIDiv, TkInt, TkDbColon, TkName, TkDbColon, TkEos}
	var got []TokenType
	for _, token := range stream {
		got = append(got, token.Type)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}
