package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/conn"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// ---- event loop -----------------------------------------------------------------

// loop is the only consumer of one wire.Client: it feeds sessions in wire
// order. It exits when the client's event channel closes.
func (q *squatter) loop(c *wire.Client, done chan struct{}) {
	defer close(done)
	for ev := range c.Events() {
		switch v := ev.(type) {
		case wire.SessionState:
			s, created := q.ensure(v.Info)
			if s == nil {
				continue
			}
			if created {
				q.attach(c, s)
			} else {
				s.markAckFrom(v.Info)
				s.applyRemoteInfo(v.Info)
			}
			q.publish()
		case wire.Removed:
			q.mu.Lock()
			s := q.sessions[v.Session]
			delete(q.sessions, v.Session)
			q.mu.Unlock()
			if s != nil {
				q.e.remove(s)
				q.publish()
			}
		case wire.Attached:
			if s := q.lookup(v.Session); s != nil && v.Gap != nil && v.Gap.Missed > 0 {
				s.systemLine(fmt.Sprintf("*** %d line(s) were missed while you were away (the squatter's history no longer has them)", v.Gap.Missed))
			}
		case wire.Backlog:
			s := q.lookup(v.Session)
			if s == nil {
				continue
			}
			for _, en := range v.Entries {
				q.ingest(s, en, false)
			}
			if v.Done {
				s.mu.Lock()
				s.remote.replaying = false
				s.publishInfoLocked()
				s.mu.Unlock()
				q.attachDone()
			}
		case wire.Line:
			if s := q.lookup(v.Session); s != nil {
				q.ingest(s, wire.Entry{Line: &v.Line}, true)
			}
		case wire.Gmcp:
			if s := q.lookup(v.Session); s != nil {
				g := v.GmcpMsg
				q.ingest(s, wire.Entry{Gmcp: &g}, true)
			}
		case wire.Error:
			q.e.bus.publish(Event{Kind: EvNotice, Level: LevelWarn, Text: fmt.Sprintf("squatter %s: %s: %s", q.name(), v.Code, v.Message)})
			q.attachDoneIfPending()
		case wire.Closed:
			q.onLost(c, v.Err)
		}
	}
	q.onLost(c, nil)
}

func (q *squatter) attachDoneIfPending() {
	q.mu.Lock()
	p := q.pending
	q.mu.Unlock()
	if p > 0 {
		q.attachDone()
	}
}

// ingest de-duplicates by daemon seq and hands an entry to its session.
func (q *squatter) ingest(s *session, en wire.Entry, live bool) {
	seq := en.Seq()
	s.mu.Lock()
	if seq <= s.remote.lastSeq {
		s.mu.Unlock()
		return
	}
	s.remote.lastSeq = seq
	s.mu.Unlock()
	switch {
	case en.Line != nil:
		s.ingestLine(*en.Line, live)
	case en.Gmcp != nil && live:
		s.onGmcp(en.Gmcp.Package, en.Gmcp.Data)
	}
}

// onLost handles the end of a connection that was not closed on purpose.
// There is no reconnect: ReconnectSquatter is an explicit action.
func (q *squatter) onLost(c *wire.Client, err error) {
	q.mu.Lock()
	if q.cl != c {
		q.mu.Unlock() // closed on purpose (closeConn already cleaned up)
		return
	}
	q.cl, q.loopDone = nil, nil
	msg, hint := describeLoss(q.cfg.Name, err)
	q.state, q.problem, q.message = SqUnreachable, ProblemConnectionLo, msg
	q.pending = 0
	if q.awayTimer != nil {
		q.awayTimer()
		q.awayTimer = nil
	}
	q.mu.Unlock()
	q.setLinks(LinkUnreachable, "*** "+msg+" "+hint)
	q.publish()
	q.e.bus.publish(Event{Kind: EvNotice, Level: LevelWarn, Text: msg})
}

// ---- session creation ---------------------------------------------------------------

