//go:build !windows

package tfbridge

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/daemon"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
)

// ---------------------------------------------------------------- a fake TinyFugue with a rendezvous
//
// rvTF mirrors what squat.c does: it owns the channel to the bridge it forked (a socketpair), listens on
// a unix socket in the data dir with a token, holds a bridge that knocked until it says ATTACH, then
// flushes and closes the old channel and continues on the new one, and re-announces its sessions on
// RESYNC. The real thing is exercised by TestRealTinyFugueReload (TF_BIN).

type rvMsg struct {
	gen int
	m   []string
}

type rvTF struct {
	t        *testing.T
	dir      string
	token    string
	sock     string
	instance string

	wmu  sync.Mutex
	cur  net.Conn
	curG int

	mu      sync.Mutex
	got     []rvMsg
	gen     int
	resyncs int
	sess    []string
	lines   map[string][]string
	bad     int // knocks refused
}

func shortDir(t *testing.T) string {
	d, err := os.MkdirTemp("/tmp", "tfrv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func newRvTF(t *testing.T, dir string) *rvTF {
	f := &rvTF{t: t, dir: dir, instance: "inst-rv", lines: map[string][]string{}, token: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	f.sock = filepath.Join(dir, "tf.sock")
	ln, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, TokenFile), []byte(f.token+"\n"+f.sock+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.candidate(c)
		}
	}()
	return f
}

func (f *rvTF) send(fields ...string) bool {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	if f.cur == nil {
		return false
	}
	_, err := f.cur.Write(Encode(fields...))
	return err == nil
}

func (f *rvTF) candidate(c net.Conn) {
	br := bufio.NewReader(c)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil {
		c.Close()
		return
	}
	m := Decode(strings.TrimSuffix(line, "\n"))
	if len(m) < 3 || m[0] != "KNOCK" || !TokenEqual(m[1], f.token) {
		f.mu.Lock()
		f.bad++
		f.mu.Unlock()
		c.Close()
		return
	}
	c.Write(Encode("HELLO", "1", "tf", f.instance, "4242", "lines,control,worlds"))
	_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))
	line, err = br.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ATTACH" {
		c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	f.wmu.Lock()
	old := f.cur
	f.cur = c
	f.mu.Lock()
	f.gen++
	g := f.gen
	f.curG = g
	f.mu.Unlock()
	f.wmu.Unlock()
	if old != nil {
		old.Close() // everything written to it before is delivered first: the EOF is the end marker
	}
	f.read(c, br, g)
}

func (f *rvTF) read(c net.Conn, r io.Reader, g int) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), MaxCtlLine)
	for sc.Scan() {
		m := Decode(sc.Text())
		f.mu.Lock()
		f.got = append(f.got, rvMsg{g, m})
		f.mu.Unlock()
		if m[0] == "RESYNC" {
			f.mu.Lock()
			f.resyncs++
			f.mu.Unlock()
			f.announceAll()
		}
	}
	f.wmu.Lock()
	if f.cur == c { // the bridge died: TinyFugue detaches and waits for a knock
		f.cur = nil
	}
	f.wmu.Unlock()
}

