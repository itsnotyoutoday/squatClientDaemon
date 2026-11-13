package tf

import (
	"fmt"
	"strings"
)

// splitAssign parses "name=value", "name value" or "name" (hasVal=false).
func splitAssign(a string) (name, val string, hasVal bool) {
	a = strings.TrimSpace(a)
	k := strings.IndexFunc(a, func(r rune) bool { return r == '=' || r == ' ' || r == '\t' })
	if k < 0 {
		return a, "", false
	}
	name = a[:k]
	rest := strings.TrimLeft(a[k:], " \t")
	if strings.HasPrefix(rest, "=") {
		rest = strings.TrimLeft(rest[1:], " \t")
	}
	return name, rest, true
}

func (i *Interp) initVarCmds() {
	i.reg("set", "/set name=value | /set name value | /set (list)", false, func(i *Interp, a string) error {
		if strings.TrimSpace(a) == "" {
			return cmdListvar(i, "")
		}
		n, v, has := splitAssign(a)
		if !has {
			val, ok := i.lookup(n)
			if !ok {
				return fmt.Errorf("/set: %s is not set", n)
			}
			i.echoText(i.curWorld(), fmt.Sprintf("/set %s=%s", n, quoteVal(val)))
			return nil
		}
		return i.setVar(n, S(v))
	})
	i.reg("setenv", "/setenv name=value   (set and export)", false, func(i *Interp, a string) error {
		n, v, has := splitAssign(a)
		if n == "" || !has {
			return fmt.Errorf("usage: /setenv name=value")
		}
		if err := i.setVar(n, S(v)); err != nil {
			return err
		}
		i.exported[n] = true
		return nil
	})
	i.reg("export", "/export name...", false, func(i *Interp, a string) error {
		for _, n := range words(a) {
			if _, ok := i.vars[n]; !ok {
				return fmt.Errorf("/export: %s is not set", n)
			}
			i.exported[n] = true
		}
		return nil
	})
	i.reg("unset", "/unset name...", false, func(i *Interp, a string) error {
		for _, n := range words(a) {
			if fr := i.top(); fr.locals != nil {
				if _, ok := fr.locals[n]; ok {
					delete(fr.locals, n)
					continue
				}
			}
			if !i.live {
				return fmt.Errorf("/unset: not allowed during replay")
			}
			delete(i.vars, n)
			delete(i.exported, n)
		}
		return nil
	})
	i.reg("let", "/let name=value   (local to the running macro)", false, func(i *Interp, a string) error {
		n, v, has := splitAssign(a)
		if n == "" || !has {
			return fmt.Errorf("usage: /let name=value")
		}
		return i.setLocal(n, S(v))
	})
	i.reg("listvar", "/listvar [glob]", false, cmdListvar)
	i.reg("shift", "/shift [n]   (drop leading positional parameters)", false, func(i *Interp, a string) error {
		n := 1
		if strings.TrimSpace(a) != "" {
			n = int(S(strings.TrimSpace(a)).Int())
		}
		fr := i.top()
		if n > len(fr.params) {
			n = len(fr.params)
		}
		if n > 0 {
			fr.params = fr.params[n:]
			fr.args = strings.Join(fr.params, " ")
		}
		return nil
	})
}

func cmdListvar(i *Interp, a string) error {
	for _, n := range i.listVars(strings.TrimSpace(a)) {
		cmd := "/set"
		if i.exported[n] {
			cmd = "/setenv"
		}
		i.echoText(i.curWorld(), fmt.Sprintf("%s %s=%s", cmd, n, quoteVal(i.vars[n])))
	}
	return nil
}
