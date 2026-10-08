// Package compile represents Lua 5.5.1 bytecode in memory.
package compile

import (
	"fmt"
	"math"

	"github.com/robogg133/glua/compile/bytecode"
)

// Encoding and VM conventions verified against the official Lua 5.5.1 sources:
// https://www.lua.org/source/5.5/lopcodes.h.html
// https://www.lua.org/source/5.5/lobject.h.html
// https://www.lua.org/source/5.5/lvm.c.html
// https://www.lua.org/source/5.5/lparser.h.html
// https://www.lua.org/source/5.5/ltm.c.html
//
// NEWTABLE and SETLIST use ivABC, not iABC. NEWTABLE always consumes an
// EXTRAARG, even with k=false. Its hash size is 0 for vB=0, otherwise
// 2^(vB-1); its array size is vC + (k ? EXTRAARG.Ax*1024 : 0).
// SETLIST writes R[A+1..A+vB] starting at table index vC+1, extending vC
// by EXTRAARG.Ax*1024 only when k=true; vB=0 takes values through top.
// Field widths alone do not guarantee valid table sizes or register ranges.
//
// Numeric FORPREP takes init/limit/step at A/A+1/A+2 and rewrites them to
// counter-or-limit/step/control. The visible variable is A+2, not A+3.
// FORPREP skips by Bx+1; FORLOOP jumps back by Bx, from the advanced pc.
// Generic TFORPREP takes iterator/state/control/closing at A..A+3, swaps
// control and closing, and marks A+2 to close. TFORCALL uses A+3..A+5 as
// call space and returns C results starting at A+3; TFORLOOP tests A+3.
// All three use the same A. These are lvm.c semantics: the brief TFOR
// descriptions in lopcodes.h retain outdated register assignments.
//
// VARARGPREP has no operands in 5.5.1: numparams and flags come from Proto.
// The named vararg slot is R[numparams]. In hidden-argument mode this slot
// is nil; GETVARG reads the hidden arguments using the key in R[C] (the VM
// ignores B), returning nil for missing keys and the argument count for "n".
// When the named parameter needs a real table, PF_VATAB replaces PF_VAHID;
// use ordinary table access instead of GETVARG. VARARG uses C-1 results
// (C=0 means all); k selects a materialized table in R[B].
//
// ERRNNIL raises an error for a non-nil R[A]; Bx is the global-name constant
// index plus one, or zero if that index cannot fit. This is a check before
// initializing any global (regular or constant), not a general undefined-global
// check.
// SELF always indexes by K[C], a short string, and copies R[B] to R[A+1];
// k does not turn its key into a register operand.
//
// RETURN uses B-1 results (B=0 means through top). RETURN/TAILCALL use
// C=numparams+1 only for hidden varargs, otherwise C=0. RETURN k closes
// upvalues and to-be-closed variables. TAILCALL k closes upvalues only and
// requires no pending to-be-closed variables. RETURN0/1 do neither closing
// nor hidden-vararg frame adjustment. TBC marks R[A]; CLOSE closes from A.
// Arithmetic/bitwise instructions require their following MMBIN* fallback;
// tests/comparisons require a following JMP; LOADKX requires EXTRAARG.

// Instruction is an unsigned 32-bit Lua VM instruction, not a serialized chunk.
// Getters decode raw fields without checking the opcode's instruction format.
type Instruction = bytecode.Bytecode

// OpCode occupies the low seven bits of an instruction.
type OpCode = bytecode.OpCode

