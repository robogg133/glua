package vm

import (
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/robogg133/glua/compile"
	"github.com/robogg133/glua/lex"
	"github.com/robogg133/glua/parser"
	"github.com/robogg133/glua/tokens"
)

// These are per-operation bounds, not a substitute for State.Context/MaxSteps
// or a total VM memory quota. Results use 16-byte Go interfaces; table entries
// are conservatively budgeted at 64 bytes each.
const libMaxBytes = 16 << 20
const libMaxResults = libMaxBytes / 16
const libMaxEntries = libMaxBytes / 64
const libMaxSourceTokens = 65536

// OpenLibraries installs the supported standard libraries. It deliberately does
// not call ensureInit: initialization itself calls OpenLibraries.
func (s *State) OpenLibraries() error {
	if s.Globals == nil {
		s.Globals = NewTable()
	}
	put := func(t *Table, name string, v any) { _ = t.RawSet(name, v) }
	base := map[string]NativeFunction{}
	var nextFunction any
	base["type"] = func(_ *State, a []any) ([]any, error) {
		if len(a) == 0 {
			return nil, libArg("type", 1, "value expected")
		}
		return []any{typeName(a[0])}, nil
	}
	base["tostring"] = func(s *State, a []any) ([]any, error) {
		if len(a) == 0 {
			return nil, libArg("tostring", 1, "value expected")
		}
		v, e := libToString(s, a[0])
		return []any{v}, e
	}
	base["print"] = func(s *State, a []any) ([]any, error) {
		w := s.Output
		if w == nil {
			w = io.Discard
		}
		for i, v := range a {
			x, e := libToString(s, v)
			if e != nil {
				return nil, e
			}
			if i > 0 {
				if _, e = io.WriteString(w, "\t"); e != nil {
					return nil, e
				}
			}
			if _, e = io.WriteString(w, x); e != nil {
				return nil, e
			}
		}
		_, e := io.WriteString(w, "\n")
		return nil, e
	}
	base["assert"] = func(_ *State, a []any) ([]any, error) {
		if len(a) == 0 {
			return nil, libArg("assert", 1, "value expected")
		}
		if truth(a[0]) {
			return a, nil
		}
		v := any("assertion failed!")
		if len(a) > 1 {
			v = a[1]
		}
		return nil, &LuaError{Value: v}
	}
	base["error"] = func(_ *State, a []any) ([]any, error) {
		if len(a) == 0 {
			return nil, libArg("error", 1, "value expected")
		}
		if len(a) > 1 && a[1] != nil {
			if _, e := libInt("error", a, 1); e != nil {
				return nil, e
			}
		}
		return nil, &LuaError{Value: a[0]}
	}
	base["pcall"] = func(s *State, a []any) ([]any, error) {
		if len(a) == 0 {
			return nil, libArg("pcall", 1, "value expected")
		}
		r, e := s.call(a[0], a[1:])
		if e != nil {
			return []any{false, libErrorValue(e)}, nil
		}
		return libProtectedResults(r)
	}
	base["xpcall"] = func(s *State, a []any) ([]any, error) {
		if len(a) < 2 {
			return nil, libArg("xpcall", 2, "function expected")
		}
		switch a[1].(type) {
		case NativeFunction, *nativeFunction, *Closure:
		default:
			return nil, libArg("xpcall", 2, "function expected")
		}
		r, e := s.call(a[0], a[2:])
		if e == nil {
			return libProtectedResults(r)
		}
		h, he := s.call(a[1], []any{libErrorValue(e)})
		if he != nil {
			return []any{false, "error in error handling"}, nil
		}
		return []any{false, libAt(h, 0)}, nil
	}
	base["tonumber"] = func(_ *State, a []any) ([]any, error) {
		if len(a) == 0 {
			return nil, libArg("tonumber", 1, "value expected")
		}
		if len(a) < 2 || a[1] == nil {
			switch v := a[0].(type) {
			case int64, float64:
				return []any{v}, nil
			case string:
				if n, ok := parseNumber(v); ok {
					return []any{n}, nil
				}
			}
			return []any{nil}, nil
		}
		b, e := libInt("tonumber", a, 1)
		if e != nil {
			return nil, e
		}
		if b < 2 || b > 36 {
			return nil, libArg("tonumber", 2, "base out of range")
		}
		v, ok := a[0].(string)
		if !ok {
			return nil, libArg("tonumber", 1, "string expected")
		}
		v = strings.TrimSpace(v)
		neg := false
		if strings.HasPrefix(v, "-") || strings.HasPrefix(v, "+") {
			neg = v[0] == '-'
			v = v[1:]
		}
		if v == "" {
			return []any{nil}, nil
		}
		var n uint64
		for _, c := range v {
			d := int64(-1)
			switch {
			case c >= '0' && c <= '9':
				d = int64(c - '0')
			case c >= 'a' && c <= 'z':
				d = int64(c-'a') + 10
			case c >= 'A' && c <= 'Z':
				d = int64(c-'A') + 10
			}
			if d < 0 || d >= b {
				return []any{nil}, nil
			}
			n = n*uint64(b) + uint64(d)
		}
		if neg {
			n = 0 - n
		}
		return []any{int64(n)}, nil
	}
	base["select"] = func(_ *State, a []any) ([]any, error) {
		if len(a) == 0 {
			return nil, libArg("select", 1, "index expected")
		}
		if a[0] == "#" {
			return []any{int64(len(a) - 1)}, nil
		}
		i, e := libInt("select", a, 0)
		if e != nil {
			return nil, e
		}
		n := int64(len(a) - 1)
		if i < 0 {
			i = n + i + 1
		}
		if i < 1 {
			return nil, libArg("select", 1, "index out of range")
		}
		if i > n {
			return nil, nil
		}
		return a[i:], nil
	}
	base["next"] = libNext
	base["pairs"] = func(s *State, a []any) ([]any, error) {
		if len(a) == 0 {
			return nil, libArg("pairs", 1, "value expected")
		}
		if m := s.metamethod(a[0], "__pairs"); m != nil {
			r, e := s.call(m, []any{a[0]})
			if e != nil {
				return nil, e
			}
			// Lua 5.5 pairs has a fourth, to-be-closed iterator value.
			return []any{libAt(r, 0), libAt(r, 1), libAt(r, 2), libAt(r, 3)}, nil
		}
		return []any{nextFunction, a[0], nil, nil}, nil
	}
	base["ipairs"] = func(_ *State, a []any) ([]any, error) {
		if len(a) == 0 {
			return nil, libArg("ipairs", 1, "value expected")
		}
		iter := NativeFunction(func(s *State, a []any) ([]any, error) {
			if len(a) < 2 {
				return nil, libArg("ipairs iterator", 2, "integer expected")
			}
			i, e := libInt("ipairs iterator", a, 1)
			if e != nil {
				return nil, e
			}
			i++ // Lua integer addition wraps, including this iterator's control value.
			v, e := s.get(a[0], i)
			if e != nil {
				return nil, e
			}
			if v == nil {
				return []any{nil}, nil
			}
			return []any{i, v}, nil
		})
		return []any{iter, a[0], int64(0)}, nil
	}
	base["rawget"] = func(_ *State, a []any) ([]any, error) {
		t, e := libTable("rawget", a, 0)
		if e != nil {
			return nil, e
		}
		if len(a) < 2 {
			return nil, libArg("rawget", 2, "value expected")
		}
		return []any{t.RawGet(a[1])}, nil
	}
	base["rawset"] = func(_ *State, a []any) ([]any, error) {
		t, e := libTable("rawset", a, 0)
		if e != nil {
			return nil, e
		}
		if len(a) < 3 {
			return nil, libArg("rawset", len(a)+1, "value expected")
		}
		if e = t.RawSet(a[1], a[2]); e != nil {
			return nil, e
		}
		return []any{t}, nil
	}
	base["rawlen"] = func(_ *State, a []any) ([]any, error) {
		switch v := libAt(a, 0).(type) {
		case string:
			return []any{int64(len(v))}, nil
		case *Table:
			return []any{v.Len()}, nil
		}
		return nil, libArg("rawlen", 1, "table or string expected")
	}
	base["rawequal"] = func(_ *State, a []any) ([]any, error) {
		if len(a) < 2 {
			return nil, libArg("rawequal", len(a)+1, "value expected")
		}
		return []any{libEqual(a[0], a[1])}, nil
	}
	base["getmetatable"] = func(s *State, a []any) ([]any, error) {
		if len(a) == 0 {
			return nil, libArg("getmetatable", 1, "value expected")
		}
		m := s.TypeMetatables[typeName(a[0])]
		if t, ok := a[0].(*Table); ok && t != nil {
			m = t.Metatable
		}
		if m != nil {
			if p := m.RawGet("__metatable"); p != nil {
				return []any{p}, nil
			}
			return []any{m}, nil
		}
		return []any{nil}, nil
	}
	base["setmetatable"] = func(_ *State, a []any) ([]any, error) {
		t, e := libTable("setmetatable", a, 0)
		if e != nil {
			return nil, e
		}
		if len(a) < 2 {
			return nil, libArg("setmetatable", 2, "nil or table expected")
		}
		var m *Table
		if a[1] != nil {
			var ok bool
			m, ok = a[1].(*Table)
			if !ok {
				return nil, libArg("setmetatable", 2, "nil or table expected")
			}
		}
		if t.Metatable != nil && t.Metatable.RawGet("__metatable") != nil {
			return nil, fmt.Errorf("cannot change a protected metatable")
		}
		t.Metatable = m
		return []any{t}, nil
	}
	base["collectgarbage"] = func(_ *State, a []any) ([]any, error) {
		mode := "collect"
		if libAt(a, 0) != nil {
			var e error
			mode, e = libString("collectgarbage", a, 0)
			if e != nil {
				return nil, e
			}
		}
		switch mode {
		case "collect":
			runtime.GC()
			return []any{int64(0)}, nil
		case "count":
			// Go heap usage, not a per-State Lua allocation counter.
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			return []any{float64(m.Alloc) / 1024}, nil
		default:
			return nil, fmt.Errorf("collectgarbage: mode %q unsupported by Go GC", mode)
		}
	}
	base["load"] = libLoad
	base["loadfile"] = libLoadFile
	base["dofile"] = func(s *State, a []any) ([]any, error) {
		r, e := libLoadFile(s, []any{libAt(a, 0)})
		if e != nil {
			return nil, e
		}
		if r[0] == nil {
			return nil, &LuaError{Value: r[1]}
		}
		return s.call(r[0], nil)
	}
	for n, f := range base {
		put(s.Globals, n, f)
	}
	nextFunction = s.Globals.RawGet("next")
	put(s.Globals, "_VERSION", "Lua 5.5")
	put(s.Globals, "_G", s.Globals)
	for name, funcs := range map[string]map[string]NativeFunction{"math": libMath(), "table": libTables(), "string": libStrings(), "utf8": libUTF8()} {
		t := NewTable()
		for n, f := range funcs {
			put(t, n, f)
		}
		put(s.Globals, name, t)
	}
	strlib := s.Globals.RawGet("string").(*Table)
	s.openPatternLibrary(strlib)
	if s.TypeMetatables == nil {
		s.TypeMetatables = make(map[string]*Table)
	}
	stringMeta := s.TypeMetatables["string"]
	if stringMeta == nil {
		stringMeta = NewTable()
		s.TypeMetatables["string"] = stringMeta
	}
	put(stringMeta, "__index", strlib)
	s.openPackageLibrary()
	m := s.Globals.RawGet("math").(*Table)
	for n, v := range map[string]any{"pi": math.Pi, "huge": math.Inf(1), "maxinteger": int64(math.MaxInt64), "mininteger": int64(math.MinInt64)} {
		put(m, n, v)
	}
	u := s.Globals.RawGet("utf8").(*Table)
	put(u, "charpattern", "[\x00-\x7f\xc2-\xf4][\x80-\xbf]*")
	return nil
}

