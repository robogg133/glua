package ir

import (
	"fmt"

	"github.com/robogg133/glua/parser"
)

// Constant identifies a proven immutable local in the input AST. Detection does
// not add <const> attributes or remove declarations (their scope still matters).
type Constant struct {
	Binding     uint32
	Declaration uint32
	Kind        parser.Tag
	Value       string
	Explicit    bool
}

// Change refers to input node indices, never to indices in the compacted output.
type Change struct {
	Kind     string
	Node     uint32
	Position [2]uint32
	Message  string
}

// Report retains the input audit and an ordered log of conservative rewrites.
type Report struct {
	Analysis    Analysis
	Constants   []Constant
	Changes     []Change
	BeforeNodes int
	AfterNodes  int
}

// Optimize audits a complete parser AST and returns a separately owned, compact
// AST with the same index-based layout. It never modifies its input.
// Like Lua's own optimizations, this assumes debug hooks/upvalue mutation and
// stack inspection are not used to observe or modify eliminated operations.
// Numeric folding targets Lua's default int64/float64 configuration.
func Optimize(input *parser.AST) (*parser.AST, Report, error) {
	analysis, err := Audit(input)
	if err != nil {
		return nil, Report{}, err
	}
	o := optimizer{
		source:    input,
		ast:       &parser.AST{Nodes: append([]parser.Node(nil), input.Nodes...), Values: append([]string(nil), input.Values...), Root: input.Root},
		report:    Report{Analysis: analysis, BeforeNodes: len(input.Nodes) - 1},
		constants: make([]constant, len(analysis.Bindings)),
		known:     make([]uint8, len(analysis.Bindings)),
	}
	for id := 1; id < len(analysis.Bindings); id++ {
		if c, ok := o.bindingConstant(uint32(id)); ok {
			b := analysis.Bindings[id]
			o.report.Constants = append(o.report.Constants, Constant{uint32(id), b.Node, c.kind, c.value, b.Attribute == "const"})
		}
	}
	o.ast.Root = o.rewrite(input.Root, false)
	output := compact(o.ast)
	if _, err := Audit(output); err != nil {
		return nil, Report{}, fmt.Errorf("ir: invalid optimized AST: %w", err)
	}
	o.report.AfterNodes = len(output.Nodes) - 1
	return output, o.report, nil
}

type optimizer struct {
	source       *parser.AST
	ast          *parser.AST
	report       Report
	constants    []constant
	known        []uint8 // 0 unknown, 1 being evaluated, 2 not constant, 3 constant
	bindingDepth int
}

func (o *optimizer) change(kind string, node uint32, message string) {
	o.report.Changes = append(o.report.Changes, Change{kind, node, o.source.Nodes[node].TokenPosition, message})
}
func (o *optimizer) add(kind parser.Tag, left, right uint32, pos [2]uint32, value string) uint32 {
	id := uint32(len(o.ast.Nodes))
	o.ast.Nodes = append(o.ast.Nodes, parser.Node{Kind: kind, Left: left, Right: right, TokenPosition: pos})
	o.ast.Values = append(o.ast.Values, value)
	return id
}
func (o *optimizer) literal(c constant, pos [2]uint32) uint32 {
	return o.add(c.kind, 0, 0, pos, c.value)
}
func (o *optimizer) list(items []uint32, pos [2]uint32) uint32 {
	var next uint32
	for i := len(items) - 1; i >= 0; i-- {
		next = o.add(parser.TagList, items[i], next, pos, "")
	}
	return next
}

func (o *optimizer) bindingConstant(id uint32) (constant, bool) {
	if id == 0 {
		return constant{}, false
	}
	if o.known[id] == 3 {
		return o.constants[id], true
	}
	if o.known[id] != 0 || o.bindingDepth >= 200 {
		return constant{}, false
	}
	b := o.report.Analysis.Bindings[id]
	o.known[id] = 1
	if b.Writes == 0 && b.Initializer != 0 && b.Attribute != "close" {
		o.bindingDepth++
		fuel := 256
		c, ok := o.evaluate(b.Initializer, nil, &fuel)
		o.bindingDepth--
		if ok {
			o.constants[id] = c
			o.known[id] = 3
			return c, true
		}
	}
	o.known[id] = 2
	return constant{}, false
}

