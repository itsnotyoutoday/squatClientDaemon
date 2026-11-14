package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/conn"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/scrollback"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// maskedEcho replaces the echoed text while the game has asked us not to echo
// (password prompts), so secrets never reach the scrollback or other clients.
const maskedEcho = "********"

// session is one game connection plus its history. All mutable fields are
// guarded by mu. Lock order: Daemon.smu -> mu -> Daemon.pmu.
type session struct {
	d     *Daemon
	dir   string
	store *scrollback.Store
	done  chan struct{} // closed when the pump has finished (or never started)

	sendMu sync.Mutex // serialises echo+send so echo order matches wire order

	mu         sync.Mutex
	m          *meta
	c          *conn.Conn
	attached   map[uint64]wire.Peer
	caps       proto.Caps
	remoteEcho bool
	stopping   bool
	stopReason string // the end reason recorded when stopping (default squatter_stopped)
	handedOff  bool   // the connection was handed to another process (squatterd reload)
	dirty      bool
	closed     bool
}

func now() int64 { return time.Now().UnixMilli() }

func humanReason(reason, detail string) string {
	var h string
	switch reason {
	case proto.EndRemoteClosed:
		h = "closed by remote host"
	case proto.EndReset:
		h = "connection reset"
	case proto.EndTimeout:
		h = "timed out"
	case proto.EndTLSError:
		h = "TLS error"
	case proto.EndConnectFailed:
		h = "could not connect"
	case proto.EndClientQuit:
		h = "disconnected by client"
	case proto.EndSquatterStopped:
		h = "squatterd stopped"
	case proto.EndSquatterRestarted:
		h = "squatterd was restarted and could not keep this connection (TLS)"
	default:
		h = reason
	}
	switch reason {
	case proto.EndRemoteClosed, proto.EndClientQuit, proto.EndSquatterStopped, proto.EndSquatterRestarted:
		return h // the reason says it all
	}
	if detail != "" {
		h += " (" + detail + ")"
	}
	return h
}

// ---------------------------------------------------------------- info / broadcast

func (s *session) info(device string) proto.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.infoLocked(device)
}

func (s *session) infoLocked(device string) proto.SessionInfo {
	first, last := s.store.Bounds()
	var unread uint64
	if ack := s.m.Acks[device]; last > ack {
		unread = last - ack
	}
	return proto.SessionInfo{ID: s.m.ID, Name: s.m.Name, Host: s.m.Host, Port: s.m.Port, TLS: s.m.TLS, State: s.m.State,
		StartedAt: s.m.StartedAt, FirstSeq: first, LastSeq: last, Unread: unread, Attached: len(s.attached),
		RemoteEcho: s.remoteEcho, EndedAt: s.m.EndedAt, EndReason: s.m.EndReason, EndDetail: s.m.EndDetail, LastText: s.m.LastText}
}

// broadcastLocked sends session.state to ALL authenticated clients.
func (s *session) broadcastLocked() {
	for _, p := range s.d.peerList() {
		p.Send(wire.SessionMsg{V: proto.Version, T: wire.TSessionState, Session: s.infoLocked(p.DeviceID())})
	}
}

func (s *session) saveLocked() {
	if err := writeMeta(s.dir, s.m); err != nil {
		s.d.log.Error("daemon: writing meta", "id", s.m.ID, "err", err)
	}
	s.dirty = false
}

func (s *session) flushIfDirty() {
	s.mu.Lock()
	if s.dirty && !s.closed {
		s.saveLocked()
	}
	s.mu.Unlock()
}

// ---------------------------------------------------------------- entries

func (s *session) lineLocked(kind string, spans []proto.Span, prompt bool) scrollback.Entry {
	if spans == nil {
		spans = []proto.Span{}
	}
	return scrollback.LineEntry(proto.Line{V: proto.Version, T: proto.TLine, Session: s.m.ID, Ts: now(), Kind: kind, Spans: spans, Prompt: prompt})
}

