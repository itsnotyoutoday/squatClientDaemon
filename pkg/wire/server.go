package wire

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// ServerConfig configures a Server. Zero durations/limits take defaults.
type ServerConfig struct {
	Backend    Backend
	Auth       *auth.Store
	ServerName string // e.g. "squatterd/0.1.0"
	Logger     *slog.Logger

	MaxClients   int           // authenticated clients (default 32)
	QueueLimit   int           // queued outbound frames before a client is dropped (default 1000)
	AuthTimeout  time.Duration // hello deadline (default 10s)
	WriteTimeout time.Duration // one blocked write (default 10s)
	PingInterval time.Duration // default 20s
	PongTimeout  time.Duration // default 60s
}

const maxPreAuth = 64

// Server is the SWP WebSocket server.
type Server struct {
	cfg    ServerConfig
	log    *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	nextID atomic.Uint64

	mu      sync.Mutex
	closed  bool
	clients map[uint64]*client
	preauth int
	wg      sync.WaitGroup
}

// NewServer builds a Server. Backend and Auth are required.
func NewServer(cfg ServerConfig) *Server {
	if cfg.MaxClients <= 0 {
		cfg.MaxClients = 32
	}
	if cfg.QueueLimit <= 0 {
		cfg.QueueLimit = 1000
	}
	if cfg.AuthTimeout <= 0 {
		cfg.AuthTimeout = 10 * time.Second
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 10 * time.Second
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = 20 * time.Second
	}
	if cfg.PongTimeout <= 0 {
		cfg.PongTimeout = 60 * time.Second
	}
	if cfg.ServerName == "" {
		cfg.ServerName = "squatterd/dev"
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{cfg: cfg, log: log, ctx: ctx, cancel: cancel, clients: map[uint64]*client{}}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Handler returns the HTTP handler serving /swp.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/swp", s.serveWS)
	return mux
}

// NumClients returns the number of authenticated clients.
func (s *Server) NumClients() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

// Close drops every client and waits for their goroutines.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
}

// ---------------------------------------------------------------- client

type client struct {
	s    *Server
	id   uint64
	ws   *websocket.Conn
	ctx  context.Context
	stop context.CancelFunc
	out  chan []byte

	devID, devName string

	mu       sync.Mutex
	caps     proto.Caps
	dropped  bool
	lastRecv time.Time
}

func (c *client) ID() uint64         { return c.id }
func (c *client) DeviceID() string   { return c.devID }
func (c *client) DeviceName() string { return c.devName }
func (c *client) Caps() proto.Caps   { c.mu.Lock(); defer c.mu.Unlock(); return c.caps }

func (c *client) Send(msg any) bool {
	b, err := json.Marshal(msg)
	if err != nil {
		c.s.log.Error("wire: marshal", "err", err)
		return false
	}
	return c.SendRaw(b)
}

func (c *client) SendRaw(frame []byte) bool {
	select {
	case <-c.ctx.Done():
		return false
	default:
	}
	select {
	case c.out <- frame:
		return true
	default:
		c.Close("slow client: outbound queue full")
		return false
	}
}

// Close drops the client (idempotent, non-blocking).
func (c *client) Close(reason string) {
	c.mu.Lock()
	if c.dropped {
		c.mu.Unlock()
		return
	}
	c.dropped = true
	c.mu.Unlock()
	c.s.log.Info("wire: dropping client", "device", c.devID, "reason", reason)
	c.stop()
	go func() { _ = c.ws.Close(websocket.StatusPolicyViolation, truncate(reason, 100)) }()
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (c *client) touch() { c.mu.Lock(); c.lastRecv = time.Now(); c.mu.Unlock() }
func (c *client) idle() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Since(c.lastRecv)
}

// ---------------------------------------------------------------- handler

func remoteIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func (s *Server) writeDirect(ctx context.Context, ws *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.WriteTimeout)
	defer cancel()
	return ws.Write(ctx, websocket.MessageText, b)
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.closed || s.preauth >= maxPreAuth {
		s.mu.Unlock()
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	s.preauth++
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	preauthDone := false
	endPreauth := func() {
		if !preauthDone {
			preauthDone = true
			s.mu.Lock()
			s.preauth--
			s.mu.Unlock()
		}
	}
	defer endPreauth()

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true}) // auth is by key, not origin
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(MaxPreAuthBytes)
	ctx, stop := context.WithCancel(s.ctx)
	defer stop()

	fail := func(code, msg string) {
		_ = s.writeDirect(ctx, ws, NewError("", code, msg))
		_ = ws.Close(websocket.StatusPolicyViolation, code)
	}

	// --- challenge / hello
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		fail(proto.ErrInternal, "entropy failure")
		return
	}
	if err := s.writeDirect(ctx, ws, Challenge{V: proto.Version, T: TChallenge, Nonce: base64.StdEncoding.EncodeToString(nonce), Server: s.cfg.ServerName}); err != nil {
		return
	}
	rctx, rcancel := context.WithTimeout(ctx, s.cfg.AuthTimeout)
	_, data, err := ws.Read(rctx)
	rcancel()
	if err != nil {
		return
	}
	env, err := proto.ParseEnvelope(data)
	if err != nil || env.T != THello {
		fail(proto.ErrAuthFailed, "expected hello")
		return
	}
	var hello Hello
	if err := json.Unmarshal(data, &hello); err != nil {
		fail(proto.ErrAuthFailed, "malformed hello")
		return
	}
	dev, err := s.cfg.Auth.Authenticate(remoteIP(r), auth.Request{PublicKey: hello.PublicKey, Sig: hello.Sig, PairCode: hello.PairCode, DeviceName: hello.DeviceName}, nonce)
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		fail(proto.ErrRateLimited, "too many failed attempts, try again later")
		return
	case errors.Is(err, auth.ErrAuthFailed):
		s.log.Warn("wire: auth failed", "ip", remoteIP(r))
		fail(proto.ErrAuthFailed, "authentication failed")
		return
	case err != nil:
		s.log.Error("wire: auth error", "err", err)
		fail(proto.ErrInternal, "internal error")
		return
	}

	// --- authenticated
	ws.SetReadLimit(MaxFrameBytes)
	c := &client{s: s, id: s.nextID.Add(1), ws: ws, ctx: ctx, stop: stop,
		out: make(chan []byte, s.cfg.QueueLimit), devID: dev.DeviceID, devName: dev.Name,
		caps: proto.DefaultCaps(), lastRecv: time.Now()}
	if hello.Caps != nil {
		c.caps = *hello.Caps
	}
	s.mu.Lock()
	if s.closed || len(s.clients) >= s.cfg.MaxClients {
		s.mu.Unlock()
		fail(proto.ErrRateLimited, "too many clients")
		return
	}
	s.clients[c.id] = c
	s.mu.Unlock()
	endPreauth()

	infos := s.cfg.Backend.PeerJoined(c)
	defer func() {
		s.cfg.Backend.PeerLeft(c)
		s.mu.Lock()
		delete(s.clients, c.id)
		s.mu.Unlock()
	}()
	if infos == nil {
		infos = []proto.SessionInfo{}
	}
	if err := s.writeDirect(ctx, ws, Welcome{V: proto.Version, T: TWelcome, Server: s.cfg.ServerName, DeviceID: dev.DeviceID, Role: "owner", Sessions: infos}); err != nil {
		c.Close("welcome write failed")
		return
	}
	s.log.Info("wire: client authenticated", "device", dev.DeviceID, "name", dev.Name, "ip", remoteIP(r))

	var wwg sync.WaitGroup
	wwg.Add(1)
	go func() { defer wwg.Done(); c.writeLoop() }()
	c.readLoop()
	c.Close("closed")
	wwg.Wait()
}

func (c *client) writeLoop() {
	s := c.s
	ping := time.NewTicker(s.cfg.PingInterval)
	check := time.NewTicker(s.cfg.PongTimeout / 4)
	defer ping.Stop()
	defer check.Stop()
	write := func(b []byte) bool {
		ctx, cancel := context.WithTimeout(c.ctx, s.cfg.WriteTimeout)
		defer cancel()
		if err := c.ws.Write(ctx, websocket.MessageText, b); err != nil {
			if c.ctx.Err() == nil {
				c.Close("write failed or timed out")
			}
			return false
		}
		return true
	}
	pingFrame, _ := json.Marshal(Simple(TPing))
	for {
		select {
		case <-c.ctx.Done():
			return
		case b := <-c.out:
			if !write(b) {
				return
			}
		case <-ping.C:
			if !write(pingFrame) {
				return
			}
		case <-check.C:
			if c.idle() > s.cfg.PongTimeout {
				c.Close("no pong")
				return
			}
		}
	}
}

func (c *client) readLoop() {
	s := c.s
	bad := 0
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		c.touch()
		if typ != websocket.MessageText {
			c.Close("binary frames not allowed")
			return
		}
		cf, known, err := DecodeClient(data)
		if err != nil {
			bad++
			s.replyErr(c, "", proto.ErrBadRequest, err.Error())
			if bad >= 20 {
				c.Close("too many malformed frames")
				return
			}
			continue
		}
		if !known {
			continue
		}
		bad = 0
		s.dispatch(c, cf)
	}
}

func (s *Server) replyErr(c *client, reqID, code, msg string) {
	c.Send(NewError(reqID, code, msg))
}

func (s *Server) fail(c *client, reqID string, err error) {
	var we *ProtoError
	if errors.As(err, &we) {
		s.replyErr(c, reqID, we.Code, we.Message)
		return
	}
	s.log.Error("wire: backend error", "err", err)
	s.replyErr(c, reqID, proto.ErrInternal, "internal error")
}

func (s *Server) dispatch(c *client, cf ClientFrame) {
	b := s.cfg.Backend
	var err error
	switch m := cf.Msg.(type) {
	case ConnectReq:
		var info proto.SessionInfo
		if info, err = b.Connect(c.ctx, c, m); err == nil {
			c.Send(SessionMsg{V: proto.Version, T: TSessionCreated, ReqID: cf.ID, Session: info})
		}
	case AttachReq:
		err = b.Attach(c, m)
	case SendReq:
		err = b.Send(c, m.Session, m.Text)
	case AckReq:
		err = b.Ack(c, m.Session, m.Seq)
	case ResizeReq:
		err = b.Resize(c, m.Session, m.Cols, m.Rows)
	case CapsReq:
		c.mu.Lock()
		c.caps = m.Caps
		c.mu.Unlock()
		b.CapsChanged(c)
	case SessionReq:
		switch cf.T {
		case TSessionDisc:
			err = b.Disconnect(c, m.Session)
		case TSessionDismiss:
			err = b.Dismiss(c, m.Session)
		case TDetach:
			err = b.Detach(c, m.Session)
		}
	default:
		switch cf.T {
		case TSessionList:
			sl := b.List(c)
			if sl == nil {
				sl = []proto.SessionInfo{}
			}
			c.Send(SessionListMsg{V: proto.Version, T: TSessionList, ReqID: cf.ID, Sessions: sl})
		case TPong:
		}
	}
	if err != nil {
		s.fail(c, cf.ID, err)
	}
}
