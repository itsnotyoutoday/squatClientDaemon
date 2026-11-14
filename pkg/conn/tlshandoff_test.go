package conn

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

func dialTLS(t *testing.T, srv *fakemush.Server, cfg Config) *Conn {
	t.Helper()
	cfg.World.TLS = true
	cfg.TLSConfig = tlsWith(srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

// A live TLS session moves to a new Conn (as a new process would, through JSON and a duplicated
// socket) without a new handshake and without the game noticing: no close_notify, same TCP connection.
func TestHandoffTLSKeepsTheSession(t *testing.T) {
	srv := fakemush.New(t, fakemush.WithTLS(), fakemush.WithGreeting("secure\r\n"))
	cfg := cfgFor(srv)
	cfg.World.TLS = true
	cfg.TLSConfig = tlsWith(srv)
	c := dialTLS(t, srv, cfg)
	if l, _ := plainLine(t, c); !strings.Contains(l, "secure") {
		t.Fatalf("%q", l)
	}
	if !c.CanHandoffTLS() {
		t.Fatal("a TLS session made by Dial must be migratable")
	}
	if _, _, err := c.Handoff(); err == nil {
		t.Fatal("plain Handoff must still refuse TLS")
	}
	for round := 0; round < 3; round++ {
		srv.Send([]byte("half a li")) // a partial line in the splitter
		time.Sleep(30 * time.Millisecond)
		st, tlsState, f, err := c.HandoffTLS()
		if err != nil {
			t.Fatalf("HandoffTLS: %v", err)
		}
		b, _ := json.Marshal(st)
		var st2 HandoffState
		if err := json.Unmarshal(b, &st2); err != nil {
			t.Fatal(err)
		}
		if err := CheckHandoffTLS(cfg, st2, tlsState); err != nil {
			t.Fatal(err)
		}
		n, err := AdoptTLSPaused(cfg, st2, tlsState, f)
		clear(tlsState)
		f.Close()
		if err != nil {
			t.Fatalf("AdoptTLSPaused: %v", err)
		}
		t.Cleanup(n.Close)
		select {
		case <-c.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("old conn not finished")
		}
		srv.Send([]byte("ne\r\n")) // arrives while the new Conn is paused: stays in the kernel buffer
		n.Resume()
		if l, _ := plainLine(t, n); l != "half a line" {
			t.Fatalf("round %d: %q", round, l)
		}
		if err := n.Send("ping"); err != nil {
			t.Fatal(err)
		}
		srv.ExpectLine("ping") // the same TLS session carries our input
		c = n
	}
	if got := srv.Peer().Received(); strings.Contains(string(got), "close") {
		t.Fatalf("unexpected: %q", got)
	}
	// ending is still reported by the new one
	srv.CloseConn()
	if ev := ended(t, c); ev.Reason == "" {
		t.Fatalf("%+v", ev)
	}
}

// Discarding a paused adopted TLS connection must not end the session (no close_notify).
func TestDiscardPausedTLSAdoptionKeepsSession(t *testing.T) {
	srv := fakemush.New(t, fakemush.WithTLS(), fakemush.WithGreeting("secure\r\n"))
	cfg := cfgFor(srv)
	cfg.World.TLS = true
	cfg.TLSConfig = tlsWith(srv)
	c := dialTLS(t, srv, cfg)
	plainLine(t, c)
	st, tlsState, f, err := c.HandoffTLS()
	if err != nil {
		t.Fatal(err)
	}
	n, err := AdoptTLSPaused(cfg, st, tlsState, f)
	if err != nil {
		t.Fatal(err)
	}
	n.Discard() // the new process refused the handoff after adopting
	// ...so a third conn can still adopt the very same session (the state is a snapshot at the
	// hand-over; nothing was read or written in between).
	n2, err := AdoptTLSPaused(cfg, st, tlsState, f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n2.Close)
	n2.Resume()
	srv.SendLine("alive")
	if l, _ := plainLine(t, n2); l != "alive" {
		t.Fatalf("%q", l)
	}
}

func TestCheckHandoffTLSRejectsCorruptState(t *testing.T) {
	srv := fakemush.New(t, fakemush.WithTLS(), fakemush.WithGreeting("secure\r\n"))
	cfg := cfgFor(srv)
	cfg.World.TLS = true
	cfg.TLSConfig = tlsWith(srv)
	c := dialTLS(t, srv, cfg)
	plainLine(t, c)
	st, tlsState, f, err := c.HandoffTLS()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	bad := append([]byte(nil), tlsState...)
	bad[len(bad)/2] ^= 1
	if err := CheckHandoffTLS(cfg, st, bad); err == nil {
		t.Fatal("corrupt TLS state accepted")
	}
	if _, err := AdoptTLSPaused(cfg, st, bad, f); err == nil {
		t.Fatal("corrupt TLS state adopted")
	}
}
