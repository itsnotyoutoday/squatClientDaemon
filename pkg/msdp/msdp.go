// Package msdp encodes and decodes the payload of MSDP (Mud Server Data
// Protocol, Telnet option 69; see https://tintin.mudhalla.net/protocols/msdp/).
//
// The payload is what sits between "IAC SB MSDP" and "IAC SE": a sequence of
//
//	VAR name VAL value
//
// where a value is a string, a table (TABLE_OPEN, VAR/VAL pairs, TABLE_CLOSE)
// or an array (ARRAY_OPEN, VAL items, ARRAY_CLOSE). Values map to Go as
// string, map[string]any (table) and []any (array), which is also their JSON form.
//
// The Telnet layer (IAC escaping, SB/SE framing) is not handled here.
package msdp

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Marker bytes.
const (
	Var        = 1
	Val        = 2
	TableOpen  = 3
	TableClose = 4
	ArrayOpen  = 5
	ArrayClose = 6
)

// Limits protecting the decoder from hostile input.
const (
	MaxDepth   = 16
	MaxPayload = 64 << 10
)

// Pair is one top-level variable. Value is a string, []any or map[string]any.
type Pair struct {
	Name  string
	Value any
}

var (
	ErrTooLarge = errors.New("msdp: payload too large")
	ErrTooDeep  = errors.New("msdp: nesting too deep")
)

func isMarker(b byte) bool { return b >= Var && b <= ArrayClose }

type decoder struct {
	p []byte
	i int
}

// Decode parses a payload. Servers differ in small ways, so it is lenient where
// that is unambiguous: a VAR with no VAL has an empty value.
func Decode(p []byte) ([]Pair, error) {
	if len(p) > MaxPayload {
		return nil, ErrTooLarge
	}
	d := &decoder{p: p}
	var out []Pair
	for d.i < len(d.p) {
		name, val, err := d.pair(0)
		if err != nil {
			return nil, err
		}
		out = append(out, Pair{name, val})
	}
	return out, nil
}

func (d *decoder) str() string {
	start := d.i
	for d.i < len(d.p) && !isMarker(d.p[d.i]) {
		d.i++
	}
	return string(d.p[start:d.i])
}

// pair reads "VAR name [VAL value]".
func (d *decoder) pair(depth int) (string, any, error) {
	if d.p[d.i] != Var {
		return "", nil, fmt.Errorf("msdp: expected VAR at offset %d, found byte %d", d.i, d.p[d.i])
	}
	d.i++
	name := d.str()
	if d.i >= len(d.p) || d.p[d.i] != Val {
		return name, "", nil // a variable with no value
	}
	d.i++
	v, err := d.value(depth)
	return name, v, err
}

