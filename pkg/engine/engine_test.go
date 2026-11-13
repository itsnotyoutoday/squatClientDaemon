package engine_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

func TestConnectLoginLinesEcho(t *testing.T) {
	srv := fakemush.New(t, fakemush.WithGreeting("Welcome\r\n"))
	e, r := newEngine(t)
	si := dial(t, e, r, srv, &engine.Login{Template: "connect {name} {password}", CharName: "bob"}, "s3cret")
	srv.ExpectLine("connect bob s3cret")
	r.waitLine(t, si.ID, "Welcome")
	srv.SendLine("hello there")
	l := r.waitLine(t, si.ID, "hello there")
	if l.Kind != proto.KindGame || l.Seq == 0 {
		t.Fatalf("bad line %+v", l)
	}
	if err := e.Input(si.ID, "look"); err != nil {
		t.Fatal(err)
	}
	srv.ExpectLine("look")
	echo := r.waitLine(t, si.ID, "look")
	if echo.Kind != proto.KindEcho {
		t.Fatalf("want echo, got %s", echo.Kind)
	}
	// the password must appear nowhere the UI can see
	for _, ev := range r.snapshot() {
		b, _ := json.Marshal(ev.Line)
		if ev.Info != nil {
			b, _ = json.Marshal(ev.Info)
		}
		if strings.Contains(string(b), "s3cret") {
			t.Fatalf("password leaked in %s", b)
		}
	}
	ls, _ := e.Scrollback(si.ID, 0, 100)
	for _, l := range ls {
		if strings.Contains(l.Plain(), "s3cret") {
			t.Fatal("password in scrollback")
		}
	}
}

func TestLoginDelay(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	start := time.Now()
	dial(t, e, r, srv, &engine.Login{Template: "connect {name} {password}", CharName: "a", Delay: 150 * time.Millisecond}, "pw")
	srv.ExpectLine("connect a pw")
	if time.Since(start) < 140*time.Millisecond {
		t.Fatal("login sent before delay")
	}
}

func TestTFMacroGagHiliteAndSend(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	for _, c := range []string{
		"/def -t'*spam*' -g nospam",
		"/def -t'*tells you*' -ahCgreen hear",
		"/def -t'PING' reply = pong",
	} {
		_ = e.Input(si.ID, c)
	}
	srv.SendLine("buy spam now")
	srv.SendLine("Ann tells you hi")
	srv.SendLine("PING")
	srv.ExpectLine("pong")
	l := r.waitLine(t, si.ID, "Ann tells you")
	if len(l.Spans) == 0 || l.Spans[0].Fg == nil || !l.Spans[0].B {
		t.Fatalf("hilite not applied: %+v", l.Spans)
	}
	r.waitLine(t, si.ID, "PING")
	if r.has(si.ID, "buy spam") {
		t.Fatal("gagged line was displayed")
	}
	if got, _ := e.Search(si.ID, "spam", engine.SearchOptions{}); len(got) != 0 {
		t.Fatalf("gagged line stored: %v", got)
	}
}

func TestLuaTriggerGagHiliteAndSend(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	_ = e.Input(si.ID, `/lua squat.trigger{match="*noise*", gag=true}`)
	_ = e.Input(si.ID, `/lua squat.trigger{match="*treasure*", fg="#ff8800", b=true}`)
	_ = e.Input(si.ID, `/lua squat.trigger{match="HP low", fn=function() squat.send("heal") end}`)
	srv.SendLine("some noise")
	srv.SendLine("a treasure chest")
	srv.SendLine("HP low")
	srv.ExpectLine("heal")
	l := r.waitLine(t, si.ID, "treasure")
	if l.Spans[0].Fg == nil || l.Spans[0].Fg.Hex() != "#ff8800" {
		t.Fatalf("truecolour hilite lost: %+v", l.Spans)
	}
	r.waitLine(t, si.ID, "HP low")
	if r.has(si.ID, "some noise") {
		t.Fatal("lua gag failed")
	}
}

func TestLuaTFRoundTrip(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	_ = e.Input(si.ID, `/lua squat.tf("/echo from tf")`)
	r.waitLine(t, si.ID, "from tf")
	_ = e.Input(si.ID, `/lua squat.tf("/lua squat.echo('nested lua')")`)
	r.waitLine(t, si.ID, "nested lua")
}

