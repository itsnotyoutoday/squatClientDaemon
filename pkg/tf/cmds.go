package tf

import (
	"fmt"
	"sort"
	"strings"
)

type command struct {
	name  string
	usage string
	raw   bool // receives unexpanded arguments
	fn    func(i *Interp, args string) error
}

func (i *Interp) reg(name, usage string, raw bool, fn func(*Interp, string) error) {
	i.cmds[name] = &command{name: name, usage: usage, raw: raw, fn: fn}
}

func (i *Interp) initCommands() {
	i.cmds = map[string]*command{}
	i.initCoreCmds()
	i.initVarCmds()
	i.initMacroCmds()
	i.initWorldCmds()
	i.initMiscCmds()
}

// CommandNames lists the built-in command names.
func CommandNames() []string {
	i := &Interp{}
	i.initCommands()
	var out []string
	for n := range i.cmds {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func sortStrings(s []string) { sort.Strings(s) }

// ---- gating -----------------------------------------------------------------

func (i *Interp) needFile(cmd string) error {
	if i.restrict >= RestrictFile {
		return fmt.Errorf("%s: file access is restricted", cmd)
	}
	return nil
}

func (i *Interp) needWorld(cmd string) error {
	if i.restrict >= RestrictWorld {
		return fmt.Errorf("%s: world access is restricted", cmd)
	}
	return nil
}

// ---- core output / flow commands --------------------------------------------

func (i *Interp) initCoreCmds() {
	i.reg("echo", "/echo [-w<world>] [-a<attrs>] [-p] text   (-p: interpret @{attr} markup)", false, cmdEcho)
	i.reg("send", "/send [-w<world>] text", false, cmdSend)
	i.reg("beep", "/beep", false, func(i *Interp, _ string) error {
		if i.live {
			i.host.Bell()
		}
		return nil
	})
	i.reg("notify", "/notify title [| body]   (extension: desktop notification)", false, func(i *Interp, a string) error {
		if !i.live {
			return nil
		}
		t, b, _ := strings.Cut(a, "|")
		i.host.Notify(strings.TrimSpace(t), strings.TrimSpace(b))
		return nil
	})
	i.reg("break", "/break   (leave the innermost /while, /do or /for)", false, func(*Interp, string) error { return errBreak })
	i.reg("return", "/return [expr]   (end the macro with a value)", true, func(i *Interp, a string) error {
		if strings.TrimSpace(a) != "" {
			v, err := i.evalExprText(a)
			if err != nil {
				return err
			}
			i.top().ret = v
		}
		return errReturn
	})
	i.reg("eval", "/eval text   (expand, then run as commands)", false, func(i *Interp, a string) error {
		return i.execBody(a)
	})
	i.reg("test", "/test expression   (evaluate; result available to $(...) and as the macro value)", true, func(i *Interp, a string) error {
		v, err := i.evalExprText(a)
		i.last = v
		return err
	})
	i.reg("builtin", "/builtin command args   (run a built-in even if a macro shadows it)", true, func(i *Interp, a string) error {
		n, rest := firstWord(a)
		c := i.cmds[strings.TrimPrefix(n, "/")]
		if c == nil {
			return fmt.Errorf("/builtin: no such command %q", n)
		}
		if !c.raw {
			var err error
			if rest, err = i.expand(rest); err != nil {
				return err
			}
		}
		return c.fn(i, rest)
	})
}

func cmdEcho(i *Interp, args string) error {
	o, text, err := parseOpts(args, "wa", "pe")
	if err != nil {
		return err
	}
	var attr Attr
	if a, ok := o['a']; ok {
		if attr, err = ParseAttr(a); err != nil {
			return err
		}
	}
	if !i.live {
		return nil
	}
	world := i.curWorld()
	if w, ok := o['w']; ok {
		world = w
	}
	if _, ok := o['p']; ok {
		i.echoSpans(world, SpansFromMarkup(text, attr))
		return nil
	}
	i.echoSpans(world, []protoSpan{attr.Apply(protoSpan{S: text})})
	return nil
}

func cmdSend(i *Interp, args string) error {
	o, text, err := parseOpts(args, "w", "n")
	if err != nil {
		return err
	}
	world := i.curWorld()
	if w, ok := o['w']; ok {
		world = w
	}
	return i.sendText(world, text)
}

// ---- timers and /quote ------------------------------------------------------

func parseInterval(s string) (float64, error) {
	var total float64
	for _, p := range strings.Split(s, ":") {
		v := S(p)
		if !v.num || v.Float() < 0 {
			return 0, fmt.Errorf("bad time %q", s)
		}
		total = total*60 + v.Float()
	}
	return total, nil
}

func (i *Interp) initMiscCmds() {
	i.reg("repeat", "/repeat [-w<world>] -<seconds|h:m:s> <count> <command>   (without -time: run count times now)", true, cmdRepeat)
	i.reg("ps", "/ps   (list /repeat timers)", false, func(i *Interp, _ string) error {
		var ids []int
		for id := range i.timers {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		for _, id := range ids {
			t := i.timers[id]
			i.echoText(i.curWorld(), fmt.Sprintf("%d: every %gs, %d left: %s", id, t.every.Seconds(), t.left, t.cmd))
		}
		return nil
	})
	i.reg("kill", "/kill id...   (stop /repeat timers)", false, func(i *Interp, a string) error {
		for _, w := range words(a) {
			v := S(w)
			t := i.timers[int(v.Int())]
			if t == nil {
				return fmt.Errorf("/kill: no such process %s", w)
			}
			t.cancel()
			delete(i.timers, t.id)
		}
		return nil
	})
	i.reg("quote", "/quote [-d<send|echo|exec>] [-w<world>] prefix 'file | #command   (shell forms are unavailable)", false, cmdQuote)
	i.reg("trigger", "/trigger [-w<world>] text   (run trigger macros as if text arrived)", false, func(i *Interp, a string) error {
		o, text, err := parseOpts(a, "w", "n")
		if err != nil {
			return err
		}
		w := i.curWorld()
		if v, ok := o['w']; ok {
			w = v
		}
		i.dispatch(w, text, []protoSpan{{S: text}}, -1<<31, nil)
		return nil
	})
	i.reg("trigpc", "/trigpc chance priority text   (trigger with a probability, only macros of at least that priority)", false, func(i *Interp, a string) error {
		c, r := firstWord(a)
		p, text := firstWord(r)
		if !S(c).num || !S(p).num || text == "" {
			return fmt.Errorf("usage: /trigpc chance priority text")
		}
		if i.rng(100) >= S(c).Int() {
			return nil
		}
		i.dispatch(i.curWorld(), text, []protoSpan{{S: text}}, int(S(p).Int()), nil)
		return nil
	})
	i.reg("hook", "/hook [NAME args...]   (no arguments: list hook names; otherwise fire the hook)", false, func(i *Interp, a string) error {
		if strings.TrimSpace(a) == "" {
			i.echoText(i.curWorld(), strings.Join(HookNames(), " "))
			return nil
		}
		n, rest := firstWord(a)
		if !isHook(n) {
			return fmt.Errorf("/hook: unknown hook %q", n)
		}
		i.fireHook(i.curWorld(), n, words(rest)...)
		return nil
	})
	i.reg("restrict", "/restrict [shell|file|world]   (raise the restriction level; it cannot be lowered)", false, func(i *Interp, a string) error {
		names := []string{"shell", "file", "world"}
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			i.echoText(i.curWorld(), "% restrict level: "+names[i.restrict])
			return nil
		}
		for k, n := range names {
			if n == a {
				if Restrict(k) > i.restrict {
					i.restrict = Restrict(k)
				}
				return nil
			}
		}
		return fmt.Errorf("/restrict: level must be shell, file or world")
	})
	i.reg("version", "/version", false, func(i *Interp, _ string) error {
		i.echoText(i.curWorld(), "% "+i.opts.Version)
		return nil
	})
	i.reg("features", "/features", false, func(i *Interp, _ string) error {
		i.echoText(i.curWorld(), "% features: 256colors truecolor utf8 regexp attr-markup process=repeat")
		return nil
	})
	i.reg("help", "/help [command]", false, func(i *Interp, a string) error {
		a = strings.TrimPrefix(strings.TrimSpace(a), "/")
		if c := i.cmds[a]; c != nil {
			i.echoText(i.curWorld(), c.usage)
			return nil
		}
		var names []string
		for n := range i.cmds {
			names = append(names, n)
		}
		sort.Strings(names)
		i.echoText(i.curWorld(), "% commands: "+strings.Join(names, " "))
		i.echoText(i.curWorld(), "% /help <command> shows usage; see docs/TF-LANGUAGE.md")
		return nil
	})
	quit := func(i *Interp, _ string) error {
		if i.live {
			i.host.Quit()
		}
		return nil
	}
	i.reg("exit", "/exit", false, quit)
	i.reg("quit", "/quit", false, quit)
	for _, n := range []string{"edit", "recordline", "watchdog", "watchname", "liststreams", "suspend", "sh", "core", "lcd"} {
		n := n
		i.reg(n, "/"+n+" (not available)", false, func(*Interp, string) error {
			return fmt.Errorf("/%s: not implemented in this client (see docs/TF-COMPAT.md)", n)
		})
	}
}

func cmdRepeat(i *Interp, args string) error {
	o, rest, err := parseOpts(args, "w", "")
	if err != nil {
		return err
	}
	world := i.curWorld()
	if w, ok := o['w']; ok {
		world = w
	}
	var every float64
	timed := false
	if strings.HasPrefix(rest, "-") {
		var tok string
		tok, rest = firstWord(rest)
		if every, err = parseInterval(tok[1:]); err != nil {
			return fmt.Errorf("/repeat: %w", err)
		}
		timed = true
	}
	cs, cmd := firstWord(rest)
	cv := S(cs)
	if !cv.num || cmd == "" || cv.Int() < 1 {
		return fmt.Errorf("usage: /repeat [-w<world>] -<seconds> <count> <command>")
	}
	count := int(cv.Int())
	if !timed {
		if count > 10000 {
			return fmt.Errorf("/repeat: count too large")
		}
		for n := 0; n < count; n++ {
			if err := i.tick(); err != nil {
				return err
			}
			if err := i.execBody(cmd); err != nil && !isNonFatal(err) {
				return err
			}
		}
		return nil
	}
	if !i.live {
		return nil
	}
	if every < 0.001 {
		every = 0.001
	}
	i.nextTmr++
	t := &timerRec{id: i.nextTmr, world: world, cmd: cmd, left: count, every: durSeconds(every)}
	i.timers[t.id] = t
	i.startTimer(t)
	return nil
}

func cmdQuote(i *Interp, args string) error {
	o, rest, err := parseOpts(args, "dw", "S0")
	if err != nil {
		return err
	}
	for strings.HasPrefix(rest, "-") && len(rest) > 1 && (isDigit(rest[1]) || rest[1] == '.') { // pacing delay: accepted, ignored
		_, rest = firstWord(rest)
	}
	mode := "send"
	if d, ok := o['d']; ok {
		mode = d
	}
	world := i.curWorld()
	if w, ok := o['w']; ok {
		world = w
	}
	pos := -1
	for k := 0; k < len(rest); k++ {
		if strings.IndexByte("'`!#", rest[k]) >= 0 && (k == 0 || rest[k-1] == ' ' || rest[k-1] == '\t') {
			pos = k
			break
		}
	}
	if pos < 0 {
		return fmt.Errorf("usage: /quote [prefix] 'file | #command")
	}
	prefix, src := rest[:pos], rest[pos+1:]
	var lines []string
	switch rest[pos] {
	case '\'':
		if err := i.needFile("/quote"); err != nil {
			return err
		}
		data, err := i.host.ReadFile(strings.TrimSpace(src))
		if err != nil {
			return fmt.Errorf("/quote: %w", err)
		}
		lines = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	case '#':
		out, err := i.captureLines(src)
		if err != nil {
			return err
		}
		lines = out
	default:
		return fmt.Errorf("/quote: shell commands are not available")
	}
	for _, l := range lines {
		if err := i.tick(); err != nil {
			return err
		}
		l = strings.TrimRight(l, "\r")
		if l == "" {
			continue
		}
		text := prefix + l
		var err error
		switch mode {
		case "echo":
			if i.live {
				i.echoText(world, text)
			}
		case "exec":
			err = i.execBody(text)
		default:
			if strings.HasPrefix(strings.TrimLeft(text, " "), "/") {
				err = i.execBody(text)
			} else {
				err = i.sendText(world, text)
			}
		}
		if err != nil && !isNonFatal(err) {
			return err
		}
	}
	return nil
}

// captureLines runs a command and returns its echoed lines.
func (i *Interp) captureLines(cmd string) ([]string, error) {
	var buf strings.Builder
	if cmd = strings.TrimSpace(cmd); !strings.HasPrefix(cmd, "/") {
		cmd = "/" + cmd
	}
	i.capture = append(i.capture, &buf)
	err := i.execBody(cmd)
	i.capture = i.capture[:len(i.capture)-1]
	if err != nil && !isNonFatal(err) {
		return nil, err
	}
	s := strings.TrimRight(buf.String(), "\n")
	if s == "" {
		return nil, nil
	}
	return strings.Split(s, "\n"), nil
}
