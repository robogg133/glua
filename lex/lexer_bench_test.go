package lex

import (
	"strings"
	"testing"
)

var benchmarkLexerSink *Lexer
var benchmarkRuneSink int64

func BenchmarkLexer(b *testing.B) {
	for _, workload := range []struct {
		name string
		line string
	}{
		{"ASCII", "local total = total + 42; print(\"hello world\") -- update\n"},
		{"Unicode", "local total = total + 42; print(\"héllo 世界 🌍\") -- café\n"},
	} {
		input := strings.Repeat(workload.line, 128)
		b.Run("Constructor/"+workload.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(input)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchmarkLexerSink = NewLexer(input)
			}
		})
		b.Run("Scan/"+workload.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(input)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				l := NewLexer(input)
				var sum int64
				for ch := l.Next(); ch != 0; ch = l.Next() {
					sum += int64(ch)
				}
				benchmarkRuneSink = sum
			}
		})
	}
}
