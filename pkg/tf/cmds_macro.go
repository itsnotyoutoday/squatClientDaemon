package tf

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func (i *Interp) initMacroCmds() {
	i.reg("def", "/def [-t<pat>] [-m<glob|simple|regexp>] [-h<HOOK [pat]>] [-p<pri>] [-n<shots>] [-1] [-F] [-g] [-a<attrs>] [-P<n><attrs>] [-b<key>] [-w<world>] [-E<expr>] [-c<pct>] [-i] [name] [= body]", true, cmdDef)
	i.reg("undef", "/undef name...", false, func(i *Interp, a string) error {
		var first error
		for _, n := range words(a) {
			m := i.macros.anyByName(strings.TrimPrefix(n, "/"))
			if m == nil {
				if first == nil {
					first = fmt.Errorf("/undef: no macro named %q", n)
				}
				continue
			}
			i.macros.remove(m)
		}
		return first
	})
	i.reg("undefn", "/undefn number...   (remove macros by number as shown by /list)", false, func(i *Interp, a string) error {
		for _, w := range words(a) {
			n, err := strconv.Atoi(w)
			if err != nil {
				return fmt.Errorf("/undefn: bad number %q", w)
			}
			for _, m := range i.macros.list {
				if m.Num == n {
					i.macros.remove(m)
					break
				}
			}
		}
		return nil
	})
	i.reg("list", "/list [-s] [glob]   (list macros whose name matches glob)", false, func(i *Interp, a string) error {
		o, rest, err := parseOpts(a, "", "s")
		if err != nil {
			return err
		}
		_, short := o['s']
		for _, m := range i.macros.list {
			if rest != "" && !globMatch(rest, m.Name) {
				continue
			}
			if short {
				i.echoText(i.curWorld(), m.Name)
			} else {
				i.echoText(i.curWorld(), fmt.Sprintf("%d: %s", m.Num, m.Def()))
			}
		}
		return nil
	})
	i.reg("purge", "/purge [glob]   (remove all macros, or those whose name matches)", false, func(i *Interp, a string) error {
		a = strings.TrimSpace(a)
		for _, m := range append([]*Macro(nil), i.macros.list...) {
			if m.Invisible && a == "" {
				continue
			}
			if a == "" || globMatch(a, m.Name) {
				i.macros.remove(m)
			}
		}
		return nil
	})
	i.reg("enable", "/enable name...   (extension)", false, func(i *Interp, a string) error { return i.setDisabled(a, false) })
	i.reg("disable", "/disable name...   (extension)", false, func(i *Interp, a string) error { return i.setDisabled(a, true) })
	i.reg("gag", "/gag pattern   (define a trigger that hides matching lines)", false, func(i *Interp, a string) error {
		return i.defShortcut("/gag", a, "g")
	})
	i.reg("hilite", "/hilite pattern   (define a trigger that highlights matching lines using $hiliteattr)", false, func(i *Interp, a string) error {
		attr := i.getVarStr("hiliteattr")
		if attr == "" {
			attr = "h"
		}
		return i.defShortcut("/hilite", a, "a"+quoteOpt(attr))
	})
	i.reg("bind", "/bind key = command", false, func(i *Interp, a string) error {
		k, body, ok := strings.Cut(a, "=")
		k = strings.TrimSpace(k)
		if !ok {
			k, body = firstWord(a)
		}
		if k == "" {
			return fmt.Errorf("usage: /bind key = command")
		}
		for _, m := range append([]*Macro(nil), i.macros.list...) {
			if m.Key == k && m.World == "" {
				i.macros.remove(m)
			}
		}
		m := &Macro{Key: k, Body: strings.TrimSpace(body), Mode: ModeGlob}
		i.macros.add(m)
		return nil
	})
	i.reg("unbind", "/unbind key", false, func(i *Interp, a string) error {
		k := strings.TrimSpace(a)
		n := 0
		for _, m := range append([]*Macro(nil), i.macros.list...) {
			if m.Key == k {
				i.macros.remove(m)
				n++
			}
		}
		if n == 0 {
			return fmt.Errorf("/unbind: %q is not bound", k)
		}
		return nil
	})
	i.reg("dokey", "/dokey name", false, func(i *Interp, a string) error {
		if i.live {
			i.host.DoKey(i.curWorld(), strings.TrimSpace(a))
		}
		return nil
	})
	i.reg("load", "/load [-q] file", false, func(i *Interp, a string) error { return i.cmdLoad(a, false) })
	i.reg("require", "/require [-q] file   (load once)", false, func(i *Interp, a string) error { return i.cmdLoad(a, true) })
	i.reg("save", "/save file [glob]   (write macro definitions)", false, func(i *Interp, a string) error {
		if err := i.needFile("/save"); err != nil {
			return err
		}
		f, glob := firstWord(a)
		if f == "" {
			return fmt.Errorf("usage: /save file [glob]")
		}
		var sb strings.Builder
		sb.WriteString("; saved by /save\n")
		for _, m := range i.macros.list {
			if m.Invisible || (glob != "" && !globMatch(glob, m.Name)) {
				continue
			}
			sb.WriteString(m.Def() + "\n")
		}
		return i.host.WriteFile(f, []byte(sb.String()), false)
	})
	i.reg("recall", "/recall [-w<world>] args   (handled by the host's scrollback)", false, func(i *Interp, a string) error {
		o, rest, err := parseOpts(a, "w", "")
		if err != nil {
			return err
		}
		w := i.curWorld()
		if v, ok := o['w']; ok {
			w = v
		}
		i.host.Recall(w, rest)
		return nil
	})
}

