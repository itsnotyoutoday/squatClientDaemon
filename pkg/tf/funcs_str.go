package tf

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// bound clamps a position into [0,max]; a negative position counts back from
// max (so -1 is the last position), clamped at 0.
func bound(v, max int) int {
	if v > max {
		return max
	}
	if v < 0 {
		v += max
		if v < 0 {
			v = 0
		}
	}
	return v
}

func runeIdx(r []rune, set string, last bool, from int) int {
	if from < 0 {
		from = 0
	}
	res := -1
	for k := from; k < len(r); k++ {
		if strings.ContainsRune(set, r[k]) {
			if !last {
				return k
			}
			res = k
		}
	}
	return res
}

func (i *Interp) initStringFuncs() {
	i.def("strlen", 1, 1, func(_ *Interp, a []Value) (Value, error) {
		return I(int64(utf8.RuneCountInString(a[0].s))), nil
	})
	i.def("strcat", 0, -1, func(_ *Interp, a []Value) (Value, error) {
		var sb strings.Builder
		for _, v := range a {
			sb.WriteString(v.s)
			if sb.Len() > maxValueLen {
				return Value{}, fmt.Errorf("strcat(): result too long")
			}
		}
		return S(sb.String()), nil
	})
	i.def("strcmp", 2, 2, func(_ *Interp, a []Value) (Value, error) {
		return I(int64(strings.Compare(a[0].s, a[1].s))), nil
	})
	i.def("strncmp", 3, 3, func(_ *Interp, a []Value) (Value, error) {
		n := int(a[2].Int())
		x, y := []rune(a[0].s), []rune(a[1].s)
		if n < 0 {
			n = 0
		}
		if len(x) > n {
			x = x[:n]
		}
		if len(y) > n {
			y = y[:n]
		}
		return I(int64(strings.Compare(string(x), string(y)))), nil
	})
	i.def("strchr", 2, 3, func(_ *Interp, a []Value) (Value, error) {
		r := []rune(a[0].s)
		from := 0
		if len(a) > 2 {
			from = bound(int(a[2].Int()), len(r))
		}
		return I(int64(runeIdx(r, a[1].s, false, from))), nil
	})
	i.def("strrchr", 2, 3, func(_ *Interp, a []Value) (Value, error) {
		r := []rune(a[0].s)
		end := len(r) - 1
		if len(a) > 2 {
			end = bound(int(a[2].Int()), len(r)-1)
		}
		if end < len(r)-1 {
			r = r[:end+1]
		}
		return I(int64(runeIdx(r, a[1].s, true, 0))), nil
	})
	i.def("strstr", 2, 3, func(_ *Interp, a []Value) (Value, error) {
		r := []rune(a[0].s)
		from := 0
		if len(a) > 2 {
			from = bound(int(a[2].Int()), len(r))
		}
		k := strings.Index(string(r[from:]), a[1].s)
		if k < 0 {
			return I(-1), nil
		}
		return I(int64(from + utf8.RuneCountInString(string(r[from:])[:k]))), nil
	})
	i.def("strrep", 2, 2, func(_ *Interp, a []Value) (Value, error) {
		n := a[1].Int()
		if n <= 0 || a[0].s == "" {
			return S(""), nil
		}
		if int64(len(a[0].s))*n > maxValueLen {
			return Value{}, fmt.Errorf("strrep(): result too long")
		}
		return S(strings.Repeat(a[0].s, int(n))), nil
	})
	i.def("substr", 2, 3, func(_ *Interp, a []Value) (Value, error) {
		r := []rune(a[0].s)
		st := bound(int(a[1].Int()), len(r))
		n := len(r) - st
		if len(a) > 2 {
			n = bound(int(a[2].Int()), n)
		}
		return S(string(r[st : st+n])), nil
	})
	i.def("pad", 2, -1, func(_ *Interp, a []Value) (Value, error) {
		var sb strings.Builder
		for k := 0; k+1 < len(a); k += 2 {
			w := int(a[k+1].Int())
			if w > 65536 || w < -65536 {
				return Value{}, fmt.Errorf("pad(): width too large")
			}
			n := utf8.RuneCountInString(a[k].s)
			abs := w
			if abs < 0 {
				abs = -abs
			}
			gap := ""
			if abs > n {
				gap = strings.Repeat(" ", abs-n)
			}
			if w < 0 {
				sb.WriteString(a[k].s + gap)
			} else {
				sb.WriteString(gap + a[k].s)
			}
		}
		if len(a)%2 == 1 {
			sb.WriteString(a[len(a)-1].s)
		}
		return S(sb.String()), nil
	})
	caseFn := func(name string, f func(string) string) {
		i.def(name, 1, 2, func(_ *Interp, a []Value) (Value, error) {
			r := []rune(a[0].s)
			n := len(r)
			if len(a) > 1 {
				n = bound(int(a[1].Int()), n)
			}
			return S(f(string(r[:n])) + string(r[n:])), nil
		})
	}
	caseFn("tolower", strings.ToLower)
	caseFn("toupper", strings.ToUpper)
	// replace(old, new, str): every occurrence of old in str becomes new.
	i.def("replace", 3, 3, func(_ *Interp, a []Value) (Value, error) {
		old, nw, str := a[0].s, a[1].s, a[2].s
		if old == "" {
			return a[2], nil
		}
		if strings.Count(str, old)*(len(nw)+1)+len(str) > maxValueLen {
			return Value{}, fmt.Errorf("replace(): result too long")
		}
		return S(strings.ReplaceAll(str, old, nw)), nil
	})
	i.def("ascii", 1, 1, func(_ *Interp, a []Value) (Value, error) {
		r, n := utf8.DecodeRuneInString(a[0].s)
		if n == 0 {
			return I(0), nil
		}
		return I(int64(r)), nil
	})
	i.def("char", 1, 1, func(_ *Interp, a []Value) (Value, error) {
		n := a[0].Int()
		if n < 0 || n > 0x10ffff {
			return S(""), nil
		}
		return S(string(rune(n))), nil
	})
	i.def("regmatch", 2, 2, func(i *Interp, a []Value) (Value, error) {
		re, err := i.compileRe(a[0].s)
		if err != nil {
			return Value{}, fmt.Errorf("regmatch(): %v", err)
		}
		text := a[1].s
		idx := re.FindStringSubmatchIndex(text)
		fr := i.top()
		if idx == nil {
			fr.match = nil
			return I(0), nil
		}
		mi := &matchInfo{text: text, start: idx[0], end: idx[1], left: text[:idx[0]], right: text[idx[1]:]}
		for k := 0; k+1 < len(idx); k += 2 {
			if idx[k] < 0 {
				mi.parts = append(mi.parts, "")
				mi.idx = append(mi.idx, [2]int{-1, -1})
			} else {
				mi.parts = append(mi.parts, text[idx[k]:idx[k+1]])
				mi.idx = append(mi.idx, [2]int{idx[k], idx[k+1]})
			}
		}
		fr.match = mi
		return I(int64(len(mi.parts))), nil
	})
	i.def("substitute", 1, 3, func(i *Interp, a []Value) (Value, error) {
		if i.lineCtx == nil || !i.live {
			return I(0), nil
		}
		var attr Attr
		if len(a) > 1 && a[1].s != "" {
			var err error
			if attr, err = ParseAttr(a[1].s); err != nil {
				return Value{}, err
			}
		}
		if len(a) > 2 && a[2].Truth() {
			i.lineCtx.spans = SpansFromMarkup(a[0].s, attr)
		} else {
			i.lineCtx.spans = []protoSpan{attr.Apply(protoSpan{S: a[0].s})}
		}
		return I(1), nil
	})
	i.def("encode_attr", 2, 2, func(_ *Interp, a []Value) (Value, error) {
		at, err := ParseAttr(a[1].s)
		if err != nil {
			return Value{}, err
		}
		return S(EncodeAttr(a[0].s, at.String())), nil
	})
	i.def("decode_attr", 1, 1, func(_ *Interp, a []Value) (Value, error) {
		return S(ansiFor(SpansFromMarkup(a[0].s, Attr{}))), nil
	})
	i.def("strip_attr", 1, 1, func(_ *Interp, a []Value) (Value, error) { return S(stripMarkup(a[0].s)), nil })
}

