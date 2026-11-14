package tfbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// Config configures a Bridge.
type Config struct {
	DataDir     string
	Logger      *slog.Logger
	Auth        *auth.Store
	Fingerprint string        // TLS fingerprint shown with pairing codes
	Hint        string        // "wss://host:port/tfsp" shown with pairing codes
	PairTTL     time.Duration // default 10 minutes
	RingSize    int           // scrollback entries per session (default 50000)
	Version     string
	ReqTimeout  time.Duration // how long a request to TinyFugue may take (default 5s)
}

// Bridge implements wire.Backend, wire.ExtBackend and wire.SecretSender on top
// of a TFCTL connection to one TinyFugue.
type Bridge struct {
	cfg Config
	log *slog.Logger
	ctl *ctl

	ready     chan struct{}
	readyOnce sync.Once
	done      chan struct{}

	mu       sync.Mutex
	instance string
	tfPID    string
	tfFeat   []string // from TinyFugue's HELLO
	sessions map[string]*session
	peers    map[uint64]wire.Peer
	pending  map[string]chan []string
	reqSeq   atomic.Uint64
	worldAcc []proto.TFWorld

	worldsMu sync.Mutex // one world.list request at a time
}

var (
	_ wire.Backend      = (*Bridge)(nil)
	_ wire.ExtBackend   = (*Bridge)(nil)
	_ wire.SecretSender = (*Bridge)(nil)
)

// New returns a Bridge talking TFCTL over conn. Old session logs from a
// previous run are removed: TinyFugue no longer has those connections.
func New(cfg Config, conn net.Conn) (*Bridge, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("tfbridge: DataDir required")
	}
	if cfg.Auth == nil {
		return nil, errors.New("tfbridge: Auth required")
	}
	if cfg.PairTTL <= 0 {
		cfg.PairTTL = auth.PairCodeTTL
	}
	if cfg.ReqTimeout <= 0 {
		cfg.ReqTimeout = 5 * time.Second
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	sdir := filepath.Join(cfg.DataDir, "sessions")
	if err := os.RemoveAll(sdir); err != nil {
		return nil, fmt.Errorf("tfbridge: %w", err)
	}
	if err := os.MkdirAll(sdir, 0o700); err != nil {
		return nil, fmt.Errorf("tfbridge: %w", err)
	}
	return &Bridge{cfg: cfg, log: log, ctl: &ctl{conn: conn}, ready: make(chan struct{}), done: make(chan struct{}),
		sessions: map[string]*session{}, peers: map[uint64]wire.Peer{}, pending: map[string]chan []string{}}, nil
}

// Run reads TinyFugue's messages until the channel closes. It returns nil on
// a clean close (TinyFugue exited or /squat off).
func (b *Bridge) Run() error {
	err := b.ctl.readLoop(b.handle)
	close(b.done)
	b.mu.Lock()
	for _, s := range b.sessions {
		s.close()
	}
	b.mu.Unlock()
	return err
}

// Done is closed when the control channel has ended.
func (b *Bridge) Done() <-chan struct{} { return b.done }

// WaitReady blocks until TinyFugue's HELLO arrived and returns its instance id.
func (b *Bridge) WaitReady(ctx context.Context) (string, error) {
	select {
	case <-b.ready:
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.instance, nil
	case <-b.done:
		return "", errors.New("tfbridge: TinyFugue closed the channel before HELLO")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Features returns what TinyFugue announced in its HELLO ("gmcp", "exec", ...).
func (b *Bridge) Features() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.tfFeat...)
}

// Close drops the control channel.
func (b *Bridge) Close() { _ = b.ctl.conn.Close() }

func (b *Bridge) peerList() []wire.Peer {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]wire.Peer, 0, len(b.peers))
	for _, p := range b.peers {
		out = append(out, p)
	}
	return out
}

func (b *Bridge) session(id string) *session {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessions[id]
}

func (b *Bridge) sessionList() []*session {
	b.mu.Lock()
	out := make([]*session, 0, len(b.sessions))
	for _, s := range b.sessions {
		out = append(out, s)
	}
	b.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].startedAt < out[j].startedAt || (out[i].startedAt == out[j].startedAt && out[i].id < out[j].id)
	})
	return out
}

// ---------------------------------------------------------------- TinyFugue -> bridge

func atoi64(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }

func field(m []string, i int) string {
	if i < len(m) {
		return m[i]
	}
	return ""
}

