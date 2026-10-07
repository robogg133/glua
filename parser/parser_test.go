package parser_test

import (
	"testing"

	"github.com/robogg133/glua/lex"
	"github.com/robogg133/glua/parser"
	"github.com/robogg133/glua/tokens"
)

func TestParse(t *testing.T) {
	ast := parser.NewAst(tokens.NewTokenizer(lex.NewLexer(`local a = 11`), "stdin"))
	if err := ast.Next(); err != nil {
		t.Fatal(err)
	}

}
