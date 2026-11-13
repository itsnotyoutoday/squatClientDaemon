package lua

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	glua "github.com/yuin/gopher-lua"
)

const maxConvDepth = 32

// ---------------------------------------------------------------- colours

// colorFromLua converts a Lua colour value: an integer 0-255 (palette index)
// or "#rrggbb" (24-bit truecolor). nil/"" yield (nil, nil).
func colorFromLua(v glua.LValue) (*proto.Color, error) {
	switch x := v.(type) {
	case *glua.LNilType:
		return nil, nil
	case glua.LNumber:
		f := float64(x)
		if f != math.Trunc(f) || f < 0 || f > 255 {
			return nil, fmt.Errorf("colour index %v out of range 0-255", f)
		}
		c := proto.Palette(uint8(f))
		return &c, nil
	case glua.LString:
		s := string(x)
		if s == "" {
			return nil, nil
		}
		if len(s) != 7 || s[0] != '#' {
			return nil, fmt.Errorf("colour %q must be 0-255 or \"#rrggbb\"", s)
		}
		n, err := strconv.ParseUint(s[1:], 16, 32)
		if err != nil {
			return nil, fmt.Errorf("colour %q must be 0-255 or \"#rrggbb\"", s)
		}
		c := proto.RGB(uint8(n>>16), uint8(n>>8), uint8(n))
		return &c, nil
	}
	return nil, fmt.Errorf("colour must be a number 0-255 or \"#rrggbb\", got %s", v.Type())
}

func colorToLua(c proto.Color) glua.LValue {
	if c.IsTrueColor() {
		return glua.LString(c.Hex())
	}
	return glua.LNumber(c.Index())
}

// ---------------------------------------------------------------- spans

func boolField(t *glua.LTable, k string) bool { return glua.LVAsBool(t.RawGetString(k)) }

// spanFromTable converts {s=..., fg=..., bg=..., b=true, ...}.
func spanFromTable(t *glua.LTable) (proto.Span, error) {
	var sp proto.Span
	s := t.RawGetString("s")
	switch x := s.(type) {
	case glua.LString:
		sp.S = string(x)
	case glua.LNumber:
		sp.S = x.String()
	default:
		return sp, fmt.Errorf("span field s must be a string, got %s", s.Type())
	}
	var err error
	if sp.Fg, err = colorFromLua(t.RawGetString("fg")); err != nil {
		return sp, fmt.Errorf("fg: %w", err)
	}
	if sp.Bg, err = colorFromLua(t.RawGetString("bg")); err != nil {
		return sp, fmt.Errorf("bg: %w", err)
	}
	sp.B, sp.Dim, sp.I = boolField(t, "b"), boolField(t, "dim"), boolField(t, "i")
	sp.U, sp.St = boolField(t, "u"), boolField(t, "st")
	sp.Blink, sp.Rev = boolField(t, "blink"), boolField(t, "rev")
	if l, ok := t.RawGetString("link").(glua.LString); ok {
		sp.Link = string(l)
	}
	return sp, nil
}

// spansFromLua accepts a string (one unstyled span), a single span table
// ({s=...}) or an array whose elements are strings or span tables.
func spansFromLua(v glua.LValue) ([]proto.Span, error) {
	switch x := v.(type) {
	case glua.LString:
		if x == "" {
			return nil, nil
		}
		return []proto.Span{{S: string(x)}}, nil
	case *glua.LTable:
		if x.RawGetString("s") != glua.LNil {
			sp, err := spanFromTable(x)
			if err != nil {
				return nil, err
			}
			return dropEmpty([]proto.Span{sp}), nil
		}
		var out []proto.Span
		for i := 1; ; i++ {
			e := x.RawGetInt(i)
			if e == glua.LNil {
				break
			}
			switch ev := e.(type) {
			case glua.LString:
				out = append(out, proto.Span{S: string(ev)})
			case *glua.LTable:
				sp, err := spanFromTable(ev)
				if err != nil {
					return nil, fmt.Errorf("span %d: %w", i, err)
				}
				out = append(out, sp)
			default:
				return nil, fmt.Errorf("span %d must be a string or table, got %s", i, e.Type())
			}
		}
		return dropEmpty(out), nil
	}
	return nil, fmt.Errorf("expected string or spans table, got %s", v.Type())
}

