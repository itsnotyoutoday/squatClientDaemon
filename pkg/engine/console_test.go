package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tfengine"
)

func consoleText(e *engine.Engine) []string {
	var out []string
	for _, l := range e.ConsoleLines(0) {
		out = append(out, l.Kind+":"+l.Plain())
	}
	return out
}

func hasConsole(e *engine.Engine, want string) bool {
	for _, s := range consoleText(e) {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

// The console has the start-up output like unconnected TinyFugue: a banner, the library load and startup.tf with their lines and errors.
func TestConsoleShowsTheStartupOutput(t *testing.T) {
	lib := t.TempDir()
	if err := os.WriteFile(filepath.Join(lib, "stdlib.tf"), []byte("/echo hello from the library\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mustWrite(t, dir+"/startup.tf", "/echo hello from startup\n/nosuchcommand\n")
	e, _ := newEngine(t, func(o *engine.Options) {
		o.ConfigDir = dir
		o.Version = "9.9"
		o.TF = tfengine.Options{StdLib: lib}
	})
	_ = e.LoadScripts()
	got := consoleText(e)
	i := func(sub string) int {
		for n, s := range got {
			if strings.Contains(s, sub) {
				return n
			}
		}
		return -1
	}
	order := []string{"SquatClient 9.9", "% Loading commands from " + filepath.Join(lib, "stdlib.tf"), "hello from the library",
		"% Loading commands from " + filepath.Join(dir, "startup.tf"), "hello from startup", "nosuchcommand"}
	prev := -1
	for _, want := range order {
		n := i(want)
		if n < 0 {
			t.Fatalf("the console lacks %q:\n%s", want, strings.Join(got, "\n"))
		}
		if n <= prev {
			t.Fatalf("%q is out of order in:\n%s", want, strings.Join(got, "\n"))
		}
		prev = n
	}
}

// Typing in the console runs commands with no world and keeps the output in the console, even when a world is in front; text for a game is
// not sent anywhere.
func TestConsoleInput(t *testing.T) {
	srv := fakemush.New(t, fakemush.WithGreeting("Welcome\r\n"))
	e, r := newEngine(t)
	si := dial(t, e, r, srv, &engine.Login{Template: "connect {name} {password}", CharName: "bob"}, "pw")
	srv.ExpectLine("connect bob pw")
	r.waitLine(t, si.ID, "Welcome")
	events, cancel := e.Subscribe()
	defer cancel()
	if err := e.ConsoleInput("/echo from the console"); err != nil {
		t.Fatal(err)
	}
	if err := e.ConsoleInput("say not for a game"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if !hasConsole(e, "echo:/echo from the console") || !hasConsole(e, "system:from the console") {
		t.Fatalf("the typed command and its output are in the console: %v", consoleText(e))
	}
	if !hasConsole(e, "No world") {
		t.Fatalf("text with no world says so: %v", consoleText(e))
	}
	if r.has(si.ID, "from the console") {
		t.Fatal("the output of a console command went to the world in front")
	}
	if strings.Contains(string(srv.Received()), "not for a game") {
		t.Fatalf("console text was sent to the game: %q", srv.Received())
	}
	// the subscribers hear of new console lines
	deadline := time.After(time.Second)
	for got := false; !got; {
		select {
		case ev := <-events:
			if ev.Kind == engine.EvConsole && ev.Line != nil && strings.Contains(ev.Line.Plain(), "from the console") {
				got = true
			}
		case <-deadline:
			t.Fatal("no console event")
		}
	}
	// incremental fetch
	all := e.ConsoleLines(0)
	if len(all) < 3 || len(e.ConsoleLines(all[len(all)-1].Seq)) != 0 || len(e.ConsoleLines(all[0].Seq)) != len(all)-1 {
		t.Fatalf("ConsoleLines(after) returns what is newer: %d lines", len(all))
	}
}
