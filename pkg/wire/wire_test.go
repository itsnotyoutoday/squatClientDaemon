package wire

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// fakeBackend implements Backend for unit tests.
type fakeBackend struct {
	mu     sync.Mutex
	peers  []Peer
	left   int
	attach func(p Peer, req AttachReq) error
	got    []string
}

func (f *fakeBackend) PeerJoined(p Peer) []proto.SessionInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.peers = append(f.peers, p)
	return []proto.SessionInfo{{ID: "s-000001", Name: "x", Host: "h", Port: 1, State: "connected"}}
}
func (f *fakeBackend) PeerLeft(p Peer) { f.mu.Lock(); f.left++; f.mu.Unlock() }
func (f *fakeBackend) List(p Peer) []proto.SessionInfo {
	return []proto.SessionInfo{{ID: "s-000001"}, {ID: "s-000002"}}
}
func (f *fakeBackend) Connect(_ context.Context, p Peer, req ConnectReq) (proto.SessionInfo, error) {
	if req.World.Host == "deny" {
		return proto.SessionInfo{}, Errf(proto.ErrBadRequest, "denied")
	}
	return proto.SessionInfo{ID: "s-new", Host: req.World.Host, Port: req.World.Port, State: "connecting"}, nil
}
func (f *fakeBackend) rec(s string) error {
	f.mu.Lock()
	f.got = append(f.got, s)
	f.mu.Unlock()
	return nil
}
func (f *fakeBackend) Disconnect(p Peer, s string) error { return f.rec("disc " + s) }
func (f *fakeBackend) Dismiss(p Peer, s string) error    { return Errf(proto.ErrBadRequest, "nope") }
func (f *fakeBackend) Attach(p Peer, r AttachReq) error {
	if f.attach != nil {
		return f.attach(p, r)
	}
	return nil
}
func (f *fakeBackend) Detach(p Peer, s string) error        { return f.rec("detach " + s) }
func (f *fakeBackend) Send(p Peer, s, t string) error       { return f.rec("send " + s + " " + t) }
func (f *fakeBackend) Ack(p Peer, s string, q uint64) error { return f.rec("ack " + s) }
func (f *fakeBackend) Resize(p Peer, s string, c, r int) error {
	return f.rec("resize " + s)
}
func (f *fakeBackend) CapsChanged(p Peer) { _ = f.rec("caps") }
func (f *fakeBackend) records() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

type rig struct {
	t   *testing.T
	fb  *fakeBackend
	as  *auth.Store
	srv *Server
	ts  *httptest.Server
	url string
}

func newRig(t *testing.T, mod func(*ServerConfig)) *rig {
	t.Helper()
	as, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fb := &fakeBackend{}
	cfg := ServerConfig{Backend: fb, Auth: as, ServerName: "squatterd/test"}
	if mod != nil {
		mod(&cfg)
	}
	srv := NewServer(cfg)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Close() })
	return &rig{t: t, fb: fb, as: as, srv: srv, ts: ts, url: "ws" + strings.TrimPrefix(ts.URL, "http") + "/swp"}
}

func (r *rig) dial(opts ClientOptions) (*Client, error) {
	r.t.Helper()
	if opts.Key == nil {
		opts.Key, _ = auth.GenerateKey()
		code, _, _ := r.as.NewPairing()
		opts.PairCode = code
	}
	opts.InsecureLoopback = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return Dial(ctx, r.url, opts)
}

func (r *rig) rawAuthed() (*websocket.Conn, ed25519.PrivateKey) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, r.url, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { ws.CloseNow() })
	var ch Challenge
	_, data, _ := ws.Read(ctx)
	_ = json.Unmarshal(data, &ch)
	key, _ := auth.GenerateKey()
	code, _, _ := r.as.NewPairing()
	sig, _ := auth.SignNonce(key, ch.Nonce)
	b, _ := json.Marshal(Hello{V: 1, T: THello, PublicKey: auth.PublicKeyB64(key), Sig: sig, PairCode: code})
	_ = ws.Write(ctx, websocket.MessageText, b)
	_, data, err = ws.Read(ctx)
	if err != nil || !strings.Contains(string(data), `"welcome"`) {
		r.t.Fatalf("raw auth: %v %s", err, data)
	}
	return ws, key
}