// evaluate only proves values; it never executes Lua, reads globals, constructs
// objects, or assumes operators on unknown operands lack metamethods.
func (o *optimizer) evaluate(index uint32, parameters map[uint32]constant, fuel *int) (constant, bool) {
	if index == 0 || *fuel <= 0 {
		return constant{}, false
	}
	*fuel--
	n := o.source.Nodes[index]
	if c, ok := constantOf(o.source, index); ok {
		return c, true
	}
	switch n.Kind {
	case parser.TagNameReference:
		id := o.report.Analysis.References[index]
		if c, ok := parameters[id]; ok {
			return c, true
		}
		return o.bindingConstant(id)
	case parser.TagParenthesized:
		return o.evaluate(n.Left, parameters, fuel)
	case parser.TagNot, parser.TagNegate, parser.TagBitwiseNot, parser.TagLength:
		c, ok := o.evaluate(n.Left, parameters, fuel)
		if !ok {
			return constant{}, false
		}
		return foldUnary(n.Kind, c)
	case parser.TagCallExpression, parser.TagTableArgumentCall, parser.TagStringArgumentCall:
		return o.evaluateCall(index, parameters, fuel)
	}
	if n.Kind >= parser.TagOr && n.Kind <= parser.TagPower {
		left, ok := o.evaluate(n.Left, parameters, fuel)
		if !ok {
			return constant{}, false
		}
		if (n.Kind == parser.TagOr && truth(left)) || (n.Kind == parser.TagAnd && !truth(left)) {
			return left, true
		}
		right, ok := o.evaluate(n.Right, parameters, fuel)
		if !ok {
			return constant{}, false
		}
		return foldBinary(n.Kind, left, right)
	}
	return constant{}, false
}

// Inlining is bounded partial evaluation, not textual substitution. Only tiny,
// single-result functions and fully constant arguments qualify, so argument
// order, unused arguments, capture identity and multiple returns are preserved.
func (o *optimizer) evaluateCall(index uint32, outer map[uint32]constant, fuel *int) (constant, bool) {
	call := o.source.Nodes[index]
	callee := call.Left
	for o.source.Nodes[callee].Kind == parser.TagParenthesized {
		callee = o.source.Nodes[callee].Left
	}
	fn := o.source.Nodes[callee]
	if fn.Kind == parser.TagNameReference {
		id := o.report.Analysis.References[callee]
		if id == 0 {
			return constant{}, false
		}
		binding := o.report.Analysis.Bindings[id]
		if binding.Writes != 0 || binding.Initializer == 0 || binding.Attribute == "close" {
			return constant{}, false
		}
		callee = binding.Initializer
		for o.source.Nodes[callee].Kind == parser.TagParenthesized {
			callee = o.source.Nodes[callee].Left
		}
		fn = o.source.Nodes[callee]
	}
	if fn.Kind != parser.TagFunctionExpression {
		return constant{}, false
	}
	statements := o.source.Nodes[fn.Right].Left
	if statements == 0 || o.source.Nodes[statements].Right != 0 {
		return constant{}, false
	}
	ret := o.source.Nodes[o.source.Nodes[statements].Left]
	if ret.Kind != parser.TagReturn || ret.Left == 0 || o.source.Nodes[ret.Left].Right != 0 {
		return constant{}, false
	}
	expression := o.source.Nodes[ret.Left].Left
	if !o.inlineSmall(expression, 12) {
		return constant{}, false
	}
	params := make(map[uint32]constant, len(outer)+4)
	for k, v := range outer {
		params[k] = v
	}
	arguments := call.Right
	count := 0
	for p := fn.Left; p != 0; p = o.source.Nodes[p].Right {
		param := o.source.Nodes[o.source.Nodes[p].Left]
		if param.Kind != parser.TagParameter || count >= 8 {
			return constant{}, false
		}
		count++
		value := constant{kind: parser.TagNil, value: "nil"}
		if arguments != 0 {
			var ok bool
			value, ok = o.evaluate(o.source.Nodes[arguments].Left, outer, fuel)
			if !ok {
				return constant{}, false
			}
			arguments = o.source.Nodes[arguments].Right
		}
		params[o.report.Analysis.References[param.Left]] = value
	}
	// Extras are evaluated by Lua even if the function ignores them.
	for arguments != 0 {
		if _, ok := o.evaluate(o.source.Nodes[arguments].Left, outer, fuel); !ok {
			return constant{}, false
		}
		arguments = o.source.Nodes[arguments].Right
	}
	return o.evaluate(expression, params, fuel)
}
func (o *optimizer) inlineSmall(index uint32, budget int) bool {
	stack := []uint32{index}
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if i == 0 {
			continue
		}
		budget--
		if budget < 0 {
			return false
		}
		n := o.source.Nodes[i]
		switch {
		case n.Kind >= parser.TagNil && n.Kind <= parser.TagNameReference:
		case n.Kind == parser.TagParenthesized || n.Kind >= parser.TagNot && n.Kind <= parser.TagPower:
		default:
			return false
		}
		stack = append(stack, n.Left, n.Right)
	}
	return true
}

