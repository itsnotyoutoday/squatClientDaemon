package daemon

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/conn"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tlsmigrate"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// Restarting the squatterd without losing the game connections (docs/RELOAD.md). The old process
// quiesces every session, gets each plain-TCP game connection back as (state, duplicated socket),
// writes its scrollback and metadata to disk and closes its stores (PrepareHandoff). A new process is
// started with the sockets inherited and creates its Daemon with Config.Handoff: sessions are rebuilt
// from disk and each connection is adopted, so the game never notices. Connections that cannot be
// handed over (TLS to the game, or a connection still being set up) end with reason squatter_restarted.

// HandoffSession is one carried-over session: its id, the position of its socket in Handoff.Files and
// the connection's state.
type HandoffSession struct {
	ID         string            `json:"id"`
	File       int               `json:"file"`
	Conn       conn.HandoffState `json:"conn"`
	RemoteEcho bool              `json:"remoteEcho,omitempty"`
	Caps       proto.Caps        `json:"caps"`
	// TLS is the serialised TLS session of a TLS game connection (pkg/tlsmigrate State): KEY MATERIAL.
	// In the file it is the required extension Ext["tls"] (ExtTLS); in memory it is kept here, apart from
	// the JSON model, so that it is never printed or encoded by accident. The old process keeps it until
	// the new one reported ready (a rollback re-imports the session from it), then wipes it.
	TLS Secret `json:"-"`
	// Ext holds optional per-session extensions, each with its own version (see statefile.go).
	Ext map[string]Section `json:"ext,omitempty"`
}

// Secret is key material: it prints as "[redacted]" with every fmt verb and in logs, and Zero wipes it.
type Secret []byte

func (Secret) String() string               { return "[redacted]" }
func (Secret) GoString() string             { return "daemon.Secret([redacted])" }
func (Secret) LogValue() slog.Value         { return slog.StringValue("[redacted]") }
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }
func (Secret) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, "[redacted]") }
func (s Secret) Zero()                      { clear(s) }

// wipe zeroes every secret in the bundle (and raw extension data that could still hold one).
func (b *HandoffBundle) wipe() {
	for i := range b.Sessions {
		b.Sessions[i].TLS.Zero()
		b.Sessions[i].TLS = nil
		for _, e := range b.Sessions[i].Ext {
			clear(e.Data)
		}
	}
}

// ReloadCarry is what the new process inherits about the history of reloads (section "reload").
type ReloadCarry struct {
	ServiceSince int64               `json:"serviceSince,omitempty"` // first start of the chain, unix ms
	Last         *proto.ReloadResult `json:"last,omitempty"`         // this reload, as the old process saw it
}

// HandoffBundle is what the new process needs besides the sockets. It is the in-memory model: the file
// format (statefile.go) maps it to versioned sections and back.
type HandoffBundle struct {
	Format     int              `json:"-"`          // format of the file it was read from; StateFormat when new
	Generation int              `json:"generation"` // the number of the NEW process (1 = first reload)
	CreatedAt  int64            `json:"createdAt"`
	Sessions   []HandoffSession `json:"sessions"`
	Ended      []string         `json:"ended,omitempty"` // sessions that could not be carried over
	Reload     ReloadCarry      `json:"-"`
	Skipped    []string         `json:"-"` // optional sections of a newer writer this reader ignored
}

// Handoff is a bundle with the duplicated sockets: Files[i] belongs to Bundle.Sessions[i].File == i.
type Handoff struct {
	Bundle HandoffBundle
	Files  []*os.File
}

// Close closes the duplicated sockets (the connections stay open in whoever else holds them) and wipes
// the TLS key material of the bundle.
func (h *Handoff) Close() {
	h.Bundle.wipe()
	for _, f := range h.Files {
		if f != nil {
			_ = f.Close()
		}
	}
	h.Files = nil
}

// ErrHandingOff is returned to requests that arrive while the daemon is being handed over.
var ErrHandingOff = wire.Errf(proto.ErrBusy, "squatterd is restarting, try again in a moment")

