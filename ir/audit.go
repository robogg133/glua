// Package ir provides index-based analysis of the parser's arena AST.
package ir

import (
	"fmt"

	"github.com/robogg133/glua/parser"
)

// Analysis uses binding IDs, with Bindings[0] reserved for unresolved globals.
// References is indexed by AST node, including declaration Identifiers.
type Analysis struct {
	Bindings   []Binding
	References []uint32
}

// Binding describes a lexical local, parameter, or loop variable. Writes counts
// subsequent assignments (including nested functions), not its initialization.
// Initializer is the directly corresponding expression, or zero if unknown;
// it is not a promise that evaluating or substituting that expression is safe.
// Function is the owning FunctionExpression, or the chunk Root.
// An unnamed vararg has Name "..." and Node zero (there is no Identifier).
type Binding struct {
	Name        string
	Node        uint32
	Initializer uint32
	Writes      int
	Captured    bool
	Attribute   string
	Function    uint32
}

// Audit validates the complete arena before resolving lexical bindings. It does
// not modify a, recheck Lua semantics, or perform dataflow/constant propagation.
// On any error it returns an empty Analysis, never a partial result.
func Audit(a *parser.AST) (Analysis, error) {
	if err := validateArena(a); err != nil {
		return Analysis{}, err
	}
	v := auditor{a: a, result: Analysis{
		Bindings: []Binding{{}}, References: make([]uint32, len(a.Nodes)),
	}}
	if err := v.walk(a.Root, a.Root, 0); err != nil {
		return Analysis{}, err
	}
	return v.result, nil
}

// Negative shapes describe context-sensitive auxiliaries and node categories;
// positive shapes are exact parser tags.
type shape int

const (
	expression shape = -1 - iota
	statement
	target
	parameter
	field
	statements
	expressions
	targets
	bindings
	parameters
	branches
	identifiers
	fields
	pathPair
	methodPair
	numericPair
	genericPair
	stepStart
	stepLimit
	stepOptional
)

type arenaCheck struct {
	node     uint32
	want     shape
	optional bool
}

func matches(kind parser.Tag, want shape) bool {
	switch want {
	case expression:
		return kind >= parser.TagNil && kind <= parser.TagStringArgumentCall || kind >= parser.TagNot && kind <= parser.TagPower
	case statement:
		return kind >= parser.TagEmpty && kind <= parser.TagLabel && kind != parser.TagBranch
	case target:
		return kind >= parser.TagNameTarget && kind <= parser.TagIndexTarget
	case parameter:
		return kind == parser.TagParameter || kind == parser.TagImplicitSelf || kind == parser.TagVarargParameter
	case field:
		return kind >= parser.TagArrayField && kind <= parser.TagNameField
	case statements, expressions, targets, bindings, parameters, branches, identifiers, fields, stepStart, stepLimit, stepOptional:
		return kind == parser.TagList
	case pathPair, methodPair, numericPair, genericPair:
		return kind == parser.TagPair
	default:
		return shape(kind) == want
	}
}

