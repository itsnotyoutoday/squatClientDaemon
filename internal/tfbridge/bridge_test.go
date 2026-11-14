package tfbridge

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/msdp"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tlsutil"
)

// ---------------------------------------------------------------- fake TinyFugue

// fakeTF is the TinyFugue end of the control channel. It records what the
// bridge sends and answers requests the way squat.c does.
type fakeTF struct {
	t    *testing.T
	conn net.Conn
	wmu  sync.Mutex

	mu    sync.Mutex
	got   [][]string
	cond  *sync.Cond
	reply func(f *fakeTF, m []string) // optional custom responder
}

func (f *fakeTF) send(fields ...string) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	if _, err := f.conn.Write(Encode(fields...)); err != nil {
		f.t.Logf("fakeTF write: %v", err)
	}
}

func (f *fakeTF) readLoop() {
	sc := bufio.NewScanner(f.conn)
	sc.Buffer(make([]byte, 0, 64<<10), MaxCtlLine)
	for sc.Scan() {
		m := Decode(sc.Text())
		f.mu.Lock()
		f.got = append(f.got, m)
		f.cond.Broadcast()
		f.mu.Unlock()
		f.mu.Lock()
		reply := f.reply
		f.mu.Unlock()
		if reply != nil {
			reply(f, m)
		}
	}
}

// wait returns the first received message of the given type at or after index
// from (and the index after it), or fails the test.
func (f *fakeTF) wait(typ string, from int) ([]string, int) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	f.mu.Lock()
	defer f.mu.Unlock()
	for {
		for i := from; i < len(f.got); i++ {
			if f.got[i][0] == typ {
				return f.got[i], i + 1
			}
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("timed out waiting for %s from the bridge; got %q", typ, f.got)
		}
		go func() { time.Sleep(50 * time.Millisecond); f.cond.Broadcast() }()
		f.cond.Wait()
	}
}

// setReply replaces the responder while the reader goroutine is running.
func (f *fakeTF) setReply(fn func(f *fakeTF, m []string)) {
	f.mu.Lock()
	f.reply = fn
	f.mu.Unlock()
}

func (f *fakeTF) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.got) }

// defaultReply mimics squat.c: OK for everything, ERR not_controller for SEND
// while the session's input is "tf".
func defaultReply(input *string, mu *sync.Mutex) func(f *fakeTF, m []string) {
	return func(f *fakeTF, m []string) {
		switch m[0] {
		case "SEND":
			mu.Lock()
			in := *input
			mu.Unlock()
			if in == "tf" {
				f.send("ERR", m[1], "not_controller", "TinyFugue has control of this session")
				return
			}
			f.send("OK", m[1])
		case "CTRL_SET":
			mu.Lock()
			if m[3] == "input" {
				*input = m[4]
			}
			cur := *input
			mu.Unlock()
			f.send("OK", m[1])
			f.send("CTRL", m[2], cur, "tf", m[5])
		case "CONNECT":
			f.send("SESS", "w-new-1-9", "newworld", "127.0.0.1", "4000", "0", "connecting", "0", "0", "1700000000000", "")
			f.send("OK", m[1], "w-new-1-9")
		case "WORLDS":
			f.send("WORLD", "alpha", "alpha.example", "4201", "tiny", "bob", "1", "1", "0", "UTF-8", "/home/u/alpha.tf", "0")
			f.send("WORLD", "beta", "beta.example", "4202", "", "", "0", "0", "0", "", "", "0")
			f.send("OK", m[1])
		case "WORLD_PUT":
			f.send("OK", m[1], "created")
		case "WORLD_DEL", "GMCP_SEND", "MSDP_SEND":
			f.send("OK", m[1])
		case "EXEC":
			if strings.HasPrefix(m[2], "/forbidden") {
				f.send("ERR", m[1], "forbidden", "remote commands are off")
				return
			}
			f.send("OK", m[1], "1", "line one", "line\ttwo")
		case "DISC", "DISMISS", "RESIZE":
			if m[0] != "RESIZE" {
				f.send("OK", m[1])
			}
		}
	}
}

// ---------------------------------------------------------------- harness

type harness struct {
	t      *testing.T
	tf     *fakeTF
	addr   string
	dir    string
	as     *auth.Store
	cancel context.CancelFunc
	done   chan error
	input  string
	imu    sync.Mutex
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	a, b := socketPair(t)
	h := &harness{t: t, dir: dir, input: "tf", done: make(chan error, 1)}
	f := &fakeTF{t: t, conn: a}
	f.cond = sync.NewCond(&f.mu)
	f.reply = defaultReply(&h.input, &h.imu)
	h.tf = f
	go f.readLoop()

	as, err := auth.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h.as = as
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	ready := make(chan string, 1)
	go func() {
		h.done <- Serve(ctx, ServeOptions{Conn: b, DataDir: dir, Bind: "127.0.0.1", Port: 0, InsecureLoopback: true,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test", Ready: func(a string) { ready <- a }})
	}()
	// TinyFugue speaks first.
	f.send("HELLO", "1", "tf", "inst-abc", "4242", "lines,control,worlds")
	select {
	case h.addr = <-ready:
	case err := <-h.done:
		t.Fatalf("Serve returned early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("bridge never became ready")
	}
	t.Cleanup(func() { cancel(); a.Close() })
	return h
}

func socketPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ch := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); ch <- c }()
	a, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return a, <-ch
}

// pairCode asks the bridge for a pairing code the way "/squat pair" does.
func (h *harness) pairCode(role string) string {
	h.t.Helper()
	from := h.tf.count()
	h.tf.send("PAIR", role)
	m, _ := h.tf.wait("PAIRCODE", from)
	return m[1]
}

type wsClient struct {
	t       *testing.T
	ws      *websocket.Conn
	welcome map[string]any
	priv    ed25519.PrivateKey
}

