package parser

import (
	"fmt"

	"github.com/robogg133/glua/tokens"
)

type parseFailure struct{ err error }

// SyntaxError reports a Lua-style diagnostic and retains its column for editors.
type SyntaxError struct {
	Filename string
	Line     uint32
	Column   uint32
	Message  string
	Near     string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("%s:%d: %s near %s", e.Filename, e.Line, e.Message, e.Near)
}

type ErrExpected struct {
	line      uint32
	filename  string
	near      string
	expecting tokens.TokenType
}

func (e *ErrExpected) Error() string {
	return fmt.Sprintf("%s:%d: %s expected near %s", e.filename, e.line, quotedToken(e.expecting), e.near)
}
func quotedToken(kind tokens.TokenType) string {
	switch kind {
	case tokens.TkEos:
		return "<eof>"
	case tokens.TkName, tokens.TkInt, tokens.TkFloat, tokens.TkString:
		return "<" + kind.String() + ">"
	default:
		return "'" + kind.String() + "'"
	}
}
func nearToken(tk tokens.Token) string {
	if tk.Type == tokens.TkEos {
		return "<eof>"
	}
	if tk.Type == tokens.TkString {
		return "<string>"
	}
	return "'" + tk.Value + "'"
}
func (a *AST) unexpected(tk tokens.Token, expected tokens.TokenType) error {
	return &ErrExpected{line: tk.Pos[0], filename: a.t.Filename(), near: nearToken(tk), expecting: expected}
}
func (a *AST) fail(message string) {
	panic(parseFailure{&SyntaxError{Filename: a.t.Filename(), Line: a.cur.Pos[0], Column: a.cur.Pos[1], Message: message, Near: nearToken(a.cur)}})
}
func (a *AST) lexical(err error) { panic(parseFailure{err}) }