func validateArena(a *parser.AST) error {
	if a == nil || len(a.Nodes) == 0 || len(a.Nodes) != len(a.Values) {
		return fmt.Errorf("ir: missing arena or mismatched Values length")
	}
	if a.Nodes[0] != (parser.Node{}) || a.Values[0] != "" || a.Root == 0 {
		return fmt.Errorf("ir: invalid sentinel or root")
	}
	seen := make([]bool, len(a.Nodes))
	checks := []arenaCheck{{a.Root, shape(parser.TagChunk), false}}
	for len(checks) != 0 {
		c := checks[len(checks)-1]
		checks = checks[:len(checks)-1]
		if c.node == 0 && c.optional {
			continue
		}
		if c.node == 0 || uint64(c.node) >= uint64(len(a.Nodes)) {
			return fmt.Errorf("ir: invalid child index %d (shape %d)", c.node, c.want)
		}
		n := a.Nodes[c.node]
		if !matches(n.Kind, c.want) {
			return fmt.Errorf("ir: node %d has tag %d, expected shape %d", c.node, n.Kind, c.want)
		}
		if seen[c.node] && n.Kind != parser.TagAttribute {
			return fmt.Errorf("ir: cycle or shared non-attribute node %d", c.node)
		}
		seen[c.node] = true
		add := func(node uint32, want shape, optional bool) {
			checks = append(checks, arenaCheck{node, want, optional})
		}
		exact := func(node uint32, tag parser.Tag, optional bool) { add(node, shape(tag), optional) }
		zero := func(node uint32) error {
			if node != 0 {
				return fmt.Errorf("ir: unexpected child of node %d", c.node)
			}
			return nil
		}
		// Pair/list roles come from their parent, so the item categories cannot
		// be bypassed by inserting a syntactically valid but misplaced node.
		switch c.want {
		case statements, expressions, targets, bindings, parameters, branches, identifiers, fields:
			item := map[shape]shape{
				statements: statement, expressions: expression, targets: target,
				bindings: shape(parser.TagBinding), parameters: parameter,
				branches: shape(parser.TagBranch), identifiers: shape(parser.TagIdentifier), fields: field,
			}[c.want]
			add(n.Left, item, false)
			add(n.Right, c.want, true)
			continue
		case stepStart, stepLimit, stepOptional:
			add(n.Left, expression, c.want == stepOptional)
			if c.want == stepOptional {
				if err := zero(n.Right); err != nil {
					return err
				}
			} else {
				add(n.Right, c.want-1, false)
			}
			continue
		case pathPair:
			add(n.Left, identifiers, true)
			exact(n.Right, parser.TagIdentifier, true)
			continue
		case methodPair:
			add(n.Left, expression, false)
			exact(n.Right, parser.TagIdentifier, false)
			continue
		case numericPair:
			exact(n.Left, parser.TagIdentifier, false)
			add(n.Right, stepStart, false)
			continue
		case genericPair:
			add(n.Left, identifiers, false)
			add(n.Right, expressions, false)
			continue
		}
		switch n.Kind {
		case parser.TagIdentifier, parser.TagAttribute, parser.TagNameReference, parser.TagNameTarget,
			parser.TagNil, parser.TagBoolean, parser.TagInteger, parser.TagFloat, parser.TagString,
			parser.TagVararg, parser.TagEmpty, parser.TagBreak:
			if err := zero(n.Left); err != nil {
				return err
			}
			if err := zero(n.Right); err != nil {
				return err
			}
			switch n.Kind {
			case parser.TagIdentifier, parser.TagNameReference, parser.TagNameTarget, parser.TagInteger, parser.TagFloat:
				if a.Values[c.node] == "" {
					return fmt.Errorf("ir: missing value at node %d", c.node)
				}
			case parser.TagAttribute:
				if a.Values[c.node] != "const" && a.Values[c.node] != "close" {
					return fmt.Errorf("ir: invalid attribute at node %d", c.node)
				}
			case parser.TagBoolean:
				if a.Values[c.node] != "true" && a.Values[c.node] != "false" {
					return fmt.Errorf("ir: invalid boolean at node %d", c.node)
				}
			}
		case parser.TagChunk, parser.TagDoBlock:
			exact(n.Left, parser.TagBlock, false)
			if err := zero(n.Right); err != nil {
				return err
			}
		case parser.TagBlock:
			add(n.Left, statements, true)
			if err := zero(n.Right); err != nil {
				return err
			}
		case parser.TagNamePath:
			exact(n.Left, parser.TagIdentifier, false)
			add(n.Right, pathPair, false)
		case parser.TagParameter, parser.TagImplicitSelf, parser.TagVarargParameter, parser.TagGoto, parser.TagLabel:
			exact(n.Left, parser.TagIdentifier, n.Kind == parser.TagVarargParameter)
			if err := zero(n.Right); err != nil {
				return err
			}
		case parser.TagBinding:
			exact(n.Left, parser.TagIdentifier, false)
			exact(n.Right, parser.TagAttribute, true)
		case parser.TagAssignment:
			add(n.Left, targets, false)
			add(n.Right, expressions, false)
		case parser.TagLocalDeclaration, parser.TagGlobalDeclaration:
			add(n.Left, bindings, false)
			add(n.Right, expressions, true)
		case parser.TagGlobalWildcardDeclaration:
			exact(n.Left, parser.TagAttribute, true)
			if err := zero(n.Right); err != nil {
				return err
			}
		case parser.TagFunctionDeclaration, parser.TagMethodDeclaration:
			exact(n.Left, parser.TagNamePath, false)
			exact(n.Right, parser.TagFunctionExpression, false)
		case parser.TagLocalFunctionDeclaration, parser.TagGlobalFunctionDeclaration:
			exact(n.Left, parser.TagIdentifier, false)
			exact(n.Right, parser.TagFunctionExpression, false)
		case parser.TagFunctionExpression:
			add(n.Left, parameters, true)
			exact(n.Right, parser.TagBlock, false)
		case parser.TagCallStatement:
			// Validate the index before inspecting the call tag.
			if n.Left == 0 || uint64(n.Left) >= uint64(len(a.Nodes)) || !isCall(a.Nodes[n.Left].Kind) {
				return fmt.Errorf("ir: invalid call statement %d", c.node)
			}
			add(n.Left, expression, false)
			if err := zero(n.Right); err != nil {
				return err
			}
		case parser.TagIf:
			add(n.Left, branches, false)
			exact(n.Right, parser.TagBlock, true)
		case parser.TagBranch, parser.TagWhile:
			add(n.Left, expression, false)
			exact(n.Right, parser.TagBlock, false)
		case parser.TagRepeatUntil:
			exact(n.Left, parser.TagBlock, false)
			add(n.Right, expression, false)
		case parser.TagNumericFor, parser.TagGenericFor:
			header := numericPair
			if n.Kind == parser.TagGenericFor {
				header = genericPair
			}
			add(n.Left, header, false)
			exact(n.Right, parser.TagBlock, false)
		case parser.TagReturn:
			add(n.Left, expressions, true)
			if err := zero(n.Right); err != nil {
				return err
			}
		case parser.TagParenthesized, parser.TagNot, parser.TagNegate, parser.TagBitwiseNot, parser.TagLength, parser.TagArrayField:
			add(n.Left, expression, false)
			if err := zero(n.Right); err != nil {
				return err
			}
		case parser.TagFieldAccess, parser.TagFieldTarget:
			add(n.Left, expression, false)
			exact(n.Right, parser.TagIdentifier, false)
		case parser.TagIndexAccess, parser.TagIndexTarget, parser.TagKeyField:
			add(n.Left, expression, false)
			add(n.Right, expression, false)
		case parser.TagNameField:
			exact(n.Left, parser.TagIdentifier, false)
			add(n.Right, expression, false)
		case parser.TagCallExpression, parser.TagMethodCallExpression, parser.TagTableArgumentCall, parser.TagStringArgumentCall:
			callee := expression
			if n.Kind == parser.TagMethodCallExpression {
				callee = methodPair
			}
			add(n.Left, callee, false)
			if n.Kind == parser.TagTableArgumentCall || n.Kind == parser.TagStringArgumentCall {
				if n.Right == 0 || uint64(n.Right) >= uint64(len(a.Nodes)) {
					return fmt.Errorf("ir: missing abbreviated argument at node %d", c.node)
				}
				arg := a.Nodes[n.Right]
				want := parser.TagTableConstructor
				if n.Kind == parser.TagStringArgumentCall {
					want = parser.TagString
				}
				if arg.Right != 0 || arg.Left == 0 || uint64(arg.Left) >= uint64(len(a.Nodes)) || a.Nodes[arg.Left].Kind != want {
					return fmt.Errorf("ir: invalid abbreviated argument at node %d", c.node)
				}
			}
			add(n.Right, expressions, true)
		case parser.TagTableConstructor:
			add(n.Left, fields, true)
			if err := zero(n.Right); err != nil {
				return err
			}
		default:
			if n.Kind < parser.TagOr || n.Kind > parser.TagPower {
				return fmt.Errorf("ir: unsupported tag %d at node %d", n.Kind, c.node)
			}
			add(n.Left, expression, false)
			add(n.Right, expression, false)
		}
	}
	for i := 1; i < len(seen); i++ {
		if !seen[i] {
			return fmt.Errorf("ir: unreachable arena node %d", i)
		}
	}
	return nil
}

