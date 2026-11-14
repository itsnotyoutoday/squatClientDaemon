package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/conn"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/scrollback"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// Config configures a Daemon.
type Config struct {
	DataDir     string
	Logger      *slog.Logger
	MaxSessions int // default 64
	RingSize    int // scrollback ring entries per session (default 50000)
	FlushEvery  time.Duration

	// ConnTweak, if set, may adjust each game connection's config (tests).
	ConnTweak func(*conn.Config)

	// Handoff, if set, makes this Daemon the continuation of one that called PrepareHandoff (squatterd
	// reload, see handoff.go): the sessions in it are adopted instead of being marked stopped.
	Handoff *Handoff
}

// Daemon is the session registry. It implements wire.Backend.
type Daemon struct {
	cfg Config
	log *slog.Logger
	ctx context.Context
	end context.CancelFunc
	wg  sync.WaitGroup // session pumps + flusher

	smu      sync.RWMutex
	sessions map[string]*session
	closing  bool

	pmu   sync.RWMutex
	peers map[uint64]wire.Peer
}

var _ wire.Backend = (*Daemon)(nil)

// New opens (creating if needed) the data dir and loads persisted sessions.
// Sessions that were not ended are loaded as ended/squatter_stopped.
func New(cfg Config) (*Daemon, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("daemon: DataDir required")
	}
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = 64
	}
	if cfg.FlushEvery <= 0 {
		cfg.FlushEvery = 2 * time.Second
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if err := os.MkdirAll(sessionsDir(cfg.DataDir), 0o700); err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}
	ctx, end := context.WithCancel(context.Background())
	d := &Daemon{cfg: cfg, log: log, ctx: ctx, end: end, sessions: map[string]*session{}, peers: map[uint64]wire.Peer{}}
	if err := d.load(); err != nil {
		end()
		return nil, err
	}
	d.wg.Add(1)
	go d.flushLoop()
	return d, nil
}

func (d *Daemon) load() error {
	ents, err := os.ReadDir(sessionsDir(d.cfg.DataDir))
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(sessionsDir(d.cfg.DataDir), e.Name())
		m, err := readMeta(dir)
		if err != nil {
			d.log.Warn("daemon: skipping unreadable session", "dir", dir, "err", err)
			continue
		}
		s, err := d.openSession(dir, m)
		if err != nil {
			d.log.Warn("daemon: cannot open session", "id", m.ID, "err", err)
			continue
		}
		hs, carried := d.handoffFor(m.ID)
		switch {
		case m.State == proto.StateEnded:
			close(s.done)
		case carried:
			if err := d.adopt(s, hs, d.cfg.Handoff.Files[hs.File]); err != nil {
				d.log.Warn("daemon: cannot adopt connection", "id", m.ID, "err", err)
				s.markEndedAtLoad(proto.EndSquatterRestarted, "could not take over the connection: "+err.Error())
			}
		default:
			s.markStoppedAtLoad()
		}
		d.sessions[m.ID] = s
	}
	return nil
}

// handoffFor finds the session id in the handoff this Daemon was started with.
func (d *Daemon) handoffFor(id string) (HandoffSession, bool) {
	if d.cfg.Handoff == nil {
		return HandoffSession{}, false
	}
	for _, hs := range d.cfg.Handoff.Bundle.Sessions {
		if hs.ID == id && hs.File >= 0 && hs.File < len(d.cfg.Handoff.Files) {
			return hs, true
		}
	}
	return HandoffSession{}, false
}

func (d *Daemon) openSession(dir string, m *meta) (*session, error) {
	st, err := scrollback.New(scrollback.Options{RingSize: d.cfg.RingSize, LogDir: dir})
	if err != nil {
		return nil, err
	}
	if m.Acks == nil {
		m.Acks = map[string]uint64{}
	}
	return &session{d: d, dir: dir, m: m, store: st, attached: map[uint64]wire.Peer{}, done: make(chan struct{})}, nil
}

