package bytecode

func (b Bytecode) SetOp(op OpCode) Bytecode {
	return (b &^ 0x7F) | Bytecode(op)&0x7F
}

func (b Bytecode) SetA(a uint8) Bytecode {
	return (b &^ (0xFF << 7)) | Bytecode(a)<<7
}

func (b Bytecode) SetK(k byte) Bytecode {
	return (b &^ (1 << 15)) | Bytecode(k&1)<<15
}

// iABC
func (b Bytecode) SetB(B uint8) Bytecode {
	return (b &^ (0xFF << 16)) | Bytecode(B)<<16
}

func (b Bytecode) SetSB(B int8) Bytecode {
	return b.SetB(uint8(B + 127))
}

func (b Bytecode) SetC(C uint8) Bytecode {
	return (b &^ (0xFF << 24)) | Bytecode(C)<<24
}

func (b Bytecode) SetSC(C int8) Bytecode {
	return b.SetC(uint8(C + 127))
}

// ivABC
func (b Bytecode) SetVB(vB uint8) Bytecode {
	return (b &^ (0x3F << 16)) | Bytecode(vB&0x3F)<<16
}

func (b Bytecode) SetVC(vC uint16) Bytecode {
	return (b &^ (0x3FF << 22)) | Bytecode(vC&0x3FF)<<22
}

// iABx
func (b Bytecode) SetBx(Bx uint32) Bytecode {
	return (b &^ (0x1FFFF << 15)) | Bytecode(Bx&0x1FFFF)<<15
}

func (b Bytecode) SetSBx(sBx int32) Bytecode {
	return b.SetBx(uint32(sBx + 65535))
}

// iAx
func (b Bytecode) SetAx(Ax uint32) Bytecode {
	return (b &^ (0x1FFFFFF << 7)) | Bytecode(Ax&0x1FFFFFF)<<7
}

// isJ
func (b Bytecode) SetSJ(sJ int32) Bytecode {
	return b.SetAx(uint32(sJ + 16777215))
}