func (i *Interp) initMathFuncs() {
	f1 := func(name string, fn func(float64) float64) {
		i.def(name, 1, 1, func(_ *Interp, a []Value) (Value, error) { return F(fn(a[0].Float())), nil })
	}
	f1("sin", math.Sin)
	f1("cos", math.Cos)
	f1("tan", math.Tan)
	f1("asin", math.Asin)
	f1("acos", math.Acos)
	f1("atan", math.Atan)
	f1("exp", math.Exp)
	f1("ln", math.Log)
	f1("log10", math.Log10)
	f1("sqrt", math.Sqrt)
	i.def("abs", 1, 1, func(_ *Interp, a []Value) (Value, error) {
		if a[0].IsInt() || !a[0].num {
			n := a[0].Int()
			if n < 0 {
				n = -n
			}
			return I(n), nil
		}
		return F(math.Abs(a[0].f)), nil
	})
	i.def("trunc", 1, 1, func(_ *Interp, a []Value) (Value, error) {
		v, _ := numPrefix(a[0])
		if v.IsInt() {
			return v, nil
		}
		f := math.Trunc(v.Float())
		if f >= math.MaxInt64 || f <= math.MinInt64 {
			return F(f), nil
		}
		return I(int64(f)), nil
	})
	i.def("pow", 2, 2, func(_ *Interp, a []Value) (Value, error) { return F(math.Pow(a[0].Float(), a[1].Float())), nil })
	i.def("mod", 2, 2, func(_ *Interp, a []Value) (Value, error) {
		if a[0].IsInt() && a[1].IsInt() {
			if a[1].i == 0 {
				return Value{}, fmt.Errorf("mod(): division by zero")
			}
			return I(a[0].i % a[1].i), nil
		}
		if a[1].Float() == 0 {
			return Value{}, fmt.Errorf("mod(): division by zero")
		}
		return F(math.Mod(a[0].Float(), a[1].Float())), nil
	})
	i.def("rand", 0, 2, func(i *Interp, a []Value) (Value, error) {
		switch len(a) {
		case 0:
			return I(i.rng(1 << 31)), nil
		case 1:
			if n := a[0].Int(); n > 0 {
				return I(i.rng(n)), nil
			}
			return I(0), nil
		}
		lo, hi := a[0].Int(), a[1].Int()
		if hi < lo {
			lo, hi = hi, lo
		}
		return I(lo + i.rng(hi-lo+1)), nil
	})
}
