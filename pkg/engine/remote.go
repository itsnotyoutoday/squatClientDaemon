package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/conn"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// Link states of a squatter-held session (SessionInfo.Link).
const (
	LinkAttached    = "attached"
	LinkDetached    = "detached"
	LinkUnreachable = "unreachable"
)

// remoteState is the squatter-specific part of a session. All fields are
// guarded by session.mu.
type remoteState struct {
	sq          *squatter
	rid         string // the daemon's session id
	link        string
	attachSent  bool   // an attach was sent on the current connection and not detached
	replaying   bool   // between attach and the final backlog frame
	lastSeq     uint64 // highest daemon seq ingested (the `since` of the next attach)
	ackedRemote uint64 // daemon seq up to which this device has acknowledged
	sized       bool   // the UI reported a size (re-sent after a re-attach)
	pairs       []seqPair
	ended       chan struct{} // closed when the session ended
	selfCtlBusy int           // tf-squatterd: own control requests in flight
}

// seqPair maps a local scrollback seq to the daemon seq it came from.
type seqPair struct{ local, remote uint64 }

// remoteTransport implements Transport for the control calls of a remote
// session. Output does not use Events (see Transport).
type remoteTransport struct{ s *session }

func (t *remoteTransport) Events() <-chan conn.Event { return nil }
func (t *remoteTransport) Send(text string) error    { return t.s.remote.send(t.s, text, false) }

// SendSecret sends a login/password line the daemon must not record.
func (t *remoteTransport) SendSecret(text string) error { return t.s.remote.send(t.s, text, true) }
func (t *remoteTransport) Resize(cols, rows int) {
	r := t.s.remote
	if c := r.sq.client(); c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.Resize(ctx, r.rid, cols, rows)
	}
}
func (t *remoteTransport) SetCaps(proto.Caps) {} // caps are per squatter connection
func (t *remoteTransport) RemoteEcho() bool {
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	return t.s.info.RemoteEcho
}

// Close asks the daemon to end the game connection (like /dc). The session
// then ends with client_quit; it is not detached.
func (t *remoteTransport) Close() {
	r := t.s.remote
	if c := r.sq.client(); c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.Disconnect(ctx, r.rid)
	}
}

// send writes one input line to the game through the daemon. It never blocks
// longer than 10 s and never reconnects.
func (r *remoteState) send(s *session, text string, secret bool) error {
	s.mu.Lock()
	link := r.link
	tfLocked := s.info.TF != nil && s.info.TF.Input == "tf"
	s.mu.Unlock()
	if tfLocked {
		// TinyFugue would refuse it asynchronously; say so now instead of dropping the text silently
		return errors.New("TinyFugue has the keyboard for this connection: take control first")
	}
	switch link {
	case LinkDetached:
		return errors.New("detached from the squatter (suspended): resume first")
	case LinkUnreachable:
		return fmt.Errorf("squatter %s is unreachable: reconnect it first", r.sq.name())
	}
	c := r.sq.client()
	if c == nil {
		return fmt.Errorf("squatter %s is not connected", r.sq.name())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if secret {
		return c.SendSecret(ctx, r.rid, text)
	}
	return c.Send(ctx, r.rid, text)
}

// ack tells the daemon this device has read up to the daemon seq (best effort).
func (r *remoteState) ack(s *session, rseq uint64) {
	s.mu.Lock()
	if rseq <= r.ackedRemote {
		s.mu.Unlock()
		return
	}
	r.ackedRemote = rseq
	s.mu.Unlock()
	if c := r.sq.client(); c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.Ack(ctx, r.rid, rseq)
	}
}

// remoteFor returns the daemon seq of the newest mapped line with local seq <= seq. s.mu held.
func (r *remoteState) remoteFor(seq uint64) uint64 {
	i := sort.Search(len(r.pairs), func(i int) bool { return r.pairs[i].local > seq })
	if i == 0 {
		return 0
	}
	return r.pairs[i-1].remote
}

// ---- ingestion (called only from the squatter's event loop) -----------------

// ingestLine handles one daemon line entry. live=false is replayed history:
// the pipeline then computes display effects only (gag, hilite) and nothing
// that sends, notifies or fires a trigger function runs.
//
// Echo and system lines are stored as received (the daemon echoes what any
// attached client sent; the engine does not echo again) and do not run the
// pipeline, exactly like local echo/system lines in direct mode.
func (s *session) ingestLine(l proto.Line, live bool) {
	if l.Kind != proto.KindGame {
		if l.Kind == proto.KindEcho && s.cap.dropEcho(l.Plain()) {
			return // echo of an internal Capture send (daemon without noEcho)
		}
		spans := l.Spans
		if l.Kind == proto.KindEcho {
			spans = s.echoWithOrigin(spans, l.Origin)
		}
		s.mu.Lock()
		s.appendRemoteLocked(l.Kind, spans, l.Prompt, l.Ts, l.Seq)
		s.mu.Unlock()
		return
	}
	// TinyFugue's display text (after its triggers) wins when it sent one.
	base := l.Spans
	if len(l.Disp) > 0 {
		base = l.Disp
	}
	line := proto.Line{V: proto.Version, T: proto.TLine, Session: s.id, Ts: l.Ts, Kind: proto.KindGame, Spans: base, Prompt: l.Prompt}
	if live {
		if s.cap.offer(line) {
			return // diverted by Capture
		}
	} else if !l.Prompt && strings.HasPrefix(line.Plain(), capPrefix) {
		return // replayed output of an earlier Capture
	}
	s.mu.Lock()
	s.collecting = true
	s.mu.Unlock()
	// On a tf-squatterd session TinyFugue runs the triggers unless this client owns
	// automation: then the pipeline computes display effects only (no sends, no
	// triggers), so nothing fires twice.
	runLive := live && (!s.remote.sq.isTF() || s.ownsAutomation())
	res := s.e.runPipeline(s, line, runLive)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collecting = false
	if !res.Gag && !(l.Gag && !s.e.showGagged.Load()) {
		s.appendRemoteLocked(proto.KindGame, res.Spans, l.Prompt, l.Ts, l.Seq)
	}
	for _, d := range s.deferred {
		s.appendLocked(d.kind, d.spans, false, 0)
	}
	s.deferred = nil
}

