package bytecode

func (b Bytecode) SetOp(op OpCode) Bytecode {
	return b&^127 | Bytecode(op)
}

func (b Bytecode) SetA(A uint8) Bytecode {
	return b&^32640 | Bytecode(A)
}

func (b Bytecode) SetK(k uint8) Bytecode {
	return b&^16384 | Bytecode(k&^254)
}
