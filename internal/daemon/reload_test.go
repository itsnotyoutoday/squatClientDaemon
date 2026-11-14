package daemon_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/daemon"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/telnet"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// lastSeqOf reads a session's lastSeq without any client.
func lastSeqOf(d *daemon.Daemon, id string) uint64 {
	for _, s := range d.Sessions("probe") {
		if s.ID == id {
			return s.LastSeq
		}
	}
	return 0
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if f() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// A state the new process cannot restore is refused BEFORE any connection is touched, and the very
// same handoff can then be used again (the old process taking everything back): no connection is lost
// and no byte that arrived meanwhile is lost either.
func TestRefusedHandoffLosesNothing(t *testing.T) {
	noLeaks(t)
	dir := t.TempDir()
	e1 := startEnv(t, dir, envOpts{})
	key, _ := auth.GenerateKey()
	a := e1.clientWithKey("a", defCaps, key, true)
	g1, g2 := fakemush.New(t), fakemush.New(t)
	s1 := a.connect(g1.Host(), g1.Port(), func(w *proto.World) { w.Name = "one" })
	s2 := a.connect(g2.Host(), g2.Port(), func(w *proto.World) { w.Name = "two" })
	a.state(s1.ID, connected)
	a.state(s2.ID, connected)
	p1, p2 := g1.Peer(), g2.Peer()
	h, err := e1.d.PrepareHandoff(1)
	if err != nil {
		t.Fatal(err)
	}
	e1.srv.Close()
	g1.SendLine("while nobody listens 1")
	g2.SendLine("while nobody listens 2")

	// break the state of whichever session sorts second: the first one is adopted before the failure
	bad := &h.Bundle.Sessions[1]
	saved := bad.Conn.Telnet.State
	bad.Conn.Telnet.State = 99
	if _, err := daemon.New(daemon.Config{DataDir: dir, Handoff: h}); err == nil {
		t.Fatal("a state that cannot be restored was accepted")
	}
	bad.Conn.Telnet.State = saved
	// a state from the future in a connection is refused the same way
	bad.Conn.V = 9
	if _, err := daemon.New(daemon.Config{DataDir: dir, Handoff: h}); err == nil {
		t.Fatal("a connection state from the future was accepted")
	}
	bad.Conn.V = 0
	// a meta.json that vanished
	meta := filepath.Join(dir, "sessions", bad.ID, "meta.json")
	hold, _ := os.ReadFile(meta)
	_ = os.Remove(meta)
	if _, err := daemon.New(daemon.Config{DataDir: dir, Handoff: h}); err == nil {
		t.Fatal("a carried session without metadata was accepted")
	}
	_ = os.WriteFile(meta, hold, 0o600)

	if p1.Gone() || p2.Gone() {
		t.Fatal("a refused handoff closed a game connection")
	}
	// the retry (the rollback): everything is intact, including what arrived in between
	e2 := startEnvFrom(t, dir, daemon.Config{Handoff: h})
	h.Close()
	b := e2.clientWithKey("a", defCaps, key, false)
	for _, id := range []string{s1.ID, s2.ID} {
		ents, _ := b.attach(id, nil, 0)
		var got []string
		for _, p := range plains(ents) {
			if strings.HasPrefix(p, "game:") {
				got = append(got, p)
			}
		}
		if len(got) != 1 || !strings.HasPrefix(got[0], "game:while nobody listens") {
			t.Fatalf("%s: %q", id, got)
		}
	}
	if err := b.c.Send(ctx(), s1.ID, "alive"); err != nil {
		t.Fatal(err)
	}
	g1.ExpectLine("alive")
	if g1.Connections() != 1 || g2.Connections() != 1 {
		t.Fatalf("connections: %d %d", g1.Connections(), g2.Connections())
	}
}

// Adopted connections do not read, and nothing is written, until ResumeAdopted: that is what lets the
// old process take them back with no loss when the new one dies before it is ready.
func TestAdoptedConnectionsWaitForResume(t *testing.T) {
	noLeaks(t)
	dir := t.TempDir()
	e1 := startEnv(t, dir, envOpts{})
	key, _ := auth.GenerateKey()
	a := e1.clientWithKey("a", defCaps, key, true)
	g := fakemush.New(t)
	s := a.connect(g.Host(), g.Port(), nil)
	a.state(s.ID, connected)
	g.SendLine("first")
	before := waitLast(a, s.ID, 2).LastSeq
	h, err := e1.d.PrepareHandoff(1)
	if err != nil {
		t.Fatal(err)
	}
	e1.srv.Close()
	e2 := startEnvPaused(t, dir, daemon.Config{Handoff: h}, false)
	h.Close()
	g.SendLine("early")
	time.Sleep(300 * time.Millisecond)
	if n := lastSeqOf(e2.d, s.ID); n != before {
		t.Fatalf("a paused connection consumed or recorded something: %d -> %d", before, n)
	}
	e2.d.ResumeAdopted()
	eventually(t, "the early line after resume", func() bool { return lastSeqOf(e2.d, s.ID) >= before+2 })
	e2.d.ResumeAdopted() // idempotent
	b := e2.clientWithKey("a", defCaps, key, false)
	ents, _ := b.attach(s.ID, u64(before), 0)
	if got := strings.Join(plains(ents), "|"); !strings.Contains(got, "game:early") || strings.Count(got, "system:*** squatterd was restarted") != 1 {
		t.Fatalf("%q", got)
	}
}

// Echo/secret mode, the partial line, the window size and per-device read marks survive; the secret
// itself is never in the state file or anywhere on disk; the state file is private.
func TestHandoffCarriesEchoModePartialLineAndDeviceState(t *testing.T) {
	noLeaks(t)
	dir := t.TempDir()
	e1 := startEnv(t, dir, envOpts{})
	keyA, _ := auth.GenerateKey()
	keyB, _ := auth.GenerateKey()
	a := e1.clientWithKey("a", defCaps, keyA, true)
	_ = e1.clientWithKey("b", defCaps, keyB, true) // device b pairs and never reads
	g := fakemush.New(t)
	s := a.connect(g.Host(), g.Port(), nil)
	a.state(s.ID, connected)
	for i := 0; i < 3; i++ {
		g.SendLine("line")
	}
	last := waitLast(a, s.ID, 4).LastSeq
	if err := a.c.Ack(ctx(), s.ID, last); err != nil { // device a has read everything, b nothing
		t.Fatal(err)
	}
	if err := a.c.Resize(ctx(), s.ID, 132, 43); err != nil {
		t.Fatal(err)
	}
	g.Will(telnet.OptEcho) // the game asks us not to echo: a password prompt
	g.Send([]byte("Password: "))
	a.state(s.ID, func(i proto.SessionInfo) bool { return i.RemoteEcho })
	if err := a.c.SendSecret(ctx(), s.ID, "hunter2-the-secret"); err != nil {
		t.Fatal(err)
	}
	g.ExpectLine("hunter2-the-secret")
	time.Sleep(300 * time.Millisecond) // the prompt is shown (flushed) after its idle time
	g.Send([]byte("a half line"))      // partial and unterminated at the moment of the handoff
	time.Sleep(20 * time.Millisecond)
	code, _, err := e1.as.NewPairing() // a pairing code pending across the restart
	if err != nil {
		t.Fatal(err)
	}

	h, err := e1.d.PrepareHandoff(1)
	if err != nil {
		t.Fatal(err)
	}
	st := h.Bundle.Sessions[0]
	if !st.RemoteEcho || !st.Conn.Echo || st.Conn.Telnet.Cols != 132 || st.Conn.Telnet.Rows != 43 {
		t.Fatalf("state: %+v", st)
	}
	path := filepath.Join(dir, "handoff.json")
	if err := daemon.WriteBundle(path, h.Bundle); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode %v", fi.Mode())
	}
	if string(st.Conn.Liner.Buf) != "a half line" {
		t.Fatalf("the partial line is not in the state: %q", st.Conn.Liner.Buf)
	}
	back, err := daemon.ReadBundle(path)
	if err != nil {
		t.Fatal(err)
	}
	h.Bundle = back // through the file, as in the real thing
	e1.srv.Close()
	e2 := startEnvFrom(t, dir, daemon.Config{Handoff: h})
	h.Close()
	_ = os.Remove(path)

	// nothing secret anywhere on disk
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if data, _ := os.ReadFile(p); strings.Contains(string(data), "hunter2") {
				t.Errorf("the secret is on disk in %s", p)
			}
		}
		return nil
	})
	// the pairing code survived and works once
	pk := mustKey(t)
	pc, err := dialWith(e2, "paired-after", pk, code)
	if err != nil {
		t.Fatalf("pairing code lost in the restart: %v", err)
	}
	pc.Close()
	// per-device read marks
	a2 := e2.clientWithKey("a", defCaps, keyA, false)
	b2 := e2.clientWithKey("b", defCaps, keyB, false)
	unread := func(c *tc) uint64 {
		for _, i := range c.c.Sessions() {
			if i.ID == s.ID {
				return i.Unread
			}
		}
		return 999
	}
	if ua, ub := unread(a2), unread(b2); ua != 2 /* the prompt and the restart note */ || ub != last+2 {
		t.Fatalf("unread: a=%d b=%d (last %d)", ua, ub, last)
	}
	// still in no-echo mode: the next typed line is masked; the half line completes
	info := a2.c.Sessions()[0]
	if !info.RemoteEcho {
		t.Fatalf("no-echo mode lost: %+v", info)
	}
	if err := a2.c.Send(ctx(), s.ID, "typed while no-echo"); err != nil {
		t.Fatal(err)
	}
	g.ExpectLine("typed while no-echo")
	g.Send([]byte(" completed\r\n"))
	ents, _ := a2.attach(s.ID, nil, 0)
	var joined []string
	for _, e := range ents {
		if e.Line != nil {
			joined = append(joined, e.Line.Kind+":"+e.Line.Plain())
		}
	}
	all := strings.Join(joined, "|")
	if strings.Contains(all, "typed while no-echo") || strings.Contains(all, "hunter2") || !strings.Contains(all, "echo:********") {
		t.Fatalf("echo masking lost across the restart: %q", all)
	}
	eventually(t, "the completed half line", func() bool {
		ents, _ := a2.attach(s.ID, nil, 0)
		return strings.Contains(strings.Join(plains(ents), "|"), "game:a half line completed")
	})
}