func (h *harness) dial(role string) *wsClient {
	h.t.Helper()
	code := h.pairCode(role)
	c := h.dialWith(code, nil)
	return c
}

func (h *harness) dialWith(code string, priv ed25519.PrivateKey) *wsClient {
	h.t.Helper()
	if priv == nil {
		var err error
		if priv, err = auth.GenerateKey(); err != nil {
			h.t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws://"+h.addr+"/tfsp", nil)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { ws.CloseNow() })
	c := &wsClient{t: h.t, ws: ws, priv: priv}
	ch := c.read(5 * time.Second)
	if ch["t"] != "challenge" || ch["kind"] != "tf-squatterd" {
		h.t.Fatalf("challenge = %v", ch)
	}
	sig, err := auth.SignNonce(priv, ch["nonce"].(string))
	if err != nil {
		h.t.Fatal(err)
	}
	c.write(map[string]any{"v": 1, "t": "hello", "client": "test", "deviceName": "testdev", "publicKey": auth.PublicKeyB64(priv),
		"sig": sig, "pairCode": code})
	c.welcome = c.read(5 * time.Second)
	if c.welcome["t"] != "welcome" {
		h.t.Fatalf("welcome = %v", c.welcome)
	}
	return c
}

func (c *wsClient) write(m map[string]any) {
	c.t.Helper()
	b, _ := json.Marshal(m)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, b); err != nil {
		c.t.Fatalf("ws write: %v", err)
	}
}

func (c *wsClient) read(d time.Duration) map[string]any {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_, data, err := c.ws.Read(ctx)
	if err != nil {
		c.t.Fatalf("ws read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		c.t.Fatalf("bad json %q", data)
	}
	return m
}

// until reads frames until pred matches (ping frames are skipped).
func (c *wsClient) until(pred func(map[string]any) bool) map[string]any {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m := c.read(5 * time.Second)
		if m["t"] == "ping" {
			continue
		}
		if pred(m) {
			return m
		}
	}
	c.t.Fatal("timed out waiting for a matching frame")
	return nil
}

func (c *wsClient) untilType(typ string) map[string]any {
	c.t.Helper()
	return c.until(func(m map[string]any) bool { return m["t"] == typ })
}

// ---------------------------------------------------------------- tests

const sid = "w-alpha-1-1"

func announce(h *harness) {
	h.tf.send("SESS", sid, "alpha", "alpha.example", "4201", "0", "connected", "0", "1", "1700000000000", "")
	h.tf.send("CTRL", sid, "tf", "tf", "")
}

func TestPairingAndWelcome(t *testing.T) {
	h := newHarness(t)
	c := h.dial("operator")
	if c.welcome["kind"] != "tf-squatterd" || c.welcome["instance"] != "inst-abc" || c.welcome["role"] != "operator" {
		t.Fatalf("welcome = %v", c.welcome)
	}
	feats := map[string]bool{}
	for _, f := range c.welcome["features"].([]any) {
		feats[f.(string)] = true
	}
	for _, want := range []string{"tf", "control", "worlds", "noEcho"} {
		if !feats[want] {
			t.Errorf("missing feature %q in %v", want, feats)
		}
	}
	// the pairing code is single use
	code := h.pairCode("operator")
	h.dialWith(code, nil)
}

func TestWrongKindClientCannotReuseCode(t *testing.T) {
	h := newHarness(t)
	code := h.pairCode("operator")
	h.dialWith(code, nil)
	// second device with the SAME (now consumed) code must fail
	priv, _ := auth.GenerateKey()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws://"+h.addr+"/tfsp", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	c := &wsClient{t: t, ws: ws}
	ch := c.read(5 * time.Second)
	sig, _ := auth.SignNonce(priv, ch["nonce"].(string))
	c.write(map[string]any{"v": 1, "t": "hello", "publicKey": auth.PublicKeyB64(priv), "sig": sig, "pairCode": code})
	if m := c.read(5 * time.Second); m["t"] != "error" || m["code"] != "auth_failed" {
		t.Fatalf("expected auth_failed, got %v", m)
	}
}

func TestSessionLinesAttachAndReplay(t *testing.T) {
	h := newHarness(t)
	announce(h)
	h.tf.send("LINE", sid, "g", "r", "game", "1700000000001", "history line")
	h.tf.send("LINE", sid, "g", "", "game", "1700000000002", "\x1b[31mred\x1b[0m text")
	h.tf.send("LINE", sid, "g", "g", "game", "1700000000003", "gagged line")
	h.tf.send("LINE", sid, "g", "p", "game", "1700000000004", "Password: ")
	time.Sleep(promptHold + 300*time.Millisecond) // a prompt is held briefly (see TestPromptThatBecomesALineIsNotShownTwice)

	c := h.dial("operator")
	sessions := c.welcome["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %v", sessions)
	}
	si := sessions[0].(map[string]any)
	tf := si["tf"].(map[string]any)
	if si["id"] != sid || si["state"] != "connected" || tf["input"] != "tf" || tf["world"] != "alpha" || si["lastSeq"].(float64) != 4 {
		t.Fatalf("session info = %v", si)
	}
	c.write(map[string]any{"v": 1, "t": "attach", "id": "a1", "session": sid})
	c.untilType("attached")
	bl := c.untilType("backlog")
	if !bl["done"].(bool) {
		t.Fatalf("backlog not done: %v", bl)
	}
	entries := bl["entries"].([]any)
	if len(entries) != 4 {
		t.Fatalf("entries = %v", entries)
	}
	red := entries[1].(map[string]any)["spans"].([]any)
	if red[0].(map[string]any)["s"] != "red" || red[0].(map[string]any)["fg"].(float64) != 1 {
		t.Errorf("ANSI not parsed into spans: %v", red)
	}
	if entries[2].(map[string]any)["gag"] != true {
		t.Errorf("gag flag missing: %v", entries[2])
	}
	if entries[3].(map[string]any)["prompt"] != true {
		t.Errorf("prompt flag missing: %v", entries[3])
	}
	// live line arrives after the backlog, in order
	h.tf.send("LINE", sid, "g", "", "game", "1700000000005", "live line")
	live := c.untilType("line")
	if live["seq"].(float64) != 5 {
		t.Fatalf("live seq = %v", live["seq"])
	}
}