func (b *Bridge) handle(m []string) {
	if b.log.Enabled(context.Background(), slog.LevelDebug) {
		short := m
		if len(short) > 6 {
			short = short[:6]
		}
		b.log.Debug("tfctl <-", "msg", fmt.Sprintf("%q", short))
	}
	switch m[0] {
	case "HELLO":
		b.mu.Lock()
		b.instance, b.tfPID = field(m, 3), field(m, 4)
		b.tfFeat = nil
		for _, f := range strings.Split(field(m, 5), ",") {
			if f = strings.TrimSpace(f); f != "" {
				b.tfFeat = append(b.tfFeat, f)
			}
		}
		b.mu.Unlock()
		_ = b.ctl.send("HELLO", "1", "bridge", b.cfg.Version)
		b.readyOnce.Do(func() { close(b.ready) })
	case "SESS":
		b.onSess(m)
	case "SESSX":
		b.removeSession(field(m, 1), true)
	case "LINE":
		// LINE sid kind flags origin ts text
		if s := b.session(field(m, 1)); s != nil && len(m) >= 7 {
			s.addLine(m[2], m[3], m[4], atoi64(m[5]), m[6], field(m, 7))
		}
	case "GMCP":
		// GMCP sid package json
		if s := b.session(field(m, 1)); s != nil && len(m) >= 3 {
			s.addGmcp(m[2], field(m, 3), 0)
		}
	case "MSDP":
		// MSDP sid payload (the raw bytes between IAC SB MSDP and IAC SE)
		if s := b.session(field(m, 1)); s != nil && len(m) >= 3 {
			if err := s.addMsdp(m[2], 0); err != nil {
				b.log.Debug("tfbridge: undecodable MSDP from the game", "err", err)
			}
		}
	case "ASK":
		// ASK sid axis mode device: TinyFugue is waiting for its user to allow a takeover
		msg := wire.ControlPendingMsg{V: proto.Version, T: wire.TControlPending, Session: field(m, 1), Axis: field(m, 2),
			Mode: field(m, 3), DeviceName: field(m, 4)}
		for _, p := range b.peerList() {
			p.Send(msg)
		}
	case "CTRL":
		// CTRL sid input automation device
		if s := b.session(field(m, 1)); s != nil && len(m) >= 4 {
			if s.setControl(m[2], m[3]) {
				msg := s.controlMsg("")
				for _, p := range b.peerList() {
					p.Send(msg)
				}
			}
		}
	case "WCHG":
		// the world list changed in TinyFugue (its keyboard, a macro file, any client): clients re-list
		b.broadcastWorldChanged(wire.WorldChangedMsg{V: proto.Version, T: wire.TWorldChanged})
	case "WORLD":
		b.onWorld(m)
	case "OK", "ERR":
		b.mu.Lock()
		ch := b.pending[field(m, 1)]
		b.mu.Unlock()
		if ch != nil {
			select {
			case ch <- m:
			default:
			}
		}
	case "PAIR":
		b.cmdPair(field(m, 1))
	case "DEVICES":
		b.cmdDevices()
	case "REVOKE":
		b.cmdRevoke(field(m, 1))
	case "INFO":
		b.cmdInfo()
	default:
		b.log.Debug("tfbridge: ignoring unknown message", "type", m[0])
	}
}

func (b *Bridge) onSess(m []string) {
	// SESS sid world host port tls state remoteEcho fg startedMs reason
	if len(m) < 10 {
		return
	}
	id := m[1]
	b.mu.Lock()
	s := b.sessions[id]
	b.mu.Unlock()
	created := false
	if s == nil {
		ns, err := newSession(b, id)
		if err != nil {
			b.log.Error("tfbridge: cannot create session", "id", id, "err", err)
			return
		}
		s = ns
		created = true
		b.mu.Lock()
		b.sessions[id] = s
		b.mu.Unlock()
	}
	state := m[6]
	if state != proto.StateConnecting && state != proto.StateConnected && state != proto.StateEnded {
		state = proto.StateEnded
	}
	s.update(m[2], m[3], m[4], m[5] == "1", state, m[7] == "1", m[8] == "1", atoi64(m[9]), field(m, 10))
	if created {
		s.mu.Lock()
		s.broadcastLocked()
		s.mu.Unlock()
	}
}

func (b *Bridge) removeSession(id string, notify bool) {
	b.mu.Lock()
	s := b.sessions[id]
	delete(b.sessions, id)
	b.mu.Unlock()
	if s == nil {
		return
	}
	s.close()
	if notify {
		for _, p := range b.peerList() {
			p.Send(wire.RemovedMsg{V: proto.Version, T: wire.TSessionRemoved, Session: id})
		}
	}
}

