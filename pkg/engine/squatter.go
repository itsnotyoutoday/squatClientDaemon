package engine

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tlsutil"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// Errors of the squatter API.
var (
	ErrNoSquatter  = errors.New("engine: no such squatter")
	ErrSquatterDup = errors.New("engine: squatter id already in use")
)

// Squatter connection states (SquatterInfo.State).
const (
	SqIdle         = "idle"         // added, never connected
	SqConnecting   = "connecting"   //
	SqConnected    = "connected"    //
	SqDisconnected = "disconnected" // closed on purpose
	SqUnreachable  = "unreachable"  // handshake failed or the connection was lost
	SqRefused      = "refused"      // fingerprint mismatch, device not paired / revoked
)

// Problem codes (SquatterInfo.Problem).
const (
	ProblemUnreachable  = "unreachable"
	ProblemFingerprint  = "fingerprint_mismatch"
	ProblemAuth         = "auth_failed"
	ProblemRateLimited  = "rate_limited"
	ProblemConnectionLo = "connection_lost"
)

// SquatterConfig describes one squatterd. The device key lives in memory only
// (the app loads it from the OS keychain); nothing here is persisted by the engine.
type SquatterConfig struct {
	ID         string // no ':' (it namespaces session ids as "<id>:<sessionId>")
	Name       string
	Host       string
	Port       int
	URL        string // optional full URL override (tests); default wss://host:port/swp
	Key        ed25519.PrivateKey
	DeviceName string
	// Fingerprint pins the daemon's TLS certificate (AA:BB:...). Empty = trust
	// on first use; the app should always pass the confirmed fingerprint.
	Fingerprint string
	// PairCode is a one-time pairing code, sent in hello on the next connect
	// only and forgotten after a successful handshake.
	PairCode         string
	InsecureLoopback bool // allow ws:// to a loopback host (tests, local daemon)
	MaxBacklog       int  // entries requested per attach (default wire.DefaultBacklog)
}

// SquatterInfo is the UI-facing state of a squatter.
type SquatterInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	State       string `json:"state"`
	Problem     string `json:"problem,omitempty"`
	Message     string `json:"message,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"` // seen during the last handshake
	DeviceID    string `json:"deviceId,omitempty"`
	Server      string `json:"server,omitempty"`
	Sessions    int    `json:"sessions"`
	Suspended   bool   `json:"suspended,omitempty"`
}

// AwayItem is one entry of the "While you were away" summary.
type AwayItem struct {
	Session   string `json:"session"`
	Name      string `json:"name"`
	Squatter  string `json:"squatter"`
	Unread    uint64 `json:"unread"`
	Ended     bool   `json:"ended"`
	EndReason string `json:"endReason,omitempty"`
	EndedAt   int64  `json:"endedAt,omitempty"`
	LastText  string `json:"lastText,omitempty"`
}

type squatter struct {
	e  *Engine
	id string

	mu        sync.Mutex
	cfg       SquatterConfig
	state     string
	problem   string
	message   string
	seenFP    string
	deviceID  string
	server    string
	cl        *wire.Client
	loopDone  chan struct{}
	sessions  map[string]*session // by daemon session id
	pending   int                 // attaches awaiting their final backlog frame
	awayTimer func()              // cancels the away fallback timer
	closed    bool
}

func (sq *squatter) name() string {
	sq.mu.Lock()
	defer sq.mu.Unlock()
	return sq.cfg.Name
}

func (sq *squatter) client() *wire.Client {
	sq.mu.Lock()
	defer sq.mu.Unlock()
	return sq.cl
}

func (sq *squatter) infoLocked() SquatterInfo {
	sq.e.mu.RLock()
	susp := sq.e.suspended
	sq.e.mu.RUnlock()
	return SquatterInfo{ID: sq.id, Name: sq.cfg.Name, Host: sq.cfg.Host, Port: sq.cfg.Port, State: sq.state,
		Problem: sq.problem, Message: sq.message, Fingerprint: sq.seenFP, DeviceID: sq.deviceID,
		Server: sq.server, Sessions: len(sq.sessions), Suspended: susp}
}

func (sq *squatter) publish() {
	sq.mu.Lock()
	in := sq.infoLocked()
	sq.mu.Unlock()
	sq.e.bus.publish(Event{Kind: EvSquatter, Session: "", Squatter: &in})
}

// ---- engine API --------------------------------------------------------------

// AddSquatter registers a squatter. It performs no I/O: call ConnectSquatter.
func (e *Engine) AddSquatter(cfg SquatterConfig) error {
	if cfg.ID == "" || strings.Contains(cfg.ID, ":") {
		return errors.New("engine: squatter id must be non-empty and contain no ':'")
	}
	if len(cfg.Key) != ed25519.PrivateKeySize {
		return errors.New("engine: squatter device key is required")
	}
	if cfg.Name == "" {
		cfg.Name = cfg.ID
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrClosed
	}
	if e.squatters[cfg.ID] != nil {
		return ErrSquatterDup
	}
	e.squatters[cfg.ID] = &squatter{e: e, id: cfg.ID, cfg: cfg, state: SqIdle, sessions: map[string]*session{}}
	return nil
}