func isCall(kind parser.Tag) bool {
	return kind >= parser.TagCallExpression && kind <= parser.TagStringArgumentCall
}

type auditor struct {
	a      *parser.AST
	result Analysis
	scopes []map[string]uint32 // A present zero is a lexical global barrier.
}

func (v *auditor) push() { v.scopes = append(v.scopes, make(map[string]uint32)) }
func (v *auditor) pop()  { v.scopes = v.scopes[:len(v.scopes)-1] }

func (v *auditor) bind(node, initializer, attribute, function uint32, global bool) {
	name := v.a.Values[node]
	if node == 0 {
		name = "..."
	}
	var id uint32
	if !global {
		id = uint32(len(v.result.Bindings))
		v.result.Bindings = append(v.result.Bindings, Binding{
			Name: name, Node: node, Initializer: initializer,
			Attribute: v.a.Values[attribute], Function: function,
		})
		if node != 0 {
			v.result.References[node] = id
		}
	}
	v.scopes[len(v.scopes)-1][name] = id
}

func (v *auditor) reference(node, function uint32, write bool) {
	for i := len(v.scopes) - 1; i >= 0; i-- {
		id, found := v.scopes[i][v.a.Values[node]]
		if !found {
			continue
		}
		v.result.References[node] = id
		if id != 0 {
			if write {
				v.result.Bindings[id].Writes++
			}
			if v.result.Bindings[id].Function != function {
				v.result.Bindings[id].Captured = true
			}
		}
		return
	}
}

