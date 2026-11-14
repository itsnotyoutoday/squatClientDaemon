package conn

import (
	"errors"
	"net"
	"os"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/ansi"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/telnet"
)

// Handing a live connection to another process (squatterd reload, docs/RELOAD.md): Handoff stops the
// reader and returns everything about the connection except the socket, plus a duplicate of the
// socket's file descriptor; Adopt builds a running Conn from them (in this or another process). The
// connection to the game is never closed: the kernel keeps the socket, and bytes that arrive in the
// meantime wait in its buffer.

// ErrNotHandoffable is returned by Handoff for a connection that cannot be handed over: a TLS
// connection (Go cannot export a TLS session's keys) or one that is not plain TCP.
var ErrNotHandoffable = errors.New("conn: this connection cannot be handed over (TLS or not plain TCP)")

// LinerState is the line splitter's carry-over: an incomplete UTF-8 sequence, the partial line (a
// prompt not yet shown) and the colour state.
type LinerState struct {
	Pend     []byte           `json:"pend,omitempty"`
	Buf      []byte           `json:"buf,omitempty"`
	Parser   ansi.ParserState `json:"parser"`
	LastText string           `json:"lastText,omitempty"`
}

// HandoffState is everything about a live connection except the socket.
type HandoffState struct {
	Telnet telnet.MachineState `json:"telnet"`
	Liner  LinerState          `json:"liner"`
	Echo   bool                `json:"echo,omitempty"`
}

func (l *liner) state() LinerState {
	return LinerState{Pend: append([]byte(nil), l.pend[:l.npend]...), Buf: append([]byte(nil), l.buf...),
		Parser: l.parser.State(), LastText: l.lastText}
}

func (l *liner) restore(s LinerState) bool {
	if len(s.Pend) > len(l.pend) {
		return false
	}
	l.npend = copy(l.pend[:], s.Pend)
	l.buf = append([]byte(nil), s.Buf...)
	l.parser.SetState(s.Parser)
	l.lastText = s.LastText
	return true
}

// Handoff stops this connection without ending it. It returns the state and a duplicate of the socket;
// the caller owns the file and must close it (Adopt makes its own duplicate). Afterwards the Events
// channel is closed WITHOUT an Ended event (events already queued can still be read): that is how a
// consumer knows the connection was handed over and not lost. A failure leaves the connection running.
func (c *Conn) Handoff() (HandoffState, *os.File, error) {
	c.ncMu.Lock()
	nc := c.nc
	c.ncMu.Unlock()
	tc, ok := nc.(*net.TCPConn)
	if !ok || c.tls || nc == nil {
		return HandoffState{}, nil, ErrNotHandoffable
	}
	if c.closing.Load() {
		return HandoffState{}, nil, ErrClosed
	}
	select {
	case <-c.done:
		return HandoffState{}, nil, ErrClosed
	default:
	}
	c.handoff.Store(true)
	_ = tc.SetReadDeadline(time.Now()) // wake a blocked Read
	select {
	case <-c.exited:
	case <-time.After(5 * time.Second):
		// the reader did not stop: undo and carry on as before
		c.handoff.Store(false)
		_ = tc.SetReadDeadline(time.Time{})
		return HandoffState{}, nil, errors.New("conn: the reader did not stop in time")
	}
	c.mu.Lock()
	st := HandoffState{Telnet: c.m.Snapshot(), Liner: c.lines.state(), Echo: c.echo.Load()}
	c.mu.Unlock()
	f, err := tc.File() // a duplicate descriptor: closing tc below does not close the connection
	if err != nil {
		// cannot hand over after all: resume reading
		c.handoff.Store(false)
		_ = tc.SetReadDeadline(time.Time{})
		go c.run2()
		return HandoffState{}, nil, err
	}
	c.closeOnce.Do(func() { // like Close, but the socket stays open in the duplicate
		c.ncMu.Lock()
		c.closing.Store(true) // writes now fail with ErrClosed
		c.ncMu.Unlock()
		_ = tc.Close() // closes only our descriptor
		close(c.quit)
		if c.cancel != nil {
			c.cancel()
		}
	})
	close(c.events)
	close(c.done)
	return st, f, nil
}

// run2 restarts the reader after a failed Handoff.
func (c *Conn) run2() {
	c.exited = make(chan struct{})
	c.adopted = true
	c.run()
}

// Adopt returns a running Conn that continues from a Handoff: f is the duplicated socket and st the
// state. cfg must be the connection's original configuration. No "connected" event is emitted.
func Adopt(cfg Config, st HandoffState, f *os.File) (*Conn, error) {
	if err := cfg.normalise(); err != nil {
		return nil, err
	}
	if cfg.World.TLS {
		return nil, ErrNotHandoffable
	}
	nc, err := net.FileConn(f) // its own duplicate: the caller may close f
	if err != nil {
		return nil, err
	}
	tc, ok := nc.(*net.TCPConn)
	if !ok {
		nc.Close()
		return nil, ErrNotHandoffable
	}
	_ = tc.SetKeepAlive(true)
	_ = tc.SetKeepAlivePeriod(cfg.KeepAlive)
	c := newConn(cfg, func() {})
	m, err := telnet.Restore(c.telnetConfig(), st.Telnet)
	if err != nil {
		nc.Close()
		return nil, err
	}
	if !c.lines.restore(st.Liner) {
		nc.Close()
		return nil, errors.New("conn: invalid line state")
	}
	c.m = m
	c.echo.Store(st.Echo)
	c.nc = nc
	c.adopted = true
	go c.run()
	return c, nil
}