func (d *decoder) value(depth int) (any, error) {
	if depth > MaxDepth {
		return nil, ErrTooDeep
	}
	if d.i >= len(d.p) {
		return "", nil
	}
	switch d.p[d.i] {
	case TableOpen:
		d.i++
		m := map[string]any{}
		for {
			if d.i >= len(d.p) {
				return nil, errors.New("msdp: unterminated table")
			}
			if d.p[d.i] == TableClose {
				d.i++
				return m, nil
			}
			name, v, err := d.pair(depth + 1)
			if err != nil {
				return nil, err
			}
			m[name] = v
		}
	case ArrayOpen:
		d.i++
		a := []any{}
		for {
			if d.i >= len(d.p) {
				return nil, errors.New("msdp: unterminated array")
			}
			if d.p[d.i] == ArrayClose {
				d.i++
				return a, nil
			}
			if d.p[d.i] != Val {
				return nil, fmt.Errorf("msdp: expected VAL in an array at offset %d, found byte %d", d.i, d.p[d.i])
			}
			d.i++
			v, err := d.value(depth + 1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
	default:
		if isMarker(d.p[d.i]) {
			return "", nil // e.g. "VAL" directly followed by the next VAR
		}
		return d.str(), nil
	}
}

// ToMap turns pairs into a JSON-friendly map (a repeated name keeps its last value).
func ToMap(ps []Pair) map[string]any {
	m := make(map[string]any, len(ps))
	for _, p := range ps {
		m[p.Name] = p.Value
	}
	return m
}

// FromMap returns the pairs of m in name order (deterministic output).
func FromMap(m map[string]any) []Pair {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	ps := make([]Pair, 0, len(names))
	for _, k := range names {
		ps = append(ps, Pair{k, m[k]})
	}
	return ps
}

// Encode builds a payload. Values may be strings, numbers, booleans, nil
// (empty), []any / []string, and map[string]any (a table, keys sorted).
// Names and values must not contain the marker bytes 1-6, NUL or 255.
func Encode(ps []Pair) ([]byte, error) {
	var out []byte
	for _, p := range ps {
		var err error
		if out, err = appendPair(out, p.Name, p.Value, 0); err != nil {
			return nil, err
		}
		if len(out) > MaxPayload {
			return nil, ErrTooLarge
		}
	}
	return out, nil
}

func checkText(s string) error {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 || isMarker(s[i]) || s[i] == 0xff {
			return fmt.Errorf("msdp: text contains the reserved byte %d", s[i])
		}
	}
	return nil
}

func appendPair(out []byte, name string, v any, depth int) ([]byte, error) {
	if name == "" {
		return nil, errors.New("msdp: empty variable name")
	}
	if err := checkText(name); err != nil {
		return nil, err
	}
	out = append(out, Var)
	out = append(out, name...)
	out = append(out, Val)
	return appendValue(out, v, depth)
}

func appendValue(out []byte, v any, depth int) ([]byte, error) {
	if depth > MaxDepth {
		return nil, ErrTooDeep
	}
	switch x := v.(type) {
	case nil:
		return out, nil
	case string:
		if err := checkText(x); err != nil {
			return nil, err
		}
		return append(out, x...), nil
	case bool:
		return append(out, strconv.FormatBool(x)...), nil
	case int:
		return append(out, strconv.Itoa(x)...), nil
	case int64:
		return append(out, strconv.FormatInt(x, 10)...), nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, errors.New("msdp: not a finite number")
		}
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return append(out, strconv.FormatInt(int64(x), 10)...), nil
		}
		return append(out, strconv.FormatFloat(x, 'g', -1, 64)...), nil
	case []string:
		a := make([]any, len(x))
		for i, s := range x {
			a[i] = s
		}
		return appendValue(out, a, depth)
	case []any:
		out = append(out, ArrayOpen)
		for _, item := range x {
			out = append(out, Val)
			var err error
			if out, err = appendValue(out, item, depth+1); err != nil {
				return nil, err
			}
		}
		return append(out, ArrayClose), nil
	case map[string]any:
		out = append(out, TableOpen)
		for _, p := range FromMap(x) {
			var err error
			if out, err = appendPair(out, p.Name, p.Value, depth+1); err != nil {
				return nil, err
			}
		}
		return append(out, TableClose), nil
	}
	return nil, fmt.Errorf("msdp: cannot encode a %T", v)
}

// String renders a payload readably, for logs: VAR=name VAL=value, {tables}, [arrays].
func String(ps []Pair) string {
	var sb strings.Builder
	var w func(v any)
	w = func(v any) {
		switch x := v.(type) {
		case string:
			sb.WriteString(strconv.Quote(x))
		case []any:
			sb.WriteByte('[')
			for i, e := range x {
				if i > 0 {
					sb.WriteByte(' ')
				}
				w(e)
			}
			sb.WriteByte(']')
		case map[string]any:
			sb.WriteByte('{')
			for i, p := range FromMap(x) {
				if i > 0 {
					sb.WriteByte(' ')
				}
				sb.WriteString(p.Name + "=")
				w(p.Value)
			}
			sb.WriteByte('}')
		}
	}
	for i, p := range ps {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(p.Name + "=")
		w(p.Value)
	}
	return sb.String()
}
