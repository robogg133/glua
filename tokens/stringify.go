package tokens

import "strconv"

func (tk TokenType) String() string {
	switch tk {
	case TkPlus, TkMinus, TkMul, TkDiv, TkMod, TkPow, TkLen,
		TkBand, TkBor, TkBxor, TkLt, TkGt, TkAssign,
		TkLParen, TkRParen, TkLBrace, TkRBrace, TkLBracket, TkRBracket,
		TkSemi, TkColon, TkComma, TkDot:
		return string(rune(tk))
	}
	names := [...]string{
		TkAnd - TkAnd:      "and",
		TkBreak - TkAnd:    "break",
		TkDo - TkAnd:       "do",
		TkElse - TkAnd:     "else",
		TkElseIf - TkAnd:   "elseif",
		TkEnd - TkAnd:      "end",
		TkFalse - TkAnd:    "false",
		TkFor - TkAnd:      "for",
		TkFunction - TkAnd: "function",
		TkGoto - TkAnd:     "goto",
		TkIf - TkAnd:       "if",
		TkIn - TkAnd:       "in",
		TkLocal - TkAnd:    "local",
		TkNil - TkAnd:      "nil",
		TkNot - TkAnd:      "not",
		TkOr - TkAnd:       "or",
		TkRepeat - TkAnd:   "repeat",
		TkReturn - TkAnd:   "return",
		TkThen - TkAnd:     "then",
		TkTrue - TkAnd:     "true",
		TkUntil - TkAnd:    "until",
		TkWhile - TkAnd:    "while",
		TkIDiv - TkAnd:     "//",
		TkShl - TkAnd:      "<<",
		TkShr - TkAnd:      ">>",
		TkEq - TkAnd:       "==",
		TkLe - TkAnd:       "<=",
		TkGe - TkAnd:       ">=",
		TkNe - TkAnd:       "~=",
		TkDbColon - TkAnd:  "::",
		TkDots - TkAnd:     "...",
		TkConcat - TkAnd:   "..",
		TkEos - TkAnd:      "<eof>",
		TkFloat - TkAnd:    "float",
		TkInt - TkAnd:      "integer",
		TkName - TkAnd:     "name",
		TkString - TkAnd:   "string",
		TkGlobal - TkAnd:   "global",
	}
	if tk >= TkAnd && int(tk-TkAnd) < len(names) {
		return names[tk-TkAnd]
	}
	return "TokenType(" + strconv.Itoa(int(tk)) + ")"
}
