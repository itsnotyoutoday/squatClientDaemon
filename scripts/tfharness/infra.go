package main

import (
	"bytes"
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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/ansi"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// ---------------------------------------------------------------- small helpers

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
func (s *syncBuf) Len() int                    { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Len() }

func freePort() int {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// freePortBelow returns a free port in [lo, hi]. (CobraMUSH clamps its port to 32000.)
func freePortBelow(lo, hi int) int {
	for p := lo + int(time.Now().UnixNano()%int64(hi-lo)); ; p++ {
		if p > hi {
			p = lo
		}
		if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err == nil {
			l.Close()
			return p
		}
	}
}

func portOpen(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func waitPort(port int, d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if portOpen(port) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// moduleRoot finds the directory holding go.mod, walking up from the cwd.
func moduleRoot() (string, error) {
	d, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		p := filepath.Dir(d)
		if p == d {
			return "", fmt.Errorf("go.mod not found; run from inside squatClientDaemon")
		}
		d = p
	}
}

// ---------------------------------------------------------------- results

type result struct {
	group, name string
	ok          bool
	skipped     bool
	detail      string
}

type report struct {
	mu  sync.Mutex
	all []result
}

func (r *report) add(x result) { r.mu.Lock(); r.all = append(r.all, x); r.mu.Unlock() }

// ---------------------------------------------------------------- game servers

type gameServer struct {
	name string
	port int
	stop func()
}

// startCobra runs a private copy of CobraMUSH on a free port. God is #1 with
// password "potrzebie" (a fresh database).
func startCobra(work, gameDir string) (*gameServer, error) {
	if _, err := os.Stat(filepath.Join(gameDir, "netmush")); err != nil {
		return nil, fmt.Errorf("no netmush in %s", gameDir)
	}
	dst := filepath.Join(work, "cobra")
	if out, err := exec.Command("cp", "-R", gameDir, dst).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("copying the game: %v\n%s", err, out)
	}
	_ = os.Remove(filepath.Join(dst, "data", "outdb"))
	port := freePortBelow(20000, 31900) // CobraMUSH limits its port to 32000
	cnf := filepath.Join(dst, "mush.cnf")
	b, err := os.ReadFile(cnf)
	if err != nil {
		return nil, err
	}
	b = regexp.MustCompile(`(?m)^port\s+\d+`).ReplaceAll(b, []byte(fmt.Sprintf("port\t%d", port)))
	b = regexp.MustCompile(`(?m)^use_dns\s+\w+`).ReplaceAll(b, []byte("use_dns\tno"))
	if err := os.WriteFile(cnf, b, 0o644); err != nil {
		return nil, err
	}
	_ = os.MkdirAll(filepath.Join(dst, "log"), 0o755)
	cmd := exec.Command("./netmush", "mush.cnf")
	cmd.Dir = dst
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if f, err := os.Create(filepath.Join(dst, "log", "harness.out")); err == nil {
		cmd.Stdout, cmd.Stderr = f, f
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	stop := func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _, _ = cmd.Process.Wait() }
	if !waitPort(port, 20*time.Second) {
		stop()
		return nil, fmt.Errorf("the CobraMUSH copy did not start (see %s/log)", dst)
	}
	return &gameServer{name: "cobra", port: port, stop: stop}, nil
}

// startEvennia uses a running Evennia on port, or starts the one in dir
// (a directory holding venv/ and testgame/). GMCP is on for it.
func startEvennia(dir string, port int) (*gameServer, error) {
	if portOpen(port) {
		return &gameServer{name: "evennia", port: port, stop: func() {}}, nil
	}
	game := filepath.Join(dir, "testgame")
	bin := filepath.Join(dir, "venv", "bin")
	if _, err := os.Stat(filepath.Join(bin, "evennia")); err != nil {
		return nil, fmt.Errorf("no Evennia in %s", dir)
	}
	run := func(arg string) error {
		cmd := exec.Command(filepath.Join(bin, "evennia"), arg)
		cmd.Dir = game
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		return cmd.Run()
	}
	if err := run("start"); err != nil {
		return nil, fmt.Errorf("evennia start: %w", err)
	}
	if !waitPort(port, 40*time.Second) {
		return nil, fmt.Errorf("the Evennia test game did not listen on %d", port)
	}
	return &gameServer{name: "evennia", port: port, stop: func() { _ = run("stop") }}, nil
}

// ---------------------------------------------------------------- TinyFugue

type tfProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *syncBuf
}

func startTF(bin, lib, home string, env ...string) (*tfProc, error) {
	cmd := exec.Command(bin, "-n", "-L"+lib)
	cmd.Env = append(os.Environ(), "HOME="+home, "TERM=dumb")
	cmd.Env = append(cmd.Env, env...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out := &syncBuf{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &tfProc{cmd: cmd, stdin: in, out: out}, nil
}

// typ types one line at TinyFugue's keyboard.
func (t *tfProc) typ(line string) {
	fmt.Fprintln(t.stdin, line)
	time.Sleep(150 * time.Millisecond)
}

func (t *tfProc) stop() {
	_ = t.stdin.Close()
	_ = t.cmd.Process.Kill()
	_ = t.cmd.Wait()
}

var codeRE = regexp.MustCompile(`pairing code\s+([A-Z0-9]{4}-[A-Z0-9]{4})`)

// pairCode asks TinyFugue for a one-time code, exactly as a user types /squat pair.
func (t *tfProc) pairCode(role string) (string, error) {
	n := len(codeRE.FindAllString(t.out.String(), -1))
	t.typ("/squat pair " + role)
	end := time.Now().Add(8 * time.Second)
	for time.Now().Before(end) {
		if m := codeRE.FindAllStringSubmatch(t.out.String(), -1); len(m) > n {
			return m[len(m)-1][1], nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("TinyFugue printed no pairing code")
}

// ---------------------------------------------------------------- remote client

// remote is a TFSP client plus a recording of every event it received.
type remote struct {
	name string
	c    *wire.Client
	mu   sync.Mutex
	cond *sync.Cond
	evs  []wire.Event
	pos  int // events before pos are "already seen" for the wait helpers
	ses  map[string]proto.SessionInfo
}

func dial(addr, code, name string, key ed25519.PrivateKey) (*remote, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := wire.Dial(ctx, "ws://"+addr, wire.ClientOptions{Key: key, PairCode: code, DeviceName: name, ClientName: "tfharness",
		Caps: proto.DefaultCaps(), InsecureLoopback: true, ExpectKind: proto.KindTFSquatterd})
	if err != nil {
		return nil, err
	}
	r := &remote{name: name, c: c, ses: map[string]proto.SessionInfo{}}
	for _, s := range c.Sessions() {
		r.ses[s.ID] = s
	}
	r.cond = sync.NewCond(&r.mu)
	go func() {
		for ev := range c.Events() {
			r.mu.Lock()
			switch x := ev.(type) {
			case wire.SessionState:
				r.ses[x.Info.ID] = x.Info
			case wire.Removed:
				delete(r.ses, x.Session)
			}
			r.evs = append(r.evs, ev)
			r.cond.Broadcast()
			r.mu.Unlock()
		}
	}()
	return r, nil
}

func newKey() ed25519.PrivateKey { k, _ := auth.GenerateKey(); return k }

// mark makes the wait helpers ignore everything received so far.
func (r *remote) mark() { r.mu.Lock(); r.pos = len(r.evs); r.mu.Unlock() }

// waitEvent returns the first event after the mark that pred accepts.
func (r *remote) waitEvent(d time.Duration, pred func(wire.Event) bool) (wire.Event, bool) {
	end := time.Now().Add(d)
	r.mu.Lock()
	defer r.mu.Unlock()
	i := r.pos
	for {
		for ; i < len(r.evs); i++ {
			if pred(r.evs[i]) {
				return r.evs[i], true
			}
		}
		if time.Now().After(end) {
			return nil, false
		}
		go func() { time.Sleep(60 * time.Millisecond); r.cond.Broadcast() }()
		r.cond.Wait()
	}
}

func lineText(l proto.Line) string { return ansi.PlainText(l.Spans) }

// entriesOf flattens live lines and replayed backlog entries.
func entriesOf(ev wire.Event) []wire.Entry {
	switch e := ev.(type) {
	case wire.Line:
		return []wire.Entry{{Line: &e.Line}}
	case wire.Gmcp:
		return []wire.Entry{{Gmcp: &e.GmcpMsg}}
	case wire.Msdp:
		return []wire.Entry{{Msdp: &e.MsdpMsg}}
	case wire.Backlog:
		return e.Entries
	}
	return nil
}

// waitLine waits for a complete GAME line of the session whose text contains
// sub. Neither the echo of our own typing nor a prompt (a partial line reported
// after 150 ms of silence, which may be superseded by the finished line) counts.
func (r *remote) waitLine(session, sub string, d time.Duration) (proto.Line, bool) {
	var found proto.Line
	_, ok := r.waitEvent(d, func(ev wire.Event) bool {
		for _, e := range entriesOf(ev) {
			if e.Line != nil && e.Line.Kind == proto.KindGame && !e.Line.Prompt && (session == "" || e.Line.Session == session) && strings.Contains(lineText(*e.Line), sub) {
				found = *e.Line
				return true
			}
		}
		return false
	})
	return found, ok
}

// waitGmcp waits for a GMCP message with the given package.
func (r *remote) waitGmcp(session, pkg string, d time.Duration) (proto.GmcpMsg, bool) {
	var found proto.GmcpMsg
	_, ok := r.waitEvent(d, func(ev wire.Event) bool {
		for _, e := range entriesOf(ev) {
			if e.Gmcp != nil && (session == "" || e.Gmcp.Session == session) && e.Gmcp.Package == pkg {
				found = *e.Gmcp
				return true
			}
		}
		return false
	})
	return found, ok
}

// waitMsdp waits for an MSDP message of the session whose variables satisfy pred.
func (r *remote) waitMsdp(session string, pred func(vars map[string]any) bool, d time.Duration) (map[string]any, bool) {
	var found map[string]any
	_, ok := r.waitEvent(d, func(ev wire.Event) bool {
		for _, e := range entriesOf(ev) {
			if e.Msdp != nil && (session == "" || e.Msdp.Session == session) && pred(e.Msdp.Vars) {
				found = e.Msdp.Vars
				return true
			}
		}
		return false
	})
	return found, ok
}

func (r *remote) close() { _ = r.c.Close() }

func putReq(name, host, port string) wire.WorldPutReq {
	return wire.WorldPutReq{World: proto.TFWorld{Name: name, Host: host, Port: port}}
}

func jsonOrString(s string) any {
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		return v
	}
	return s
}