// load/loadfile restore the API stack even when called from an active chunk.
// Only text chunks are supported; no compatible binary chunk decoder exists.
func libCompileSource(s *State, source, name, mode string, env any, hasEnv bool) ([]any, error) {
	fail := func(e error) ([]any, error) { return []any{nil, libErrorValue(e)}, nil }
	if len(name) > 256 {
		name = name[:256] + "..."
	}
	if len(source) > libMaxBytes {
		return fail(fmt.Errorf("%s: source exceeds 16 MiB limit", name))
	}
	if strings.HasPrefix(source, "\x1b") {
		return fail(fmt.Errorf("%s: binary chunks unsupported", name))
	}
	if !strings.Contains(mode, "t") {
		return fail(fmt.Errorf("%s: attempt to load a text chunk (mode is %q)", name, mode))
	}
	if strings.IndexByte(source, 0) >= 0 {
		return fail(fmt.Errorf("%s: unexpected NUL byte in source", name))
	}
	// A byte limit alone permits millions of tiny AST nodes. Count tokens
	// without retaining them before allowing the parser/compiler to allocate.
	if e := libSourceBudget(s, source, name); e != nil {
		return fail(e)
	}
	ast := parser.NewAst(tokens.NewTokenizer(lex.NewLexer(source), name))
	if e := ast.Next(); e != nil {
		return fail(fmt.Errorf("%s: %w", name, e))
	}
	proto, e := compile.Compile(ast)
	if e != nil {
		return fail(fmt.Errorf("%s: %w", name, e))
	}
	top := s.GetTop()
	defer s.SetTop(top)
	if e = s.Load(proto); e != nil {
		return fail(e)
	}
	closure, ok := s.At(-1).(*Closure)
	if !ok {
		return nil, fmt.Errorf("load: core did not push a closure")
	}
	if hasEnv && len(closure.upvalues) > 0 {
		closure.upvalues[0].set(env)
	}
	return []any{closure}, nil
}
func libSourceBudget(s *State, source, name string) error {
	tokenizer := tokens.NewTokenizer(lex.NewLexer(source), name)
	for count := 0; ; count++ {
		if s.Context != nil {
			if e := s.Context.Err(); e != nil {
				return e
			}
		}
		token, e := tokenizer.NextChecked()
		if e != nil {
			return e
		}
		if token.Type == tokens.TkEos {
			return nil
		}
		if count == libMaxSourceTokens {
			return fmt.Errorf("%s: source token limit exceeded (%d)", name, libMaxSourceTokens)
		}
	}
}
func libProtectedResults(r []any) ([]any, error) {
	if len(r) >= libMaxResults {
		return nil, fmt.Errorf("protected call: too many results (16 MiB limit)")
	}
	results := make([]any, len(r)+1)
	results[0] = true
	copy(results[1:], r)
	return results, nil
}
func libLoad(s *State, a []any) ([]any, error) {
	if len(a) == 0 {
		return nil, libArg("load", 1, "string or function expected")
	}
	source, isText := a[0].(string)
	if !isText {
		switch a[0].(type) {
		case int64, float64:
			source = luaString(a[0])
			isText = true
		}
	}
	if !isText && !isFunction(a[0]) {
		return nil, libArg("load", 1, "string or function expected")
	}
	name := "=(load)"
	if isText {
		name = source
	}
	var e error
	if libAt(a, 1) != nil {
		name, e = libString("load", a, 1)
		if e != nil {
			return nil, e
		}
	}
	mode := "bt"
	if libAt(a, 2) != nil {
		mode, e = libString("load", a, 2)
		if e != nil {
			return nil, e
		}
	}
	if len(name) > libMaxBytes {
		return []any{nil, "load: chunk name exceeds 16 MiB limit"}, nil
	}
	if !isText {
		var b strings.Builder
		for {
			r, e := s.call(a[0], nil)
			if e != nil {
				return []any{nil, libErrorValue(e)}, nil
			}
			v := libAt(r, 0)
			if v == nil {
				break
			}
			part, e := libString("load reader", []any{v}, 0)
			if e != nil {
				return []any{nil, "reader function must return a string"}, nil
			}
			if part == "" {
				break
			}
			if e = libWriteString(&b, part, "load"); e != nil {
				return []any{nil, e.Error()}, nil
			}
		}
		source = b.String()
	}
	return libCompileSource(s, source, name, mode, libAt(a, 3), len(a) > 3)
}
func libLoadFile(s *State, a []any) ([]any, error) {
	mode := "bt"
	var e error
	if libAt(a, 1) != nil {
		mode, e = libString("loadfile", a, 1)
		if e != nil {
			return nil, e
		}
	}
	name, chunkName := "stdin", "=stdin"
	var source string
	if libAt(a, 0) == nil {
		source, e = libReadChunk(os.Stdin, name)
	} else {
		name, e = libString("loadfile", a, 0)
		if e != nil {
			return nil, e
		}
		chunkName = "@" + name
		source, e = libReadSource(name)
	}
	if e != nil {
		return []any{nil, e.Error()}, nil
	}
	source = strings.TrimPrefix(source, "\xef\xbb\xbf")
	if strings.HasPrefix(source, "#") {
		if at := strings.IndexByte(source, '\n'); at >= 0 {
			tail := source[at+1:]
			if strings.HasPrefix(tail, "\x1b") {
				return []any{nil, "loadfile: binary chunks unsupported"}, nil
			}
			source = "\n" + tail
		} else {
			source = ""
		}
	}
	return libCompileSource(s, source, chunkName, mode, libAt(a, 2), len(a) > 2)
}
func libReadSource(name string) (string, error) {
	if len(name) > libMaxBytes {
		return "", fmt.Errorf("loadfile: filename exceeds 16 MiB limit")
	}
	f, e := os.Open(name)
	if e != nil {
		return "", fmt.Errorf("cannot open %s: %w", name, e)
	}
	defer f.Close()
	return libReadChunk(f, name)
}
func libReadChunk(reader io.Reader, name string) (string, error) {
	var b strings.Builder
	buf := make([]byte, 32<<10)
	for {
		n, e := reader.Read(buf)
		if n > 0 {
			if e := libGrow(&b, n, "loadfile"); e != nil {
				return "", e
			}
			b.Write(buf[:n])
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", fmt.Errorf("cannot read %s: %w", name, e)
		}
	}
	return b.String(), nil
}
func libGrow(b *strings.Builder, n int, name string) error {
	if n > libMaxBytes-b.Len() {
		return fmt.Errorf("%s: result exceeds 16 MiB limit", name)
	}
	if n > b.Cap()-b.Len() {
		// Builder's implicit doubling can allocate beyond the result-size limit.
		capacity := max(b.Len()+n, min(2*b.Cap(), libMaxBytes))
		old := b.String()
		b.Reset()
		b.Grow(capacity)
		b.WriteString(old)
	}
	return nil
}
func libWriteString(b *strings.Builder, text, name string) error {
	if e := libGrow(b, len(text), name); e != nil {
		return e
	}
	b.WriteString(text)
	return nil
}
func libRangeSize(name string, first, last int64, limit uint64) error {
	if last < first {
		return nil
	}
	// Unsigned subtraction avoids overflow even across the signed integer range.
	if uint64(last)-uint64(first) >= limit {
		return fmt.Errorf("%s: range exceeds allocation/operation limit", name)
	}
	return nil
}
func libReplace(text, old, new, name string) (string, error) {
	if len(text) > libMaxBytes {
		return "", fmt.Errorf("%s: input exceeds 16 MiB limit", name)
	}
	if old == "" {
		return text, nil
	}
	if extra := len(new) - len(old); extra > 0 && strings.Count(text, old) > (libMaxBytes-len(text))/extra {
		return "", fmt.Errorf("%s: result exceeds 16 MiB limit", name)
	}
	return strings.ReplaceAll(text, old, new), nil
}

