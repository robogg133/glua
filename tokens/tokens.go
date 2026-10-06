package tokens

type TokenType uint8

type Token struct {
	Type  TokenType
	Value string
	Len   int
	Pos   [2]uint32
}

const (
	TkPlus     TokenType = '+' // +
	TkMinus    TokenType = '-' // -
	TkMul      TokenType = '*' // *
	TkDiv      TokenType = '/' // /
	TkMod      TokenType = '%' // %
	TkPow      TokenType = '^' // ^
	TkLen      TokenType = '#' // #
	TkBand     TokenType = '&' // &
	TkBor      TokenType = '|' // |
	TkBxor     TokenType = '~' // ~
	TkLt       TokenType = '<' // <
	TkGt       TokenType = '>' // >
	TkAssign   TokenType = '=' // =
	TkLParen   TokenType = '(' // (
	TkRParen   TokenType = ')' // )
	TkLBrace   TokenType = '{' // {
	TkRBrace   TokenType = '}' // }
	TkLBracket TokenType = '[' // [
	TkRBracket TokenType = ']' // ]
	TkSemi     TokenType = ';' // ;
	TkColon    TokenType = ':' // :
	TkComma    TokenType = ',' // ,
	TkDot      TokenType = '.' // .
)

const (
	TkAnd      TokenType = 128 + iota // and
	TkBreak                           // break
	TkDo                              // do
	TkElse                            // else
	TkElseIf                          // elseif
	TkEnd                             // end
	TkFalse                           // false
	TkFor                             // for
	TkFunction                        // function
	TkGoto                            // goto
	TkIf                              // if
	TkIn                              // in
	TkLocal                           // local
	TkNil                             // nil
	TkNot                             // not
	TkOr                              // or
	TkRepeat                          // repeat
	TkReturn                          // return
	TkThen                            // then
	TkTrue                            // true
	TkUntil                           // until
	TkWhile                           // while

	TkIDiv    // //
	TkShl     // <<
	TkShr     // >>
	TkEq      // ==
	TkLe      // <=
	TkGe      // >=
	TkNe      // ~=
	TkDbColon // ::
	TkDots    // ...
	TkConcat  // ..
	TkEos     // <eof>
	TkFloat   // 3.14 | 1e3 | 0x1.8p1
	TkInt     // 42 | 0x2A
	TkName    // i | _internal42
	TkString  // "foo" | 'bar' | [[baz]] | [=[buzz]=]
)

func wordToTokenType(word string) TokenType {
	switch word {
	case "and":
		return TkAnd
	case "break":
		return TkBreak
	case "do":
		return TkDo
	case "else":
		return TkElse
	case "elseif":
		return TkElseIf
	case "end":
		return TkEnd
	case "false":
		return TkFalse
	case "for":
		return TkFor
	case "function":
		return TkFunction
	case "goto":
		return TkGoto
	case "if":
		return TkIf
	case "in":
		return TkIn
	case "local":
		return TkLocal
	case "nil":
		return TkNil
	case "not":
		return TkNot
	case "or":
		return TkOr
	case "repeat":
		return TkRepeat
	case "return":
		return TkReturn
	case "then":
		return TkThen
	case "true":
		return TkTrue
	case "until":
		return TkUntil
	case "while":
		return TkWhile
	default:
		return TkName
	}
}
