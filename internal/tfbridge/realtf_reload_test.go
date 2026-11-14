//go:build !windows

package tfbridge

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/daemon"
)

// TestRealTinyFugueReload restarts the REAL bridge under a REAL TinyFugue (the squatterbridge branch):
// the game connection never drops, the control channel keeps working, a killed bridge is replaced by one
// that knocks, and a wrong token is refused.
//
//	TF_BIN=/path/to/tf TF_LIB=/path/to/lib/tf go test -run RealTinyFugueReload ./internal/tfbridge/
func TestRealTinyFugueReload(t *testing.T) {
	tfBin, tfLib := os.Getenv("TF_BIN"), os.Getenv("TF_LIB")
	if tfBin == "" {
		t.Skip("TF_BIN not set")
	}
	bridgeBin := bridgeBinary(t)
	mud := newMockMUD(t)
	port := freePort(t)
	work := shortDir(t)
	home := filepath.Join(work, "home")
	os.MkdirAll(home, 0o755)
	sq := filepath.Join(work, "sq")
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
	tfType("/set squat_dir=" + sq)
	tfType("/squat on")
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	waitListen(t, addr)
	t.Cleanup(func() {
		if info, err := daemon.ReloadInfo(sq); err == nil {
			syscall.Kill(info.PID, syscall.SIGKILL)
		}
	})
	waitOut := func(re string) {
		t.Helper()
		rx := regexp.MustCompile(re)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if rx.MatchString(out.String()) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("TF output never matched %q:\n%s", re, out.String())
	}

	// the rendezvous exists and is private
	for _, f := range []string{"ctl.token", "tf.sock"} {
		fi, err := os.Stat(filepath.Join(sq, f))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", f, fi, err)
		}
	}

	tfType("/squat pair")
	var code string
	for i := 0; i < 50 && code == ""; i++ {
		if m := regexp.MustCompile(`pairing code\s+([A-Z0-9]{4}-[A-Z0-9]{4})`).FindStringSubmatch(out.String()); m != nil {
			code = m[1]
		}
		time.Sleep(100 * time.Millisecond)
	}
	h := &harness{t: t, addr: addr}
	c := h.dialWith(code, nil)
	c.ws.SetReadLimit(8 << 20)
	priv := c.priv
	tfType(fmt.Sprintf("/connect 127.0.0.1 %d", mud.port))
	st := c.until(func(m map[string]any) bool {
		return m["t"] == "session.state" && m["session"].(map[string]any)["state"] == "connected"
	})
	sid := st["session"].(map[string]any)["id"].(string)
	mud.waitConns(t, 1)
	time.Sleep(400 * time.Millisecond)
	c.write(map[string]any{"v": 1, "t": "attach", "session": sid})
	c.untilType("backlog")
	mud.say(0, "before reload 1\r\n")
	c.until(func(m map[string]any) bool {
		return m["t"] == "line" && strings.Contains(fmt.Sprint(m["spans"]), "before reload 1")
	})

	reload := func() string {
		var o strings.Builder
		rc := exec.Command(bridgeBin, "reload", "--data-dir", sq, "--port", fmt.Sprint(port), "--bind", "127.0.0.1", "--insecure-loopback")
		rc.Stdout, rc.Stderr = &o, &o
		if err := rc.Run(); err != nil {
			t.Fatalf("tf-squatterd reload: %v\n%s\nTF:\n%s", err, o.String(), out.String())
		}
		return o.String()
	}

	// ---- 1. a reload with the real TinyFugue
	before, _ := daemon.ReloadInfo(sq)
	// traffic while it happens
	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for i := 2; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			mud.say(0, fmt.Sprintf("during reload n=%d\r\n", i))
			time.Sleep(2 * time.Millisecond)
		}
	}()
	o := reload()
	close(stop)
	<-finished
	if !strings.Contains(o, "reloaded") {
		t.Fatalf("reload output: %s", o)
	}
	after, err := daemon.ReloadInfo(sq)
	if err != nil || after.PID == before.PID || after.Generation != before.Generation+1 {
		t.Fatalf("status after: %+v err %v (before %+v)", after, err, before)
	}
	waitOut(`squat: a bridge \(pid \d+\) attached`)
	if !c.closed(3 * time.Second) {
		t.Fatal("client not dropped by the reload")
	}
	// the game connection is the same one: the MUD still has exactly one, and its text still arrives
	if n := func() int { mud.mu.Lock(); defer mud.mu.Unlock(); return len(mud.conns) }(); n != 1 {
		t.Fatalf("the MUD saw %d connections", n)
	}
	mud.say(0, "after reload\r\n")
	c2 := h.dialWith("", priv)
	c2.ws.SetReadLimit(8 << 20)
	c2.write(map[string]any{"v": 1, "t": "attach", "session": sid, "maxBacklog": 100000})
	c2.untilType("attached")
	var all strings.Builder
	for {
		bl := c2.untilType("backlog")
		all.WriteString(fmt.Sprint(bl["entries"]))
		if bl["done"] == true {
			break
		}
	}
	c2.until(func(m map[string]any) bool {
		all.WriteString(fmt.Sprint(m))
		return strings.Contains(all.String(), "after reload")
	})
	if !strings.Contains(all.String(), "before reload 1") {
		t.Fatalf("history lost across the reload: %s", all.String())
	}
	// numbered lines during the reload: every one that TinyFugue read arrived once, in order
	nums := nRe.FindAllStringSubmatch(all.String(), -1)
	if len(nums) < 3 {
		t.Fatalf("expected numbered lines sent during the reload, got %d", len(nums))
	}
	last := nums0(nums)
	for _, m := range nums[1:] {
		var n int
		fmt.Sscan(m[1], &n)
		if n != last+1 {
			t.Fatalf("numbered lines jump from %d to %d across the reload", last, n)
		}
		last = n
	}
	// control works through the new bridge: take over and send
	c2.write(map[string]any{"v": 1, "t": "control.set", "id": "c1", "session": sid, "preset": "takeover"})
	c2.untilType("control.state")
	c2.write(map[string]any{"v": 1, "t": "send", "session": sid, "text": "hello after reload"})
	mud.waitFor(t, "hello after reload\r\n")

	// ---- 2. a wrong token is refused and changes nothing
	bad := filepath.Join(work, "bad")
	os.MkdirAll(bad, 0o700)
	os.WriteFile(filepath.Join(bad, TokenFile), []byte(strings.Repeat("a", 64)+"\n"+filepath.Join(sq, "tf.sock")+"\n"), 0o600)
	if _, err := Knock(bad, 2*time.Second); err == nil {
		t.Fatal("TinyFugue accepted a wrong token")
	}
	waitOut(`rejected a knock`)
	mud.say(0, "still fine\r\n")
	c2.until(func(m map[string]any) bool {
		return m["t"] == "line" && strings.Contains(fmt.Sprint(m["spans"]), "still fine")
	})

	// ---- 3. the bridge is killed: TinyFugue keeps the connection and waits; a new bridge knocks
	info, _ := daemon.ReloadInfo(sq)
	syscall.Kill(info.PID, syscall.SIGKILL)
	waitOut(`no bridge attached; waiting for one`)
	mud.say(0, "while detached\r\n")
	mud.waitFor(t, "hello after reload") // the game connection lives on
	tfType("/echo TF still works")
	waitOut(`TF still works`)
	o = reload() // none running: starts one that knocks
	if !strings.Contains(o, "started one") {
		t.Fatalf("reload output: %s", o)
	}
	waitListen(t, addr)
	c3 := h.dialWith("", priv)
	c3.ws.SetReadLimit(8 << 20)
	sessions := c3.welcome["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions after the knock = %v", sessions)
	}
	c3.write(map[string]any{"v": 1, "t": "attach", "session": sid, "maxBacklog": 100000})
	c3.untilType("attached")
	all.Reset()
	for {
		bl := c3.untilType("backlog")
		all.WriteString(fmt.Sprint(bl["entries"]))
		if bl["done"] == true {
			break
		}
	}
	if !strings.Contains(all.String(), "after reload") {
		t.Fatalf("TinyFugue's history was not replayed to the new bridge: %s", all.String())
	}
	mud.say(0, "after the knock\r\n")
	c3.until(func(m map[string]any) bool {
		return m["t"] == "line" && strings.Contains(fmt.Sprint(m["spans"]), "after the knock")
	})

	// ---- 4. /squat off removes the rendezvous
	tfType("/squat off")
	time.Sleep(600 * time.Millisecond)
	for _, f := range []string{"ctl.token", "tf.sock"} {
		if _, err := os.Stat(filepath.Join(sq, f)); err == nil {
			t.Fatalf("%s still exists after /squat off", f)
		}
	}
	if c4, err := net.Dial("tcp", addr); err == nil {
		c4.Close()
		t.Fatal("bridge still listening after /squat off")
	}
}

func nums0(m [][]string) int {
	var n int
	fmt.Sscan(m[0][1], &n)
	return n
}
