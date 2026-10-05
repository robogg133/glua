package tokens

import (
	"strings"
	"testing"

	"github.com/robogg133/glua/lex"
)

var benchmarkTokenSink Token
var benchmarkTokensSink []Token
var benchmarkTokenCountSink int

const benchmarkLuaChunk = `local total = 0
local message = "hello world"
for i = 1, 20 do
  total = total + i * 0x2A / 3.5
  if total >= 100 and i ~= 2 then
    print(message .. "\n", total) -- report progress
  end
end
`

func BenchmarkTokenizerNextChecked(b *testing.B) {
	for _, workload := range []struct {
		name  string
		input string
	}{
		{"Representative", strings.Repeat(benchmarkLuaChunk, 16)},
		{"Identifiers", strings.Repeat("alpha beta_2 _private longer_identifier item42 counter\n", 128)},
		{"Numbers", strings.Repeat("0 42 123456789 3.14159 .5 1e-9 0x2A 0x1.8p+2\n", 128)},
		{"Operators", strings.Repeat("+ - * / // % ^ # & | ~ << >> == ~= <= >= < > = ( ) { } [ ] ; : , . .. ... ::\n", 128)},
		{"PlainStrings", strings.Repeat("\"hello world\" 'plain text' \"another string value\"\n", 128)},
		{"EscapedStrings", strings.Repeat(`"line\nnext\tvalue" 'quote\'slash\\' "\065\x42\u{43}"`+"\n", 128)},
		{"LongComments", "--[=[\n" + strings.Repeat("comment body with words, 123, and unmatched ]] delimiters\n", 128) + "]=]\nlocal done = true\n"},
	} {
		b.Run(workload.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(workload.input)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				t := NewTokenizer(lex.NewLexer(workload.input), "")
				count := 0
				for {
					token, err := t.NextChecked()
					if err != nil {
						b.Fatal(err)
					}
					if token.Type == TkEos {
						break
					}
					count++
				}
				benchmarkTokenCountSink = count
			}
		})
	}
}

func BenchmarkTokenizerPeekNextChecked(b *testing.B) {
	input := strings.Repeat(benchmarkLuaChunk, 16)
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t := NewTokenizer(lex.NewLexer(input), "")
		count := 0
		for {
			// Alternate direct consumption and peek-then-consume.
			if count%2 == 0 {
				if _, err := t.PeekChecked(); err != nil {
					b.Fatal(err)
				}
			}
			token, err := t.NextChecked()
			if err != nil {
				b.Fatal(err)
			}
			if token.Type == TkEos {
				break
			}
			count++
		}
		benchmarkTokenCountSink = count
	}
}

func BenchmarkTokenizerTokenizeChecked(b *testing.B) {
	input := strings.Repeat(benchmarkLuaChunk, 16)
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := TokenizeChecked(input)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkTokensSink = result
	}
}

func BenchmarkTokenizerFirstTokenLongInput(b *testing.B) {
	input := strings.Repeat(benchmarkLuaChunk, 256)
	b.ReportAllocs()
	// Full source bytes, not consumed bytes: includes eager lexer construction.
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t := NewTokenizer(lex.NewLexer(input), "")
		token, err := t.NextChecked()
		if err != nil {
			b.Fatal(err)
		}
		benchmarkTokenSink = token
	}
}
