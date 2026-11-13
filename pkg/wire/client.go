package wire

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tlsutil"
)

// ErrInsecure is returned by Dial for a ws:// URL unless InsecureLoopback is
// set and the host is a loopback address.
var ErrInsecure = errors.New("wire: plain ws:// is only allowed to a loopback host with InsecureLoopback")

// ErrClosed is returned by Client methods after the connection ended.
var ErrClosed = errors.New("wire: client closed")

// ClientOptions configures Dial.
type ClientOptions struct {
	Key               ed25519.PrivateKey // device key (required)
	PairCode          string             // one-time pairing code (first connect only)
	DeviceName        string
	ClientName        string // default "SquatClient"
	Caps              proto.Caps
	PinnedFingerprint string // AA:BB:... ; empty = trust on first use (read ServerFingerprint after Dial)
	InsecureLoopback  bool   // allow ws:// to loopback
	EventBuffer       int    // default 256; the reader blocks (TCP backpressure) when full
	HandshakeTimeout  time.Duration
}

// Event is one of SessionState, Line, Gmcp, Backlog, Attached, Removed, Error, Closed.
type Event interface{ isEvent() }

// Entry is one replayed item: exactly one of Line or Gmcp is set.
type Entry struct {
	Line *proto.Line
	Gmcp *proto.GmcpMsg
}

// Seq returns the entry's sequence number.
func (e Entry) Seq() uint64 {
	if e.Line != nil {
		return e.Line.Seq
	}
	if e.Gmcp != nil {
		return e.Gmcp.Seq
	}
	return 0
}

// Event types.
type (
	// SessionState is session.state (any change to any session).
	SessionState struct{ Info proto.SessionInfo }
	// Line is a live line entry (after the session's backlog was done).
	Line struct{ proto.Line }
	// Gmcp is a live GMCP entry.
	Gmcp struct{ proto.GmcpMsg }
	// Backlog is one replay frame; entries are ascending, de-duplicated and
	// all newer than anything delivered before for the session.
	Backlog struct {
		Session string
		Entries []Entry
		Done    bool
	}
	// Attached acknowledges an attach.
	Attached struct {
		Session           string
		FirstSeq, LastSeq uint64
		ReplayFrom        uint64
		Gap               *Gap
	}
	// Removed says a session was dismissed.
	Removed struct{ Session string }
	// Error is a daemon error not consumed by a pending request.
	Error struct {
		ReqID, Code, Message string
	}
	// Closed is the final event; Err is nil after a local Close.
	Closed struct{ Err error }
)

func (SessionState) isEvent() {}
func (Line) isEvent()         {}
func (Gmcp) isEvent()         {}
func (Backlog) isEvent()      {}
func (Attached) isEvent()     {}
func (Removed) isEvent()      {}
func (Error) isEvent()        {}
func (Closed) isEvent()       {}

// Client is one authenticated SWP connection. It never reconnects: when it
// closes, the app decides whether to Dial again.
type Client struct {
	ws     *websocket.Conn
	events chan Event
	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc

	deviceID string
	server   string
	sessions []proto.SessionInfo
	features []string
	serverFP atomic.Value // string

	reqSeq atomic.Uint64

	mu      sync.Mutex
	pending map[string]chan any
	floor   map[string]uint64
	err     error
}

