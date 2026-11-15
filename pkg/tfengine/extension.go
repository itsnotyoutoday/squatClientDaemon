package tfengine

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/itsnotyoutoday/gtfparser"
)

// clientExtension is everything this client adds to the engine, as one table
// (gtfparser.Extension). Anything the engine implements itself is NOT here:
// the world book, history (/recall), /limit, /log, /status_*, /connect and its
// family, /localecho, /beep, /dokey, /input, /save, ... are the engine's, fed
// and displayed by corehost.go. What is left is client-specific or a library
// command real tf defines in lib/tf rather than in C.
func clientExtension(a *Engine) gtfparser.Extension {
	in := a.in
	defCmd, _ := in.Command("def")
	logCmd, _ := in.Command("log")
	return gtfparser.Extension{
		Name: "squatclient",
		Commands: []gtfparser.CommandSpec{
			{Name: "def", Category: "macros", Usage: "[options] [name] [= body]",
				Help: "the engine's /def, plus -g (gag, an old SquatClient shortcut for -ag)",
				Fn:   a.defWithGag(defCmd)},
			{Name: "log", Category: "files", Usage: "[-w world] [-gli] [file|OFF]",
				Help: "the engine's /log; a relative file name goes under the logs directory",
				Fn:   a.logInDir(logCmd)},
			{Name: "world", Category: "worlds", Usage: "<name>", MinArgs: 1,
				Help: "connect to a world if needed and bring it to the front (SquatClient extension)",
				Do: func(c *gtfparser.Call) gtfparser.Value {
					w := c.Arg(0)
					if !a.connected(w) {
						if !c.ExecFull("/connect " + w).Truth() {
							return gtfparser.Int(0)
						}
					}
					return c.ExecFull("/fg " + w)
				}},
			{Name: "addworld", Category: "worlds", Usage: "[-Ttype] [-pxe] [-s srchost] name [character password] host port [mfile]", Options: "T:pxes:", MinArgs: 3, MaxArgs: 6,
				Help: "define a world (in tf this is a library macro over addworld())",
				Do: func(c *gtfparser.Call) gtfparser.Value {
					args := c.Args
					var name, char, pass, host, port, mfile string
					switch len(args) {
					case 3:
						name, host, port = args[0], args[1], args[2]
					case 4:
						name, host, port, mfile = args[0], args[1], args[2], args[3]
					case 5:
						name, char, pass, host, port = args[0], args[1], args[2], args[3], args[4]
					default:
						name, char, pass, host, port, mfile = args[0], args[1], args[2], args[3], args[4], args[5]
					}
					flags := ""
					for _, f := range "pxe" {
						if c.Has(byte(f)) {
							flags += string(f)
						}
					}
					q := func(s string) string { return "'" + strings.NewReplacer(`\`, `\\`, "'", `\'`).Replace(s) + "'" }
					v, err := c.Eval("addworld(" + strings.Join([]string{q(name), q(c.Opt('T')), q(host), q(port), q(char), q(pass), q(mfile), q(flags), q(c.Opt('s'))}, ",") + ")")
					if err != nil {
						return c.Fail("%v", err)
					}
					return v
				}},
			{Name: "notify", Category: "display", Usage: "title [| body]",
				Help: "SquatClient extension: a desktop notification",
				Do: func(c *gtfparser.Call) gtfparser.Value {
					t, b, _ := strings.Cut(c.Raw, "|")
					a.host.Notify(strings.TrimSpace(t), strings.TrimSpace(b))
					return gtfparser.Int(1)
				}},
			{Name: "loadworld", Category: "worlds", Usage: "[file]",
				Help: "load a world file (tf's library defines it as /load of WORLDFILE)",
				Do: func(c *gtfparser.Call) gtfparser.Value {
					f := c.Arg(0)
					if f == "" {
						f = c.Var("WORLDFILE")
					}
					if f == "" {
						return c.Fail("no file given and WORLDFILE is not set")
					}
					return c.ExecFull("/load -q " + f)
				}},
			// library commands real tf defines in lib/tf/stdlib.tf; present so they work
			// without the library, and shadowed by it when it is loaded
			{Name: "untrig", Category: "macros", Usage: "pattern", MinArgs: 1, Help: "remove the trigger with exactly that pattern",
				Do: a.removeMacros("untrig", "trigger", func(m *gtfparser.Macro, c *gtfparser.Call) bool { return m.Trigger == strings.TrimSpace(c.Raw) })},
			{Name: "undeft", Category: "macros", Usage: "pattern", MinArgs: 1, Help: "remove the macro triggered by that pattern",
				Do: a.removeMacros("undeft", "trigger", func(m *gtfparser.Macro, c *gtfparser.Call) bool { return m.Trigger == strings.TrimSpace(c.Raw) })},
			{Name: "unhook", Category: "macros", Usage: "event [pattern]", MinArgs: 1, Help: "remove the hook macro on that event",
				Do: a.removeMacros("unhook", "hook", func(m *gtfparser.Macro, c *gtfparser.Call) bool {
					ev, pat := cut(c.Raw)
					e, p := cut(m.Hook)
					return strings.EqualFold(e, ev) && (pat == "" || p == pat)
				})},
			{Name: "nogag", Category: "macros", Usage: "[pattern]", Help: "remove gags; no argument turns gagging off",
				Do: func(c *gtfparser.Call) gtfparser.Value {
					if strings.TrimSpace(c.Raw) == "" {
						c.SetVar("gag", "off")
						return gtfparser.Int(1)
					}
					return a.removeMacros("nogag", "gag", func(m *gtfparser.Macro, c *gtfparser.Call) bool {
						return m.Gag && m.Trigger == strings.TrimSpace(c.Raw)
					})(c)
				}},
			{Name: "nohilite", Category: "macros", Usage: "[pattern]", Help: "remove hilites; no argument turns hiliting off",
				Do: func(c *gtfparser.Call) gtfparser.Value {
					if strings.TrimSpace(c.Raw) == "" {
						c.SetVar("hilite", "off")
						return gtfparser.Int(1)
					}
					return a.removeMacros("nohilite", "hilite", func(m *gtfparser.Macro, c *gtfparser.Call) bool {
						return m.Attrs != "" && m.Trigger == strings.TrimSpace(c.Raw)
					})(c)
				}},
			{Name: "partial", Category: "macros", Usage: "regexp", MinArgs: 1, Help: "highlight just the matching part of lines (tf's library: /def -F -p%{hpri-0} -Ph -t...)",
				Do: func(c *gtfparser.Call) gtfparser.Value {
					pat := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(strings.TrimSpace(c.Raw))
					hpri := c.Var("hpri")
					if hpri == "" {
						hpri = "0"
					}
					return c.Exec("/def -F -p" + hpri + ` -Ph -t"` + pat + `"`)
				}},
			{Name: "trig", Category: "macros", Usage: "pattern = body", Help: "define a trigger (tf's library: /trigpc 0 100)",
				Do: func(c *gtfparser.Call) gtfparser.Value { return c.ExecFull("/trigpc 0 100 " + c.Raw) }},
			{Name: "trigc", Category: "macros", Usage: "chance pattern = body", MinArgs: 1, Help: "define a trigger with a probability",
				Do: func(c *gtfparser.Call) gtfparser.Value { return c.ExecFull("/trigpc 0 " + c.Raw) }},
			{Name: "trigp", Category: "macros", Usage: "priority pattern = body", MinArgs: 1, Help: "define a trigger with a priority",
				Do: func(c *gtfparser.Call) gtfparser.Value {
					p, rest := cut(c.Raw)
					return c.ExecFull("/trigpc " + p + " 100 " + rest)
				}},
			// tf's wrapping and visual-mode commands are library macros over variables; the GUI
			// reads those variables (see Host.Var), so these only set them
			{Name: "nowrap", Category: "display", Help: "turn wrapping off",
				Do: func(c *gtfparser.Call) gtfparser.Value { c.SetVar("wrap", "off"); return gtfparser.Int(1) }},
			{Name: "wrap", Category: "display", Usage: "[width|on|off]", Help: "turn wrapping on or off, or set the wrap width",
				Do: func(c *gtfparser.Call) gtfparser.Value {
					switch v := strings.ToLower(c.Arg(0)); {
					case v == "":
						c.SetVar("wrap", "on")
					case v == "on" || v == "off" || v == "1" || v == "0":
						c.SetVar("wrap", v)
					default:
						c.SetVar("wrapsize", v)
						c.SetVar("wrap", "on")
					}
					return gtfparser.Int(1)
				}},
			{Name: "wrapspace", Category: "display", Usage: "n", MinArgs: 1, Help: "indent of wrapped continuation lines",
				Do: func(c *gtfparser.Call) gtfparser.Value { c.SetVar("wrapspace", c.Arg(0)); return gtfparser.Int(1) }},
			{Name: "visual", Category: "display", Usage: "[on|off]", Help: "visual mode: the GUI is always split into output, status and input, so this only records the setting",
				Do: func(c *gtfparser.Call) gtfparser.Value {
					v := c.Arg(0)
					if v == "" {
						v = "on"
					}
					c.SetVar("visual", v)
					return gtfparser.Int(1)
				}},
		},
		Functions: []gtfparser.FuncSpec{
			gtfparser.Func("isatty", func() int { return 1 }),
			gtfparser.Func("nread", func() int { return 0 }),
			gtfparser.Func("nmail", func() int { return 0 }),
		},
	}
}

func (a *Engine) connected(world string) bool {
	for _, s := range a.host.Sockets() {
		if strings.EqualFold(s.World, world) && s.State != SockDead {
			return true
		}
	}
	return false
}

func cut(s string) (string, string) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

// removeMacros builds a handler that removes the macros pred selects.
func (a *Engine) removeMacros(who, what string, pred func(*gtfparser.Macro, *gtfparser.Call) bool) func(*gtfparser.Call) gtfparser.Value {
	return func(c *gtfparser.Call) gtfparser.Value {
		n := 0
		for _, m := range c.Macros() {
			if pred(m, c) && c.UndefID(m.ID) {
				n++
			}
		}
		if n == 0 {
			return c.Fail("no such %s %q", what, strings.TrimSpace(c.Raw))
		}
		return gtfparser.Int(1)
	}
}

// defWithGag accepts the old "/def -g" (gag) shortcut next to the engine's /def: it becomes the gag
// attribute, added to an -a option when there is one (a second -a would replace -ag).
func (a *Engine) defWithGag(def gtfparser.CmdFunc) gtfparser.CmdFunc {
	return func(in *gtfparser.Interp, args string) gtfparser.Value {
		opts, rest := splitOptions(args)
		gag, hasAttr := false, false
		for i, o := range opts {
			switch {
			case o == "-g":
				gag = true
			case strings.HasPrefix(o, "-a") && len(o) > 2:
				hasAttr = true
				_ = i
			}
		}
		if !gag {
			return def(in, args)
		}
		var out []string
		for _, o := range opts {
			switch {
			case o == "-g":
				if !hasAttr {
					out = append(out, "-ag")
				}
			case strings.HasPrefix(o, "-a") && len(o) > 2:
				if q := o[len(o)-1]; len(o) > 3 && (q == '\'' || q == '"') {
					o = o[:len(o)-1] + ",g" + string(q)
				} else {
					o += ",g"
				}
				out = append(out, o)
			default:
				out = append(out, o)
			}
		}
		return def(in, strings.Join(append(out, rest), " "))
	}
}

// splitOptions cuts the leading -option words off a command line; quoted parts (-t'a b') stay in one word.
func splitOptions(s string) (opts []string, rest string) {
	rest = strings.TrimLeft(s, " \t")
	for strings.HasPrefix(rest, "-") && rest != "-" && !strings.HasPrefix(rest, "- ") && !strings.HasPrefix(rest, "--") {
		i, q := 0, byte(0)
		for ; i < len(rest); i++ {
			c := rest[i]
			if q != 0 {
				if c == '\\' && i+1 < len(rest) {
					i++
				} else if c == q {
					q = 0
				}
				continue
			}
			if c == '\'' || c == '"' {
				q = c
			} else if c == ' ' || c == '\t' {
				break
			}
		}
		opts = append(opts, rest[:i])
		rest = strings.TrimLeft(rest[i:], " \t")
	}
	return opts, rest
}

// logInDir resolves a relative /log file name under the logs directory.
func (a *Engine) logInDir(log gtfparser.CmdFunc) gtfparser.CmdFunc {
	return func(in *gtfparser.Interp, args string) gtfparser.Value {
		f := strings.Fields(args)
		if len(f) > 0 && a.opts.LogsDir != "" {
			last := f[len(f)-1]
			if !strings.HasPrefix(last, "-") && !strings.EqualFold(last, "off") && !filepath.IsAbs(last) && !strings.HasPrefix(last, "~") {
				args = args[:strings.LastIndex(args, last)] + filepath.Join(a.opts.LogsDir, filepath.Base(last))
			}
		}
		return log(in, args)
	}
}

var _ = fmt.Sprintf
