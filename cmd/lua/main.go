package main

import (
	"fmt"
	"os"

	"github.com/robogg133/glua/lex"
	"github.com/robogg133/glua/tokens"
)

func main() {
	stream := tokens.NewTokenizer(lex.NewLexer(`local bazz = [[buzz]]
print(bazz)`), "")
	for {
		token, err := stream.NextChecked()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("%s(%s) [%d:%d] - %d\n", tokenTypeName(token.Type), token.Value, token.Pos[0], token.Pos[1], token.Len)
		if token.Type == tokens.TkEos {
			break
		}
	}
}

func tokenTypeName(tokenType tokens.TokenType) string {
	names := map[tokens.TokenType]string{
		tokens.TkPlus: "TK_PLUS", tokens.TkMinus: "TK_MINUS", tokens.TkMul: "TK_MUL",
		tokens.TkDiv: "TK_DIV", tokens.TkMod: "TK_MOD", tokens.TkPow: "TK_POW",
		tokens.TkLen: "TK_LEN", tokens.TkBand: "TK_BAND", tokens.TkBor: "TK_BOR",
		tokens.TkBxor: "TK_BXOR", tokens.TkLt: "TK_LT", tokens.TkGt: "TK_GT",
		tokens.TkAssign: "TK_ASSIGN", tokens.TkLParen: "TK_LPAREN", tokens.TkRParen: "TK_RPAREN",
		tokens.TkLBrace: "TK_LBRACE", tokens.TkRBrace: "TK_RBRACE", tokens.TkLBracket: "TK_LBRACKET",
		tokens.TkRBracket: "TK_RBRACKET", tokens.TkSemi: "TK_SEMI", tokens.TkColon: "TK_COLON",
		tokens.TkComma: "TK_COMMA", tokens.TkDot: "TK_DOT", tokens.TkAnd: "TK_AND",
		tokens.TkBreak: "TK_BREAK", tokens.TkDo: "TK_DO", tokens.TkElse: "TK_ELSE",
		tokens.TkElseIf: "TK_ELSEIF", tokens.TkEnd: "TK_END", tokens.TkFalse: "TK_FALSE",
		tokens.TkFor: "TK_FOR", tokens.TkFunction: "TK_FUNCTION", tokens.TkGoto: "TK_GOTO",
		tokens.TkIf: "TK_IF", tokens.TkIn: "TK_IN", tokens.TkLocal: "TK_LOCAL",
		tokens.TkNil: "TK_NIL", tokens.TkNot: "TK_NOT", tokens.TkOr: "TK_OR",
		tokens.TkRepeat: "TK_REPEAT", tokens.TkReturn: "TK_RETURN", tokens.TkThen: "TK_THEN",
		tokens.TkTrue: "TK_TRUE", tokens.TkUntil: "TK_UNTIL", tokens.TkWhile: "TK_WHILE",
		tokens.TkIDiv: "TK_IDIV", tokens.TkShl: "TK_SHL", tokens.TkShr: "TK_SHR",
		tokens.TkEq: "TK_EQ", tokens.TkLe: "TK_LE", tokens.TkGe: "TK_GE", tokens.TkNe: "TK_NE",
		tokens.TkDbColon: "TK_DBCOLON", tokens.TkDots: "TK_DOTS", tokens.TkConcat: "TK_CONCAT",
		tokens.TkEos: "TK_EOS", tokens.TkFloat: "TK_FLOAT", tokens.TkInt: "TK_INT",
		tokens.TkName: "TK_NAME", tokens.TkString: "TK_STRING",
	}
	if name, ok := names[tokenType]; ok {
		return name
	}
	return "TK_UNKNOWN"
}