func (o *optimizer) rewrite(index uint32, keepCall bool) uint32 {
	if index == 0 {
		return 0
	}
	n := o.ast.Nodes[index]
	switch n.Kind {
	case parser.TagBlock:
		var items []uint32
		dead := false
		for list := n.Left; list != 0; list = o.source.Nodes[list].Right {
			item := o.source.Nodes[list].Left
			if o.source.Nodes[item].Kind == parser.TagLabel {
				dead = false
			}
			if dead {
				o.change("unreachable", item, "statement after unconditional transfer removed")
				continue
			}
			rewritten := o.rewrite(item, false)
			if rewritten != 0 {
				items = append(items, rewritten)
				dead = o.terminal(rewritten)
			}
		}
		n.Left = o.list(items, n.TokenPosition)
	case parser.TagList:
		// Lists can be much longer than the parser's nesting limit.
		for list := index; list != 0; {
			cell := o.ast.Nodes[list]
			next := cell.Right
			cell.Left = o.rewrite(cell.Left, false)
			o.ast.Nodes[list] = cell
			list = next
		}
		return index
	case parser.TagIf:
		return o.rewriteIf(index)
	case parser.TagWhile:
		n.Left = o.rewrite(n.Left, false)
		if c, ok := constantOf(o.ast, n.Left); ok && !truth(c) {
			o.change("unreachable", index, "while with a false constant condition removed")
			return 0
		}
		n.Right = o.rewrite(n.Right, false)
	case parser.TagNameReference:
		if c, ok := o.bindingConstant(o.report.Analysis.References[index]); ok {
			o.change("constant", index, "immutable local reference replaced by its constant value")
			return o.literal(c, n.TokenPosition)
		}
	case parser.TagCallStatement:
		// A call statement cannot be replaced by an arbitrary expression statement.
		n.Left = o.rewrite(n.Left, true)
	case parser.TagAnd, parser.TagOr:
		n.Left = o.rewrite(n.Left, false)
		if c, ok := constantOf(o.ast, n.Left); ok {
			o.change("fold", index, "constant short-circuit operand simplified")
			if truth(c) == (n.Kind == parser.TagOr) {
				return n.Left
			}
			right := o.rewrite(n.Right, false)
			// and/or always produce one value, unlike an unparenthesized call/vararg.
			if result, ok := constantOf(o.ast, right); ok {
				return o.literal(result, n.TokenPosition)
			}
			return o.add(parser.TagParenthesized, right, 0, n.TokenPosition, "")
		}
		n.Right = o.rewrite(n.Right, false)
	default:
		n.Left = o.rewrite(n.Left, false)
		n.Right = o.rewrite(n.Right, false)
	}
	o.ast.Nodes[index] = n
	if n.Kind >= parser.TagNot && n.Kind <= parser.TagLength {
		if operand, ok := constantOf(o.ast, n.Left); ok {
			if value, ok := foldUnary(n.Kind, operand); ok {
				o.change("fold", index, "constant unary operation folded")
				return o.literal(value, n.TokenPosition)
			}
		}
	}
	if n.Kind >= parser.TagEqual && n.Kind <= parser.TagPower {
		left, lok := constantOf(o.ast, n.Left)
		right, rok := constantOf(o.ast, n.Right)
		if lok && rok {
			if value, ok := foldBinary(n.Kind, left, right); ok {
				o.change("fold", index, "constant binary operation folded")
				return o.literal(value, n.TokenPosition)
			}
		}
	}
	if !keepCall && (n.Kind == parser.TagCallExpression || n.Kind == parser.TagStringArgumentCall || n.Kind == parser.TagTableArgumentCall) {
		fuel := 256
		if value, ok := o.evaluateCall(index, nil, &fuel); ok {
			o.change("inline", index, "small single-result function evaluated with constant arguments")
			return o.literal(value, n.TokenPosition)
		}
	}
	return index
}

