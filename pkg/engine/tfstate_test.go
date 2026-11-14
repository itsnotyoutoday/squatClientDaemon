package engine_test

import (
	"strings"
	"testing"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

func hasWindowEvent(r *rec, text string) bool {
	for _, ev := range r.snapshot() {
		if ev.Kind == engine.EvWindow && ev.Text == text {
			return true
		}
	}
	return false
}

// The engine's own history serves /recall from lines that really passed through the session.
func TestRecallServesReceivedTypedAndLocalLines(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	srv.SendLine("alpha line from the game")
	r.waitLine(t, si.ID, "alpha line from the game")
	_ = e.Input(si.ID, "say typed words")
	srv.ExpectLine("say typed words")
	_ = e.Input(si.ID, "/recall -w /2")
	r.waitLine(t, si.ID, "Recall end")
	if !r.has(si.ID, "alpha line from the game") {
		t.Fatal("/recall does not show the received line")
	}
	_ = e.Input(si.ID, "/recall -i /3")
	r.waitLine(t, si.ID, "say typed words")
}

// /limit hides lines in the live stream and in the scrollback, /unlimit brings them back.
func TestLimitFiltersLiveAndScrollback(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	srv.SendLine("keep this one")
	srv.SendLine("drop that one")
	r.waitLine(t, si.ID, "drop that one")
	_ = e.Input(si.ID, "/limit -mglob keep*")
	srv.SendLine("keep also")
	srv.SendLine("drop also")
	srv.SendLine("marker keep")
	srv.SendLine("keep marker")
	r.waitLine(t, si.ID, "keep marker")
	if r.has(si.ID, "drop also") {
		t.Fatal("a limited line was published live")
	}
	ls, _ := e.Scrollback(si.ID, 0, 100)
	for _, l := range ls {
		if strings.Contains(l.Plain(), "drop") {
			t.Fatalf("scrollback shows a limited line: %q", l.Plain())
		}
	}
	_ = e.Input(si.ID, "/unlimit")
	ls, _ = e.Scrollback(si.ID, 0, 100)
	got := false
	for _, l := range ls {
		got = got || strings.Contains(l.Plain(), "drop that one")
	}
	if !got {
		t.Fatal("/unlimit did not bring the lines back")
	}
}

// GUI mappings: typed alone, world commands open the dialog; with arguments they print text.
func TestWorldCommandsOpenTheDialogOnlyWhenTypedAlone(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	e.SetWorlds([]engine.World{{ID: "a", Name: "alpha", Host: "a.example", Port: 4000}})
	si := dial(t, e, r, srv, nil, "")
	_ = e.Input(si.ID, "/listworlds")
	r.until(t, "dialog event", func() bool { return hasWindowEvent(r, "dialog:worlds") })
	_ = e.Input(si.ID, "/listworlds -s")
	r.waitLine(t, si.ID, "alpha")
	_ = e.Input(si.ID, "/world")
	_ = e.Input(si.ID, "/addworld")
	// inside a script, a pipe or $(...) the text form is always there
	_ = e.Input(si.ID, "/eval /echo names: $(/listworlds -s)")
	r.waitLine(t, si.ID, "names: alpha")
}

// Wrapping variables reach the window.
func TestWrapCommandsEmitWindowEvents(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	_ = e.Input(si.ID, "/wrap 60")
	_ = e.Input(si.ID, "/nowrap")
	r.until(t, "wrap events", func() bool {
		return hasWindowEvent(r, "var:wrapsize=60") && hasWindowEvent(r, "var:wrap=off")
	})
}

// A script's /addworld reaches the world book the app persists.
func TestScriptWorldChangesReachThePersistenceHooks(t *testing.T) {
	var added, removed []string
	e, r := newEngine(t, func(o *engine.Options) {
		o.OnWorldAdd = func(w engine.World) error { added = append(added, w.Name); return nil }
		o.OnWorldRemove = func(n string) error { removed = append(removed, n); return nil }
	})
	srv := fakemush.New(t)
	si := dial(t, e, r, srv, nil, "")
	_ = e.Input(si.ID, "/addworld scripted 127.0.0.1 4999")
	_ = e.Input(si.ID, "/unworld scripted")
	if len(added) != 1 || added[0] != "scripted" || len(removed) != 1 || removed[0] != "scripted" {
		t.Fatalf("added=%v removed=%v", added, removed)
	}
}

// Key bindings run from the input line and may edit it.
func TestKeyBindingsAreRunByTheInputLine(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	_ = e.Input(si.ID, "/def -ib'^K' = /dokey deol")
	if b := e.TFKeyBindings(); len(b) == 0 {
		t.Fatalf("no bindings listed: %v", b)
	}
	ok, text, cur, err := e.TFKey(si.ID, "^K", "hello world", 5)
	if err != nil || !ok || text != "hello" || cur != 5 {
		t.Fatalf("%v %v %q %d", err, ok, text, cur)
	}
	if ok, _, _, _ := e.TFKey(si.ID, "^Q", "x", 1); ok {
		t.Fatal("unbound key handled")
	}
}
