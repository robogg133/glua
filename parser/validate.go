package parser

import "fmt"

// All links are arena indices. Environments are persistent, so a goto keeps
// the exact declarations at its source even after sibling scopes are visited.
type validationBinding struct {
	previous, depth  int
	name             string
	readonly, global bool
}

type validationBlock struct {
	parent int
	labels map[string]validationJump
}

type validationJump struct {
	node               uint32
	block, environment int
}

type astValidator struct {
	a        *AST
	bindings []validationBinding
	blocks   []validationBlock
	gotos    []validationJump
}

func (a *AST) validate() error {
	v := astValidator{a: a, bindings: []validationBinding{{}}, blocks: []validationBlock{{}}}
	env := v.bind(0, "_ENV", false, false)
	if err := v.block(a.Nodes[a.Root].Left, 0, env, 0); err != nil {
		return err
	}
	for _, jump := range v.gotos {
		name := a.Values[a.Nodes[jump.node].Left]
		found := false
		for block := jump.block; block != 0; block = v.blocks[block].parent {
			label, ok := v.blocks[block].labels[name]
			if !ok {
				continue
			}
			found = true
			source, target := jump.environment, label.environment
			for v.bindings[source].depth > v.bindings[target].depth {
				source = v.bindings[source].previous
			}
			if source != target {
				// Find the first declaration at the target absent at the source.
				missing := target
				for source != target {
					if v.bindings[target].depth >= v.bindings[source].depth {
						missing = target
						target = v.bindings[target].previous
					} else {
						source = v.bindings[source].previous
					}
				}
				variable := v.bindings[missing].name
				if variable == "" {
					variable = "*"
				}
				return v.error(jump.node, "<goto %s> jumps into the scope of variable '%s'", name, variable)
			}
			break
		}
		if !found {
			return v.error(jump.node, "no visible label '%s' for <goto>", name)
		}
	}
	return nil
}

func (v *astValidator) error(node uint32, format string, args ...any) error {
	return fmt.Errorf("%s:%d: %s", v.a.t.Filename(), v.a.Nodes[node].TokenPosition[0], fmt.Sprintf(format, args...))
}

func (v *astValidator) bind(env int, name string, readonly, global bool) int {
	v.bindings = append(v.bindings, validationBinding{env, v.bindings[env].depth + 1, name, readonly, global})
	return len(v.bindings) - 1
}

func (v *astValidator) lookup(env int, name string) (validationBinding, bool) {
	fallback := validationBinding{global: true}
	declared, collective := true, false
	for env != 0 {
		b := v.bindings[env]
		if b.name == name {
			return b, true
		}
		if b.global {
			if b.name == "" && !collective {
				fallback, declared, collective = b, true, true
			} else if !collective {
				declared = false
			}
		}
		env = b.previous
	}
	return fallback, declared
}

func (v *astValidator) environment(node uint32, env int) error {
	b, _ := v.lookup(env, "_ENV")
	if b.global {
		return v.error(node, "_ENV is global when accessing variable '%s'", v.a.Values[node])
	}
	return nil
}

func (v *astValidator) name(node uint32, env int, write bool) error {
	name := v.a.Values[node]
	b, declared := v.lookup(env, name)
	if !declared {
		return v.error(node, "variable '%s' not declared", name)
	}
	if b.global {
		if err := v.environment(node, env); err != nil {
			return err
		}
	}
	if write && b.readonly {
		return v.error(node, "attempt to assign to const variable '%s'", name)
	}
	return nil
}

