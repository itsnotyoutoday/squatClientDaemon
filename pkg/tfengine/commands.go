package tfengine

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/itsnotyoutoday/gtfparser"
)

// This file registers, with the engine's public RegisterCommand, the commands
// that need the client's real data: its world book, session list, scrollback,
// logs and window. docs/COMMAND-STATUS.md lists every real tf builtin and how
// this client answers it.

// WorldRenamer is optionally implemented by a Host that can rename a world in
// its book (/renameworld).
type WorldRenamer interface{ RenameWorld(old, new string) error }

func (a *Engine) cmd(name string, f func(in *gtfparser.Interp, args string) (ok bool)) {
	a.in.RegisterCommand(name, func(in *gtfparser.Interp, args string) gtfparser.Value {
		return gtfparser.Bool(f(in, args))
	})
}

// allow applies /restrict and the deployment Policy to a privileged command.
func (a *Engine) allow(in *gtfparser.Interp, op gtfparser.Op, who, detail string) bool {
	level := gtfparser.RestrictWorld
	switch op {
	case gtfparser.OpShell:
		level = gtfparser.RestrictShell
	case gtfparser.OpFileRead, gtfparser.OpFileWrite, gtfparser.OpDirChange:
		level = gtfparser.RestrictFile
	}
	if in.Restriction() >= level {
		a.warn(who + ": restricted")
		return false
	}
	if a.opts.Policy != nil {
		if err := a.opts.Policy(op.String(), detail); err != nil {
			a.warn(who + ": " + err.Error())
			return false
		}
	}
	return true
}

func (a *Engine) fail(who string, err error) bool {
	a.warn(who + ": " + err.Error())
	return false
}

// optArgs reads leading -x (flags) and -xVALUE / -x VALUE (valued) options.
func optArgs(s, valued, flags string) (opts map[byte]string, rest string, err error) {
	opts = map[byte]string{}
	rest = strings.TrimLeft(s, " \t")
	for len(rest) > 1 && rest[0] == '-' {
		c := rest[1]
		switch {
		case strings.IndexByte(valued, c) >= 0:
			v := rest[2:]
			if v == "" {
				fs := strings.Fields(rest)
				if len(fs) < 2 {
					return nil, "", fmt.Errorf("option -%c needs a value", c)
				}
				v = fs[1]
				rest = strings.TrimLeft(strings.TrimPrefix(rest, fs[0]), " \t")
				rest = strings.TrimLeft(strings.TrimPrefix(rest, v), " \t")
			} else {
				i := strings.IndexAny(v, " \t")
				if i < 0 {
					rest = ""
				} else {
					rest, v = strings.TrimLeft(v[i:], " \t"), v[:i]
				}
			}
			opts[c] = v
		case strings.IndexByte(flags, c) >= 0:
			opts[c] = ""
			rest = strings.TrimLeft(rest[2:], " \t")
		default:
			if rest == "--" || strings.HasPrefix(rest, "-- ") {
				rest = strings.TrimLeft(rest[2:], " \t")
			}
			return
		}
	}
	return
}