func (e *Engine) newRemoteSession(q *squatter, in proto.SessionInfo, link string) (*session, error) {
	id := q.id + ":" + in.ID
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, ErrClosed
	}
	tfName := e.uniqueName(in.Name)
	e.mu.Unlock()
	w := World{Name: in.Name, Host: in.Host, Port: in.Port, TLS: in.TLS, Via: q.id}
	s, err := newSession(e, id, tfName, w, conn.Config{}, "")
	if err != nil {
		return nil, err
	}
	s.info.State, s.info.StartedAt, s.info.RemoteEcho, s.info.Attached = in.State, in.StartedAt, in.RemoteEcho, in.Attached
	if in.State == proto.StateEnded {
		s.info.EndedAt, s.info.EndReason, s.info.EndDetail, s.info.LastText = in.EndedAt, in.EndReason, in.EndDetail, in.LastText
	}
	s.remote = &remoteState{sq: q, rid: in.ID, link: link, ended: make(chan struct{})}
	if in.State == proto.StateEnded {
		close(s.remote.ended)
	}
	if in.Unread <= in.LastSeq {
		s.remote.ackedRemote = in.LastSeq - in.Unread
	}
	s.tr = &remoteTransport{s}
	e.mu.Lock()
	e.sessions[id] = s
	e.order = append(e.order, id)
	first := e.fg == ""
	if first {
		e.fg = id
	}
	e.mu.Unlock()
	if first {
		e.bus.publish(Event{Kind: EvForeground, Session: id})
	}
	s.mu.Lock()
	s.publishInfoLocked()
	s.mu.Unlock()
	return s, nil
}

// dialVia creates a session through the squatter named by w.Via. The login
// template runs here, in the engine, after the daemon reports the game
// connection as established; the password is not stored by the engine or the
// daemon. Dial blocks for the daemon's reply (up to 20 s).
func (e *Engine) dialVia(w World, password string) (SessionInfo, error) {
	q := e.getSquatter(w.Via)
	if q == nil {
		return SessionInfo{}, fmt.Errorf("%w: %s", ErrNoSquatter, w.Via)
	}
	c := q.client()
	if c == nil {
		st, _ := e.SquatterStatus(w.Via)
		return SessionInfo{}, fmt.Errorf("squatter %s is not connected (%s)", st.Name, st.State)
	}
	if q.linkNow() == LinkDetached {
		return SessionInfo{}, errors.New("suspended: resume before opening new sessions")
	}
	ctx, cancel := context.WithTimeout(e.ctx, 20*time.Second)
	defer cancel()
	in, err := c.Connect(ctx, proto.World{Name: w.Name, Host: w.Host, Port: w.Port, TLS: w.TLS,
		TLSVerify: orDefault(w.TLSVerify, "strict"), Encoding: orDefault(w.Encoding, "utf-8")})
	if err != nil {
		return SessionInfo{}, fmt.Errorf("squatter %s: %w", q.name(), err)
	}
	s, _ := q.ensure(in)
	if s == nil {
		return SessionInfo{}, ErrClosed
	}
	s.armLogin(w, loginLine(w, password))
	q.attach(c, s)
	return s.snapshot(), nil
}

// dismissRemote ends (if needed) and removes a squatter session on the daemon.
func (e *Engine) dismissRemote(s *session) error {
	r := s.remote
	c := r.sq.client()
	if c == nil {
		return fmt.Errorf("squatter %s is not connected: cannot dismiss its session", r.sq.name())
	}
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	if !s.isEnded() {
		if err := c.Disconnect(ctx, r.rid); err != nil {
			return err
		}
		select {
		case <-r.ended:
		case <-ctx.Done():
			return errors.New("the session did not end in time")
		}
	}
	if err := c.Dismiss(ctx, r.rid); err != nil {
		return err
	}
	for e.get(s.id) != nil { // the daemon's session.removed broadcast removes it locally
		select {
		case <-ctx.Done():
			return errors.New("the squatter did not confirm the removal")
		case <-time.After(5 * time.Millisecond):
		}
	}
	return nil
}

// describeLoss turns the error that ended a squatter connection into text that
// says what most likely happened, without promising more than we know.
func describeLoss(name string, err error) (msg, hint string) {
	switch {
	case err == nil || errors.Is(err, io.EOF) || strings.Contains(strings.ToLower(err.Error()), "eof") ||
		strings.Contains(strings.ToLower(err.Error()), "going away") || strings.Contains(strings.ToLower(err.Error()), "closed"):
		return fmt.Sprintf("Squatter %q closed the connection (it was probably stopped or restarted; the game sessions it held may have ended).", name),
			"Reconnect to the squatter to see which sessions survived."
	default:
		return fmt.Sprintf("Lost the network connection to squatter %q (%v).", name, err),
			"If the squatter is still running, your sessions are still running there: reconnect to catch up."
	}
}
