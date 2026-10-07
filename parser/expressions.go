package parser

import "github.com/robogg133/glua/tokens"

// Binding powers match Lua's subexpr priorities; concat and power associate right.
var binaryOperators = [...]struct {
	kind        Tag
	left, right int
}{
	tokens.TkOr:     {TagOr, 1, 1},
	tokens.TkAnd:    {TagAnd, 2, 2},
	tokens.TkEq:     {TagEqual, 3, 3},
	tokens.TkNe:     {TagNotEqual, 3, 3},
	tokens.TkLt:     {TagLess, 3, 3},
	tokens.TkLe:     {TagLessEqual, 3, 3},
	tokens.TkGt:     {TagGreater, 3, 3},
	tokens.TkGe:     {TagGreaterEqual, 3, 3},
	tokens.TkBor:    {TagBitOr, 4, 4},
	tokens.TkBxor:   {TagBitXor, 5, 5},
	tokens.TkBand:   {TagBitAnd, 6, 6},
	tokens.TkShl:    {TagShiftLeft, 7, 7},
	tokens.TkShr:    {TagShiftRight, 7, 7},
	tokens.TkConcat: {TagConcat, 9, 8},
	tokens.TkPlus:   {TagAdd, 10, 10},
	tokens.TkMinus:  {TagSubtract, 10, 10},
	tokens.TkMul:    {TagMultiply, 11, 11},
	tokens.TkMod:    {TagModulo, 11, 11},
	tokens.TkDiv:    {TagDivide, 11, 11},
	tokens.TkIDiv:   {TagIntegerDivide, 11, 11},
	tokens.TkPow:    {TagPower, 14, 13},
}

func (a *AST) expr(limit int) uint32 {
	a.enter()
	defer a.leave()

	start := a.cur
	var result uint32
	switch start.Type {
	case tokens.TkNot, tokens.TkMinus, tokens.TkBxor, tokens.TkLen:
		var kind Tag
		switch start.Type {
		case tokens.TkNot:
			kind = TagNot
		case tokens.TkMinus:
			kind = TagNegate
		case tokens.TkBxor:
			kind = TagBitwiseNot
		case tokens.TkLen:
			kind = TagLength
		}
		a.advance()
		result = a.node(kind, a.expr(12), 0, start.Pos)
	case tokens.TkNil:
		a.advance()
		result = a.leaf(TagNil, start)
	case tokens.TkFalse, tokens.TkTrue:
		a.advance()
		result = a.leaf(TagBoolean, start)
	case tokens.TkInt:
		a.advance()
		result = a.leaf(TagInteger, start)
	case tokens.TkFloat:
		a.advance()
		result = a.leaf(TagFloat, start)
	case tokens.TkString:
		a.advance()
		result = a.leaf(TagString, start)
	case tokens.TkDots:
		if !a.vararg {
			a.fail("cannot use '...' outside a vararg function")
		}
		a.advance()
		result = a.leaf(TagVararg, start)
	case tokens.TkFunction:
		a.advance()
		result = a.functionBody(start, false)
	case tokens.TkLBrace:
		result = a.table()
	default:
		result = a.prefix()
	}

	for int(a.cur.Type) < len(binaryOperators) {
		op := binaryOperators[a.cur.Type]
		if op.left == 0 || op.left <= limit {
			break
		}
		token := a.cur
		a.advance()
		right := a.expr(op.right)
		result = a.node(op.kind, result, right, token.Pos)
	}
	return result
}

func (a *AST) exprList() uint32 {
	pos := a.cur.Pos
	items := []uint32{a.expr(0)}
	for a.accept(tokens.TkComma) {
		items = append(items, a.expr(0))
	}
	return a.list(items, pos)
}

func (a *AST) prefix() uint32 {
	start := a.cur
	var result uint32
	switch start.Type {
	case tokens.TkName:
		a.advance()
		result = a.leaf(TagNameReference, start)
	case tokens.TkLParen:
		a.advance()
		inner := a.expr(0)
		a.close(tokens.TkRParen, start)
		result = a.node(TagParenthesized, inner, 0, start.Pos)
	default:
		a.fail("unexpected symbol")
		return 0
	}

	for {
		start = a.cur
		switch start.Type {
		case tokens.TkDot:
			a.advance()
			result = a.node(TagFieldAccess, result, a.name(), start.Pos)
		case tokens.TkLBracket:
			a.advance()
			index := a.expr(0)
			a.close(tokens.TkRBracket, start)
			result = a.node(TagIndexAccess, result, index, start.Pos)
		case tokens.TkColon:
			a.advance()
			method := a.name()
			result = a.args(result, method)
		case tokens.TkLParen, tokens.TkLBrace, tokens.TkString:
			result = a.args(result, 0)
		default:
			return result
		}
	}
}

func (a *AST) args(callee uint32, method uint32) uint32 {
	start := a.cur
	kind := TagCallExpression
	var arguments uint32
	switch start.Type {
	case tokens.TkLParen:
		a.advance()
		if a.cur.Type == tokens.TkRParen {
			arguments = a.list(nil, start.Pos)
		} else {
			arguments = a.exprList()
		}
		a.close(tokens.TkRParen, start)
	case tokens.TkLBrace:
		kind = TagTableArgumentCall
		arguments = a.list([]uint32{a.table()}, start.Pos)
	case tokens.TkString:
		kind = TagStringArgumentCall
		a.advance()
		arguments = a.list([]uint32{a.leaf(TagString, start)}, start.Pos)
	default:
		a.fail("function arguments expected")
		return 0
	}
	if method != 0 {
		kind = TagMethodCallExpression
		callee = a.node(TagPair, callee, method, a.Nodes[method].TokenPosition)
	}
	return a.node(kind, callee, arguments, start.Pos)
}

func (a *AST) table() uint32 {
	start := a.expect(tokens.TkLBrace)
	var fields []uint32
	for a.cur.Type != tokens.TkRBrace {
		pos := a.cur.Pos
		var field uint32
		switch {
		case a.accept(tokens.TkLBracket):
			key := a.expr(0)
			a.expect(tokens.TkRBracket)
			a.expect(tokens.TkAssign)
			field = a.node(TagKeyField, key, a.expr(0), pos)
		default:
			named := false
			if a.cur.Type == tokens.TkName {
				next, err := a.t.PeekChecked()
				if err != nil {
					a.lexical(err)
				}
				named = next.Type == tokens.TkAssign
			}
			if named {
				name := a.name()
				a.expect(tokens.TkAssign)
				field = a.node(TagNameField, name, a.expr(0), pos)
			} else {
				field = a.node(TagArrayField, a.expr(0), 0, pos)
			}
		}
		fields = append(fields, field)
		if !a.accept(tokens.TkComma) && !a.accept(tokens.TkSemi) {
			break
		}
	}
	a.close(tokens.TkRBrace, start)
	return a.node(TagTableConstructor, a.list(fields, start.Pos), 0, start.Pos)
}
