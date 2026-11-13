package daemon_test

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

var defCaps = proto.DefaultCaps()

func connected(s proto.SessionInfo) bool { return s.State == proto.StateConnected }

// waitLast polls session.list until the session's lastSeq >= n.
func waitLast(c *tc, id string, n uint64) proto.SessionInfo {
	c.t.Helper()
	for i := 0; i < 800; i++ {
		l, err := c.c.List(ctx())
		if err != nil {
			c.t.Fatal(err)
		}
		for _, s := range l {
			if s.ID == id && s.LastSeq >= n {
				return s
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.t.Fatalf("session %s never reached seq %d", id, n)
	return proto.SessionInfo{}
}

func info(c *tc, id string) proto.SessionInfo {
	c.t.Helper()
	l, err := c.c.List(ctx())
	if err != nil {
		c.t.Fatal(err)
	}
	for _, s := range l {
		if s.ID == id {
			return s
		}
	}
	c.t.Fatalf("session %s not listed", id)
	return proto.SessionInfo{}
}

func TestConnectLinesAndAttachReplay(t *testing.T) {
	noLeaks(t)
	e := newEnv(t)
	m := fakemush.New(t, fakemush.WithGreeting("Welcome\r\nline2\r\n"))
	a := e.client("a", defCaps)
	s := a.connect(m.Host(), m.Port(), nil)
	if s.State != proto.StateConnecting && s.State != proto.StateConnected {
		t.Fatalf("created state %q", s.State)
	}
	if !strings.HasPrefix(s.ID, "s-") || len(s.ID) != 8 || s.Host != m.Host() || s.Port != m.Port() {
		t.Fatalf("bad info %+v", s)
	}
	a.state(s.ID, connected)
	waitLast(a, s.ID, 3)

	ents, att := a.attach(s.ID, nil, 0)
	want := []string{fmt.Sprintf("system:*** Connected to %s:%d", m.Host(), m.Port()), "game:Welcome", "game:line2"}
	if got := plains(ents); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("replay %q want %q", got, want)
	}
	if att.FirstSeq != 1 || att.LastSeq != 3 || att.ReplayFrom != 1 || att.Gap != nil {
		t.Fatalf("attached %+v", att)
	}
	m.SendLine("live1")
	if l := a.line("live1"); l.Seq != 4 || l.Kind != proto.KindGame {
		t.Fatalf("live %+v", l)
	}
	if err := a.c.Detach(ctx(), s.ID); err != nil {
		t.Fatal(err)
	}
	a.state(s.ID, func(i proto.SessionInfo) bool { return i.Attached == 0 })
	m.SendLine("l2")
	m.SendLine("l3")
	waitLast(a, s.ID, 6)

	// since inside retention: only the new entries, no gap
	ents, att = a.attach(s.ID, u64(4), 0)
	if got := plains(ents); strings.Join(got, "|") != "game:l2|game:l3" || att.Gap != nil || att.ReplayFrom != 5 {
		t.Fatalf("since=4: %v %+v", got, att)
	}
	// since == lastSeq: empty replay with done
	ents, att = a.attach(s.ID, u64(6), 0)
	if len(ents) != 0 || att.ReplayFrom != 0 || att.Gap != nil {
		t.Fatalf("since=last: %v %+v", ents, att)
	}
	// absent since + maxBacklog: the last N, no gap (client has nothing)
	ents, att = a.attach(s.ID, nil, 2)
	if got := plains(ents); strings.Join(got, "|") != "game:l2|game:l3" || att.Gap != nil {
		t.Fatalf("max=2: %v %+v", got, att)
	}
	// since given but more than maxBacklog behind: gap reports what was skipped
	ents, att = a.attach(s.ID, u64(0), 2)
	if att.Gap == nil || att.Gap.Missed != 4 || len(ents) != 2 || att.ReplayFrom != 5 {
		t.Fatalf("gap: %v %+v", plains(ents), att)
	}
}

func TestLiveDuringReplaySeam(t *testing.T) {
	noLeaks(t)
	e := newEnv(t)
	m := fakemush.New(t)
	a := e.client("a", defCaps)
	s := a.connect(m.Host(), m.Port(), nil)
	a.state(s.ID, connected)

	const N = 4000
	var sent atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < N; i++ {
			m.SendLine(fmt.Sprintf("spam %d", i))
			sent.Add(1)
		}
	}()
	// attach in the middle of the stream, several times
	r := e.rawClient()
	for sent.Load() < 300 {
		time.Sleep(time.Millisecond)
	}
	var seqs []uint64
	r.send(map[string]any{"v": 1, "t": "attach", "session": s.ID, "maxBacklog": 20000})
	var lastText string
	for lastText != fmt.Sprintf("spam %d", N-1) {
		f, err := r.recv(wait)
		if err != nil {
			t.Fatalf("recv: %v (got %d entries)", err, len(seqs))
		}
		switch str(f, "t") {
		case "backlog":
			var es []map[string]json.RawMessage
			_ = json.Unmarshal(f["entries"], &es)
			for _, en := range es {
				seqs = append(seqs, num(en, "seq"))
			}
		case "line":
			seqs = append(seqs, num(f, "seq"))
			var sp []proto.Span
			_ = json.Unmarshal(f["spans"], &sp)
			if len(sp) > 0 {
				lastText = sp[0].S
			}
		}
	}
	<-done
	if len(seqs) < N {
		t.Fatalf("only %d entries", len(seqs))
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Fatalf("seam broken at index %d: %d then %d", i, seqs[i-1], seqs[i])
		}
	}
	if seqs[0] != 1 {
		t.Fatalf("first seq %d", seqs[0])
	}
}