// Opcodes alias the Lua 5.5.1 encoding package.
const (
	OpMOVE       = bytecode.OP_MOVE
	OpLOADI      = bytecode.OP_LOADI
	OpLOADF      = bytecode.OP_LOADF
	OpLOADK      = bytecode.OP_LOADK
	OpLOADKX     = bytecode.OP_LOADKX
	OpLOADFALSE  = bytecode.OP_LOADFALSE
	OpLFALSESKIP = bytecode.OP_LFALSESKIP
	OpLOADTRUE   = bytecode.OP_LOADTRUE
	OpLOADNIL    = bytecode.OP_LOADNIL
	OpGETUPVAL   = bytecode.OP_GETUPVAL
	OpSETUPVAL   = bytecode.OP_SETUPVAL
	OpGETTABUP   = bytecode.OP_GETTABUP
	OpGETTABLE   = bytecode.OP_GETTABLE
	OpGETI       = bytecode.OP_GETI
	OpGETFIELD   = bytecode.OP_GETFIELD
	OpSETTABUP   = bytecode.OP_SETTABUP
	OpSETTABLE   = bytecode.OP_SETTABLE
	OpSETI       = bytecode.OP_SETI
	OpSETFIELD   = bytecode.OP_SETFIELD
	OpNEWTABLE   = bytecode.OP_NEWTABLE
	OpSELF       = bytecode.OP_SELF
	OpADDI       = bytecode.OP_ADDI
	OpADDK       = bytecode.OP_ADDK
	OpSUBK       = bytecode.OP_SUBK
	OpMULK       = bytecode.OP_MULK
	OpMODK       = bytecode.OP_MODK
	OpPOWK       = bytecode.OP_POWK
	OpDIVK       = bytecode.OP_DIVK
	OpIDIVK      = bytecode.OP_IDIVK
	OpBANDK      = bytecode.OP_BANDK
	OpBORK       = bytecode.OP_BORK
	OpBXORK      = bytecode.OP_BXORK
	OpSHLI       = bytecode.OP_SHLI
	OpSHRI       = bytecode.OP_SHRI
	OpADD        = bytecode.OP_ADD
	OpSUB        = bytecode.OP_SUB
	OpMUL        = bytecode.OP_MUL
	OpMOD        = bytecode.OP_MOD
	OpPOW        = bytecode.OP_POW
	OpDIV        = bytecode.OP_DIV
	OpIDIV       = bytecode.OP_IDIV
	OpBAND       = bytecode.OP_BAND
	OpBOR        = bytecode.OP_BOR
	OpBXOR       = bytecode.OP_BXOR
	OpSHL        = bytecode.OP_SHL
	OpSHR        = bytecode.OP_SHR
	OpMMBIN      = bytecode.OP_MMBIN
	OpMMBINI     = bytecode.OP_MMBINI
	OpMMBINK     = bytecode.OP_MMBINK
	OpUNM        = bytecode.OP_UNM
	OpBNOT       = bytecode.OP_BNOT
	OpNOT        = bytecode.OP_NOT
	OpLEN        = bytecode.OP_LEN
	OpCONCAT     = bytecode.OP_CONCAT
	OpCLOSE      = bytecode.OP_CLOSE
	OpTBC        = bytecode.OP_TBC
	OpJMP        = bytecode.OP_JMP
	OpEQ         = bytecode.OP_EQ
	OpLT         = bytecode.OP_LT
	OpLE         = bytecode.OP_LE
	OpEQK        = bytecode.OP_EQK
	OpEQI        = bytecode.OP_EQI
	OpLTI        = bytecode.OP_LTI
	OpLEI        = bytecode.OP_LEI
	OpGTI        = bytecode.OP_GTI
	OpGEI        = bytecode.OP_GEI
	OpTEST       = bytecode.OP_TEST
	OpTESTSET    = bytecode.OP_TESTSET
	OpCALL       = bytecode.OP_CALL
	OpTAILCALL   = bytecode.OP_TAILCALL
	OpRETURN     = bytecode.OP_RETURN
	OpRETURN0    = bytecode.OP_RETURN0
	OpRETURN1    = bytecode.OP_RETURN1
	OpFORLOOP    = bytecode.OP_FORLOOP
	OpFORPREP    = bytecode.OP_FORPREP
	OpTFORPREP   = bytecode.OP_TFORPREP
	OpTFORCALL   = bytecode.OP_TFORCALL
	OpTFORLOOP   = bytecode.OP_TFORLOOP
	OpSETLIST    = bytecode.OP_SETLIST
	OpCLOSURE    = bytecode.OP_CLOSURE
	OpVARARG     = bytecode.OP_VARARG
	OpGETVARG    = bytecode.OP_GETVARG
	OpERRNNIL    = bytecode.OP_ERRNNIL
	OpVARARGPREP = bytecode.OP_VARARGPREP
	OpEXTRAARG   = bytecode.OP_EXTRAARG
	NUM_OPCODES  = bytecode.OP_EXTRAARG + 1
)

// Encoding limits, not semantic limits for a particular instruction. Signed
// fields use excess-K, so their positive limit is one larger than -minimum.
// The compiler must check source-dependent limits before calling the helpers.
const (
	MaxArgA   = 1<<8 - 1
	MaxArgB   = 1<<8 - 1
	MaxArgC   = 1<<8 - 1
	MaxArgVB  = 1<<6 - 1
	MaxArgVC  = 1<<10 - 1
	MaxArgBx  = 1<<17 - 1
	MaxArgAx  = 1<<25 - 1
	OffsetSBx = MaxArgBx >> 1
	OffsetSJ  = MaxArgAx >> 1
	MinArgSBx = -OffsetSBx
	MaxArgSBx = MaxArgBx - OffsetSBx
	MinArgSJ  = -OffsetSJ
	MaxArgSJ  = MaxArgAx - OffsetSJ
	// MaxStack is the maximum register count; register 255 is the NO_REG sentinel.
	MaxStack = MaxArgA
)

func checkBytecodeArg(name string, value, min, max int) {
	if value < min || value > max {
		panic(fmt.Sprintf("compile: %s=%d outside [%d, %d]", name, value, min, max))
	}
}

func checkBytecodeOp(op OpCode) {
	checkBytecodeArg("opcode", int(op), 0, int(NUM_OPCODES)-1)
}

