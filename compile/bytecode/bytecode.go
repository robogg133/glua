package bytecode

type Bytecode uint32

func (b Bytecode) Op() OpCode {
	return OpCode(b) & 127
}

// A returns the 'A' field of the bytecode. This exists in iABC, ivABC
func (b Bytecode) A() uint8 {
	return uint8(b >> 7)
}

// K returns the 'k' field of the bytecode. This exists in iABC, ivABC
func (b Bytecode) K() byte {
	return byte(b>>15) & 1
}

// B returns the 'B' field of an iABC bytecode.
func (b Bytecode) B() uint8 {
	return byte(b >> 16)
}

// SB returns the 'sB' field of an iABC bytecode. This is the signed 'B' field.
func (b Bytecode) SB() int8 {
	return int8(b>>16) - 127
}

// C returns the 'C' field of an iABC bytecode.
func (b Bytecode) C() uint8 {
	return byte(b >> 24)
}

// SC returns the 'sC' field of an iABC bytecode. This is the signed 'C' field.
func (b Bytecode) SC() int8 {
	return int8(b>>24) - 127
}

// VB returns the 'vB' field of an ivABC bytecode.
func (b Bytecode) VB() uint8 {
	return byte(b>>16) & 63
}

// VC returns the 'vC' field of an ivABC bytecode.
func (b Bytecode) VC() uint16 {
	return uint16(b >> 22) // 32-10
}

// Bx returns the 'Bx' field of an iABx bytecode.
func (b Bytecode) Bx() uint32 {
	return uint32(b >> 15) // 32-17
}

// SBx returns the 'sBx' field of an iAsBx bytecode. This is the signed 'Bx' field from iABx bytecode.
func (b Bytecode) SBx() int32 {
	return int32(b>>15) - 65535 // 32 - 17
}

// Ax returns the 'Ax' field of an iAx bytecode.
func (b Bytecode) Ax() uint32 {
	return uint32(b >> 7) // 32-25
}

// SJ returns the 'sJ' field of an isJ bytecode.
func (b Bytecode) SJ() int32 {
	return int32(b>>7) - 16777215 // 32-25
}