// PrepareHandoff quiesces the daemon for a restart. On success every session is either ended (and says
// why) or in the returned Handoff, all scrollback is on disk and closed, and this Daemon is inert: it
// refuses new requests. The caller starts the new process with the files, or, if that fails, creates a
// new Daemon with Config.Handoff = h in this process to carry on. If the error is non-nil and h is not,
// nothing was handed over and this Daemon is untouched or already inert; if both are non-nil, a store
// failed to close but the connections are in h: roll back with it, never discard it.
func (d *Daemon) PrepareHandoff(generation int) (*Handoff, error) {
	d.smu.Lock()
	if d.closing {
		d.smu.Unlock()
		return nil, errors.New("daemon: already shutting down or handed over")
	}
	d.closing = true
	d.smu.Unlock()

	for _, p := range d.peerList() {
		p.Send(wire.Simple("server.restarting")) // clients reconnect at once and replay what they missed
	}
	h := &Handoff{Bundle: HandoffBundle{Format: StateFormat, Generation: generation, CreatedAt: time.Now().UnixMilli()}}
	all := d.list()
	for _, s := range all {
		s.sendMu.Lock() // no input reaches the game mid-handoff
		s.mu.Lock()
		state, c, caps, echo := s.m.State, s.c, s.caps, s.remoteEcho
		s.mu.Unlock()
		switch {
		case state == proto.StateEnded || c == nil:
		case state == proto.StateConnected:
			var (
				st  conn.HandoffState
				f   *os.File
				tls []byte
				err error
			)
			if c.CanHandoffTLS() {
				st, tls, f, err = handoffTLS(c)
			} else {
				st, f, err = c.Handoff() // refuses a TLS connection that cannot be migrated: it ends below
			}
			if err != nil {
				s.mu.Lock()
				s.stopping, s.stopReason = true, proto.EndSquatterRestarted
				s.mu.Unlock()
				c.Close()
				h.Bundle.Ended = append(h.Bundle.Ended, s.m.ID)
				d.log.Info("daemon: connection not carried over", "id", s.m.ID, "why", err)
				break
			}
			s.mu.Lock()
			s.handedOff = true
			s.mu.Unlock()
			h.Bundle.Sessions = append(h.Bundle.Sessions, HandoffSession{ID: s.m.ID, File: len(h.Files), Conn: st, RemoteEcho: echo, Caps: caps, TLS: tls})
			h.Files = append(h.Files, f)
		default: // still connecting: nothing to carry
			s.mu.Lock()
			s.stopping, s.stopReason = true, proto.EndSquatterRestarted
			s.mu.Unlock()
			c.Close()
			h.Bundle.Ended = append(h.Bundle.Ended, s.m.ID)
		}
	}
	// every pump finishes (queued events are stored, ended sessions record their end)
	for _, s := range all {
		<-s.done
	}
	d.end()
	d.wg.Wait()
	var firstErr error
	for _, s := range all {
		if err := s.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	d.rmu.Lock()
	h.Bundle.Reload = ReloadCarry{ServiceSince: d.rs.serviceSince, Last: &proto.ReloadResult{
		At: h.Bundle.CreatedAt, Ok: true, Generation: generation, Carried: len(h.Bundle.Sessions), Ended: h.Bundle.Ended}}
	d.rmu.Unlock()
	// An error here (a store that did not close cleanly) must NOT drop the connections: h is returned
	// with it and the caller rolls back with it (the sockets are in h.Files).
	return h, firstErr
}

// tlsHandoffRetry bounds how long PrepareHandoff retries a TLS session that is not at a record boundary
// at the moment (a Read or Write in flight, a handshake message split across records).
const tlsHandoffRetry = 3 * time.Second

// handoffTLS is c.HandoffTLS with a few retries for "not right now". On any error the connection is
// still running here (HandoffTLS resumes it). The returned blob is key material: it goes to the bundle
// (Secret) and nowhere else.
func handoffTLS(c *conn.Conn) (conn.HandoffState, []byte, *os.File, error) {
	deadline := time.Now().Add(tlsHandoffRetry)
	for {
		st, blob, f, err := c.HandoffTLS()
		if err == nil || !(errors.Is(err, tlsmigrate.ErrBusy) || errors.Is(err, tlsmigrate.ErrNotQuiescent)) || time.Now().After(deadline) {
			return st, blob, f, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// adopt rebuilds the connection of a session from a handoff. The connection is PAUSED: nothing is read
// from the game and nothing is written to disk until ResumeAdopted, after the new process told the old
// one it is ready. If it dies before that, the old process takes the connections back with no byte lost.
func (d *Daemon) adopt(s *session, hs HandoffSession, f *os.File) error {
	w := proto.World{Name: s.m.Name, Host: s.m.Host, Port: s.m.Port, TLS: s.m.TLS, TLSVerify: s.m.TLSVerify, Encoding: s.m.Encoding}
	cc := conn.Config{World: w, Caps: hs.Caps, Proxied: true}
	if d.cfg.ConnTweak != nil {
		d.cfg.ConnTweak(&cc)
	}
	var c *conn.Conn
	var err error
	if len(hs.TLS) > 0 {
		c, err = conn.AdoptTLSPaused(cc, hs.Conn, hs.TLS, f)
		hs.TLS.Zero() // the adopted connection holds the keys now; the old process's copy is gone with its Handoff
	} else {
		c, err = conn.AdoptPaused(cc, hs.Conn, f)
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.c, s.caps, s.remoteEcho = c, hs.Caps, hs.RemoteEcho
	s.paused = true
	s.mu.Unlock()
	return nil
}

// ResumeAdopted starts reading from the connections adopted from a handoff and records the restart
// note in their scrollback. The squatterd calls it right after it reported ready to the old process
// (and a Daemon built without a handoff has nothing to resume). It is safe to call more than once.
func (d *Daemon) ResumeAdopted() {
	for _, s := range d.list() {
		s.mu.Lock()
		if s.paused {
			s.paused = false
			s.systemLocked("*** squatterd was restarted; the connection to the game was kept")
			s.saveLocked()
			s.c.Resume()
		}
		s.mu.Unlock()
	}
	d.rmu.Lock()
	if r := d.rs.last; r != nil && r.DurationMs == 0 && d.rs.pendingFrom > 0 {
		r.DurationMs = time.Now().UnixMilli() - d.rs.pendingFrom
	}
	d.rs.pendingFrom = 0
	d.rmu.Unlock()
}