func dropEmpty(in []proto.Span) []proto.Span {
	out := in[:0]
	for _, s := range in {
		if s.S != "" {
			out = append(out, s)
		}
	}
	return out
}

func spanToLua(L *glua.LState, sp proto.Span) *glua.LTable {
	t := L.CreateTable(0, 4)
	t.RawSetString("s", glua.LString(sp.S))
	if sp.Fg != nil {
		t.RawSetString("fg", colorToLua(*sp.Fg))
	}
	if sp.Bg != nil {
		t.RawSetString("bg", colorToLua(*sp.Bg))
	}
	for _, f := range []struct {
		k string
		v bool
	}{{"b", sp.B}, {"dim", sp.Dim}, {"i", sp.I}, {"u", sp.U}, {"st", sp.St}, {"blink", sp.Blink}, {"rev", sp.Rev}} {
		if f.v {
			t.RawSetString(f.k, glua.LTrue)
		}
	}
	if sp.Link != "" {
		t.RawSetString("link", glua.LString(sp.Link))
	}
	return t
}

func spansToLua(L *glua.LState, spans []proto.Span) *glua.LTable {
	t := L.CreateTable(len(spans), 0)
	for _, sp := range spans {
		t.Append(spanToLua(L, sp))
	}
	return t
}

func plainOf(spans []proto.Span) string {
	if len(spans) == 1 {
		return spans[0].S
	}
	var sb strings.Builder
	for _, s := range spans {
		sb.WriteString(s.S)
	}
	return sb.String()
}

// styleMask is the style applied by a trigger's hilite options.
type styleMask struct {
	fg, bg                       *proto.Color
	b, dim, i, u, st, blink, rev bool
}

func (m styleMask) empty() bool { return m == styleMask{} }

func (m styleMask) apply(sp proto.Span) proto.Span {
	if m.fg != nil {
		c := *m.fg
		sp.Fg = &c
	}
	if m.bg != nil {
		c := *m.bg
		sp.Bg = &c
	}
	sp.B = sp.B || m.b
	sp.Dim = sp.Dim || m.dim
	sp.I = sp.I || m.i
	sp.U = sp.U || m.u
	sp.St = sp.St || m.st
	sp.Blink = sp.Blink || m.blink
	sp.Rev = sp.Rev || m.rev
	return sp
}

// styleRange applies m to the byte range [from,to) of the concatenated span
// text, splitting spans at the boundaries and re-merging equal neighbours.
func styleRange(spans []proto.Span, m styleMask, from, to int) []proto.Span {
	out := make([]proto.Span, 0, len(spans)+2)
	push := func(sp proto.Span) {
		if sp.S == "" {
			return
		}
		if n := len(out); n > 0 && out[n-1].SameStyle(sp) {
			out[n-1].S += sp.S
			return
		}
		out = append(out, sp)
	}
	off := 0
	for _, sp := range spans {
		end := off + len(sp.S)
		lo, hi := max(from, off), min(to, end)
		if lo >= hi {
			push(sp)
		} else {
			a, b, c := sp, sp, sp
			a.S, b.S, c.S = sp.S[:lo-off], sp.S[lo-off:hi-off], sp.S[hi-off:]
			push(a)
			push(m.apply(b))
			push(c)
		}
		off = end
	}
	return out
}

// ---------------------------------------------------------------- generic values

// toLua converts a Go value (typically an event payload) to a Lua value.
func toLua(L *glua.LState, v any) glua.LValue { return toLuaD(L, v, 0) }