func TestSendRequiresControl(t *testing.T) {
	h := newHarness(t)
	announce(h)
	time.Sleep(100 * time.Millisecond)
	c := h.dial("operator")
	c.write(map[string]any{"v": 1, "t": "send", "id": "s1", "session": sid, "text": "look"})
	e := c.untilType("error")
	if e["code"] != "not_controller" {
		t.Fatalf("error = %v", e)
	}
	// takeover, then send works
	c.write(map[string]any{"v": 1, "t": "control.set", "id": "c1", "session": sid, "axis": "input", "mode": "remote"})
	st := c.untilType("control.state")
	if st["input"] != "remote" || st["device"] == "" {
		t.Fatalf("control.state = %v", st)
	}
	c.write(map[string]any{"v": 1, "t": "send", "id": "s2", "session": sid, "text": "look"})
	m, _ := h.tf.wait("SEND", 0)
	_ = m
	h.tf.mu.Lock()
	var sends [][]string
	for _, g := range h.tf.got {
		if g[0] == "SEND" {
			sends = append(sends, g)
		}
	}
	h.tf.mu.Unlock()
	last := sends[len(sends)-1]
	if last[2] != sid || last[4] != "0" || last[5] != "look" || last[3] == "" {
		t.Fatalf("SEND fields = %q", last)
	}
}

func TestViewerCannotSendOrControl(t *testing.T) {
	h := newHarness(t)
	announce(h)
	time.Sleep(100 * time.Millisecond)
	c := h.dial("viewer")
	if c.welcome["role"] != "viewer" {
		t.Fatalf("role = %v", c.welcome["role"])
	}
	c.write(map[string]any{"v": 1, "t": "send", "session": sid, "text": "look"})
	if e := c.untilType("error"); e["code"] != "forbidden" {
		t.Fatalf("send error = %v", e)
	}
	c.write(map[string]any{"v": 1, "t": "control.set", "session": sid, "axis": "input", "mode": "remote"})
	if e := c.untilType("error"); e["code"] != "forbidden" {
		t.Fatalf("control error = %v", e)
	}
	c.write(map[string]any{"v": 1, "t": "session.connect", "world": map[string]any{"ref": "alpha"}})
	if e := c.untilType("error"); e["code"] != "forbidden" {
		t.Fatalf("connect error = %v", e)
	}
	// viewers may still read: world.list works
	c.write(map[string]any{"v": 1, "t": "world.list", "id": "w1"})
	if wl := c.untilType("world.list"); len(wl["worlds"].([]any)) != 2 {
		t.Fatalf("world.list = %v", wl)
	}
}

func TestWorldListHidesPasswordAndMacroFile(t *testing.T) {
	h := newHarness(t)
	op := h.dial("operator")
	op.write(map[string]any{"v": 1, "t": "world.list", "id": "w1"})
	raw, _ := json.Marshal(op.untilType("world.list"))
	if strings.Contains(string(raw), "alpha.tf") {
		t.Errorf("operator saw the macro file: %s", raw)
	}
	var wl struct {
		Worlds []struct {
			Name    string `json:"name"`
			PassSet bool   `json:"passSet"`
		} `json:"worlds"`
	}
	json.Unmarshal(raw, &wl)
	if len(wl.Worlds) != 2 || wl.Worlds[0].Name != "alpha" || !wl.Worlds[0].PassSet || wl.Worlds[1].PassSet {
		t.Fatalf("worlds = %s", raw)
	}
	if strings.Contains(strings.ToLower(string(raw)), "\"pass\"") || strings.Contains(string(raw), "secret") {
		t.Errorf("a password field leaked: %s", raw)
	}
	ad := h.dial("admin")
	ad.write(map[string]any{"v": 1, "t": "world.list", "id": "w2"})
	raw, _ = json.Marshal(ad.untilType("world.list"))
	if !strings.Contains(string(raw), "alpha.tf") {
		t.Errorf("admin should see the macro file: %s", raw)
	}
}

func TestConnectByRefReturnsSession(t *testing.T) {
	h := newHarness(t)
	c := h.dial("operator")
	c.write(map[string]any{"v": 1, "t": "session.connect", "id": "n1", "world": map[string]any{"ref": "newworld"}})
	m := c.until(func(m map[string]any) bool { return m["t"] == "session.created" })
	s := m["session"].(map[string]any)
	if s["id"] != "w-new-1-9" || m["reqId"] != "n1" {
		t.Fatalf("session.created = %v", m)
	}
	cm, _ := h.tf.wait("CONNECT", 0)
	if cm[2] != "newworld" {
		t.Fatalf("CONNECT = %q", cm)
	}
}

func TestEchoMaskedWhileRemoteEcho(t *testing.T) {
	h := newHarness(t)
	announce(h)
	h.tf.send("SESS", sid, "alpha", "alpha.example", "4201", "0", "connected", "1", "1", "1700000000000", "") // remoteEcho on
	h.tf.send("LINE", sid, "e", "", "tf", "1700000000001", "hunter2")
	time.Sleep(200 * time.Millisecond)
	c := h.dial("operator")
	c.write(map[string]any{"v": 1, "t": "attach", "session": sid})
	c.untilType("attached")
	bl := c.untilType("backlog")
	raw, _ := json.Marshal(bl)
	if strings.Contains(string(raw), "hunter2") || !strings.Contains(string(raw), "********") {
		t.Fatalf("password echoed to clients: %s", raw)
	}
}

