package tfbridge

import (
	"bytes"
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
	"testing"
	"time"
)

// TestRealTinyFugue drives a REAL TinyFugue (built from the squatterbridge
// branch) with the REAL tf-squatterd binary, a mock MUD and a TFSP client.
//
//	TF_BIN=/path/to/tf TF_LIB=/path/to/lib/tf go test -run RealTinyFugue ./internal/tfbridge/
//
// It is skipped unless TF_BIN is set.
func TestRealTinyFugue(t *testing.T) {
	tfBin, tfLib := os.Getenv("TF_BIN"), os.Getenv("TF_LIB")
	if tfBin == "" {
		t.Skip("TF_BIN not set")
	}
	work := t.TempDir()

	// build the bridge
	bridgeBin := filepath.Join(work, "tf-squatterd")
	if out, err := exec.Command("go", "build", "-o", bridgeBin, "../../cmd/tf-squatterd").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// mock MUD
	mud := newMockMUD(t)

	// free port for the bridge
	pl, _ := net.Listen("tcp", "127.0.0.1:0")
	port := pl.Addr().(*net.TCPAddr).Port
	pl.Close()

	home := filepath.Join(work, "home")
	os.MkdirAll(home, 0o755)
	cmd := exec.Command(tfBin, "-n", "-L"+tfLib)
	cmd.Env = append(os.Environ(), "HOME="+home, "TERM=dumb")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	out := &syncBuf{}
	go io.Copy(out, stdout)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); cmd.Process.Kill(); cmd.Wait() })
	tfType := func(line string) { fmt.Fprintln(stdin, line); time.Sleep(150 * time.Millisecond) }

	tfType("/set squat_bin=" + bridgeBin)
	tfType(fmt.Sprintf("/set squat_port=%d", port))
	tfType("/set squat_plain=1")
	tfType("/set squat_dir=" + filepath.Join(work, "sq"))
	tfType("/squat on")
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("bridge never listened on %s\nTF output:\n%s", addr, out.String())
		}
		time.Sleep(100 * time.Millisecond)
	}

	// pair from TF, exactly as a user would
	tfType("/squat pair")
	var code string
	for i := 0; i < 50 && code == ""; i++ {
		if m := regexp.MustCompile(`pairing code\s+([A-Z0-9]{4}-[A-Z0-9]{4})`).FindStringSubmatch(out.String()); m != nil {
			code = m[1]
		}
		time.Sleep(100 * time.Millisecond)
	}
	if code == "" {
		t.Fatalf("TF never printed a pairing code\n%s", out.String())
	}

	h := &harness{t: t, addr: addr}
	c := h.dialWith(code, nil)
	if c.welcome["kind"] != "tf-squatterd" || c.welcome["role"] != "operator" {
		t.Fatalf("welcome = %v", c.welcome)
	}

	// a connection made in TF appears at the client
	tfType(fmt.Sprintf("/connect 127.0.0.1 %d", mud.port))
	st := c.until(func(m map[string]any) bool {
		return m["t"] == "session.state" && m["session"].(map[string]any)["state"] == "connected"
	})
	si := st["session"].(map[string]any)
	sid := si["id"].(string)
	if si["tf"].(map[string]any)["input"] != "tf" {
		t.Fatalf("session = %v", si)
	}
	time.Sleep(400 * time.Millisecond)

	// attach: the MUD's welcome text and its ANSI colour arrive, raw
	c.write(map[string]any{"v": 1, "t": "attach", "session": sid})
	c.untilType("attached")
	bl := c.untilType("backlog")
	raw, _ := json.Marshal(bl)
	if !strings.Contains(string(raw), "Welcome to the mock MUD") || !strings.Contains(string(raw), `"fg":1`) {
		t.Fatalf("backlog missing MUD text or colour: %s", raw)
	}
	if !strings.Contains(string(raw), "Password:") || !strings.Contains(string(raw), `"prompt":true`) {
		t.Fatalf("the unterminated 'Password: ' line was not reported as a prompt: %s", raw)
	}

	// 1. TF controls by default: a remote send is refused
	c.write(map[string]any{"v": 1, "t": "send", "id": "s0", "session": sid, "text": "look"})
	if e := c.untilType("error"); e["code"] != "not_controller" {
		t.Fatalf("expected not_controller, got %v", e)
	}
	// 2. take over; send reaches the MUD, echo carries the device as origin
	c.write(map[string]any{"v": 1, "t": "control.set", "id": "c1", "session": sid, "preset": "takeover"})
	if cs := c.untilType("control.state"); cs["input"] != "remote" {
		t.Fatalf("control.state = %v", cs)
	}
	c.write(map[string]any{"v": 1, "t": "send", "id": "s1", "session": sid, "text": "look"})
	mud.waitFor(t, "look\r\n")
	echo := c.until(func(m map[string]any) bool { return m["t"] == "line" && m["kind"] == "echo" })
	if echo["origin"] == "" || echo["origin"] == "tf" {
		t.Fatalf("echo origin = %v", echo["origin"])
	}
	// 3. text that looks like a TinyFugue command goes to the MUD, never to TF's parser
	c.write(map[string]any{"v": 1, "t": "send", "session": sid, "text": "/echo INJECTED-BY-REMOTE"})
	mud.waitFor(t, "/echo INJECTED-BY-REMOTE\r\n")
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(out.String(), "INJECTED-BY-REMOTE\n") && regexp.MustCompile(`(?m)^\s*INJECTED-BY-REMOTE\s*$`).MatchString(out.String()) {
		t.Fatalf("TF executed remote text as a command:\n%s", out.String())
	}
	// 4. secrets: reach the MUD, never appear in TF's output or in a scrollback frame
	c.write(map[string]any{"v": 1, "t": "send", "session": sid, "text": "s3cr3t-pass", "noEcho": true})
	mud.waitFor(t, "s3cr3t-pass\r\n")
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(out.String(), "s3cr3t-pass") {
		t.Fatalf("secret appeared on TF's screen")
	}
	// 5. the TF keyboard is locked while the remote controls the session
	before := mud.received()
	tfType("typed at the TF keyboard")
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(mud.received()[len(before):], "typed at the TF keyboard") {
		t.Fatalf("TF keyboard was not locked")
	}
	if !strings.Contains(out.String(), "controlled by a remote client") {
		t.Fatalf("TF did not explain the lock:\n%s", out.String())
	}
	// 6. world list: password never crosses the wire
	tfType("/addworld -Ttiny testw bob hunter2pw 127.0.0.1 1")
	c.write(map[string]any{"v": 1, "t": "world.list", "id": "w1"})
	wl, _ := json.Marshal(c.untilType("world.list"))
	if !strings.Contains(string(wl), `"name":"testw"`) || !strings.Contains(string(wl), `"passSet":true`) || strings.Contains(string(wl), "hunter2pw") {
		t.Fatalf("world.list = %s", wl)
	}
	// 7. connect by reference: a world record that works creates a second session...
	tfType(fmt.Sprintf("/addworld -Ttiny mudw 127.0.0.1 %d", mud.port))
	c.write(map[string]any{"v": 1, "t": "session.connect", "id": "n1", "world": map[string]any{"ref": "mudw"}})
	cr := c.until(func(m map[string]any) bool { return m["t"] == "session.created" || m["t"] == "error" })
	if cr["t"] != "session.created" || cr["reqId"] != "n1" || cr["session"].(map[string]any)["tf"].(map[string]any)["world"] != "mudw" {
		t.Fatalf("connect by ref = %v", cr)
	}
	mud.waitConns(t, 2)
	// ...and one that cannot connect is reported as an error, not a phantom session
	c.write(map[string]any{"v": 1, "t": "session.connect", "id": "n2", "world": map[string]any{"ref": "testw"}})
	if e := c.until(func(m map[string]any) bool { return m["t"] == "session.created" || m["t"] == "error" }); e["t"] != "error" || e["code"] != "connect_failed" {
		t.Fatalf("connect to a refusing port = %v", e)
	}
	// 7b. automation: TF runs triggers by default; a client that takes automation stops them
	tfType(`/def -t"*TRIGME" trigtest = /send trigger-fired`)
	mud.say(0, "TRIGME\r\n")
	mud.waitFor(t, "trigger-fired\r\n")
	c.write(map[string]any{"v": 1, "t": "control.set", "session": sid, "axis": "automation", "mode": "client"})
	if cs := c.until(func(m map[string]any) bool { return m["t"] == "control.state" && m["automation"] == "client" }); cs["session"] != sid {
		t.Fatalf("control.state = %v", cs)
	}
	time.Sleep(200 * time.Millisecond)
	mud.say(0, "TRIGME\r\n")
	time.Sleep(500 * time.Millisecond)
	if n := strings.Count(mud.received(), "trigger-fired\r\n"); n != 1 {
		t.Fatalf("TF ran its trigger while a client owns automation (fired %d times)", n)
	}
	// 8. the client goes away: TF takes control back by itself
	c.ws.Close(1000, "bye")
	time.Sleep(800 * time.Millisecond)
	tfType("back in control")
	mud.waitFor(t, "back in control\r\n")
	// automation went back to TF too, so the trigger fires again
	mud.say(0, "TRIGME\r\n")
	deadline = time.Now().Add(5 * time.Second)
	for strings.Count(mud.received(), "trigger-fired\r\n") < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := strings.Count(mud.received(), "trigger-fired\r\n"); n != 2 {
		t.Fatalf("automation was not handed back to TF after the client left (fired %d times)", n)
	}
	// 9. /squat off stops the bridge cleanly
	tfType("/squat off")
	time.Sleep(500 * time.Millisecond)
	if c2, err := net.Dial("tcp", addr); err == nil {
		c2.Close()
		t.Fatalf("bridge still listening after /squat off")
	}
}