func (e *Engine) getSquatter(id string) *squatter {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.squatters[id]
}

// Squatters lists the registered squatters.
func (e *Engine) Squatters() []SquatterInfo {
	e.mu.RLock()
	sqs := make([]*squatter, 0, len(e.squatters))
	for _, q := range e.squatters {
		sqs = append(sqs, q)
	}
	e.mu.RUnlock()
	out := make([]SquatterInfo, 0, len(sqs))
	for _, q := range sqs {
		q.mu.Lock()
		out = append(out, q.infoLocked())
		q.mu.Unlock()
	}
	return out
}

// SquatterStatus returns one squatter's state.
func (e *Engine) SquatterStatus(id string) (SquatterInfo, error) {
	q := e.getSquatter(id)
	if q == nil {
		return SquatterInfo{}, ErrNoSquatter
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.infoLocked(), nil
}

// RemoveSquatter disconnects the squatter and forgets it and its mirrored
// sessions locally. The daemon keeps running its sessions.
func (e *Engine) RemoveSquatter(id string) error {
	e.mu.Lock()
	q := e.squatters[id]
	delete(e.squatters, id)
	e.mu.Unlock()
	if q == nil {
		return ErrNoSquatter
	}
	q.shutdown()
	q.mu.Lock()
	ss := make([]*session, 0, len(q.sessions))
	for _, s := range q.sessions {
		ss = append(ss, s)
	}
	q.sessions = map[string]*session{}
	q.mu.Unlock()
	for _, s := range ss {
		e.remove(s)
	}
	return nil
}

// ProbeSquatter performs only a TLS handshake with host:port and returns the
// server certificate fingerprint. No SWP frame and no pairing code is sent:
// use it to show the fingerprint to the user (trust on first use) before
// calling AddSquatter with the confirmed Fingerprint and the PairCode.
func ProbeSquatter(ctx context.Context, host string, port int) (string, error) {
	var seen string
	cfg := tlsutil.PinnedConfig("", func(fp string) { seen = fp })
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: cfg}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return "", fmt.Errorf("engine: probe %s:%d: %w", host, port, err)
	}
	_ = c.Close()
	if seen == "" {
		return "", errors.New("engine: the server presented no certificate")
	}
	return seen, nil
}

func (cfg SquatterConfig) url() string {
	if cfg.URL != "" {
		return cfg.URL
	}
	scheme := "wss"
	if cfg.InsecureLoopback {
		scheme = "ws"
	}
	return scheme + "://" + net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)) + "/swp"
}

// ConnectSquatter dials the squatter, authenticates, mirrors all of its
// sessions as engine sessions "<id>:<sessionId>" and attaches to them (with
// `since` = the last seq already held; replayed lines are flagged live=false).
// It never retries: on failure the state says why (unreachable / refused).
func (e *Engine) ConnectSquatter(ctx context.Context, id string) error {
	q := e.getSquatter(id)
	if q == nil {
		return ErrNoSquatter
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrClosed
	}
	if q.state == SqConnecting || q.state == SqConnected {
		q.mu.Unlock()
		return nil
	}
	q.state, q.problem, q.message = SqConnecting, "", ""
	cfg := q.cfg
	q.mu.Unlock()
	q.publish()

	c, err := wire.Dial(ctx, cfg.url(), wire.ClientOptions{Key: cfg.Key, PairCode: cfg.PairCode, DeviceName: cfg.DeviceName,
		ClientName: "SquatClient", Caps: e.getCaps(), PinnedFingerprint: cfg.Fingerprint, InsecureLoopback: cfg.InsecureLoopback})
	if err != nil {
		state, problem, msg := classifyDialErr(cfg, err)
		q.mu.Lock()
		q.state, q.problem, q.message = state, problem, msg
		q.mu.Unlock()
		q.publish()
		e.bus.publish(Event{Kind: EvNotice, Level: LevelError, Text: msg})
		q.setLinks(LinkUnreachable, "")
		return fmt.Errorf("squatter %s: %s: %w", cfg.Name, msg, err)
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		_ = c.Close()
		return ErrClosed
	}
	q.cl, q.cfg.PairCode = c, "" // the pairing code is single use: forget it
	q.state, q.problem, q.message = SqConnected, "", ""
	q.seenFP, q.deviceID, q.server = c.ServerFingerprint(), c.DeviceID(), c.ServerName()
	q.loopDone = make(chan struct{})
	go q.loop(c, q.loopDone)
	q.mu.Unlock()
	q.adoptWelcome(c, c.Sessions())
	q.publish()
	return nil
}

