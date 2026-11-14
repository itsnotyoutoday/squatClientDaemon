//go:build !windows

package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/daemon"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// proc is a running squatterd binary; its log is scanned for the listening address.
type proc struct {
	t    *testing.T
	cmd  *osexec.Cmd
	mu   sync.Mutex
	log  strings.Builder
	addr chan string
	done chan error
}

var listenRe = regexp.MustCompile(`url=ws://([0-9.:]+)/swp`)

func startProc(t *testing.T, bin string, args ...string) *proc {
	t.Helper()
	p := &proc{t: t, addr: make(chan string, 1), done: make(chan error, 1)}
	p.cmd = osexec.Command(bin, args...)
	pr, pw, _ := os.Pipe()
	p.cmd.Stdout, p.cmd.Stderr = pw, pw
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = pw.Close()
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			l := sc.Text()
			p.mu.Lock()
			p.log.WriteString(l + "\n")
			p.mu.Unlock()
			if m := listenRe.FindStringSubmatch(l); m != nil {
				select {
				case p.addr <- m[1]:
				default:
				}
			}
		}
	}()
	go func() { p.done <- p.cmd.Wait() }()
	t.Cleanup(func() { _ = p.cmd.Process.Signal(syscall.SIGKILL) })
	return p
}

func (p *proc) logText() string { p.mu.Lock(); defer p.mu.Unlock(); return p.log.String() }

func (p *proc) address() string {
	select {
	case a := <-p.addr:
		return a
	case <-time.After(15 * time.Second):
		p.t.Fatalf("squatterd did not start:\n%s", p.logText())
	}
	return ""
}

func buildBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "squatterd")
	if out, err := osexec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func control(t *testing.T, dir, cmd string) string {
	t.Helper()
	sock, err := controlPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.DialTimeout("unix", sock, 3*time.Second)
	if err != nil {
		t.Fatalf("control socket: %v", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	_, _ = c.Write([]byte(cmd + "\n"))
	var out strings.Builder
	sc := bufio.NewScanner(c)
	for sc.Scan() {
		out.WriteString(sc.Text() + "\n")
	}
	return out.String()
}

func dialWS(t *testing.T, addr string, key ed25519.PrivateKey, code string) *wire.Client {
	t.Helper()
	var last error
	for i := 0; i < 40; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, err := wire.Dial(ctx, "ws://"+addr+"/swp", wire.ClientOptions{Key: key, PairCode: code, DeviceName: "t", InsecureLoopback: true})
		cancel()
		if err == nil {
			t.Cleanup(func() { c.Close() })
			return c
		}
		last = err
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("dial: %v", last)
	return nil
}

// waitLines waits until every text has been seen as a line, live or in a replay, in any order.
func waitLines(t *testing.T, c *wire.Client, texts ...string) {
	t.Helper()
	need := map[string]bool{}
	for _, x := range texts {
		need[x] = true
	}
	deadline := time.After(30 * time.Second)
	for len(need) > 0 {
		select {
		case ev, ok := <-c.Events():
			if !ok {
				t.Fatalf("connection closed waiting for %v", need)
			}
			switch e := ev.(type) {
			case wire.Line:
				delete(need, e.Plain())
			case wire.Backlog:
				for _, en := range e.Entries {
					if en.Line != nil {
						delete(need, en.Line.Plain())
					}
				}
			}
		case <-deadline:
			t.Fatalf("timeout waiting for lines %v", need)
		}
	}
}

// The whole thing with real processes: a game connection survives "squatterd reload" (the old process
// exits, a new one serves), the game never sees a disconnect, nothing is lost, and a client that
// reconnects replays what it missed.
func TestReloadKeepsTheGameConnection(t *testing.T) {
	bin := buildBinary(t)
	dir := t.TempDir()
	p1 := startProc(t, bin, "serve", "--insecure-loopback", "--bind", "127.0.0.1", "--port", "0", "--data-dir", dir)
	addr := p1.address()
	as, _ := auth.Open(dir)
	code, _, _ := as.NewPairing()
	key, _ := auth.GenerateKey()
	c1 := dialWS(t, addr, key, code)
	game := fakemush.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := c1.Connect(ctx, proto.World{Name: "w", Host: game.Host(), Port: game.Port()})
	if err != nil {
		t.Fatal(err)
	}
	if err := c1.Attach(ctx, info.ID, nil, 0); err != nil {
		t.Fatal(err)
	}
	game.SendLine("before the reload")
	waitLines(t, c1, "before the reload")
	peer := game.Peer()
	fromBefore := peer.RemoteAddr() // the squatterd's side of the TCP connection to the game
	game.Send([]byte("half a li"))  // an unfinished line must survive too

	var out strings.Builder
	rc := run(context.Background(), []string{"reload", "--data-dir", dir}, &out, &out, nil)
	if rc != 0 || !strings.Contains(out.String(), "reloaded") {
		t.Fatalf("reload: %d\n%s\nlog:\n%s", rc, out.String(), p1.logText())
	}
	select {
	case err := <-p1.done:
		if err != nil {
			t.Fatalf("old process: %v\n%s", err, p1.logText())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the old process did not exit:\n%s", p1.logText())
	}
	st := control(t, dir, "status")
	if !strings.Contains(st, "generation 1") {
		t.Fatalf("status of the new process: %q", st)
	}
	stopPID(t, st)

	game.Send([]byte("ne\r\n"))
	game.SendLine("after the reload")
	// the old client was dropped; reconnect to the new process on the same address
	c2 := dialWS(t, addr, key, "")
	var got proto.SessionInfo
	for _, i := range c2.Sessions() {
		if i.ID == info.ID {
			got = i
		}
	}
	if got.State != proto.StateConnected {
		t.Fatalf("the session after the reload: %+v", got)
	}
	if err := c2.Attach(ctx, info.ID, nil, 0); err != nil {
		t.Fatal(err)
	}
	waitLines(t, c2, "before the reload", "half a line", "after the reload")
	// input still reaches the game over the very same connection
	if err := c2.Send(ctx, info.ID, "hello again"); err != nil {
		t.Fatal(err)
	}
	game.ExpectLine("hello again")
	// and it really is the very same TCP connection: the game saw exactly one, from the same source
	// port, and it was never closed
	if n := game.Connections(); n != 1 {
		t.Fatalf("the game saw %d connections, want 1", n)
	}
	if game.Peer().RemoteAddr() != fromBefore || peer.Gone() {
		t.Fatalf("the connection changed or closed: before %s, now %s, gone=%v", fromBefore, game.Peer().RemoteAddr(), peer.Gone())
	}
}

// An upgrade to a broken binary fails safely: the old process takes everything back and carries on.
func TestReloadWithABrokenBinaryRollsBack(t *testing.T) {
	bin := buildBinary(t)
	dir := t.TempDir()
	p1 := startProc(t, bin, "serve", "--insecure-loopback", "--bind", "127.0.0.1", "--port", "0", "--data-dir", dir)
	addr := p1.address()
	as, _ := auth.Open(dir)
	code, _, _ := as.NewPairing()
	key, _ := auth.GenerateKey()
	c1 := dialWS(t, addr, key, code)
	game := fakemush.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := c1.Connect(ctx, proto.World{Name: "w", Host: game.Host(), Port: game.Port()})
	if err != nil {
		t.Fatal(err)
	}
	if err := c1.Attach(ctx, info.ID, nil, 0); err != nil {
		t.Fatal(err)
	}
	game.SendLine("one")
	waitLines(t, c1, "one")
	peer := game.Peer()
	fromBefore := peer.RemoteAddr()

	// the "upgrade": the binary on disk is replaced by something that cannot work
	if err := os.WriteFile(bin+".new", []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(bin+".new", bin); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	rc := run(context.Background(), []string{"reload", "--data-dir", dir}, &out, &out, nil)
	if rc == 0 || !strings.Contains(out.String(), "kept running") {
		t.Fatalf("a broken upgrade must fail and say it kept running: %d\n%s", rc, out.String())
	}
	// the old process is still there, serving, with the game connection intact
	select {
	case err := <-p1.done:
		t.Fatalf("the old process exited: %v\n%s", err, p1.logText())
	case <-time.After(300 * time.Millisecond):
	}
	game.SendLine("two")
	c2 := dialWS(t, addr, key, "")
	if err := c2.Attach(ctx, info.ID, nil, 0); err != nil {
		t.Fatal(err)
	}
	waitLines(t, c2, "one", "two")
	if err := c2.Send(ctx, info.ID, "still me"); err != nil {
		t.Fatal(err)
	}
	game.ExpectLine("still me")
	// the failed upgrade never touched the connection to the game
	if n := game.Connections(); n != 1 || game.Peer().RemoteAddr() != fromBefore || peer.Gone() {
		t.Fatalf("the game saw %d connections; before %s, now %s, gone=%v", n, fromBefore, game.Peer().RemoteAddr(), peer.Gone())
	}
}

// stopPID stops the process named in a control "status" reply when the test ends: a reloaded squatterd
// is independent of the one the test started, so the test has to stop it.
func stopPID(t *testing.T, status string) {
	t.Helper()
	var pid int
	for _, l := range strings.Split(status, "\n") {
		if strings.HasPrefix(l, "pid ") {
			pid = atoi(strings.TrimPrefix(l, "pid "))
		}
	}
	if pid <= 0 {
		t.Fatalf("no pid in %q", status)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		for i := 0; i < 100; i++ {
			if syscall.Kill(pid, 0) != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	})
}

func atoi(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// ---- more reload scenarios with real processes

// reloadFixture starts a squatterd, one session and an attached client.
type reloadFixture struct {
	t          *testing.T
	lastReload string // output of the last reload, for failure diagnostics
	bin        string
	dir        string
	p          *proc
	addr       string
	key        ed25519.PrivateKey
	c          *wire.Client
	game       *fakemush.Server
	info       proto.SessionInfo
	ctx        context.Context
	from       string // the squatterd's side of the TCP connection to the game
	peer       *fakemush.Peer
}

func newReloadFixture(t *testing.T) *reloadFixture {
	t.Helper()
	return newReloadFixtureWith(t, nil, nil)
}

// newReloadFixtureWith is newReloadFixture with a game of the test's choosing (nil: a plain one) and a
// change to the world (for example TLS).
func newReloadFixtureWith(t *testing.T, game *fakemush.Server, world func(*proto.World)) *reloadFixture {
	t.Helper()
	f := newReloadFixtureBase(t)
	if game == nil {
		game = fakemush.New(t)
	}
	f.game = game
	w := proto.World{Name: "w", Host: f.game.Host(), Port: f.game.Port()}
	if world != nil {
		world(&w)
	}
	f.openSession(w)
	f.game.SendLine("hello")
	waitLines(t, f.c, "hello")
	f.peer = f.game.Peer()
	f.from = f.peer.RemoteAddr()
	return f
}

// newReloadFixtureBase starts a squatterd and an attached admin client, with no session yet.
func newReloadFixtureBase(t *testing.T) *reloadFixture {
	t.Helper()
	f := &reloadFixture{t: t, bin: buildBinary(t), dir: t.TempDir()}
	f.p = startProc(t, f.bin, "serve", "--insecure-loopback", "--bind", "127.0.0.1", "--port", "0", "--data-dir", f.dir)
	f.addr = f.p.address()
	as, _ := auth.Open(f.dir)
	code, _, _ := as.NewPairingRole(time.Minute, "admin")
	f.key, _ = auth.GenerateKey()
	f.c = dialWS(t, f.addr, f.key, code)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	f.ctx = ctx
	return f
}

// openSession connects the world and attaches the client to it.
func (f *reloadFixture) openSession(w proto.World) {
	f.t.Helper()
	var err error
	if f.info, err = f.c.Connect(f.ctx, w); err != nil {
		f.t.Fatal(err)
	}
	if err := f.c.Attach(f.ctx, f.info.ID, nil, 0); err != nil {
		f.t.Fatal(err)
	}
}

// intact checks that the game saw exactly one connection, the original one, never closed.
func (f *reloadFixture) intact() {
	f.t.Helper()
	if n := f.game.Connections(); n != 1 || f.game.Peer().RemoteAddr() != f.from || f.peer.Gone() {
		f.t.Fatalf("the game saw %d connections; before %s, now %s, gone=%v", n, f.from, f.game.Peer().RemoteAddr(), f.peer.Gone())
	}
}

// replace swaps the binary on disk like an upgrade does.
func (f *reloadFixture) replace(script string) {
	f.t.Helper()
	if err := os.WriteFile(f.bin+".new", []byte(script), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Rename(f.bin+".new", f.bin); err != nil {
		f.t.Fatal(err)
	}
}

func (f *reloadFixture) reload(args ...string) (int, string) {
	var out strings.Builder
	rc := run(context.Background(), append([]string{"reload", "--data-dir", f.dir}, args...), &out, &out, nil)
	return rc, out.String()
}

// serving checks that squatterd still answers, a client can reconnect, replay and type, and nothing
// that the game sent in the meantime is missing.
func (f *reloadFixture) serving(want ...string) {
	f.t.Helper()
	c := dialWS(f.t, f.addr, f.key, "")
	if err := c.Attach(f.ctx, f.info.ID, nil, 0); err != nil {
		f.t.Fatal(err)
	}
	waitLines(f.t, c, want...)
	if err := c.Send(f.ctx, f.info.ID, "still here"); err != nil {
		f.t.Fatal(err)
	}
	f.game.ExpectLine("still here")
}

func TestReloadStatusNoticeAndCleanup(t *testing.T) {
	f := newReloadFixture(t)
	rc, out := f.reload()
	if rc != 0 {
		t.Fatalf("%d %s", rc, out)
	}
	// the client was told before it was dropped
	sawNotice := false
	deadline := time.After(5 * time.Second)
wait:
	for {
		select {
		case ev, ok := <-f.c.Events():
			if !ok {
				break wait
			}
			if x, isExt := ev.(wire.Ext); isExt && x.T == "server.restarting" {
				sawNotice = true
			}
		case <-deadline:
			break wait
		}
	}
	if !sawNotice {
		t.Fatal("the client never got server.restarting")
	}
	st := control(t, f.dir, "status")
	stopPID(t, st)
	// no state file lying around once the new process confirmed
	if _, err := os.Stat(filepath.Join(f.dir, handoffFile)); !os.IsNotExist(err) {
		t.Fatalf("handoff.json is still there: %v", err)
	}
	var sb strings.Builder
	if rc := run(context.Background(), []string{"status", "--data-dir", f.dir}, &sb, &sb, nil); rc != 0 {
		t.Fatal(sb.String())
	}
	for _, want := range []string{"running:     yes", "generation:  1", "last reload: ", " ok, 1 connection(s) carried, 0 ended", "next reload: carries 1 session(s)"} {
		if !strings.Contains(sb.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, sb.String())
		}
	}
	// the client can read it too
	c2 := dialWS(t, f.addr, f.key, "")
	ri := c2.ReloadInfo()
	if ri == nil || ri.Generation != 1 || ri.Last == nil || !ri.Last.Ok || ri.Last.Carried != 1 || !ri.Supported {
		t.Fatalf("welcome.reload: %+v", ri)
	}
	// and an admin can reload from the client; it answers first
	if _, err := c2.AdminReload(f.ctx, false, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		if st := control(t, f.dir, "info"); strings.Contains(st, `"generation":2`) {
			stopPID(t, control(t, f.dir, "status"))
			break
		}
		if i > 100 {
			t.Fatalf("admin.reload did not lead to generation 2: %s", control(t, f.dir, "info"))
		}
		time.Sleep(100 * time.Millisecond)
	}
	f.intact()
}

// Two reloads at once: one wins, the other is told so (or runs after it); never a corrupt state.
func TestConcurrentReloads(t *testing.T) {
	f := newReloadFixture(t)
	var wg sync.WaitGroup
	rcs := make([]int, 3)
	outs := make([]string, 3)
	for i := range rcs {
		wg.Add(1)
		go func() { defer wg.Done(); rcs[i], outs[i] = f.reload() }()
	}
	wg.Wait()
	ok := 0
	for i, rc := range rcs {
		if rc == 0 {
			ok++
		} else if !strings.Contains(outs[i], "already in progress") && !strings.Contains(outs[i], "already handed over") && !strings.Contains(outs[i], "no running squatterd") && !strings.Contains(outs[i], "closed the connection") {
			t.Errorf("unexpected failure: %s", outs[i])
		}
	}
	if ok < 1 {
		t.Fatalf("no reload succeeded: %q", outs)
	}
	st := control(t, f.dir, "status")
	stopPID(t, st)
	f.game.SendLine("after the race")
	f.serving("hello", "after the race")
	f.intact()
}

// The new binary refuses the state (too new, corrupt) before touching anything: the old process
// carries on with the connection, the bytes that arrived meanwhile are not lost, and a later good
// reload works.
func TestReloadRefusedStateRollsBack(t *testing.T) {
	f := newReloadFixture(t)
	real := f.bin + ".good"
	data, err := os.ReadFile(f.bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, data, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, sedExpr := range map[string]string{
		"too new":   `s/"format": 2/"format": 99/; s/"minReader": 2/"minReader": 99/`,
		"corrupt":   `s/"sessions": {/"sessions": [/`,
		"bad state": `s/"state": 0/"state": 77/`,
	} {
		script := "#!/bin/sh\nt=$(mktemp)\nsed '" + sedExpr + "' \"$SQUATTERD_HANDOFF\" > \"$t\"\nSQUATTERD_HANDOFF=$t exec " + real + " \"$@\"\n"
		f.replace(script)
		f.game.SendLine("sent during " + name)
		rc, out := f.reload()
		if rc == 0 || !strings.Contains(out, "kept running") {
			t.Fatalf("%s: a refused state must fail and say it kept running: %d\n%s\nlog:\n%s", name, rc, out, f.p.logText())
		}
		select {
		case err := <-f.p.done:
			t.Fatalf("%s: the old process exited: %v", name, err)
		case <-time.After(200 * time.Millisecond):
		}
		f.serving("hello", "sent during "+name)
		f.intact()
		if _, err := os.Stat(filepath.Join(f.dir, handoffFile)); !os.IsNotExist(err) {
			t.Fatalf("%s: handoff.json left behind", name)
		}
		var sb strings.Builder
		run(context.Background(), []string{"status", "--data-dir", f.dir}, &sb, &sb, nil)
		if !strings.Contains(sb.String(), "FAILED and was rolled back") || !strings.Contains(sb.String(), "generation:  0") {
			t.Fatalf("%s: status after a rollback:\n%s", name, sb.String())
		}
	}
	// and now a good binary: the reload works after all the failures
	f.replace("#!/bin/sh\nexec " + real + " \"$@\"\n")
	rc, out := f.reload()
	if rc != 0 {
		t.Fatalf("%d\n%s\n%s", rc, out, f.p.logText())
	}
	stopPID(t, control(t, f.dir, "status"))
	f.game.SendLine("after the good one")
	f.serving("hello", "after the good one")
	f.intact()
}

// A new binary that hangs without ever saying ready is killed after the timeout and the old process
// carries on.
func TestReloadWithAHangingBinaryRollsBack(t *testing.T) {
	t.Setenv("SQUATTERD_RELOAD_TIMEOUT", "2s")
	f := newReloadFixture(t)
	f.replace("#!/bin/sh\nexec sleep 300\n")
	start := time.Now()
	rc, out := f.reload()
	if rc == 0 || !strings.Contains(out, "not ready within 2s") || !strings.Contains(out, "kept running") {
		t.Fatalf("%d\n%s", rc, out)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("the rollback took %s", d)
	}
	f.game.SendLine("after the hang")
	f.serving("hello", "after the hang")
	f.intact()
}

// The plan: a TLS session that cannot be migrated (TLS 1.2 with a CBC suite) blocks an unforced reload
// (nothing is touched) and --force goes ahead and ends exactly that session; the plain one and a
// migratable TLS 1.3 one are carried and never listed.
func TestReloadNeedsForceForTLSSessions(t *testing.T) {
	f := newReloadFixture(t)
	secure := cbcGame(t) // TLS 1.2 with a CBC suite: the one kind of TLS session that cannot be migrated
	ts, err := f.c.Connect(f.ctx, proto.World{Name: "secure", Host: secure.Host(), Port: secure.Port(), TLS: true, TLSVerify: "insecure"})
	if err != nil {
		t.Fatal(err)
	}
	modern := fakemush.New(t, fakemush.WithTLS())
	ms, err := f.c.Connect(f.ctx, proto.World{Name: "modern", Host: modern.Host(), Port: modern.Port(), TLS: true, TLSVerify: "insecure"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		if p := control(t, f.dir, "plan"); strings.Contains(p, ts.ID) && strings.Contains(p, ms.ID) && !strings.Contains(p, "connecting") {
			break
		}
		if i > 100 {
			t.Fatal("the TLS sessions never finished connecting in the plan")
		}
		time.Sleep(50 * time.Millisecond)
	}
	plan, err := daemon.ReloadPlan(f.dir)
	if err != nil || len(plan.End) != 1 || plan.End[0].ID != ts.ID || plan.End[0].Reason != "tls" || len(plan.Carry) != 2 {
		t.Fatalf("%v %+v", err, plan)
	}
	rc, out := f.reload()
	if rc == 0 || !strings.Contains(out, "would end") || !strings.Contains(out, "--force") {
		t.Fatalf("%d %s", rc, out)
	}
	if _, err := daemon.RequestReload(f.dir, false); err == nil {
		t.Fatal("RequestReload without force reloaded")
	} else if ref := new(daemon.ReloadRefused); !errors.As(err, &ref) || len(ref.Plan.End) != 1 {
		t.Fatalf("%v", err)
	}
	select {
	case <-f.p.done:
		t.Fatal("a refused reload stopped the process")
	case <-time.After(200 * time.Millisecond):
	}
	res, err := daemon.RequestReload(f.dir, true)
	if err != nil {
		t.Fatal(err)
	}
	stopPID(t, "pid "+itoa(res.Status.PID))
	if res.Status.Generation != 1 || res.Status.Last == nil || len(res.Status.Last.Ended) != 1 || res.Status.Last.Ended[0] != ts.ID {
		t.Fatalf("%+v", res.Status)
	}
	c2 := dialWS(t, f.addr, f.key, "")
	for _, i := range c2.Sessions() {
		switch i.ID {
		case ts.ID:
			if i.State != proto.StateEnded || i.EndReason != proto.EndSquatterRestarted {
				t.Fatalf("%+v", i)
			}
		case f.info.ID, ms.ID:
			if i.State != proto.StateConnected {
				t.Fatalf("%+v", i)
			}
		}
	}
	f.intact()
	if err := c2.Send(f.ctx, ms.ID, "modern after"); err != nil {
		t.Fatal(err)
	}
	modern.Peer().ExpectLine("modern after")
	if modern.Connections() != 1 {
		t.Fatalf("the migrated TLS game saw %d connections", modern.Connections())
	}
}

func TestReloadWhenNothingRuns(t *testing.T) {
	dir := t.TempDir()
	if _, err := daemon.ReloadPlan(dir); !errors.Is(err, daemon.ErrNotRunning) {
		t.Fatalf("%v", err)
	}
	if _, err := daemon.RequestReload(dir, false); !errors.Is(err, daemon.ErrNotRunning) {
		t.Fatalf("%v", err)
	}
	var out strings.Builder
	if rc := run(context.Background(), []string{"status", "--data-dir", dir}, &out, &out, nil); rc != 0 || !strings.Contains(out.String(), "running:     no") {
		t.Fatalf("%d\n%s", rc, out.String())
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
