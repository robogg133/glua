package tokens

import (
	"strings"
	"testing"

	"github.com/robogg133/glua/lex"
)

func TestReadShortString(t *testing.T) {
	cases := []struct{ input, want string }{
		{`""`, ""},
		{`'ação'`, "ação"},
		{`"世界🙂�"`, "世界🙂�"},
		{"\"a\xff\xfe世界\xc0\xaf\xed\xa0\x80\"", "a\xff\xfe世界\xc0\xaf\xed\xa0\x80"},
		{"\"é\xff\\n🙂\xfe\\z \xfffin\"", "é\xff\n🙂\xfe\xfffin"},
		{`"\z "`, ""},
		{`"é\n世界\t🙂"`, "é\n世界\t🙂"},
		{`"\a\b\f\n\r\t\v\\\"\'"`, "\a\b\f\n\r\t\v\\\"'"},
		{`"\0\12\1234\255\x00\xAf"`, "\x00\x0c{4\xff\x00\xaf"},
		{`"\u{0}\u{7f}\u{80}\u{7ff}\u{800}\u{ffff}\u{10000}\u{10ffff}"`, "\x00\x7f\xc2\x80\xdf\xbf\xe0\xa0\x80\xef\xbf\xbf\xf0\x90\x80\x80\xf4\x8f\xbf\xbf"},
		{`"\u{d800}\u{1fffff}\u{200000}\u{3ffffff}\u{4000000}\u{7fffffff}"`, "\xed\xa0\x80\xf7\xbf\xbf\xbf\xf8\x88\x80\x80\x80\xfb\xbf\xbf\xbf\xbf\xfc\x84\x80\x80\x80\x80\xfd\xbf\xbf\xbf\xbf\xbf"},
		{`"\u{00000000000041}"`, "A"},
		{"\"a\\z \t\v\f\r\nb\"", "ab"},
		{"\"a\\\nb\\\rc\\\r\nd\\\n\re\"", "a\nb\nc\nd\ne"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			l := lex.NewLexer(tc.input + "!")
			got, err := readShortString(l)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
			if l.Peek() != '!' {
				t.Fatal("consumed beyond closing quote")
			}
		})
	}
}

func TestReadShortStringErrors(t *testing.T) {
	for _, input := range []string{
		"", "x", `"`, `"abc`, "\"\\", "\"a\nb\"", "\"a\rb\"",
		`"\q"`, `"\256"`, `"\999"`, `"\x"`, `"\x0"`, `"\xGG"`,
		`"\u"`, `"\u{}"`, `"\u{1"`, `"\u{g}"`, `"\u{80000000}"`,
		`"\u{ffffffffffffffff}"`, `"\z `,
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := readShortString(lex.NewLexer(input)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestLongDelimiter(t *testing.T) {
	for _, tc := range []struct {
		input   string
		closing bool
		want    int
	}{
		{"[[", false, 0}, {"[==[x", false, 2}, {"[=[", false, 1},
		{"]]", true, 0}, {"]==]x", true, 2},
		{"", false, -1}, {"[=", false, -1}, {"[=x", false, -1},
		{"[==]", false, -1}, {"]==[", true, -1}, {"[[", true, -1},
		{"[=é[", false, -1}, {"[=\xff[", false, -1},
		{"[" + strings.Repeat("=", 4096) + "[🙂", false, 4096},
		{"]" + strings.Repeat("=", 4096) + "]🙂", true, 4096},
	} {
		l := lex.NewLexer(tc.input)
		if got := longDelimiter(l, tc.closing); got != tc.want {
			t.Errorf("%q: got %d; want %d", tc.input, got, tc.want)
		}
		// Reading the entire source also checks that lookahead consumed nothing.
		var got []byte
		for ch := l.Next(); ch != 0; ch = l.Next() {
			got = append(got, byte(ch))
		}
		if string(got) != tc.input {
			t.Errorf("delimiter check consumed input %q", tc.input)
		}
	}
}

func TestReadLongString(t *testing.T) {
	for _, tc := range []struct {
		input string
		level int
		want  string
	}{
		{"[[]]", 0, ""},
		{"[[\nabc]]", 0, "abc"},
		{"[[\rabc]]", 0, "abc"},
		{"[[\r\nabc]]", 0, "abc"},
		{"[[\n\rabc]]", 0, "abc"},
		{"[[\n\nabc]]", 0, "\nabc"},
		{"[[a\rb\nc\r\nd\n\re\r\rf]]", 0, "a\nb\nc\nd\ne\n\nf"},
		{`[==[a]=]b]]c\n[==[d]==]`, 2, `a]=]b]]c\n[==[d`},
		{"[=[ação]=]", 1, "ação"},
		{"[[世界🙂�\nété\n]]", 0, "世界🙂�\nété\n"},
		{"[[a\xff\xfe世界\xc0\xaf\xed\xa0\x80]]", 0, "a\xff\xfe世界\xc0\xaf\xed\xa0\x80"},
		{"[[é\xff\r\n🙂\xfe\n\r世界\xff]]", 0, "é\xff\n🙂\xfe\n世界\xff"},
		{"[[\n\r\xff\n\n\r\r\nend]]", 0, "\xff\n\n\nend"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			l := lex.NewLexer(tc.input + "!")
			got, err := readLongString(l, tc.level)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
			if l.Peek() != '!' {
				t.Fatal("consumed beyond closing delimiter")
			}
			comment := lex.NewLexer(tc.input + "!")
			if err := skipLongComment(comment, tc.level); err != nil {
				t.Fatalf("skipLongComment: %v", err)
			}
			if comment.Peek() != '!' || comment.ByteIndex() != l.ByteIndex() ||
				comment.Index() != l.Index() || comment.Line() != l.Line() || comment.Col() != l.Col() {
				t.Fatal("skipLongComment cursor differs from readLongString")
			}
		})
	}
}

func TestReadLongStringErrors(t *testing.T) {
	for _, tc := range []struct {
		input string
		level int
	}{
		{"", 0}, {"[[", 0}, {"[[abc]", 0}, {"[=[abc]]", 1},
		{"[=x", 1}, {"[[]]", 1}, {"[[]]", -1},
	} {
		if _, err := readLongString(lex.NewLexer(tc.input), tc.level); err == nil {
			t.Errorf("%q, level %d: expected error", tc.input, tc.level)
		}
		if err := skipLongComment(lex.NewLexer(tc.input), tc.level); err == nil {
			t.Errorf("skipLongComment %q, level %d: expected error", tc.input, tc.level)
		}
	}
}