func (o *optimizer) rewriteIf(index uint32) uint32 {
	n := o.source.Nodes[index]
	var kept []uint32
	otherwise := uint32(0)
	selected := false
	for list := n.Left; list != 0; list = o.source.Nodes[list].Right {
		branchID := o.source.Nodes[list].Left
		branch := o.source.Nodes[branchID]
		condition := o.rewrite(branch.Left, false)
		if c, ok := constantOf(o.ast, condition); ok {
			o.change("branch", branchID, "constant condition removed")
			if !truth(c) {
				continue
			}
			otherwise = o.rewrite(branch.Right, false)
			selected = true
			break
		}
		branch.Left = condition
		branch.Right = o.rewrite(branch.Right, false)
		o.ast.Nodes[branchID] = branch
		kept = append(kept, branchID)
	}
	if !selected {
		otherwise = o.rewrite(n.Right, false)
	}
	if len(kept) == 0 {
		if otherwise == 0 {
			return 0
		}
		// Keep the original lexical scope, including <close> lifetimes and labels.
		return o.add(parser.TagDoBlock, otherwise, 0, n.TokenPosition, "")
	}
	n.Left = o.list(kept, n.TokenPosition)
	n.Right = otherwise
	o.ast.Nodes[index] = n
	return index
}

// Labels resume reachability. A loop is a terminal transfer only when its
// condition is provably endless and it has no break belonging to that loop.
// Goto targets are kept rather than threaded across scopes.
func (o *optimizer) terminal(index uint32) bool {
	n := o.ast.Nodes[index]
	switch n.Kind {
	case parser.TagReturn, parser.TagBreak, parser.TagGoto:
		return true
	case parser.TagDoBlock:
		return o.terminal(n.Left)
	case parser.TagWhile:
		c, ok := constantOf(o.ast, n.Left)
		return ok && truth(c) && !o.hasBreak(n.Right)
	case parser.TagRepeatUntil:
		c, ok := constantOf(o.ast, n.Right)
		return ok && !truth(c) && !o.hasBreak(n.Left)
	case parser.TagBlock:
		last := uint32(0)
		for l := n.Left; l != 0; l = o.ast.Nodes[l].Right {
			item := o.ast.Nodes[l].Left
			if o.ast.Nodes[item].Kind != parser.TagEmpty {
				last = item
			}
		}
		return last != 0 && o.terminal(last)
	case parser.TagIf:
		if n.Right == 0 || !o.terminal(n.Right) {
			return false
		}
		for l := n.Left; l != 0; l = o.ast.Nodes[l].Right {
			if !o.terminal(o.ast.Nodes[o.ast.Nodes[l].Left].Right) {
				return false
			}
		}
		return true
	}
	return false
}

func (o *optimizer) hasBreak(root uint32) bool {
	stack := []uint32{root}
	for len(stack) != 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if i == 0 {
			continue
		}
		n := o.ast.Nodes[i]
		switch n.Kind {
		case parser.TagBreak:
			return true
		case parser.TagFunctionExpression, parser.TagWhile, parser.TagRepeatUntil, parser.TagNumericFor, parser.TagGenericFor:
			continue
		}
		stack = append(stack, n.Left, n.Right)
	}
	return false
}

// Compact iteratively so discarded subtrees and old list cells do not remain
// in the returned arena. Attribute sharing is preserved by the index map.
func compact(a *parser.AST) *parser.AST {
	out := &parser.AST{Nodes: []parser.Node{{}}, Values: []string{""}}
	ids := make([]uint32, len(a.Nodes))
	stack := []uint32{a.Root}
	order := make([]uint32, 0, len(a.Nodes))
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if i == 0 || ids[i] != 0 {
			continue
		}
		ids[i] = uint32(len(out.Nodes))
		order = append(order, i)
		out.Nodes = append(out.Nodes, a.Nodes[i])
		out.Values = append(out.Values, a.Values[i])
		stack = append(stack, a.Nodes[i].Right, a.Nodes[i].Left)
	}
	for _, old := range order {
		i := ids[old]
		out.Nodes[i].Left = ids[out.Nodes[i].Left]
		out.Nodes[i].Right = ids[out.Nodes[i].Right]
	}
	out.Root = ids[a.Root]
	return out
}
