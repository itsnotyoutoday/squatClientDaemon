package engine

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// The console is the client's own screen, shown before any world is open and always available as a tab: the start-up banner, the library and
// startup.tf loading lines and their errors, whatever scripts print with no world, and a command line that runs TinyFugue commands without a
// world (as unconnected TinyFugue does). It is not a session: it has no connection, no scrollback file and nothing to reconnect.

// consoleMax is how many lines the console keeps.
const consoleMax = 2000

type consoleLog struct {
	mu    sync.Mutex
	lines []proto.Line
	seq   uint64
}

// consoleAdd appends one line (a system line, or the echo of what was typed) and tells the subscribers.
func (e *Engine) consoleAdd(kind string, spans []proto.Span) {
	c := &e.console
	c.mu.Lock()
	c.seq++
	l := proto.Line{V: proto.Version, T: proto.TLine, Session: "", Seq: c.seq, Ts: time.Now().UnixMilli(), Kind: kind, Spans: spans}
	c.lines = append(c.lines, l)
	if over := len(c.lines) - consoleMax; over > 0 {
		c.lines = append([]proto.Line(nil), c.lines[over:]...)
	}
	c.mu.Unlock()
	e.bus.publish(Event{Kind: EvConsole, Line: &l})
}

func (e *Engine) consoleSystem(text string) {
	e.consoleAdd(proto.KindSystem, []proto.Span{{S: text}})
}

// ConsoleLines returns the console's lines with a sequence number above after (0 = everything kept).
func (e *Engine) ConsoleLines(after uint64) []proto.Line {
	c := &e.console
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []proto.Line
	for _, l := range c.lines {
		if l.Seq > after {
			out = append(out, l)
		}
	}
	return out
}

// inConsole runs fn with everything scripts print for "no world" routed to the console, even when a session is in front.
func (e *Engine) inConsole(fn func()) {
	e.consoleOut.Add(1)
	defer e.consoleOut.Add(-1)
	fn()
}

// ConsoleInput runs one line typed into the console: a TinyFugue command (a line starting with "/") runs with no current world; anything else is
// text for a game and there is none.
func (e *Engine) ConsoleInput(text string) error {
	text = strings.TrimRight(text, "\r\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	e.consoleAdd(proto.KindEcho, []proto.Span{{S: text}})
	e.inConsole(func() {
		if strings.HasPrefix(text, "/") {
			e.tf.Exec("", text)
			return
		}
		e.consoleSystem(fmt.Sprintf("%% No world: connect with /connect <world> or the Worlds button; %q was not sent.", text))
	})
	return nil
}
