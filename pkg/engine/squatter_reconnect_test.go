package engine_test

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

// connectedViaProxy returns an engine connected to the squatter through a TCP proxy (so the
// "network" can be cut), a live game session, and the proxy.
func connectedViaProxy(t *testing.T, delays []time.Duration) (*engine.Engine, *rec, engine.SessionInfo, *fakemush.Server, *proxy, *sqEnv) {
	t.Helper()
	env := newSqEnv(t)
	game := fakemush.New(t)
	px := newProxy(t, net.JoinHostPort(env.host, strconv.Itoa(env.port)))
	cfg := env.cfg("sq")
	cfg.Port = px.port
	e, r := newEngine(t, func(o *engine.Options) { o.SquatterReconnectDelays = delays })
	if err := e.AddSquatter(cfg); err != nil {
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
	return e, r, si, game, px, env
}

func hasNotice(r *rec, sub string) bool {
	for _, ev := range r.kinds(engine.EvNotice) {
		if strings.Contains(ev.Text, sub) {
			return true
		}
	}
	return false
}

// lost reports whether the "link to the squatter was lost" warning has been published.
func lost(r *rec) bool {
	for _, ev := range r.kinds(engine.EvNotice) {
		if ev.Level == engine.LevelWarn && strings.Contains(strings.ToLower(ev.Text), "squatter") {
			return true
		}
	}
	return false
}

// A lost link to the squatter (laptop sleep, Wi-Fi drop) comes back by itself and catches up.
func TestRemoteAutoReconnectAfterLoss(t *testing.T) {
	noLeaksAll(t)
	e, r, si, game, px, _ := connectedViaProxy(t, []time.Duration{60 * time.Millisecond})
	px.cut()
	r.until(t, "loss notice", func() bool { return lost(r) })
	game.SendLine("while you were away") // arrives at the squatter while the link may be down
	r.until(t, "reconnected on its own", func() bool {
		st, _ := e.SquatterStatus("sq")
		cur, _ := e.Session(si.ID)
		return st.State == engine.SqConnected && cur.Link == engine.LinkAttached
	})
	r.until(t, "reconnected notice", func() bool { return hasNotice(r, "Reconnected to squatter") })
	r.waitLine(t, si.ID, "while you were away")
	game.SendLine("and now")
	r.waitLine(t, si.ID, "and now")
}

// A connection you close on purpose is never reopened behind your back.
func TestRemoteAutoReconnectNotAfterDisconnect(t *testing.T) {
	noLeaksAll(t)
	e, r, _, _, px, _ := connectedViaProxy(t, []time.Duration{300 * time.Millisecond})
	px.cut()
	r.until(t, "loss notice", func() bool { return lost(r) })
	if err := e.DisconnectSquatter("sq"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(900 * time.Millisecond)
	if st, _ := e.SquatterStatus("sq"); st.State != engine.SqDisconnected {
		t.Fatalf("reconnected after a deliberate disconnect: %+v", st)
	}
}

// Problems that need the user (revoked device) stop the retries instead of hammering the squatter.
func TestRemoteAutoReconnectStopsWhenRefused(t *testing.T) {
	noLeaksAll(t)
	e, r, _, _, px, env := connectedViaProxy(t, []time.Duration{60 * time.Millisecond})
	st, _ := e.SquatterStatus("sq")
	if err := env.as.Revoke(st.DeviceID); err != nil {
		t.Fatal(err)
	}
	px.cut()
	r.until(t, "refused", func() bool {
		cur, _ := e.SquatterStatus("sq")
		return cur.State == engine.SqRefused
	})
	time.Sleep(400 * time.Millisecond)
	if cur, _ := e.SquatterStatus("sq"); cur.State != engine.SqRefused || cur.Problem != engine.ProblemAuth {
		t.Fatalf("should stay refused: %+v", cur)
	}
}

// Waking up (or the network returning) retries at once instead of waiting out the backoff.
func TestRemoteNudgeReconnectsImmediately(t *testing.T) {
	noLeaksAll(t)
	e, r, si, _, px, _ := connectedViaProxy(t, []time.Duration{time.Hour})
	px.cut()
	r.until(t, "loss notice", func() bool { return lost(r) })
	if st, _ := e.SquatterStatus("sq"); st.State == engine.SqConnected {
		t.Fatal("expected the link to be down")
	}
	if n := e.NudgeSquatters(); n != 1 {
		t.Fatalf("nudged %d squatters, want 1", n)
	}
	r.until(t, "reconnected", func() bool {
		cur, _ := e.Session(si.ID)
		st, _ := e.SquatterStatus("sq")
		return st.State == engine.SqConnected && cur.Link == engine.LinkAttached
	})
}

// With automatic reconnection off a lost link stays down until you reconnect.
func TestRemoteAutoReconnectCanBeTurnedOff(t *testing.T) {
	noLeaksAll(t)
	e, r, _, _, px, _ := connectedViaProxy(t, []time.Duration{50 * time.Millisecond})
	e.SetAutoReconnectSquatters(false)
	if e.AutoReconnectSquatters() {
		t.Fatal("setting not applied")
	}
	px.cut()
	r.until(t, "loss notice", func() bool { return lost(r) })
	time.Sleep(400 * time.Millisecond)
	if st, _ := e.SquatterStatus("sq"); st.State == engine.SqConnected {
		t.Fatal("reconnected although turned off")
	}
	if n := e.NudgeSquatters(); n != 0 {
		t.Fatalf("nudge should do nothing when off, did %d", n)
	}
}

var _ = fakemush.New