func TestTriggerEchoComesAfterLine(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	_ = e.Input(si.ID, "/def -t'ding*' dd = /echo reacted")
	srv.SendLine("dingdong")
	r.waitLine(t, si.ID, "reacted")
	ls := r.lines(si.ID)
	var a, b int
	for i, l := range ls {
		if strings.Contains(l.Plain(), "dingdong") {
			a = i
		}
		if l.Plain() == "reacted" {
			b = i
		}
	}
	if !(a < b) {
		t.Fatalf("echo must follow its line: %d %d", a, b)
	}
}

func TestReplayIsDisplayOnly(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	_ = e.Input(si.ID, "/def -t'danger*' -g -ahCred dg = flee")
	_ = e.Input(si.ID, `/lua squat.trigger{match="danger*", fn=function() squat.send("run") end, fg=1}`)
	line := proto.Line{V: 1, T: "line", Session: si.ID, Kind: proto.KindGame, Spans: []proto.Span{{S: "danger ahead"}}}
	out, gagged, err := e.Restyle(si.ID, line)
	if err != nil {
		t.Fatal(err)
	}
	if !gagged {
		t.Fatal("display effect (gag) should be recomputed on replay")
	}
	_ = out
	time.Sleep(200 * time.Millisecond)
	if got := string(srv.Received()); strings.Contains(got, "flee") || strings.Contains(got, "run") {
		t.Fatalf("replay performed side effects: %q", got)
	}
	// the same line live does send
	srv.SendLine("danger ahead")
	srv.ExpectLine("flee")
	srv.ExpectLine("run")
}

func TestInputCommands(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	_ = e.Input(si.ID, "/nosuchcmd")
	r.waitLine(t, si.ID, "no such command")
	_ = e.Input(si.ID, "/lua error('boom')")
	r.waitLine(t, si.ID, "boom")
	_ = e.Input(si.ID, "/echo -aBCred styled")
	r.waitLine(t, si.ID, "styled")
	_ = e.Input(si.ID, "/send hello world")
	srv.ExpectLine("hello world")
	_ = e.Input(si.ID, `/lua squat.alias{match="k *", fn=function(m) squat.send("kill "..m[1]) end}`)
	_ = e.Input(si.ID, "k orc")
	srv.ExpectLine("kill orc")
	if err := e.Input("nope", "x"); err != engine.ErrNoSession {
		t.Fatalf("want ErrNoSession, got %v", err)
	}
}

func TestSendToEndedSession(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	srv.CloseConn()
	r.waitState(t, e, si.ID, proto.StateEnded)
	_ = e.Input(si.ID, "look")
	r.waitLine(t, si.ID, "not connected")
}

func TestEndedReasons(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	srv.SendLine("goodbye cruel world")
	r.waitLine(t, si.ID, "goodbye")
	srv.CloseConn()
	got := r.waitState(t, e, si.ID, proto.StateEnded)
	if got.EndReason != proto.EndRemoteClosed || got.LastText != "goodbye cruel world" || got.EndedAt == 0 {
		t.Fatalf("bad end info %+v", got.SessionInfo)
	}
	time.Sleep(100 * time.Millisecond)
	if cur, _ := e.Session(si.ID); cur.State != proto.StateEnded {
		t.Fatal("ended sessions must not reconnect")
	}

	// connect failure
	addr := fakemush.ClosedAddr(t)
	host, port := splitAddr(t, addr)
	si2, err := e.Dial(engine.World{Name: "dead", Host: host, Port: port}, "")
	if err != nil {
		t.Fatal(err)
	}
	if g := r.waitState(t, e, si2.ID, proto.StateEnded); g.EndReason != proto.EndConnectFailed {
		t.Fatalf("reason %q", g.EndReason)
	}

	// client quit
	srv2 := fakemush.New(t)
	si3 := dial(t, e, r, srv2, nil, "")
	if err := e.Disconnect(si3.ID); err != nil {
		t.Fatal(err)
	}
	if g := r.waitState(t, e, si3.ID, proto.StateEnded); g.EndReason != proto.EndClientQuit {
		t.Fatalf("reason %q", g.EndReason)
	}

	// dismiss
	if err := e.Dismiss(si.ID); err != nil {
		t.Fatal(err)
	}
	r.until(t, "removed event", func() bool {
		for _, ev := range r.snapshot() {
			if ev.Kind == engine.EvSessionRemoved && ev.Session == si.ID {
				return true
			}
		}
		return false
	})
	if _, err := e.Session(si.ID); err != engine.ErrNoSession {
		t.Fatal("session should be gone")
	}
}

