package daemon_test

import (
	"context"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/daemon"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tlsutil"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

const wait = 8 * time.Second

// noLeaks fails the test if goroutines started during it are still running at the end.
// Call it first so its cleanup runs last.
func noLeaks(t *testing.T) {
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

type env struct {
	t       *testing.T
	dataDir string
	d       *daemon.Daemon
	as      *auth.Store
	srv     *wire.Server
	ts      *httptest.Server
	url     string
	fp      string
	stopped bool
}

type envOpts struct {
	queueLimit int
	maxSess    int
}

func newEnv(t *testing.T, o ...envOpts) *env {
	t.Helper()
	var op envOpts
	if len(o) > 0 {
		op = o[0]
	}
	return startEnv(t, t.TempDir(), op)
}

func startEnv(t *testing.T, dir string, op envOpts) *env {
	t.Helper()
	d, err := daemon.New(daemon.Config{DataDir: dir, MaxSessions: op.maxSess, FlushEvery: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	as, err := auth.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := wire.NewServer(wire.ServerConfig{Backend: d, Auth: as, ServerName: "squatterd/test", QueueLimit: op.queueLimit})
	ts := httptest.NewTLSServer(srv.Handler())
	e := &env{t: t, dataDir: dir, d: d, as: as, srv: srv, ts: ts,
		url: "wss" + strings.TrimPrefix(ts.URL, "https") + "/swp", fp: tlsutil.Fingerprint(ts.Certificate().Raw)}
	t.Cleanup(e.stop)
	return e
}

// stop shuts the daemon down like squatterd does (daemon first, then wire).
func (e *env) stop() {
	if e.stopped {
		return
	}
	e.stopped = true
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if err := e.d.Shutdown(ctx); err != nil {
		e.t.Errorf("shutdown: %v", err)
	}
	e.srv.Close()
	e.ts.Close()
}

// tc is a test client that records every event.
type tc struct {
	t      *testing.T
	c      *wire.Client
	name   string
	events []wire.Event
}

func (e *env) client(name string, caps proto.Caps) *tc {
	e.t.Helper()
	key, err := auth.GenerateKey()
	if err != nil {
		e.t.Fatal(err)
	}
	return e.clientWithKey(name, caps, key, true)
}

func (e *env) clientWithKey(name string, caps proto.Caps, key []byte, pair bool) *tc {
	e.t.Helper()
	opts := wire.ClientOptions{Key: key, DeviceName: name, Caps: caps, PinnedFingerprint: e.fp}
	if pair {
		code, _, err := e.as.NewPairing()
		if err != nil {
			e.t.Fatal(err)
		}
		opts.PairCode = code
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	c, err := wire.Dial(ctx, e.url, opts)
	if err != nil {
		e.t.Fatalf("dial %s: %v", name, err)
	}
	e.t.Cleanup(func() { c.Close() })
	return &tc{t: e.t, c: c, name: name}
}

// expect reads events until pred matches (recording all of them).
func (c *tc) expect(what string, pred func(wire.Event) bool) wire.Event {
	c.t.Helper()
	timeout := time.After(wait)
	for {
		select {
		case ev, ok := <-c.c.Events():
			if !ok {
				c.t.Fatalf("%s: connection closed while waiting for %s", c.name, what)
			}
			c.events = append(c.events, ev)
			if pred(ev) {
				return ev
			}
		case <-timeout:
			c.t.Fatalf("%s: timeout waiting for %s; got %d events", c.name, what, len(c.events))
		}
	}
}

func (c *tc) state(id string, pred func(proto.SessionInfo) bool) proto.SessionInfo {
	c.t.Helper()
	ev := c.expect("session.state "+id, func(ev wire.Event) bool {
		s, ok := ev.(wire.SessionState)
		return ok && s.Info.ID == id && pred(s.Info)
	})
	return ev.(wire.SessionState).Info
}

func ctx() context.Context {
	c, cancel := context.WithTimeout(context.Background(), wait)
	_ = cancel // released with the test process; contexts are short-lived
	return c
}

func (c *tc) connect(host string, port int, mod func(*proto.World)) proto.SessionInfo {
	c.t.Helper()
	w := proto.World{Name: "w", Host: host, Port: port}
	if mod != nil {
		mod(&w)
	}
	info, err := c.c.Connect(ctx(), w)
	if err != nil {
		c.t.Fatalf("connect: %v", err)
	}
	return info
}

// attach attaches and collects the whole replay; it returns the entries and the Attached event.
func (c *tc) attach(id string, since *uint64, max int) ([]wire.Entry, wire.Attached) {
	c.t.Helper()
	if err := c.c.Attach(ctx(), id, since, max); err != nil {
		c.t.Fatal(err)
	}
	att := c.expect("attached", func(ev wire.Event) bool { _, ok := ev.(wire.Attached); return ok }).(wire.Attached)
	var out []wire.Entry
	for {
		bl := c.expect("backlog", func(ev wire.Event) bool { _, ok := ev.(wire.Backlog); return ok }).(wire.Backlog)
		out = append(out, bl.Entries...)
		if bl.Done {
			return out, att
		}
	}
}

// line waits for a live line whose plain text equals text.
func (c *tc) line(text string) proto.Line {
	c.t.Helper()
	ev := c.expect("line "+text, func(ev wire.Event) bool {
		l, ok := ev.(wire.Line)
		return ok && l.Plain() == text
	})
	return ev.(wire.Line).Line
}

func plains(es []wire.Entry) []string {
	var out []string
	for _, e := range es {
		if e.Line != nil {
			out = append(out, e.Line.Kind+":"+e.Line.Plain())
		} else {
			out = append(out, "gmcp:"+e.Gmcp.Package)
		}
	}
	return out
}

func u64(n uint64) *uint64 { return &n }
