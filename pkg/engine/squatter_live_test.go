package engine_test

import (
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// TestLiveSquatterStarTrekKin drives a REAL squatterd binary and a REAL game:
//
//	squatterd serve --data-dir D --port P --bind 127.0.0.1 &
//	squatterd pair --data-dir D            # prints the code
//	SQUAT_LIVE=1 SQUAT_LIVE_SQUATTER=127.0.0.1:P SQUAT_LIVE_PAIRCODE=<code> \
//	  go test -run TestLiveSquatterStarTrekKin -v ./pkg/engine/
//
// It pairs through the engine API, opens a session to startrekkin.net:1701
// via the squatter, receives the banner, drops the client, re-attaches with
// a new engine and checks the replay is complete and free of duplicates.
func TestLiveSquatterStarTrekKin(t *testing.T) {
	if os.Getenv("SQUAT_LIVE") == "" {
		t.Skip("set SQUAT_LIVE=1 (see the test doc)")
	}
	addr, code := os.Getenv("SQUAT_LIVE_SQUATTER"), os.Getenv("SQUAT_LIVE_PAIRCODE")
	h, p, err := net.SplitHostPort(addr)
	if err != nil || code == "" {
		t.Fatalf("SQUAT_LIVE_SQUATTER=host:port and SQUAT_LIVE_PAIRCODE are required")
	}
	port, _ := strconv.Atoi(p)
	key, err := auth.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	fp, err := engine.ProbeSquatter(ctxT(t), h, port)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("squatter TLS fingerprint (shown to the user before pairing): %s", fp)
	cfg := engine.SquatterConfig{ID: "live", Name: "live", Host: h, Port: port, Key: key, DeviceName: "engine-live-test", Fingerprint: fp, PairCode: code}

	e1, r1 := newEngine(t)
	if err := e1.AddSquatter(cfg); err != nil {
		t.Fatal(err)
	}
	if err := e1.ConnectSquatter(ctxT(t), "live"); err != nil {
		t.Fatalf("pair/connect: %v", err)
	}
	t.Logf("paired and connected: %+v", mustStatus(t, e1))
	si, err := e1.Dial(engine.World{Name: "startrekkin", Host: "startrekkin.net", Port: 1701, Via: "live"}, "")
	if err != nil {
		t.Fatal(err)
	}
	r1.waitState(t, e1, si.ID, proto.StateConnected)
	t.Logf("session %s connected via the squatter; waiting for the banner (~5s)", si.ID)
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) && len(gameLines(t, e1, si.ID)) < 5 {
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(1500 * time.Millisecond)
	live := gameLines(t, e1, si.ID)
	if len(live) < 3 {
		t.Fatalf("no banner received (%d lines)", len(live))
	}
	for _, l := range live {
		t.Logf("LIVE   %q", l.Plain())
	}

	e1.Close() // drop the client; the squatter keeps the game connection
	time.Sleep(3 * time.Second)

	cfg.PairCode = ""
	e2, r2 := newEngine(t)
	if err := e2.AddSquatter(cfg); err != nil {
		t.Fatal(err)
	}
	if err := e2.ConnectSquatter(ctxT(t), "live"); err != nil {
		t.Fatalf("reattach: %v", err)
	}
	r2.waitLine(t, si.ID, "")
	time.Sleep(1500 * time.Millisecond)
	replay := gameLines(t, e2, si.ID)
	seen := map[string]int{}
	for _, l := range replay {
		t.Logf("REPLAY %q", l.Plain())
		if pl := strings.TrimSpace(l.Plain()); pl != "" {
			seen[pl]++
		}
	}
	for _, l := range live {
		if countPlain(replay, l.Plain()) < countPlain(live, l.Plain()) {
			t.Fatalf("line lost in replay: %q", l.Plain())
		}
	}
	if len(replay) != len(live) {
		t.Fatalf("replay has %d game lines, live had %d (duplicates or gaps)", len(replay), len(live))
	}
	for i := 1; i < len(replay); i++ {
		if replay[i].Seq <= replay[i-1].Seq {
			t.Fatal("seq not increasing")
		}
	}
	if _, err := e2.Session(si.ID); err != nil {
		t.Fatal(err)
	}
	// leave the squatter clean: end and dismiss the session
	_ = e2.Dismiss(si.ID)
}

func mustStatus(t *testing.T, e *engine.Engine) engine.SquatterInfo {
	t.Helper()
	st, err := e.SquatterStatus("live")
	if err != nil {
		t.Fatal(err)
	}
	return st
}
