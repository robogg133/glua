package bytecode_test

import (
	"testing"

	"github.com/robogg133/glua/compile/bytecode"
)

func TestBytecode_OpCode(t *testing.T) {
	b := bytecode.Bytecode(305419905)
	if b.Op() != 1 {
		t.Errorf("expected opcode 1, got %d", b.Op())
	}
	t.Run("iABC", func(t *testing.T) {
		if b.A() != 173 {
			t.Errorf("expected A 173, got %d", b.A())
		}
		if b.K() != 0 {
			t.Errorf("expected K 0, got %d", b.K())
		}
		if b.B() != 52 {
			t.Errorf("expected B 52, got %d", b.B())
		}
		if b.C() != 18 {
			t.Errorf("expected C 18, got %d", b.C())
		}

		// signed

		if b.SB() != -75 {
			t.Errorf("expected sB -75, got %d", b.SB())
		}
		if b.SC() != -109 {
			t.Errorf("expected sC -109, got %d", b.SC())
		}
	})
	t.Run("ivABC", func(t *testing.T) {
		if b.VB() != 52 {
			t.Errorf("expected vB 52, got %d", b.VB())
		}
		if b.VC() != 72 {
			t.Errorf("expected vC 72, got %d", b.VC())
		}
	})
	t.Run("iABx", func(t *testing.T) {
		if b.Bx() != 9320 {
			t.Errorf("expected Bx 9320, got %d", b.Bx())
		}
	})
	t.Run("iAsBx", func(t *testing.T) {
		if b.SBx() != -56215 {
			t.Errorf("expected sBx -56215, got %d", b.SBx())
		}
	})
	t.Run("iAx", func(t *testing.T) {
		if b.Ax() != 2386093 {
			t.Errorf("expected Ax 2386093, got %d", b.Ax())
		}
	})
	t.Run("isJ", func(t *testing.T) {
		if b.SJ() != -14391122 {
			t.Errorf("expected sJ -14391122, got %d", b.SJ())
		}
	})
}
