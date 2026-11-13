package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/ansi"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/conn"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/scrollback"
)

// Transport is what a session needs from a connection. *conn.Conn implements
// it (direct mode).
//
// Remote sessions (held by a squatterd, see remote.go and squatter.go) use a
// small Transport implementation for the control calls only (Send, Resize,
// Close = ask the daemon to end the game connection); their output does not
// flow through Events: the squatter's wire event loop feeds the session
// directly so replayed history can be flagged live=false.
type Transport interface {
	Events() <-chan conn.Event
	Send(text string) error
	Resize(cols, rows int)
	SetCaps(proto.Caps)
	RemoteEcho() bool
	Close()
}

type deferredLine struct {
	kind  string
	spans []proto.Span
}

type session struct {
	e      *Engine
	id     string
	tfName string
	world  World
	cfg    conn.Config
	tr     Transport
	store  *scrollback.Store
	done   chan struct{}

	userSend atomic.Int32 // >0 while a user-typed line is being sent (it is echoed)

	mu          sync.Mutex
	info        proto.SessionInfo
	unread      []uint64
	collecting  bool
	deferred    []deferredLine
	login       string
	loginCancel func()
	status      []StatusField
	logw        *os.File
	cols, rows  int
	localEcho   bool
	limit       *regexp.Regexp
	storeClosed bool
	started     bool

	cap      capState     // Capture machinery (capture.go)
	props    propsCache   // flag/power catalogues (mushobj_props.go)
	remote   *remoteState // non-nil for sessions held by a squatter
	noUnread bool         // appendLocked: do not count the next game line as unread
}

func newSession(e *Engine, id, tfName string, w World, cfg conn.Config, login string) (*session, error) {
	so := scrollback.Options{RingSize: e.opts.RingSize}
	if e.opts.SessionLogDir != "" {
		so.LogDir = filepath.Join(e.opts.SessionLogDir, fmt.Sprintf("%s-%d", id, e.now().Unix()))
		if err := os.MkdirAll(so.LogDir, 0o700); err != nil {
			return nil, fmt.Errorf("engine: %w", err)
		}
	}
	st, err := scrollback.New(so)
	if err != nil {
		return nil, fmt.Errorf("engine: %w", err)
	}
	s := &session{e: e, id: id, tfName: tfName, world: w, cfg: cfg, store: st, done: make(chan struct{}),
		login: login, localEcho: true, cols: 80, rows: 24}
	s.info = proto.SessionInfo{ID: id, Name: w.Name, Host: w.Host, Port: w.Port, TLS: w.TLS,
		State: proto.StateConnecting, StartedAt: e.now().UnixMilli(), Attached: 1}
	return s, nil
}

func (s *session) start() error {
	dial := s.e.opts.Dialer
	if dial == nil {
		dial = func(ctx context.Context, cfg conn.Config) (Transport, error) { return conn.Open(ctx, cfg) }
	}
	tr, err := dial(s.e.ctx, s.cfg)
	if err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	s.tr = tr
	s.mu.Lock()
	s.started = true
	s.publishInfoLocked()
	s.mu.Unlock()
	go s.pump()
	return nil
}

// shutdown closes the transport and waits for the event pump to finish.
func (s *session) shutdown() {
	s.mu.Lock()
	started := s.started
	if s.loginCancel != nil {
		s.loginCancel()
	}
	s.mu.Unlock()
	s.cap.abort()
	if started {
		s.tr.Close()
		<-s.done
	}
	s.mu.Lock()
	if s.logw != nil {
		_ = s.logw.Close()
		s.logw = nil
	}
	s.mu.Unlock()
}

func (s *session) closeStore() {
	s.mu.Lock()
	if s.storeClosed {
		s.mu.Unlock()
		return
	}
	s.storeClosed = true
	s.mu.Unlock()
	_ = s.store.Close()
}

func (s *session) isEnded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info.State == proto.StateEnded
}

func (s *session) snapshot() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *session) snapshotLocked() SessionInfo {
	in := s.info
	in.FirstSeq, in.LastSeq = s.store.Bounds()
	in.Unread = uint64(len(s.unread))
	si := SessionInfo{SessionInfo: in, WorldID: s.world.ID, Via: "direct"}
	if s.remote != nil {
		si.Via, si.Link = s.remote.sq.id, s.remote.link
	}
	return si
}

func (s *session) publishInfoLocked() {
	si := s.snapshotLocked()
	s.e.bus.publish(Event{Kind: EvSession, Session: s.id, Info: &si})
}

// ---- pump --------------------------------------------------------------

