package engine_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

type capEnv struct {
	e   *engine.Engine
	r   *rec
	sid string
	sim *sim
	srv *fakemush.Server
}

func directEnv(t *testing.T, mod ...func(*engine.Options)) capEnv {
	t.Helper()
	t.Cleanup(func() { leakCheck(t) })
	srv := fakemush.New(t)
	e, r := newEngine(t, mod...)
	si := dial(t, e, r, srv, nil, "")
	return capEnv{e, r, si.ID, startSim(t, srv), srv}
}

func squatEnv(t *testing.T, mod ...func(*engine.Options)) capEnv {
	t.Helper()
	noLeaksAll(t)
	env := newSqEnv(t)
	game := fakemush.New(t)
	e, r := env.connected(t, mod...)
	si, err := e.Dial(env.world("g", game.Host(), game.Port(), nil), "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	return capEnv{e, r, si.ID, startSim(t, game), game}
}

// both runs f against a direct session and a squatter-held session.
func both(t *testing.T, f func(t *testing.T, c capEnv)) {
	t.Run("direct", func(t *testing.T) { f(t, directEnv(t)) })
	t.Run("squatter", func(t *testing.T) { f(t, squatEnv(t)) })
}

func capPlains(ls []proto.Line) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Plain())
	}
	return out
}

// shown returns the plain text of everything the user's output received.
func (c capEnv) shown() []string { return capPlains(c.r.lines(c.sid)) }

func (c capEnv) assertClean(t *testing.T) {
	t.Helper()
	for _, l := range c.r.lines(c.sid) {
		if strings.Contains(l.Plain(), "SQUATCAP") {
			t.Fatalf("capture text leaked into the user's output: %q", l.Plain())
		}
		if l.Kind == proto.KindEcho {
			t.Fatalf("capture command echoed to the user: %q", l.Plain())
		}
	}
	// scrollback too
	sb, err := c.e.Scrollback(c.sid, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range sb {
		if strings.Contains(l.Plain(), "SQUATCAP") {
			t.Fatalf("capture text stored in scrollback: %q", l.Plain())
		}
	}
}

func TestCaptureSilentInterleavedChatter(t *testing.T) {
	both(t, func(t *testing.T, c capEnv) {
		c.sim.onCmd = func(cmd string) []string {
			switch {
			case strings.HasPrefix(cmd, "think SQUATCAP-") && strings.HasSuffix(cmd, "-BEGIN"):
				return []string{"Ann pages: before"}
			case cmd == "think hello":
				return []string{"Bob says, \"mid chatter\""}
			}
			return nil
		}
		got, err := c.e.Capture(ctxT(t), c.sid, []string{"think hello", "think world"}, engine.CaptureOpts{
			Accept: func(p string) bool { return p == "hello" || p == "world" }})
		if err != nil {
			t.Fatal(err)
		}
		if p := capPlains(got); len(p) != 2 || p[0] != "hello" || p[1] != "world" {
			t.Fatalf("captured %q", p)
		}
		cmds := c.sim.commands()
		if len(cmds) != 4 || !strings.HasPrefix(cmds[0], "think SQUATCAP-") || !strings.HasSuffix(cmds[0], "-BEGIN") ||
			cmds[1] != "think hello" || cmds[2] != "think world" || !strings.HasSuffix(cmds[3], "-END") {
			t.Fatalf("commands sent: %q", cmds)
		}
		c.r.waitLine(t, c.sid, "Ann pages: before")
		c.r.waitLine(t, c.sid, "mid chatter")
		for _, s := range c.shown() {
			if s == "hello" || s == "world" {
				t.Fatalf("captured line displayed: %q", s)
			}
		}
		c.assertClean(t)
	})
}

func TestCaptureAcceptFilterKeepsChatterVisible(t *testing.T) {
	c := directEnv(t)
	c.sim.onCmd = func(cmd string) []string {
		if cmd == "think mine" {
			return []string{"Ann pages: not yours"}
		}
		return nil
	}
	got, err := c.e.Capture(ctxT(t), c.sid, []string{"think mine"}, engine.CaptureOpts{
		Accept: func(p string) bool { return p == "mine" }})
	if err != nil || len(got) != 1 || got[0].Plain() != "mine" {
		t.Fatalf("got %v %v", capPlains(got), err)
	}
	c.r.waitLine(t, c.sid, "not yours")
	c.assertClean(t)
}

func TestCaptureVisibleShowsLinesNotMarkers(t *testing.T) {
	c := directEnv(t)
	got, err := c.e.Capture(ctxT(t), c.sid, []string{"think shown"}, engine.CaptureOpts{Visible: true})
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", capPlains(got), err)
	}
	c.r.waitLine(t, c.sid, "shown")
	c.assertClean(t)
}