func TestUnreadAndAck(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	for i := 0; i < 5; i++ {
		srv.SendLine("line " + string(rune('a'+i)))
	}
	r.waitLine(t, si.ID, "line e")
	cur, _ := e.Session(si.ID)
	if cur.Unread != 5 {
		t.Fatalf("unread=%d", cur.Unread)
	}
	ls := r.lines(si.ID)
	var third uint64
	for _, l := range ls {
		if l.Plain() == "line c" {
			third = l.Seq
		}
	}
	_ = e.Ack(si.ID, third)
	if cur, _ = e.Session(si.ID); cur.Unread != 2 {
		t.Fatalf("after ack unread=%d", cur.Unread)
	}
	// background session counts too
	srv2 := fakemush.New(t)
	s2 := dial(t, e, r, srv2, nil, "")
	if e.Foreground() != si.ID {
		t.Fatal("first session should stay foreground")
	}
	srv2.SendLine("bg activity")
	r.waitLine(t, s2.ID, "bg activity")
	if cur, _ = e.Session(s2.ID); cur.Unread != 1 {
		t.Fatalf("background unread=%d", cur.Unread)
	}
	if err := e.SetForeground(s2.ID); err != nil || e.Foreground() != s2.ID {
		t.Fatal("foreground switch failed")
	}
}

func TestScrollbackPagingAndSearch(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	for i := 1; i <= 60; i++ {
		srv.SendLine("row " + itoa(i))
	}
	r.waitLine(t, si.ID, "row 60")
	page1, err := e.Scrollback(si.ID, 0, 25)
	if err != nil || len(page1) != 25 || page1[24].Plain() != "row 60" {
		t.Fatalf("page1: %v len=%d", err, len(page1))
	}
	page2, _ := e.Scrollback(si.ID, page1[0].Seq, 25)
	if len(page2) != 25 || page2[24].Seq != page1[0].Seq-1 || page2[24].Plain() != "row 35" {
		t.Fatalf("page2 bad: %d last=%q", len(page2), page2[len(page2)-1].Plain())
	}
	rest, _ := e.Scrollback(si.ID, page2[0].Seq, 100)
	if rest[len(rest)-1].Plain() != "row 10" {
		t.Fatalf("rest last=%q len=%d", rest[len(rest)-1].Plain(), len(rest))
	}
	hits, err := e.Search(si.ID, "row 4", engine.SearchOptions{})
	if err != nil || len(hits) != 11 { // 4, 40..49
		t.Fatalf("hits=%d err=%v", len(hits), err)
	}
	hits, _ = e.Search(si.ID, `^row 5\d$`, engine.SearchOptions{Regex: true, CaseSensitive: true})
	if len(hits) != 10 {
		t.Fatalf("regex hits=%d", len(hits))
	}
}

func TestRunawayWhileAborts(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t, func(o *engine.Options) { o.TF.MaxTime = 300 * time.Millisecond })
	si := dial(t, e, r, srv, nil, "")
	done := make(chan struct{})
	go func() { _ = e.Input(si.ID, "/while (1) /test 1 %; /done"); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runaway /while froze the engine")
	}
	_ = e.Input(si.ID, "after")
	srv.ExpectLine("after")
	r.waitLine(t, si.ID, "budget")
}

func TestColoursAndUTF8ByteExact(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	srv.SendString("\x1b[31mred\x1b[0m \x1b[38;5;196mc196\x1b[0m \x1b[48;5;232;38;5;231mgrey\x1b[0m \x1b[38;2;1;2;3mtc\x1b[0m h\xc3\xa9llo \xe2\x9c\x93 \xf0\x9f\x98\x80\r\n")
	l := r.waitLine(t, si.ID, "llo")
	got, _ := json.Marshal(l.Spans)
	want := `[{"s":"red","fg":1},{"s":" "},{"s":"c196","fg":196},{"s":" "},{"s":"grey","fg":231,"bg":232},{"s":" "},{"s":"tc","fg":"#010203"},{"s":" héllo ✓ 😀"}]`
	if string(got) != want {
		t.Fatalf("spans differ\n got %s\nwant %s", got, want)
	}
	// and the stored copy is identical
	ls, _ := e.Scrollback(si.ID, 0, 10)
	again, _ := json.Marshal(ls[len(ls)-1].Spans)
	if string(again) != want {
		t.Fatalf("scrollback differs: %s", again)
	}
	// blank lines keep "spans":[]
	srv.SendLine("")
	r.until(t, "blank", func() bool {
		for _, l := range r.lines(si.ID) {
			if len(l.Spans) == 0 {
				b, _ := json.Marshal(l)
				return strings.Contains(string(b), `"spans":[]`)
			}
		}
		return false
	})
}

