package tfengine

import (
	"fmt"
	"strings"

	"github.com/itsnotyoutoday/gtfparser"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// coreHost implements the engine's host interfaces. Every method runs on the
// interpreter's goroutine, inside a call that set a.cur.
type coreHost struct{ a *Engine }

var (
	_ gtfparser.Host      = (*coreHost)(nil)
	_ gtfparser.EchoHost  = (*coreHost)(nil)
	_ gtfparser.WorldHost = (*coreHost)(nil)
	_ gtfparser.Notifier  = (*coreHost)(nil)
	_ gtfparser.TermHost  = (*coreHost)(nil)
	_ gtfparser.HelpHost  = (*coreHost)(nil)
	_ gtfparser.KeyHost   = (*coreHost)(nil)
)

// Echo shows plain text in the current world's window.
func (h *coreHost) Echo(text string) { h.a.echo(h.a.cur, plainSpans(text)) }

func plainSpans(text string) []proto.Span { return []proto.Span{{S: text}} }

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
	h.a.echo(world, spans)
}

func (a *Engine) echo(world string, spans []proto.Span) { a.host.Echo(world, spans) }

// echoText shows a plain line in the current world.
func (a *Engine) echoText(s string) { a.echo(a.cur, []proto.Span{{S: s}}) }

// warn is the interpreter's diagnostics channel: "% message".
func (a *Engine) warn(s string) {
	if strings.HasSuffix(s, ": no such command") {
		s += ". Type /help for help."
	}
	a.echoText("% " + s)
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
		prev := a.in.World()
		a.in.SetWorld(world)
		a.in.Hook("SEND", text)
		a.in.SetWorld(prev)
		a.inSend = false
	}
	if err := a.host.SendToWorld(world, text); err != nil {
		a.warn(err.Error())
		return false
	}
	return true
}

// ---- WorldHost ----

func (h *coreHost) Connect(world string) error    { return h.a.host.Connect(world) }
func (h *coreHost) Disconnect(world string) error { return h.a.host.Disconnect(world) }
func (h *coreHost) Foreground(world string) error { return h.a.host.Foreground(world) }
func (h *coreHost) ForegroundWorld() string       { return h.a.host.ForegroundWorld() }
func (h *coreHost) Connected(world string) bool   { return h.a.host.IsConnected(world) }

// ---- Notifier: engine events a client may want to react to ----

func (h *coreHost) Notify(ev gtfparser.Event) {
	a := h.a
	arg := ""
	if len(ev.Args) > 0 {
		arg = ev.Args[0]
	}
	switch ev.Kind {
	case "beep":
		a.host.Bell()
	case "localecho":
		a.host.LocalEcho(a.cur, flagOn(arg, false))
	case "restrict":
		a.echoText("% restriction level is now " + arg)
	case "limit", "status", "log", "suspend":
		// the matching commands are handled by this package; the event is
		// informational
	default:
		a.echoText(fmt.Sprintf("%% (%s: not available in this client)", ev.Kind))
	}
}

// Size reports the display size for columns() and lines().
func (h *coreHost) Size() (int, int) { return h.a.host.Screen() }

// Help is /help: hosts that have a help browser implement HelpProvider.
func (h *coreHost) Help(topic string) { h.a.help(topic) }

// KeyCode answers keycode(): a GUI has no termcap, so only the names scripts
// use as bind targets are answered.
func (h *coreHost) KeyCode(name string) string {
	if k, ok := keyCodes[strings.ToUpper(name)]; ok {
		return k
	}
	return ""
}

var keyCodes = map[string]string{
	"UP": "^[[A", "DOWN": "^[[B", "RIGHT": "^[[C", "LEFT": "^[[D",
	"HOME": "^[[H", "END": "^[[F", "PGUP": "^[[5~", "PGDN": "^[[6~",
}

// HelpProvider is optionally implemented by a Host that has a help browser;
// without it /help lists the commands.
type HelpProvider interface{ Help(world, topic string) }
