package engine_test

import (
	"context"
	"crypto/ed25519"
	"io"
	"net"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/daemon"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tlsutil"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// sqEnv is a REAL in-process squatterd (internal/daemon + wire server over TLS).
type sqEnv struct {
	t    *testing.T
	d    *daemon.Daemon
	as   *auth.Store
	srv  *wire.Server
	ts   *httptest.Server
	fp   string
	host string
	port int
	key  ed25519.PrivateKey
}

// noLeaksAll fails the test when goroutines started during it still run at
// the end. Call it first so its cleanup runs last.
func noLeaksAll(t *testing.T) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		var n int
		for i := 0; i < 100; i++ {
			if n = runtime.NumGoroutine(); n <= before {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		buf := make([]byte, 1<<16)
		buf = buf[:runtime.Stack(buf, true)]
		t.Errorf("goroutine leak: %d before, %d after\n%s", before, n, buf)
	})
}

func newSqEnv(t *testing.T) *sqEnv {
	t.Helper()
	dir := t.TempDir()
	d, err := daemon.New(daemon.Config{DataDir: dir, FlushEvery: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	as, err := auth.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := wire.NewServer(wire.ServerConfig{Backend: d, Auth: as, ServerName: "squatterd/test"})
	ts := httptest.NewTLSServer(srv.Handler())
	u, _ := url.Parse(ts.URL)
	h, p, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(p)
	key, err := auth.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	s := &sqEnv{t: t, d: d, as: as, srv: srv, ts: ts, fp: tlsutil.Fingerprint(ts.Certificate().Raw), host: h, port: port, key: key}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = d.Shutdown(ctx)
		srv.Close()
		ts.Close()
	})
	return s
}

// cfg returns a squatter config with a fresh pairing code and the pinned fingerprint.
func (s *sqEnv) cfg(id string) engine.SquatterConfig {
	s.t.Helper()
	code, _, err := s.as.NewPairing()
	if err != nil {
		s.t.Fatal(err)
	}
	return engine.SquatterConfig{ID: id, Name: "home", Host: s.host, Port: s.port, Key: s.key, DeviceName: "test-device",
		Fingerprint: s.fp, PairCode: code}
}

// connected builds an engine, adds and connects the squatter (pairing it).
func (s *sqEnv) connected(t *testing.T, mod ...func(*engine.Options)) (*engine.Engine, *rec) {
	t.Helper()
	e, r := newEngine(t, mod...)
	if err := e.AddSquatter(s.cfg("sq")); err != nil {
		t.Fatal(err)
	}
	if err := e.ConnectSquatter(ctxT(t), "sq"); err != nil {
		t.Fatal(err)
	}
	return e, r
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func (s *sqEnv) world(name, host string, port int, login *engine.Login) engine.World {
	return engine.World{ID: "w-" + name, Name: name, Host: host, Port: port, Via: "sq", Login: login}
}

// daemonLast returns the daemon's lastSeq of its only session.
func (s *sqEnv) daemonLast(t *testing.T) uint64 {
	t.Helper()
	l := s.d.Sessions("")
	if len(l) != 1 {
		t.Fatalf("want 1 daemon session, got %d", len(l))
	}
	return l[0].LastSeq
}

func (s *sqEnv) waitDaemonLast(t *testing.T, atLeast uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.daemonLast(t) >= atLeast {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("daemon lastSeq never reached %d (is %d)", atLeast, s.daemonLast(t))
}

// gameLines returns the plain text of kind=game lines in scrollback order.
func gameLines(t *testing.T, e *engine.Engine, id string) []proto.Line {
	t.Helper()
	ls, err := e.Scrollback(id, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	var out []proto.Line
	for _, l := range ls {
		if l.Kind == proto.KindGame {
			out = append(out, l)
		}
	}
	return out
}

func countPlain(ls []proto.Line, text string) int {
	n := 0
	for _, l := range ls {
		if l.Plain() == text {
			n++
		}
	}
	return n
}

func (r *rec) kinds(k engine.EventKind) []engine.Event {
	var out []engine.Event
	for _, ev := range r.snapshot() {
		if ev.Kind == k {
			out = append(out, ev)
		}
	}
	return out
}

// proxy is a TCP forwarder in front of the daemon that the test can cut.
type proxy struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
	wg    sync.WaitGroup
	port  int
}

func newProxy(t *testing.T, target string) *proxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{ln: ln}
	p.port = ln.Addr().(*net.TCPAddr).Port
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, c, u)
			p.mu.Unlock()
			p.wg.Add(2)
			go func() { defer p.wg.Done(); _, _ = io.Copy(u, c); u.Close(); c.Close() }()
			go func() { defer p.wg.Done(); _, _ = io.Copy(c, u); c.Close(); u.Close() }()
		}
	}()
	t.Cleanup(func() { p.cut(); ln.Close(); p.wg.Wait() })
	return p
}

// cut closes every forwarded connection (new ones are still accepted).
func (p *proxy) cut() {
	p.mu.Lock()
	for _, c := range p.conns {
		c.Close()
	}
	p.conns = nil
	p.mu.Unlock()
}
