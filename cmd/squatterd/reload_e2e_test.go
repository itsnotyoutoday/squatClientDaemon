//go:build !windows

package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

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
	deadline := time.After(10 * time.Second)
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
	game.Send([]byte("half a li")) // an unfinished line must survive too

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
