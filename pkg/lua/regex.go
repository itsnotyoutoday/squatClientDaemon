package lua

import (
	"regexp"

	glua "github.com/yuin/gopher-lua"
)

// regexTable builds squat.regex. Patterns use Go RE2 syntax (linear time, no
// backreferences or lookaround); see https://github.com/google/re2/wiki/Syntax.
//
//	squat.regex.match(pattern, text)       -> captures table or nil
//	squat.regex.test(pattern, text)        -> boolean
//	squat.regex.replace(pattern, text, rp) -> string (rp uses $1, ${name})
//
// match returns m[0] (whole match), m[1..n] (groups; "" when a group did not
// participate) and m.named[name].
func (r *Runtime) regexTable(L *glua.LState) *glua.LTable {
	t := L.NewTable()
	compile := func(L *glua.LState) *regexp.Regexp {
		p := L.CheckString(1)
		if len(p) > maxPatternLen {
			L.ArgError(1, "pattern too long")
		}
		re, err := r.cachedRegex(p)
		if err != nil {
			L.ArgError(1, err.Error())
		}
		return re
	}
	t.RawSetString("match", L.NewFunction(func(L *glua.LState) int {
		re := compile(L)
		text := L.CheckString(2)
		idx := re.FindStringSubmatchIndex(text)
		if idx == nil {
			L.Push(glua.LNil)
			return 1
		}
		m := matchTable(L, re, text, idx, r.cur.world, r.cur.live)
		for _, k := range []string{"text", "world", "live"} {
			m.RawSetString(k, glua.LNil)
		}
		L.Push(m)
		return 1
	}))
	t.RawSetString("test", L.NewFunction(func(L *glua.LState) int {
		re := compile(L)
		L.Push(glua.LBool(re.MatchString(L.CheckString(2))))
		return 1
	}))
	t.RawSetString("replace", L.NewFunction(func(L *glua.LState) int {
		re := compile(L)
		L.Push(glua.LString(re.ReplaceAllString(L.CheckString(2), L.CheckString(3))))
		return 1
	}))
	return t
}