// ---------------------------------------------------------------- helpers

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type mockMUD struct {
	ln    net.Listener
	port  int
	mu    sync.Mutex
	buf   strings.Builder
	conns []net.Conn
}

// say writes text to the i-th connection.
func (m *mockMUD) say(i int, text string) {
	m.mu.Lock()
	c := m.conns[i]
	m.mu.Unlock()
	c.Write([]byte(text))
}

func (m *mockMUD) waitConns(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		got := len(m.conns)
		m.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the MUD never got %d connections", n)
}

func newMockMUD(t *testing.T) *mockMUD {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m := &mockMUD{ln: ln, port: ln.Addr().(*net.TCPAddr).Port}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			m.mu.Lock()
			m.conns = append(m.conns, c)
			m.mu.Unlock()
			go func() {
				defer c.Close()
				c.Write([]byte("Welcome to the mock MUD\r\n\x1b[31mred text\x1b[0m\r\nPassword: "))
				b := make([]byte, 4096)
				for {
					n, err := c.Read(b)
					if n > 0 {
						m.mu.Lock()
						m.buf.Write(b[:n])
						m.mu.Unlock()
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return m
}

func (m *mockMUD) received() string { m.mu.Lock(); defer m.mu.Unlock(); return m.buf.String() }

func (m *mockMUD) waitFor(t *testing.T, s string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(m.received(), s) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the MUD never received %q; got %q", s, m.received())
}