func classifyDialErr(cfg SquatterConfig, err error) (state, problem, msg string) {
	var pe *wire.ProtoError
	switch {
	case errors.Is(err, tlsutil.ErrFingerprintMismatch) || strings.Contains(err.Error(), "fingerprint"):
		return SqRefused, ProblemFingerprint, fmt.Sprintf("Squatter %q presented a different certificate than the one you trusted. "+
			"Refusing to connect (someone may be intercepting the connection, or squatterd was reinstalled). Remove and re-pair it only if you are sure.", cfg.Name)
	case errors.As(err, &pe) && pe.Code == proto.ErrAuthFailed:
		return SqRefused, ProblemAuth, fmt.Sprintf("Squatter %q does not accept this device (not paired, pairing code expired or the device was revoked). Pair it again.", cfg.Name)
	case errors.As(err, &pe) && pe.Code == proto.ErrRateLimited:
		return SqUnreachable, ProblemRateLimited, fmt.Sprintf("Squatter %q is rate limiting this address; wait a few minutes.", cfg.Name)
	}
	return SqUnreachable, ProblemUnreachable, fmt.Sprintf("Cannot reach squatter %q (%s:%d): %v", cfg.Name, cfg.Host, cfg.Port, err)
}

// DisconnectSquatter closes the connection on purpose. Its sessions keep
// running in the daemon; locally they become "detached".
func (e *Engine) DisconnectSquatter(id string) error {
	q := e.getSquatter(id)
	if q == nil {
		return ErrNoSquatter
	}
	q.closeConn(SqDisconnected, "", "", LinkDetached)
	return nil
}

// ReconnectSquatter is an explicit user action: close (if open) and connect
// again, re-attaching with `since`. The engine never does this by itself.
func (e *Engine) ReconnectSquatter(ctx context.Context, id string) error {
	if err := e.DisconnectSquatter(id); err != nil {
		return err
	}
	return e.ConnectSquatter(ctx, id)
}

// closeConn tears the connection down and marks sessions with link.
func (q *squatter) closeConn(state, problem, msg, link string) {
	q.mu.Lock()
	c, done := q.cl, q.loopDone
	q.cl, q.loopDone = nil, nil
	q.state, q.problem, q.message = state, problem, msg
	q.pending = 0
	if q.awayTimer != nil {
		q.awayTimer()
		q.awayTimer = nil
	}
	q.mu.Unlock()
	if c != nil {
		_ = c.Close()
		<-done
	}
	q.setLinks(link, "")
	q.publish()
}

// shutdown is the final close (Engine.Close / RemoveSquatter).
func (q *squatter) shutdown() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.closeConn(SqDisconnected, "", "", LinkDetached)
}

func (q *squatter) setCaps(c proto.Caps) {
	if cl := q.client(); cl != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = cl.SetCaps(ctx, c)
	}
}

// setLinks sets the link of every mirrored session (and clears attach state
// when it is not "attached"). note, if non-empty, is stored as a system line.
func (q *squatter) setLinks(link, note string) {
	q.mu.Lock()
	ss := make([]*session, 0, len(q.sessions))
	for _, s := range q.sessions {
		ss = append(ss, s)
	}
	q.mu.Unlock()
	for _, s := range ss {
		s.mu.Lock()
		changed := s.remote.link != link
		s.remote.link = link
		if link != LinkAttached {
			s.remote.attachSent, s.remote.replaying = false, false
		}
		if changed {
			s.publishInfoLocked()
		}
		s.mu.Unlock()
		if note != "" && changed {
			s.systemLine(note)
		}
	}
}

// ---- mirroring ----------------------------------------------------------------

// linkNow is the link a new or re-attached session should have.
func (q *squatter) linkNow() string {
	q.e.mu.RLock()
	susp := q.e.suspended
	q.e.mu.RUnlock()
	q.mu.Lock()
	connected := q.cl != nil
	q.mu.Unlock()
	switch {
	case !connected:
		return LinkUnreachable
	case susp:
		return LinkDetached
	}
	return LinkAttached
}

// ensure returns the mirror of a daemon session, creating it when new.
func (q *squatter) ensure(in proto.SessionInfo) (s *session, created bool) {
	link := q.linkNow()
	q.mu.Lock()
	defer q.mu.Unlock()
	if s = q.sessions[in.ID]; s != nil {
		return s, false
	}
	s, err := q.e.newRemoteSession(q, in, link)
	if err != nil {
		q.e.log.Warn("squatter: cannot mirror session", "squatter", q.id, "session", in.ID, "err", err)
		return nil, false
	}
	q.sessions[in.ID] = s
	return s, true
}

func (q *squatter) lookup(rid string) *session {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.sessions[rid]
}