// appendLocked stamps, stores and fans the entry out to attached clients.
func (s *session) appendLocked(e scrollback.Entry) {
	seq, err := s.store.Append(e)
	if err != nil {
		s.d.log.Error("daemon: scrollback append", "id", s.m.ID, "err", err)
		return
	}
	switch {
	case e.Line != nil:
		e.Line.Seq = seq
	case e.Gmcp != nil:
		e.Gmcp.Seq = seq
	}
	b, err := json.Marshal(e)
	if err != nil {
		s.d.log.Error("daemon: marshal entry", "id", s.m.ID, "err", err)
		return
	}
	for _, p := range s.attached {
		p.SendRaw(b)
	}
}

func (s *session) systemLocked(text string) {
	s.appendLocked(s.lineLocked(proto.KindSystem, []proto.Span{{S: text}}, false))
}

// ---------------------------------------------------------------- pump

func (s *session) pump() {
	defer s.d.wg.Done()
	defer close(s.done)
	for ev := range s.c.Events() {
		s.handle(ev)
	}
}

func (s *session) handle(ev conn.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch ev.Kind {
	case conn.EvState:
		if ev.Connected && s.m.State == proto.StateConnecting {
			s.m.State = proto.StateConnected
			s.systemLocked(fmt.Sprintf("*** Connected to %s:%d", s.m.Host, s.m.Port))
			s.saveLocked()
			s.broadcastLocked()
		}
	case conn.EvLine:
		s.appendLocked(s.lineLocked(proto.KindGame, ev.Spans, ev.Prompt))
	case conn.EvGmcp:
		s.appendLocked(scrollback.GmcpEntry(proto.GmcpMsg{V: proto.Version, T: proto.TGmcp, Session: s.m.ID, Ts: now(), Package: ev.Package, Data: ev.Data}))
	case conn.EvRemoteEcho:
		if s.remoteEcho != ev.RemoteEcho {
			s.remoteEcho = ev.RemoteEcho
			s.broadcastLocked()
		}
	case conn.EvEnded:
		reason, detail := ev.Reason, ev.Detail
		if s.stopping {
			reason, detail = proto.EndSquatterStopped, ""
			if s.stopReason != "" {
				reason = s.stopReason
			}
		}
		s.endLocked(reason, detail, ev.LastText)
	}
}

func (s *session) endLocked(reason, detail, lastText string) {
	s.m.State = proto.StateEnded
	s.m.EndedAt = now()
	s.m.EndReason, s.m.EndDetail = reason, detail
	if lastText != "" {
		s.m.LastText = lastText
	}
	s.remoteEcho = false
	s.systemLocked("*** Connection closed: " + humanReason(reason, detail))
	s.saveLocked()
	s.broadcastLocked()
}

// markStoppedAtLoad turns a session found live on disk into an ended one.
func (s *session) markStoppedAtLoad() { s.markEndedAtLoad(proto.EndSquatterStopped, "") }

func (s *session) markEndedAtLoad(reason, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	last := ""
	if ents, err := s.store.Last(300); err == nil {
		for _, e := range ents {
			if e.Line != nil && e.Line.Kind == proto.KindGame {
				if p := strings.TrimSpace(e.Line.Plain()); p != "" {
					last = p
				}
			}
		}
	}
	s.endLocked(reason, detail, last)
	close(s.done)
}

// ---------------------------------------------------------------- lifecycle

func (s *session) stop() {
	s.mu.Lock()
	s.stopping = true
	c, live := s.c, s.m.State != proto.StateEnded
	s.mu.Unlock()
	if c != nil && live {
		c.Close()
	}
}

func (s *session) disconnect() error {
	s.mu.Lock()
	c, ended := s.c, s.m.State == proto.StateEnded
	s.mu.Unlock()
	if ended || c == nil {
		return wire.Errf(proto.ErrSessionEnded, "session already ended")
	}
	c.Close()
	return nil
}

func (s *session) canDismiss() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m.State != proto.StateEnded {
		return wire.Errf(proto.ErrBadRequest, "session is still live: disconnect it first")
	}
	return nil
}

func (s *session) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if err := writeMeta(s.dir, s.m); err != nil {
		s.d.log.Error("daemon: writing meta", "id", s.m.ID, "err", err)
	}
	return s.store.Close()
}

// ---------------------------------------------------------------- clients

