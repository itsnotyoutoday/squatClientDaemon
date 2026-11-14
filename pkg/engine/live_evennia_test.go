package engine_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// TestLiveEvennia drives the TF engine against a local Evennia game (a throwaway
// test account): triggers, hilite, gag, a hook, a timer and an alias from real
// tf's lib. Run with SQUAT_LIVE_EVENNIA=1 (and the game on localhost:4392).
func TestLiveEvennia(t *testing.T) {
	if os.Getenv("SQUAT_LIVE_EVENNIA") == "" {
		t.Skip("set SQUAT_LIVE_EVENNIA=1 with a local Evennia on 4392")
	}
	e, r := newEngine(t)
	si, err := e.Dial(engine.World{Name: "evennia", Host: "localhost", Port: 4392}, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	for _, c := range []string{
		"/def -hCONNECT conn = /echo CONNECT-HOOK-RAN %*",
		"/def -t'*Market Square*' -ahCred limbo = /echo TRIGGER-FIRED",
		"/def -t'*MudInfo*' -ag nocmd",
		"/def -mregexp -t'(room)' -P1Cblue partial",
		"/def hello = /echo ALIAS-VIA-MACRO %*",
	} {
		_ = e.Input(si.ID, c)
	}
	time.Sleep(1500 * time.Millisecond)
	_ = e.Input(si.ID, "connect god layouttest")
	time.Sleep(3 * time.Second)
	_ = e.Input(si.ID, "look")
	r.waitLine(t, si.ID, "TRIGGER-FIRED")
	_ = e.Input(si.ID, "/hello there")
	r.waitLine(t, si.ID, "ALIAS-VIA-MACRO there")
	_ = e.Input(si.ID, "/repeat -0:0:1 1 /echo TIMER-FIRED")
	r.waitLine(t, si.ID, "TIMER-FIRED")
	var red, blue, gagged bool
	for _, l := range r.lines(si.ID) {
		p := l.Plain()
		if strings.Contains(p, "Market Square") && len(l.Spans) > 0 && l.Spans[0].Fg != nil {
			red = true
		}
		for _, sp := range l.Spans {
			if strings.Contains(sp.S, "room") && sp.Fg != nil {
				blue = true
			}
		}
		if strings.Contains(p, "MudInfo") {
			gagged = true
		}
	}
	t.Logf("hilite=%v partial=%v gagLeaked=%v", red, blue, gagged)
	if gagged {
		t.Errorf("a gagged line was shown")
	}
	if !red || !blue {
		t.Errorf("hilite=%v partial=%v", red, blue)
	}
	for _, l := range r.lines(si.ID) {
		t.Logf("[%s] %s", l.Kind, l.Plain())
	}
}