func TestDisconnectingControllerRevertsControl(t *testing.T) {
	h := newHarness(t)
	announce(h)
	time.Sleep(100 * time.Millisecond)
	c := h.dial("operator")
	c.write(map[string]any{"v": 1, "t": "control.set", "session": sid, "preset": "full"})
	c.untilType("control.state")
	from := h.tf.count()
	c.ws.Close(websocket.StatusNormalClosure, "bye")
	// the bridge must hand both axes back to TinyFugue
	sawInput, sawAuto := false, false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !(sawInput && sawAuto) {
		h.tf.mu.Lock()
		for _, g := range h.tf.got[from:] {
			if g[0] == "CTRL_SET" && g[4] == "tf" {
				sawInput = sawInput || g[3] == "input"
				sawAuto = sawAuto || g[3] == "automation"
			}
		}
		h.tf.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	if !sawInput || !sawAuto {
		t.Fatalf("control not reverted: input=%v automation=%v", sawInput, sawAuto)
	}
}

func TestSessionRemovedAndEnded(t *testing.T) {
	h := newHarness(t)
	announce(h)
	c := h.dial("operator")
	h.tf.send("SESS", sid, "alpha", "alpha.example", "4201", "0", "ended", "0", "1", "1700000000000", "remote_closed")
	st := c.until(func(m map[string]any) bool {
		return m["t"] == "session.state" && m["session"].(map[string]any)["state"] == "ended"
	})
	if st["session"].(map[string]any)["endReason"] != "remote_closed" {
		t.Fatalf("state = %v", st)
	}
	h.tf.send("SESSX", sid)
	if r := c.untilType("session.removed"); r["session"] != sid {
		t.Fatalf("removed = %v", r)
	}
}

func TestResizeOnlyFromController(t *testing.T) {
	h := newHarness(t)
	announce(h)
	time.Sleep(100 * time.Millisecond)
	c := h.dial("operator")
	c.write(map[string]any{"v": 1, "t": "resize", "session": sid, "cols": 100, "rows": 30})
	time.Sleep(200 * time.Millisecond)
	if n := countType(h.tf, "RESIZE"); n != 0 {
		t.Fatalf("resize forwarded for a non-controller (%d)", n)
	}
	c.write(map[string]any{"v": 1, "t": "control.set", "session": sid, "axis": "input", "mode": "remote"})
	c.untilType("control.state")
	c.write(map[string]any{"v": 1, "t": "resize", "session": sid, "cols": 100, "rows": 30})
	m, _ := h.tf.wait("RESIZE", 0)
	if m[1] != sid || m[2] != "100" || m[3] != "30" {
		t.Fatalf("RESIZE = %q", m)
	}
}

func countType(f *fakeTF, typ string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, g := range f.got {
		if g[0] == typ {
			n++
		}
	}
	return n
}

func TestUnknownMessagesIgnoredAndShutdownOnEOF(t *testing.T) {
	h := newHarness(t)
	h.tf.send("FROBNICATE", "x", "y") // forward compatibility: ignored
	h.tf.send("LINE", "no-such-session", "g", "", "game", "1", "text")
	time.Sleep(100 * time.Millisecond)
	h.tf.conn.Close() // TinyFugue exits
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("Serve = %v, want nil on a clean TinyFugue exit", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop when TinyFugue closed the channel")
	}
}

// The default transport is TLS: the pairing code TinyFugue prints carries the
// fingerprint of the certificate the server really presents, and wss works.
func TestServeTLSAndFingerprint(t *testing.T) {
	dir := t.TempDir()
	a, b := socketPair(t)
	f := &fakeTF{t: t, conn: a}
	f.cond = sync.NewCond(&f.mu)
	go f.readLoop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ServeOptions{Conn: b, DataDir: dir, Bind: "127.0.0.1", Port: 0,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test", Ready: func(a string) { ready <- a }})
	}()
	f.send("HELLO", "1", "tf", "inst-tls", "1", "")
	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("Serve: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("not ready")
	}
	f.send("PAIR", "operator")
	m, _ := f.wait("PAIRCODE", 0)
	code, fp, hint := m[1], m[3], m[4]
	if !strings.HasPrefix(hint, "wss://") || !strings.HasSuffix(hint, "/tfsp") || fp == "" {
		t.Fatalf("PAIRCODE = %q", m)
	}
	cert, err := tlsutil.LoadOrCreate(filepath.Join(dir, "tls"))
	if err != nil {
		t.Fatal(err)
	}
	if want := tlsutil.CertFingerprint(cert); fp != want {
		t.Fatalf("fingerprint shown to the user %q is not the served certificate's %q", fp, want)
	}
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dcancel()
	ws, _, err := websocket.Dial(dctx, "wss://"+addr+"/tfsp", &websocket.DialOptions{HTTPClient: hc})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	c := &wsClient{t: t, ws: ws}
	ch := c.read(5 * time.Second)
	priv, _ := auth.GenerateKey()
	sig, _ := auth.SignNonce(priv, ch["nonce"].(string))
	c.write(map[string]any{"v": 1, "t": "hello", "publicKey": auth.PublicKeyB64(priv), "sig": sig, "pairCode": code})
	if w := c.read(5 * time.Second); w["t"] != "welcome" || w["kind"] != "tf-squatterd" || w["instance"] != "inst-tls" {
		t.Fatalf("welcome over TLS = %v", w)
	}
	// plain ws:// must not work against the TLS listener
	if _, _, err := websocket.Dial(dctx, "ws://"+addr+"/tfsp", nil); err == nil {
		t.Fatal("plain ws:// was accepted on a TLS listener")
	}
}

// --insecure-loopback is refused with a non-loopback bind.
func TestInsecureRequiresLoopbackBind(t *testing.T) {
	a, b := socketPair(t)
	defer a.Close()
	err := Serve(context.Background(), ServeOptions{Conn: b, DataDir: t.TempDir(), Bind: "0.0.0.0", Port: 0, InsecureLoopback: true})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("Serve = %v, want a loopback error", err)
	}
}