func (s *session) pump() {
	defer close(s.done)
	for ev := range s.tr.Events() {
		switch ev.Kind {
		case conn.EvLine:
			s.onLine(ev.Spans, ev.Prompt)
		case conn.EvGmcp:
			s.onGmcp(ev.Package, ev.Data)
		case conn.EvState:
			if ev.Connected {
				s.onConnected()
			}
		case conn.EvRemoteEcho:
			s.mu.Lock()
			s.info.RemoteEcho = ev.RemoteEcho
			s.publishInfoLocked()
			s.mu.Unlock()
		case conn.EvEnded:
			s.onEnded(ev)
		}
	}
}

func (s *session) onLine(spans []proto.Span, prompt bool) {
	if spans == nil {
		spans = []proto.Span{}
	}
	line := proto.Line{V: proto.Version, T: proto.TLine, Session: s.id, Ts: s.e.now().UnixMilli(),
		Kind: proto.KindGame, Spans: spans, Prompt: prompt}
	if s.cap.offer(line) {
		return // diverted by Capture: not shown, stored or trigger-processed
	}
	s.mu.Lock()
	s.collecting = true
	s.mu.Unlock()
	res := s.e.runPipeline(s, line, true)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collecting = false
	if !res.Gag {
		s.appendLocked(proto.KindGame, res.Spans, prompt, line.Ts)
	}
	for _, d := range s.deferred {
		s.appendLocked(d.kind, d.spans, false, 0)
	}
	s.deferred = nil
}

func (s *session) onGmcp(pkg string, data json.RawMessage) {
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	msg := proto.GmcpMsg{V: proto.Version, T: proto.TGmcp, Session: s.id, Ts: s.e.now().UnixMilli(), Package: pkg, Data: data}
	var dec any
	_ = json.Unmarshal(data, &dec)
	s.e.lua.FireEventFor(s.id, "gmcp", map[string]any{"package": pkg, "data": dec, "raw": string(data), "world": s.id}, true)
	s.e.bus.publish(Event{Kind: EvGmcp, Session: s.id, Gmcp: &msg})
}

func (s *session) onConnected() {
	s.mu.Lock()
	s.info.State = proto.StateConnected
	s.appendLocked(proto.KindSystem, []proto.Span{{S: fmt.Sprintf("*** Connected to %s:%d", s.world.Host, s.world.Port)}}, false, 0)
	login := s.login
	if login != "" {
		delay := time.Duration(0)
		if s.world.Login != nil {
			delay = s.world.Login.Delay
		}
		send := func() {
			s.mu.Lock()
			l := s.login
			s.login = "" // the password does not outlive the login line
			s.loginCancel = nil
			s.mu.Unlock()
			if l != "" {
				_ = sendLogin(s.tr, l)
			}
		}
		if delay > 0 {
			s.loginCancel = s.e.schedule(delay, send)
		} else {
			defer send()
		}
	}
	s.mu.Unlock()
	s.e.tf.FireHookWorld(s.tfName, "CONNECT", s.world.Name)
	s.e.lua.FireEventFor(s.id, "connect", map[string]any{"world": s.id, "name": s.tfName}, true)
}

func (s *session) onEnded(ev conn.Event) {
	s.cap.abort()
	s.mu.Lock()
	if s.loginCancel != nil {
		s.loginCancel()
		s.loginCancel = nil
	}
	s.login = ""
	s.info.State = proto.StateEnded
	s.info.EndedAt = s.e.now().UnixMilli()
	s.info.EndReason, s.info.EndDetail, s.info.LastText = ev.Reason, ev.Detail, ev.LastText
	s.info.RemoteEcho = false
	msg := "*** Connection ended: " + ev.Reason
	if ev.Detail != "" {
		msg += " (" + ev.Detail + ")"
	}
	s.appendLocked(proto.KindSystem, []proto.Span{{S: msg}}, false, 0)
	if s.logw != nil {
		_ = s.logw.Close()
		s.logw = nil
	}
	s.mu.Unlock()
	s.e.tf.FireHookWorld(s.tfName, "DISCONNECT", s.world.Name)
	s.e.lua.FireEventFor(s.id, "disconnect", map[string]any{"world": s.id, "name": s.tfName, "reason": ev.Reason}, true)
}

// ---- output ------------------------------------------------------------

// appendLocked stores a line, publishes it and updates counters. s.mu held.
func (s *session) appendLocked(kind string, spans []proto.Span, prompt bool, ts int64) uint64 {
	if spans == nil {
		spans = []proto.Span{}
	}
	if ts == 0 {
		ts = s.e.now().UnixMilli()
	}
	l := proto.Line{V: proto.Version, T: proto.TLine, Session: s.id, Ts: ts, Kind: kind, Spans: spans, Prompt: prompt}
	seq, err := s.store.Append(scrollback.LineEntry(l))
	if err != nil {
		s.e.log.Warn("scrollback append failed", "session", s.id, "err", err)
		return 0
	}
	l.Seq = seq
	if kind == proto.KindGame && !prompt && !s.noUnread {
		s.unread = append(s.unread, seq)
		if over := len(s.unread) - s.e.opts.RingSize; over > 0 {
			s.unread = append(s.unread[:0], s.unread[over:]...)
		}
	}
	if s.logw != nil && !prompt {
		_, _ = fmt.Fprintln(s.logw, ansi.PlainText(spans))
	}
	if s.limit == nil || s.limit.MatchString(l.Plain()) {
		s.e.bus.publish(Event{Kind: EvLine, Session: s.id, Line: &l})
	}
	s.publishInfoLocked()
	return seq
}