// Dial connects, authenticates and returns a ready Client.
func Dial(ctx context.Context, rawURL string, opts ClientOptions) (*Client, error) {
	if len(opts.Key) != ed25519.PrivateKeySize {
		return nil, errors.New("wire: ClientOptions.Key is required")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("wire: bad url: %w", err)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/swp"
	}
	hc := &http.Client{}
	c := &Client{events: make(chan Event, max(opts.EventBuffer, 256)), done: make(chan struct{}),
		pending: map[string]chan any{}, floor: map[string]uint64{}}
	c.serverFP.Store("")
	switch u.Scheme {
	case "wss":
		cfg := tlsutil.PinnedConfig(opts.PinnedFingerprint, func(fp string) { c.serverFP.Store(fp) })
		hc.Transport = &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: false, TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}}
	case "ws":
		if !opts.InsecureLoopback || !isLoopbackHost(u.Hostname()) {
			return nil, ErrInsecure
		}
	default:
		return nil, fmt.Errorf("wire: unsupported scheme %q", u.Scheme)
	}
	ht := opts.HandshakeTimeout
	if ht <= 0 {
		ht = 15 * time.Second
	}
	dctx, dcancel := context.WithTimeout(ctx, ht)
	defer dcancel()
	ws, _, err := websocket.Dial(dctx, u.String(), &websocket.DialOptions{HTTPClient: hc})
	if err != nil {
		return nil, fmt.Errorf("wire: dial: %w", err)
	}
	ws.SetReadLimit(4 << 20)
	ok := false
	defer func() {
		if !ok {
			_ = ws.CloseNow()
		}
	}()

	var ch Challenge
	if err := readJSON(dctx, ws, &ch); err != nil {
		return nil, fmt.Errorf("wire: reading challenge: %w", wrapErrMsg(err))
	}
	if ch.T != TChallenge {
		return nil, fmt.Errorf("wire: expected challenge, got %q", ch.T)
	}
	sig, err := auth.SignNonce(opts.Key, ch.Nonce)
	if err != nil {
		return nil, err
	}
	name := opts.ClientName
	if name == "" {
		name = "SquatClient"
	}
	caps := opts.Caps
	hello := Hello{V: proto.Version, T: THello, Client: name, DeviceName: opts.DeviceName,
		PublicKey: auth.PublicKeyB64(opts.Key), Sig: sig, PairCode: opts.PairCode, Caps: &caps}
	b, _ := json.Marshal(hello)
	if err := ws.Write(dctx, websocket.MessageText, b); err != nil {
		return nil, fmt.Errorf("wire: sending hello: %w", err)
	}
	var raw json.RawMessage
	if err := readJSON(dctx, ws, &raw); err != nil {
		return nil, fmt.Errorf("wire: waiting for welcome: %w", wrapErrMsg(err))
	}
	env, err := proto.ParseEnvelope(raw)
	if err != nil {
		return nil, err
	}
	switch env.T {
	case TError:
		var em ErrorMsg
		_ = json.Unmarshal(raw, &em)
		return nil, &ProtoError{Code: em.Code, Message: em.Message}
	case TWelcome:
		var w Welcome
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("wire: bad welcome: %w", err)
		}
		c.deviceID, c.server, c.sessions, c.features = w.DeviceID, w.Server, w.Sessions, w.Features
	default:
		return nil, fmt.Errorf("wire: unexpected %q instead of welcome", env.T)
	}
	ws.SetReadLimit(MaxFrameBytes * 4)
	c.ws = ws
	c.ctx, c.cancel = context.WithCancel(context.Background())
	ok = true
	go c.readLoop()
	return c, nil
}

func wrapErrMsg(err error) error { return err }

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func readJSON(ctx context.Context, ws *websocket.Conn, v any) error {
	_, data, err := ws.Read(ctx)
	if err != nil {
		return err
	}
	// an immediate error message (e.g. rate limited) may precede the challenge
	if rm, ok := v.(*json.RawMessage); ok {
		*rm = data
		return nil
	}
	var env proto.Envelope
	if e, err := proto.ParseEnvelope(data); err == nil {
		env = e
		if env.T == TError {
			var em ErrorMsg
			_ = json.Unmarshal(data, &em)
			return &ProtoError{Code: em.Code, Message: em.Message}
		}
	}
	return json.Unmarshal(data, v)
}

// DeviceID is this device's id as reported by the daemon.
func (c *Client) DeviceID() string { return c.deviceID }

// ServerName is the daemon's version string.
func (c *Client) ServerName() string { return c.server }

// Sessions returns the session list from the welcome message.
func (c *Client) Sessions() []proto.SessionInfo { return c.sessions }

// ServerFingerprint is the TLS fingerprint seen during the handshake ("" for ws://).
func (c *Client) ServerFingerprint() string { return c.serverFP.Load().(string) }

// Events delivers events in wire order; the channel closes after Closed.
// The consumer must drain it.
func (c *Client) Events() <-chan Event { return c.events }

// Done is closed when the connection has ended.
func (c *Client) Done() <-chan struct{} { return c.done }

// Close closes the connection and waits for the reader to finish.
func (c *Client) Close() error {
	c.cancel()
	_ = c.ws.Close(websocket.StatusNormalClosure, "bye")
	<-c.done
	return nil
}

// ---------------------------------------------------------------- reading

func (c *Client) emit(ev Event) bool {
	select {
	case c.events <- ev:
		return true
	case <-c.ctx.Done():
		return false
	}
}