func toLuaD(L *glua.LState, v any, depth int) glua.LValue {
	if depth > maxConvDepth {
		return glua.LNil
	}
	switch x := v.(type) {
	case nil:
		return glua.LNil
	case glua.LValue:
		return x
	case bool:
		return glua.LBool(x)
	case string:
		return glua.LString(x)
	case int:
		return glua.LNumber(x)
	case int64:
		return glua.LNumber(x)
	case uint64:
		return glua.LNumber(x)
	case float64:
		return glua.LNumber(x)
	case json.RawMessage:
		if len(x) == 0 {
			return glua.LNil
		}
		var any_ any
		if err := json.Unmarshal(x, &any_); err != nil {
			return glua.LString(string(x))
		}
		return toLuaD(L, any_, depth+1)
	case proto.GmcpMsg:
		t := L.CreateTable(0, 6)
		t.RawSetString("package", glua.LString(x.Package))
		t.RawSetString("raw", glua.LString(string(x.Data)))
		t.RawSetString("data", toLuaD(L, x.Data, depth+1))
		t.RawSetString("world", glua.LString(x.Session))
		t.RawSetString("seq", glua.LNumber(x.Seq))
		t.RawSetString("ts", glua.LNumber(x.Ts))
		return t
	case proto.Span:
		return spanToLua(L, x)
	case []proto.Span:
		return spansToLua(L, x)
	case proto.Color:
		return colorToLua(x)
	case []any:
		t := L.CreateTable(len(x), 0)
		for _, e := range x {
			t.Append(toLuaD(L, e, depth+1))
		}
		return t
	case []string:
		t := L.CreateTable(len(x), 0)
		for _, e := range x {
			t.Append(glua.LString(e))
		}
		return t
	case map[string]any:
		t := L.CreateTable(0, len(x))
		for k, e := range x {
			t.RawSetString(k, toLuaD(L, e, depth+1))
		}
		return t
	case map[string]string:
		t := L.CreateTable(0, len(x))
		for k, e := range x {
			t.RawSetString(k, glua.LString(e))
		}
		return t
	}
	// Fallback: structs and other kinds via their JSON form.
	if rv := reflect.ValueOf(v); rv.Kind() != reflect.Func && rv.Kind() != reflect.Chan {
		if b, err := json.Marshal(v); err == nil {
			var any_ any
			if json.Unmarshal(b, &any_) == nil {
				return toLuaD(L, any_, depth+1)
			}
		}
	}
	return glua.LString(fmt.Sprint(v))
}

// fromLua converts a Lua value to a JSON-compatible Go value (nil, bool,
// float64, string, []any, map[string]any). Functions, userdata and cycles
// (depth > 32) are errors. A table is an array when its keys are exactly
// 1..n, otherwise an object (keys must be strings or numbers).
func fromLua(v glua.LValue) (any, error) { return fromLuaD(v, 0) }

func fromLuaD(v glua.LValue, depth int) (any, error) {
	if depth > maxConvDepth {
		return nil, fmt.Errorf("value nested too deeply (cycle?)")
	}
	switch x := v.(type) {
	case *glua.LNilType:
		return nil, nil
	case glua.LBool:
		return bool(x), nil
	case glua.LNumber:
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("cannot store NaN or Inf")
		}
		return f, nil
	case glua.LString:
		return string(x), nil
	case *glua.LTable:
		n := x.Len()
		count := 0
		x.ForEach(func(_, _ glua.LValue) { count++ })
		if n > 0 && count == n {
			arr := make([]any, 0, n)
			for i := 1; i <= n; i++ {
				e, err := fromLuaD(x.RawGetInt(i), depth+1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, e)
			}
			return arr, nil
		}
		obj := make(map[string]any, count)
		var ferr error
		x.ForEach(func(k, e glua.LValue) {
			if ferr != nil {
				return
			}
			var ks string
			switch kk := k.(type) {
			case glua.LString:
				ks = string(kk)
			case glua.LNumber:
				ks = kk.String()
			default:
				ferr = fmt.Errorf("table key must be string or number, got %s", k.Type())
				return
			}
			ev, err := fromLuaD(e, depth+1)
			if err != nil {
				ferr = err
				return
			}
			obj[ks] = ev
		})
		if ferr != nil {
			return nil, ferr
		}
		return obj, nil
	}
	return nil, fmt.Errorf("cannot convert %s to a stored value", v.Type())
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