// spawn starts a bridge the way TinyFugue's /squat on does: fd 3 is its end of a socketpair.
func (f *rvTF) spawn(bin string, port int, env ...string) *exec.Cmd {
	f.t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	pf, cf := os.NewFile(uintptr(fds[0]), "tf"), os.NewFile(uintptr(fds[1]), "bridge")
	conn, err := net.FileConn(pf)
	pf.Close()
	if err != nil {
		f.t.Fatal(err)
	}
	cmd := exec.Command(bin, "serve", "--fd", "3", "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--data-dir", f.dir, "--insecure-loopback")
	cmd.Env = append(os.Environ(), env...)
	cmd.ExtraFiles = []*os.File{cf}
	if lf, err := os.OpenFile(filepath.Join(f.dir, "bridge.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err == nil {
		cmd.Stderr = lf // a file, not a pipe: bridges that outlive the test must not block Wait
		defer lf.Close()
	}
	if err := cmd.Start(); err != nil {
		f.t.Fatal(err)
	}
	cf.Close()
	f.t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	f.wmu.Lock()
	f.cur = conn
	f.mu.Lock()
	f.gen++
	g := f.gen
	f.curG = g
	f.mu.Unlock()
	f.wmu.Unlock()
	go f.read(conn, conn, g)
	f.send("HELLO", "1", "tf", f.instance, "4242", "lines,control,worlds")
	return cmd
}

func (f *rvTF) addSession(id, world string) {
	f.mu.Lock()
	f.sess = append(f.sess, id)
	f.mu.Unlock()
	f.announce(id, world)
}

func (f *rvTF) announce(id, world string) {
	f.send("SESS", id, world, "game.example", "4201", "0", "connected", "0", "1", "1700000000000", "")
	f.send("CTRL", id, "tf", "tf", "")
}

func (f *rvTF) announceAll() {
	f.mu.Lock()
	ids := append([]string(nil), f.sess...)
	f.mu.Unlock()
	for _, id := range ids {
		f.announce(id, "alpha")
		f.mu.Lock()
		ls := append([]string(nil), f.lines[id]...)
		f.mu.Unlock()
		for _, l := range ls { // history replay, like squat_sock_replay
			f.send("LINE", id, "g", "r", "game", "1700000000001", l)
		}
	}
}

func (f *rvTF) line(id, text string) {
	f.mu.Lock()
	f.lines[id] = append(f.lines[id], text)
	f.mu.Unlock()
	f.send("LINE", id, "g", "", "game", strconv.FormatInt(time.Now().UnixMilli(), 10), text)
}

func (f *rvTF) waitMsg(typ string, from int) ([]string, int) {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for i := from; i < len(f.got); i++ {
			if f.got[i].m[0] == typ {
				m := f.got[i].m
				f.mu.Unlock()
				return m, i + 1
			}
		}
		f.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("no %s message from the bridge", typ)
	return nil, 0
}

func (f *rvTF) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.got) }

func (f *rvTF) pairCode() string {
	f.t.Helper()
	from := f.count()
	if !f.send("PAIR", "operator") {
		f.t.Fatal("TinyFugue has no bridge attached")
	}
	m, _ := f.waitMsg("PAIRCODE", from)
	return m[1]
}

// ---------------------------------------------------------------- helpers

var (
	buildOnce sync.Once
	builtBin  string
	buildErr  error
)

