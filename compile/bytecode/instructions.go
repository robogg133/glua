package bytecode

type LOADI struct {
	B Bytecode
}
type MOVE struct {
	B Bytecode
}

func NewLOADI(reg uint8, i int32) LOADI {
	return LOADI{B: Bytecode(0).SetOp(OP_LOADI).SetA(reg).SetSBx(i)}
}

func (i LOADI) Reg() uint8 {
	return i.B.A()
}
func (i LOADI) Integer() int32 {
	return i.B.SBx()
}

func NewMOVE(reg uint8, src uint8) MOVE {
	return MOVE{B: Bytecode(0).SetOp(OP_MOVE).SetA(reg).SetB(src)}
}