func TestEchoBroadcastUnreadAck(t *testing.T) {
	noLeaks(t)
	e := newEnv(t)
	m := fakemush.New(t)
	a := e.client("a", defCaps)
	b := e.client("b", defCaps)
	s := a.connect(m.Host(), m.Port(), nil)
	a.state(s.ID, connected)
	b.state(s.ID, connected)
	a.attach(s.ID, nil, 0)
	b.attach(s.ID, nil, 0)
	a.state(s.ID, func(i proto.SessionInfo) bool { return i.Attached == 2 })

	if err := a.c.Send(ctx(), s.ID, "say hello"); err != nil {
		t.Fatal(err)
	}
	m.ExpectLine("say hello")
	for _, c := range []*tc{a, b} {
		l := c.line("say hello")
		if l.Kind != proto.KindEcho || len(l.Spans) != 1 {
			t.Fatalf("%s echo %+v", c.name, l)
		}
	}
	// embedded newline: two sends, two echoes
	if err := b.c.Send(ctx(), s.ID, "a\nb"); err != nil {
		t.Fatal(err)
	}
	m.ExpectLine("a")
	m.ExpectLine("b")
	a.line("a")
	a.line("b")
	m.SendLine("reply")
	a.line("reply")

	waitLast(a, s.ID, 5) // 1 system, 2 echo, 3 echo a, 4 echo b, 5 reply
	ia := info(a, s.ID)
	if ia.Unread != ia.LastSeq || ia.Unread < 5 {
		t.Fatalf("unread before ack: %+v", ia)
	}
	if err := a.c.Ack(ctx(), s.ID, ia.LastSeq); err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		if info(a, s.ID).Unread == 0 {
			break
		}
		if i > 300 {
			t.Fatal("ack not applied")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ib := info(b, s.ID); ib.Unread != ib.LastSeq {
		t.Fatalf("b's unread must be independent: %+v", ib)
	}
	m.SendLine("one more")
	a.line("one more")
	if got := info(a, s.ID).Unread; got != 1 {
		t.Fatalf("unread after new line: %d", got)
	}
	// ack beyond lastSeq is clamped
	_ = a.c.Ack(ctx(), s.ID, 99999)
	time.Sleep(50 * time.Millisecond)
	if got := info(a, s.ID).Unread; got != 0 {
		t.Fatalf("clamped ack: unread %d", got)
	}
}

func hostPort(t *testing.T, addr string) (string, int) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(p)
	return h, n
}