// ---------------------------------------------------------------- remote world editing

func TestWorldPutDeleteAndBroadcast(t *testing.T) {
	h := newHarness(t)
	op := h.dial("operator")
	other := h.dial("viewer")
	op.write(map[string]any{"v": 1, "t": "world.put", "id": "p1", "password": "s3cr3t",
		"world": map[string]any{"name": "newmud", "host": "mud.example", "port": "4000", "type": "tiny", "char": "bob", "tls": true, "charset": "UTF-8"}})
	ch := op.until(func(m map[string]any) bool { return m["t"] == "world.changed" })
	if ch["name"] != "newmud" || ch["created"] != true || ch["reqId"] != "p1" {
		t.Fatalf("world.changed = %v", ch)
	}
	if o := other.untilType("world.changed"); o["name"] != "newmud" { // everyone is told
		t.Fatalf("viewer was not told: %v", o)
	}
	raw, _ := json.Marshal(ch)
	if strings.Contains(string(raw), "s3cr3t") {
		t.Fatalf("password leaked in a broadcast: %s", raw)
	}
	m, _ := h.tf.wait("WORLD_PUT", 0)
	// WORLD_PUT reqid name host port type char passmode pass ssl echo charset mfile mfilemode save
	want := map[int]string{2: "newmud", 3: "mud.example", 4: "4000", 5: "tiny", 6: "bob", 7: "set", 8: "s3cr3t", 9: "1", 10: "0", 11: "UTF-8", 12: "", 13: "keep", 14: "0"}
	for i, v := range want {
		if m[i] != v {
			t.Errorf("WORLD_PUT field %d = %q, want %q (%q)", i, m[i], v, m)
		}
	}
	op.write(map[string]any{"v": 1, "t": "world.delete", "id": "d1", "name": "newmud"})
	if ch := op.until(func(m map[string]any) bool { return m["t"] == "world.changed" }); ch["deleted"] != true {
		t.Fatalf("delete broadcast = %v", ch)
	}
}

func TestWorldPutPermissions(t *testing.T) {
	h := newHarness(t)
	v := h.dial("viewer")
	v.write(map[string]any{"v": 1, "t": "world.put", "world": map[string]any{"name": "x", "host": "h", "port": "1"}})
	if e := v.untilType("error"); e["code"] != "forbidden" {
		t.Fatalf("viewer put = %v", e)
	}
	op := h.dial("operator")
	// an operator may not set a macro file (it is code TinyFugue loads on connect)
	op.write(map[string]any{"v": 1, "t": "world.put", "setMfile": true, "world": map[string]any{"name": "x", "host": "h", "port": "1", "mfile": "/tmp/evil.tf"}})
	if e := op.untilType("error"); e["code"] != "forbidden" {
		t.Fatalf("operator mfile = %v", e)
	}
	if countType(h.tf, "WORLD_PUT") != 0 {
		t.Fatal("a forbidden put reached TinyFugue")
	}
	// without setMfile the stored macro file is kept, even if the client sends one
	op.write(map[string]any{"v": 1, "t": "world.put", "world": map[string]any{"name": "x", "host": "h", "port": "1", "mfile": "/tmp/ignored.tf"}})
	op.untilType("world.changed")
	m, _ := h.tf.wait("WORLD_PUT", 0)
	if m[12] != "" || m[13] != "keep" {
		t.Fatalf("mfile was not kept: %q", m)
	}
	// an admin can set it explicitly
	ad := h.dial("admin")
	ad.write(map[string]any{"v": 1, "t": "world.put", "setMfile": true, "world": map[string]any{"name": "y", "host": "h", "port": "1", "mfile": "/home/u/y.tf"}})
	ad.untilType("world.changed")
	h.tf.mu.Lock()
	var last []string
	for _, g := range h.tf.got {
		if g[0] == "WORLD_PUT" {
			last = g
		}
	}
	h.tf.mu.Unlock()
	if last[12] != "/home/u/y.tf" || last[13] != "set" {
		t.Fatalf("admin mfile = %q", last)
	}
	// bad input is refused before TinyFugue sees it
	for _, bad := range []string{"", "has space", "(unnamed1)"} {
		op.write(map[string]any{"v": 1, "t": "world.put", "world": map[string]any{"name": bad, "host": "h", "port": "1"}})
		if e := op.untilType("error"); e["code"] != "bad_request" {
			t.Errorf("name %q: %v", bad, e)
		}
	}
	op.write(map[string]any{"v": 1, "t": "world.put", "world": map[string]any{"name": "z", "host": "h\nx", "port": "1"}})
	if e := op.untilType("error"); e["code"] != "bad_request" {
		t.Errorf("control character in host: %v", e)
	}
}

// ---------------------------------------------------------------- GMCP

func TestGmcpFromGameAndToGame(t *testing.T) {
	h := newHarness(t)
	announce(h)
	h.tf.send("GMCP", sid, "Char.Vitals", `{"hp":5,"mp":7}`)
	h.tf.send("GMCP", sid, "Core.Goodbye", "")
	h.tf.send("GMCP", sid, "Broken.Json", `{not json`)
	time.Sleep(200 * time.Millisecond)
	c := h.dial("operator")
	c.write(map[string]any{"v": 1, "t": "attach", "session": sid})
	c.untilType("attached")
	entries := c.untilType("backlog")["entries"].([]any)
	if len(entries) != 3 {
		t.Fatalf("entries = %v", entries)
	}
	g := entries[0].(map[string]any)
	if g["t"] != "gmcp" || g["package"] != "Char.Vitals" || g["data"].(map[string]any)["hp"].(float64) != 5 {
		t.Fatalf("gmcp = %v", g)
	}
	if entries[1].(map[string]any)["data"] != nil || entries[2].(map[string]any)["data"] != nil {
		t.Errorf("absent or invalid JSON must become null: %v %v", entries[1], entries[2])
	}
	// to the game: needs a controller, valid package and JSON
	c.write(map[string]any{"v": 1, "t": "gmcp.send", "session": sid, "package": "Core.Supports.Set", "data": []string{"Char 1"}})
	m, _ := h.tf.wait("GMCP_SEND", 0) // a successful send is silent, like send
	if m[2] != sid || m[3] != `Core.Supports.Set ["Char 1"]` {
		t.Fatalf("GMCP_SEND = %q", m)
	}
	for _, bad := range []string{"bad package!", "", "a b"} {
		c.write(map[string]any{"v": 1, "t": "gmcp.send", "session": sid, "package": bad})
		if e := c.untilType("error"); e["code"] != "bad_request" {
			t.Errorf("package %q: %v", bad, e)
		}
	}
	v := h.dial("viewer")
	v.write(map[string]any{"v": 1, "t": "gmcp.send", "session": sid, "package": "Core.Hello"})
	if e := v.untilType("error"); e["code"] != "forbidden" {
		t.Errorf("viewer gmcp.send = %v", e)
	}
}

