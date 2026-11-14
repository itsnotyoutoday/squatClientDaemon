package daemon_test

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/daemon"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tlsutil"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// startEnvFrom is startEnv for a Daemon the test built itself (here: one continuing a handoff).
func startEnvFrom(t *testing.T, dir string, cfg daemon.Config) *env {
	t.Helper()
	cfg.DataDir, cfg.FlushEvery = dir, 200*time.Millisecond
	d, err := daemon.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	as, err := auth.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := wire.NewServer(wire.ServerConfig{Backend: d, Auth: as, ServerName: "squatterd/test"})
	ts := httptest.NewTLSServer(srv.Handler())
	e := &env{t: t, dataDir: dir, d: d, as: as, srv: srv, ts: ts,
		url: "wss" + strings.TrimPrefix(ts.URL, "https") + "/swp", fp: tlsutil.Fingerprint(ts.Certificate().Raw)}
	t.Cleanup(e.stop)
	return e
}

// The "restart": the first Daemon hands everything over, a second one over the same data directory
// continues. The game's TCP connection is never closed, nothing is lost or repeated, and a client that
// reconnects replays exactly what it missed.
func TestHandoffKeepsTheGameConnection(t *testing.T) {
	noLeaks(t)
	dir := t.TempDir()
	e1 := startEnv(t, dir, envOpts{})
	key, _ := auth.GenerateKey()
	a := e1.clientWithKey("a", defCaps, key, true)
	game := fakemush.New(t)
	s := a.connect(game.Host(), game.Port(), func(w *proto.World) { w.Name = "kept" })
	a.state(s.ID, connected)
	game.SendLine("one")
	game.SendLine("two")
	last := waitLast(a, s.ID, 3).LastSeq
	if err := a.c.Ack(ctx(), s.ID, last); err != nil {
		t.Fatal(err)
	}

	h, err := e1.d.PrepareHandoff(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Bundle.Sessions) != 1 || len(h.Files) != 1 || len(h.Bundle.Ended) != 0 {
		t.Fatalf("%+v", h.Bundle)
	}
	// the old daemon is inert
	if _, err := e1.d.Connect(ctx(), nil, wire.ConnectReq{World: proto.World{Host: "x", Port: 1}}); err == nil {
		t.Fatal("a daemon that handed over accepted a new session")
	}
	// the game talks while nobody is listening: it waits in the socket buffer
	game.SendLine("while restarting")
	game.Send([]byte("half a li")) // and a line that is not complete yet

	e1.srv.Close() // the old process's wire server goes away; clients are dropped
	e2 := startEnvFrom(t, dir, daemon.Config{Handoff: h})
	h.Close()

	b := e2.clientWithKey("a", defCaps, key, false)
	var info proto.SessionInfo
	for _, i := range b.c.Sessions() {
		if i.ID == s.ID {
			info = i
		}
	}
	if info.State != proto.StateConnected || info.Name != "kept" {
		t.Fatalf("session after the restart: %+v", info)
	}
	game.Send([]byte("ne\r\n"))
	waitLast(b, s.ID, last+3) // the restart note, the line sent meanwhile, the completed half line
	entries, _ := b.attach(s.ID, u64(last), 0)
	got := plains(entries)
	want := []string{"system:*** squatterd was restarted; the connection to the game was kept", "game:while restarting", "game:half a line"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("replay after the restart:\n got %q\nwant %q", got, want)
	}
	// input goes out over the very same connection
	if err := b.c.Send(ctx(), s.ID, "hello again"); err != nil {
		t.Fatal(err)
	}
	game.ExpectLine("hello again")
	// no gap, no repeat in the whole scrollback
	all, _ := b.attach(s.ID, nil, 0)
	var seen []string
	for _, p := range plains(all) {
		if strings.HasPrefix(p, "game:") {
			seen = append(seen, p)
		}
	}
	if strings.Join(seen, "|") != "game:one|game:two|game:while restarting|game:half a line" {
		t.Fatalf("scrollback: %q", seen)
	}
}

// A TLS connection to the game cannot be handed over: it ends with a clear reason, the plain one lives.
func TestHandoffEndsTLSSessionsOnly(t *testing.T) {
	noLeaks(t)
	dir := t.TempDir()
	e1 := startEnv(t, dir, envOpts{})
	key, _ := auth.GenerateKey()
	a := e1.clientWithKey("a", defCaps, key, true)
	plain := fakemush.New(t)
	secure := fakemush.New(t, fakemush.WithTLS())
	sp := a.connect(plain.Host(), plain.Port(), func(w *proto.World) { w.Name = "plain" })
	ss := a.connect(secure.Host(), secure.Port(), func(w *proto.World) { w.Name = "secure"; w.TLS = true; w.TLSVerify = "insecure" })
	a.state(sp.ID, connected)
	a.state(ss.ID, connected)

	h, err := e1.d.PrepareHandoff(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Bundle.Sessions) != 1 || h.Bundle.Sessions[0].ID != sp.ID || len(h.Bundle.Ended) != 1 || h.Bundle.Ended[0] != ss.ID {
		t.Fatalf("%+v", h.Bundle)
	}
	e1.srv.Close()
	e2 := startEnvFrom(t, dir, daemon.Config{Handoff: h})
	h.Close()
	b := e2.clientWithKey("a", defCaps, key, false)
	for _, i := range b.c.Sessions() {
		switch i.ID {
		case sp.ID:
			if i.State != proto.StateConnected {
				t.Fatalf("plain session: %+v", i)
			}
		case ss.ID:
			if i.State != proto.StateEnded || i.EndReason != proto.EndSquatterRestarted {
				t.Fatalf("tls session: %+v", i)
			}
		}
	}
}

// A Daemon started without a handoff still marks live sessions found on disk as stopped.
func TestWithoutHandoffLiveSessionsAreStopped(t *testing.T) {
	noLeaks(t)
	dir := t.TempDir()
	e1 := startEnv(t, dir, envOpts{})
	key, _ := auth.GenerateKey()
	a := e1.clientWithKey("a", defCaps, key, true)
	game := fakemush.New(t)
	s := a.connect(game.Host(), game.Port(), nil)
	a.state(s.ID, connected)
	h, err := e1.d.PrepareHandoff(1)
	if err != nil {
		t.Fatal(err)
	}
	h.Close() // the new process never got the sockets
	e1.srv.Close()
	e2 := startEnvFrom(t, dir, daemon.Config{})
	b := e2.clientWithKey("a", defCaps, key, false)
	for _, i := range b.c.Sessions() {
		if i.ID == s.ID && (i.State != proto.StateEnded || i.EndReason != proto.EndSquatterStopped) {
			t.Fatalf("%+v", i)
		}
	}
}
