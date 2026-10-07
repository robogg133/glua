package parser

import (
	"fmt"

	"github.com/robogg133/glua/tokens"
)

// Node references are indices into AST.Nodes; zero means no node.
// TokenPosition is the source position [line, column], not a token index.
type Node struct {
	Left          uint32
	Right         uint32
	TokenPosition [2]uint32
	Kind          Tag
}

// AST owns the arena and a parallel slice containing names and literal values.
// Values preserves numeric spelling and decoded string bytes from the tokenizer.
type AST struct {
	Nodes  []Node
	Values []string
	Root   uint32
	t      *tokens.Tokenizer
	cur    tokens.Token
	vararg bool
	loops  int
	depth  int
	done   bool
	err    error
}

func NewAst(t *tokens.Tokenizer) *AST {
	return &AST{Nodes: []Node{{Kind: TagNone}}, Values: []string{""}, t: t, vararg: true}
}

// Next parses the entire chunk. Repeated calls return the same result.
// On failure Root is zero; the incomplete arena must not be executed.
func (a *AST) Next() (err error) {
	if a.done {
		return a.err
	}
	a.done = true
	defer func() {
		if r := recover(); r != nil {
			if failure, ok := r.(parseFailure); ok {
				err = failure.err
			} else {
				panic(r)
			}
		}
		a.err = err
		if err != nil {
			a.Root = 0
		}
	}()
	a.advance()
	pos := a.cur.Pos
	body := a.block()
	a.expect(tokens.TkEos)
	a.Root = a.node(TagChunk, body, 0, pos)
	return a.validate()
}

