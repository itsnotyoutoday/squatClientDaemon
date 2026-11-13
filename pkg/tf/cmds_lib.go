package tf

import (
	"fmt"
	"strings"
)

// Commands that TinyFugue itself defines in its standard library or as thin
// wrappers over /def: /trig /trigp /trigc /trigpc /gag /hilite /partial
// /hook /untrig /undeft /unhook /nogag /nohilite, plus /trigger.

// defFrom builds and installs a macro from /def-style options plus a body.
func (i *Interp) defFrom(opts, body string) (*Macro, error) {
	line := opts
	if body != "" {
		line += " = " + body
	}
	m, err := i.parseDef(line)
	if err != nil {
		return nil, err
	}
	i.addMacro(m)
	return m, nil
}

// splitEq splits "pattern = body" at the first '='.
func splitEq(a string) (pat, body string) {
	if k := strings.IndexByte(a, '='); k >= 0 {
		return strings.TrimSpace(a[:k]), strings.TrimSpace(a[k+1:])
	}
	return strings.TrimSpace(a), ""
}

func (i *Interp) initLibCmds() {
	trig := func(name string, nums int) {
		i.reg(name, "/"+name+" [priority] [chance] pattern = body   (define an unnamed trigger)", true, func(i *Interp, a string) error {
			var pri, chance string
			rest := a
			switch name {
			case "trigp":
				pri, rest = firstWord(rest)
			case "trigc":
				chance, rest = firstWord(rest)
			case "trigpc":
				pri, rest = firstWord(rest)
				chance, rest = firstWord(rest)
			}
			if (name != "trig" && (pri == "" && chance == "")) || (nums == 2 && (pri == "" || chance == "")) {
				return fmt.Errorf("usage: /%s", name)
			}
			pat, body := splitEq(rest)
			if pat == "" {
				return fmt.Errorf("usage: /%s pattern = body", name)
			}
			opts := ""
			if pri != "" {
				opts += " -p" + quoteOpt(pri)
			}
			if chance != "" {
				opts += " -c" + quoteOpt(chance)
			}
			_, err := i.defFrom(opts+" -t"+quoteOpt(pat), body)
			return err
		})
	}
	trig("trig", 0)
	trig("trigp", 1)
	trig("trigc", 1)
	trig("trigpc", 2)

	i.reg("gag", "/gag [pattern [= response]]   (hide matching lines; no argument turns gagging on)", true, func(i *Interp, a string) error {
		if strings.TrimSpace(a) == "" {
			return i.setGlobal("gag", S("1"))
		}
		pat, body := splitEq(a)
		_, err := i.defFrom("-ag -t"+quoteOpt(pat), body)
		return err
	})
	i.reg("hilite", "/hilite [pattern [= response]]   (highlight matching lines using $hiliteattr)", true, func(i *Interp, a string) error {
		if strings.TrimSpace(a) == "" {
			return i.setGlobal("hilite", S("1"))
		}
		attr := i.getVarStr("hiliteattr")
		if attr == "" {
			attr = "h"
		}
		pat, body := splitEq(a)
		_, err := i.defFrom("-a"+quoteOpt(attr)+" -t"+quoteOpt(pat), body)
		return err
	})
	i.reg("partial", "/partial regexp   (highlight just the matching part of lines)", true, func(i *Interp, a string) error {
		pat := strings.TrimSpace(a)
		if pat == "" {
			return fmt.Errorf("usage: /partial regexp")
		}
		attr := i.getVarStr("hiliteattr")
		if attr == "" {
			attr = "h"
		}
		_, err := i.defFrom("-P0"+quoteOpt(attr)+" -F -mregexp -t"+quoteOpt(pat), "")
		return err
	})
	i.reg("hook", "/hook event[ pattern] [= body] | /hook [on|off]   (define a hook macro)", true, func(i *Interp, a string) error {
		a = strings.TrimSpace(a)
		switch strings.ToLower(a) {
		case "":
			state := "on"
			if i.getVarStr("hook") == "0" {
				state = "off"
			}
			i.echoText(i.curWorld(), "% Hooks are "+state)
			return nil
		case "on", "off":
			v := "1"
			if strings.EqualFold(a, "off") {
				v = "0"
			}
			return i.setGlobal("hook", S(v))
		}
		ev, body := splitEq(a)
		_, err := i.defFrom("-h"+quoteOpt(ev), body)
		return err
	})

	// removal helpers: simple (exact) comparison, as in TF
	removeWhere := func(pred func(*Macro) bool) int {
		n := 0
		for _, m := range append([]*Macro(nil), i.macros.list...) {
			if pred(m) {
				i.macros.remove(m)
				n++
			}
		}
		return n
	}
	i.reg("untrig", "/untrig [-a<attrs>] pattern   (remove the trigger with exactly that pattern)", false, func(i *Interp, a string) error {
		o, pat, err := parseOpts(a, "a", "")
		if err != nil {
			return err
		}
		pat = strings.TrimSpace(pat)
		want := ""
		if v, ok := o['a']; ok {
			want = v
		}
		if removeWhere(func(m *Macro) bool {
			if m.Trigger != pat {
				return false
			}
			at := m.Attr
			at.Gag = false
			as := at.String()
			if m.Gag {
				as = "g" + as
			}
			return as == want || (want == "" || want == "n") && as == ""
		}) == 0 {
			return fmt.Errorf("/untrig: no such trigger %q", pat)
		}
		return nil
	})
	i.reg("undeft", "/undeft pattern   (remove a macro triggered by exactly that pattern)", false, func(i *Interp, a string) error {
		pat := strings.TrimSpace(a)
		if removeWhere(func(m *Macro) bool { return m.Trigger == pat && !m.deleted }) == 0 {
			return fmt.Errorf("/undeft: no such trigger %q", pat)
		}
		return nil
	})
	i.reg("unhook", "/unhook event [pattern]   (remove the hook macro on that event)", false, func(i *Interp, a string) error {
		ev, pat := firstWord(strings.TrimSpace(a))
		ev = strings.ToUpper(ev)
		if removeWhere(func(m *Macro) bool { return m.Hook == ev && (pat == "" || m.HookPat == pat) }) == 0 {
			return fmt.Errorf("/unhook: no such hook %q", ev)
		}
		return nil
	})
	i.reg("nogag", "/nogag [pattern]   (remove gags; no argument turns gagging off)", false, func(i *Interp, a string) error {
		pat := strings.TrimSpace(a)
		if pat == "" {
			return i.setGlobal("gag", S("0"))
		}
		if removeWhere(func(m *Macro) bool { return m.Gag && m.Trigger == pat }) == 0 {
			return fmt.Errorf("/nogag: no such gag %q", pat)
		}
		return nil
	})
	i.reg("nohilite", "/nohilite [pattern]   (remove hilites; no argument turns hiliting off)", false, func(i *Interp, a string) error {
		pat := strings.TrimSpace(a)
		if pat == "" {
			return i.setGlobal("hilite", S("0"))
		}
		if removeWhere(func(m *Macro) bool { return !m.Attr.IsZero() && m.Trigger == pat }) == 0 {
			return fmt.Errorf("/nohilite: no such hilite %q", pat)
		}
		return nil
	})

	i.reg("trigger", "/trigger [-gnl] [-w[world]] [-h[event]] text   (run trigger or hook macros as if text arrived)", false, func(i *Interp, a string) error {
		// options may be glued (-gn) or valued (-wworld, -hevent)
		world := i.curWorld()
		var list, listFull, hook bool
		var event string
		rest := strings.TrimLeft(a, " \t")
		for strings.HasPrefix(rest, "-") && len(rest) > 1 && rest[1] != ' ' {
			tok, r := firstWord(rest)
			rest = r
			for k := 1; k < len(tok); k++ {
				switch tok[k] {
				case 'g':
				case 'n':
					list = true
				case 'l':
					list, listFull = true, true
				case 'w':
					if w := tok[k+1:]; w != "" {
						world = w
					}
					k = len(tok)
				case 'h':
					hook, event = true, tok[k+1:]
					k = len(tok)
				default:
					return fmt.Errorf("/trigger: invalid option -%c", tok[k])
				}
			}
		}
		text := strings.TrimSpace(rest)
		if hook {
			if event == "" {
				event, text = firstWord(text)
			}
			if !isHook(event) {
				return fmt.Errorf("/trigger: unknown hook %q", event)
			}
			var args []string
			if text != "" {
				args = strings.Fields(text)
			}
			if list {
				return i.listMatches(world, strings.ToUpper(event), text, listFull)
			}
			i.fireHook(world, event, args...)
			return nil
		}
		if list {
			return i.listMatches(world, "", text, listFull)
		}
		i.dispatch(world, text, []protoSpan{{S: text}}, -1<<31, nil)
		return nil
	})
}