// package supports preload and Lua-source modules. C/shared-library searchers
// are deliberately absent. Paths are relative to the process working directory.
func (s *State) openPackageLibrary() {
	pkg, loaded, preload, searchers := NewTable(), NewTable(), NewTable(), NewTable()
	put := func(t *Table, k any, v any) { _ = t.RawSet(k, v) }
	put(pkg, "loaded", loaded)
	put(pkg, "preload", preload)
	put(pkg, "searchers", searchers)
	put(pkg, "path", "./?.lua;./?/init.lua")
	put(pkg, "cpath", "")
	put(pkg, "config", string(filepath.Separator)+"\n;\n?\n!\n-\n")
	put(pkg, "searchpath", NativeFunction(libSearchPath))
	put(pkg, "loadlib", NativeFunction(func(_ *State, a []any) ([]any, error) {
		if _, e := libString("package.loadlib", a, 0); e != nil {
			return nil, e
		}
		if _, e := libString("package.loadlib", a, 1); e != nil {
			return nil, e
		}
		return []any{nil, "package.loadlib: native shared libraries unsupported", "absent"}, nil
	}))
	put(searchers, int64(1), NativeFunction(func(s *State, a []any) ([]any, error) {
		name, e := libString("preload searcher", a, 0)
		if e != nil {
			return nil, e
		}
		if len(name) > libMaxBytes-64 {
			return nil, fmt.Errorf("preload searcher: module name exceeds result limit")
		}
		f, e := s.get(preload, name)
		if e != nil {
			return nil, e
		}
		if f != nil {
			return []any{f, ":preload:"}, nil
		}
		return []any{"\n\tno field package.preload['" + name + "']"}, nil
	}))
	put(searchers, int64(2), NativeFunction(func(s *State, a []any) ([]any, error) {
		name, e := libString("Lua searcher", a, 0)
		if e != nil {
			return nil, e
		}
		pathValue, e := s.get(pkg, "path")
		if e != nil {
			return nil, e
		}
		path, e := libString("package.path", []any{pathValue}, 0)
		if e != nil {
			return nil, e
		}
		found, e := libSearchPath(s, []any{name, path})
		if e != nil {
			return nil, e
		}
		if found[0] == nil {
			return []any{found[1]}, nil
		}
		r, e := libLoadFile(s, []any{found[0]})
		if e != nil {
			return nil, e
		}
		if r[0] == nil {
			return nil, fmt.Errorf("error loading module %q from file %q: %v", name, found[0], r[1])
		}
		return []any{r[0], found[0]}, nil
	}))
	put(s.Globals, "package", pkg)
	put(s.Globals, "require", NativeFunction(func(s *State, a []any) ([]any, error) {
		name, e := libString("require", a, 0)
		if e != nil {
			return nil, e
		}
		if len(name) > libMaxBytes {
			return nil, fmt.Errorf("require: module name exceeds 16 MiB limit")
		}
		cached, e := s.get(loaded, name)
		if e != nil {
			return nil, e
		}
		if truth(cached) {
			return []any{cached}, nil
		}
		listValue, e := s.get(pkg, "searchers")
		if e != nil {
			return nil, e
		}
		list, e := libTable("package.searchers", []any{listValue}, 0)
		if e != nil {
			return nil, e
		}
		var messages strings.Builder
		for i := int64(1); i <= 1024; i++ {
			searcher := list.RawGet(i)
			if searcher == nil {
				return nil, fmt.Errorf("module %q not found:%s", name, messages.String())
			}
			r, e := s.call(searcher, []any{name})
			if e != nil {
				return nil, e
			}
			loader := libAt(r, 0)
			if isFunction(loader) {
				data := libAt(r, 1)
				results, e := s.call(loader, []any{name, data})
				if e != nil {
					return nil, e
				}
				if v := libAt(results, 0); v != nil {
					if e = s.set(loaded, name, v); e != nil {
						return nil, e
					}
				}
				v, e := s.get(loaded, name)
				if e != nil {
					return nil, e
				}
				if v == nil {
					v = true
					if e = s.set(loaded, name, v); e != nil {
						return nil, e
					}
				}
				return []any{v, data}, nil
			}
			switch loader.(type) {
			case string, int64, float64:
				if e = libWriteString(&messages, luaString(loader), "require"); e != nil {
					return nil, e
				}
			}
		}
		return nil, fmt.Errorf("require: too many searchers (limit 1024)")
	}))
	for _, name := range []string{"_G", "package", "math", "table", "string", "utf8"} {
		put(loaded, name, s.Globals.RawGet(name))
	}
}
func libSearchPath(_ *State, a []any) ([]any, error) {
	name, e := libString("package.searchpath", a, 0)
	if e != nil {
		return nil, e
	}
	path, e := libString("package.searchpath", a, 1)
	if e != nil {
		return nil, e
	}
	sep, rep := ".", string(filepath.Separator)
	if libAt(a, 2) != nil {
		sep, e = libString("package.searchpath", a, 2)
		if e != nil {
			return nil, e
		}
	}
	if libAt(a, 3) != nil {
		rep, e = libString("package.searchpath", a, 3)
		if e != nil {
			return nil, e
		}
	}
	name, e = libReplace(name, sep, rep, "package.searchpath")
	if e != nil {
		return nil, e
	}
	if len(path) > libMaxBytes {
		return nil, fmt.Errorf("package.searchpath: path exceeds 16 MiB limit")
	}
	var messages strings.Builder
	for template := range strings.SplitSeq(path, ";") {
		if template == "" {
			continue
		}
		filename, e := libReplace(template, "?", name, "package.searchpath")
		if e != nil {
			return nil, e
		}
		f, e := os.Open(filename)
		if e == nil {
			f.Close()
			return []any{filename}, nil
		}
		if e = libWriteString(&messages, "\n\tno file '"+filename+"'", "package.searchpath"); e != nil {
			return nil, e
		}
	}
	return []any{nil, messages.String()}, nil
}