func unionCaps(peers map[uint64]wire.Peer) (proto.Caps, bool) {
	var u proto.Caps
	if len(peers) == 0 {
		return u, false
	}
	for _, p := range peers {
		c := p.Caps()
		u.Truecolor = u.Truecolor || c.Truecolor
		u.UTF8 = u.UTF8 || c.UTF8
		u.ScreenReader = u.ScreenReader || c.ScreenReader
	}
	return u, true
}

func (s *session) updateCapsLocked() {
	u, ok := unionCaps(s.attached)
	if !ok || u == s.caps || s.m.State == proto.StateEnded || s.c == nil {
		return
	}
	s.caps = u
	s.c.SetCaps(u)
}

func (s *session) capsChanged(p wire.Peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.attached[p.ID()]; ok {
		s.updateCapsLocked()
	}
}

func (s *session) detach(p wire.Peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.attached[p.ID()]; !ok {
		return
	}
	delete(s.attached, p.ID())
	s.updateCapsLocked()
	s.broadcastLocked()
}

// attach implements PROTOCOL 5.1. Everything from the replay snapshot to the
// registration of the client happens under s.mu, and live entries are only
// produced under s.mu too, so the client's queue receives
// attached, backlog..., done, then every later live entry in seq order: no
// duplicate and no gap at the seam without any extra buffering.
func (s *session) attach(p wire.Peer, req wire.AttachReq) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var since uint64
	if req.Since != nil {
		since = *req.Since
	}
	rp, err := s.store.Replay(since, req.Since != nil, req.MaxBacklog)
	if err != nil {
		return fmt.Errorf("replay: %w", err)
	}
	var gap *wire.Gap
	if rp.Gap != nil {
		gap = &wire.Gap{Missed: rp.Gap.Missed}
	}
	p.Send(wire.AttachedMsg{V: proto.Version, T: wire.TAttached, Session: s.m.ID, FirstSeq: rp.FirstSeq, LastSeq: rp.LastSeq, ReplayFrom: rp.ReplayFrom, Gap: gap})
	frame := make([]json.RawMessage, 0, wire.BacklogChunk)
	size := 0
	flush := func(done bool) {
		p.Send(wire.BacklogMsg{V: proto.Version, T: wire.TBacklog, Session: s.m.ID, Entries: frame, Done: done})
		frame, size = make([]json.RawMessage, 0, wire.BacklogChunk), 0
	}
	for _, e := range rp.Entries {
		b, err := json.Marshal(e)
		if err != nil {
			continue
		}
		frame = append(frame, b)
		size += len(b)
		if len(frame) >= wire.BacklogChunk || size >= wire.BacklogChunkBytes {
			flush(false)
		}
	}
	flush(true)
	s.attached[p.ID()] = p
	s.updateCapsLocked()
	s.broadcastLocked()
	return nil
}

func (s *session) ack(device string, seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, last := s.store.Bounds()
	if seq > last {
		seq = last
	}
	if seq > s.m.Acks[device] {
		s.m.Acks[device] = seq
		s.dirty = true
	}
}

func (s *session) resize(cols, rows int) {
	s.mu.Lock()
	c, ended := s.c, s.m.State == proto.StateEnded
	s.mu.Unlock()
	if c != nil && !ended {
		c.Resize(cols, rows)
	}
}

func (s *session) sendText(text string, echo bool) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	switch s.m.State {
	case proto.StateEnded:
		s.mu.Unlock()
		return wire.Errf(proto.ErrSessionEnded, "session has ended")
	case proto.StateConnecting:
		s.mu.Unlock()
		return wire.Errf(proto.ErrBadRequest, "session is still connecting")
	}
	c := s.c
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", ""), "\n")
	if !echo {
		lines = nil // secret: delivered to the game, never recorded or echoed
	}
	for _, line := range lines {
		shown := line
		if s.remoteEcho {
			shown = maskedEcho
		}
		var spans []proto.Span
		if shown != "" {
			spans = []proto.Span{{S: shown}}
		}
		s.appendLocked(s.lineLocked(proto.KindEcho, spans, false))
	}
	s.mu.Unlock()
	if err := c.Send(text); err != nil {
		s.mu.Lock()
		s.systemLocked("*** Send failed: " + err.Error())
		s.mu.Unlock()
		return wire.Errf(proto.ErrSessionEnded, "send failed: "+err.Error())
	}
	return nil
}