func (d *Daemon) flushLoop() {
	defer d.wg.Done()
	t := time.NewTicker(d.cfg.FlushEvery)
	defer t.Stop()
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-t.C:
			for _, s := range d.list() {
				s.flushIfDirty()
			}
		}
	}
}

func (d *Daemon) list() []*session {
	d.smu.RLock()
	defer d.smu.RUnlock()
	out := make([]*session, 0, len(d.sessions))
	for _, s := range d.sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].m.StartedAt < out[j].m.StartedAt || (out[i].m.StartedAt == out[j].m.StartedAt && out[i].m.ID < out[j].m.ID)
	})
	return out
}

func (d *Daemon) get(id string) (*session, error) {
	d.smu.RLock()
	s := d.sessions[id]
	d.smu.RUnlock()
	if s == nil {
		return nil, wire.Errf(proto.ErrNoSuchSession, "no such session: "+id)
	}
	return s, nil
}

func (d *Daemon) peerList() []wire.Peer {
	d.pmu.RLock()
	defer d.pmu.RUnlock()
	out := make([]wire.Peer, 0, len(d.peers))
	for _, p := range d.peers {
		out = append(out, p)
	}
	return out
}

// Sessions returns the session list as seen by deviceID (unread is per device).
func (d *Daemon) Sessions(deviceID string) []proto.SessionInfo {
	out := []proto.SessionInfo{}
	for _, s := range d.list() {
		out = append(out, s.info(deviceID))
	}
	return out
}

