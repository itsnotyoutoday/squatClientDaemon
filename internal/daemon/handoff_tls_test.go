package daemon_test

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/daemon"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

// TLS game connections across a handoff (docs/RELOAD.md): the state travels in the file as the required
// per-session extension "tls", exactly like a real reload does (WriteBundle / ReadBundle).

func tlsGame(t *testing.T, version uint16, suite uint16, extra ...fakemush.Option) *fakemush.Server {
	opts := append([]fakemush.Option{fakemush.WithTLSConfig(func(c *tls.Config) {
		c.MinVersion, c.MaxVersion = version, version
		if suite != 0 {
			c.CipherSuites = []uint16{suite}
		}
	})}, extra...)
	return fakemush.New(t, opts...)
}

// viaFile does what a reload does with the bundle: write it (it must be 0600), read it back in "the new
// process" (an independent copy; the old one keeps its own for a rollback) and delete the file.
func viaFile(t *testing.T, dir string, h *daemon.Handoff) *daemon.Handoff {
	t.Helper()
	path := filepath.Join(dir, "handoff.json")
	if err := daemon.WriteBundle(path, h.Bundle); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", err, fi.Mode())
	}
	b, err := daemon.ReadBundle(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// the new process has its own descriptors for the same sockets (exec inheritance dups them)
	var files []*os.File
	for _, f := range h.Files {
		rc, err := f.SyscallConn()
		if err != nil {
			t.Fatal(err)
		}
		var nf *os.File
		if err := rc.Control(func(fd uintptr) {
			n, err := syscall.Dup(int(fd))
			if err != nil {
				t.Fatal(err)
			}
			nf = os.NewFile(uintptr(n), f.Name())
		}); err != nil {
			t.Fatal(err)
		}
		files = append(files, nf)
	}
	return &daemon.Handoff{Bundle: b, Files: files}
}

func TestHandoffCarriesTLSSessions(t *testing.T) {
	cases := []struct {
		name           string
		version, suite uint16
	}{
		{"tls13", tls.VersionTLS13, 0},
		{"tls12-aes128gcm", tls.VersionTLS12, tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
		{"tls12-aes256gcm", tls.VersionTLS12, tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384},
		{"tls12-chacha", tls.VersionTLS12, tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			noLeaks(t)
			dir := t.TempDir()
			e1 := startEnv(t, dir, envOpts{})
			key, _ := auth.GenerateKey()
			a := e1.clientWithKey("a", defCaps, key, true)
			game := tlsGame(t, tc.version, tc.suite)
			s := a.connect(game.Host(), game.Port(), func(w *proto.World) { w.Name = "secure"; w.TLS = true; w.TLSVerify = "insecure" })
			a.state(s.ID, connected)
			game.SendLine("before")
			last := waitLast(a, s.ID, 2).LastSeq
			peer := game.Peer()
			from := peer.RemoteAddr()
			if v, _ := peer.TLSInfo(); v != tc.version {
				t.Fatalf("negotiated %#x", v)
			}
			if plan := e1.d.ReloadPlan(); len(plan.End) != 0 || len(plan.Carry) != 1 {
				t.Fatalf("plan: %+v", plan)
			}
			game.Send([]byte("half a li"))
			time.Sleep(100 * time.Millisecond) // it is in the client's TLS buffer or the socket

			h, err := e1.d.PrepareHandoff(1)
			if err != nil {
				t.Fatal(err)
			}
			if len(h.Bundle.Sessions) != 1 || len(h.Bundle.Ended) != 0 || len(h.Bundle.Sessions[0].TLS) == 0 {
				t.Fatalf("%+v", h.Bundle)
			}
			if got := fmt.Sprintf("%v %+v %#v %s", h.Bundle.Sessions[0], h.Bundle, h.Bundle.Sessions[0].TLS, h.Bundle.Sessions[0].TLS); strings.Contains(got, "\x00") || !strings.Contains(got, "[redacted]") {
				t.Fatalf("the secret is printable: %q", got)
			}
			original := h.Bundle.Sessions[0].TLS
			game.Send([]byte("ne\r\n"), []byte("while restarting\r\n")) // waits in the socket
			e1.srv.Close()
			h2 := viaFile(t, dir, h)
			e2 := startEnvFrom(t, dir, daemon.Config{Handoff: h2})
			h2.Close()
			h.Close()
			if !bytes.Equal(original, make([]byte, len(original))) {
				t.Fatal("the old process's copy of the TLS state was not wiped")
			}
			b := e2.clientWithKey("a", defCaps, key, false)
			waitLast(b, s.ID, last+3)
			ents, _ := b.attach(s.ID, nil, 0)
			var seen []string
			for _, p := range plains(ents) {
				if strings.HasPrefix(p, "game:") {
					seen = append(seen, p)
				}
			}
			if strings.Join(seen, "|") != "game:before|game:half a line|game:while restarting" {
				t.Fatalf("scrollback: %q", seen)
			}
			if err := b.c.Send(ctx(), s.ID, "hello over tls"); err != nil {
				t.Fatal(err)
			}
			game.ExpectLine("hello over tls")
			if n := game.Connections(); n != 1 || game.Peer().RemoteAddr() != from || peer.Gone() {
				t.Fatalf("connections %d, gone=%v", n, peer.Gone())
			}
			// and a second handoff of the same session (the imported connection exports again)
			time.Sleep(50 * time.Millisecond)
			h, err = e2.d.PrepareHandoff(2)
			if err != nil || len(h.Bundle.Sessions) != 1 || len(h.Bundle.Sessions[0].TLS) == 0 {
				t.Fatalf("second handoff: %v %+v", err, h.Bundle)
			}
			e2.srv.Close()
			game.SendLine("between")
			h3 := viaFile(t, dir, h)
			e3 := startEnvFrom(t, dir, daemon.Config{Handoff: h3})
			h3.Close()
			h.Close()
			c := e3.clientWithKey("a", defCaps, key, false)
			if err := c.c.Send(ctx(), s.ID, "third"); err != nil {
				t.Fatal(err)
			}
			game.ExpectLine("third")
			ents, _ = c.attach(s.ID, nil, 0)
			if got := strings.Join(plains(ents), "|"); !strings.Contains(got, "game:between") {
				t.Fatalf("%q", got)
			}
			if game.Connections() != 1 || peer.Gone() {
				t.Fatal("the TLS connection changed")
			}
		})
	}
}

