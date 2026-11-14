package conn

import (
	"errors"
	"fmt"
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

// HandoffStateVersion is the version of HandoffState written by this binary. Fields may be ADDED
// without changing it (an older reader ignores them: they must be optional for correctness); anything
// else bumps it, and Adopt/CheckHandoff then refuse a state they do not know.
const HandoffStateVersion = 1

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
	// V is the version of this structure (HandoffStateVersion); 0 (absent) means 1. A reader refuses a
	// higher one (internal/daemon turns that into "state too new" and the old process carries on).
	V      int                 `json:"v,omitempty"`
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
	st := HandoffState{V: HandoffStateVersion, Telnet: c.m.Snapshot(), Liner: c.lines.state(), Echo: c.echo.Load()}
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

// CanHandoff reports whether Handoff would currently be attempted for this connection: it is plain
// TCP and still open (used to predict which sessions a reload would end).
func (c *Conn) CanHandoff() bool {
	c.ncMu.Lock()
	nc := c.nc
	c.ncMu.Unlock()
	_, ok := nc.(*net.TCPConn)
	return ok && !c.tls && !c.closing.Load()
}

// ErrStateTooNew is returned for a HandoffState written by a newer binary.
var ErrStateTooNew = errors.New("conn: handoff state written by a newer squatterd")

// CheckHandoff verifies that Adopt would accept st for cfg, without touching any socket. The new
// process checks every carried connection BEFORE adopting any, so that a state it cannot restore makes
// it refuse the whole handoff (the old process then carries on with all connections) instead of
// dropping one connection halfway.
func CheckHandoff(cfg Config, st HandoffState) error {
	if err := cfg.normalise(); err != nil {
		return err
	}
	if cfg.World.TLS {
		return ErrNotHandoffable
	}
	if st.V > HandoffStateVersion {
		return fmt.Errorf("%w (state version %d, this build understands %d)", ErrStateTooNew, st.V, HandoffStateVersion)
	}
	c := newConn(cfg, func() {})
	if _, err := telnet.Restore(c.telnetConfig(), st.Telnet); err != nil {
		return err
	}
	if !c.lines.restore(st.Liner) {
		return errors.New("conn: invalid line state")
	}
	return nil
}

// Adopt returns a running Conn that continues from a Handoff: f is the duplicated socket and st the
// state. cfg must be the connection's original configuration. No "connected" event is emitted.
func Adopt(cfg Config, st HandoffState, f *os.File) (*Conn, error) {
	c, err := AdoptPaused(cfg, st, f)
	if err != nil {
		return nil, err
	}
	c.Resume()
	return c, nil
}

// AdoptPaused is Adopt, but nothing is read from the socket until Resume (or Close) is called. A new
// squatterd process adopts its connections paused and resumes them only after it told the old process
// it is ready: if it dies before that, no byte was consumed from any game socket and the old process
// takes the connections back with nothing lost.
func AdoptPaused(cfg Config, st HandoffState, f *os.File) (*Conn, error) {
	if err := cfg.normalise(); err != nil {
		return nil, err
	}
	if err := CheckHandoff(cfg, st); err != nil {
		return nil, err
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
	c.paused = true
	return c, nil
}

// Resume starts reading from an adopted connection (a no-op for any other and when already started).
func (c *Conn) Resume() {
	c.resumeOnce.Do(func() {
		if c.paused {
			go c.run()
		}
	})
}

// Discard drops a paused adopted connection WITHOUT ending anything: it closes only this process's
// own descriptor (the game connection stays open in whoever else holds the socket), reads nothing and
// closes the event channel without an Ended event. Used when a new process refuses a handoff after
// adopting some connections.
func (c *Conn) Discard() {
	c.resumeOnce.Do(func() {}) // the reader never starts
	if !c.paused {
		return
	}
	c.closeOnce.Do(func() {
		c.ncMu.Lock()
		c.closing.Store(true)
		if c.nc != nil {
			c.nc.Close()
		}
		c.ncMu.Unlock()
		close(c.quit)
		if c.cancel != nil {
			c.cancel()
		}
		close(c.events)
		close(c.done)
	})
}
