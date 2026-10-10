package main

import (
	"fmt"
	"os"

	"github.com/robogg133/glua"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: lua arquivo.lua")
		os.Exit(1)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(filename string) error {
	_, err := glua.ExecFile(filename)
	return err
}
