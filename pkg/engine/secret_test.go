package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

func secretFilesClean(t *testing.T, dir, secret string) {
	t.Helper()
	time.Sleep(400 * time.Millisecond) // let the daemon flush its log
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if b, _ := os.ReadFile(path); strings.Contains(string(b), secret) {
			t.Errorf("secret %q found in daemon file %s", secret, path)
		}
		return nil
	})
}

// Typing a password line by hand (not via the saved login) through a squatter
// must reach the game but never be echoed, recorded, replayed or logged by it.
func TestTypedPasswordLineNeverRecordedBySquatter(t *testing.T) {
	noLeaksAll(t)
	env := newSqEnv(t)
	game := fakemush.New(t, fakemush.WithGreeting("Welcome\r\n"))
	e, r := newEngine(t, withStartup(t))
	_ = e.LoadScripts()
	if err := e.AddSquatter(env.cfg("sq")); err != nil {
		t.Fatal(err)
	}
	if err := e.ConnectSquatter(ctxT(t), "sq"); err != nil {
		t.Fatal(err)
	}
	si, err := e.Dial(env.world("g", game.Host(), game.Port(), nil), "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	r.waitLine(t, si.ID, "Welcome")

	for _, in := range []string{"connect bob typed-pw-1", `connect "Bob Two" typed-pw-2`, "@password old=typed-pw-3", "/secret anything-pw-4"} {
		if err := e.Input(si.ID, in); err != nil {
			t.Fatal(err)
		}
	}
	game.ExpectLine("connect bob typed-pw-1") // the game receives every one of them intact
	game.ExpectLine(`connect "Bob Two" typed-pw-2`)
	game.ExpectLine("@password old=typed-pw-3")
	game.ExpectLine("anything-pw-4")

	// ordinary text is still echoed by the daemon (and still reaches the game)
	if err := e.Input(si.ID, "say hello"); err != nil {
		t.Fatal(err)
	}
	game.ExpectLine("say hello")
	r.waitLine(t, si.ID, "say hello")

	// the user sees masked echoes and never the passwords
	r.waitLine(t, si.ID, "connect bob "+"********")
	for _, l := range r.lines(si.ID) {
		if strings.Contains(l.Plain(), "typed-pw") || strings.Contains(l.Plain(), "anything-pw") {
			t.Fatalf("password shown in the output: %q", l.Plain())
		}
	}
	ls, _ := e.Scrollback(si.ID, 0, 1000)
	for _, l := range ls {
		if strings.Contains(l.Plain(), "typed-pw") || strings.Contains(l.Plain(), "anything-pw") {
			t.Fatalf("password in scrollback: %q", l.Plain())
		}
	}
	secretFilesClean(t, env.dir, "typed-pw")
	secretFilesClean(t, env.dir, "anything-pw")
}

// Direct connections: the game gets the real line, the display gets the masked one.
func TestTypedPasswordLineMaskedOnDirectConnections(t *testing.T) {
	game := fakemush.New(t, fakemush.WithGreeting("Welcome\r\n"))
	e, r := newEngine(t)
	si, err := e.Dial(engine.World{Name: "d", Host: game.Host(), Port: game.Port(), Encoding: "utf-8", Via: "direct"}, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	r.waitLine(t, si.ID, "Welcome")
	if err := e.Input(si.ID, "connect carol direct-pw"); err != nil {
		t.Fatal(err)
	}
	game.ExpectLine("connect carol direct-pw")
	r.waitLine(t, si.ID, "connect carol ********")
	for _, l := range r.lines(si.ID) {
		if strings.Contains(l.Plain(), "direct-pw") {
			t.Fatalf("password shown: %q", l.Plain())
		}
	}
}
