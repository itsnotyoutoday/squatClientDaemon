package tfengine

import (
	"strconv"
	"strings"
	"time"

	"github.com/itsnotyoutoday/gtfparser"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// coreHost implements the engine's host interfaces on top of Host. Every
// method runs on the interpreter's goroutine, inside a call that set a.cur.
type coreHost struct{ a *Engine }

var (
	_ gtfparser.Host        = (*coreHost)(nil)
	_ gtfparser.EchoHost    = (*coreHost)(nil)
	_ gtfparser.WorldHost   = (*coreHost)(nil)
	_ gtfparser.ConnectHost = (*coreHost)(nil)
	_ gtfparser.FgHost      = (*coreHost)(nil)
	_ gtfparser.SocketHost  = (*coreHost)(nil)
	_ gtfparser.IdleHost    = (*coreHost)(nil)
	_ gtfparser.LimitHost   = (*coreHost)(nil)
	_ gtfparser.LineHost    = (*coreHost)(nil)
	_ gtfparser.PromptHost  = (*coreHost)(nil)
	_ gtfparser.WindowHost  = (*coreHost)(nil)
	_ gtfparser.Notifier    = (*coreHost)(nil)
	_ gtfparser.TermHost    = (*coreHost)(nil)
	_ gtfparser.HelpHost    = (*coreHost)(nil)
	_ gtfparser.KeyHost     = (*coreHost)(nil)
	_ gtfparser.StatusHost  = (*coreHost)(nil)
)

// ---- output ----

// Echo shows plain text in the current world's window (and records it in tf's
// local history, which /recall -l reads).
func (h *coreHost) Echo(text string) { h.a.echoRecord(h.a.cur, []proto.Span{{S: text}}, text) }

// EchoTo is /echo with -w, -a or -p.
func (h *coreHost) EchoTo(o gtfparser.EchoOpts, text string) {
	var base Attr
	if o.Attrs != "" {
		var err error
		if base, err = ParseAttr(o.Attrs); err != nil {
			h.a.warn("/echo: " + err.Error())
			return
		}
	}
	world := h.a.cur
	if o.World != "" {
		world = o.World
	}
	var spans []proto.Span
	if o.Markup {
		spans = SpansFromMarkup(text, base)
	} else {
		spans = []proto.Span{base.Apply(proto.Span{S: text})}
	}
	h.a.echoRecord(world, spans, text)
}

func (a *Engine) echoRecord(world string, spans []proto.Span, plain string) {
	if a.muted {
		return
	}
	a.in.RecordLocalLine(plain)
	a.host.Echo(world, spans)
}

// echoText shows a plain line in the current world.
func (a *Engine) echoText(s string) { a.echoRecord(a.cur, []proto.Span{{S: s}}, s) }

// warn is the interpreter's diagnostics channel: "% message".
func (a *Engine) warn(s string) {
	if strings.HasSuffix(s, ": no such command") {
		s += ". Type /help for help."
	}
	a.echoText("% " + s)
}

// DisplayLine shows a line fake_recv() injected, as if the world had sent it.
func (h *coreHost) DisplayLine(world, text string) {
	if world == "" {
		world = h.a.cur
	}
	h.a.host.DisplayLine(world, text)
}

// Send sends text to a world (the current one for ""), firing the SEND hook
// first. A failure is shown to the user the TF way.
func (h *coreHost) Send(world, text string) bool {
	a := h.a
	if world == "" {
		world = a.cur
	}
	if world == "" {
		world = a.host.ForegroundWorld()
	}
	if !a.inSend {
		a.inSend = true
		prev := a.in.CurrentWorld()
		a.in.SetWorld(world)
		fired := a.in.FireHook("SEND", text)
		a.in.SetWorld(prev)
		a.inSend = false
		for _, m := range fired {
			if m.Gag || attrsGag(m.Attrs) {
				return true // a gagging SEND hook swallows the text, as in real tf (lib/tf/alias.tf)
			}
		}
	}
	if err := a.host.SendToWorld(world, text); err != nil {
		a.warn(err.Error())
		return false
	}
	return true
}

// ---- connections ----

func (h *coreHost) world(name string) WorldInfo {
	if w, ok := h.a.in.World(name); ok {
		return fromCore(w)
	}
	return WorldInfo{Name: name}
}

func (h *coreHost) Connect(world string) error {
	return h.a.host.Connect(h.world(world), ConnectOptions{})
}

func (h *coreHost) ConnectWith(world string, o gtfparser.ConnectOptions) error {
	return h.a.host.Connect(h.world(world), ConnectOptions{NoLogin: o.NoLogin, Quiet: o.Quiet, TLS: o.SSL,
		Foreground: o.Foreground, Background: o.Background})
}

func (h *coreHost) Disconnect(world string) error { return h.a.host.Disconnect(world) }
func (h *coreHost) Foreground(world string) error { return h.a.host.Foreground(world, false) }
func (h *coreHost) ForegroundQuiet(world string, quiet bool) error {
	return h.a.host.Foreground(world, quiet)
}
func (h *coreHost) ForegroundWorld() string     { return h.a.host.ForegroundWorld() }
func (h *coreHost) Connected(world string) bool { return h.a.connected(world) }

func (h *coreHost) Sockets() []gtfparser.SocketInfo {
	var out []gtfparser.SocketInfo
	for _, s := range h.a.host.Sockets() {
		st := gtfparser.SockConnected
		switch s.State {
		case SockConnecting:
			st = gtfparser.SockConnecting
		case SockDead:
			st = gtfparser.SockDead
		}
		out = append(out, gtfparser.SocketInfo{World: s.World, Host: s.Host, Port: s.Port, State: st, SSL: s.TLS,
			NewLines: s.Unseen, Active: s.Unseen > 0, LastRecv: s.LastRecv, LastSend: s.LastSend})
	}
	return out
}

func (h *coreHost) LastKeyboard() (t time.Time) { return h.a.host.LastKeyboard() }

// ---- screen: /limit, prompt, size, window ----

func (h *coreHost) Redraw(world string) int {
	return h.a.host.SetLimit(world, h.a.in.LimitFilter(world))
}
func (h *coreHost) SetPrompt(world, text string) { h.a.host.SetPrompt(world, text) }
func (h *coreHost) Size() (int, int)             { return h.a.host.Screen() }
func (h *coreHost) WinLines() int                { _, l := h.a.host.Screen(); return l }

// ---- events ----

// guiVars are the TF variables the GUI reads: wrapping and layout.
var guiVars = map[string]bool{"wrap": true, "wrapsize": true, "wrapspace": true, "wrappunct": true,
	"visual": true, "more": true}

// Notify receives the engine's events: state a GUI may need to react to.
func (h *coreHost) Notify(ev gtfparser.Event) {
	a := h.a
	arg := func(i int) string {
		if i < len(ev.Args) {
			return ev.Args[i]
		}
		return ""
	}
	switch ev.Kind {
	case "beep":
		a.host.Bell()
	case "localecho":
		w := arg(0)
		if w == "" {
			w = a.cur
		}
		a.host.LocalEcho(w, arg(1) == "on" || arg(1) == "1")
	case "status":
		a.statusDirty = true
	case "var":
		if guiVars[arg(0)] {
			a.host.Var(arg(0), arg(2))
		}
		if strings.HasPrefix(arg(0), "status_") {
			a.statusDirty = true
		}
	case "renameworld":
		a.renames = append(a.renames, [2]string{arg(0), arg(1)})
	case "suspend":
		a.host.Suspend()
	case "restrict":
		a.echoText("% restriction level is now " + arg(0))
	case "dokey":
		// REDRAW, REFRESH, CLEAR, SELFLUSH and LNEXT have nothing to do in a GUI window: the
		// browser keeps the display up to date and the input line has its own literal-next.
	case "core":
		a.echoText("% /core: not available in this client (a GUI has no core dump to make)")
	case "limit", "log", "more", "prompt", "watchdog", "watchname", "reboot":
		// "limit" is answered by Redraw, the logs are written by the engine, paging does not exist
		// in a scrolling window, prompts arrive through PromptHost; the rest is informational.
	}
}

// Help is /help: the host's help browser, or a pointer when it has none.
func (h *coreHost) Help(topic string) {
	if hp, ok := h.a.host.(HelpProvider); ok {
		hp.Help(h.a.cur, strings.TrimPrefix(strings.TrimSpace(topic), "/"))
		return
	}
	h.a.echoText("% /help: see docs/TF-ENGINE.md and docs/COMMAND-STATUS.md")
}

// KeyCode answers keycode(): a GUI has no termcap, so only the names scripts
// use as bind targets are answered.
func (h *coreHost) KeyCode(name string) string {
	if k, ok := keyCodes[strings.ToUpper(name)]; ok {
		return k
	}
	return ""
}

var keyCodes = map[string]string{
	"UP": "\x1b[A", "DOWN": "\x1b[B", "RIGHT": "\x1b[C", "LEFT": "\x1b[D",
	"HOME": "\x1b[H", "END": "\x1b[F", "PGUP": "\x1b[5~", "PGDN": "\x1b[6~",
}

// StatusValue supplies the internal status fields from the client's own state.
func (h *coreHost) StatusValue(name string) (string, bool) {
	switch name {
	case "world":
		return h.a.host.ForegroundWorld(), true
	case "active":
		n := 0
		for _, s := range h.a.host.Sockets() {
			if s.Unseen > 0 {
				n++
			}
		}
		if n == 0 {
			return "", true
		}
		return "(Active: " + strconv.Itoa(n) + ")", true
	case "log":
		if h.a.in.LogCount() > 0 {
			return "(Log)", true
		}
		return "", true
	case "clock":
		return h.a.host.Now().Format("15:04"), true
	case "more", "read", "mail":
		return "", true // a scrolling window does not page; there is no mail or read-ahead
	}
	return "", false
}