// appendRemoteLocked stores a daemon entry, remembering its daemon seq (for
// ack) and counting it unread only when it is newer than what this device
// already acknowledged. s.mu held.
func (s *session) appendRemoteLocked(kind string, spans []proto.Span, prompt bool, ts int64, rseq uint64) {
	r := s.remote
	s.noUnread = rseq <= r.ackedRemote
	seq := s.appendLocked(kind, spans, prompt, ts)
	s.noUnread = false
	if seq == 0 {
		return
	}
	r.pairs = append(r.pairs, seqPair{seq, rseq})
	if over := len(r.pairs) - s.e.opts.RingSize; over > 0 {
		r.pairs = append(r.pairs[:0], r.pairs[over:]...)
	}
}

// applyRemoteInfo merges the daemon's view of the session (state changes).
func (s *session) applyRemoteInfo(in proto.SessionInfo) {
	var after []func()
	s.mu.Lock()
	r := s.remote
	prev := s.info.State
	s.info.State, s.info.StartedAt, s.info.RemoteEcho, s.info.Attached = in.State, in.StartedAt, in.RemoteEcho, in.Attached
	if in.TF != nil {
		tf := *in.TF
		var old proto.TFInfo
		if s.info.TF != nil {
			old = *s.info.TF
		}
		if n := s.lossNoticeLocked(old, tf); n != "" {
			after = append(after, func() { s.publishLoss(n) })
		}
		if old.Input == "tf" && tf.Input != "tf" {
			after = append(after, s.armProbe) // the user took the keyboard: find out what game this is
		}
		s.info.TF = &tf
	}
	if in.State == proto.StateEnded {
		s.cap.abort()
		s.info.EndedAt, s.info.EndReason, s.info.EndDetail, s.info.LastText = in.EndedAt, in.EndReason, in.EndDetail, in.LastText
		s.info.RemoteEcho = false
		if s.loginCancel != nil {
			s.loginCancel()
			s.loginCancel = nil
		}
		s.login = ""
		select {
		case <-r.ended:
		default:
			close(r.ended)
		}
		if prev != proto.StateEnded {
			after = append(after, func() {
				s.e.tf.FireHookWorld(s.tfName, "DISCONNECT", s.world.Name)
				s.e.lua.FireEventFor(s.id, "disconnect", map[string]any{"world": s.id, "name": s.tfName, "reason": in.EndReason}, true)
			})
		}
	}
	if prev == proto.StateConnecting && in.State == proto.StateConnected {
		after = append(after, s.loginAfterConnectLocked()...)
		after = append(after, func() {
			s.e.tf.FireHookWorld(s.tfName, "CONNECT", s.world.Name)
			s.e.lua.FireEventFor(s.id, "connect", map[string]any{"world": s.id, "name": s.tfName}, true)
		})
	}
	s.publishInfoLocked()
	s.mu.Unlock()
	for _, f := range after {
		f()
	}
}

// loginAfterConnectLocked returns the actions that send the pending login
// line (if any) once the game connection is up. The line is sent by the
// CLIENT through the daemon's `send`; the daemon stores no credentials. s.mu held.
func (s *session) loginAfterConnectLocked() []func() {
	if s.login == "" {
		return nil
	}
	send := func() {
		s.mu.Lock()
		l := s.login
		s.login = "" // the password does not outlive the login line
		s.loginCancel = nil
		s.mu.Unlock()
		if l != "" {
			if err := sendLogin(s.tr, l); err != nil {
				s.systemLine("% login not sent: " + err.Error())
			}
		}
	}
	if s.world.Login != nil && s.world.Login.Delay > 0 {
		s.loginCancel = s.e.schedule(s.world.Login.Delay, send)
		return nil
	}
	return []func(){send}
}

// armLogin sets the login line for a session created by Dial; when the game
// connection is already up it is sent at once.
func (s *session) armLogin(w World, login string) {
	var after []func()
	s.mu.Lock()
	s.world = w
	s.login = login
	if s.info.State == proto.StateConnected {
		after = s.loginAfterConnectLocked()
	}
	s.mu.Unlock()
	for _, f := range after {
		f()
	}
}

// echoWithOrigin marks echo lines typed by someone else: "[tf] " for TinyFugue's
// own keyboard, "[remote] " for another device. Our own typing is shown as it was.
func (s *session) echoWithOrigin(spans []proto.Span, origin string) []proto.Span {
	if origin == "" || !s.remote.sq.isTF() || origin == s.remote.sq.deviceIDValue() {
		return spans
	}
	tag := "[remote] "
	if origin == "tf" {
		tag = "[tf] "
	}
	return append([]proto.Span{{S: tag, Dim: true}}, spans...)
}

// ownsAutomation reports whether TinyFugue hands trigger execution for this
// session to THIS client (automation=client and we are the device that set it).
func (s *session) ownsAutomation() bool {
	s.mu.Lock()
	var t proto.TFInfo
	if s.info.TF != nil {
		t = *s.info.TF
	}
	s.mu.Unlock()
	return t.Automation == "client" && t.ControllerDevice != "" && t.ControllerDevice == s.remote.sq.deviceIDValue()
}