func libArg(name string, i int, message string) error {
	return fmt.Errorf("bad argument #%d to '%s' (%s)", i, name, message)
}
func libAt(a []any, i int) any {
	if i >= 0 && i < len(a) {
		return a[i]
	}
	return nil
}
func libTable(n string, a []any, i int) (*Table, error) {
	if t, ok := libAt(a, i).(*Table); ok && t != nil {
		return t, nil
	}
	return nil, libArg(n, i+1, "table expected")
}
func libString(n string, a []any, i int) (string, error) {
	switch v := libAt(a, i).(type) {
	case string:
		return v, nil
	case int64, float64:
		return luaString(v), nil
	}
	return "", libArg(n, i+1, "string expected")
}

func libNumber(n string, a []any, i int) (float64, error) {
	if x, ok := toNumber(libAt(a, i)); ok {
		return x, nil
	}
	return 0, libArg(n, i+1, "number expected")
}
func libInt(n string, a []any, i int) (int64, error) {
	if x, ok := toInteger(libAt(a, i)); ok {
		return x, nil
	}
	if _, ok := toNumber(libAt(a, i)); ok {
		return 0, libArg(n, i+1, "number has no integer representation")
	}
	return 0, libArg(n, i+1, "integer expected")
}
func libOptInt(n string, a []any, i int, d int64) (int64, error) {
	if libAt(a, i) == nil {
		return d, nil
	}
	return libInt(n, a, i)
}
func libErrorValue(e error) any {
	var le *LuaError
	if errors.As(e, &le) {
		return le.Value
	}
	var value LuaError
	if errors.As(e, &value) {
		return value.Value
	}
	return e.Error()
}
func libToString(s *State, v any) (string, error) {
	if m := s.metamethod(v, "__tostring"); m != nil {
		r, e := s.call(m, []any{v})
		if e != nil {
			return "", e
		}
		switch result := libAt(r, 0).(type) {
		case string:
			return result, nil
		case int64, float64:
			return luaString(result), nil
		default:
			return "", fmt.Errorf("'__tostring' must return a string")
		}
	}
	switch v.(type) {
	case *Table, *Closure, *nativeFunction, NativeFunction:
		if name, ok := s.metamethod(v, "__name").(string); ok {
			if len(name) > libMaxBytes-64 {
				return "", fmt.Errorf("tostring: type name exceeds result limit")
			}
			return fmt.Sprintf("%s: %p", name, v), nil
		}
	}
	return luaString(v), nil
}
func libEqual(a, b any) bool {
	if reflect.TypeOf(a) == reflect.TypeOf(b) {
		if a == nil {
			return true
		}
		if reflect.TypeOf(a).Comparable() {
			return a == b
		}
		return false
	}
	if i, ok := a.(int64); ok {
		if f, ok := b.(float64); ok {
			return f >= -0x1p63 && f < 0x1p63 && math.Trunc(f) == f && int64(f) == i
		}
	}
	if _, ok := a.(float64); ok {
		if _, ok := b.(int64); ok {
			return libEqual(b, a)
		}
	}
	return false
}
func libNext(_ *State, a []any) ([]any, error) {
	t, e := libTable("next", a, 0)
	if e != nil {
		return nil, e
	}
	if len(t.values) > libMaxResults {
		return nil, fmt.Errorf("next: key list exceeds 16 MiB limit")
	}
	keys := t.Keys()
	key := libAt(a, 1)
	if key == nil {
		if len(keys) == 0 {
			return []any{nil}, nil
		}
		return []any{keys[0], t.RawGet(keys[0])}, nil
	}
	for i, k := range keys {
		if libEqual(key, k) {
			if i+1 == len(keys) {
				return []any{nil}, nil
			}
			return []any{keys[i+1], t.RawGet(keys[i+1])}, nil
		}
	}
	return nil, fmt.Errorf("invalid key to 'next'")
}

