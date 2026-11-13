package tf

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Information functions with little or no meaning in a GUI client: they exist
// so scripts written for TinyFugue run and see neutral values.
func (i *Interp) initInfoFuncs() {
	zero := func(name string) {
		i.def(name, 0, 2, func(*Interp, []Value) (Value, error) { return I(0), nil })
	}
	for _, n := range []string{"nmail", "nread", "morepaused", "moresize", "morescroll"} {
		zero(n)
	}
	i.def("nlog", 0, 0, func(i *Interp, _ []Value) (Value, error) { return I(int64(i.nlogs)), nil })
	i.def("getpid", 0, 0, func(*Interp, []Value) (Value, error) { return I(1), nil })
	i.def("gethostname", 0, 0, func(*Interp, []Value) (Value, error) { return S(""), nil })
	i.def("cputime", 0, 0, func(*Interp, []Value) (Value, error) { return F(0), nil })
	i.def("filename", 1, 1, func(_ *Interp, a []Value) (Value, error) { return a[0], nil })
	i.def("strcmpattr", 2, 2, func(_ *Interp, a []Value) (Value, error) {
		return I(int64(strings.Compare(a[0].s, a[1].s))), nil
	})
	i.def("textencode", 1, 1, func(_ *Interp, a []Value) (Value, error) {
		var sb strings.Builder
		for _, r := range a[0].s {
			if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
				sb.WriteRune(r)
			} else {
				fmt.Fprintf(&sb, "_%d_", r)
			}
		}
		return S(sb.String()), nil
	})
	i.def("textdecode", 1, 1, func(_ *Interp, a []Value) (Value, error) {
		var sb strings.Builder
		s := a[0].s
		for k := 0; k < len(s); k++ {
			if s[k] != '_' {
				sb.WriteByte(s[k])
				continue
			}
			e := strings.IndexByte(s[k+1:], '_')
			if e < 0 {
				sb.WriteByte('_')
				continue
			}
			n, err := strconv.Atoi(s[k+1 : k+1+e])
			if err != nil {
				sb.WriteByte('_')
				continue
			}
			sb.WriteRune(rune(n))
			k += e + 1
		}
		return S(sb.String()), nil
	})
}