func (b *Bridge) onWorld(m []string) {
	// WORLD name host port type char passSet ssl echo charset mfile temp
	if len(m) < 12 {
		return
	}
	w := proto.TFWorld{Name: m[1], Host: m[2], Port: m[3], Type: m[4], Char: m[5], PassSet: m[6] == "1", TLS: m[7] == "1",
		Echo: m[8] == "1", Charset: m[9], MFile: m[10], Temp: m[11] == "1"}
	b.mu.Lock()
	b.worldAcc = append(b.worldAcc, w)
	b.mu.Unlock()
}

// ---------------------------------------------------------------- bridge -> TinyFugue

// request sends a message with a fresh request id as its second field and waits
// for TinyFugue's OK (returned) or ERR (returned as *wire.ProtoError).
func (b *Bridge) request(fields ...string) ([]string, error) {
	return b.requestT(b.cfg.ReqTimeout, fields...)
}

// requestT is request with its own timeout (some answers wait for a person).
func (b *Bridge) requestT(timeout time.Duration, fields ...string) ([]string, error) {
	id := "r" + strconv.FormatUint(b.reqSeq.Add(1), 10)
	ch := make(chan []string, 1)
	b.mu.Lock()
	b.pending[id] = ch
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()
	msg := append([]string{fields[0], id}, fields[1:]...)
	if err := b.ctl.send(msg...); err != nil {
		return nil, wire.Errf(proto.ErrInternal, "TinyFugue is not reachable")
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case m := <-ch:
		if m[0] == "ERR" {
			return nil, wire.Errf(field(m, 2), field(m, 3))
		}
		return m, nil
	case <-t.C:
		return nil, wire.Errf(proto.ErrInternal, "TinyFugue did not answer")
	case <-b.done:
		return nil, wire.Errf(proto.ErrInternal, "TinyFugue has exited")
	}
}

func (b *Bridge) notice(text string) { _ = b.ctl.send("NOTICE", text) }

func (b *Bridge) cmdPair(role string) {
	switch role {
	case "":
		role = wire.RoleOperator
	case wire.RoleViewer, wire.RoleOperator, wire.RoleAdmin:
	default:
		b.notice("unknown role " + role + " (use viewer, operator or admin)")
		return
	}
	code, exp, err := b.cfg.Auth.NewPairingRole(b.cfg.PairTTL, role)
	if err != nil {
		b.notice("could not create a pairing code: " + err.Error())
		return
	}
	hint := b.cfg.Hint
	if role != wire.RoleOperator {
		hint += "   (role: " + role + ")"
	}
	_ = b.ctl.send("PAIRCODE", code, fmt.Sprintf("%s (%s, single use)", exp.Format(time.RFC1123), time.Until(exp).Round(time.Minute)),
		b.cfg.Fingerprint, hint)
}

func (b *Bridge) cmdDevices() {
	devs, err := b.cfg.Auth.Devices()
	if err != nil {
		b.notice("cannot read devices: " + err.Error())
		return
	}
	if len(devs) == 0 {
		b.notice("no paired devices (use /squat pair)")
		return
	}
	for _, d := range devs {
		last := "never"
		if d.LastSeen > 0 {
			last = time.UnixMilli(d.LastSeen).Format("2006-01-02 15:04")
		}
		role := d.Role
		if role == "" {
			role = "owner"
		}
		_ = b.ctl.send("DEVICE", d.DeviceID, d.Name, role, last)
	}
}

func (b *Bridge) cmdRevoke(id string) {
	if strings.TrimSpace(id) == "" {
		b.notice("usage: /squat revoke <deviceId>")
		return
	}
	if err := b.cfg.Auth.Revoke(id); err != nil {
		b.notice("revoke failed: " + err.Error())
		return
	}
	b.notice("revoked " + id + " (an already-connected client stays connected until it disconnects)")
}

func (b *Bridge) cmdInfo() {
	b.mu.Lock()
	n, np := len(b.sessions), len(b.peers)
	b.mu.Unlock()
	devs, _ := b.cfg.Auth.Devices()
	b.notice("listening at " + b.cfg.Hint)
	b.notice("TLS fingerprint " + b.cfg.Fingerprint)
	b.notice(fmt.Sprintf("%d paired device(s), %d client(s) connected, %d session(s)", len(devs), np, n))
}
