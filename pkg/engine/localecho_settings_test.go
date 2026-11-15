package engine_test

import (
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

func typedEchoes(e *engine.Engine, id, text string) int {
	n := 0
	ls, _ := e.Scrollback(id, 0, 1000)
	for _, l := range ls {
		if l.Kind == proto.KindEcho && l.Plain() == text {
			n++
		}
	}
	return n
}

// type sends a line and waits until the game got it, then lets the echo (if any) settle.
func typeAndSettle(t *testing.T, e *engine.Engine, srv *fakemush.Server, id, text string) {
	t.Helper()
	if err := e.Input(id, text); err != nil {
		t.Fatal(err)
	}
	srv.ExpectLine(text)
	time.Sleep(120 * time.Millisecond)
}

func dialNamed(t *testing.T, e *engine.Engine, r *rec, srv *fakemush.Server, name string) string {
	t.Helper()
	si, err := e.Dial(engine.World{Name: name, Host: srv.Host(), Port: srv.Port()}, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	return si.ID
}

// The persisted setting: the global default, a per-world override, a change while the session is open, and a typed /localecho that wins over it.
func TestLocalEchoSettings(t *testing.T) {
	// global off: a new session shows no echo of what is typed
	e, r := newEngine(t)
	e.SetLocalEchoDefault(false)
	srv := fakemush.New(t, fakemush.WithGreeting("Welcome\r\n"))
	id := dialNamed(t, e, r, srv, "Alpha")
	typeAndSettle(t, e, srv, id, "one")
	if n := typedEchoes(e, id, "one"); n != 0 {
		t.Fatalf("global local echo off: %d echoes", n)
	}
	// the global setting changes while the session is open: it follows
	e.SetLocalEchoDefault(true)
	typeAndSettle(t, e, srv, id, "two")
	if n := typedEchoes(e, id, "two"); n != 1 {
		t.Fatalf("global on again, open session follows: %d echoes", n)
	}
	// a world's own setting beats the global one (in both directions)
	e.SetLocalEchoDefault(false)
	e.SetWorldLocalEcho(map[string]string{"alpha": "on"})
	typeAndSettle(t, e, srv, id, "three")
	if n := typedEchoes(e, id, "three"); n != 1 {
		t.Fatalf("world on, global off: %d echoes", n)
	}
	srv2 := fakemush.New(t, fakemush.WithGreeting("Hi\r\n"))
	id2 := dialNamed(t, e, r, srv2, "Beta")
	typeAndSettle(t, e, srv2, id2, "four")
	if n := typedEchoes(e, id2, "four"); n != 0 {
		t.Fatalf("a world without its own setting follows the global (off): %d echoes", n)
	}
	// a typed /localecho wins over the settings for that session
	if err := e.SetForeground(id2); err != nil { // typed commands act on the foreground session
		t.Fatal(err)
	}
	if err := e.Input(id2, "/localecho on"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	e.SetLocalEchoDefault(false)
	e.SetWorldLocalEcho(map[string]string{"beta": "off"})
	typeAndSettle(t, e, srv2, id2, "five")
	if n := typedEchoes(e, id2, "five"); n != 1 {
		t.Fatalf("a typed /localecho on must not be undone by the settings: %d echoes", n)
	}
	// junk values in the per-world map are ignored
	e.SetWorldLocalEcho(map[string]string{"alpha": "maybe"})
	typeAndSettle(t, e, srv, id, "six")
	if n := typedEchoes(e, id, "six"); n != 0 {
		t.Fatalf("an unknown per-world value falls back to the global setting (off): %d echoes", n)
	}
}
