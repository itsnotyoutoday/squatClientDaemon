package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/conn"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// Restarting the squatterd without losing the game connections (docs/RELOAD.md). The old process
// quiesces every session, gets each plain-TCP game connection back as (state, duplicated socket),
// writes its scrollback and metadata to disk and closes its stores (PrepareHandoff). A new process is
// started with the sockets inherited and creates its Daemon with Config.Handoff: sessions are rebuilt
// from disk and each connection is adopted, so the game never notices. Connections that cannot be
// handed over (TLS to the game, or a connection still being set up) end with reason squatter_restarted.

// HandoffVersion is the version of the handoff file; a different one is refused.
const HandoffVersion = 1

// HandoffSession is one carried-over session: its id, the position of its socket in Handoff.Files and
// the connection's state.
type HandoffSession struct {
	ID         string            `json:"id"`
	File       int               `json:"file"`
	Conn       conn.HandoffState `json:"conn"`
	RemoteEcho bool              `json:"remoteEcho,omitempty"`
	Caps       proto.Caps        `json:"caps"`
}

// HandoffBundle is what the new process needs besides the sockets.
type HandoffBundle struct {
	Version    int              `json:"version"`
	Generation int              `json:"generation"` // the number of the NEW process (1 = first reload)
	CreatedAt  int64            `json:"createdAt"`
	Sessions   []HandoffSession `json:"sessions"`
	Ended      []string         `json:"ended,omitempty"` // sessions that could not be carried over
}

// Handoff is a bundle with the duplicated sockets: Files[i] belongs to Bundle.Sessions[i].File == i.
type Handoff struct {
	Bundle HandoffBundle
	Files  []*os.File
}

// Close closes the duplicated sockets (the connections stay open in whoever else holds them).
func (h *Handoff) Close() {
	for _, f := range h.Files {
		if f != nil {
			_ = f.Close()
		}
	}
	h.Files = nil
}

// WriteBundle writes the bundle atomically (mode 0600).
func WriteBundle(path string, b HandoffBundle) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// ReadBundle reads and checks a bundle.
func ReadBundle(path string) (HandoffBundle, error) {
	var b HandoffBundle
	data, err := os.ReadFile(path)
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return b, fmt.Errorf("daemon: handoff file: %w", err)
	}
	if b.Version != HandoffVersion {
		return b, fmt.Errorf("daemon: handoff file version %d, this squatterd understands %d", b.Version, HandoffVersion)
	}
	return b, nil
}

// ErrHandingOff is returned to requests that arrive while the daemon is being handed over.
var ErrHandingOff = wire.Errf(proto.ErrBusy, "squatterd is restarting, try again in a moment")

// PrepareHandoff quiesces the daemon for a restart. On success every session is either ended (and says
// why) or in the returned Handoff, all scrollback is on disk and closed, and this Daemon is inert: it
// refuses new requests. The caller starts the new process with the files, or, if that fails, creates a
// new Daemon with Config.Handoff = h in this process to carry on.
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
	h := &Handoff{Bundle: HandoffBundle{Version: HandoffVersion, Generation: generation, CreatedAt: time.Now().UnixMilli()}}
	all := d.list()
	for _, s := range all {
		s.sendMu.Lock() // no input reaches the game mid-handoff
		s.mu.Lock()
		state, c, caps, echo := s.m.State, s.c, s.caps, s.remoteEcho
		s.mu.Unlock()
		switch {
		case state == proto.StateEnded || c == nil:
		case state == proto.StateConnected:
			st, f, err := c.Handoff()
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
			h.Bundle.Sessions = append(h.Bundle.Sessions, HandoffSession{ID: s.m.ID, File: len(h.Files), Conn: st, RemoteEcho: echo, Caps: caps})
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
	if firstErr != nil {
		h.Close()
		return nil, firstErr
	}
	return h, nil
}

// adoptInto rebuilds the connection of a session from a handoff.
func (d *Daemon) adopt(s *session, hs HandoffSession, f *os.File) error {
	w := proto.World{Name: s.m.Name, Host: s.m.Host, Port: s.m.Port, TLS: s.m.TLS, TLSVerify: s.m.TLSVerify, Encoding: s.m.Encoding}
	cc := conn.Config{World: w, Caps: hs.Caps, Proxied: true}
	if d.cfg.ConnTweak != nil {
		d.cfg.ConnTweak(&cc)
	}
	c, err := conn.Adopt(cc, hs.Conn, f)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.c, s.caps, s.remoteEcho = c, hs.Caps, hs.RemoteEcho
	s.systemLocked("*** squatterd was restarted; the connection to the game was kept")
	s.saveLocked()
	s.mu.Unlock()
	d.wg.Add(1)
	go s.pump()
	return nil
}
