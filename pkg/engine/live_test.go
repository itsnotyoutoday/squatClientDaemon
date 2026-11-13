package engine_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// TestLiveStarTrekKin connects to a real game. Run with SQUAT_LIVE=1:
//
//	SQUAT_LIVE=1 go test -run TestLiveStarTrekKin -v ./pkg/engine/
func TestLiveStarTrekKin(t *testing.T) {
	if os.Getenv("SQUAT_LIVE") == "" {
		t.Skip("set SQUAT_LIVE=1 to connect to startrekkin.net:1701")
	}
	e, r := newEngine(t)
	si, err := e.Dial(engine.World{Name: "startrekkin", Host: "startrekkin.net", Port: 1701}, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	t.Logf("connected; waiting for the banner")
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && len(r.lines(si.ID)) < 8 {
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(2 * time.Second)
	for _, l := range r.lines(si.ID) {
		b, _ := json.Marshal(l.Spans)
		t.Logf("[%d %s] %q  spans=%s", l.Seq, l.Kind, l.Plain(), b)
	}
}