func (i *Interp) setDisabled(a string, dis bool) error {
	for _, n := range words(a) {
		m := i.macros.anyByName(n)
		if m == nil {
			return fmt.Errorf("no macro named %q", n)
		}
		m.Disabled = dis
	}
	return nil
}

func (i *Interp) defShortcut(cmd, pat, opt string) error {
	pat = strings.TrimSpace(pat)
	if pat == "" {
		return fmt.Errorf("usage: %s pattern", cmd)
	}
	m, err := i.parseDef("-" + opt + " -t" + quoteOpt(pat))
	if err != nil {
		return err
	}
	i.macros.add(m)
	return nil
}

func cmdDef(i *Interp, args string) error {
	m, err := i.parseDef(args)
	if err != nil {
		return err
	}
	if i.macros.add(m) {
		i.fireHook(i.curWorld(), "REDEF", m.Name)
	}
	return nil
}

// ---- loading ----------------------------------------------------------------

func (i *Interp) cmdLoad(a string, once bool) error {
	if err := i.needFile("/load"); err != nil {
		return err
	}
	o, f, err := parseOpts(a, "", "q")
	if err != nil {
		return err
	}
	_ = o
	f = strings.TrimSpace(f)
	if f == "" {
		return fmt.Errorf("usage: /load file")
	}
	if once && i.loaded[f] {
		return nil
	}
	data, err := i.host.ReadFile(f)
	if err != nil {
		i.fireHook(i.curWorld(), "LOADFAIL", f)
		return fmt.Errorf("/load: %w", err)
	}
	if i.loadDep >= 16 {
		return fmt.Errorf("/load: files nested too deeply")
	}
	i.loaded[f] = true
	i.loadDep++
	err = i.loadSource(f, string(data))
	i.loadDep--
	i.fireHook(i.curWorld(), "LOAD", f)
	return err
}

// LoadFile loads a script through Host.ReadFile (not gated by /restrict:
// the application chose the path).
func (i *Interp) LoadFile(path string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	data, err := i.host.ReadFile(path)
	if err != nil {
		return fmt.Errorf("tf: load %s: %w", path, err)
	}
	var lerr error
	_ = i.run("", true, func() error {
		lerr = i.loadSource(path, string(data))
		i.fireHook("", "LOAD", path)
		return nil
	})
	return lerr
}

// LoadString runs script source as if loaded from a file called name.
// Errors are reported through Host.Echo as name:line: message; the first is
// returned and loading continues with later lines.
func (i *Interp) LoadString(name, src string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	var lerr error
	_ = i.run("", true, func() error {
		lerr = i.loadSource(name, src)
		return nil
	})
	return lerr
}

type logicalLine struct {
	n    int
	text string
}

// logicalLines joins lines ending in a backslash with their successor
// (dropping the successor's leading whitespace) and returns the result.
func logicalLines(src string) []logicalLine {
	var out []logicalLine
	raw := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	for k := 0; k < len(raw); k++ {
		start := k + 1
		line := raw[k]
		for strings.HasSuffix(line, "\\") && !strings.HasSuffix(line, "\\\\") && k+1 < len(raw) {
			k++
			line = line[:len(line)-1] + strings.TrimLeft(raw[k], " \t")
		}
		out = append(out, logicalLine{start, line})
	}
	return out
}

func (i *Interp) resetBudget() {
	i.steps = 0
	if i.opts.MaxTime > 0 {
		i.deadline = i.host.Now().Add(i.opts.MaxTime)
	}
}

func (i *Interp) loadSource(name, src string) error {
	var first error
	fail := func(line int, err error) {
		var r reported
		if !errors.As(err, &r) {
			i.echoText(i.curWorld(), fmt.Sprintf("%% %s:%d: %v", name, line, err))
		}
		if first == nil {
			first = fmt.Errorf("%s:%d: %w", name, line, err)
		}
	}
	var pending []string
	pendStart := 0
	for _, ll := range logicalLines(src) {
		t := strings.TrimLeft(ll.text, " \t")
		if t == "" || t[0] == ';' {
			continue
		}
		if len(pending) == 0 {
			if t[0] != '/' {
				fail(ll.n, fmt.Errorf("not a command (script lines must start with /): %s", clip(t)))
				continue
			}
			pendStart = ll.n
		}
		pending = append(pending, splitStatements(t)...)
		nodes, err := parseNodes(pending, false)
		if errors.Is(err, errIncomplete) {
			continue
		}
		if err != nil {
			fail(pendStart, err)
			pending = nil
			continue
		}
		pending = nil
		i.resetBudget()
		if err := i.execNodes(nodes); err != nil {
			fail(pendStart, err)
		}
	}
	if len(pending) > 0 {
		fail(pendStart, errIncomplete)
	}
	return first
}