// ---------------------------------------------------------------- remote commands

func TestExec(t *testing.T) {
	h := newHarness(t)
	op := h.dial("operator")
	op.write(map[string]any{"v": 1, "t": "tf.exec", "line": "/echo hi"})
	if e := op.untilType("error"); e["code"] != "forbidden" {
		t.Fatalf("operator exec = %v", e)
	}
	ad := h.dial("admin")
	ad.write(map[string]any{"v": 1, "t": "tf.exec", "id": "x1", "line": "/echo hi"})
	r := ad.untilType("tf.exec.result")
	out := r["out"].([]any)
	if r["ok"] != true || r["reqId"] != "x1" || len(out) != 2 || out[0] != "line one" || out[1] != "line\ttwo" {
		t.Fatalf("exec result = %v", r)
	}
	// TinyFugue's own switch (squat_exec) can still refuse
	ad.write(map[string]any{"v": 1, "t": "tf.exec", "line": "/forbidden"})
	if e := ad.untilType("error"); e["code"] != "forbidden" {
		t.Fatalf("TF-side refusal = %v", e)
	}
	ad.write(map[string]any{"v": 1, "t": "tf.exec", "line": ""})
	if e := ad.untilType("error"); e["code"] != "bad_request" {
		t.Fatalf("empty line = %v", e)
	}
}

// ---------------------------------------------------------------- display text and takeover requests

func TestDispSpansAndAskBroadcast(t *testing.T) {
	h := newHarness(t)
	announce(h)
	h.tf.send("LINE", sid, "g", "", "game", "1700000000001", "you see a sword", "\x1b[1;33myou see a sword\x1b[0m")
	time.Sleep(150 * time.Millisecond)
	c := h.dial("operator")
	c.write(map[string]any{"v": 1, "t": "attach", "session": sid})
	c.untilType("attached")
	l := c.untilType("backlog")["entries"].([]any)[0].(map[string]any)
	if _, has := l["disp"]; !has || l["disp"].([]any)[0].(map[string]any)["b"] != true {
		t.Fatalf("disp spans missing: %v", l)
	}
	if l["spans"].([]any)[0].(map[string]any)["b"] != nil {
		t.Errorf("raw spans must stay unstyled: %v", l["spans"])
	}
	// TinyFugue asks its user: every client sees control.pending
	other := h.dial("viewer")
	h.tf.send("ASK", sid, "input", "remote", "laptop")
	p := other.untilType("control.pending")
	if p["session"] != sid || p["axis"] != "input" || p["mode"] != "remote" || p["deviceName"] != "laptop" {
		t.Fatalf("control.pending = %v", p)
	}
}

// a denial from TinyFugue's user reaches the requesting client as an error
func TestTakeoverDeniedByTinyFugueUser(t *testing.T) {
	h := newHarness(t)
	announce(h)
	time.Sleep(100 * time.Millisecond)
	h.tf.setReply(func(f *fakeTF, m []string) {
		if m[0] == "CTRL_SET" {
			f.send("ERR", m[1], "denied", "the TinyFugue user said no")
		}
	})
	c := h.dial("operator")
	c.write(map[string]any{"v": 1, "t": "control.set", "id": "k1", "session": sid, "axis": "input", "mode": "remote"})
	e := c.untilType("error")
	if e["code"] != "denied" || e["reqId"] != "k1" {
		t.Fatalf("error = %v", e)
	}
	// and the bridge must not believe the device is in control
	c.write(map[string]any{"v": 1, "t": "resize", "session": sid, "cols": 90, "rows": 30})
	time.Sleep(200 * time.Millisecond)
	if n := countType(h.tf, "RESIZE"); n != 0 {
		t.Fatalf("a denied device had its resize forwarded (%d)", n)
	}
}

// Regression: a GMCP entry must carry its sequence number, otherwise the client
// library (which de-duplicates by seq) silently drops every live GMCP message.
func TestLiveGmcpHasSeqAndIsNotDropped(t *testing.T) {
	h := newHarness(t)
	announce(h)
	time.Sleep(100 * time.Millisecond)
	c := h.dial("operator")
	c.write(map[string]any{"v": 1, "t": "attach", "session": sid})
	c.untilType("attached")
	c.untilType("backlog")
	h.tf.send("GMCP", sid, "Char.Vitals", `{"hp":1}`)
	g := c.untilType("gmcp")
	if g["seq"] == nil || g["seq"].(float64) < 1 {
		t.Fatalf("live gmcp has no sequence number: %v", g)
	}
}

