package vm

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Lua patterns operate on bytes, not Unicode characters. Captures are copied
// at branches so failed backtracking cannot leak capture state.
type patternCapture struct{ start, end int }
type patternCaptures struct {
	values [32]patternCapture
	n      int
}
type luaPattern struct {
	state         *State
	text, pattern string
	work          uint64
	err           error
}

func (m *luaPattern) tick() error {
	if m.err != nil {
		return m.err
	}
	m.work++
	if m.state.Context != nil {
		if err := m.state.Context.Err(); err != nil {
			return err
		}
	}
	if m.state.MaxSteps != 0 && m.state.steps >= m.state.MaxSteps {
		return valueError("pattern matching step limit exceeded")
	}
	m.state.steps++
	// A hard ceiling also protects native calls when instruction limits are off.
	if m.work > 10000000 {
		return valueError("pattern matching work limit exceeded")
	}
	return nil
}
func patternClass(c, cl byte) bool {
	lower := cl
	if cl >= 'A' && cl <= 'Z' {
		lower += 'a' - 'A'
	}
	var yes bool
	switch lower {
	case 'a':
		yes = c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
	case 'c':
		yes = c < 32 || c == 127
	case 'd':
		yes = c >= '0' && c <= '9'
	case 'g':
		yes = c >= 33 && c <= 126
	case 'l':
		yes = c >= 'a' && c <= 'z'
	case 'p':
		yes = c >= 33 && c <= 126 && !patternClass(c, 'a') && !patternClass(c, 'd')
	case 's':
		yes = c == ' ' || c >= '\t' && c <= '\r'
	case 'u':
		yes = c >= 'A' && c <= 'Z'
	case 'w':
		yes = patternClass(c, 'a') || patternClass(c, 'd')
	case 'x':
		yes = patternClass(c, 'd') || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
	case 'z':
		yes = c == 0
	default:
		return c == cl
	}
	if cl >= 'A' && cl <= 'Z' {
		return !yes
	}
	return yes
}
func (m *luaPattern) classEnd(p int) (int, error) {
	pat := m.pattern
	if pat[p] == '%' {
		if p+1 >= len(pat) {
			return 0, valueError("malformed pattern (ends with '%%')")
		}
		return p + 2, nil
	}
	if pat[p] != '[' {
		return p + 1, nil
	}
	i := p + 1
	if i < len(pat) && pat[i] == '^' {
		i++
	}
	first := i
	for i < len(pat) {
		if err := m.tick(); err != nil {
			return 0, err
		}
		if pat[i] == '%' {
			i++
			if i >= len(pat) {
				break
			}
		} else if pat[i] == ']' && i > first {
			return i + 1, nil
		}
		i++
	}
	return 0, valueError("malformed pattern (missing ']')")
}
func (m *luaPattern) bracket(c byte, p, end int) bool {
	i := p + 1
	negate := i < end-1 && m.pattern[i] == '^'
	if negate {
		i++
	}
	yes := false
	for i < end-1 {
		if err := m.tick(); err != nil {
			m.err = err
			return false
		}
		if m.pattern[i] == '%' {
			yes = yes || patternClass(c, m.pattern[i+1])
			i += 2
		} else if i+2 < end-1 && m.pattern[i+1] == '-' {
			yes = yes || c >= m.pattern[i] && c <= m.pattern[i+2]
			i += 3
		} else {
			yes = yes || c == m.pattern[i]
			i++
		}
	}
	if negate {
		return !yes
	}
	return yes
}
func (m *luaPattern) single(c byte, p, end int) bool {
	switch m.pattern[p] {
	case '.':
		return true
	case '%':
		return patternClass(c, m.pattern[p+1])
	case '[':
		return m.bracket(c, p, end)
	}
	return c == m.pattern[p]
}
func (m *luaPattern) match(pos, p int, caps patternCaptures, depth int) (int, patternCaptures, bool, error) {
	fail := func(err error) (int, patternCaptures, bool, error) {
		if err == nil {
			err = m.err
		}
		return 0, caps, false, err
	}
	if depth > 200 {
		return fail(valueError("pattern too complex"))
	}
	for {
		if err := m.tick(); err != nil {
			return fail(err)
		}
		if p == len(m.pattern) {
			return pos, caps, true, nil
		}
		switch m.pattern[p] {
		case '(':
			if caps.n == 32 {
				return fail(valueError("too many captures"))
			}
			i := caps.n
			caps.n++
			caps.values[i] = patternCapture{pos, -1}
			p++
			if p < len(m.pattern) && m.pattern[p] == ')' {
				caps.values[i].end = -2
				p++
			}
			return m.match(pos, p, caps, depth+1)
		case ')':
			i := caps.n - 1
			for i >= 0 && caps.values[i].end != -1 {
				i--
			}
			if i < 0 {
				return fail(valueError("invalid pattern capture"))
			}
			caps.values[i].end = pos
			return m.match(pos, p+1, caps, depth+1)
		case '$':
			if p+1 == len(m.pattern) {
				return pos, caps, pos == len(m.text), nil
			}
		case '%':
			if p+1 >= len(m.pattern) {
				return fail(valueError("malformed pattern (ends with '%%')"))
			}
			cl := m.pattern[p+1]
			if cl == 'b' {
				if p+3 >= len(m.pattern) {
					return fail(valueError("malformed pattern (missing arguments to '%%b')"))
				}
				if pos == len(m.text) || m.text[pos] != m.pattern[p+2] {
					return fail(nil)
				}
				balance := 1
				i := pos + 1
				for ; i < len(m.text); i++ {
					if err := m.tick(); err != nil {
						return fail(err)
					}
					if m.text[i] == m.pattern[p+3] {
						balance--
						if balance == 0 {
							break
						}
					} else if m.text[i] == m.pattern[p+2] {
						balance++
					}
				}
				if balance != 0 {
					return fail(nil)
				}
				pos = i + 1
				p += 4
				continue
			}
			if cl == 'f' {
				if p+2 >= len(m.pattern) || m.pattern[p+2] != '[' {
					return fail(valueError("missing '[' after '%%f' in pattern"))
				}
				end, err := m.classEnd(p + 2)
				if err != nil {
					return fail(err)
				}
				var prev, next byte
				if pos > 0 {
					prev = m.text[pos-1]
				}
				if pos < len(m.text) {
					next = m.text[pos]
				}
				if m.bracket(prev, p+2, end) || !m.bracket(next, p+2, end) {
					return fail(nil)
				}
				p = end
				continue
			}
			if cl >= '0' && cl <= '9' {
				i := int(cl - '1')
				if i < 0 || i >= caps.n || caps.values[i].end == -1 {
					return fail(valueError("invalid capture index %%%c", cl))
				}
				c := caps.values[i]
				if c.end == -2 {
					return fail(nil)
				}
				n := c.end - c.start
				if n > len(m.text)-pos {
					return fail(nil)
				}
				for j := 0; j < n; j++ {
					if err := m.tick(); err != nil {
						return fail(err)
					}
					if m.text[c.start+j] != m.text[pos+j] {
						return fail(nil)
					}
				}
				pos += n
				p += 2
				continue
			}
		}
		end, err := m.classEnd(p)
		if err != nil {
			return fail(err)
		}
		matches := pos < len(m.text) && m.single(m.text[pos], p, end)
		if end < len(m.pattern) {
			switch m.pattern[end] {
			case '?':
				if matches {
					if e, c, ok, err := m.match(pos+1, end+1, caps, depth+1); ok || err != nil {
						return e, c, ok, err
					}
				}
				p = end + 1
				continue
			case '*', '+', '-':
				q := m.pattern[end]
				first := pos
				if q == '+' {
					if !matches {
						return fail(nil)
					}
					first++
				}
				if q == '-' {
					for i := first; ; i++ {
						if e, c, ok, err := m.match(i, end+1, caps, depth+1); ok || err != nil {
							return e, c, ok, err
						}
						if i == len(m.text) || !m.single(m.text[i], p, end) {
							break
						}
					}
				} else {
					last := first
					for last < len(m.text) && m.single(m.text[last], p, end) {
						if err := m.tick(); err != nil {
							return fail(err)
						}
						last++
					}
					for i := last; i >= first; i-- {
						if e, c, ok, err := m.match(i, end+1, caps, depth+1); ok || err != nil {
							return e, c, ok, err
						}
					}
				}
				return fail(nil)
			}
		}
		if !matches {
			return fail(nil)
		}
		pos++
		p = end
	}
}
func (m *luaPattern) capture(c patternCaptures, index, start, end int) (any, error) {
	if c.n == 0 && index == 0 {
		return m.text[start:end], nil
	}
	if index >= c.n {
		return nil, valueError("invalid capture index %%%d", index+1)
	}
	v := c.values[index]
	switch v.end {
	case -1:
		return nil, valueError("unfinished capture")
	case -2:
		return int64(v.start + 1), nil
	default:
		return m.text[v.start:v.end], nil
	}
}
func (m *luaPattern) captures(c patternCaptures, start, end int) ([]any, error) {
	if c.n == 0 {
		return []any{m.text[start:end]}, nil
	}
	out := make([]any, c.n)
	for i := 0; i < c.n; i++ {
		v, err := m.capture(c, i, start, end)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}
func patternStart(name string, a []any, index, n int) (int, error) {
	init, err := libOptInt(name, a, index, 1)
	if err != nil {
		return 0, err
	}
	if init < 0 {
		init = int64(n) + init + 1
	}
	if init < 1 {
		init = 1
	}
	if init > int64(n)+1 {
		return n + 1, nil
	}
	return int(init - 1), nil
}
func patternArgs(name string, a []any) (string, string, error) {
	text, e := libString(name, a, 0)
	if e != nil {
		return "", "", e
	}
	pat, e := libString(name, a, 1)
	return text, pat, e
}
func patternFind(s *State, a []any, find bool) ([]any, error) {
	name := "match"
	if find {
		name = "find"
	}
	text, pat, err := patternArgs(name, a)
	if err != nil {
		return nil, err
	}
	start, err := patternStart(name, a, 2, len(text))
	if err != nil {
		return nil, err
	}
	if start > len(text) {
		return []any{nil}, nil
	}
	if find && (truth(libAt(a, 3)) || !strings.ContainsAny(pat, "^$*+?.([%-")) {
		i := strings.Index(text[start:], pat)
		if i < 0 {
			return []any{nil}, nil
		}
		return []any{int64(start + i + 1), int64(start + i + len(pat))}, nil
	}
	m := luaPattern{state: s, text: text, pattern: pat}
	p := 0
	anchor := strings.HasPrefix(pat, "^")
	if anchor {
		p++
	}
	for i := start; i <= len(text); i++ {
		end, c, ok, e := m.match(i, p, patternCaptures{}, 0)
		if e != nil {
			return nil, e
		}
		if ok {
			values, e := m.captures(c, i, end)
			if e != nil {
				return nil, e
			}
			if find {
				out := []any{int64(i + 1), int64(end)}
				if c.n > 0 {
					out = append(out, values...)
				}
				return out, nil
			}
			return values, nil
		}
		if anchor {
			break
		}
	}
	return []any{nil}, nil
}
func patternGMatch(s *State, a []any) ([]any, error) {
	text, pat, err := patternArgs("gmatch", a)
	if err != nil {
		return nil, err
	}
	pos, err := patternStart("gmatch", a, 2, len(text))
	if err != nil {
		return nil, err
	}
	last := -1
	iterator := NativeFunction(func(s *State, _ []any) ([]any, error) {
		m := luaPattern{state: s, text: text, pattern: pat}
		for pos <= len(text) {
			start := pos
			end, c, ok, e := m.match(start, 0, patternCaptures{}, 0)
			if e != nil {
				return nil, e
			}
			if ok && end != last {
				v, e := m.captures(c, start, end)
				if e != nil {
					return nil, e
				}
				pos = end
				last = end
				return v, nil
			}
			pos++
		}
		return nil, nil
	})
	return []any{iterator}, nil
}

// patternBuffer stops writes before the library's native result-size limit.
// Errors are sticky so helpers cannot accidentally allocate after a failed write.
type patternBuffer struct {
	strings.Builder
	err  error
	name string
}

func (b *patternBuffer) WriteString(text string) (int, error) {
	if b.err == nil {
		b.err = libWriteString(&b.Builder, text, b.name)
	}
	if b.err != nil {
		return 0, b.err
	}
	return len(text), nil
}
func (b *patternBuffer) WriteByte(c byte) error {
	if b.err == nil && b.Len() == libMaxBytes {
		b.err = valueError("%s: result exceeds 16 MiB limit", b.name)
	}
	if b.err != nil {
		return b.err
	}
	return b.Builder.WriteByte(c)
}
func (b *patternBuffer) Write(p []byte) (int, error) { return b.WriteString(string(p)) }

func patternReplacement(s *State, m *luaPattern, repl any, c patternCaptures, start, end int) (string, error) {
	switch r := repl.(type) {
	case string, int64, float64:
		text := luaString(r)
		b := patternBuffer{name: "string.gsub"}
		for i := 0; i < len(text) && b.err == nil; i++ {
			if text[i] != '%' {
				b.WriteByte(text[i])
				continue
			}
			i++
			if i == len(text) {
				return "", valueError("invalid use of '%%' in replacement string")
			}
			switch ch := text[i]; {
			case ch == '%':
				b.WriteByte('%')
			case ch == '0':
				b.WriteString(m.text[start:end])
			case ch >= '1' && ch <= '9':
				v, err := m.capture(c, int(ch-'1'), start, end)
				if err != nil {
					return "", err
				}
				b.WriteString(luaString(v))
			default:
				return "", valueError("invalid use of '%%' in replacement string")
			}
		}
		return b.String(), b.err
	default:
		var v any
		var err error
		if t, ok := repl.(*Table); ok {
			key, e := m.capture(c, 0, start, end)
			if e != nil {
				return "", e
			}
			v, err = s.get(t, key)
		} else {
			vals, e := m.captures(c, start, end)
			if e != nil {
				return "", e
			}
			var out []any
			out, err = s.call(repl, vals)
			if len(out) > 0 {
				v = out[0]
			}
		}
		if err != nil {
			return "", err
		}
		if v == nil || v == false {
			return m.text[start:end], nil
		}
		switch v.(type) {
		case string, int64, float64:
			return luaString(v), nil
		}
		return "", valueError("invalid replacement value (a %s)", typeName(v))
	}
}
func patternGSub(s *State, a []any) ([]any, error) {
	text, pat, err := patternArgs("gsub", a)
	if err != nil {
		return nil, err
	}
	repl := libAt(a, 2)
	switch repl.(type) {
	case string, int64, float64, *Table:
	default:
		if !isFunction(repl) {
			return nil, libArg("gsub", 3, "string/function/table expected")
		}
	}
	limit, err := libOptInt("gsub", a, 3, int64(len(text))+1)
	if err != nil {
		return nil, err
	}
	m := luaPattern{state: s, text: text, pattern: pat}
	p := 0
	anchor := strings.HasPrefix(pat, "^")
	if anchor {
		p++
	}
	pos, last, count := 0, -1, int64(0)
	b := patternBuffer{name: "string.gsub"}
	for count < limit && pos <= len(text) && b.err == nil {
		end, c, ok, e := m.match(pos, p, patternCaptures{}, 0)
		if e != nil {
			return nil, e
		}
		if ok && end != last {
			r, e := patternReplacement(s, &m, repl, c, pos, end)
			if e != nil {
				return nil, e
			}
			b.WriteString(r)
			count++
			pos = end
			last = end
		} else if pos < len(text) {
			b.WriteByte(text[pos])
			pos++
		} else {
			break
		}
		if anchor {
			break
		}
	}
	b.WriteString(text[pos:])
	if b.err != nil {
		return nil, b.err
	}
	return []any{b.String(), count}, nil
}

// openPatternLibrary is called after the basic string library is populated.
func (s *State) openPatternLibrary(lib *Table) {
	functions := map[string]NativeFunction{
		"find":   func(s *State, a []any) ([]any, error) { return patternFind(s, a, true) },
		"match":  func(s *State, a []any) ([]any, error) { return patternFind(s, a, false) },
		"gmatch": patternGMatch, "gsub": patternGSub, "format": patternFormat,
	}
	for name, fn := range functions {
		_ = lib.RawSet(name, fn)
	}
}

func patternQuote(text string) string {
	b := patternBuffer{name: "string.format"}
	b.WriteByte('"')
	for i := 0; i < len(text) && b.err == nil; i++ {
		c := text[i]
		switch c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n':
			b.WriteString("\\\n")
		default:
			if c < 32 || c == 127 {
				if i+1 < len(text) && text[i+1] >= '0' && text[i+1] <= '9' {
					fmt.Fprintf(&b, "\\%03d", c)
				} else {
					fmt.Fprintf(&b, "\\%d", c)
				}
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// patternHexFloat follows C %a's exponent and subnormal representation.
func patternHexFloat(n float64, precision int, alternate, upper bool) string {
	bits := math.Float64bits(n)
	fraction := bits & ((uint64(1) << 52) - 1)
	exponent := int((bits >> 52) & 2047)
	if exponent != 0 {
		fraction |= uint64(1) << 52
		exponent -= 1023
	} else if fraction != 0 {
		exponent = -1022
	}
	if precision >= 0 && precision < 13 {
		shift := uint(52 - precision*4)
		low := fraction & ((uint64(1) << shift) - 1)
		whole := fraction >> shift
		half := uint64(1) << (shift - 1)
		if low > half || low == half && whole&1 != 0 {
			whole++
		}
		fraction = whole << shift
	}
	digits := fmt.Sprintf("%014x", fraction)
	whole, tail := digits[:1], digits[1:]
	if precision < 0 {
		tail = strings.TrimRight(tail, "0")
	} else if precision <= 13 {
		tail = tail[:precision]
	} else {
		tail += strings.Repeat("0", precision-13)
	}
	point := ""
	if tail != "" || alternate {
		point = "."
	}
	exp := strconv.Itoa(exponent)
	if exponent >= 0 {
		exp = "+" + exp
	}
	text := "0x" + whole + point + tail + "p" + exp
	if upper {
		text = strings.ToUpper(text)
	}
	return text
}
func patternNumberPadding(text, flags string, width int, negative, finite bool) string {
	sign := ""
	if negative {
		sign = "-"
	} else if strings.Contains(flags, "+") {
		sign = "+"
	} else if strings.Contains(flags, " ") {
		sign = " "
	}
	padding := width - len(sign) - len(text)
	if padding <= 0 {
		return sign + text
	}
	if strings.Contains(flags, "-") {
		return sign + text + strings.Repeat(" ", padding)
	}
	if strings.Contains(flags, "0") && finite {
		prefix := ""
		if strings.HasPrefix(text, "0x") || strings.HasPrefix(text, "0X") {
			prefix, text = text[:2], text[2:]
		}
		return sign + prefix + strings.Repeat("0", padding) + text
	}
	return strings.Repeat(" ", padding) + sign + text
}

// Formatting is parsed one conversion at a time: Go fmt never receives a
// user-controlled verb or dynamic width. Lua-specific conversions are explicit.
func patternFormat(s *State, a []any) ([]any, error) {
	format, err := libString("format", a, 0)
	if err != nil {
		return nil, err
	}
	b := patternBuffer{name: "string.format"}
	arg := 1
	for i := 0; i < len(format) && b.err == nil; i++ {
		if format[i] != '%' {
			b.WriteByte(format[i])
			continue
		}
		i++
		if i == len(format) {
			return nil, valueError("invalid format (ends with '%%')")
		}
		if format[i] == '%' {
			b.WriteByte('%')
			continue
		}
		start := i
		for i < len(format) && strings.ContainsRune("-+ #0", rune(format[i])) {
			i++
		}
		flagsEnd := i
		for i < len(format) && format[i] >= '0' && format[i] <= '9' {
			i++
		}
		if i-flagsEnd > 2 {
			return nil, valueError("invalid format (width too long)")
		}
		if i < len(format) && format[i] == '.' {
			i++
			p := i
			for i < len(format) && format[i] >= '0' && format[i] <= '9' {
				i++
			}
			if i-p > 2 {
				return nil, valueError("invalid format (precision too long)")
			}
		}
		if i == len(format) {
			return nil, valueError("invalid format")
		}
		verb := format[i]
		if i-start+1 >= 22 {
			return nil, valueError("invalid format (too long)")
		}
		flags := format[start:flagsEnd]
		allowed := ""
		switch verb {
		case 'c', 's':
			allowed = "-"
		case 'd', 'i':
			allowed = "-+0 "
		case 'u':
			allowed = "-0"
		case 'o', 'x', 'X':
			allowed = "-#0"
		case 'a', 'A', 'e', 'E', 'f', 'g', 'G':
			allowed = "-+#0 "
		case 'q':
		default:
			return nil, valueError("unsupported conversion '%%%c'", verb)
		}
		for _, flag := range flags {
			if !strings.ContainsRune(allowed, flag) {
				return nil, valueError("invalid conversion specification")
			}
		}
		modifiers := format[flagsEnd:i]
		parts := strings.SplitN(modifiers, ".", 2)
		width := 0
		if parts[0] != "" {
			width, _ = strconv.Atoi(parts[0])
		}
		precision := -1
		if len(parts) == 2 {
			precision = 0
			if parts[1] != "" {
				precision, _ = strconv.Atoi(parts[1])
			}
		}
		if verb == 'c' && precision >= 0 {
			return nil, valueError("invalid character format")
		}
		spec := "%" + format[start:i]
		var v any
		if arg >= len(a) {
			return nil, libArg("format", arg+1, "no value")
		}
		switch verb {
		case 'q':
			if i != start {
				return nil, valueError("specifier '%%q' cannot have modifiers")
			}
			switch x := a[arg].(type) {
			case string:
				// Measure escaped output before building an intermediate quoted string.
				size := 2
				for j := 0; j < len(x); j++ {
					n := 1
					c := x[j]
					switch {
					case c == '"' || c == '\\' || c == '\n':
						n = 2
					case c < 32 || c == 127:
						n = 2
						if c >= 10 {
							n = 3
						}
						if c >= 100 || j+1 < len(x) && x[j+1] >= '0' && x[j+1] <= '9' {
							n = 4
						}
					}
					if n > libMaxBytes-b.Len()-size {
						return nil, valueError("string.format: result exceeds 16 MiB limit")
					}
					size += n
				}
				if size > libMaxBytes-b.Len() {
					return nil, valueError("string.format: result exceeds 16 MiB limit")
				}
				b.WriteString(patternQuote(x))
			case nil:
				b.WriteString("nil")
			case bool:
				b.WriteString(strconv.FormatBool(x))
			case int64:
				if x == math.MinInt64 {
					b.WriteString("0x8000000000000000")
				} else {
					b.WriteString(strconv.FormatInt(x, 10))
				}
			case float64:
				switch {
				case math.IsNaN(x):
					b.WriteString("(0/0)")
				case math.IsInf(x, 1):
					b.WriteString("1e9999")
				case math.IsInf(x, -1):
					b.WriteString("-1e9999")
				default:
					quoted := patternHexFloat(x, -1, false, false)
					if math.Signbit(x) {
						quoted = "-" + quoted
					}
					b.WriteString(quoted)
				}
			default:
				return nil, libArg("format", arg+1, "value has no literal form")
			}
			arg++
			continue
		case 's':
			v, err = libToString(s, a[arg])
		case 'c':
			var n int64
			n, err = libInt("format", a, arg)
			v = string([]byte{byte(n)})
			verb = 's'
		case 'd', 'i', 'o', 'u', 'x', 'X':
			n, e := libInt("format", a, arg)
			if e != nil {
				return nil, e
			}
			negative := n < 0 && (verb == 'd' || verb == 'i')
			unsigned := uint64(n)
			if negative {
				unsigned = uint64(-(n + 1)) + 1
			}
			base := 10
			if verb == 'o' {
				base = 8
			} else if verb == 'x' || verb == 'X' {
				base = 16
			}
			digits := strconv.FormatUint(unsigned, base)
			if verb == 'X' {
				digits = strings.ToUpper(digits)
			}
			if precision == 0 && unsigned == 0 {
				digits = ""
			}
			if precision > len(digits) {
				digits = strings.Repeat("0", precision-len(digits)) + digits
			}
			if strings.Contains(flags, "#") {
				if verb == 'o' && (digits == "" || digits[0] != '0') {
					digits = "0" + digits
				}
				if base == 16 && unsigned != 0 {
					prefix := "0x"
					if verb == 'X' {
						prefix = "0X"
					}
					digits = prefix + digits
				}
			}
			if precision >= 0 {
				flags = strings.ReplaceAll(flags, "0", "")
			}
			b.WriteString(patternNumberPadding(digits, flags, width, negative, true))
			arg++
			continue
		case 'a', 'A', 'e', 'E', 'f', 'g', 'G':
			v, err = libNumber("format", a, arg)
			if err != nil {
				return nil, err
			}
			n := v.(float64)
			if verb == 'a' || verb == 'A' || math.IsInf(n, 0) || math.IsNaN(n) {
				finite := !math.IsInf(n, 0) && !math.IsNaN(n)
				text := "inf"
				if math.IsNaN(n) {
					text = "nan"
				}
				if verb == 'A' || verb == 'E' || verb == 'G' {
					text = strings.ToUpper(text)
				}
				if finite {
					text = patternHexFloat(n, precision, strings.Contains(flags, "#"), verb == 'A')
				}
				b.WriteString(patternNumberPadding(text, flags, width, math.Signbit(n), finite))
				arg++
				continue
			}
			if !strings.Contains(spec, ".") && strings.ContainsRune("eEfgG", rune(verb)) {
				spec += ".6"
			}
		default:
			return nil, valueError("invalid conversion '%%%c'", verb)
		}
		if err != nil {
			return nil, err
		}
		if verb == 's' {
			// Lua widths and precision count bytes, unlike Go fmt's rune counts.
			left := strings.Contains(flags, "-")
			text := v.(string)
			if strings.ContainsRune(text, 0) && spec != "%" && format[i] == 's' {
				return nil, valueError("string contains zeros")
			}
			if len(parts) == 2 {
				if format[i] == 'c' {
					return nil, valueError("invalid character format")
				}

				if len(text) > precision {
					text = text[:precision]
				}
			}
			padding := ""
			if width > len(text) {
				padding = strings.Repeat(" ", width-len(text))
			}
			if !left {
				b.WriteString(padding)
			}
			b.WriteString(text)
			if left {
				b.WriteString(padding)
			}
		} else {

			b.WriteString(fmt.Sprintf(spec+string(verb), v))
		}
		arg++
	}
	if b.err != nil {
		return nil, b.err
	}
	return []any{b.String()}, nil
}