func (a *AST) node(kind Tag, left, right uint32, pos [2]uint32) uint32 {
	index := uint32(len(a.Nodes))
	a.Nodes = append(a.Nodes, Node{Left: left, Right: right, TokenPosition: pos, Kind: kind})
	a.Values = append(a.Values, "")
	return index
}
func (a *AST) leaf(kind Tag, tk tokens.Token) uint32 {
	n := a.node(kind, 0, 0, tk.Pos)
	a.Values[n] = tk.Value
	return n
}
func (a *AST) list(items []uint32, pos [2]uint32) uint32 {
	var next uint32
	for i := len(items) - 1; i >= 0; i-- {
		next = a.node(TagList, items[i], next, pos)
	}
	return next
}
func (a *AST) advance() {
	tk, err := a.t.NextChecked()
	if err != nil {
		a.lexical(err)
	}
	a.cur = tk
}
func (a *AST) accept(kind tokens.TokenType) bool {
	if a.cur.Type != kind {
		return false
	}
	a.advance()
	return true
}
func (a *AST) expect(kind tokens.TokenType) tokens.Token {
	tk := a.cur
	if tk.Type != kind {
		panic(parseFailure{a.unexpected(tk, kind)})
	}
	a.advance()
	return tk
}
func (a *AST) name() uint32 { return a.leaf(TagIdentifier, a.expect(tokens.TkName)) }
func (a *AST) enter() {
	a.depth++
	if a.depth > 200 {
		a.fail("too many syntax levels")
	}
}
func (a *AST) leave() { a.depth-- }
func (a *AST) close(kind tokens.TokenType, open tokens.Token) {
	if a.cur.Type != kind && a.cur.Pos[0] != open.Pos[0] {
		a.fail(fmt.Sprintf("%s expected (to close %s at line %d)", quotedToken(kind), quotedToken(open.Type), open.Pos[0]))
	}
	a.expect(kind)
}
func blockEnd(kind tokens.TokenType) bool {
	switch kind {
	case tokens.TkEos, tokens.TkEnd, tokens.TkElse, tokens.TkElseIf, tokens.TkUntil:
		return true
	}
	return false
}
func (a *AST) block() uint32 {
	a.enter()
	defer a.leave()
	pos := a.cur.Pos
	var statements []uint32
	for !blockEnd(a.cur.Type) {
		n := a.statement()
		statements = append(statements, n)
		if a.Nodes[n].Kind == TagReturn {
			break
		}
	}
	return a.node(TagBlock, a.list(statements, pos), 0, pos)
}
func (a *AST) statement() uint32 {
	tk := a.cur
	switch tk.Type {
	case tokens.TkSemi:
		a.advance()
		return a.node(TagEmpty, 0, 0, tk.Pos)
	case tokens.TkLocal, tokens.TkGlobal:
		return a.declaration()
	case tokens.TkFunction:
		a.advance()
		base := a.name()
		var fields []uint32
		for a.accept(tokens.TkDot) {
			fields = append(fields, a.name())
		}
		var method uint32
		if a.accept(tokens.TkColon) {
			method = a.name()
		}
		path := a.node(TagNamePath, base, a.node(TagPair, a.list(fields, tk.Pos), method, tk.Pos), tk.Pos)
		kind := TagFunctionDeclaration
		if method != 0 {
			kind = TagMethodDeclaration
		}
		return a.node(kind, path, a.functionBody(tk, method != 0), tk.Pos)
	case tokens.TkDo:
		a.advance()
		body := a.block()
		a.close(tokens.TkEnd, tk)
		return a.node(TagDoBlock, body, 0, tk.Pos)
	case tokens.TkWhile:
		a.advance()
		cond := a.expr(0)
		a.expect(tokens.TkDo)
		a.loops++
		body := a.block()
		a.loops--
		a.close(tokens.TkEnd, tk)
		return a.node(TagWhile, cond, body, tk.Pos)
	case tokens.TkRepeat:
		a.advance()
		a.loops++
		body := a.block()
		a.loops--
		a.close(tokens.TkUntil, tk)
		cond := a.expr(0)
		return a.node(TagRepeatUntil, body, cond, tk.Pos)
	case tokens.TkIf:
		a.advance()
		var branches []uint32
		for {
			pos := a.cur.Pos
			cond := a.expr(0)
			a.expect(tokens.TkThen)
			body := a.block()
			branches = append(branches, a.node(TagBranch, cond, body, pos))
			if !a.accept(tokens.TkElseIf) {
				break
			}
		}
		var otherwise uint32
		if a.accept(tokens.TkElse) {
			otherwise = a.block()
		}
		a.close(tokens.TkEnd, tk)
		return a.node(TagIf, a.list(branches, tk.Pos), otherwise, tk.Pos)
	case tokens.TkFor:
		return a.forStatement()
	case tokens.TkReturn:
		a.advance()
		var values uint32
		if !blockEnd(a.cur.Type) && a.cur.Type != tokens.TkSemi {
			values = a.exprList()
		}
		a.accept(tokens.TkSemi)
		return a.node(TagReturn, values, 0, tk.Pos)
	case tokens.TkBreak:
		if a.loops == 0 {
			a.fail("break outside loop")
		}
		a.advance()
		return a.node(TagBreak, 0, 0, tk.Pos)
	case tokens.TkGoto:
		a.advance()
		return a.node(TagGoto, a.name(), 0, tk.Pos)
	case tokens.TkDbColon:
		a.advance()
		name := a.name()
		a.expect(tokens.TkDbColon)
		return a.node(TagLabel, name, 0, tk.Pos)
	default:
		first := a.prefix()
		if a.cur.Type != tokens.TkAssign && a.cur.Type != tokens.TkComma {
			switch a.Nodes[first].Kind {
			case TagCallExpression, TagMethodCallExpression, TagTableArgumentCall, TagStringArgumentCall:
				return a.node(TagCallStatement, first, 0, tk.Pos)
			default:
				a.fail("syntax error")
			}
		}
		targets := []uint32{a.target(first)}
		for a.accept(tokens.TkComma) {
			targets = append(targets, a.target(a.prefix()))
		}
		a.expect(tokens.TkAssign)
		values := a.exprList()
		return a.node(TagAssignment, a.list(targets, tk.Pos), values, tk.Pos)
	}
}
func (a *AST) target(n uint32) uint32 {
	switch a.Nodes[n].Kind {
	case TagNameReference:
		a.Nodes[n].Kind = TagNameTarget
	case TagFieldAccess:
		a.Nodes[n].Kind = TagFieldTarget
	case TagIndexAccess:
		a.Nodes[n].Kind = TagIndexTarget
	default:
		a.fail("syntax error")
	}
	return n
}
func (a *AST) attribute() tokens.Token {
	if !a.accept(tokens.TkLt) {
		return tokens.Token{}
	}
	tk := a.expect(tokens.TkName)
	if tk.Value != "const" && tk.Value != "close" {
		a.fail(fmt.Sprintf("unknown attribute '%s'", tk.Value))
	}
	a.expect(tokens.TkGt)
	return tk
}
func (a *AST) declaration() uint32 {
	tk := a.cur
	a.advance()
	global := tk.Type == tokens.TkGlobal
	if a.cur.Type == tokens.TkFunction {
		function := a.cur
		a.advance()
		name := a.name()
		kind := TagLocalFunctionDeclaration
		if global {
			kind = TagGlobalFunctionDeclaration
		}
		return a.node(kind, name, a.functionBody(function, false), tk.Pos)
	}
	defaultAttribute := a.attribute()
	var attr uint32
	if global && defaultAttribute.Value == "close" {
		a.fail("global variable cannot be marked 'close'")
	}
	if global && a.accept(tokens.TkMul) {
		if defaultAttribute.Value != "" {
			attr = a.leaf(TagAttribute, defaultAttribute)
		}
		return a.node(TagGlobalWildcardDeclaration, attr, 0, tk.Pos)
	}
	var bindings []uint32
	closeSeen := false
	for {
		name := a.name()
		ownAttribute := a.attribute()
		var own uint32
		if ownAttribute.Value != "" {
			own = a.leaf(TagAttribute, ownAttribute)
		} else if defaultAttribute.Value != "" {
			if attr == 0 {
				attr = a.leaf(TagAttribute, defaultAttribute)
			}
			own = attr
		}
		if own != 0 && a.Values[own] == "close" {
			if global {
				a.fail("global variable cannot be marked 'close'")
			}
			if closeSeen {
				a.fail("multiple to-be-closed variables in local list")
			}
			closeSeen = true
		}
		bindings = append(bindings, a.node(TagBinding, name, own, a.Nodes[name].TokenPosition))
		if !a.accept(tokens.TkComma) {
			break
		}
	}
	var values uint32
	if a.accept(tokens.TkAssign) {
		values = a.exprList()
	}
	kind := TagLocalDeclaration
	if global {
		kind = TagGlobalDeclaration
	}
	return a.node(kind, a.list(bindings, tk.Pos), values, tk.Pos)
}
func (a *AST) functionBody(start tokens.Token, method bool) uint32 {
	a.enter()
	defer a.leave()
	oldVararg, oldLoops := a.vararg, a.loops
	a.vararg, a.loops = false, 0
	defer func() { a.vararg, a.loops = oldVararg, oldLoops }()
	open := a.expect(tokens.TkLParen)
	var params []uint32
	if method {
		self := a.leaf(TagIdentifier, tokens.Token{Value: "self", Pos: start.Pos})
		params = append(params, a.node(TagImplicitSelf, self, 0, start.Pos))
	}
	if a.cur.Type != tokens.TkRParen {
		for {
			if a.cur.Type == tokens.TkDots {
				tk := a.cur
				a.advance()
				a.vararg = true
				var name uint32
				if a.cur.Type == tokens.TkName {
					name = a.name()
				}
				params = append(params, a.node(TagVarargParameter, name, 0, tk.Pos))
				break
			}
			name := a.name()
			params = append(params, a.node(TagParameter, name, 0, a.Nodes[name].TokenPosition))
			if !a.accept(tokens.TkComma) {
				break
			}
		}
	}
	a.close(tokens.TkRParen, open)
	body := a.block()
	a.close(tokens.TkEnd, start)
	return a.node(TagFunctionExpression, a.list(params, start.Pos), body, start.Pos)
}
func (a *AST) forStatement() uint32 {
	tk := a.expect(tokens.TkFor)
	name := a.name()
	var header uint32
	kind := TagGenericFor
	if a.accept(tokens.TkAssign) {
		kind = TagNumericFor
		start := a.expr(0)
		a.expect(tokens.TkComma)
		limit := a.expr(0)
		var step uint32
		if a.accept(tokens.TkComma) {
			step = a.expr(0)
		}
		header = a.node(TagPair, name, a.list([]uint32{start, limit, step}, tk.Pos), tk.Pos)
	} else {
		names := []uint32{name}
		for a.accept(tokens.TkComma) {
			names = append(names, a.name())
		}
		a.expect(tokens.TkIn)
		header = a.node(TagPair, a.list(names, tk.Pos), a.exprList(), tk.Pos)
	}
	a.expect(tokens.TkDo)
	a.loops++
	body := a.block()
	a.loops--
	a.close(tokens.TkEnd, tk)
	return a.node(kind, header, body, tk.Pos)
}