// condition is nonzero only for repeat: its locals remain live through until.
func (v *astValidator) block(node uint32, parent, env int, condition uint32) error {
	if node == 0 {
		return nil
	}
	a := v.a
	block := len(v.blocks)
	v.blocks = append(v.blocks, validationBlock{parent, make(map[string]validationJump)})
	entry := env
	var last uint32
	for list := a.Nodes[node].Left; list != 0; list = a.Nodes[list].Right {
		item := a.Nodes[list].Left
		if kind := a.Nodes[item].Kind; kind != TagLabel && kind != TagEmpty {
			last = list
		}
	}
	trailing := last == 0
	for list := a.Nodes[node].Left; list != 0; list = a.Nodes[list].Right {
		item := a.Nodes[list].Left
		n := a.Nodes[item]
		switch n.Kind {
		case TagLabel:
			name := a.Values[n.Left]
			for scope := block; scope != 0; scope = v.blocks[scope].parent {
				if previous, ok := v.blocks[scope].labels[name]; ok {
					return v.error(item, "label '%s' already defined on line %d", name, a.Nodes[previous.node].TokenPosition[0])
				}
			}
			labelEnv := env
			if trailing && condition == 0 {
				labelEnv = entry
			}
			v.blocks[block].labels[name] = validationJump{item, block, labelEnv}
		case TagGoto:
			v.gotos = append(v.gotos, validationJump{item, block, env})
		case TagLocalDeclaration, TagGlobalDeclaration:
			global := n.Kind == TagGlobalDeclaration
			if global && n.Right != 0 {
				for l := n.Left; l != 0; l = a.Nodes[l].Right {
					if err := v.environment(a.Nodes[a.Nodes[l].Left].Left, env); err != nil {
						return err
					}
				}
			}
			if err := v.walk(n.Right, block, env); err != nil {
				return err
			}
			for l := n.Left; l != 0; l = a.Nodes[l].Right {
				binding := a.Nodes[a.Nodes[l].Left]
				env = v.bind(env, a.Values[binding.Left], binding.Right != 0, global)
			}
		case TagGlobalWildcardDeclaration:
			env = v.bind(env, "", n.Left != 0, true)
		case TagLocalFunctionDeclaration, TagGlobalFunctionDeclaration:
			global := n.Kind == TagGlobalFunctionDeclaration
			env = v.bind(env, a.Values[n.Left], false, global)
			if global {
				if err := v.environment(n.Left, env); err != nil {
					return err
				}
			}
			if err := v.walk(n.Right, block, env); err != nil {
				return err
			}
		default:
			if err := v.walk(item, block, env); err != nil {
				return err
			}
		}
		if list == last {
			trailing = true
		}
	}
	return v.walk(condition, block, env)
}

func (v *astValidator) walk(node uint32, block, env int) error {
	if node == 0 {
		return nil
	}
	a := v.a
	n := a.Nodes[node]
	switch n.Kind {
	case TagList:
		for node != 0 {
			if err := v.walk(a.Nodes[node].Left, block, env); err != nil {
				return err
			}
			node = a.Nodes[node].Right
		}
		return nil
	case TagBlock:
		return v.block(node, block, env, 0)
	case TagNameReference, TagNameTarget:
		return v.name(node, env, n.Kind == TagNameTarget)
	case TagFunctionExpression:
		for list := n.Left; list != 0; list = a.Nodes[list].Right {
			param := a.Nodes[a.Nodes[list].Left]
			if param.Left != 0 {
				env = v.bind(env, a.Values[param.Left], param.Kind == TagVarargParameter, false)
			}
		}
		// Captures share the lexical environment, never the label namespace.
		return v.block(n.Right, 0, env, 0)
	case TagFunctionDeclaration, TagMethodDeclaration:
		path := a.Nodes[n.Left]
		fields := a.Nodes[path.Right]
		if err := v.name(path.Left, env, fields.Left == 0 && fields.Right == 0); err != nil {
			return err
		}
		return v.walk(n.Right, block, env)
	case TagRepeatUntil:
		return v.block(n.Left, block, env, n.Right)
	case TagNumericFor, TagGenericFor:
		header := a.Nodes[n.Left]
		if err := v.walk(header.Right, block, env); err != nil {
			return err
		}
		if n.Kind == TagNumericFor {
			env = v.bind(env, a.Values[header.Left], true, false)
		} else {
			for list := header.Left; list != 0; list = a.Nodes[list].Right {
				env = v.bind(env, a.Values[a.Nodes[list].Left], list == header.Left, false)
			}
		}
		return v.block(n.Right, block, env, 0)
	}
	// Identifiers used as field keys/method names are leaves, not references.
	if err := v.walk(n.Left, block, env); err != nil {
		return err
	}
	return v.walk(n.Right, block, env)
}
