package tf

import (
	"fmt"
	"strings"
)

func (i *Interp) initWorldCmds() {
	i.reg("connect", "/connect [world]", false, func(i *Interp, a string) error {
		return i.worldOp("/connect", a, i.host.Connect)
	})
	i.reg("dc", "/dc [world]", false, func(i *Interp, a string) error {
		return i.worldOp("/dc", a, i.host.Disconnect)
	})
	i.reg("fg", "/fg [world]", false, func(i *Interp, a string) error {
		return i.worldOp("/fg", a, i.host.Foreground)
	})
	i.reg("world", "/world name   (connect if needed and bring to the foreground)", false, func(i *Interp, a string) error {
		return i.worldOp("/world", a, func(w string) error {
			if !i.host.IsConnected(w) {
				if err := i.host.Connect(w); err != nil {
					return err
				}
			}
			return i.host.Foreground(w)
		})
	})
	i.reg("addworld", "/addworld [-T<type>] [-x] name [character password] host port", false, cmdAddWorld)
	i.reg("unworld", "/unworld name", false, func(i *Interp, a string) error {
		return i.worldOp("/unworld", a, i.host.RemoveWorld)
	})
	i.reg("listworlds", "/listworlds [glob]", false, func(i *Interp, a string) error {
		for _, w := range i.host.ListWorlds() {
			if a != "" && !globMatch(strings.TrimSpace(a), w.Name) {
				continue
			}
			i.echoText(i.curWorld(), fmt.Sprintf("/addworld %s %s %s", w.Name, w.Host, w.Port))
		}
		return nil
	})
	i.reg("listsockets", "/listsockets", false, func(i *Interp, _ string) error {
		for _, w := range i.host.ListWorlds() {
			if w.Connected {
				i.echoText(i.curWorld(), fmt.Sprintf("%s  %s %s", w.Name, w.Host, w.Port))
			}
		}
		return nil
	})
	i.reg("saveworld", "/saveworld", false, func(i *Interp, _ string) error {
		if err := i.needWorld("/saveworld"); err != nil {
			return err
		}
		return i.host.SaveWorlds()
	})
	i.reg("localecho", "/localecho on|off", false, func(i *Interp, a string) error {
		if i.live {
			i.host.LocalEcho(i.curWorld(), S(strings.TrimSpace(a)).Truth() && strings.TrimSpace(a) != "off")
		}
		return nil
	})
	i.reg("log", "/log [-w<world>] off|<file>", false, func(i *Interp, a string) error {
		if err := i.needFile("/log"); err != nil {
			return err
		}
		o, rest, err := parseOpts(a, "w", "")
		if err != nil {
			return err
		}
		w := i.curWorld()
		if v, ok := o['w']; ok {
			w = v
		}
		rest = strings.TrimSpace(rest)
		on := rest != "off" && rest != ""
		if err := i.host.Log(w, on, rest); err != nil {
			return err
		}
		if on {
			i.nlogs++
		} else if i.nlogs > 0 {
			i.nlogs--
		}
		return nil
	})
	for _, op := range []string{"limit", "unlimit", "relimit"} {
		op := op
		i.reg(op, "/"+op+" [pattern]   (view filter, handled by the host)", false, func(i *Interp, a string) error {
			if i.live {
				i.host.Limit(i.curWorld(), op, strings.TrimSpace(a))
			}
			return nil
		})
	}
	for _, op := range []string{"status_add", "status_edit", "status_rm"} {
		op := op
		i.reg(op, "/"+op+" [options] field   (status bar, handled by the host)", false, func(i *Interp, a string) error {
			if i.live {
				w := words(a)
				if len(w) == 0 {
					return fmt.Errorf("usage: /%s field", op)
				}
				i.host.Status(op, w[len(w)-1], w[:len(w)-1]...)
			}
			return nil
		})
	}
	i.reg("histsize", "/histsize [n]", false, func(i *Interp, a string) error {
		if strings.TrimSpace(a) == "" {
			i.echoText(i.curWorld(), "% histsize="+i.getVarStr("histsize"))
			return nil
		}
		return i.setVar("histsize", S(strings.TrimSpace(a)))
	})
}

func (i *Interp) worldOp(cmd, arg string, op func(string) error) error {
	if err := i.needWorld(cmd); err != nil {
		return err
	}
	if !i.live {
		return nil
	}
	w := strings.TrimSpace(arg)
	if w == "" {
		w = i.curWorld()
	}
	if w == "" {
		return fmt.Errorf("%s: no world given", cmd)
	}
	return op(w)
}

func cmdAddWorld(i *Interp, a string) error {
	if err := i.needWorld("/addworld"); err != nil {
		return err
	}
	o, rest, err := parseOpts(a, "T", "pxes")
	if err != nil {
		return err
	}
	w := words(rest)
	wi := WorldInfo{Type: o['T']}
	_, wi.TLS = o['x']
	switch len(w) {
	case 3:
		wi.Name, wi.Host, wi.Port = w[0], w[1], w[2]
	case 5:
		wi.Name, wi.Character, wi.Password, wi.Host, wi.Port = w[0], w[1], w[2], w[3], w[4]
	default:
		return fmt.Errorf("usage: /addworld [-T<type>] [-x] name [character password] host port")
	}
	if !i.live {
		return nil
	}
	return i.host.AddWorld(wi)
}