func mustKey(t *testing.T) []byte {
	t.Helper()
	k, err := auth.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func dialWith(e *env, name string, key []byte, code string) (*wire.Client, error) {
	return wire.Dial(ctx(), e.url, wire.ClientOptions{Key: key, DeviceName: name, PinnedFingerprint: e.fp, PairCode: code})
}

// An MCCP2 compressed stream continues across the restart, with the window size and the telnet options
// negotiated before it intact.
func TestHandoffMidMCCPSession(t *testing.T) {
	noLeaks(t)
	dir := t.TempDir()
	e1 := startEnv(t, dir, envOpts{})
	key, _ := auth.GenerateKey()
	a := e1.clientWithKey("a", defCaps, key, true)
	g := fakemush.New(t)
	s := a.connect(g.Host(), g.Port(), nil)
	a.state(s.ID, connected)
	g.Will(telnet.OptMCCP2)
	g.StartMCCP2()
	g.SendLine("compressed one")
	waitLast(a, s.ID, 2)
	h, err := e1.d.PrepareHandoff(1)
	if err != nil {
		t.Fatal(err)
	}
	if h.Bundle.Sessions[0].Conn.Telnet.Z == nil {
		t.Fatal("the decompressor state is not carried")
	}
	e1.srv.Close()
	e2 := startEnvFrom(t, dir, daemon.Config{Handoff: h})
	h.Close()
	g.SendLine("compressed two")
	b := e2.clientWithKey("a", defCaps, key, false)
	eventually(t, "compressed two", func() bool {
		ents, _ := b.attach(s.ID, nil, 0)
		return strings.Contains(strings.Join(plains(ents), "|"), "game:compressed two")
	})
}

// What a reload would do, as the control socket and admin.reload report it.
func TestReloadPlanLists(t *testing.T) {
	noLeaks(t)
	e := newEnv(t)
	a := e.client("a", defCaps)
	plain := fakemush.New(t)
	secure := cbcServer(t)
	modern := fakemush.New(t, fakemush.WithTLS())
	sp := a.connect(plain.Host(), plain.Port(), func(w *proto.World) { w.Name = "plain" })
	ss := a.connect(secure.Host(), secure.Port(), func(w *proto.World) { w.Name = "secure"; w.TLS = true; w.TLSVerify = "insecure" })
	sm := a.connect(modern.Host(), modern.Port(), func(w *proto.World) { w.Name = "modern"; w.TLS = true; w.TLSVerify = "insecure" })
	a.state(sp.ID, connected)
	a.state(ss.ID, connected)
	a.state(sm.ID, connected)
	plan := e.d.ReloadPlan()
	// the TLS 1.3 (AEAD) session is carried like the plain one, only the CBC one would end
	if len(plan.Carry) != 2 || plan.Carry[0].ID != sp.ID || plan.Carry[1].ID != sm.ID || len(plan.End) != 1 || plan.End[0].ID != ss.ID || plan.End[0].Reason != "tls" {
		t.Fatalf("%+v", plan)
	}
	st := e.d.ReloadStatus()
	if st.Supported || st.Generation != 0 || st.Last != nil || st.Plan == nil || st.PID != os.Getpid() || st.StartedAt == 0 || st.ServiceSince == 0 {
		t.Fatalf("%+v", st)
	}
}

func roleClient(t *testing.T, e *env, name, role string) *wire.Client {
	t.Helper()
	code, _, err := e.as.NewPairingRole(time.Minute, role)
	if err != nil {
		t.Fatal(err)
	}
	c, err := dialWith(e, name, mustKey(t), code)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// admin.reload: admin role only, refuses when sessions would end unless forced, dry run, status for
// everyone, the welcome carries the status, and the hook runs after the reply.
func TestAdminReloadRequest(t *testing.T) {
	noLeaks(t)
	var calls atomic.Int32
	var forced atomic.Bool
	hook := make(chan struct{}, 4)
	e := startEnvFrom(t, t.TempDir(), daemon.Config{Reload: func(force bool) error {
		calls.Add(1)
		forced.Store(force)
		hook <- struct{}{}
		return nil
	}})
	admin := roleClient(t, e, "adm", "admin")
	owner := roleClient(t, e, "owner", "")
	viewer := roleClient(t, e, "view", "viewer")
	operator := roleClient(t, e, "op", "operator")

	if !admin.HasFeature(wire.FeatureReload) {
		t.Fatal("the reload feature is not advertised")
	}
	if ri := viewer.ReloadInfo(); ri == nil || !ri.Supported || ri.Generation != 0 || ri.PID == 0 || ri.Plan == nil {
		t.Fatalf("welcome.reload: %+v", ri)
	}
	// status: any role
	st, err := viewer.ReloadStatus(ctx())
	if err != nil || !st.Supported || st.UptimeMs < 0 {
		t.Fatalf("%v %+v", err, st)
	}
	// reload: admin only
	for _, c := range []*wire.Client{viewer, operator} {
		_, err := c.AdminReload(ctx(), false, false)
		var pe *wire.ProtoError
		if !errors.As(err, &pe) || pe.Code != proto.ErrForbidden {
			t.Fatalf("non-admin: %v", err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("a non-admin triggered a reload")
	}
	// a TLS session would end: refused without force, nothing started
	secure := cbcServer(t)
	sess, err := admin.Connect(ctx(), proto.World{Name: "secure", Host: secure.Host(), Port: secure.Port(), TLS: true, TLSVerify: "insecure"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "tls session connected", func() bool { return len(e.d.ReloadPlan().End) == 1 })
	_ = sess
	_, err = admin.AdminReload(ctx(), false, false)
	var pe *wire.ProtoError
	if !errors.As(err, &pe) || pe.Code != proto.ErrReloadNeedsForce || !strings.Contains(pe.Message, "secure") {
		t.Fatalf("%v", err)
	}
	plan, err := admin.AdminReload(ctx(), true, true) // dry run with force: the plan, nothing started
	if err != nil || len(plan.End) != 1 {
		t.Fatalf("%v %+v", err, plan)
	}
	time.Sleep(300 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("refusals and dry runs started a reload")
	}
	if _, err := owner.AdminReload(ctx(), true, false); err != nil { // an old full-trust device counts as admin
		t.Fatal(err)
	}
	select {
	case <-hook:
	case <-time.After(5 * time.Second):
		t.Fatal("the reload hook was not called")
	}
	if !forced.Load() || calls.Load() != 1 {
		t.Fatalf("force=%v calls=%d", forced.Load(), calls.Load())
	}
}

func TestAdminReloadUnsupported(t *testing.T) {
	noLeaks(t)
	e := newEnv(t) // no Reload hook
	admin := roleClient(t, e, "adm", "admin")
	_, err := admin.AdminReload(ctx(), false, false)
	var pe *wire.ProtoError
	if !errors.As(err, &pe) || pe.Code != proto.ErrReloadUnsupported {
		t.Fatalf("%v", err)
	}
	if ri := admin.ReloadInfo(); ri == nil || ri.Supported {
		t.Fatalf("%+v", ri)
	}
}

// The new process knows how many reloads happened, when the service began and how the last one went.
func TestReloadStatusIsCarried(t *testing.T) {
	noLeaks(t)
	dir := t.TempDir()
	e1 := startEnv(t, dir, envOpts{})
	g := fakemush.New(t)
	a := e1.client("a", defCaps)
	s := a.connect(g.Host(), g.Port(), nil)
	a.state(s.ID, connected)
	since := e1.d.ReloadStatus().ServiceSince
	h, err := e1.d.PrepareHandoff(4)
	if err != nil {
		t.Fatal(err)
	}
	e1.srv.Close()
	e2 := startEnvFrom(t, dir, daemon.Config{Handoff: h, Reload: func(bool) error { return nil }})
	h.Close()
	st := e2.d.ReloadStatus()
	if st.Generation != 4 || st.ServiceSince != since || st.Last == nil || !st.Last.Ok || st.Last.Carried != 1 || st.Last.Generation != 4 || st.Last.DurationMs <= 0 {
		t.Fatalf("%+v %+v", st, st.Last)
	}
	// a failed attempt that was rolled back is what "last" says afterwards
	e2.d.RecordReload(proto.ReloadResult{At: 1, Error: "the new process was not ready"}, 4)
	if st := e2.d.ReloadStatus(); st.Last.Ok || st.Last.Error == "" || st.Generation != 4 {
		t.Fatalf("%+v", st.Last)
	}
}

// A client in the middle of a resume (a big replay), more of them reading, and the game talking, while
// the daemon is handed over: no deadlock, no panic, no lost line, and a reconnecting client replays
// exactly what it is missing.
func TestHandoffWhileClientsResume(t *testing.T) {
	noLeaks(t)
	dir := t.TempDir()
	e1 := startEnv(t, dir, envOpts{})
	key, _ := auth.GenerateKey()
	a := e1.clientWithKey("a", defCaps, key, true)
	g := fakemush.New(t)
	s := a.connect(g.Host(), g.Port(), nil)
	a.state(s.ID, connected)
	for i := 0; i < 1000; i++ {
		g.SendLine("history line")
	}
	waitLast(a, s.ID, 1001)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		c := e1.client("r", defCaps)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := c.c.Attach(ctx(), s.ID, nil, 0); err != nil {
					return // the connection was dropped by the handoff
				}
			}
		}()
	}
	var talk sync.WaitGroup
	talk.Add(1)
	go func() {
		defer talk.Done()
		for i := 0; i < 200; i++ {
			g.SendLine("live")
			time.Sleep(time.Millisecond)
		}
	}()
	time.Sleep(50 * time.Millisecond)
	done := make(chan struct{})
	var h *daemon.Handoff
	var herr error
	go func() { h, herr = e1.d.PrepareHandoff(1); close(done) }()
	select {
	case <-done:
	case <-time.After(90 * time.Second): // generous: with -race on two cores four clients replaying in a loop starve it
		t.Fatal("PrepareHandoff deadlocked against resuming clients")
	}
	if herr != nil {
		t.Fatal(herr)
	}
	e1.srv.Close()
	close(stop)
	wg.Wait()
	talk.Wait()
	e2 := startEnvFrom(t, dir, daemon.Config{Handoff: h})
	h.Close()
	b := e2.clientWithKey("a", defCaps, key, false)
	eventually(t, "all 200 live lines", func() bool { return lastSeqOf(e2.d, s.ID) >= 1000+200 })
	ents, att := b.attach(s.ID, nil, 20000)
	seen := map[uint64]bool{}
	for _, e := range ents {
		if seen[e.Seq()] {
			t.Fatalf("duplicate seq %d in the replay", e.Seq())
		}
		seen[e.Seq()] = true
	}
	if att.Gap != nil || len(seen) < 1200 {
		t.Fatalf("gap %+v, %d entries", att.Gap, len(seen))
	}
}

// Shutdown races: asking for a handoff twice, or while shutting down, fails cleanly the second time.
func TestHandoffTwiceFails(t *testing.T) {
	noLeaks(t)
	e := newEnv(t)
	h, err := e.d.PrepareHandoff(1)
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
	if h2, err := e.d.PrepareHandoff(2); err == nil || h2 != nil {
		t.Fatalf("second handoff: %v %v", h2, err)
	}
	_ = e.d.Shutdown(context.Background())
}