func TestGMCPEvent(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	srv.Will(fakemush.OptGMCP)
	srv.WaitNeg(fakemush.DO, fakemush.OptGMCP)
	_ = e.Input(si.ID, `/lua squat.on("gmcp", function(g) squat.echo("gmcp:"..g.package) end)`)
	srv.GMCP("Char.Vitals", `{"hp":5}`)
	r.until(t, "gmcp event", func() bool {
		for _, ev := range r.snapshot() {
			if ev.Kind == engine.EvGmcp && ev.Gmcp.Package == "Char.Vitals" && string(ev.Gmcp.Data) == `{"hp":5}` {
				return true
			}
		}
		return false
	})
	r.waitLine(t, si.ID, "gmcp:Char.Vitals")
}

func TestResizeSendsNAWS(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	srv.Do(fakemush.OptNAWS)
	srv.WaitSub(fakemush.OptNAWS, 1)
	if err := e.Resize(si.ID, 120, 40); err != nil {
		t.Fatal(err)
	}
	srv.WaitSub(fakemush.OptNAWS, 2)
}

func TestScriptsLoadedFromConfigDir(t *testing.T) {
	srv := fakemush.New(t)
	dir := t.TempDir()
	mustWrite(t, dir+"/startup.tf", "/def -t'hello*' greet = /echo startup-ran\n")
	mustWrite(t, dir+"/plugins/p.lua", `squat.trigger{match="ping", fn=function() squat.send("pong-lua") end}`)
	e, r := newEngine(t, func(o *engine.Options) { o.ConfigDir = dir })
	if err := e.LoadScripts(); err != nil {
		t.Fatal(err)
	}
	si := dial(t, e, r, srv, nil, "")
	srv.SendLine("hello world")
	r.waitLine(t, si.ID, "startup-ran")
	srv.SendLine("ping")
	srv.ExpectLine("pong-lua")
	// /reload and /load of a lua file
	mustWrite(t, dir+"/plugins/q.lua", `squat.trigger{match="zap", fn=function() squat.send("zapped") end}`)
	_ = e.Input(si.ID, "/reload")
	time.Sleep(50 * time.Millisecond)
	srv.SendLine("zap")
	srv.ExpectLine("zapped")
}

func TestWorldCommands(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	var pwWorld string
	e2, _ := newEngine(t, func(o *engine.Options) {
		o.PasswordFor = func(id string) string { pwWorld = id; return "pw" }
	})
	_ = e
	_ = r
	e2.SetWorlds([]engine.World{{ID: "wid", Name: "game", Host: srv.Host(), Port: srv.Port(),
		Login: &engine.Login{Template: "connect {name} {password}", CharName: "zed"}}})
	r2 := collect(t, e2)
	first := dial(t, e2, r2, fakemush.New(t), nil, "")
	_ = e2.Input(first.ID, "/connect game")
	srv.ExpectLine("connect zed pw")
	if pwWorld != "wid" {
		t.Fatalf("PasswordFor world %q", pwWorld)
	}
	_ = e2.Input(first.ID, "/addworld other 127.0.0.1 4000")
	found := false
	for _, w := range e2.Worlds() {
		found = found || w.Name == "other"
	}
	if !found {
		t.Fatal("/addworld did not register")
	}
	_ = e2.Input(first.ID, "/fg game")
	r2.until(t, "fg event", func() bool {
		for _, ev := range r2.snapshot() {
			if ev.Kind == engine.EvForeground && ev.Session != first.ID {
				return true
			}
		}
		return false
	})
	_ = e2.Input(first.ID, "/dc game")
	r2.until(t, "dc", func() bool {
		for _, s := range e2.Sessions() {
			if s.Name == "game" && s.State == proto.StateEnded {
				return true
			}
		}
		return false
	})
}

func TestNoLeaksAfterClose(t *testing.T) {
	srv := fakemush.New(t)
	e := engine.New(engine.Options{ConfigDir: t.TempDir()})
	ch, cancel := e.Subscribe()
	go func() {
		for range ch {
		}
	}()
	si, err := e.Dial(engine.World{Name: "x", Host: srv.Host(), Port: srv.Port()}, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		srv.SendLine("hi")
	}
	_ = e.Input(si.ID, "/lua squat.timer{every=0.05, fn=function() end}")
	time.Sleep(150 * time.Millisecond)
	e.Close()
	cancel()
	e.Close() // idempotent
	leakCheck(t)
}