// bridgeBinary builds the real tf-squatterd once.
func bridgeBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		d, err := os.MkdirTemp("", "tfsq-bin")
		if err != nil {
			buildErr = err
			return
		}
		builtBin = filepath.Join(d, "tf-squatterd")
		if out, err := exec.Command("go", "build", "-o", builtBin, "../../cmd/tf-squatterd").CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	// each test gets its own copy: it may replace it
	b, _ := os.ReadFile(builtBin)
	p := filepath.Join(shortDir(t), "tf-squatterd")
	if err := os.WriteFile(p, b, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitListen(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("nothing listens on %s", addr)
}

// rvEnv is a started bridge in front of an rvTF.
type rvEnv struct {
	t    *testing.T
	tf   *rvTF
	bin  string
	dir  string
	addr string
	port int
	cmd  *exec.Cmd
	h    *harness
}

func startRv(t *testing.T, env ...string) *rvEnv {
	t.Helper()
	bin := bridgeBinary(t)
	dir := shortDir(t)
	f := newRvTF(t, dir)
	port := freePort(t)
	e := &rvEnv{t: t, tf: f, bin: bin, dir: dir, port: port, addr: fmt.Sprintf("127.0.0.1:%d", port)}
	e.cmd = f.spawn(bin, port, env...)
	waitListen(t, e.addr)
	e.h = &harness{t: t, addr: e.addr, dir: dir}
	return e
}

// dialPaired connects with a device key that is already paired.
func (e *rvEnv) dialPaired(priv ed25519.PrivateKey) *wsClient {
	c := e.h.dialWith("", priv)
	c.ws.SetReadLimit(8 << 20)
	return c
}

func (e *rvEnv) pairNew() *wsClient {
	c := e.h.dialWith(e.tf.pairCode(), nil)
	c.ws.SetReadLimit(8 << 20)
	return c
}

func (e *rvEnv) reload() (int, error) {
	out, err := daemon.RequestReload(e.dir, false)
	return out.Status.PID, err
}

var nRe = regexp.MustCompile(`n=(\d+)`)

// backlogNumbers returns the n=<k> numbers in the session's whole scrollback, in order.
func backlogNumbers(t *testing.T, c *wsClient, sid string) []int {
	t.Helper()
	c.write(map[string]any{"v": 1, "t": "attach", "session": sid, "maxBacklog": 1000000})
	c.untilType("attached")
	var out []int
	for {
		bl := c.untilType("backlog")
		raw, _ := json.Marshal(bl["entries"])
		for _, m := range nRe.FindAllStringSubmatch(string(raw), -1) {
			n, _ := strconv.Atoi(m[1])
			out = append(out, n)
		}
		if bl["done"] == true {
			return out
		}
	}
}

func requireSeq(t *testing.T, got []int, n int) {
	t.Helper()
	if len(got) != n {
		t.Fatalf("scrollback has %d numbered lines, want %d: %v", len(got), n, got)
	}
	for i, v := range got {
		if v != i+1 {
			t.Fatalf("scrollback is not 1..%d in order (position %d holds %d): %v", n, i, v, got)
		}
	}
}

func (c *wsClient) closed(d time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	for {
		if _, _, err := c.ws.Read(ctx); err != nil {
			return ctx.Err() == nil
		}
	}
}

const rvSid = "w-alpha-1-1"

// ---------------------------------------------------------------- tests

func TestReloadKeepsTheControlChannelWithTrafficInFlight(t *testing.T) {
	e := startRv(t)
	e.tf.addSession(rvSid, "alpha")
	c := e.pairNew()
	priv := c.priv
	c.write(map[string]any{"v": 1, "t": "attach", "session": rvSid})
	c.untilType("backlog")

	// TinyFugue streams numbered lines (and a long one, to cut a message across the switch) without pause
	stop := make(chan struct{})
	var sent int
	var smu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		pad := strings.Repeat("x", 20000)
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			txt := fmt.Sprintf("n=%d %s", i, pad[:(i%5)*4000])
			e.tf.line(rvSid, txt)
			smu.Lock()
			sent = i
			smu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()
	time.Sleep(300 * time.Millisecond)
	// a read mark that must survive
	c.write(map[string]any{"v": 1, "t": "ack", "session": rvSid, "seq": 5})
	time.Sleep(100 * time.Millisecond)

	oldPID := e.cmd.Process.Pid
	pid, err := e.reload()
	if err != nil {
		t.Fatal(err)
	}
	if pid == 0 || pid == oldPID {
		t.Fatalf("new pid = %d, old = %d", pid, oldPID)
	}
	time.Sleep(300 * time.Millisecond) // more traffic on the new bridge
	close(stop)
	<-done
	smu.Lock()
	n := sent
	smu.Unlock()
	time.Sleep(300 * time.Millisecond)

	if !c.closed(3 * time.Second) {
		t.Fatal("the old client connection was not dropped by the reload")
	}
	e.tf.mu.Lock()
	gen := e.tf.curG
	e.tf.mu.Unlock()
	if gen < 2 {
		t.Fatalf("TinyFugue never switched channels (gen %d)", gen)
	}
	// reconnect with the same device (no pairing code): every line exactly once, in order
	c2 := e.dialPaired(priv)
	si := c2.welcome["sessions"].([]any)[0].(map[string]any)
	if si["lastSeq"].(float64) != float64(n) {
		t.Fatalf("lastSeq = %v, TinyFugue sent %d", si["lastSeq"], n)
	}
	if si["unread"].(float64) != float64(n-5) {
		t.Fatalf("read mark lost: unread = %v, want %d", si["unread"], n-5)
	}
	requireSeq(t, backlogNumbers(t, c2, rvSid), n)
	// the channel works both ways on the new bridge
	code := e.tf.pairCode()
	if code == "" {
		t.Fatal("no pairing code from the new bridge")
	}
	e.tf.mu.Lock()
	resyncs := e.tf.resyncs
	e.tf.mu.Unlock()
	if resyncs != 0 {
		t.Fatalf("a reload with state must not need a resync (got %d)", resyncs)
	}
	// and the old process is gone
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(oldPID, 0) != nil || e.cmd.ProcessState != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// swapBinary atomically replaces the binary the running bridge will start next.
func swapBinary(t *testing.T, path, script string) {
	t.Helper()
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func assertStillServing(t *testing.T, e *rvEnv, priv ed25519.PrivateKey, wantPID int) {
	t.Helper()
	if e.cmd.ProcessState != nil {
		t.Fatalf("the old bridge died: %v", e.cmd.ProcessState)
	}
	e.tf.mu.Lock()
	gen := e.tf.curG
	attached := e.tf.cur != nil
	e.tf.mu.Unlock()
	if !attached || gen != 1 {
		t.Fatalf("TinyFugue's channel changed (gen %d, attached %v): the rollback disturbed it", gen, attached)
	}
	c := e.dialPaired(priv)
	e.tf.line(rvSid, "n=1000")
	c.write(map[string]any{"v": 1, "t": "attach", "session": rvSid})
	c.untilType("attached")
	e.tf.line(rvSid, "n=1001")
	c.until(func(m map[string]any) bool {
		raw, _ := json.Marshal(m)
		return m["t"] == "line" && strings.Contains(string(raw), "n=1001")
	})
	info, err := daemon.ReloadInfo(e.dir)
	if err != nil || info.PID != wantPID {
		t.Fatalf("control socket: pid %d err %v, want %d", info.PID, err, wantPID)
	}
	if info.Last == nil || info.Last.Ok {
		t.Fatalf("the failed attempt is not reported in the status: %+v", info.Last)
	}
}

func TestReloadRollsBackWhenTheNewBinaryDiesAtStart(t *testing.T) {
	e := startRv(t)
	e.tf.addSession(rvSid, "alpha")
	c := e.pairNew()
	time.Sleep(100 * time.Millisecond)
	swapBinary(t, e.bin, "#!/bin/sh\nexit 3\n")
	_, err := e.reload()
	if err == nil || !strings.Contains(err.Error(), "kept running") {
		t.Fatalf("reload error = %v", err)
	}
	assertStillServing(t, e, c.priv, e.cmd.Process.Pid)
}

func TestReloadRollsBackWhenTheNewBinaryHangs(t *testing.T) {
	e := startRv(t, "TFSQUATTERD_RELOAD_TIMEOUT=2s")
	e.tf.addSession(rvSid, "alpha")
	c := e.pairNew()
	swapBinary(t, e.bin, "#!/bin/sh\nexec sleep 60\n")
	start := time.Now()
	_, err := e.reload()
	if err == nil || !strings.Contains(err.Error(), "kept running") || !strings.Contains(err.Error(), "ready") {
		t.Fatalf("reload error = %v", err)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatalf("a hung binary took %s to give up", time.Since(start))
	}
	assertStillServing(t, e, c.priv, e.cmd.Process.Pid)
}

func TestReloadRefusesTooNewState(t *testing.T) {
	e := startRv(t)
	e.tf.addSession(rvSid, "alpha")
	c := e.pairNew()
	real, _ := os.ReadFile(e.bin)
	realPath := e.bin + ".real"
	if err := os.WriteFile(realPath, real, 0o755); err != nil {
		t.Fatal(err)
	}
	// a "newer" binary: it finds a state it cannot read. (The wrapper rewrites the draft's minReader.)
	swapBinary(t, e.bin, fmt.Sprintf("#!/bin/sh\nsed 's/\"minReader\": 1/\"minReader\": 99/' \"$TFSQUATTERD_HANDOFF\" > \"$TFSQUATTERD_HANDOFF.x\" && mv \"$TFSQUATTERD_HANDOFF.x\" \"$TFSQUATTERD_HANDOFF\"\nexec %s \"$@\"\n", realPath))
	_, err := e.reload()
	if err == nil || !strings.Contains(err.Error(), "kept running") {
		t.Fatalf("reload error = %v", err)
	}
	assertStillServing(t, e, c.priv, e.cmd.Process.Pid)
}

func TestConcurrentReloadsAreSerialised(t *testing.T) {
	e := startRv(t)
	e.tf.addSession(rvSid, "alpha")
	c := e.pairNew()
	for i := 1; i <= 20; i++ {
		e.tf.line(rvSid, fmt.Sprintf("n=%d", i))
	}
	time.Sleep(200 * time.Millisecond)
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := daemon.Control(e.dir, "reload", 90*time.Second, nil)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case strings.Contains(err.Error(), "already in progress"), strings.Contains(err.Error(), "already handed over"),
			strings.Contains(err.Error(), "without saying"), strings.Contains(err.Error(), "no running"):
		default:
			t.Errorf("unexpected reload error: %v", err)
		}
	}
	if ok < 1 {
		t.Fatal("no reload succeeded")
	}
	time.Sleep(500 * time.Millisecond)
	c2 := e.dialPaired(c.priv)
	_ = c2
	requireSeq(t, backlogNumbers(t, c2, rvSid), 20)
	if e.tf.pairCode() == "" {
		t.Fatal("no bridge answers")
	}
}

func TestReloadTwiceInARow(t *testing.T) {
	e := startRv(t)
	e.tf.addSession(rvSid, "alpha")
	c := e.pairNew()
	e.tf.line(rvSid, "n=1")
	p1, err := e.reload()
	if err != nil {
		t.Fatal(err)
	}
	e.tf.line(rvSid, "n=2")
	p2, err := e.reload()
	if err != nil {
		t.Fatal(err)
	}
	e.tf.line(rvSid, "n=3")
	if p1 == p2 {
		t.Fatalf("same pid twice: %d", p1)
	}
	time.Sleep(200 * time.Millisecond)
	info, _ := daemon.ReloadInfo(e.dir)
	if info.Generation != 2 || info.Last == nil || !info.Last.Ok {
		t.Fatalf("status after two reloads: %+v", info)
	}
	requireSeq(t, backlogNumbers(t, e.dialPaired(c.priv), rvSid), 3)
}

func TestAClientReconnectsAndReplaysByDeviceReadMark(t *testing.T) {
	e := startRv(t)
	e.tf.addSession(rvSid, "alpha")
	c := e.pairNew()
	c.write(map[string]any{"v": 1, "t": "attach", "session": rvSid})
	c.untilType("backlog")
	e.tf.line(rvSid, "n=1")
	e.tf.line(rvSid, "n=2")
	c.until(func(m map[string]any) bool { return m["t"] == "line" && m["seq"] == float64(2) })
	if _, err := e.reload(); err != nil {
		t.Fatal(err)
	}
	if !c.closed(3 * time.Second) {
		t.Fatal("client not dropped")
	}
	e.tf.line(rvSid, "n=3") // arrives while no client is connected
	time.Sleep(200 * time.Millisecond)
	c2 := e.dialPaired(c.priv)
	since := float64(2)
	c2.write(map[string]any{"v": 1, "t": "attach", "session": rvSid, "since": since})
	c2.untilType("attached")
	bl := c2.untilType("backlog")
	raw, _ := json.Marshal(bl["entries"])
	if !strings.Contains(string(raw), "n=3") || strings.Contains(string(raw), "n=2") {
		t.Fatalf("replay since 2 = %s", raw)
	}
}

func TestBridgeStartedByHandAttachesToAWaitingTinyFugue(t *testing.T) {
	e := startRv(t)
	e.tf.addSession(rvSid, "alpha")
	c := e.pairNew()
	for i := 1; i <= 5; i++ {
		e.tf.line(rvSid, fmt.Sprintf("n=%d", i))
	}
	time.Sleep(200 * time.Millisecond)
	// the bridge is killed: TinyFugue detaches and keeps its rendezvous and its sessions
	e.cmd.Process.Kill()
	e.cmd.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.tf.wmu.Lock()
		gone := e.tf.cur == nil
		e.tf.wmu.Unlock()
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("TinyFugue never noticed the bridge died")
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.tf.line(rvSid, "n=6") // not delivered: nobody is attached
	// "tf-squatterd reload" with no bridge running starts one that knocks
	var out strings.Builder
	cmd := exec.Command(e.bin, "reload", "--data-dir", e.dir, "--port", strconv.Itoa(e.port), "--bind", "127.0.0.1", "--insecure-loopback")
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("reload: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "started one") {
		t.Fatalf("output: %s", out.String())
	}
	t.Cleanup(func() {
		if info, err := daemon.ReloadInfo(e.dir); err == nil {
			syscall.Kill(info.PID, syscall.SIGKILL)
		}
	})
	waitListen(t, e.addr)
	e.tf.mu.Lock()
	rs := e.tf.resyncs
	e.tf.mu.Unlock()
	if rs != 1 {
		t.Fatalf("resyncs = %d", rs)
	}
	// TinyFugue replayed its history (n=1..6 as it holds them) and the new bridge mirrors it
	c2 := e.dialPaired(c.priv)
	time.Sleep(300 * time.Millisecond)
	_ = c2
	c3 := e.dialPaired(c.priv)
	requireSeq(t, backlogNumbers(t, c3, rvSid), 6)
}

func TestBadTokenKnockIsRefusedAndChangesNothing(t *testing.T) {
	e := startRv(t)
	e.tf.addSession(rvSid, "alpha")
	e.pairNew()
	bad := filepath.Join(e.dir, "bad")
	os.MkdirAll(bad, 0o700)
	os.WriteFile(filepath.Join(bad, TokenFile), []byte(strings.Repeat("0", 64)+"\n"+e.tf.sock+"\n"), 0o600)
	if _, err := Knock(bad, 2*time.Second); err == nil {
		t.Fatal("a knock with a wrong token was accepted")
	}
	e.tf.mu.Lock()
	b, gen := e.tf.bad, e.tf.curG
	e.tf.mu.Unlock()
	if b != 1 || gen != 1 {
		t.Fatalf("bad=%d gen=%d", b, gen)
	}
	if e.tf.pairCode() == "" {
		t.Fatal("the attached bridge was disturbed")
	}
}

func TestStateCompatibility(t *testing.T) {
	st := State{Generation: 3, CreatedAt: 5, Bridge: BridgeState{Instance: "i", TFPID: "1", Features: []string{"lines"}},
		Sessions: []SessionState{{ID: "a", World: "w", Input: "tf", Automation: "tf", LastSeq: 7, Acks: map[string]uint64{"d": 4}}}}
	b, err := EncodeState(st)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeState(b)
	if err != nil || got.Generation != 3 || got.Sessions[0].Acks["d"] != 4 || got.Bridge.Instance != "i" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	mut := func(f func(m map[string]any)) []byte {
		var m map[string]any
		json.Unmarshal(b, &m)
		f(m)
		o, _ := json.Marshal(m)
		return o
	}
	// too new: the envelope says older readers must refuse
	if _, err := DecodeState(mut(func(m map[string]any) { m["format"] = 2; m["minReader"] = 2 })); !errorsIs(err, ErrStateTooNew) {
		t.Fatalf("minReader 2: %v", err)
	}
	// a newer writer that only added things keeps minReader: still readable, extra optional sections skipped
	got, err = DecodeState(mut(func(m map[string]any) {
		m["format"] = 2
		m["sections"].(map[string]any)["extra"] = map[string]any{"v": 1, "data": 1}
	}))
	if err != nil || len(got.Skipped) != 1 || got.Skipped[0] != "extra" {
		t.Fatalf("optional unknown section: %+v %v", got, err)
	}
	// an unknown REQUIRED section, or a known one at a newer version, is refused
	if _, err := DecodeState(mut(func(m map[string]any) {
		m["sections"].(map[string]any)["extra"] = map[string]any{"v": 1, "required": true, "data": 1}
	})); !errorsIs(err, ErrStateTooNew) {
		t.Fatalf("required unknown: %v", err)
	}
	if _, err := DecodeState(mut(func(m map[string]any) {
		m["sections"].(map[string]any)["sessions"].(map[string]any)["v"] = 2
	})); !errorsIs(err, ErrStateTooNew) {
		t.Fatalf("sessions v2: %v", err)
	}
	for name, data := range map[string][]byte{"garbage": []byte("{"), "empty": []byte("{}"), "nosessions": mut(func(m map[string]any) { delete(m["sections"].(map[string]any), "sessions") }),
		"badid": mut(func(m map[string]any) {
			m["sections"].(map[string]any)["sessions"].(map[string]any)["data"] = []any{map[string]any{"id": "../x"}}
		})} {
		if _, err := DecodeState(data); !errorsIs(err, ErrStateCorrupt) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// the file is private and written atomically
	p := filepath.Join(t.TempDir(), StateFile)
	if err := WriteState(p, st); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func errorsIs(err, target error) bool {
	return err != nil && strings.Contains(err.Error(), target.Error())
}

// keep imports used even when a test above changes
var _ = websocket.MessageText
var _ = auth.GenerateKey