func (a *Engine) registerCommands() {
	in := a.in

	// ---- connections ----
	worldOp := func(who string, op func(string) error) func(*gtfparser.Interp, string) bool {
		return func(in *gtfparser.Interp, args string) bool {
			if !a.allow(in, gtfparser.OpNewWorld, who, args) {
				return false
			}
			w := strings.TrimSpace(args)
			if w == "" {
				w = a.cur
			}
			if w == "" {
				return a.fail(who, fmt.Errorf("no world given"))
			}
			if err := op(w); err != nil {
				return a.fail(who, err)
			}
			return true
		}
	}
	a.cmd("connect", worldOp("/connect", a.host.Connect))
	a.cmd("dc", worldOp("/dc", a.host.Disconnect))
	a.cmd("fg", worldOp("/fg", a.host.Foreground))
	a.cmd("world", worldOp("/world", func(w string) error {
		if !a.host.IsConnected(w) {
			if err := a.host.Connect(w); err != nil {
				return err
			}
		}
		return a.host.Foreground(w)
	}))
	a.cmd("unworld", worldOp("/unworld", a.host.RemoveWorld))
	a.cmd("addworld", func(in *gtfparser.Interp, args string) bool {
		if !a.allow(in, gtfparser.OpNewWorld, "/addworld", args) {
			return false
		}
		o, rest, err := optArgs(args, "T", "pxes")
		if err != nil {
			return a.fail("/addworld", err)
		}
		w := strings.Fields(rest)
		wi := WorldInfo{Type: o['T']}
		_, wi.TLS = o['x']
		switch len(w) {
		case 3:
			wi.Name, wi.Host, wi.Port = w[0], w[1], w[2]
		case 5:
			wi.Name, wi.Character, wi.Password, wi.Host, wi.Port = w[0], w[1], w[2], w[3], w[4]
		default:
			return a.fail("/addworld", fmt.Errorf("usage: /addworld [-T<type>] [-x] name [character password] host port"))
		}
		if err := a.host.AddWorld(wi); err != nil {
			return a.fail("/addworld", err)
		}
		return true
	})
	a.cmd("saveworld", func(in *gtfparser.Interp, args string) bool {
		if !a.allow(in, gtfparser.OpNewWorld, "/saveworld", args) {
			return false
		}
		if err := a.host.SaveWorlds(); err != nil {
			return a.fail("/saveworld", err)
		}
		return true
	})
	a.cmd("loadworld", func(in *gtfparser.Interp, args string) bool {
		a.echoText("% /loadworld: this client keeps its worlds in its own book; edit them in the Worlds dialog (nothing to load)")
		return false
	})
	a.cmd("renameworld", func(in *gtfparser.Interp, args string) bool {
		if !a.allow(in, gtfparser.OpNewWorld, "/renameworld", args) {
			return false
		}
		f := strings.Fields(args)
		var from, to string
		switch len(f) {
		case 1:
			from, to = a.cur, f[0]
		case 2:
			from, to = f[0], f[1]
		default:
			return a.fail("/renameworld", fmt.Errorf("usage: /renameworld [old] new"))
		}
		rn, ok := a.host.(WorldRenamer)
		if !ok {
			return a.fail("/renameworld", fmt.Errorf("not available in this client"))
		}
		if err := rn.RenameWorld(from, to); err != nil {
			return a.fail("/renameworld", err)
		}
		return true
	})
	a.cmd("listworlds", func(in *gtfparser.Interp, args string) bool {
		o, pat, _ := optArgs(args, "", "sum")
		_, short := o['s']
		pat = strings.TrimSpace(pat)
		for _, w := range a.host.ListWorlds() {
			if pat != "" && !globMatch(pat, w.Name) {
				continue
			}
			if short {
				a.echoText(w.Name)
				continue
			}
			a.echoText(fmt.Sprintf("/addworld %s %s %s", w.Name, w.Host, w.Port))
		}
		return true
	})
	a.cmd("listsockets", func(in *gtfparser.Interp, args string) bool {
		fg := a.host.ForegroundWorld()
		for _, w := range a.host.ListWorlds() {
			if !w.Connected {
				continue
			}
			mark := " "
			if strings.EqualFold(w.Name, fg) {
				mark = "*"
			}
			a.echoText(fmt.Sprintf("%s %s  %s %s", mark, w.Name, w.Host, w.Port))
		}
		return true
	})

	// ---- display and window ----
	a.cmd("beep", func(in *gtfparser.Interp, args string) bool { a.host.Bell(); return true })
	a.cmd("notify", func(in *gtfparser.Interp, args string) bool {
		t, b, _ := strings.Cut(args, "|")
		a.host.Notify(strings.TrimSpace(t), strings.TrimSpace(b))
		return true
	})
	a.cmd("localecho", func(in *gtfparser.Interp, args string) bool {
		v := strings.ToLower(strings.TrimSpace(args))
		a.host.LocalEcho(a.cur, v != "off" && v != "0")
		return true
	})
	a.cmd("log", func(in *gtfparser.Interp, args string) bool {
		if !a.allow(in, gtfparser.OpFileWrite, "/log", args) {
			return false
		}
		o, rest, err := optArgs(args, "w", "")
		if err != nil {
			return a.fail("/log", err)
		}
		w := a.cur
		if v, ok := o['w']; ok {
			w = v
		}
		rest = strings.TrimSpace(rest)
		on := rest != "off" && rest != ""
		if err := a.host.Log(w, on, rest); err != nil {
			return a.fail("/log", err)
		}
		if on {
			a.nlogs++
		} else if a.nlogs > 0 {
			a.nlogs--
		}
		return true
	})
	for _, op := range []string{"limit", "unlimit", "relimit"} {
		op := op
		a.cmd(op, func(in *gtfparser.Interp, args string) bool {
			a.host.Limit(a.cur, op, strings.TrimSpace(args))
			return true
		})
	}
	a.cmd("recall", func(in *gtfparser.Interp, args string) bool {
		o, rest, err := optArgs(args, "w", "")
		if err != nil {
			return a.fail("/recall", err)
		}
		w := a.cur
		if v, ok := o['w']; ok {
			w = v
		}
		a.host.Recall(w, rest)
		return true
	})
	for _, op := range []string{"status_add", "status_edit", "status_rm", "status_clear"} {
		op := op
		a.cmd(op, func(in *gtfparser.Interp, args string) bool {
			f := strings.Fields(args)
			if op == "status_clear" {
				a.host.Status(op, "")
				return true
			}
			if len(f) == 0 {
				return a.fail("/"+op, fmt.Errorf("usage: /%s field", op))
			}
			a.host.Status(op, f[len(f)-1], f[:len(f)-1]...)
			return true
		})
	}
	a.cmd("input", func(in *gtfparser.Interp, args string) bool { a.host.Input(a.cur, args); return true })
	a.cmd("dokey", func(in *gtfparser.Interp, args string) bool {
		a.host.DoKey(a.cur, strings.TrimSpace(args))
		return true
	})
	a.cmd("suspend", func(in *gtfparser.Interp, args string) bool {
		a.echoText("% /suspend: type /suspend (or /detach) as a command of its own; scripts cannot suspend the client")
		return false
	})
	a.cmd("histsize", func(in *gtfparser.Interp, args string) bool {
		if v := strings.TrimSpace(args); v != "" {
			in.SetVar("histsize", v)
			return true
		}
		a.echoText("% histsize=" + in.Var("histsize"))
		return true
	})
	a.cmd("features", func(in *gtfparser.Interp, args string) bool {
		a.echoText("% features: 256colors truecolor utf8 regexp attr-markup process=repeat")
		return true
	})
	a.cmd("help", func(in *gtfparser.Interp, args string) bool { a.help(args); return true })
	quit := func(in *gtfparser.Interp, args string) bool { a.host.Quit(); return true }
	a.cmd("quit", quit)

	// ---- macro-library commands real tf defines in its library, not in C ----
	a.cmd("undefn", func(in *gtfparser.Interp, args string) bool {
		ok := true
		for _, w := range strings.Fields(args) {
			n, err := strconv.Atoi(strings.TrimPrefix(w, "#"))
			if err != nil || !in.UndefID(n) {
				a.warn("/undefn: no macro number " + w)
				ok = false
			}
		}
		return ok
	})
	removeWhere := func(pred func(*gtfparser.Macro) bool) int {
		n := 0
		for _, m := range in.Macros() {
			if pred(m) && in.UndefID(m.ID) {
				n++
			}
		}
		return n
	}
	a.cmd("untrig", func(in *gtfparser.Interp, args string) bool {
		_, pat, _ := optArgs(args, "a", "")
		pat = strings.TrimSpace(pat)
		if removeWhere(func(m *gtfparser.Macro) bool { return m.Trigger == pat }) == 0 {
			return a.fail("/untrig", fmt.Errorf("no such trigger %q", pat))
		}
		return true
	})
	a.cmd("undeft", func(in *gtfparser.Interp, args string) bool {
		pat := strings.TrimSpace(args)
		if removeWhere(func(m *gtfparser.Macro) bool { return m.Trigger == pat }) == 0 {
			return a.fail("/undeft", fmt.Errorf("no such trigger %q", pat))
		}
		return true
	})
	a.cmd("unhook", func(in *gtfparser.Interp, args string) bool {
		ev, pat := cut(strings.TrimSpace(args))
		ev = strings.ToUpper(ev)
		if removeWhere(func(m *gtfparser.Macro) bool {
			e, p := cut(m.Hook)
			return strings.EqualFold(e, ev) && (pat == "" || p == pat)
		}) == 0 {
			return a.fail("/unhook", fmt.Errorf("no such hook %q", ev))
		}
		return true
	})
	a.cmd("nogag", func(in *gtfparser.Interp, args string) bool {
		pat := strings.TrimSpace(args)
		if pat == "" {
			in.SetVar("gag", "off")
			return true
		}
		if removeWhere(func(m *gtfparser.Macro) bool { return (m.Gag || attrsGag(m.Attrs)) && m.Trigger == pat }) == 0 {
			return a.fail("/nogag", fmt.Errorf("no such gag %q", pat))
		}
		return true
	})
	a.cmd("nohilite", func(in *gtfparser.Interp, args string) bool {
		pat := strings.TrimSpace(args)
		if pat == "" {
			in.SetVar("hilite", "off")
			return true
		}
		if removeWhere(func(m *gtfparser.Macro) bool { return m.Attrs != "" && m.Trigger == pat }) == 0 {
			return a.fail("/nohilite", fmt.Errorf("no such hilite %q", pat))
		}
		return true
	})
	a.cmd("partial", func(in *gtfparser.Interp, args string) bool {
		pat := strings.TrimSpace(args)
		if pat == "" {
			return a.fail("/partial", fmt.Errorf("usage: /partial regexp"))
		}
		attr := in.Var("hiliteattr")
		if attr == "" {
			attr = "h"
		}
		in.Exec("/def -P0" + attr + " -F -mregexp -t'" + strings.ReplaceAll(pat, "'", `\'`) + "'")
		return true
	})
	// -g (gag) is an old SquatClient extension of /def: "-g" means "-ag", and
	// it keeps working next to an -a option (which would replace -ag).
	if def, ok := in.Command("def"); ok {
		in.RegisterCommand("def", func(in *gtfparser.Interp, args string) gtfparser.Value {
			fs := strings.Fields(args)
			gag := false
			for _, f := range fs {
				if !strings.HasPrefix(f, "-") {
					break
				}
				if f == "-g" {
					gag = true
				}
			}
			if gag {
				args = dropWord(args, "-g")
			}
			before := len(in.Macros())
			v := def(in, args)
			if gag {
				if ms := in.Macros(); len(ms) > before {
					ms[len(ms)-1].Gag = true
				}
			}
			return v
		})
	}
}

func cut(s string) (string, string) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

// dropWord removes the first whitespace-delimited occurrence of w.
func dropWord(s, w string) string {
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		j := i
		for j < len(s) && s[j] != ' ' && s[j] != '\t' {
			j++
		}
		if s[i:j] == w {
			return s[:i] + strings.TrimLeft(s[j:], " \t")
		}
		i = j
	}
	return s
}

// help is /help [topic]: the host's help browser if it has one, otherwise a
// listing of the commands.
func (a *Engine) help(topic string) {
	topic = strings.TrimPrefix(strings.TrimSpace(topic), "/")
	if hp, ok := a.host.(HelpProvider); ok {
		hp.Help(a.cur, topic)
		return
	}
	var names []string
	for _, st := range CommandStatuses() {
		if st.Status != Unsupported {
			names = append(names, st.Name)
		}
	}
	sort.Strings(names)
	a.echoText("% commands: " + strings.Join(names, " "))
	a.echoText("% see docs/TF-ENGINE.md and docs/COMMAND-STATUS.md")
}