// A takeover request that waits for the TinyFugue user must not freeze the
// client: it can keep sending and issuing other requests meanwhile.
func TestPendingControlRequestDoesNotBlockTheClient(t *testing.T) {
	h := newHarness(t)
	announce(h)
	time.Sleep(100 * time.Millisecond)
	release := make(chan struct{})
	h.tf.setReply(func(f *fakeTF, m []string) {
		switch m[0] {
		case "CTRL_SET":
			go func() { <-release; f.send("OK", m[1]); f.send("CTRL", m[2], "remote", "tf", "x") }()
		case "WORLDS":
			f.send("OK", m[1])
		}
	})
	c := h.dial("operator")
	c.write(map[string]any{"v": 1, "t": "control.set", "id": "slow", "session": sid, "axis": "input", "mode": "remote"})
	time.Sleep(150 * time.Millisecond)
	// while that waits, another request on the same connection is answered at once
	start := time.Now()
	c.write(map[string]any{"v": 1, "t": "world.list", "id": "quick"})
	wl := c.until(func(m map[string]any) bool { return m["t"] == "world.list" })
	if wl["reqId"] != "quick" || time.Since(start) > 2*time.Second {
		t.Fatalf("world.list was blocked behind the pending control.set (%v after %v)", wl, time.Since(start))
	}
	close(release)
	st := c.until(func(m map[string]any) bool { return m["t"] == "control.state" && m["reqId"] == "slow" })
	if st["input"] != "remote" {
		t.Fatalf("control.state = %v", st)
	}
}

// ---------------------------------------------------------------- MSDP

func msdpBytes(t *testing.T, vars map[string]any) string {
	t.Helper()
	p, err := msdp.Encode(msdp.FromMap(vars))
	if err != nil {
		t.Fatal(err)
	}
	return string(p)
}

func TestMsdpFromGameIsDecodedStoredAndLive(t *testing.T) {
	h := newHarness(t)
	announce(h)
	h.tf.send("MSDP", sid, msdpBytes(t, map[string]any{"HEALTH": "100", "ROOM": map[string]any{"NAME": "Café ☕", "EXITS": []any{"n", "s"}}}))
	h.tf.send("MSDP", sid, "garbage that is not msdp")                    // ignored
	h.tf.send("MSDP", sid, string([]byte{msdp.Var, 'x', msdp.Val, 0xc3})) // invalid UTF-8 must not break the frame
	time.Sleep(200 * time.Millisecond)
	c := h.dial("operator")
	c.write(map[string]any{"v": 1, "t": "attach", "session": sid})
	c.untilType("attached")
	entries := c.untilType("backlog")["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("entries = %v", entries)
	}
	m := entries[0].(map[string]any)
	room := m["vars"].(map[string]any)["ROOM"].(map[string]any)
	if m["t"] != "msdp" || m["vars"].(map[string]any)["HEALTH"] != "100" || room["NAME"] != "Café ☕" || room["EXITS"].([]any)[1] != "s" {
		t.Fatalf("msdp entry = %v", m)
	}
	// Regression (same bug GMCP had): a LIVE entry must carry its sequence number,
	// or the client library drops it as a duplicate.
	h.tf.send("MSDP", sid, msdpBytes(t, map[string]any{"HEALTH": "99"}))
	live := c.untilType("msdp")
	if live["seq"] == nil || live["seq"].(float64) < 3 {
		t.Fatalf("live msdp has no sequence number: %v", live)
	}
}

func TestMsdpToGame(t *testing.T) {
	h := newHarness(t)
	announce(h)
	time.Sleep(100 * time.Millisecond)
	c := h.dial("operator")
	c.write(map[string]any{"v": 1, "t": "msdp.send", "id": "m1", "session": sid, "vars": map[string]any{"REPORT": []string{"HEALTH", "MANA"}}})
	if ok := c.untilType("ok"); ok["reqId"] != "m1" {
		t.Fatalf("ok = %v", ok)
	}
	m, _ := h.tf.wait("MSDP_SEND", 0)
	want := string([]byte{msdp.Var}) + "REPORT" + string([]byte{msdp.Val, msdp.ArrayOpen, msdp.Val}) + "HEALTH" + string([]byte{msdp.Val}) + "MANA" + string([]byte{msdp.ArrayClose})
	if m[2] != sid || m[3] != want {
		t.Fatalf("MSDP_SEND = %q, want payload %q", m, want)
	}
	for name, vars := range map[string]any{"empty": map[string]any{}, "reserved byte": map[string]any{"X": "a\x01b"}, "no vars": nil} {
		c.write(map[string]any{"v": 1, "t": "msdp.send", "session": sid, "vars": vars})
		if e := c.untilType("error"); e["code"] != "bad_request" {
			t.Errorf("%s: %v", name, e)
		}
	}
	c.write(map[string]any{"v": 1, "t": "msdp.send", "session": "w-nope-1-1", "vars": map[string]any{"A": "b"}})
	if e := c.untilType("error"); e["code"] != "no_such_session" {
		t.Errorf("unknown session: %v", e)
	}
	v := h.dial("viewer")
	v.write(map[string]any{"v": 1, "t": "msdp.send", "session": sid, "vars": map[string]any{"A": "b"}})
	if e := v.untilType("error"); e["code"] != "forbidden" {
		t.Errorf("viewer: %v", e)
	}
	if n := countType(h.tf, "MSDP_SEND"); n != 1 {
		t.Errorf("%d MSDP_SEND reached TinyFugue, want exactly the one valid request", n)
	}
}

// ---------------------------------------------------------------- state snapshots

