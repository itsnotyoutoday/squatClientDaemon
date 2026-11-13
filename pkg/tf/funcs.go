package tf

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

type funcDef struct {
	min, max int // max < 0: variadic
	fn       func(i *Interp, a []Value) (Value, error)
}

func (i *Interp) def(name string, min, max int, fn func(*Interp, []Value) (Value, error)) {
	i.funcs[name] = &funcDef{min, max, fn}
}

func (i *Interp) initFuncs() {
	i.funcs = map[string]*funcDef{}
	i.initStringFuncs()
	i.initMathFuncs()
	i.initTimeFuncs()
	i.initMiscFuncs()
}

// FuncNames lists the built-in function names.
func FuncNames() []string {
	i := &Interp{}
	i.initFuncs()
	var out []string
	for n := range i.funcs {
		out = append(out, n)
	}
	sortStrings(out)
	return out
}

func (i *Interp) callFunc(name string, args []Value) (Value, error) {
	if f := i.funcs[name]; f != nil {
		if len(args) < f.min || (f.max >= 0 && len(args) > f.max) {
			return Value{}, fmt.Errorf("%s(): wrong number of arguments (%d)", name, len(args))
		}
		v, err := f.fn(i, args)
		if len(v.s) > maxValueLen {
			return Value{}, fmt.Errorf("%s(): result too long", name)
		}
		return v, err
	}
	// A user macro can be called like a function: its /return value is the result.
	if m := i.macros.byName(name); m != nil {
		strs := make([]string, len(args))
		for k, a := range args {
			strs[k] = a.s
		}
		i.last = Value{}
		err := i.callMacroWith(m, strings.Join(strs, " "), strs, i.top().world, nil)
		return i.last, err
	}
	return Value{}, fmt.Errorf("unknown function %s()", name)
}

func (i *Interp) compileRe(p string) (*regexp.Regexp, error) {
	if re, ok := i.reCache[p]; ok {
		return re, nil
	}
	re, err := compileTFRegexp(p)
	if err != nil {
		return nil, err
	}
	if len(i.reCache) > 256 {
		i.reCache = map[string]*regexp.Regexp{}
	}
	i.reCache[p] = re
	return re, nil
}

// compileTFRegexp compiles a pattern the way TinyFugue treats regexps: a
// pattern containing no upper-case letter matches case-insensitively.
func compileTFRegexp(p string) (*regexp.Regexp, error) {
	for _, r := range p {
		if unicode.IsUpper(r) {
			return regexp.Compile(p)
		}
	}
	return regexp.Compile("(?i)" + p)
}