// addLine adds a locally generated line; while the session's own line is
// being run through the scripts it is deferred until after that line.
func (s *session) addLine(kind string, spans []proto.Span) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.collecting {
		s.deferred = append(s.deferred, deferredLine{kind, spans})
		return
	}
	s.appendLocked(kind, spans, false, 0)
}

func (s *session) systemLine(text string) {
	s.addLine(proto.KindSystem, []proto.Span{{S: text}})
}

// ---- input -------------------------------------------------------------

// sendText sends one line to the game. echo shows it as an echo line unless
// the server asked for no echo (password prompt) or /localecho is off.
func (s *session) sendText(text string, echo bool) error {
	masked, secret := secretLine(text)
	return s.sendLine(text, masked, secret, echo)
}

// sendSecretText sends text that must never be recorded or echoed anywhere
// (the /secret command): the daemon does not store it and the display shows a mask.
func (s *session) sendSecretText(text string) error {
	return s.sendLine(text, secretMask, true, true)
}

// sendLine is the common send path. For a secret line the far end is told not to
// record it (squatterd noEcho), and only the masked text is ever shown locally.
func (s *session) sendLine(text, shown string, secret, echo bool) error {
	s.mu.Lock()
	state, le := s.info.State, s.localEcho
	s.mu.Unlock()
	switch state {
	case proto.StateEnded:
		return conn.ErrClosed
	case proto.StateConnecting:
		return conn.ErrNotConnected
	}
	if s.remote != nil {
		if secret {
			// the daemon is told not to echo or record it, so show a masked local echo
			if echo && le {
				s.addLine(proto.KindEcho, []proto.Span{{S: shown}})
			}
		}
		// otherwise the daemon echoes what it is sent (to every attached client): no local echo
		return s.remote.send(s, text, secret)
	}
	if echo && le && !s.tr.RemoteEcho() {
		s.addLine(proto.KindEcho, []proto.Span{{S: shown}})
	}
	return s.tr.Send(text)
}

func (s *session) setSize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	s.mu.Lock()
	s.cols, s.rows = cols, rows
	if s.remote != nil {
		s.remote.sized = true
	}
	s.mu.Unlock()
	s.tr.Resize(cols, rows)
}

func (s *session) size() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

// ---- status bar, view filter, logging ------------------------------------

func (s *session) statusOp(op, field string, args []string) {
	s.mu.Lock()
	idx := -1
	for i, f := range s.status {
		if f.Name == field {
			idx = i
		}
	}
	switch op {
	case "status_rm":
		if idx >= 0 {
			s.status = append(s.status[:idx], s.status[idx+1:]...)
		}
	default:
		f := StatusField{Name: field}
		if len(args) > 0 {
			f.Attrs = args[0]
		}
		if idx >= 0 {
			f.Text = s.status[idx].Text
			s.status[idx] = f
		} else {
			s.status = append(s.status, f)
		}
	}
	s.publishStatusLocked()
	s.mu.Unlock()
}

func (s *session) setStatusText(name, text string) {
	s.mu.Lock()
	for i := range s.status {
		if s.status[i].Name == name {
			s.status[i].Text = text
			s.publishStatusLocked()
			s.mu.Unlock()
			return
		}
	}
	s.status = append(s.status, StatusField{Name: name, Text: text})
	s.publishStatusLocked()
	s.mu.Unlock()
}

func (s *session) publishStatusLocked() {
	f := append([]StatusField(nil), s.status...)
	s.e.bus.publish(Event{Kind: EvStatus, Session: s.id, Fields: f})
}

func (s *session) setLimit(re *regexp.Regexp) {
	s.mu.Lock()
	s.limit = re
	s.mu.Unlock()
	s.e.bus.publish(Event{Kind: EvReset, Session: s.id})
}

func (s *session) setLog(on bool, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logw != nil {
		_ = s.logw.Close()
		s.logw = nil
	}
	if !on {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	s.logw = f
	return nil
}

// secretSender is implemented by transports whose far end would otherwise record
// what we send (a squatterd): login lines go through it so the password is
// never echoed, stored or logged there.
type secretSender interface{ SendSecret(text string) error }

// sendLogin sends a login line, using the secret path when the transport has one.
func sendLogin(tr Transport, line string) error {
	if ss, ok := tr.(secretSender); ok {
		return ss.SendSecret(line)
	}
	return tr.Send(line)
}