func TestCaptureMarkerCommands(t *testing.T) {
	c := directEnv(t)
	for _, cmd := range []string{"@pemit me=", "think"} {
		got, err := c.e.Capture(ctxT(t), c.sid, []string{"think x"}, engine.CaptureOpts{MarkerCmd: cmd})
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: %v %v", cmd, capPlains(got), err)
		}
	}
	if !strings.HasPrefix(c.sim.commands()[0], "@pemit me=SQUATCAP-") {
		t.Fatalf("marker command: %q", c.sim.commands()[0])
	}
	// per-session default: say echoes inside a sentence, still recognised
	if err := c.e.SetCaptureMarker(c.sid, "say"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.e.Capture(ctxT(t), c.sid, nil, engine.CaptureOpts{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.sim.commands()[len(c.sim.commands())-2], "say SQUATCAP-") {
		t.Fatalf("session default ignored: %q", c.sim.commands())
	}
	c.assertClean(t)
}

func TestCaptureNoTriggersOnCapturedLines(t *testing.T) {
	both(t, func(t *testing.T, c capEnv) {
		_ = c.e.Input(c.sid, "/def -t'*zzzmatch*' fire = fired")
		if _, err := c.e.Capture(ctxT(t), c.sid, []string{"think a zzzmatch b"}, engine.CaptureOpts{}); err != nil {
			t.Fatal(err)
		}
		c.sim.p.SendLine("control zzzmatch line") // outside a capture: fires
		c.r.waitLine(t, c.sid, "control zzzmatch line")
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			n := 0
			for _, s := range c.sim.commands() {
				if s == "fired" {
					n++
				}
			}
			if n == 1 {
				time.Sleep(100 * time.Millisecond)
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		n := 0
		for _, s := range c.sim.commands() {
			if s == "fired" {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("trigger fired %d times (want exactly 1, from the control line): %q", n, c.sim.commands())
		}
	})
}

func TestCaptureTimeoutRestoresOutput(t *testing.T) {
	both(t, func(t *testing.T, c capEnv) {
		c.sim.mu.Lock()
		c.sim.blackhole = true
		c.sim.mu.Unlock()
		_, err := c.e.Capture(ctxT(t), c.sid, []string{"think x"}, engine.CaptureOpts{Timeout: 150 * time.Millisecond, Token: "SQUATCAP-late"})
		if !errors.Is(err, engine.ErrCaptureTimeout) {
			t.Fatalf("want ErrCaptureTimeout, got %v", err)
		}
		// late markers are still swallowed ...
		c.sim.p.SendLine("SQUATCAP-late-BEGIN")
		c.sim.p.SendLine("SQUATCAP-late-END")
		// ... and ordinary output flows again, and a new capture works
		c.sim.p.SendLine("Ann pages: after")
		c.r.waitLine(t, c.sid, "Ann pages: after")
		c.sim.mu.Lock()
		c.sim.blackhole = false
		c.sim.mu.Unlock()
		got, err := c.e.Capture(ctxT(t), c.sid, []string{"think again"}, engine.CaptureOpts{})
		if err != nil || len(got) != 1 || got[0].Plain() != "again" {
			t.Fatalf("after timeout: %v %v", capPlains(got), err)
		}
		c.assertClean(t)
	})
}

func TestCaptureSessionEndsMidCapture(t *testing.T) {
	both(t, func(t *testing.T, c capEnv) {
		c.sim.mu.Lock()
		c.sim.blackhole = true
		c.sim.mu.Unlock()
		errc := make(chan error, 1)
		go func() {
			_, err := c.e.Capture(ctxT(t), c.sid, []string{"think x"}, engine.CaptureOpts{Timeout: 10 * time.Second})
			errc <- err
		}()
		c.r.until(t, "BEGIN sent", func() bool { return len(c.sim.commands()) >= 2 })
		c.srv.CloseConn()
		select {
		case err := <-errc:
			if !errors.Is(err, engine.ErrCaptureSessionEnded) {
				t.Fatalf("want ErrCaptureSessionEnded, got %v", err)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("capture did not return after the session ended")
		}
		if _, err := c.e.Capture(ctxT(t), c.sid, nil, engine.CaptureOpts{}); err == nil {
			t.Fatal("capture on an ended session must fail")
		}
	})
}

func TestCaptureQueuesFIFO(t *testing.T) {
	both(t, func(t *testing.T, c capEnv) {
		c.sim.onCmd = func(cmd string) []string {
			if cmd == "think slow" {
				time.Sleep(200 * time.Millisecond)
			}
			return nil
		}
		var wg sync.WaitGroup
		res := make([][]string, 3)
		errs := make([]error, 3)
		first := make(chan struct{})
		run := func(i int, cmd string) {
			defer wg.Done()
			got, err := c.e.Capture(ctxT(t), c.sid, []string{"think " + cmd}, engine.CaptureOpts{})
			res[i], errs[i] = capPlains(got), err
		}
		wg.Add(1)
		go func() { close(first); run(0, "slow") }()
		<-first
		c.r.until(t, "first capture started", func() bool { return len(c.sim.commands()) >= 1 })
		wg.Add(1)
		go run(1, "second")
		time.Sleep(30 * time.Millisecond)
		wg.Add(1)
		go run(2, "third")
		wg.Wait()
		for i, want := range []string{"slow", "second", "third"} {
			if errs[i] != nil || len(res[i]) != 1 || res[i][0] != want {
				t.Fatalf("capture %d: %v %v", i, res[i], errs[i])
			}
		}
		var order []string
		for _, s := range c.sim.commands() {
			switch {
			case strings.HasSuffix(s, "-BEGIN"):
				order = append(order, "B")
			case strings.HasSuffix(s, "-END"):
				order = append(order, "E")
			}
		}
		if strings.Join(order, "") != "BEBEBE" {
			t.Fatalf("captures overlapped on the wire: %v (%q)", order, c.sim.commands())
		}
		cm := c.sim.commands()
		if !(idx(cm, "think slow") < idx(cm, "think second") && idx(cm, "think second") < idx(cm, "think third")) {
			t.Fatalf("not FIFO: %q", cm)
		}
		c.assertClean(t)
	})
}

func idx(l []string, s string) int {
	for i, x := range l {
		if x == s {
			return i
		}
	}
	return -1
}

func TestCaptureContextCancelWhileQueuedAndRunning(t *testing.T) {
	c := directEnv(t)
	c.sim.mu.Lock()
	c.sim.blackhole = true
	c.sim.mu.Unlock()
	ctx1, cancel1 := context.WithCancel(context.Background())
	e1 := make(chan error, 1)
	go func() {
		_, err := c.e.Capture(ctx1, c.sid, []string{"think a"}, engine.CaptureOpts{Timeout: 10 * time.Second})
		e1 <- err
	}()
	c.r.until(t, "first running", func() bool { return len(c.sim.commands()) >= 2 })
	ctx2, cancel2 := context.WithCancel(context.Background())
	e2 := make(chan error, 1)
	go func() {
		_, err := c.e.Capture(ctx2, c.sid, []string{"think b"}, engine.CaptureOpts{Timeout: 10 * time.Second})
		e2 <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel2() // queued one leaves without ever sending
	if err := <-e2; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancel: %v", err)
	}
	cancel1()
	if err := <-e1; !errors.Is(err, context.Canceled) {
		t.Fatalf("running cancel: %v", err)
	}
	for _, s := range c.sim.commands() {
		if s == "think b" {
			t.Fatal("cancelled queued capture was sent")
		}
	}
	// the queue is free again
	c.sim.mu.Lock()
	c.sim.blackhole = false
	c.sim.mu.Unlock()
	if _, err := c.e.Capture(ctxT(t), c.sid, []string{"think c"}, engine.CaptureOpts{}); err != nil {
		t.Fatal(err)
	}
	c.assertClean(t)
}

func TestCaptureRejectsUnsafeInput(t *testing.T) {
	c := directEnv(t)
	for _, bad := range []string{"think a\nquit", "think a\rquit", "think \x00", "think \x1b[2J"} {
		if _, err := c.e.Capture(ctxT(t), c.sid, []string{bad}, engine.CaptureOpts{}); !errors.Is(err, engine.ErrCaptureInput) {
			t.Fatalf("%q: want ErrCaptureInput, got %v", bad, err)
		}
	}
	if _, err := c.e.Capture(ctxT(t), c.sid, nil, engine.CaptureOpts{MarkerCmd: "think\nquit"}); !errors.Is(err, engine.ErrCaptureInput) {
		t.Fatalf("marker cmd: %v", err)
	}
	if _, err := c.e.Capture(ctxT(t), "nope", nil, engine.CaptureOpts{}); !errors.Is(err, engine.ErrNoSession) {
		t.Fatalf("unknown session: %v", err)
	}
	if n := len(c.sim.commands()); n != 0 {
		t.Fatalf("unsafe input reached the game: %q", c.sim.commands())
	}
}

func TestCaptureNotInTypedInputPath(t *testing.T) {
	// the engine records nothing for typed input itself; this guards that
	// Capture neither echoes (kind echo) nor goes through Input hooks.
	c := directEnv(t)
	_ = c.e.Input(c.sid, "/def -t'*' catchall = say CAUGHT") // would hit every game line
	if _, err := c.e.Capture(ctxT(t), c.sid, []string{"think q"}, engine.CaptureOpts{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	for _, s := range c.sim.commands() {
		if strings.HasPrefix(s, "say CAUGHT") {
			t.Fatalf("trigger saw capture output: %q", c.sim.commands())
		}
	}
	c.assertClean(t)
}
