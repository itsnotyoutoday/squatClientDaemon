package engine_test

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

const startup = `/def -t'PING' reply = pong
/def -t'*spam*' -g nospam
/def -t'*tells you*' -ahCgreen hear
`

func withStartup(t *testing.T) func(*engine.Options) {
	dir := t.TempDir()
	mustWrite(t, dir+"/startup.tf", startup)
	return func(o *engine.Options) { o.ConfigDir = dir }
}

func TestRemoteProbePairConnectLive(t *testing.T) {
	noLeaksAll(t)
	env := newSqEnv(t)
	game := fakemush.New(t, fakemush.WithGreeting("Welcome to the game\r\n"))

	fp, err := engine.ProbeSquatter(ctxT(t), env.host, env.port)
	if err != nil || fp != env.fp {
		t.Fatalf("probe: %q %v (want %s)", fp, err, env.fp)
	}
	// probing must not have consumed the pairing code
	cfg := env.cfg("sq")
	e, r := newEngine(t, withStartup(t))
	_ = e.LoadScripts()
	if err := e.AddSquatter(cfg); err != nil {
		t.Fatal(err)
	}
	if err := e.ConnectSquatter(ctxT(t), "sq"); err != nil {
		t.Fatal(err)
	}
	st, _ := e.SquatterStatus("sq")
	if st.State != engine.SqConnected || st.Fingerprint != env.fp || st.DeviceID == "" {
		t.Fatalf("status %+v", st)
	}
	si, err := e.Dial(env.world("g", game.Host(), game.Port(), &engine.Login{Template: "connect {name} {password}", CharName: "bob"}), "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(si.ID, "sq:") || si.Via != "sq" || si.Link != engine.LinkAttached {
		t.Fatalf("bad session info %+v", si)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	game.ExpectLine("connect bob s3cret") // login executed by the client through send
	r.waitLine(t, si.ID, "Welcome to the game")
	game.SendLine("PING")
	game.ExpectLine("pong") // live line fires the trigger
	r.waitLine(t, si.ID, "PING")

	// the daemon echo is shown once; the engine must not add its own
	if err := e.Input(si.ID, "look"); err != nil {
		t.Fatal(err)
	}
	game.ExpectLine("look")
	r.waitLine(t, si.ID, "look")
	time.Sleep(100 * time.Millisecond)
	n := 0
	ls, _ := e.Scrollback(si.ID, 0, 1000)
	for _, l := range ls {
		if l.Kind == proto.KindEcho && l.Plain() == "look" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("echo of 'look' shown %d times", n)
	}
	// the pairing code was forgotten after the handshake
	e2, _ := newEngine(t)
	cfg2 := cfg
	cfg2.ID = "again"
	if err := e2.AddSquatter(cfg2); err != nil {
		t.Fatal(err)
	}
	if err := e2.ConnectSquatter(ctxT(t), "again"); err != nil {
		t.Fatalf("same device (already paired) must connect: %v", err)
	}
}

func TestRemoteDetachedReplayNoSideEffects(t *testing.T) {
	noLeaksAll(t)
	env := newSqEnv(t)
	game := fakemush.New(t)
	e, r := env.connected(t, withStartup(t))
	_ = e.LoadScripts()
	si, err := e.Dial(env.world("g", game.Host(), game.Port(), nil), "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	game.SendLine("one")
	r.waitLine(t, si.ID, "one")
	game.SendLine("PING")
	game.ExpectLine("pong")
	r.waitLine(t, si.ID, "PING")
	before := len(r.lines(si.ID))

	if err := e.DisconnectSquatter("sq"); err != nil {
		t.Fatal(err)
	}
	cur, _ := e.Session(si.ID)
	if cur.Link != engine.LinkDetached {
		t.Fatalf("link after disconnect: %s", cur.Link)
	}
	if err := e.Input(si.ID, "nope"); err != nil { // error is shown, not returned
		t.Fatal(err)
	}
	// while detached the game keeps talking
	first := env.daemonLast(t)
	game.SendLine("PING")
	game.SendLine("buy spam now")
	game.SendLine("Ann tells you hi")
	game.SendLine("two")
	game.SendLine("three")
	env.waitDaemonLast(t, first+5)
	if len(r.lines(si.ID)) > before+2 { // + the "cannot send" system line at most
		t.Fatalf("lines arrived while detached")
	}

	if err := e.ReconnectSquatter(ctxT(t), "sq"); err != nil {
		t.Fatal(err)
	}
	r.waitLine(t, si.ID, "three")
	time.Sleep(200 * time.Millisecond)
	ls := gameLines(t, e, si.ID)
	for _, p := range []string{"one", "PING", "two", "three", "Ann tells you hi"} {
		want := 1
		if p == "PING" {
			want = 2
		}
		if c := countPlain(ls, p); c != want {
			t.Fatalf("%q appears %d times, want %d: %v", p, c, want, plains(ls))
		}
	}
	if countPlain(ls, "buy spam now") != 0 {
		t.Fatal("gagged replay line is displayed")
	}
	for _, l := range ls {
		if l.Plain() == "Ann tells you hi" && (l.Spans[0].Fg == nil || !l.Spans[0].B) {
			t.Fatalf("hilite not applied on replay: %+v", l.Spans)
		}
	}
	// the replayed PING must NOT have fired the trigger again
	time.Sleep(150 * time.Millisecond)
	if c := strings.Count(string(game.Received()), "pong"); c != 1 {
		t.Fatalf("trigger fired %d times (replay must not send)", c)
	}
	// order and seq monotonic; the missed lines are summarised
	all, _ := e.Scrollback(si.ID, 0, 1000)
	for i := 1; i < len(all); i++ {
		if all[i].Seq <= all[i-1].Seq {
			t.Fatalf("seq not increasing")
		}
	}
	r.until(t, "away summary", func() bool { return len(r.kinds(engine.EvAway)) > 0 })
	away := r.kinds(engine.EvAway)[0].Away
	if len(away) != 1 || away[0].Session != si.ID || away[0].Unread == 0 {
		t.Fatalf("away: %+v", away)
	}
	// live again after the replay, still a single copy
	game.SendLine("four")
	r.waitLine(t, si.ID, "four")
	time.Sleep(100 * time.Millisecond)
	if c := countPlain(gameLines(t, e, si.ID), "four"); c != 1 {
		t.Fatalf("four x%d", c)
	}
}

func plains(ls []proto.Line) []string {
	var o []string
	for _, l := range ls {
		o = append(o, l.Plain())
	}
	return o
}

func TestRemoteFreshEngineReplaysWithoutFiring(t *testing.T) {
	noLeaksAll(t)
	env := newSqEnv(t)
	game := fakemush.New(t)
	e1, r1 := env.connected(t, withStartup(t))
	_ = e1.LoadScripts()
	si, err := e1.Dial(env.world("g", game.Host(), game.Port(), nil), "")
	if err != nil {
		t.Fatal(err)
	}
	r1.waitState(t, e1, si.ID, proto.StateConnected)
	game.SendLine("hello")
	r1.waitLine(t, si.ID, "hello")
	_ = e1.Ack(si.ID, 1<<40)
	e1.Close() // the app closes: the session survives in the daemon
	game.SendLine("PING")
	game.SendLine("tells you late")
	env.waitDaemonLast(t, 4)

	e2, r2 := newEngine(t, withStartup(t))
	_ = e2.LoadScripts()
	cfg := env.cfg("sq")
	cfg.PairCode = "" // already paired: the key is enough
	if err := e2.AddSquatter(cfg); err != nil {
		t.Fatal(err)
	}
	if err := e2.ConnectSquatter(ctxT(t), "sq"); err != nil {
		t.Fatal(err)
	}
	sid := "sq:" + env.d.Sessions("")[0].ID
	r2.waitLine(t, sid, "tells you late")
	time.Sleep(200 * time.Millisecond)
	if strings.Contains(string(game.Received()), "pong") {
		t.Fatal("replayed PING fired the trigger")
	}
	ls := gameLines(t, e2, sid)
	if countPlain(ls, "hello") != 1 || countPlain(ls, "PING") != 1 {
		t.Fatalf("replay: %v", plains(ls))
	}
	away := e2.Away()
	if len(away) != 1 || away[0].Unread != 2 { // PING + "tells you late"; "hello" was acked
		t.Fatalf("away: %+v", away)
	}
}

func TestRemoteEndedSessions(t *testing.T) {
	noLeaksAll(t)
	env := newSqEnv(t)
	game := fakemush.New(t)
	e, r := env.connected(t)
	si, err := e.Dial(env.world("g", game.Host(), game.Port(), nil), "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	game.SendLine("last words")
	r.waitLine(t, si.ID, "last words")
	game.CloseConn()
	got := r.waitState(t, e, si.ID, proto.StateEnded)
	if got.EndReason != proto.EndRemoteClosed || got.LastText != "last words" || got.EndedAt == 0 {
		t.Fatalf("ended info %+v", got)
	}
	r.waitLine(t, si.ID, "Connection closed")
	if err := e.Dismiss(si.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Session(si.ID); err == nil {
		t.Fatal("session still listed after dismiss")
	}

	game2 := fakemush.New(t)
	si2, err := e.Dial(env.world("g2", game2.Host(), game2.Port(), nil), "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si2.ID, proto.StateConnected)
	if err := e.Disconnect(si2.ID); err != nil {
		t.Fatal(err)
	}
	got = r.waitState(t, e, si2.ID, proto.StateEnded)
	if got.EndReason != proto.EndClientQuit {
		t.Fatalf("want client_quit, got %+v", got)
	}
	if err := e.Input(si2.ID, "x"); err != nil {
		t.Fatal(err)
	}
	// ended while the app was away: summary data
	e2, r2 := newEngine(t)
	cfg := env.cfg("sq")
	cfg.PairCode = ""
	_ = e2.AddSquatter(cfg)
	if err := e2.ConnectSquatter(ctxT(t), "sq"); err != nil {
		t.Fatal(err)
	}
	r2.until(t, "away", func() bool { return len(r2.kinds(engine.EvAway)) > 0 })
	a := r2.kinds(engine.EvAway)[0].Away
	if len(a) != 1 || !a[0].Ended || a[0].EndReason != proto.EndClientQuit {
		t.Fatalf("away %+v", a)
	}
}

func TestRemoteSuspendResume(t *testing.T) {
	noLeaksAll(t)
	env := newSqEnv(t)
	game := fakemush.New(t)
	direct := fakemush.New(t)
	e, r := env.connected(t, withStartup(t))
	_ = e.LoadScripts()
	si, _ := e.Dial(env.world("g", game.Host(), game.Port(), nil), "")
	r.waitState(t, e, si.ID, proto.StateConnected)
	di, err := e.Dial(engine.World{Name: "direct", Host: direct.Host(), Port: direct.Port()}, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, di.ID, proto.StateConnected)

	_ = e.Input(di.ID, "/suspend") // typed in the DIRECT session: still detaches the squatter one
	r.until(t, "suspend + window events", func() bool {
		return len(r.kinds(engine.EvSuspend)) == 1 && len(r.kinds(engine.EvWindow)) == 1
	})
	if !e.Suspended() || !r.kinds(engine.EvSuspend)[0].Suspended || r.kinds(engine.EvWindow)[0].Text != "minimise" {
		t.Fatal("not suspended")
	}
	if cur, _ := e.Session(si.ID); cur.Link != engine.LinkDetached || cur.State != proto.StateConnected {
		t.Fatalf("squatter session: %+v", cur)
	}
	if cur, _ := e.Session(di.ID); cur.State != proto.StateConnected || cur.Link != "" {
		t.Fatalf("direct session must stay live: %+v", cur)
	}
	direct.SendLine("direct still live")
	r.waitLine(t, di.ID, "direct still live")
	game.SendLine("while suspended")
	env.waitDaemonLast(t, 2)
	time.Sleep(100 * time.Millisecond)
	if r.has(si.ID, "while suspended") {
		t.Fatal("line delivered while suspended")
	}
	if err := e.ResumeSquatters(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	r.waitLine(t, si.ID, "while suspended")
	if e.Suspended() {
		t.Fatal("still suspended")
	}
	if cur, _ := e.Session(si.ID); cur.Link != engine.LinkAttached {
		t.Fatalf("link %s", cur.Link)
	}
	if c := countPlain(gameLines(t, e, si.ID), "while suspended"); c != 1 {
		t.Fatalf("x%d", c)
	}
}

func TestDirectOnlySuspendMinimises(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	_ = e.Input(si.ID, "/detach")
	r.until(t, "window event", func() bool { return len(r.kinds(engine.EvWindow)) == 1 })
	if e.Suspended() || len(r.kinds(engine.EvSuspend)) != 0 {
		t.Fatal("nothing to detach: must not be suspended")
	}
	if cur, _ := e.Session(si.ID); cur.State != proto.StateConnected {
		t.Fatalf("direct connection must stay live: %+v", cur)
	}
	srv.SendLine("still here")
	r.waitLine(t, si.ID, "still here")
}

func TestRemoteFingerprintMismatchRefused(t *testing.T) {
	noLeaksAll(t)
	env := newSqEnv(t)
	cfg := env.cfg("sq")
	cfg.Fingerprint = strings.Repeat("AA:", 31) + "AA"
	e, r := newEngine(t)
	_ = e.AddSquatter(cfg)
	err := e.ConnectSquatter(ctxT(t), "sq")
	if err == nil {
		t.Fatal("must refuse")
	}
	st, _ := e.SquatterStatus("sq")
	if st.State != engine.SqRefused || st.Problem != engine.ProblemFingerprint || !strings.Contains(st.Message, "Refusing") {
		t.Fatalf("status %+v", st)
	}
	r.until(t, "notice", func() bool { return len(r.kinds(engine.EvNotice)) > 0 })
	// the pairing code must NOT have been sent to the impostor: a correct pin still pairs
	cfg2 := cfg
	cfg2.ID = "ok"
	cfg2.Fingerprint = env.fp
	_ = e.AddSquatter(cfg2)
	if err := e.ConnectSquatter(ctxT(t), "ok"); err != nil {
		t.Fatalf("pairing code was consumed by the refused attempt: %v", err)
	}
}

func TestRemoteUnreachableAndNoAutoReconnect(t *testing.T) {
	noLeaksAll(t)
	env := newSqEnv(t)
	game := fakemush.New(t)
	px := newProxy(t, net.JoinHostPort(env.host, strconv.Itoa(env.port)))
	cfg := env.cfg("sq")
	cfg.Port = px.port
	e, r := newEngine(t)
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
	px.cut() // the network drops
	r.until(t, "unreachable", func() bool {
		st, _ := e.SquatterStatus("sq")
		return st.State == engine.SqUnreachable
	})
	cur, _ := e.Session(si.ID)
	if cur.Link != engine.LinkUnreachable || cur.State != proto.StateConnected {
		t.Fatalf("session %+v", cur)
	}
	_ = e.Input(si.ID, "look")
	r.waitLine(t, si.ID, "unreachable")
	time.Sleep(300 * time.Millisecond) // nothing may reconnect by itself
	if st, _ := e.SquatterStatus("sq"); st.State != engine.SqUnreachable {
		t.Fatalf("state changed by itself: %+v", st)
	}
	if err := e.ReconnectSquatter(ctxT(t), "sq"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := e.Session(si.ID); cur.Link != engine.LinkAttached {
		t.Fatalf("after reconnect %+v", cur)
	}
	game.SendLine("back")
	r.waitLine(t, si.ID, "back")

	// a squatter that is not there at all
	dead := env.cfg("dead")
	dead.Host, dead.Port = "127.0.0.1", 1
	_ = e.AddSquatter(dead)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.ConnectSquatter(ctx, "dead"); err == nil {
		t.Fatal("expected failure")
	}
	if st, _ := e.SquatterStatus("dead"); st.State != engine.SqUnreachable || st.Problem != engine.ProblemUnreachable {
		t.Fatalf("%+v", st)
	}
	w := env.world("y", "127.0.0.1", 9, nil)
	w.Via = "dead"
	if _, err := e.Dial(w, ""); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("dial via dead squatter: %v", err)
	}
}

func TestRemoteRevokedDevice(t *testing.T) {
	noLeaksAll(t)
	env := newSqEnv(t)
	e, _ := env.connected(t)
	st, _ := e.SquatterStatus("sq")
	if err := env.as.Revoke(st.DeviceID); err != nil {
		t.Fatal(err)
	}
	if err := e.ReconnectSquatter(ctxT(t), "sq"); err == nil {
		t.Fatal("revoked device must be refused")
	}
	st, _ = e.SquatterStatus("sq")
	if st.State != engine.SqRefused || st.Problem != engine.ProblemAuth {
		t.Fatalf("%+v", st)
	}
}

func TestRemoteColourUTF8ByteExact(t *testing.T) {
	noLeaksAll(t)
	env := newSqEnv(t)
	viaGame := fakemush.New(t)
	directGame := fakemush.New(t)
	e, r := env.connected(t)
	vi, err := e.Dial(env.world("via", viaGame.Host(), viaGame.Port(), nil), "")
	if err != nil {
		t.Fatal(err)
	}
	di, err := e.Dial(engine.World{Name: "direct", Host: directGame.Host(), Port: directGame.Port()}, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, vi.ID, proto.StateConnected)
	r.waitState(t, e, di.ID, proto.StateConnected)
	raw := "\x1b[1;38;2;255;128;0mTrue\x1b[0m \x1b[38;5;196m256\x1b[0m \x1b[48;2;1;2;3;4mhéllo ✓ 日本\U0001F600\x1b[0m plain\r\n"
	viaGame.Send([]byte(raw))
	directGame.Send([]byte(raw))
	r.waitLine(t, vi.ID, "plain")
	r.waitLine(t, di.ID, "plain")
	a := gameLines(t, e, vi.ID)
	b := gameLines(t, e, di.ID)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("lines: %d %d", len(a), len(b))
	}
	ja, _ := json.Marshal(a[0].Spans)
	jb, _ := json.Marshal(b[0].Spans)
	if string(ja) != string(jb) {
		t.Fatalf("spans differ via squatter:\n%s\n%s", ja, jb)
	}
	want := `[{"s":"True","fg":"#ff8000","b":true},{"s":" "},{"s":"256","fg":196},{"s":" "},{"s":"héllo ✓ 日本😀","bg":"#010203","u":true},{"s":" plain"}]`
	_ = want
	if got := a[0].Plain(); got != "True 256 héllo ✓ 日本\U0001F600 plain" {
		t.Fatalf("text mangled: %q", got)
	}
	s0, s2 := a[0].Spans[0], a[0].Spans[2]
	if s0.Fg == nil || s0.Fg.Hex() != "#ff8000" || !s0.B || s2.Fg == nil || s2.Fg.IsTrueColor() || s2.Fg.Index() != 196 {
		t.Fatalf("colour spans wrong: %s", ja)
	}
	// replay (fresh engine) keeps them byte-exact too
	e2, r2 := newEngine(t)
	cfg := env.cfg("sq")
	cfg.PairCode = ""
	_ = e2.AddSquatter(cfg)
	if err := e2.ConnectSquatter(ctxT(t), "sq"); err != nil {
		t.Fatal(err)
	}
	r2.waitLine(t, vi.ID, "plain")
	c := gameLines(t, e2, vi.ID)
	jc, _ := json.Marshal(c[0].Spans)
	if string(jc) != string(ja) {
		t.Fatalf("replayed spans differ:\n%s\n%s", jc, ja)
	}
}

func TestRemoteAddValidation(t *testing.T) {
	e, _ := newEngine(t)
	if err := e.AddSquatter(engine.SquatterConfig{ID: "a:b"}); err == nil {
		t.Fatal("colon in id accepted")
	}
	if err := e.AddSquatter(engine.SquatterConfig{ID: "a"}); err == nil {
		t.Fatal("missing key accepted")
	}
	if err := e.ConnectSquatter(context.Background(), "nope"); err == nil {
		t.Fatal("unknown squatter")
	}
}