// listMatches implements /trigger -n and -l: show what would match, grouped
// the way TF reports it (fall-thrus apply, then one of the highest-priority
// non-fall-thrus, then lower-priority matches that would not apply).
func (i *Interp) listMatches(world, event, text string, full bool) error {
	var cands []*Macro
	if event == "" {
		cands = i.macros.matching(func(m *Macro) bool { return m.Trigger != "" && worldOK(m, world) })
	} else {
		cands = i.macros.matching(func(m *Macro) bool { return m.Hook == event && worldOK(m, world) })
	}
	params := strings.Fields(text)
	header, ran, lower := 0, 0, -1<<31
	for _, m := range cands {
		if event == "" {
			mi, ok := m.match(text)
			if !ok || !i.passes(m, world, text, mi, params) {
				continue
			}
		} else if m.HookPat != "" && !globMatch(m.HookPat, text) {
			continue
		}
		if lower == -1<<31 {
			ran++
		}
		if header < 3 && lower != -1<<31 && m.Priority < lower {
			i.echoText(world, "% The following matching macros would not be applied:")
			header = 3
		}
		if header < 2 && !m.Fall {
			also := ""
			if ran > 1 {
				also = "also "
			}
			i.echoText(world, "% One of the following macros would "+also+"be applied:")
			lower, header = m.Priority, 2
		}
		if header < 1 {
			i.echoText(world, "% All of the following macros would be applied:")
			header = 1
		}
		if full {
			i.echoText(world, fmt.Sprintf("%% %d: %s", m.Num, m.Def()))
		} else {
			f := " "
			if m.Fall {
				f = "F"
			}
			i.echoText(world, fmt.Sprintf("%% %s %10d %s", f, m.Priority, m.Name))
		}
	}
	what, verb := "Text", "triggered"
	if event != "" {
		what, verb = "Event", "hooked"
	}
	pl := "s"
	if ran == 1 {
		pl = ""
	}
	i.echoText(world, fmt.Sprintf("%% %s would have %s %d macro%s.", what, verb, ran, pl))
	return nil
}