func libMath() map[string]NativeFunction {
	out := map[string]NativeFunction{}
	for name, f := range map[string]func(float64) float64{"acos": math.Acos, "asin": math.Asin, "atan": math.Atan, "cos": math.Cos, "sin": math.Sin, "tan": math.Tan, "sqrt": math.Sqrt, "exp": math.Exp, "deg": func(x float64) float64 { return x * 180 / math.Pi }, "rad": func(x float64) float64 { return x * math.Pi / 180 }} {
		out[name] = func(_ *State, a []any) ([]any, error) {
			x, e := libNumber("math."+name, a, 0)
			if e != nil {
				return nil, e
			}
			return []any{f(x)}, nil
		}
	}
	out["atan"] = func(_ *State, a []any) ([]any, error) {
		x, e := libNumber("math.atan", a, 0)
		if e != nil {
			return nil, e
		}
		y := 1.0
		if libAt(a, 1) != nil {
			y, e = libNumber("math.atan", a, 1)
			if e != nil {
				return nil, e
			}
		}
		return []any{math.Atan2(x, y)}, nil
	}
	out["log"] = func(_ *State, a []any) ([]any, error) {
		x, e := libNumber("math.log", a, 0)
		if e != nil {
			return nil, e
		}
		v := math.Log(x)
		if libAt(a, 1) != nil {
			b, e := libNumber("math.log", a, 1)
			if e != nil {
				return nil, e
			}
			switch b {
			case 2:
				v = math.Log2(x)
			case 10:
				v = math.Log10(x)
			default:
				v /= math.Log(b)
			}
		}
		return []any{v}, nil
	}
	for name, f := range map[string]func(float64) float64{"floor": math.Floor, "ceil": math.Ceil, "abs": math.Abs} {
		out[name] = func(_ *State, a []any) ([]any, error) {
			if i, ok := libAt(a, 0).(int64); ok {
				if name == "abs" && i < 0 {
					i = -i
				}
				return []any{i}, nil
			}
			x, e := libNumber("math."+name, a, 0)
			if e != nil {
				return nil, e
			}
			v := f(x)
			if name != "abs" && v >= -0x1p63 && v < 0x1p63 {
				return []any{int64(v)}, nil
			}
			return []any{v}, nil
		}
	}
	out["tointeger"] = func(_ *State, a []any) ([]any, error) {
		if len(a) == 0 {
			return nil, libArg("math.tointeger", 1, "value expected")
		}
		i, e := libInt("math.tointeger", a, 0)
		if e != nil {
			return []any{nil}, nil
		}
		return []any{i}, nil
	}
	out["type"] = func(_ *State, a []any) ([]any, error) {
		if len(a) == 0 {
			return nil, libArg("math.type", 1, "value expected")
		}
		switch a[0].(type) {
		case int64:
			return []any{"integer"}, nil
		case float64:
			return []any{"float"}, nil
		}
		return []any{nil}, nil
	}
	out["ult"] = func(_ *State, a []any) ([]any, error) {
		x, e := libInt("math.ult", a, 0)
		if e != nil {
			return nil, e
		}
		y, e := libInt("math.ult", a, 1)
		if e != nil {
			return nil, e
		}
		return []any{uint64(x) < uint64(y)}, nil
	}
	out["fmod"] = func(_ *State, a []any) ([]any, error) {
		x, e := libNumber("math.fmod", a, 0)
		if e != nil {
			return nil, e
		}
		y, e := libNumber("math.fmod", a, 1)
		if e != nil {
			return nil, e
		}
		if i, ok := a[0].(int64); ok {
			if j, ok := a[1].(int64); ok {
				if j == 0 {
					return nil, libArg("math.fmod", 2, "zero")
				}
				if j == -1 {
					return []any{int64(0)}, nil
				}
				return []any{i % j}, nil
			}
		}
		return []any{math.Mod(x, y)}, nil
	}
	out["modf"] = func(_ *State, a []any) ([]any, error) {
		if i, ok := libAt(a, 0).(int64); ok {
			return []any{i, float64(0)}, nil
		}
		x, e := libNumber("math.modf", a, 0)
		if e != nil {
			return nil, e
		}
		i, f := math.Modf(x)
		if math.IsInf(x, 0) {
			f = 0
		}
		var v any = i
		if i >= -0x1p63 && i < 0x1p63 {
			v = int64(i)
		}
		return []any{v, f}, nil
	}
	for _, name := range []string{"min", "max"} {
		out[name] = func(s *State, a []any) ([]any, error) {
			if len(a) == 0 {
				return nil, libArg("math."+name, 1, "value expected")
			}
			v := a[0]
			for _, x := range a[1:] {
				left, right := x, v
				if name == "max" {
					left, right = v, x
				}
				b, e := s.compare(compile.OpLT, left, right)
				if e != nil {
					return nil, e
				}
				if b {
					v = x
				}
			}
			return []any{v}, nil
		}
	}
	rng := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	out["randomseed"] = func(_ *State, a []any) ([]any, error) {
		x, y := int64(rand.Uint64()), int64(rand.Uint64())
		var e error
		if len(a) > 0 {
			x, e = libInt("math.randomseed", a, 0)
			if e != nil {
				return nil, e
			}
			y, e = libOptInt("math.randomseed", a, 1, 0)
			if e != nil {
				return nil, e
			}
		}
		rng = rand.New(rand.NewPCG(uint64(x), uint64(y)))
		return []any{x, y}, nil
	}
	out["random"] = func(_ *State, a []any) ([]any, error) {
		if len(a) > 2 {
			return nil, fmt.Errorf("math.random: expected at most 2 arguments")
		}
		if len(a) == 0 {
			return []any{rng.Float64()}, nil
		}
		lo := int64(1)
		hi, e := libInt("math.random", a, 0)
		if e != nil {
			return nil, e
		}
		if len(a) == 1 && hi == 0 {
			return []any{int64(rng.Uint64())}, nil
		}
		if len(a) > 1 {
			lo = hi
			hi, e = libInt("math.random", a, 1)
			if e != nil {
				return nil, e
			}
		}
		if lo > hi {
			return nil, libArg("math.random", 1, "interval is empty")
		}
		span := uint64(hi) - uint64(lo) + 1
		var n uint64
		if span == 0 {
			n = rng.Uint64()
		} else {
			n = rng.Uint64N(span)
		}
		return []any{int64(uint64(lo) + n)}, nil
	}
	return out
}