func TestStateSnapshotOnAttachAndOnRequest(t *testing.T) {
	h := newHarness(t)
	announce(h)
	h.tf.send("GMCP", sid, "Char.Vitals", `{"hp":100}`)
	h.tf.send("GMCP", sid, "Char.Vitals", `{"hp":90}`) // the latest wins
	h.tf.send("GMCP", sid, "Room.Info", `{"name":"hall"}`)
	h.tf.send("MSDP", sid, msdpBytes(t, map[string]any{"HEALTH": "100", "NAME": "Zoë"}))
	h.tf.send("MSDP", sid, msdpBytes(t, map[string]any{"HEALTH": "80"}))
	for i := 0; i < 30; i++ { // plenty of text so a short backlog would not reach the data
		h.tf.send("LINE", sid, "g", "", "game", "1700000000001", fmt.Sprintf("filler %d", i))
	}
	time.Sleep(300 * time.Millisecond)

	c := h.dial("operator")
	c.write(map[string]any{"v": 1, "t": "attach", "session": sid, "maxBacklog": 2})
	c.untilType("attached")
	bl := c.untilType("backlog")
	if n := len(bl["entries"].([]any)); n != 2 {
		t.Fatalf("backlog entries = %d, want 2", n)
	}
	st := c.untilType("state")
	gm := st["gmcp"].(map[string]any)
	ms := st["msdp"].(map[string]any)
	if gm["Char.Vitals"].(map[string]any)["hp"].(float64) != 90 || gm["Room.Info"].(map[string]any)["name"] != "hall" {
		t.Fatalf("gmcp snapshot = %v", gm)
	}
	if ms["HEALTH"] != "80" || ms["NAME"] != "Zoë" {
		t.Fatalf("msdp snapshot = %v (the later HEALTH must win, the earlier NAME must survive)", ms)
	}
	if st["asOf"].(float64) < 35 || st["session"] != sid {
		t.Fatalf("snapshot header = %v", st)
	}
	// the live feed continues after the snapshot, and the snapshot follows it
	h.tf.send("GMCP", sid, "Char.Vitals", `{"hp":70}`)
	if g := c.untilType("gmcp"); g["data"].(map[string]any)["hp"].(float64) != 70 {
		t.Fatalf("live gmcp = %v", g)
	}
	c.write(map[string]any{"v": 1, "t": "state.get", "id": "s1", "session": sid})
	st = c.untilType("state")
	if st["reqId"] != "s1" || st["gmcp"].(map[string]any)["Char.Vitals"].(map[string]any)["hp"].(float64) != 70 {
		t.Fatalf("state.get = %v", st)
	}
	c.write(map[string]any{"v": 1, "t": "state.get", "session": "w-nope-1-1"})
	if e := c.untilType("error"); e["code"] != "no_such_session" {
		t.Fatalf("unknown session: %v", e)
	}
}

func TestNoSnapshotForPlainTextSessions(t *testing.T) {
	h := newHarness(t)
	announce(h)
	h.tf.send("LINE", sid, "g", "", "game", "1700000000001", "just text")
	time.Sleep(150 * time.Millisecond)
	c := h.dial("operator")
	c.write(map[string]any{"v": 1, "t": "attach", "session": sid})
	c.untilType("attached")
	c.untilType("backlog")
	h.tf.send("LINE", sid, "g", "", "game", "1700000000002", "more text")
	// the next thing is the live line, not an empty snapshot
	if m := c.until(func(m map[string]any) bool { return m["t"] == "state" || m["t"] == "line" }); m["t"] != "line" {
		t.Fatalf("got %v, want the live line", m)
	}
}

// TinyFugue reports an unterminated line as a prompt and keeps the partial text, so
// when the line completes it reports the whole line again. A remote must see it once.
func TestPromptThatBecomesALineIsNotShownTwice(t *testing.T) {
	h := newHarness(t)
	announce(h)
	c := h.dial("operator")
	c.write(map[string]any{"v": 1, "t": "attach", "id": "a1", "session": sid})
	c.untilType("backlog")

	// partial line, then the finished line (with TinyFugue's highlight): one line, the finished one
	h.tf.send("LINE", sid, "g", "p", "game", "1700000000001", `You say, "hello"`)
	h.tf.send("LINE", sid, "g", "", "game", "1700000000002", `You say, "hello"`, "\x1b[1mYou say, \"hello\"\x1b[0m")
	l := c.untilType("line")
	if l["prompt"] == true || l["seq"].(float64) != 1 {
		t.Fatalf("first line = %v, want the finished line as seq 1", l)
	}
	if l["disp"] == nil {
		t.Errorf("the finished line lost TinyFugue's display text: %v", l)
	}

	// a prompt that stays a prompt appears (once) after the hold, and keeps its place in the order
	h.tf.send("LINE", sid, "g", "p", "game", "1700000000003", "Login: ")
	p := c.untilType("line")
	if p["prompt"] != true || p["seq"].(float64) != 2 {
		t.Fatalf("prompt = %v, want seq 2 and prompt", p)
	}

	// a prompt followed by an UNRELATED line is flushed first (order kept)
	h.tf.send("LINE", sid, "g", "p", "game", "1700000000004", "Password: ")
	h.tf.send("LINE", sid, "g", "", "game", "1700000000005", "something else")
	a := c.untilType("line")
	b := c.untilType("line")
	if a["prompt"] != true || a["seq"].(float64) != 3 || b["seq"].(float64) != 4 {
		t.Fatalf("order: %v then %v", a, b)
	}

	// a growing prompt is shown as its final form only
	h.tf.send("LINE", sid, "g", "p", "game", "1700000000006", "Pass")
	h.tf.send("LINE", sid, "g", "p", "game", "1700000000007", "Password again: ")
	g := c.untilType("line")
	if g["seq"].(float64) != 5 || g["spans"].([]any)[0].(map[string]any)["s"] != "Password again: " {
		t.Fatalf("growing prompt = %v", g)
	}
}

func TestFeaturesAnnouncedToClientsIncludeScriptsOnlyWhenTinyFugueAllowsThem(t *testing.T) {
	has := func(fs []string, want string) bool {
		for _, f := range fs {
			if f == want {
				return true
			}
		}
		return false
	}
	if has(featuresFor([]string{"lines", "exec"}), "scripts") {
		t.Fatal("scripts announced although TinyFugue did not offer them")
	}
	if !has(featuresFor([]string{"lines", "scripts"}), "scripts") {
		t.Fatal("scripts offered by TinyFugue were not announced")
	}
}
