package tf

import (
	"fmt"
	"math"
	"sort"
	"strconv"
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
	i.initLibCmds()
	i.initInputCmds()
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

// parseInterval reads a TF time: seconds, hours:minutes, or
// hours:minutes:seconds (seconds may be fractional).
func parseInterval(s string) (float64, error) {
	parts := strings.Split(s, ":")
	if len(parts) > 3 || s == "" {
		return 0, fmt.Errorf("bad time %q", s)
	}
	mult := []float64{1}
	switch len(parts) {
	case 2:
		mult = []float64{3600, 60}
	case 3:
		mult = []float64{3600, 60, 1}
	}
	var total float64
	for k, p := range parts {
		v := S(p)
		if !v.num || v.Float() < 0 {
			return 0, fmt.Errorf("bad time %q", s)
		}
		total += v.Float() * mult[k]
	}
	return total, nil
}

// fmtDTime renders seconds the way /ps shows times: h:mm:ss from a minute up,
// plain (fractional) seconds below.
func fmtDTime(sec float64) string {
	if sec < 60 {
		return strconv.FormatFloat(math.Round(sec*1e6)/1e6, 'f', -1, 64)
	}
	t := int64(sec)
	return fmt.Sprintf("%d:%02d:%02d", t/3600, t/60%60, t%60)
}

func (i *Interp) initMiscCmds() {
	i.reg("repeat", "/repeat [-w[world]] [-n] [-<time>|-S] count command   (count may be i for endless; returns the process id)", true, cmdRepeat)
	i.reg("ps", "/ps [-srq] [-w[world]] [pid]   (list /repeat processes)", false, cmdPs)
	i.reg("kill", "/kill pid...   (stop /repeat processes)", false, func(i *Interp, a string) error {
		var first error
		for _, w := range words(a) {
			v := S(w)
			t := i.timers[int(v.Int())]
			if t == nil {
				if first == nil {
					first = fmt.Errorf("/kill: no process %s", w)
				}
				continue
			}
			t.cancel()
			delete(i.timers, t.id)
		}
		return first
	})
	i.reg("quote", "/quote [-d<send|echo|exec>] [-w<world>] prefix 'file | #command   (shell forms are unavailable)", false, cmdQuote)
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
	for _, n := range []string{"edit", "recordline", "watchdog", "watchname", "liststreams", "sh", "core", "lcd"} {
		n := n
		i.reg(n, "/"+n+" (not available)", false, func(*Interp, string) error {
			return fmt.Errorf("/%s: not implemented in this client (see docs/TF-COMPAT.md)", n)
		})
	}
}

func cmdRepeat(i *Interp, args string) error {
	world := i.curWorld()
	var every float64
	timed, sync, first := false, false, false
	rest := strings.TrimLeft(args, " \t")
	for strings.HasPrefix(rest, "-") && len(rest) > 1 && rest[1] != ' ' {
		tok, r := firstWord(rest)
		rest = r
		switch {
		case tok == "-S":
			sync = true
		case tok == "-n":
			first = true
		case tok == "-P":
			return fmt.Errorf("/repeat: -P (repeat on prompt) is not supported")
		case strings.HasPrefix(tok, "-w"):
			if w := tok[2:]; w != "" {
				world = w
			}
		case isDigit(tok[1]) || tok[1] == '.':
			var err error
			if every, err = parseInterval(tok[1:]); err != nil {
				return fmt.Errorf("/repeat: %w", err)
			}
			timed = true
		default:
			return fmt.Errorf("/repeat: invalid option %s", tok)
		}
	}
	cs, cmd := firstWord(rest)
	infinite := cs == "i"
	cv := S(cs)
	if (!infinite && (!cv.num || cv.Int() < 1)) || cmd == "" {
		return fmt.Errorf("/repeat: invalid repeat count (%s)", cs)
	}
	count := int(cv.Int())
	if infinite {
		count = -1
	}
	if sync {
		i.nextTmr++ // a synchronous repeat still uses up a process id
		if count > 10000 || infinite {
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
	if !timed {
		every = S(i.getVarStr("ptime")).Float()
		if every <= 0 {
			every = 1
		}
	}
	if every < 0.001 {
		every = 0.001
	}
	i.nextTmr++
	t := &timerRec{id: i.nextTmr, world: world, cmd: cmd, left: count, every: durSeconds(every), explicit: timed}
	i.timers[t.id] = t
	if first {
		i.startTimerAfter(t, 0)
	} else {
		i.startTimer(t)
	}
	i.last = I(int64(t.id))
	return nil
}

func cmdPs(i *Interp, args string) error {
	o, rest, err := parseOpts(args, "", "sr")
	if err != nil {
		return err
	}
	_, short := o['s']
	pid := -1
	if f := strings.TrimSpace(rest); f != "" {
		pid = int(S(f).Int())
	}
	var ids []int
	for id := range i.timers {
		if pid < 0 || id == pid {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	if short {
		for _, id := range ids {
			i.echoText(i.curWorld(), strconv.Itoa(id))
		}
		return nil
	}
	i.echoText(i.curWorld(), "  PID     NEXT T D WORLD       PTIME COUNT COMMAND")
	now := i.host.Now()
	for _, id := range ids {
		t := i.timers[id]
		next := t.next.Sub(now).Seconds()
		if next < 0 {
			next = 0
		}
		ptime, w := "", ""
		if t.explicit {
			ptime = fmtDTime(t.every.Seconds())
		}
		if t.world != i.curWorld() {
			w = t.world
		}
		count := "i"
		if t.left >= 0 {
			count = strconv.Itoa(t.left)
		}
		i.echoText(i.curWorld(), fmt.Sprintf("%5d %8s %c %c %-9s %7s %5s %s", id, fmtDTime(next), 'r', ' ', w, ptime, count, t.cmd))
	}
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