// Lists are always iterative. Other nesting is bounded even for arenas not
// produced by the parser; deep left-associated expressions may hit this bound.
const maxAuditDepth = 512

func (v *auditor) block(node, condition, function uint32, depth int) error {
	v.push()
	defer v.pop()
	if err := v.walk(v.a.Nodes[node].Left, function, depth+1); err != nil {
		return err
	}
	return v.walk(condition, function, depth+1)
}

func (v *auditor) walk(node, function uint32, depth int) error {
	if node == 0 {
		return nil
	}
	if depth > maxAuditDepth {
		return fmt.Errorf("ir: analysis nesting exceeds %d at node %d", maxAuditDepth, node)
	}
	n := v.a.Nodes[node]
	walk := func(child uint32) error { return v.walk(child, function, depth+1) }
	switch n.Kind {
	case parser.TagList:
		for list := node; list != 0; list = v.a.Nodes[list].Right {
			if err := walk(v.a.Nodes[list].Left); err != nil {
				return err
			}
		}
		return nil
	case parser.TagBlock:
		return v.block(node, 0, function, depth)
	case parser.TagNameReference, parser.TagNameTarget:
		v.reference(node, function, n.Kind == parser.TagNameTarget)
		return nil
	case parser.TagLocalDeclaration, parser.TagGlobalDeclaration:
		if err := walk(n.Right); err != nil {
			return err
		}
		value := n.Right
		for list := n.Left; list != 0; list = v.a.Nodes[list].Right {
			b := v.a.Nodes[v.a.Nodes[list].Left]
			var initializer uint32
			if value != 0 {
				initializer = v.a.Nodes[value].Left
				value = v.a.Nodes[value].Right
			}
			// Only an explicit nth expression maps to the nth binding. Extra
			// results from a final call/vararg and implicit nils remain unknown.
			v.bind(b.Left, initializer, b.Right, function, n.Kind == parser.TagGlobalDeclaration)
		}
		return nil
	case parser.TagLocalFunctionDeclaration, parser.TagGlobalFunctionDeclaration:
		v.bind(n.Left, n.Right, 0, function, n.Kind == parser.TagGlobalFunctionDeclaration)
		return walk(n.Right)
	case parser.TagGlobalWildcardDeclaration:
		// Like the parser, wildcard declarations do not shadow named locals.
		return nil
	case parser.TagFunctionExpression:
		v.push()
		defer v.pop()
		for list := n.Left; list != 0; list = v.a.Nodes[list].Right {
			p := v.a.Nodes[v.a.Nodes[list].Left]
			v.bind(p.Left, 0, 0, node, false)
		}
		return v.walk(n.Right, node, depth+1)
	case parser.TagFunctionDeclaration, parser.TagMethodDeclaration:
		path := v.a.Nodes[n.Left]
		fields := v.a.Nodes[path.Right]
		v.reference(path.Left, function, fields.Left == 0 && fields.Right == 0)
		return walk(n.Right)
	case parser.TagRepeatUntil:
		return v.block(n.Left, n.Right, function, depth)
	case parser.TagNumericFor, parser.TagGenericFor:
		header := v.a.Nodes[n.Left]
		if err := walk(header.Right); err != nil {
			return err
		}
		v.push()
		defer v.pop()
		if n.Kind == parser.TagNumericFor {
			v.bind(header.Left, 0, 0, function, false)
		} else {
			for list := header.Left; list != 0; list = v.a.Nodes[list].Right {
				v.bind(v.a.Nodes[list].Left, 0, 0, function, false)
			}
		}
		return walk(n.Right)
	}
	if err := walk(n.Left); err != nil {
		return err
	}
	return walk(n.Right)
}