func (c *Client) readLoop() {
	var err error
	defer func() {
		c.mu.Lock()
		c.err = err
		for k, ch := range c.pending {
			close(ch)
			delete(c.pending, k)
		}
		c.mu.Unlock()
		_ = c.ws.CloseNow()
		c.cancel()
		// the Closed event must always be deliverable, even when nobody drains
		select {
		case c.events <- Closed{Err: err}:
		default:
		}
		close(c.events)
		close(c.done)
	}()
	for {
		var typ websocket.MessageType
		var data []byte
		typ, data, err = c.ws.Read(c.ctx)
		if err != nil {
			if c.ctx.Err() != nil {
				err = nil
			}
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		c.handle(data)
	}
}

func (c *Client) resolve(reqID string, v any) bool {
	if reqID == "" {
		return false
	}
	c.mu.Lock()
	ch, ok := c.pending[reqID]
	if ok {
		delete(c.pending, reqID)
	}
	c.mu.Unlock()
	if ok {
		ch <- v // buffered 1
	}
	return ok
}

func (c *Client) handle(data []byte) {
	env, err := proto.ParseEnvelope(data)
	if err != nil {
		return
	}
	switch env.T {
	case TPing:
		b, _ := json.Marshal(Simple(TPong))
		wctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
		_ = c.ws.Write(wctx, websocket.MessageText, b)
		cancel()
	case TError:
		var m ErrorMsg
		if json.Unmarshal(data, &m) != nil {
			return
		}
		if !c.resolve(m.ReqID, m) {
			c.emit(Error{ReqID: m.ReqID, Code: m.Code, Message: m.Message})
		}
	case TSessionCreated:
		var m SessionMsg
		if json.Unmarshal(data, &m) == nil && !c.resolve(m.ReqID, m.Session) {
			c.emit(SessionState{Info: m.Session})
		}
	case TSessionList:
		var m SessionListMsg
		if json.Unmarshal(data, &m) == nil {
			c.resolve(m.ReqID, m.Sessions)
		}
	case TSessionState:
		var m SessionMsg
		if json.Unmarshal(data, &m) == nil {
			c.emit(SessionState{Info: m.Session})
		}
	case TSessionRemoved:
		var m RemovedMsg
		if json.Unmarshal(data, &m) == nil {
			c.mu.Lock()
			delete(c.floor, m.Session)
			c.mu.Unlock()
			c.emit(Removed{Session: m.Session})
		}
	case TAttached:
		var m AttachedMsg
		if json.Unmarshal(data, &m) == nil {
			c.emit(Attached{Session: m.Session, FirstSeq: m.FirstSeq, LastSeq: m.LastSeq, ReplayFrom: m.ReplayFrom, Gap: m.Gap})
		}
	case TBacklog:
		var m BacklogMsg
		if json.Unmarshal(data, &m) != nil {
			return
		}
		var out []Entry
		for _, raw := range m.Entries {
			if e, ok := c.decodeEntry(m.Session, raw); ok {
				out = append(out, e)
			}
		}
		c.emit(Backlog{Session: m.Session, Entries: out, Done: m.Done})
	case proto.TLine:
		var l proto.Line
		if json.Unmarshal(data, &l) == nil && c.accept(l.Session, l.Seq) {
			c.emit(Line{l})
		}
	case proto.TGmcp:
		var g proto.GmcpMsg
		if json.Unmarshal(data, &g) == nil && c.accept(g.Session, g.Seq) {
			c.emit(Gmcp{g})
		}
	}
}

// accept implements de-duplication: only seqs above the session floor pass.
func (c *Client) accept(session string, seq uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if seq <= c.floor[session] {
		return false
	}
	c.floor[session] = seq
	return true
}

func (c *Client) decodeEntry(session string, raw json.RawMessage) (Entry, bool) {
	var h struct {
		T   string `json:"t"`
		Seq uint64 `json:"seq"`
	}
	if json.Unmarshal(raw, &h) != nil {
		return Entry{}, false
	}
	switch h.T {
	case proto.TLine:
		var l proto.Line
		if json.Unmarshal(raw, &l) != nil || !c.accept(session, l.Seq) {
			return Entry{}, false
		}
		return Entry{Line: &l}, true
	case proto.TGmcp:
		var g proto.GmcpMsg
		if json.Unmarshal(raw, &g) != nil || !c.accept(session, g.Seq) {
			return Entry{}, false
		}
		return Entry{Gmcp: &g}, true
	}
	return Entry{}, false
}

// LastSeq returns the highest seq delivered for a session (the value to pass
// as `since` when re-attaching after a redial the app should persist).
func (c *Client) LastSeq(session string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.floor[session]
}

// ---------------------------------------------------------------- requests

func (c *Client) write(ctx context.Context, v any) error {
	select {
	case <-c.done:
		return ErrClosed
	default:
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := c.ws.Write(ctx, websocket.MessageText, b); err != nil {
		return fmt.Errorf("wire: write: %w", err)
	}
	return nil
}

func (c *Client) request(ctx context.Context, msg func(id string) any) (any, error) {
	id := "r" + strconv.FormatUint(c.reqSeq.Add(1), 10)
	ch := make(chan any, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	cleanup := func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }
	if err := c.write(ctx, msg(id)); err != nil {
		cleanup()
		return nil, err
	}
	select {
	case v, ok := <-ch:
		if !ok {
			return nil, ErrClosed
		}
		if e, isErr := v.(ErrorMsg); isErr {
			return nil, &ProtoError{Code: e.Code, Message: e.Message}
		}
		return v, nil
	case <-ctx.Done():
		cleanup()
		return nil, ctx.Err()
	case <-c.done:
		return nil, ErrClosed
	}
}

// Connect asks the daemon to open a new session to world.
func (c *Client) Connect(ctx context.Context, w proto.World) (proto.SessionInfo, error) {
	v, err := c.request(ctx, func(id string) any {
		return struct {
			V int    `json:"v"`
			T string `json:"t"`
			ConnectReq
		}{proto.Version, TSessionConnect, ConnectReq{ID: id, World: w}}
	})
	if err != nil {
		return proto.SessionInfo{}, err
	}
	return v.(proto.SessionInfo), nil
}

// List fetches the current session list.
func (c *Client) List(ctx context.Context) ([]proto.SessionInfo, error) {
	v, err := c.request(ctx, func(id string) any {
		return SessionListMsg{V: proto.Version, T: TSessionList, ID: id, Sessions: nil}
	})
	if err != nil {
		return nil, err
	}
	return v.([]proto.SessionInfo), nil
}

func (c *Client) sessionMsg(ctx context.Context, t, session string) error {
	return c.write(ctx, struct {
		V int    `json:"v"`
		T string `json:"t"`
		SessionReq
	}{proto.Version, t, SessionReq{Session: session}})
}

// Disconnect ends a live session (the result arrives as SessionState).
func (c *Client) Disconnect(ctx context.Context, session string) error {
	return c.sessionMsg(ctx, TSessionDisc, session)
}

// Dismiss removes an ended session (the result arrives as Removed).
func (c *Client) Dismiss(ctx context.Context, session string) error {
	return c.sessionMsg(ctx, TSessionDismiss, session)
}

// Detach stops live delivery for a session.
func (c *Client) Detach(ctx context.Context, session string) error {
	return c.sessionMsg(ctx, TDetach, session)
}

// Attach starts replay + live delivery. since is the last seq the app already
// holds (nil = nothing); entries at or below it are filtered out as duplicates.
func (c *Client) Attach(ctx context.Context, session string, since *uint64, maxBacklog int) error {
	c.mu.Lock()
	if since != nil {
		c.floor[session] = *since
	} else {
		c.floor[session] = 0
	}
	c.mu.Unlock()
	return c.write(ctx, struct {
		V int    `json:"v"`
		T string `json:"t"`
		AttachReq
	}{proto.Version, TAttach, AttachReq{Session: session, Since: since, MaxBacklog: maxBacklog}})
}

// Send sends one line of input (the daemon echoes it to all attached clients).
func (c *Client) Send(ctx context.Context, session, text string) error {
	return c.write(ctx, struct {
		V int    `json:"v"`
		T string `json:"t"`
		SendReq
	}{proto.Version, TSend, SendReq{Session: session, Text: text}})
}

// HasFeature reports whether the daemon advertised an optional feature (e.g. FeatureNoEcho).
func (c *Client) HasFeature(name string) bool {
	for _, f := range c.features {
		if f == name {
			return true
		}
	}
	return false
}

// SendSecret sends one line the daemon must NOT record, echo or log (logins,
// passwords). It fails if the daemon does not advertise FeatureNoEcho, so a
// secret is never sent to a daemon that would store it.
func (c *Client) SendSecret(ctx context.Context, session, text string) error {
	if !c.HasFeature(FeatureNoEcho) {
		return &ProtoError{Code: proto.ErrBadRequest, Message: "this squatterd is too old to hide secrets: update it (needs noEcho)"}
	}
	return c.write(ctx, struct {
		V int    `json:"v"`
		T string `json:"t"`
		SendReq
	}{proto.Version, TSend, SendReq{Session: session, Text: text, NoEcho: true}})
}

// Ack tells the daemon this device has read up to seq.
func (c *Client) Ack(ctx context.Context, session string, seq uint64) error {
	return c.write(ctx, struct {
		V int    `json:"v"`
		T string `json:"t"`
		AckReq
	}{proto.Version, TAck, AckReq{Session: session, Seq: seq}})
}

// Resize reports the terminal size (NAWS).
func (c *Client) Resize(ctx context.Context, session string, cols, rows int) error {
	return c.write(ctx, struct {
		V int    `json:"v"`
		T string `json:"t"`
		ResizeReq
	}{proto.Version, TResize, ResizeReq{Session: session, Cols: cols, Rows: rows}})
}

// SetCaps updates the capabilities announced to games.
func (c *Client) SetCaps(ctx context.Context, caps proto.Caps) error {
	return c.write(ctx, struct {
		V int    `json:"v"`
		T string `json:"t"`
		CapsReq
	}{proto.Version, TCaps, CapsReq{Caps: caps}})
}