func libTables() map[string]NativeFunction {
	return map[string]NativeFunction{
		"pack": func(_ *State, a []any) ([]any, error) {
			if len(a) > libMaxEntries {
				return nil, fmt.Errorf("table.pack: too many entries")
			}
			t := NewTable()
			for i, v := range a {
				if e := t.RawSet(int64(i+1), v); e != nil {
					return nil, e
				}
			}
			_ = t.RawSet("n", int64(len(a)))
			return []any{t}, nil
		},
		"unpack": func(s *State, a []any) ([]any, error) {
			if len(a) == 0 {
				return nil, libArg("table.unpack", 1, "table expected")
			}
			i, e := libOptInt("table.unpack", a, 1, 1)
			if e != nil {
				return nil, e
			}
			if e = libTableAccess(s, "table.unpack", a[0], 1, "__index"); e != nil {
				return nil, e
			}
			n := int64(0)
			if libAt(a, 2) == nil {
				n, e = libSequenceLength(s, a[0])
				if e != nil {
					return nil, e
				}
			}
			j, e := libOptInt("table.unpack", a, 2, n)
			if e != nil {
				return nil, e
			}
			if j < i {
				return nil, nil
			}
			if e = libRangeSize("table.unpack", i, j, libMaxResults); e != nil {
				return nil, e
			}
			r := make([]any, 0, int(j-i+1))
			for k := i; ; k++ {
				v, e := s.get(a[0], k)
				if e != nil {
					return nil, e
				}
				r = append(r, v)
				if k == j {
					break
				}
			}
			return r, nil
		},
		"concat": func(s *State, a []any) ([]any, error) {
			if len(a) == 0 {
				return nil, libArg("table.concat", 1, "table expected")
			}
			n, e := libSequenceLength(s, a[0])
			if e != nil {
				return nil, e
			}
			sep := ""
			if libAt(a, 1) != nil {
				sep, e = libString("table.concat", a, 1)
				if e != nil {
					return nil, e
				}
			}
			i, e := libOptInt("table.concat", a, 2, 1)
			if e != nil {
				return nil, e
			}
			j, e := libOptInt("table.concat", a, 3, n)
			if e != nil {
				return nil, e
			}
			if e = libRangeSize("table.concat", i, j, libMaxEntries); e != nil {
				return nil, e
			}
			var b strings.Builder
			for k := i; k <= j; k++ {
				v, e := s.get(a[0], k)
				if e != nil {
					return nil, e
				}
				text, e := libString("table.concat", []any{v}, 0)
				if e != nil {
					return nil, fmt.Errorf("invalid value (%s) at index %d in table for 'concat'", typeName(v), k)
				}
				if k != i {
					if e = libWriteString(&b, sep, "table.concat"); e != nil {
						return nil, e
					}
				}
				if e = libWriteString(&b, text, "table.concat"); e != nil {
					return nil, e
				}
				if k == math.MaxInt64 {
					break
				}
			}
			return []any{b.String()}, nil
		},
		"insert": func(s *State, a []any) ([]any, error) {
			if len(a) != 2 && len(a) != 3 {
				return nil, fmt.Errorf("table.insert: expected 2 or 3 arguments")
			}
			n, e := libSequenceLength(s, a[0])
			if e != nil {
				return nil, e
			}
			if e = libTableAccess(s, "table.insert", a[0], 1, "__newindex"); e != nil {
				return nil, e
			}
			if n < 0 || n >= libMaxEntries {
				return nil, fmt.Errorf("table.insert: invalid or excessive length")
			}
			p, v := n+1, a[1]
			if len(a) == 3 {
				p, e = libInt("table.insert", a, 1)
				if e != nil {
					return nil, e
				}
				v = a[2]
			}
			if p < 1 || p > n+1 {
				return nil, libArg("table.insert", 2, "position out of bounds")
			}
			for k := n; k >= p; k-- {
				x, e := s.get(a[0], k)
				if e != nil {
					return nil, e
				}
				if e = s.set(a[0], k+1, x); e != nil {
					return nil, e
				}
			}
			return nil, s.set(a[0], p, v)
		},
		"remove": func(s *State, a []any) ([]any, error) {
			if len(a) == 0 {
				return nil, libArg("table.remove", 1, "table expected")
			}
			n, e := libSequenceLength(s, a[0])
			if e != nil {
				return nil, e
			}
			if e = libTableAccess(s, "table.remove", a[0], 1, "__newindex"); e != nil {
				return nil, e
			}
			if n < 0 || n > libMaxEntries {
				return nil, fmt.Errorf("table.remove: invalid or excessive length")
			}
			p, e := libOptInt("table.remove", a, 1, n)
			if e != nil {
				return nil, e
			}
			if p != n && (p < 1 || p > n+1) {
				return nil, libArg("table.remove", 2, "position out of bounds")
			}
			v, e := s.get(a[0], p)
			if e != nil {
				return nil, e
			}
			for k := p; k < n; k++ {
				x, e := s.get(a[0], k+1)
				if e != nil {
					return nil, e
				}
				if e = s.set(a[0], k, x); e != nil {
					return nil, e
				}
			}
			if e = s.set(a[0], max(p, n), nil); e != nil {
				return nil, e
			}
			return []any{v}, nil
		},
		"move": func(s *State, a []any) ([]any, error) {
			if len(a) < 4 {
				return nil, libArg("table.move", len(a)+1, "value expected")
			}
			f, e := libInt("table.move", a, 1)
			if e != nil {
				return nil, e
			}
			last, e := libInt("table.move", a, 2)
			if e != nil {
				return nil, e
			}
			target, e := libInt("table.move", a, 3)
			if e != nil {
				return nil, e
			}
			dest := a[0]
			if libAt(a, 4) != nil {
				dest = a[4]
			}
			if e = libTableAccess(s, "table.move", a[0], 1, "__index"); e != nil {
				return nil, e
			}
			if e = libTableAccess(s, "table.move", dest, 5, "__newindex"); e != nil {
				return nil, e
			}
			if last < f {
				return []any{dest}, nil
			}
			delta := uint64(last) - uint64(f)
			if delta >= math.MaxInt64 || target > math.MaxInt64-int64(delta) {
				return nil, fmt.Errorf("table.move: range overflow")
			}
			if e = libRangeSize("table.move", f, last, libMaxEntries); e != nil {
				return nil, e
			}
			if libEqual(dest, a[0]) && target > f && target <= last {
				for d := int64(delta); d >= 0; d-- {
					v, e := s.get(a[0], f+d)
					if e != nil {
						return nil, e
					}
					if e = s.set(dest, target+d, v); e != nil {
						return nil, e
					}
				}
			} else {
				for d := int64(0); ; d++ {
					v, e := s.get(a[0], f+d)
					if e != nil {
						return nil, e
					}
					if e = s.set(dest, target+d, v); e != nil {
						return nil, e
					}
					if d == int64(delta) {
						break
					}
				}
			}
			return []any{dest}, nil
		},
		"sort": func(s *State, a []any) ([]any, error) {
			if len(a) == 0 {
				return nil, libArg("table.sort", 1, "table expected")
			}
			n, e := libSequenceLength(s, a[0])
			if e != nil {
				return nil, e
			}
			if e = libTableAccess(s, "table.sort", a[0], 1, "__newindex"); e != nil {
				return nil, e
			}
			if n < 0 || n > libMaxEntries {
				return nil, fmt.Errorf("table.sort: invalid or excessive length")
			}
			cmp := libAt(a, 1)
			if cmp != nil {
				switch cmp.(type) {
				case NativeFunction, *nativeFunction, *Closure:
				default:
					return nil, libArg("table.sort", 2, "function expected")
				}
			}
			less := func(x, y any) (bool, error) {
				if cmp == nil {
					return s.compare(compile.OpLT, x, y)
				}
				r, e := s.call(cmp, []any{x, y})
				return truth(libAt(r, 0)), e
			} // In-place heap sort keeps comparisons fallible and uses bounded storage.
			swap := func(i, j int64) error {
				x, e := s.get(a[0], i)
				if e != nil {
					return e
				}
				y, e := s.get(a[0], j)
				if e != nil {
					return e
				}
				if e = s.set(a[0], i, y); e != nil {
					return e
				}
				return s.set(a[0], j, x)
			}
			sift := func(root, end int64) error {
				for root <= end/2 {
					child := root * 2
					x, e := s.get(a[0], child)
					if e != nil {
						return e
					}
					if child < end {
						y, e := s.get(a[0], child+1)
						if e != nil {
							return e
						}
						b, e := less(x, y)
						if e != nil {
							return e
						}
						if b {
							child++
							x = y
						}
					}
					v, e := s.get(a[0], root)
					if e != nil {
						return e
					}
					b, e := less(v, x)
					if e != nil {
						return e
					}
					if !b {
						return nil
					}
					if e = swap(root, child); e != nil {
						return e
					}
					root = child
				}
				return nil
			}
			for i := n / 2; i >= 1; i-- {
				if e = sift(i, n); e != nil {
					return nil, e
				}
			}
			for end := n; end > 1; end-- {
				if e = swap(1, end); e != nil {
					return nil, e
				}
				if e = sift(1, end-1); e != nil {
					return nil, e
				}
			}
			return nil, nil
		},
	}
}
func libTableAccess(s *State, name string, v any, arg int, methods ...string) error {
	if t, ok := v.(*Table); ok && t != nil {
		return nil
	}
	for _, m := range methods {
		if s.metamethod(v, m) == nil {
			return libArg(name, arg, "table expected")
		}
	}
	return nil
}
func libSequenceLength(s *State, v any) (int64, error) {
	if e := libTableAccess(s, "table operation", v, 1, "__len", "__index"); e != nil {
		return 0, e
	}
	x, e := s.length(v)
	if e != nil {
		return 0, e
	}
	return libInt("table length", []any{x}, 0)
}