// A new process that adopted the TLS session and died before it said ready (it never read a byte): the
// old process takes the session back from its own copy of the exported state and carries on. The game
// never notices and nothing that it sent in the meantime is lost.
func TestTLSRollbackAfterNewProcessDies(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version uint16
	}{{"tls13", tls.VersionTLS13}, {"tls12", tls.VersionTLS12}} {
		t.Run(tc.name, func(t *testing.T) {
			noLeaks(t)
			dir := t.TempDir()
			e1 := startEnv(t, dir, envOpts{})
			key, _ := auth.GenerateKey()
			a := e1.clientWithKey("a", defCaps, key, true)
			game := tlsGame(t, tc.version, 0)
			s := a.connect(game.Host(), game.Port(), func(w *proto.World) { w.Name = "secure"; w.TLS = true; w.TLSVerify = "insecure" })
			a.state(s.ID, connected)
			game.SendLine("one")
			waitLast(a, s.ID, 2)
			peer := game.Peer()
			from := peer.RemoteAddr()
			game.Send([]byte("par")) // a partial line is carried too
			time.Sleep(100 * time.Millisecond)

			h, err := e1.d.PrepareHandoff(1)
			if err != nil {
				t.Fatal(err)
			}
			if len(h.Bundle.Sessions[0].TLS) == 0 {
				t.Fatal("no TLS state")
			}
			e1.srv.Close()
			// the "new process": its own copy of the file's content, its own view of the data directory
			// (the real one writes nothing before it is ready), adopts paused and then dies
			dir2 := t.TempDir()
			if err := os.CopyFS(dir2, os.DirFS(dir)); err != nil {
				t.Fatal(err)
			}
			h2 := viaFile(t, dir2, h)
			e2 := startEnvPaused(t, dir2, daemon.Config{Handoff: h2}, false)
			h2.Close()
			game.Send([]byte("tial\r\n"), []byte("sent while the new one is starting\r\n"))
			time.Sleep(200 * time.Millisecond)
			e2.stop() // dies without having read anything: a paused TLS connection closes silently
			time.Sleep(100 * time.Millisecond)
			if peer.Gone() {
				t.Fatal("the game saw the TLS connection end (close_notify or reset)")
			}

			// the old process resumes from the exported state it kept in memory
			e3 := startEnvFrom(t, dir, daemon.Config{Handoff: h})
			h.Close()
			b := e3.clientWithKey("a", defCaps, key, false)
			ents, _ := b.attach(s.ID, nil, 0)
			eventually(t, "all lines after the rollback", func() bool {
				ents, _ = b.attach(s.ID, nil, 0)
				got := strings.Join(plains(ents), "|")
				return strings.Contains(got, "game:one") && strings.Contains(got, "game:sent while the new one is starting") && strings.Contains(got, "game:partial")
			})
			// read AND write still work on the very same TLS session
			game.SendLine("afterwards")
			eventually(t, "a line after the rollback", func() bool {
				ents, _ := b.attach(s.ID, nil, 0)
				return strings.Contains(strings.Join(plains(ents), "|"), "game:afterwards")
			})
			if err := b.c.Send(ctx(), s.ID, "still here"); err != nil {
				t.Fatal(err)
			}
			game.ExpectLine("still here")
			if n := game.Connections(); n != 1 || game.Peer().RemoteAddr() != from || peer.Gone() {
				t.Fatalf("connections %d gone=%v", n, peer.Gone())
			}
		})
	}
}