// ABC encodes iABC (Op:7, A:8, k:1, B:8, C:8, from low to high).
// All encoders panic on invalid opcodes or out-of-range fields, but do not
// validate opcode/format combinations, register allocation, or VM semantics.
func ABC(op OpCode, a, b, c int, k bool) Instruction {
	checkBytecodeOp(op)
	checkBytecodeArg("A", a, 0, MaxArgA)
	checkBytecodeArg("B", b, 0, MaxArgB)
	checkBytecodeArg("C", c, 0, MaxArgC)
	i := Instruction(0).SetOp(op).SetA(uint8(a)).SetB(uint8(b)).SetC(uint8(c))
	if k {
		i = i.SetK(1)
	}
	return i
}

// VABC encodes ivABC (Op:7, A:8, k:1, vB:6, vC:10).
func VABC(op OpCode, a, b, c int, k bool) Instruction {
	checkBytecodeOp(op)
	checkBytecodeArg("A", a, 0, MaxArgA)
	checkBytecodeArg("vB", b, 0, MaxArgVB)
	checkBytecodeArg("vC", c, 0, MaxArgVC)
	i := Instruction(0).SetOp(op).SetA(uint8(a)).SetVB(uint8(b)).SetVC(uint16(c))
	if k {
		i = i.SetK(1)
	}
	return i
}

// ABx encodes iABx (Op:7, A:8, Bx:17).
func ABx(op OpCode, a, bx int) Instruction {
	checkBytecodeOp(op)
	checkBytecodeArg("A", a, 0, MaxArgA)
	checkBytecodeArg("Bx", bx, 0, MaxArgBx)
	return Instruction(0).SetOp(op).SetA(uint8(a)).SetBx(uint32(bx))
}

// AsBx encodes a signed immediate in excess-65535 representation.
func AsBx(op OpCode, a, sbx int) Instruction {
	checkBytecodeArg("sBx", sbx, MinArgSBx, MaxArgSBx)
	return ABx(op, a, sbx+OffsetSBx)
}

// Ax encodes iAx (Op:7, Ax:25).
func Ax(op OpCode, ax int) Instruction {
	checkBytecodeOp(op)
	checkBytecodeArg("Ax", ax, 0, MaxArgAx)
	return Instruction(0).SetOp(op).SetAx(uint32(ax))
}

// SJ encodes a signed jump in excess-16777215 representation. The displacement
// is relative to the next instruction. Bit 15 belongs to sJ, not a separate k.
func SJ(op OpCode, sj int) Instruction {
	checkBytecodeArg("sJ", sj, MinArgSJ, MaxArgSJ)
	return Ax(op, sj+OffsetSJ)
}

// ConstantKind is an in-memory discriminator, not a Lua TValue or dump tag.
type ConstantKind uint8

const (
	ConstantNil ConstantKind = iota
	ConstantBoolean
	ConstantInteger
	ConstantFloat
	ConstantString
)

// Constant stores integers as two's-complement Bits, floats as IEEE 754 bits,
// and booleans as Bits=0/1. String contains arbitrary bytes, including NUL.
// Unused fields are zero. Bits preserve negative zero and NaN payloads.
type Constant struct {
	Kind   ConstantKind
	Bits   uint64
	String string
}

func IntConstant(v int64) Constant { return Constant{Kind: ConstantInteger, Bits: uint64(v)} }
func FloatConstant(v float64) Constant {
	return Constant{Kind: ConstantFloat, Bits: math.Float64bits(v)}
}
func StringConstant(v string) Constant { return Constant{Kind: ConstantString, String: v} }
func BoolConstant(v bool) Constant {
	var bits uint64
	if v {
		bits = 1
	}
	return Constant{Kind: ConstantBoolean, Bits: bits}
}
func NilConstant() Constant { return Constant{Kind: ConstantNil} }

// Upvalue kinds are variable kinds from lparser.h, NOT bit flags. In particular,
// 5.5 inserts the vararg kind at 2, so to-be-closed is 3 rather than 5.4's 2.
const (
	UpvalueRegular          uint8 = iota // VDKREG
	UpvalueConst                         // RDKCONST
	UpvalueVararg                        // RDKVAVAR
	UpvalueToClose                       // RDKTOCLOSE
	UpvalueCompileTimeConst              // RDKCTC; normally folded, not captured
	UpvalueGlobal                        // GDKREG; globals normally use _ENV
	UpvalueGlobalConst                   // GDKCONST
)

// Upvalue describes a capture from the parent's register or upvalue list.
type Upvalue struct {
	Name    string
	InStack bool
	Index   uint8
	Kind    uint8
}

// Prototype owns bytecode data without retaining AST nodes. Lines, when present,
// has one absolute source line per instruction (including auxiliary words),
// rather than Lua's compressed lineinfo/abslineinfo representation.
// IsVararg means either vararg mode. VarargTable requires IsVararg=true.
// A future dumper maps these to flag 0, PF_VAHID=1, or PF_VATAB=2, never both.
// PF_FIXED=4 is a runtime memory-ownership flag, not compiler metadata.
type Prototype struct {
	Code            []Instruction
	Constants       []Constant
	Children        []Prototype
	Upvalues        []Upvalue
	NumParams       uint8
	IsVararg        bool
	MaxStackSize    uint8
	VarargTable     bool
	Lines           []uint32
	LineDefined     uint32
	LastLineDefined uint32
}