func TestEndedReasons(t *testing.T) {
	noLeaks(t)
	e := newEnv(t)
	a := e.client("a", defCaps)

	// remote_closed + lastText
	m := fakemush.New(t)
	s := a.connect(m.Host(), m.Port(), nil)
	a.state(s.ID, connected)
	m.SendLine("\x1b[31mgoodbye\x1b[0m")
	m.SendString("   \r\n")
	waitLast(a, s.ID, 3)
	m.CloseConn()
	end := a.state(s.ID, func(i proto.SessionInfo) bool { return i.State == proto.StateEnded })
	if end.EndReason != proto.EndRemoteClosed || end.EndedAt == 0 || end.LastText != "goodbye" {
		t.Fatalf("remote_closed: %+v", end)
	}
	ents, _ := a.attach(s.ID, nil, 0) // ended sessions keep their history
	if got := plains(ents); got[len(got)-1] != "system:*** Connection closed: closed by remote host" {
		t.Fatalf("history %q", got)
	}
	if err := a.c.Send(ctx(), s.ID, "x"); err != nil {
		t.Fatal(err)
	}
	ev := a.expect("session_ended", func(ev wire.Event) bool { er, ok := ev.(wire.Error); return ok && er.Code == proto.ErrSessionEnded })
	_ = ev
	if err := a.c.Disconnect(ctx(), s.ID); err != nil {
		t.Fatal(err)
	}
	a.expect("session_ended on disconnect", func(ev wire.Event) bool { er, ok := ev.(wire.Error); return ok && er.Code == proto.ErrSessionEnded })

	// connect_failed
	h, p := hostPort(t, fakemush.ClosedAddr(t))
	s2 := a.connect(h, p, nil)
	end = a.state(s2.ID, func(i proto.SessionInfo) bool { return i.State == proto.StateEnded })
	if end.EndReason != proto.EndConnectFailed || end.EndDetail == "" {
		t.Fatalf("connect_failed: %+v", end)
	}

	// client_quit
	m3 := fakemush.New(t)
	s3 := a.connect(m3.Host(), m3.Port(), nil)
	a.state(s3.ID, connected)
	if err := a.c.Disconnect(ctx(), s3.ID); err != nil {
		t.Fatal(err)
	}
	end = a.state(s3.ID, func(i proto.SessionInfo) bool { return i.State == proto.StateEnded })
	if end.EndReason != proto.EndClientQuit {
		t.Fatalf("client_quit: %+v", end)
	}

	// tls_error: TLS to a plain-text server
	m4 := fakemush.New(t, fakemush.WithGreeting("hello, I am not TLS\r\n"))
	s4 := a.connect(m4.Host(), m4.Port(), func(w *proto.World) { w.TLS = true; w.TLSVerify = "insecure" })
	end = a.state(s4.ID, func(i proto.SessionInfo) bool { return i.State == proto.StateEnded })
	if end.EndReason != proto.EndTLSError {
		t.Fatalf("tls_error: %+v", end)
	}
}

func TestTLSWorldAndLatin1(t *testing.T) {
	noLeaks(t)
	e := newEnv(t)
	a := e.client("a", defCaps)
	m := fakemush.New(t, fakemush.WithTLS())
	s := a.connect(m.Host(), m.Port(), func(w *proto.World) { w.TLS = true; w.TLSVerify = "insecure"; w.Encoding = "latin1" })
	a.state(s.ID, connected)
	a.attach(s.ID, nil, 0)
	m.SendString("caf\xe9 \xfc\r\n")
	if l := a.line("café ü"); l.Kind != proto.KindGame {
		t.Fatal("latin1 decode")
	}
	if err := a.c.Send(ctx(), s.ID, "café"); err != nil {
		t.Fatal(err)
	}
	if got := m.ReadLine(); got != "caf\xe9" {
		t.Fatalf("latin1 encode: %q", got)
	}
	a.line("café")
}

func TestGMCPAndPrompts(t *testing.T) {
	noLeaks(t)
	e := newEnv(t)
	a := e.client("a", defCaps)
	m := fakemush.New(t)
	s := a.connect(m.Host(), m.Port(), nil)
	a.state(s.ID, connected)
	a.attach(s.ID, nil, 0)

	m.Will(fakemush.OptGMCP)
	m.WaitNeg(fakemush.DO, fakemush.OptGMCP)
	m.GMCP("Room.Info", `{"name":"Bridge","exits":["n","s"]}`)
	ev := a.expect("gmcp", func(ev wire.Event) bool { _, ok := ev.(wire.Gmcp); return ok }).(wire.Gmcp)
	if ev.Package != "Room.Info" || string(ev.Data) != `{"name":"Bridge","exits":["n","s"]}` || ev.Seq < 2 {
		t.Fatalf("gmcp %+v", ev)
	}
	// prompt flushed by idle timer
	m.SendString("Password: ")
	if l := a.expect("idle prompt", func(ev wire.Event) bool { l, ok := ev.(wire.Line); return ok && l.Prompt }).(wire.Line); l.Plain() != "Password: " {
		t.Fatalf("prompt %q", l.Plain())
	}
	// prompt flushed by EOR
	m.Will(fakemush.OptEOR)
	m.WaitNeg(fakemush.DO, fakemush.OptEOR)
	m.SendString("HP 10> ")
	m.EOR()
	a.expect("eor prompt", func(ev wire.Event) bool { l, ok := ev.(wire.Line); return ok && l.Prompt && l.Plain() == "HP 10> " })
	// replay includes gmcp entries
	ents, _ := a.attach(s.ID, nil, 0)
	var sawG bool
	for _, en := range ents {
		if en.Gmcp != nil && en.Gmcp.Package == "Room.Info" {
			sawG = true
		}
	}
	if !sawG {
		t.Fatalf("gmcp missing from replay: %v", plains(ents))
	}
}

