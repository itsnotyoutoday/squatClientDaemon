package engine_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

func TestColorTestCommandShowsEveryColourMode(t *testing.T) {
	game := fakemush.New(t, fakemush.WithGreeting("hi\r\n"))
	e, r := newEngine(t)
	si, err := e.Dial(engine.World{Name: "c", Host: game.Host(), Port: game.Port()}, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	r.waitLine(t, si.ID, "hi")
	if err := e.Input(si.ID, "/colortest"); err != nil {
		t.Fatal(err)
	}
	r.waitLine(t, si.ID, "Colour test")
	// the lines arrive one by one: wait for the last one before reading them all
	r.waitLine(t, si.ID, "日本語")
	var all strings.Builder
	for _, l := range r.lines(si.ID) {
		b, _ := json.Marshal(l.Spans)
		all.Write(b)
	}
	s := all.String()
	for _, want := range []string{
		`"fg":3`,         // 16-colour yellow (the slot that looked different)
		`"bg":196`,       // 256-colour cube (index 196)
		`"bg":255`,       // top of the grey ramp
		`"bg":"#ff0000"`, // truecolor gradient start
		`"fg":"#ff8000"`, // truecolor text
		`"b":true`,       // bold
		`"st":true`,      // strike
		`"rev":true`,     // reverse
		"日本語",            // UTF-8 CJK
	} {
		if !strings.Contains(s, want) {
			t.Errorf("/colortest output lacks %s", want)
		}
	}
}