// Shutdown ends every live session as squatter_stopped, flushes and closes all
// scrollback stores. Connected SWP clients receive the final session.state
// messages before it returns (close the wire server afterwards).
func (d *Daemon) Shutdown(ctx context.Context) error {
	d.smu.Lock()
	if d.closing {
		d.smu.Unlock()
		return nil
	}
	d.closing = true
	d.smu.Unlock()
	all := d.list()
	for _, s := range all {
		s.stop()
	}
	var firstErr error
	for _, s := range all {
		select {
		case <-s.done:
		case <-ctx.Done():
			firstErr = ctx.Err()
		}
	}
	d.end()
	d.wg.Wait()
	for _, s := range all {
		if err := s.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func newID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return "s-" + hex.EncodeToString(b[:])
}

// ---------------------------------------------------------------- wire.Backend

// PeerJoined implements wire.Backend.
func (d *Daemon) PeerJoined(p wire.Peer) []proto.SessionInfo {
	d.pmu.Lock()
	d.peers[p.ID()] = p
	d.pmu.Unlock()
	return d.Sessions(p.DeviceID())
}

// PeerLeft implements wire.Backend.
func (d *Daemon) PeerLeft(p wire.Peer) {
	d.pmu.Lock()
	delete(d.peers, p.ID())
	d.pmu.Unlock()
	for _, s := range d.list() {
		s.detach(p)
	}
}

// List implements wire.Backend.
func (d *Daemon) List(p wire.Peer) []proto.SessionInfo { return d.Sessions(p.DeviceID()) }

// Connect implements wire.Backend.
func (d *Daemon) Connect(_ context.Context, p wire.Peer, req wire.ConnectReq) (proto.SessionInfo, error) {
	w := req.World
	if w.Name == "" {
		w.Name = fmt.Sprintf("%s:%d", w.Host, w.Port)
	}
	d.smu.Lock()
	if d.closing {
		d.smu.Unlock()
		return proto.SessionInfo{}, ErrHandingOff
	}
	if len(d.sessions) >= d.cfg.MaxSessions {
		d.smu.Unlock()
		return proto.SessionInfo{}, wire.Errf(proto.ErrBadRequest, fmt.Sprintf("session limit (%d) reached: dismiss ended sessions first", d.cfg.MaxSessions))
	}
	id := newID()
	for d.sessions[id] != nil {
		id = newID()
	}
	dir := filepath.Join(sessionsDir(d.cfg.DataDir), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		d.smu.Unlock()
		return proto.SessionInfo{}, fmt.Errorf("daemon: %w", err)
	}
	m := &meta{ID: id, Name: w.Name, Host: w.Host, Port: w.Port, TLS: w.TLS, TLSVerify: w.TLSVerify, Encoding: w.Encoding,
		State: proto.StateConnecting, StartedAt: time.Now().UnixMilli()}
	s, err := d.openSession(dir, m)
	if err != nil {
		d.smu.Unlock()
		_ = os.RemoveAll(dir)
		return proto.SessionInfo{}, fmt.Errorf("daemon: %w", err)
	}
	s.caps = p.Caps()
	cc := conn.Config{World: w, Caps: s.caps, Proxied: true}
	if d.cfg.ConnTweak != nil {
		d.cfg.ConnTweak(&cc)
	}
	c, err := conn.Open(d.ctx, cc)
	if err != nil {
		d.smu.Unlock()
		_ = s.store.Close()
		_ = os.RemoveAll(dir)
		return proto.SessionInfo{}, wire.Errf(proto.ErrBadRequest, err.Error())
	}
	s.c = c
	_ = writeMeta(dir, m)
	d.sessions[id] = s
	d.wg.Add(1)
	d.smu.Unlock()
	go s.pump()
	s.mu.Lock()
	s.broadcastLocked()
	info := s.infoLocked(p.DeviceID())
	s.mu.Unlock()
	d.log.Info("daemon: session created", "id", id, "host", w.Host, "port", w.Port)
	return info, nil
}

// Disconnect implements wire.Backend.
func (d *Daemon) Disconnect(_ wire.Peer, id string) error {
	s, err := d.get(id)
	if err != nil {
		return err
	}
	return s.disconnect()
}

// Dismiss implements wire.Backend: removes an ended session and its files.
func (d *Daemon) Dismiss(_ wire.Peer, id string) error {
	s, err := d.get(id)
	if err != nil {
		return err
	}
	if err := s.canDismiss(); err != nil {
		return err
	}
	d.smu.Lock()
	delete(d.sessions, id)
	d.smu.Unlock()
	if err := s.close(); err != nil {
		d.log.Warn("daemon: closing dismissed session", "id", id, "err", err)
	}
	if err := os.RemoveAll(s.dir); err != nil {
		d.log.Warn("daemon: removing session dir", "id", id, "err", err)
	}
	msg := wire.RemovedMsg{V: proto.Version, T: wire.TSessionRemoved, Session: id}
	for _, p := range d.peerList() {
		p.Send(msg)
	}
	return nil
}

// Attach implements wire.Backend.
func (d *Daemon) Attach(p wire.Peer, req wire.AttachReq) error {
	s, err := d.get(req.Session)
	if err != nil {
		return err
	}
	return s.attach(p, req)
}

// Detach implements wire.Backend.
func (d *Daemon) Detach(p wire.Peer, id string) error {
	s, err := d.get(id)
	if err != nil {
		return err
	}
	s.detach(p)
	return nil
}

// Send implements wire.Backend.
func (d *Daemon) Send(p wire.Peer, id, text string) error {
	s, err := d.get(id)
	if err != nil {
		return err
	}
	return s.sendText(text, true)
}

// SendSecret implements wire.SecretSender: the text reaches the game but is never
// echoed to clients, stored in the scrollback or written to the log.
func (d *Daemon) SendSecret(p wire.Peer, id, text string) error {
	s, err := d.get(id)
	if err != nil {
		return err
	}
	return s.sendText(text, false)
}

// Ack implements wire.Backend.
func (d *Daemon) Ack(p wire.Peer, id string, seq uint64) error {
	s, err := d.get(id)
	if err != nil {
		return err
	}
	s.ack(p.DeviceID(), seq)
	return nil
}

// Resize implements wire.Backend: the most recent resize from any client wins.
func (d *Daemon) Resize(_ wire.Peer, id string, cols, rows int) error {
	s, err := d.get(id)
	if err != nil {
		return err
	}
	s.resize(cols, rows)
	return nil
}

// CapsChanged implements wire.Backend.
func (d *Daemon) CapsChanged(p wire.Peer) {
	for _, s := range d.list() {
		s.capsChanged(p)
	}
}