func libRelative(i int64, n int) int64 {
	if i < 0 {
		if i < -int64(n) {
			return 0
		}
		return int64(n) + i + 1
	}
	return i
}
func libStringRange(name string, a []any, start int, text string, defaultEnd int64) (int, int, error) {
	i, e := libOptInt(name, a, start, 1)
	if e != nil {
		return 0, 0, e
	}
	j, e := libOptInt(name, a, start+1, defaultEnd)
	if e != nil {
		return 0, 0, e
	}
	i = libRelative(i, len(text))
	j = libRelative(j, len(text))
	i = max(i, 1)
	j = min(j, int64(len(text)))
	if i > j {
		return 0, 0, nil
	}
	return int(i - 1), int(j), nil
}
func libStrings() map[string]NativeFunction {
	out := map[string]NativeFunction{}
	out["len"] = func(_ *State, a []any) ([]any, error) {
		v, e := libString("string.len", a, 0)
		return []any{int64(len(v))}, e
	}
	for name, f := range map[string]func(string) string{"lower": func(s string) string {
		b := []byte(s)
		for i, c := range b {
			if c >= 'A' && c <= 'Z' {
				b[i] = c + 32
			}
		}
		return string(b)
	}, "upper": func(s string) string {
		b := []byte(s)
		for i, c := range b {
			if c >= 'a' && c <= 'z' {
				b[i] = c - 32
			}
		}
		return string(b)
	}, "reverse": func(s string) string {
		b := []byte(s)
		for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
			b[i], b[j] = b[j], b[i]
		}
		return string(b)
	}} {
		out[name] = func(_ *State, a []any) ([]any, error) {
			v, e := libString("string."+name, a, 0)
			if e != nil {
				return nil, e
			}
			if len(v) > libMaxBytes {
				return nil, fmt.Errorf("string.%s: result exceeds 16 MiB limit", name)
			}
			return []any{f(v)}, nil
		}
	}
	out["sub"] = func(_ *State, a []any) ([]any, error) {
		v, e := libString("string.sub", a, 0)
		if e != nil {
			return nil, e
		}
		if _, e = libInt("string.sub", a, 1); e != nil {
			return nil, e
		}
		i, j, e := libStringRange("string.sub", a, 1, v, -1)
		if e != nil {
			return nil, e
		}
		return []any{v[i:j]}, nil
	}
	out["byte"] = func(_ *State, a []any) ([]any, error) {
		v, e := libString("string.byte", a, 0)
		if e != nil {
			return nil, e
		}
		start, e := libOptInt("string.byte", a, 1, 1)
		if e != nil {
			return nil, e
		}
		i, j, e := libStringRange("string.byte", a, 1, v, start)
		if e != nil {
			return nil, e
		}
		if j-i > libMaxResults {
			return nil, fmt.Errorf("string.byte: too many results (16 MiB limit)")
		}
		r := make([]any, j-i)
		for k := i; k < j; k++ {
			r[k-i] = int64(v[k])
		}
		return r, nil
	}
	out["char"] = func(_ *State, a []any) ([]any, error) {
		if len(a) > libMaxBytes {
			return nil, fmt.Errorf("string.char: result exceeds 16 MiB limit")
		}
		b := make([]byte, len(a))
		for i := range a {
			x, e := libInt("string.char", a, i)
			if e != nil {
				return nil, e
			}
			if x < 0 || x > 255 {
				return nil, libArg("string.char", i+1, "value out of range")
			}
			b[i] = byte(x)
		}
		return []any{string(b)}, nil
	}
	out["rep"] = func(_ *State, a []any) ([]any, error) {
		v, e := libString("string.rep", a, 0)
		if e != nil {
			return nil, e
		}
		n, e := libInt("string.rep", a, 1)
		if e != nil {
			return nil, e
		}
		sep := ""
		if libAt(a, 2) != nil {
			sep, e = libString("string.rep", a, 2)
			if e != nil {
				return nil, e
			}
		}
		if n <= 0 {
			return []any{""}, nil
		}
		if len(v) == 0 && len(sep) == 0 {
			return []any{""}, nil
		}
		if len(v) > libMaxBytes || len(v) > 0 && n > int64(libMaxBytes/len(v)) {
			return nil, fmt.Errorf("string.rep: result exceeds 16 MiB limit")
		}
		total := n * int64(len(v))
		if len(sep) > 0 && n-1 > int64(libMaxBytes-int(total))/int64(len(sep)) {
			return nil, fmt.Errorf("string.rep: result exceeds 16 MiB limit")
		}
		if n == 1 {
			return []any{v}, nil
		}
		return []any{strings.Repeat(v+sep, int(n-1)) + v}, nil
	}
	for _, name := range []string{"pack", "packsize", "unpack", "dump"} {
		out[name] = func(_ *State, _ []any) ([]any, error) { return nil, fmt.Errorf("string.%s: unsupported", name) }
	}
	return out
}