func TestHandshakeAndRequests(t *testing.T) {
	r := newRig(t, nil)
	c, err := r.dial(ClientOptions{DeviceName: "t"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.ServerName() != "squatterd/test" || len(c.DeviceID()) != 16 || len(c.Sessions()) != 1 {
		t.Fatalf("welcome: %q %q %v", c.ServerName(), c.DeviceID(), c.Sessions())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	l, err := c.List(ctx)
	if err != nil || len(l) != 2 {
		t.Fatalf("list %v %v", l, err)
	}
	info, err := c.Connect(ctx, proto.World{Host: "example.org", Port: 4201})
	if err != nil || info.ID != "s-new" {
		t.Fatalf("connect %v %v", info, err)
	}
	_, err = c.Connect(ctx, proto.World{Host: "deny", Port: 4201})
	var pe *ProtoError
	if !errors.As(err, &pe) || pe.Code != proto.ErrBadRequest {
		t.Fatalf("expected bad_request, got %v", err)
	}
	// fire-and-forget requests reach the backend; errors surface as Error events
	_ = c.Send(ctx, "s-1", "hi")
	_ = c.Ack(ctx, "s-1", 3)
	_ = c.Resize(ctx, "s-1", 100, 30)
	_ = c.Detach(ctx, "s-1")
	_ = c.Disconnect(ctx, "s-1")
	_ = c.SetCaps(ctx, proto.Caps{UTF8: true})
	_ = c.Dismiss(ctx, "s-1")
	select {
	case ev := <-c.Events():
		if e, ok := ev.(Error); !ok || e.Code != proto.ErrBadRequest {
			t.Fatalf("event %#v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no error event")
	}
	got := strings.Join(r.fb.records(), ",")
	if got != "send s-1 hi,ack s-1,resize s-1,detach s-1,disc s-1,caps" {
		t.Fatalf("backend saw %q", got)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.List(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}

func TestAuthRejected(t *testing.T) {
	r := newRig(t, nil)
	key, _ := auth.GenerateKey()
	_, err := r.dial(ClientOptions{Key: key}) // not paired, no code
	var pe *ProtoError
	if !errors.As(err, &pe) || pe.Code != proto.ErrAuthFailed {
		t.Fatalf("got %v", err)
	}
	// rate limit kicks in after 5 failures from this IP
	for i := 0; i < 5; i++ {
		_, _ = r.dial(ClientOptions{Key: key, PairCode: "AAAA-BBBB"})
	}
	_, err = r.dial(ClientOptions{})
	if !errors.As(err, &pe) || pe.Code != proto.ErrRateLimited {
		t.Fatalf("expected rate_limited, got %v", err)
	}
}

func TestInsecureRefused(t *testing.T) {
	r := newRig(t, nil)
	key, _ := auth.GenerateKey()
	ctx := context.Background()
	if _, err := Dial(ctx, r.url, ClientOptions{Key: key}); !errors.Is(err, ErrInsecure) {
		t.Fatalf("no flag: %v", err)
	}
	if _, err := Dial(ctx, "ws://example.org:10327/swp", ClientOptions{Key: key, InsecureLoopback: true}); !errors.Is(err, ErrInsecure) {
		t.Fatalf("non-loopback: %v", err)
	}
	if _, err := Dial(ctx, "http://x", ClientOptions{Key: key}); err == nil {
		t.Fatal("bad scheme accepted")
	}
	if _, err := Dial(ctx, r.url, ClientOptions{}); err == nil {
		t.Fatal("missing key accepted")
	}
}

func TestClientDedupAndOrdering(t *testing.T) {
	r := newRig(t, nil)
	line := func(seq uint64) proto.Line {
		return proto.Line{V: 1, T: "line", Session: "s-1", Seq: seq, Kind: "game", Spans: []proto.Span{{S: "x"}}}
	}
	r.fb.attach = func(p Peer, req AttachReq) error {
		var raws []json.RawMessage
		for _, s := range []uint64{4, 5, 6} {
			b, _ := json.Marshal(line(s))
			raws = append(raws, b)
		}
		p.Send(AttachedMsg{V: 1, T: TAttached, Session: "s-1", FirstSeq: 1, LastSeq: 6, ReplayFrom: 4})
		p.Send(BacklogMsg{V: 1, T: TBacklog, Session: "s-1", Entries: raws, Done: true})
		for _, s := range []uint64{6, 6, 7, 5, 8} { // duplicates / stale seqs must be dropped
			p.Send(line(s))
		}
		return nil
	}
	c, err := r.dial(ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	since := uint64(3)
	if err := c.Attach(context.Background(), "s-1", &since, 100); err != nil {
		t.Fatal(err)
	}
	var seqs []uint64
	deadline := time.After(5 * time.Second)
	for len(seqs) < 5 {
		select {
		case ev := <-c.Events():
			switch e := ev.(type) {
			case Backlog:
				for _, en := range e.Entries {
					seqs = append(seqs, en.Seq())
				}
			case Line:
				seqs = append(seqs, e.Seq)
			}
		case <-deadline:
			t.Fatalf("timeout, got %v", seqs)
		}
	}
	if got := seqs; len(got) != 5 || got[0] != 4 || got[1] != 5 || got[2] != 6 || got[3] != 7 || got[4] != 8 {
		t.Fatalf("seqs %v", got)
	}
	if c.LastSeq("s-1") != 8 {
		t.Fatalf("LastSeq %d", c.LastSeq("s-1"))
	}
}

func TestPingTimeoutDropsOnlyThatClient(t *testing.T) {
	r := newRig(t, func(c *ServerConfig) { c.PingInterval = 40 * time.Millisecond; c.PongTimeout = 300 * time.Millisecond })
	good, err := r.dial(ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer good.Close()
	go func() {
		for range good.Events() {
		}
	}()
	raw, _ := r.rawAuthed() // never reads, never pongs
	_ = raw
	waitFor(t, "silent client dropped", func() bool { return r.srv.NumClients() == 1 })
	time.Sleep(500 * time.Millisecond)
	if r.srv.NumClients() != 1 {
		t.Fatalf("responsive client was dropped (n=%d)", r.srv.NumClients())
	}
}

func TestSlowClientIsDropped(t *testing.T) {
	r := newRig(t, func(c *ServerConfig) { c.QueueLimit = 5 })
	good, err := r.dial(ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer good.Close()
	go func() {
		for range good.Events() {
		}
	}()
	raw, _ := r.rawAuthed() // authenticated but never reads
	_ = raw
	waitFor(t, "two clients", func() bool { return r.srv.NumClients() == 2 })
	r.fb.mu.Lock()
	peers := append([]Peer(nil), r.fb.peers...)
	r.fb.mu.Unlock()
	big := []byte(`{"v":1,"t":"line","session":"s","seq":1,"ts":0,"kind":"game","spans":[{"s":"` + strings.Repeat("x", 1<<20) + `"}]}`)
	dropped := false
	for i := 0; i < 200 && !dropped; i++ {
		for _, p := range peers {
			if !p.SendRaw(big) && p.DeviceID() != good.DeviceID() {
				dropped = true
			}
		}
		time.Sleep(time.Millisecond)
	}
	if !dropped {
		t.Fatal("slow client was never dropped")
	}
	waitFor(t, "slow client gone", func() bool { return r.srv.NumClients() == 1 })
	if good.Sessions() == nil {
		t.Fatal("unreachable")
	}
	select {
	case <-good.Done():
		t.Fatal("healthy client was dropped")
	default:
	}
}

func TestHostileFrames(t *testing.T) {
	r := newRig(t, nil)
	ws, _ := r.rawAuthed()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	write := func(s string) { _ = ws.Write(ctx, websocket.MessageText, []byte(s)) }
	read := func() string {
		_, d, err := ws.Read(ctx)
		if err != nil {
			return "ERR " + err.Error()
		}
		return string(d)
	}
	write("not json")
	if m := read(); !strings.Contains(m, `"bad_request"`) {
		t.Fatalf("garbage: %s", m)
	}
	write(`{"v":1,"t":"from.the.future","x":1}`) // ignored silently
	write(`{"v":1,"t":"send","session":"s-1"` + `,"text":5}`)
	if m := read(); !strings.Contains(m, `"bad_request"`) {
		t.Fatalf("bad field type: %s", m)
	}
	write(`{"v":1,"t":"session.list","id":"q1"}`)
	if m := read(); !strings.Contains(m, `"reqId":"q1"`) {
		t.Fatalf("still alive? %s", m)
	}
	write(`{"v":1,"t":"session.connect","id":"c","world":{"host":"h","port":99999}}`)
	if m := read(); !strings.Contains(m, `"bad_request"`) {
		t.Fatalf("bad port: %s", m)
	}
	// oversized frame closes the connection
	_ = ws.Write(ctx, websocket.MessageText, []byte(`{"v":1,"t":"send","session":"s","text":"`+strings.Repeat("a", 2<<20)+`"}`))
	for i := 0; i < 5; i++ {
		if m := read(); strings.HasPrefix(m, "ERR") {
			return
		}
	}
	t.Fatal("oversized frame did not close the connection")
}

func TestPreAuthGarbage(t *testing.T) {
	r := newRig(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, hello := range []string{"junk", `{"v":1,"t":"send"}`, `{"v":1,"t":"hello","publicKey":"AAAA","sig":"AAAA"}`} {
		ws, _, err := websocket.Dial(ctx, r.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _ = ws.Read(ctx) // challenge
		_ = ws.Write(ctx, websocket.MessageText, []byte(hello))
		_, d, _ := ws.Read(ctx)
		if !strings.Contains(string(d), "auth_failed") {
			t.Fatalf("%q -> %s", hello, d)
		}
		ws.CloseNow()
	}
}

func TestReplayedNonceRejected(t *testing.T) {
	r := newRig(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key, _ := auth.GenerateKey()
	code, _, _ := r.as.NewPairing()
	// connection A: capture a valid hello
	a, _, _ := websocket.Dial(ctx, r.url, nil)
	var cha Challenge
	_, d, _ := a.Read(ctx)
	_ = json.Unmarshal(d, &cha)
	sig, _ := auth.SignNonce(key, cha.Nonce)
	hello, _ := json.Marshal(Hello{V: 1, T: THello, PublicKey: auth.PublicKeyB64(key), Sig: sig, PairCode: code})
	a.CloseNow()
	// connection B has a different nonce: the captured hello must fail
	b, _, _ := websocket.Dial(ctx, r.url, nil)
	var chb Challenge
	_, d, _ = b.Read(ctx)
	_ = json.Unmarshal(d, &chb)
	if chb.Nonce == cha.Nonce {
		t.Fatal("nonce reused")
	}
	if raw, _ := base64.StdEncoding.DecodeString(chb.Nonce); len(raw) != 32 {
		t.Fatalf("nonce len %d", len(raw))
	}
	_ = b.Write(ctx, websocket.MessageText, hello)
	_, d, _ = b.Read(ctx)
	if !strings.Contains(string(d), "auth_failed") {
		t.Fatalf("replay accepted: %s", d)
	}
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}
