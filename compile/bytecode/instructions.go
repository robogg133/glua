package bytecode

type LOADI struct {
	b Bytecode
}

func (i LOADI) Reg() uint8 {
	return i.b.A()
}
func (i LOADI) Integer() int32 {
	return i.b.SBx()
}
