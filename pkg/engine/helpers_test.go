package engine_test

import (
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

type rec struct {
	mu   sync.Mutex
	evs  []engine.Event
	done chan struct{}
}

func collect(t *testing.T, e *engine.Engine) *rec {
	t.Helper()
	ch, cancel := e.Subscribe()
	r := &rec{done: make(chan struct{})}
	go func() {
		defer close(r.done)
		for ev := range ch {
			r.mu.Lock()
			r.evs = append(r.evs, ev)
			r.mu.Unlock()
		}
	}()
	t.Cleanup(cancel)
	return r
}

func (r *rec) snapshot() []engine.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]engine.Event(nil), r.evs...)
}

func (r *rec) lines(sid string) []proto.Line {
	var out []proto.Line
	for _, ev := range r.snapshot() {
		if ev.Kind == engine.EvLine && ev.Session == sid {
			out = append(out, *ev.Line)
		}
	}
	return out
}

func (r *rec) until(t *testing.T, what string, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s; events=%d", what, len(r.snapshot()))
}

func (r *rec) waitLine(t *testing.T, sid, sub string) proto.Line {
	t.Helper()
	var found proto.Line
	r.until(t, "line containing "+sub, func() bool {
		for _, l := range r.lines(sid) {
			if strings.Contains(l.Plain(), sub) {
				found = l
				return true
			}
		}
		return false
	})
	return found
}

func (r *rec) has(sid, sub string) bool {
	for _, l := range r.lines(sid) {
		if strings.Contains(l.Plain(), sub) {
			return true
		}
	}
	return false
}

func (r *rec) waitState(t *testing.T, e *engine.Engine, sid, state string) engine.SessionInfo {
	t.Helper()
	var si engine.SessionInfo
	r.until(t, "state "+state, func() bool {
		var err error
		si, err = e.Session(sid)
		return err == nil && si.State == state
	})
	return si
}

func newEngine(t *testing.T, mod ...func(*engine.Options)) (*engine.Engine, *rec) {
	t.Helper()
	o := engine.Options{ConfigDir: t.TempDir()}
	for _, m := range mod {
		m(&o)
	}
	e := engine.New(o)
	r := collect(t, e)
	t.Cleanup(e.Close)
	return e, r
}

func dial(t *testing.T, e *engine.Engine, r *rec, srv *fakemush.Server, login *engine.Login, pw string) engine.SessionInfo {
	t.Helper()
	si, err := e.Dial(engine.World{ID: "w1", Name: "test", Host: srv.Host(), Port: srv.Port(), Login: login}, pw)
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	return si
}

// leakCheck fails when engine/conn goroutines outlive the test.
func leakCheck(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		buf := make([]byte, 1<<20)
		buf = buf[:runtime.Stack(buf, true)]
		var bad []string
		for _, g := range strings.Split(string(buf), "\n\n") {
			if (strings.Contains(g, "pkg/engine.") || strings.Contains(g, "pkg/conn.")) &&
				!strings.Contains(g, "testing.") && !strings.Contains(g, "leakCheck") {
				bad = append(bad, g)
			}
		}
		if len(bad) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine leak:\n%s", strings.Join(bad, "\n\n"))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func itoa(i int) string { return strconvItoa(i) }