// adoptWelcome mirrors every session of the welcome list and attaches to them
// (unless suspended). Sessions the daemon no longer has are dropped locally.
func (q *squatter) adoptWelcome(c *wire.Client, list []proto.SessionInfo) {
	known := map[string]bool{}
	for _, in := range list {
		known[in.ID] = true
		s, created := q.ensure(in)
		if s == nil {
			continue
		}
		if !created {
			s.markAckFrom(in)
			s.applyRemoteInfo(in)
		}
		link := q.linkNow()
		s.mu.Lock()
		s.remote.link = link
		s.publishInfoLocked()
		s.mu.Unlock()
		q.attach(c, s)
	}
	q.mu.Lock()
	var gone []*session
	for rid, s := range q.sessions {
		if !known[rid] {
			gone = append(gone, s)
			delete(q.sessions, rid)
		}
	}
	q.mu.Unlock()
	for _, s := range gone {
		q.e.remove(s)
		q.e.bus.publish(Event{Kind: EvNotice, Level: LevelInfo, Text: fmt.Sprintf("session %s was removed on the squatter", s.world.Name)})
	}
}

// markAckFrom records the daemon's per-device acknowledgement from an info
// before the first attach (unread = lastSeq - ack).
func (s *session) markAckFrom(in proto.SessionInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.remote.attachSent && in.Unread <= in.LastSeq {
		if a := in.LastSeq - in.Unread; a > s.remote.ackedRemote {
			s.remote.ackedRemote = a
		}
	}
}

// attach sends `attach` with since=<last seq held> unless already attached or suspended.
func (q *squatter) attach(c *wire.Client, s *session) {
	if q.linkNow() != LinkAttached {
		return
	}
	r := s.remote
	s.mu.Lock()
	if r.attachSent {
		s.mu.Unlock()
		return
	}
	r.attachSent, r.replaying, r.link = true, true, LinkAttached
	since := r.lastSeq
	sized, cols, rows := r.sized, s.cols, s.rows
	s.publishInfoLocked()
	s.mu.Unlock()
	var sp *uint64
	if since > 0 {
		sp = &since
	}
	q.mu.Lock()
	q.pending++
	if q.awayTimer == nil {
		q.awayTimer = q.e.schedule(8*time.Second, q.flushAway)
	}
	backlog := q.cfg.MaxBacklog
	q.mu.Unlock()
	if backlog <= 0 {
		backlog = wire.DefaultBacklog
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Attach(ctx, r.rid, sp, backlog); err != nil {
		s.mu.Lock()
		r.attachSent, r.replaying = false, false
		s.mu.Unlock()
		q.attachDone()
		return
	}
	if sized {
		_ = c.Resize(ctx, r.rid, cols, rows)
	}
}

func (q *squatter) attachDone() {
	q.mu.Lock()
	if q.pending > 0 {
		q.pending--
	}
	flush := q.pending == 0
	q.mu.Unlock()
	if flush {
		q.flushAway()
	}
}

// flushAway publishes the "While you were away" summary for the sessions of
// this squatter that have unread output (once per attach round).
func (q *squatter) flushAway() {
	q.mu.Lock()
	q.pending = 0
	if q.awayTimer != nil {
		q.awayTimer()
		q.awayTimer = nil
	}
	ss := make([]*session, 0, len(q.sessions))
	for _, s := range q.sessions {
		ss = append(ss, s)
	}
	q.mu.Unlock()
	if items := awayOf(q.id, ss); len(items) > 0 {
		q.e.bus.publish(Event{Kind: EvAway, Away: items})
	}
}

func awayOf(sqID string, ss []*session) []AwayItem {
	var out []AwayItem
	for _, s := range ss {
		si := s.snapshot()
		s.mu.Lock()
		unseenEnd := si.State == proto.StateEnded && s.remote != nil && s.remote.lastSeq > s.remote.ackedRemote
		s.mu.Unlock()
		if si.Unread == 0 && !unseenEnd { // an ended session whose end this device has not acknowledged counts too
			continue
		}
		out = append(out, AwayItem{Session: si.ID, Name: si.Name, Squatter: sqID, Unread: si.Unread,
			Ended: si.State == proto.StateEnded, EndReason: si.EndReason, EndedAt: si.EndedAt, LastText: si.LastText})
	}
	return out
}

// Away returns the current "While you were away" data: every squatter session
// with unread output, and whether it has ended.
func (e *Engine) Away() []AwayItem {
	e.mu.RLock()
	var ss []*session
	for _, id := range e.order {
		if s := e.sessions[id]; s != nil && s.remote != nil {
			ss = append(ss, s)
		}
	}
	e.mu.RUnlock()
	var out []AwayItem
	for _, s := range ss {
		out = append(out, awayOf(s.remote.sq.id, []*session{s})...)
	}
	return out
}