func libUTF8() map[string]NativeFunction {
	return map[string]NativeFunction{
		"char": func(_ *State, a []any) ([]any, error) {
			if len(a) > libMaxBytes/utf8.UTFMax {
				return nil, fmt.Errorf("utf8.char: result exceeds 16 MiB limit")
			}
			var b strings.Builder
			b.Grow(len(a) * utf8.UTFMax)
			for i := range a {
				x, e := libInt("utf8.char", a, i)
				if e != nil {
					return nil, e
				}
				if x < 0 || x > utf8.MaxRune || x >= 0xd800 && x <= 0xdfff {
					return nil, libArg("utf8.char", i+1, "value out of range (Unicode scalar required)")
				}
				b.WriteRune(rune(x))
			}
			return []any{b.String()}, nil
		},
		"len": func(_ *State, a []any) ([]any, error) {
			v, e := libString("utf8.len", a, 0)
			if e != nil {
				return nil, e
			}
			if truth(libAt(a, 3)) {
				return nil, fmt.Errorf("utf8.len: lax decoding unsupported")
			}
			i, e := libOptInt("utf8.len", a, 1, 1)
			if e != nil {
				return nil, e
			}
			j, e := libOptInt("utf8.len", a, 2, -1)
			if e != nil {
				return nil, e
			}
			i = libRelative(i, len(v))
			j = libRelative(j, len(v))
			if i < 1 || i > int64(len(v))+1 {
				return nil, libArg("utf8.len", 2, "initial position out of bounds")
			}
			if j > int64(len(v)) {
				return nil, libArg("utf8.len", 3, "final position out of bounds")
			}
			n := int64(0)
			for pos := i - 1; pos < j; {
				r, size := utf8.DecodeRuneInString(v[pos:])
				if r == utf8.RuneError && size == 1 {
					return []any{nil, pos + 1}, nil
				}
				n++
				pos += int64(size)
			}
			return []any{n}, nil
		},
		"codepoint": func(_ *State, a []any) ([]any, error) {
			v, e := libString("utf8.codepoint", a, 0)
			if e != nil {
				return nil, e
			}
			if truth(libAt(a, 3)) {
				return nil, fmt.Errorf("utf8.codepoint: lax decoding unsupported")
			}
			i, e := libOptInt("utf8.codepoint", a, 1, 1)
			if e != nil {
				return nil, e
			}
			j, e := libOptInt("utf8.codepoint", a, 2, i)
			if e != nil {
				return nil, e
			}
			i = libRelative(i, len(v))
			j = libRelative(j, len(v))
			if i < 1 {
				return nil, libArg("utf8.codepoint", 2, "out of bounds")
			}
			if j > int64(len(v)) {
				return nil, libArg("utf8.codepoint", 3, "out of bounds")
			}
			var out []any
			for pos := i - 1; pos < j; {
				r, size := utf8.DecodeRuneInString(v[pos:])
				if r == utf8.RuneError && size == 1 {
					return nil, fmt.Errorf("invalid UTF-8 code at byte %d", pos+1)
				}
				if len(out) == libMaxResults {
					return nil, fmt.Errorf("utf8.codepoint: too many results (16 MiB limit)")
				}
				if len(out) == cap(out) {
					next := min(2*cap(out)+1, libMaxResults)
					grown := make([]any, len(out), next)
					copy(grown, out)
					out = grown
				}
				out = append(out, int64(r))
				pos += int64(size)
			}
			return out, nil
		},
		"codes": func(_ *State, a []any) ([]any, error) {
			v, e := libString("utf8.codes", a, 0)
			if e != nil {
				return nil, e
			}
			if truth(libAt(a, 1)) {
				return nil, fmt.Errorf("utf8.codes: lax decoding unsupported")
			}
			if len(v) > 0 && v[0]&0xc0 == 0x80 {
				return nil, libArg("utf8.codes", 1, "invalid UTF-8 code")
			}
			iter := NativeFunction(func(_ *State, args []any) ([]any, error) {
				text, e := libString("utf8.codes iterator", args, 0)
				if e != nil {
					return nil, e
				}
				i, e := libInt("utf8.codes iterator", args, 1)
				if e != nil {
					return nil, e
				}
				if i < 0 || i >= int64(len(text)) {
					return nil, nil
				}
				pos := i
				for pos < int64(len(text)) && text[pos]&0xc0 == 0x80 {
					pos++
				}
				if pos >= int64(len(text)) {
					return nil, nil
				}
				r, n := utf8.DecodeRuneInString(text[pos:])
				next := pos + int64(n)
				if r == utf8.RuneError && n == 1 || next < int64(len(text)) && text[next]&0xc0 == 0x80 {
					return nil, fmt.Errorf("invalid UTF-8 code at byte %d", pos+1)
				}
				return []any{pos + 1, int64(r)}, nil
			})
			return []any{iter, v, int64(0)}, nil
		},
		"offset": func(_ *State, a []any) ([]any, error) {
			v, e := libString("utf8.offset", a, 0)
			if e != nil {
				return nil, e
			}
			n, e := libInt("utf8.offset", a, 1)
			if e != nil {
				return nil, e
			}
			def := int64(1)
			if n < 0 {
				def = int64(len(v)) + 1
			}
			i, e := libOptInt("utf8.offset", a, 2, def)
			if e != nil {
				return nil, e
			}
			i = libRelative(i, len(v))
			if i < 1 || i > int64(len(v))+1 {
				return nil, libArg("utf8.offset", 3, "position out of bounds")
			}
			pos := int(i - 1)
			continuation := func(p int) bool { return p < len(v) && v[p]&0xc0 == 0x80 }
			if n == 0 {
				for pos > 0 && continuation(pos) {
					pos--
				}
				return []any{int64(pos + 1)}, nil
			}
			if continuation(pos) {
				return nil, fmt.Errorf("initial position is a continuation byte")
			}
			if n > 0 {
				n--
				for n > 0 && pos < len(v) {
					pos++
					for continuation(pos) {
						pos++
					}
					n--
				}
			} else {
				for n < 0 && pos > 0 {
					pos--
					for pos > 0 && continuation(pos) {
						pos--
					}
					n++
				}
			}
			if n == 0 {
				return []any{int64(pos + 1)}, nil
			}
			return []any{nil}, nil
		},
	}
}