// A TLS state the new process cannot take is refused before anything is adopted (the same handoff then
// works again): corrupt blob, a TLS blob on a plain session, no blob on a TLS session.
func TestTLSStateRefusedBeforeAdopting(t *testing.T) {
	noLeaks(t)
	dir := t.TempDir()
	e1 := startEnv(t, dir, envOpts{})
	key, _ := auth.GenerateKey()
	a := e1.clientWithKey("a", defCaps, key, true)
	plain := fakemush.New(t)
	game := fakemush.New(t, fakemush.WithTLS())
	sp := a.connect(plain.Host(), plain.Port(), func(w *proto.World) { w.Name = "plain" })
	ss := a.connect(game.Host(), game.Port(), func(w *proto.World) { w.Name = "secure"; w.TLS = true; w.TLSVerify = "insecure" })
	a.state(sp.ID, connected)
	a.state(ss.ID, connected)
	h, err := e1.d.PrepareHandoff(1)
	if err != nil {
		t.Fatal(err)
	}
	e1.srv.Close()
	var ti, pi int
	for i, hs := range h.Bundle.Sessions {
		if hs.ID == ss.ID {
			ti = i
		} else {
			pi = i
		}
	}
	good := append([]byte(nil), h.Bundle.Sessions[ti].TLS...)
	restore := func() { h.Bundle.Sessions[ti].TLS, h.Bundle.Sessions[pi].TLS = append([]byte(nil), good...), nil }
	try := func(what string, mod func()) {
		mod()
		if d, err := daemon.New(daemon.Config{DataDir: dir, Handoff: h}); err == nil {
			_ = d.Shutdown(ctx())
			t.Fatalf("%s: accepted", what)
		}
		restore()
	}
	try("corrupt blob", func() { h.Bundle.Sessions[ti].TLS[len(good)/2] ^= 0xff })
	try("truncated blob", func() { h.Bundle.Sessions[ti].TLS = h.Bundle.Sessions[ti].TLS[:len(good)-5] })
	try("TLS session without a blob", func() { h.Bundle.Sessions[ti].TLS = nil })
	try("blob on a plain session", func() { h.Bundle.Sessions[pi].TLS = append([]byte(nil), good...) })
	if plain.Peer().Gone() || game.Peer().Gone() {
		t.Fatal("a refused handoff closed a connection")
	}
	e2 := startEnvFrom(t, dir, daemon.Config{Handoff: h})
	h.Close()
	b := e2.clientWithKey("a", defCaps, key, false)
	if err := b.c.Send(ctx(), ss.ID, "tls ok"); err != nil {
		t.Fatal(err)
	}
	game.ExpectLine("tls ok")
}

// KeyUpdate: one that arrived while nobody was reading (it is processed by the new process after it
// resumes), one the peer sends later asking us to update too, and one sent before the handoff.
func TestTLS13KeyUpdateAcrossHandoff(t *testing.T) {
	noLeaks(t)
	dir := t.TempDir()
	e1 := startEnv(t, dir, envOpts{})
	key, _ := auth.GenerateKey()
	a := e1.clientWithKey("a", defCaps, key, true)
	game := fakemush.New(t, fakemush.WithKeyUpdate())
	s := a.connect(game.Host(), game.Port(), func(w *proto.World) { w.Name = "secure"; w.TLS = true; w.TLSVerify = "insecure" })
	a.state(s.ID, connected)
	game.SendLine("one")
	waitLast(a, s.ID, 2)
	peer := game.Peer()
	peer.KeyUpdate(true) // before the handoff
	game.SendLine("two")
	waitLast(a, s.ID, 3)
	if err := a.c.Send(ctx(), s.ID, "up one"); err != nil {
		t.Fatal(err)
	}
	game.ExpectLine("up one")

	h, err := e1.d.PrepareHandoff(1)
	if err != nil {
		t.Fatal(err)
	}
	e1.srv.Close()
	peer.KeyUpdate(true) // while nobody reads
	game.SendLine("three")
	h2 := viaFile(t, dir, h)
	e2 := startEnvFrom(t, dir, daemon.Config{Handoff: h2})
	h2.Close()
	h.Close()
	b := e2.clientWithKey("a", defCaps, key, false)
	eventually(t, "the line behind the KeyUpdate", func() bool {
		ents, _ := b.attach(s.ID, nil, 0)
		return strings.Contains(strings.Join(plains(ents), "|"), "game:three")
	})
	if err := b.c.Send(ctx(), s.ID, "up two"); err != nil { // our keys changed when we answered the update
		t.Fatal(err)
	}
	game.ExpectLine("up two")
	peer.KeyUpdate(true) // after the handoff
	game.SendLine("four")
	eventually(t, "the line after a KeyUpdate in the new process", func() bool {
		ents, _ := b.attach(s.ID, nil, 0)
		return strings.Contains(strings.Join(plains(ents), "|"), "game:four")
	})
	if err := b.c.Send(ctx(), s.ID, "up three"); err != nil {
		t.Fatal(err)
	}
	game.ExpectLine("up three")
	if game.Connections() != 1 || peer.Gone() {
		t.Fatal("the TLS connection changed")
	}
}
