package vm_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/robogg133/glua/compile"
	"github.com/robogg133/glua/vm"
)

// This manual prototype demonstrates the immediate instruction formats.
// LOADF converts an integer immediate to float; fractional floats use LOADK.
func ExampleState_Run() {
	state := vm.NewState()
	proto := &compile.Prototype{
		MaxStackSize: 3,
		Code: []compile.Instruction{
			compile.AsBx(compile.OpLOADI, 0, 42),
			compile.AsBx(compile.OpLOADF, 1, -7),
			compile.ABC(compile.OpMOVE, 2, 0, 0, false),
		},
	}
	if err := state.Run(proto); err != nil {
		fmt.Println(err)
		return
	}
	for i, value := range state.Registers {
		fmt.Printf("R[%d] = %v (%T)\n", i, value, value)
	}
	// Output:
	// R[0] = 42 (int64)
	// R[1] = -7 (float64)
	// R[2] = 42 (int64)
}

func TestRun(t *testing.T) {
	var state vm.State // NewState is optional; the zero value also works.
	proto := &compile.Prototype{
		MaxStackSize: 6,
		Code: []compile.Instruction{
			compile.AsBx(compile.OpLOADI, 0, compile.MinArgSBx),
			compile.AsBx(compile.OpLOADI, 1, compile.MaxArgSBx),
			compile.AsBx(compile.OpLOADF, 2, compile.MinArgSBx),
			compile.AsBx(compile.OpLOADF, 3, compile.MaxArgSBx),
			compile.ABC(compile.OpMOVE, 4, 0, 0, false),
			compile.ABC(compile.OpMOVE, 5, 2, 0, false),
			compile.ABC(compile.OpMOVE, 5, 5, 0, false),
		},
	}
	if err := state.Run(proto); err != nil {
		t.Fatal(err)
	}
	want := []any{int64(compile.MinArgSBx), int64(compile.MaxArgSBx),
		float64(compile.MinArgSBx), float64(compile.MaxArgSBx),
		int64(compile.MinArgSBx), float64(compile.MinArgSBx)}
	if !reflect.DeepEqual(state.Registers, want) || state.PC != len(proto.Code) {
		t.Fatalf("state = %+v, want registers %v, pc %d", state, want, len(proto.Code))
	}

	// A new execution clears the previous frame, including reused storage.
	if err := state.Run(&compile.Prototype{
		MaxStackSize: 2,
		Code:         []compile.Instruction{compile.ABC(compile.OpMOVE, 0, 1, 0, false)},
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state.Registers, []any{nil, nil}) || state.PC != 1 {
		t.Fatalf("frame was not reset: %+v", state)
	}
	if err := state.Run(&compile.Prototype{MaxStackSize: 6}); err != nil {
		t.Fatal(err)
	}
	for _, value := range state.Registers {
		if value != nil {
			t.Fatalf("retained value in reused frame: %v", value)
		}
	}
	if state.PC != 0 {
		t.Fatalf("empty code advanced PC to %d", state.PC)
	}
}

func TestRunErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proto *compile.Prototype
		want  string
	}{
		{"nil", nil, "nil prototype"},
		{"empty_frame", &compile.Prototype{}, "invalid stack size"},
		{"destination", &compile.Prototype{MaxStackSize: 2,
			Code: []compile.Instruction{compile.AsBx(compile.OpLOADI, 2, 1)}}, "register R[2]"},
		{"source", &compile.Prototype{MaxStackSize: 2,
			Code: []compile.Instruction{compile.ABC(compile.OpMOVE, 0, 2, 0, false)}}, "register R[2]"},
		{"unknown_opcode", &compile.Prototype{MaxStackSize: 2,
			Code: []compile.Instruction{compile.Instruction(127)}}, "unknown opcode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := vm.NewState()
			if err := state.Run(tc.proto); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run error = %v, want %q", err, tc.want)
			}
			if state.PC != 0 {
				t.Fatalf("failed instruction advanced PC to %d", state.PC)
			}
		})
	}

	t.Run("partial_execution", func(t *testing.T) {
		state := vm.NewState()
		proto := &compile.Prototype{MaxStackSize: 2, Code: []compile.Instruction{
			compile.AsBx(compile.OpLOADI, 0, 42),
			compile.Instruction(127),
		}}
		err := state.Run(proto)
		if err == nil || !strings.Contains(err.Error(), "pc 1") || state.PC != 1 || state.Registers[0] != int64(42) {
			t.Fatalf("state = %+v, error = %v", state, err)
		}
	})
}