func TestResizeAndCapsUnion(t *testing.T) {
	noLeaks(t)
	e := newEnv(t)
	m := fakemush.New(t)
	a := e.client("a", proto.Caps{UTF8: true})                     // no truecolor
	b := e.client("b", proto.Caps{Truecolor: true, UTF8: true})    // truecolor
	c := e.client("c", proto.Caps{UTF8: true, ScreenReader: true}) // screen reader
	s := a.connect(m.Host(), m.Port(), nil)
	a.state(s.ID, connected)
	m.Do(fakemush.OptTType)
	m.Do(fakemush.OptNAWS)
	m.WaitNeg(fakemush.WILL, fakemush.OptNAWS)
	if got := m.WaitSub(fakemush.OptNAWS, 1); string(got) != "\x00\x50\x00\x18" {
		t.Fatalf("initial naws %v", got)
	}
	a.attach(s.ID, nil, 0)
	if err := a.c.Resize(ctx(), s.ID, 100, 40); err != nil {
		t.Fatal(err)
	}
	if got := m.WaitSub(fakemush.OptNAWS, 2); string(got) != "\x00\x64\x00\x28" {
		t.Fatalf("naws after a: %v", got)
	}
	b.attach(s.ID, nil, 0)
	if err := b.c.Resize(ctx(), s.ID, 132, 50); err != nil { // most recent resize wins
		t.Fatal(err)
	}
	if got := m.WaitSub(fakemush.OptNAWS, 3); string(got) != "\x00\x84\x00\x32" {
		t.Fatalf("naws after b: %v", got)
	}
	_ = c

	// TTYPE cycle for the union of attached caps. Only a is attached... no: a and b are.
	a.state(s.ID, func(i proto.SessionInfo) bool { return i.Attached == 2 })
	ask := []byte{1}
	var names []string
	for i := 1; i <= 3; i++ {
		m.Sub(fakemush.OptTType, ask)
		names = append(names, string(m.WaitSub(fakemush.OptTType, i)[1:]))
	}
	if names[0] != "SquatClient" || names[1] != "XTERM-TRUECOLOR" {
		t.Fatalf("ttype %v", names)
	}
	// a(utf8)+b(truecolor): ANSI1 + UTF8 4 + 256col 8 + PROXY 128 + TRUECOLOR 256
	if names[2] != "MTTS 397" {
		t.Fatalf("union MTTS %q", names[2])
	}
	// c (screen reader) attaches: +64
	c.attach(s.ID, nil, 0)
	a.state(s.ID, func(i proto.SessionInfo) bool { return i.Attached == 3 })
	m.Sub(fakemush.OptTType, ask)
	if got := string(m.WaitSub(fakemush.OptTType, 4)[1:]); got != "MTTS 461" {
		t.Fatalf("after screenreader client: %q", got)
	}
	// b leaves and c changes caps via the caps message: union shrinks accordingly
	if err := b.c.Detach(ctx(), s.ID); err != nil {
		t.Fatal(err)
	}
	a.state(s.ID, func(i proto.SessionInfo) bool { return i.Attached == 2 })
	m.Sub(fakemush.OptTType, ask)
	if got := string(m.WaitSub(fakemush.OptTType, 5)[1:]); got != "MTTS 205" { // 1+4+8+128+64
		t.Fatalf("after b detached: %q", got)
	}
	if err := a.c.SetCaps(ctx(), proto.Caps{UTF8: true, Truecolor: true}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	m.Sub(fakemush.OptTType, ask)
	if got := string(m.WaitSub(fakemush.OptTType, 6)[1:]); got != "MTTS 461" {
		t.Fatalf("after caps msg: %q", got)
	}
}

func TestDismiss(t *testing.T) {
	noLeaks(t)
	e := newEnv(t)
	a := e.client("a", defCaps)
	b := e.client("b", defCaps)
	m := fakemush.New(t)
	s := a.connect(m.Host(), m.Port(), nil)
	a.state(s.ID, connected)
	// live sessions cannot be dismissed
	_ = a.c.Dismiss(ctx(), s.ID)
	a.expect("bad_request", func(ev wire.Event) bool { er, ok := ev.(wire.Error); return ok && er.Code == proto.ErrBadRequest })
	_ = a.c.Dismiss(ctx(), "s-nope00")
	a.expect("no_such_session", func(ev wire.Event) bool { er, ok := ev.(wire.Error); return ok && er.Code == proto.ErrNoSuchSession })
	m.CloseConn()
	a.state(s.ID, func(i proto.SessionInfo) bool { return i.State == proto.StateEnded })
	dir := filepath.Join(e.dataDir, "sessions", s.ID)
	if _, err := os.Stat(filepath.Join(dir, "meta.json")); err != nil {
		t.Fatal(err)
	}
	if err := a.c.Dismiss(ctx(), s.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*tc{a, b} {
		c.expect("removed", func(ev wire.Event) bool { r, ok := ev.(wire.Removed); return ok && r.Session == s.ID })
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("session dir still there: %v", err)
	}
	if l, _ := a.c.List(ctx()); len(l) != 0 {
		t.Fatalf("list %v", l)
	}
}

func TestSessionLimit(t *testing.T) {
	noLeaks(t)
	e := newEnv(t, envOpts{maxSess: 1})
	a := e.client("a", defCaps)
	m := fakemush.New(t)
	a.connect(m.Host(), m.Port(), nil)
	if _, err := a.c.Connect(ctx(), proto.World{Host: m.Host(), Port: m.Port()}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("limit not enforced: %v", err)
	}
}

func TestRestartPersistence(t *testing.T) {
	noLeaks(t)
	dir := t.TempDir()
	e := startEnv(t, dir, envOpts{})
	key, _ := auth.GenerateKey()
	a := e.clientWithKey("a", defCaps, key, true)
	m := fakemush.New(t)
	s := a.connect(m.Host(), m.Port(), func(w *proto.World) { w.Name = "persisted" })
	a.state(s.ID, connected)
	m.SendLine("before restart 1")
	m.SendLine("before restart 2")
	waitLast(a, s.ID, 3)
	if err := a.c.Ack(ctx(), s.ID, 2); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	// a second, already-ended session
	m2 := fakemush.New(t)
	s2 := a.connect(m2.Host(), m2.Port(), nil)
	a.state(s2.ID, connected)
	m2.SendLine("bye")
	waitLast(a, s2.ID, 2)
	m2.CloseConn()
	a.state(s2.ID, func(i proto.SessionInfo) bool { return i.State == proto.StateEnded })

	// Simulate a crash: snapshot the data dir while the daemon is still live.
	time.Sleep(1300 * time.Millisecond) // scrollback fsync batching + meta flush
	crash := t.TempDir()
	copyDir(t, dir, crash)

	// Graceful shutdown: live sessions end as squatter_stopped, flushed.
	a.c.Close()
	e.stop()
	for name, d := range map[string]string{"graceful": dir, "crash": crash} {
		e2 := startEnv(t, d, envOpts{})
		a2 := e2.clientWithKey("a", defCaps, key, false) // device survived the restart
		var got, got2 proto.SessionInfo
		for _, i := range a2.c.Sessions() {
			switch i.ID {
			case s.ID:
				got = i
			case s2.ID:
				got2 = i
			}
		}
		if got.State != proto.StateEnded || got.EndReason != proto.EndSquatterStopped || got.Name != "persisted" ||
			got.LastText != "before restart 2" || got.Unread != got.LastSeq-2 || got.LastSeq < 4 {
			t.Fatalf("%s: restarted session %+v", name, got)
		}
		if got2.State != proto.StateEnded || got2.EndReason != proto.EndRemoteClosed || got2.LastText != "bye" {
			t.Fatalf("%s: ended-before-restart session %+v", name, got2)
		}
		ents, _ := a2.attach(s.ID, nil, 0)
		pl := plains(ents)
		if len(pl) < 4 || pl[1] != "game:before restart 1" || pl[len(pl)-1] != "system:*** Connection closed: squatterd stopped" {
			t.Fatalf("%s: history %q", name, pl)
		}
		for i, en := range ents { // seq continuity survives
			if en.Seq() != uint64(i+1) {
				t.Fatalf("%s: seq %d at %d", name, en.Seq(), i)
			}
		}
		e2.stop()
	}
}

func copyDir(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.Walk(from, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if fi.IsDir() {
			return os.MkdirAll(dst, 0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}
