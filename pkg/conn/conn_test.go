package conn

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

func TestEndToEndTextColourGMCPMCCP(t *testing.T) {
	s := fakemush.New(t)
	c := dialT(t, cfgFor(s))
	if ev := next(t, c); ev.Kind != EvState || !ev.Connected {
		t.Fatalf("first event %+v", ev)
	}

	s.SendString("\x1b[38;2;255;128;0mtrue\x1b[0m \x1b[48;2;1;2;3mbg\x1b[0m\r\n")
	expectLine(t, c, `[{"s":"true","fg":"#ff8000"},{"s":" "},{"s":"bg","bg":"#010203"}]`, false)

	s.SendString("\x1b[38;5;196mc256\x1b[48;5;21m+bg\x1b[0m\r\n")
	expectLine(t, c, `[{"s":"c256","fg":196},{"s":"+bg","fg":196,"bg":21}]`, false)

	s.SendString("\x1b[1;31mbold\x1b[0m \x1b[92mhi\x1b[0m \x1b[41mbgred\x1b[0m\r\n")
	expectLine(t, c, `[{"s":"bold","fg":1,"b":true},{"s":" "},{"s":"hi","fg":10},{"s":" "},{"s":"bgred","bg":1}]`, false)

	s.SendString("\x1b[2;3;4;5;7;9mattr\x1b[0m\r\n")
	expectLine(t, c, `[{"s":"attr","dim":true,"i":true,"u":true,"st":true,"blink":true,"rev":true}]`, false)

	s.SendString("\x1b]8;;http://x.y/z\x1b\\link\x1b]8;;\x1b\\ tail\r\n")
	expectLine(t, c, `[{"s":"link","link":"http://x.y/z"},{"s":" tail"}]`, false)

	// UTF-8 (2, 3 and 4 byte sequences) split across TCP reads at every
	// interesting place, with colour state continuing across lines.
	emoji := []byte("a😀é日本\r\n")
	s.SendChunks(emoji[:2], emoji[2:4], emoji[4:6], emoji[6:7], emoji[7:9], emoji[9:])
	expectLine(t, c, `[{"s":"a😀é日本"}]`, false)

	s.SendString("\x1b[32mgreen")
	s.SendString("\r\nstill\x1b[0m\r\n")
	expectLine(t, c, `[{"s":"green","fg":2}]`, false)
	expectLine(t, c, `[{"s":"still","fg":2}]`, false)

	// invalid UTF-8 -> U+FFFD
	s.SendString("bad\xc3(\x80!\r\n")
	expectLine(t, c, `[{"s":"bad�(�!"}]`, false)

	// prompt flushed after 150ms of silence, then consumed
	start := time.Now()
	s.SendString("HP: 10> ")
	expectLine(t, c, `[{"s":"HP: 10\u003e "}]`, true)
	if d := time.Since(start); d < 100*time.Millisecond {
		t.Fatalf("prompt flushed too early: %v", d)
	}
	s.SendString("next\r\n")
	expectLine(t, c, `[{"s":"next"}]`, false)

	// EOR flushes immediately
	s.Will(fakemush.OptEOR)
	s.SendString("EOR> ")
	s.EOR()
	expectLine(t, c, `[{"s":"EOR\u003e "}]`, true)

	// GMCP + remote echo
	s.Will(fakemush.OptGMCP)
	s.WaitSub(fakemush.OptGMCP, 2)
	s.GMCP("Char.Vitals", `{"hp":10}`)
	g := nextKind(t, c, EvGmcp)
	if g.Package != "Char.Vitals" || string(g.Data) != `{"hp":10}` {
		t.Fatalf("%+v", g)
	}
	s.Will(fakemush.OptEcho)
	if ev := nextKind(t, c, EvRemoteEcho); !ev.RemoteEcho || !c.RemoteEcho() {
		t.Fatal("remote echo")
	}
	s.Wont(fakemush.OptEcho)
	if ev := nextKind(t, c, EvRemoteEcho); ev.RemoteEcho {
		t.Fatal("remote echo off")
	}

	// MCCP2: compressed segment (split arbitrarily), then back to plain
	s.Will(fakemush.OptMCCP2)
	s.WaitNeg(fakemush.DO, fakemush.OptMCCP2)
	s.StartMCCP2()
	s.SendString("\x1b[31mzipped\x1b[0m ")
	s.SendChunks([]byte("pa"), []byte("rt\r\n"))
	s.GMCP("Zipped.Gmcp", `[1,2]`)
	s.EndMCCP2()
	s.SendString("unzipped\r\n")
	expectLine(t, c, `[{"s":"zipped","fg":1},{"s":" part"}]`, false)
	if g := nextKind(t, c, EvGmcp); g.Package != "Zipped.Gmcp" || string(g.Data) != `[1,2]` {
		t.Fatalf("%+v", g)
	}
	expectLine(t, c, `[{"s":"unzipped"}]`, false)

	// sending: everything arrives as lines, 0xFF escaped
	if err := c.Send("look\nsay hi \xff"); err != nil {
		t.Fatal(err)
	}
	s.ExpectLine("look")
	if got := s.ReadLine(); got != "say hi \xff" {
		t.Fatalf("%q", got)
	}
	s.CloseConn()
	e := ended(t, c)
	if e.Reason != proto.EndRemoteClosed || e.LastText != "unzipped" {
		t.Fatalf("%+v", e)
	}
	if err := c.Send("x"); !errors.Is(err, ErrClosed) {
		t.Fatalf("send after end: %v", err)
	}
}

func TestMTTSNAWSAndTTYPE(t *testing.T) {
	s := fakemush.New(t)
	c := dialT(t, cfgFor(s))
	s.Do(fakemush.OptTType)
	s.Do(fakemush.OptNAWS)
	s.Will(fakemush.OptSGA)
	s.Do(fakemush.OptEOR)
	s.WaitNeg(fakemush.WILL, fakemush.OptTType)
	s.WaitNeg(fakemush.WILL, fakemush.OptNAWS)
	s.WaitNeg(fakemush.DO, fakemush.OptSGA)
	if got := s.WaitSub(fakemush.OptNAWS, 1); !bytes.Equal(got, []byte{0, 80, 0, 24}) {
		t.Fatalf("naws %v", got)
	}
	ask := []byte{1}
	var got []string
	for i := 1; i <= 3; i++ {
		s.Sub(fakemush.OptTType, ask)
		d := s.WaitSub(fakemush.OptTType, i)
		if d[0] != 0 {
			t.Fatalf("not IS: %v", d)
		}
		got = append(got, string(d[1:]))
	}
	want := []string{"SquatClient", "XTERM-TRUECOLOR", "MTTS 269"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("ttype %v want %v", got, want)
	}
	c.Resize(132, 43)
	if got := s.WaitSub(fakemush.OptNAWS, 2); !bytes.Equal(got, []byte{0, 132, 0, 43}) {
		t.Fatalf("naws2 %v", got)
	}
	c.SetCaps(proto.Caps{Truecolor: true, UTF8: true, ScreenReader: true})
	s.Sub(fakemush.OptTType, ask)
	if d := s.WaitSub(fakemush.OptTType, 4); string(d) != "\x00MTTS 333" { // 269 + 64
		t.Fatalf("%q", d)
	}
}

func TestMTTSProxiedTLSAndNo256(t *testing.T) {
	s := fakemush.New(t, fakemush.WithTLS())
	cfg := cfgFor(s)
	cfg.World.TLS, cfg.World.TLSVerify = true, "insecure"
	cfg.Proxied = true
	cfg.Caps = proto.Caps{UTF8: true} // no truecolor
	c := dialT(t, cfg)
	_ = c
	s.Do(fakemush.OptTType)
	for i := 1; i <= 3; i++ {
		s.Sub(fakemush.OptTType, []byte{1})
		s.WaitSub(fakemush.OptTType, i)
	}
	subs := s.Subnegs()
	if string(subs[1].Data) != "\x00XTERM-256COLOR" {
		t.Fatalf("%q", subs[1].Data)
	}
	// ANSI1 + UTF8 4 + 256c 8 + PROXY 128 + SSL 2048 = 2189
	if string(subs[2].Data) != "\x00MTTS 2189" {
		t.Fatalf("%q", subs[2].Data)
	}
}

func TestEndReasons(t *testing.T) {
	t.Run("remote_closed", func(t *testing.T) {
		s := fakemush.New(t)
		c := dialT(t, cfgFor(s))
		s.SendString("partial no newline")
		s.CloseConn()
		nextKind(t, c, EvLine) // flushed as prompt at end
		if e := ended(t, c); e.Reason != proto.EndRemoteClosed || e.LastText != "partial no newline" {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("reset", func(t *testing.T) {
		s := fakemush.New(t)
		c := dialT(t, cfgFor(s))
		s.SendLine("x")
		nextKind(t, c, EvLine)
		s.Reset()
		if e := ended(t, c); e.Reason != proto.EndReset {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("client_quit", func(t *testing.T) {
		s := fakemush.New(t)
		c := dialT(t, cfgFor(s))
		c.Close()
		c.Close()
		if e := ended(t, c); e.Reason != proto.EndClientQuit {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("connect_failed_dial", func(t *testing.T) {
		addr := fakemush.ClosedAddr(t)
		var host string
		var port int
		fmt.Sscanf(strings.Replace(addr, ":", " ", 1), "%s %d", &host, &port)
		_, err := Dial(context.Background(), Config{World: proto.World{Host: host, Port: port}})
		var de *DialError
		if !errors.As(err, &de) || de.Reason != proto.EndConnectFailed {
			t.Fatalf("%v", err)
		}
	})
	t.Run("connect_failed_open", func(t *testing.T) {
		addr := fakemush.ClosedAddr(t)
		var host string
		var port int
		fmt.Sscanf(strings.Replace(addr, ":", " ", 1), "%s %d", &host, &port)
		c, err := Open(context.Background(), Config{World: proto.World{Host: host, Port: port}})
		if err != nil {
			t.Fatal(err)
		}
		if e := ended(t, c); e.Reason != proto.EndConnectFailed || e.Detail == "" {
			t.Fatalf("%+v", e)
		}
		if err := c.Send("x"); err == nil {
			t.Fatal("send must fail")
		}
	})
	t.Run("open_ok", func(t *testing.T) {
		s := fakemush.New(t)
		c, _ := Open(context.Background(), cfgFor(s))
		defer c.Close()
		if ev := next(t, c); ev.Kind != EvState || !ev.Connected {
			t.Fatalf("%+v", ev)
		}
	})
	t.Run("tls_to_plain_server", func(t *testing.T) {
		s := fakemush.New(t, fakemush.WithGreeting("this is not tls\r\n"))
		cfg := cfgFor(s)
		cfg.World.TLS = true
		_, err := Dial(context.Background(), cfg)
		var de *DialError
		if !errors.As(err, &de) || de.Reason != proto.EndTLSError {
			t.Fatalf("%v", err)
		}
	})
	t.Run("tls_untrusted_strict", func(t *testing.T) {
		s := fakemush.New(t, fakemush.WithTLS())
		cfg := cfgFor(s)
		cfg.World.TLS, cfg.World.TLSVerify = true, "strict"
		_, err := Dial(context.Background(), cfg)
		var de *DialError
		if !errors.As(err, &de) || de.Reason != proto.EndTLSError {
			t.Fatalf("%v", err)
		}
	})
	t.Run("tls_ok_variants", func(t *testing.T) {
		s := fakemush.New(t, fakemush.WithTLS())
		cfg := cfgFor(s)
		cfg.World.TLS, cfg.World.TLSVerify = true, "strict"
		cfg.TLSConfig = nil
		// trusted via RootCAs
		cfg.TLSConfig = tlsWith(s)
		c := dialT(t, cfg)
		s.SendLine("secure \x1b[32mok")
		expectLine(t, c, `[{"s":"secure "},{"s":"ok","fg":2}]`, false)
		// insecure + matching pin
		cfg = cfgFor(s)
		cfg.World.TLS, cfg.World.TLSVerify = true, "insecure"
		want := s.Fingerprint()
		cfg.VerifyFingerprint = func(sum [32]byte) error {
			if fmt.Sprintf("%x", sum) != want {
				return errors.New("pin mismatch")
			}
			return nil
		}
		c2 := dialT(t, cfg)
		_ = c2
		// insecure + wrong pin
		cfg.VerifyFingerprint = func(sum [32]byte) error { return errors.New("pin mismatch") }
		_, err := Dial(context.Background(), cfg)
		var de *DialError
		if !errors.As(err, &de) || de.Reason != proto.EndTLSError || !strings.Contains(err.Error(), "pin mismatch") {
			t.Fatalf("%v", err)
		}
		_ = sha256.Size
	})
}

func TestLatin1AndSend(t *testing.T) {
	s := fakemush.New(t)
	cfg := cfgFor(s)
	cfg.World.Encoding = "latin1"
	c := dialT(t, cfg)
	s.SendString("caf\xe9 \xff\xff!\r\n")
	expectLine(t, c, `[{"s":"café ÿ!"}]`, false)
	if err := c.Send("café ☃"); err != nil {
		t.Fatal(err)
	}
	s.Peer()
	s.Eventually(func() bool { return bytes.HasSuffix(s.Received(), []byte("caf\xe9 ?\r\n")) }, "latin1 send")
	// MTTS has no UTF-8 bit
	s.Do(fakemush.OptTType)
	for i := 1; i <= 3; i++ {
		s.Sub(fakemush.OptTType, []byte{1})
		s.WaitSub(fakemush.OptTType, i)
	}
	if d := s.Subnegs()[2].Data; string(d) != "\x00MTTS 265" { // 1+8+256
		t.Fatalf("%q", d)
	}
}

func TestSendEncodingUTF8(t *testing.T) {
	s := fakemush.New(t)
	c := dialT(t, cfgFor(s))
	if err := c.Send("日本\r\n\nend"); err != nil {
		t.Fatal(err)
	}
	s.ExpectLine("日本")
	s.ExpectLine("")
	s.ExpectLine("end")
}

func TestLongLineSplit(t *testing.T) {
	s := fakemush.New(t)
	c := dialT(t, cfgFor(s))
	long := strings.Repeat("é", 40000) // 80000 bytes
	s.SendString(long + "\r\nshort\r\n")
	var total int
	var n int
	for {
		ev := nextKind(t, c, EvLine)
		text := ev.Spans[0].S
		if text == "short" {
			break
		}
		if len(text) > MaxLine {
			t.Fatalf("line of %d bytes", len(text))
		}
		total += len(text)
		n++
	}
	if total != len(long) || n != 2 {
		t.Fatalf("total=%d n=%d", total, n)
	}
}

func TestBackpressureNoDrop(t *testing.T) {
	s := fakemush.New(t)
	cfg := cfgFor(s)
	cfg.EventBuffer = 2
	c := dialT(t, cfg)
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&sb, "line %d\r\n", i)
	}
	s.SendString(sb.String())
	time.Sleep(200 * time.Millisecond) // consumer stalls; reader must block, not drop
	for i := 0; i < 300; i++ {
		ev := nextKind(t, c, EvLine)
		if got := ev.Spans[0].S; got != fmt.Sprintf("line %d", i) {
			t.Fatalf("got %q at %d", got, i)
		}
	}
}

func TestCloseWithBlockedConsumer(t *testing.T) {
	s := fakemush.New(t)
	cfg := cfgFor(s)
	cfg.EventBuffer = 1
	c := dialT(t, cfg)
	s.SendString(strings.Repeat("spam\r\n", 100))
	time.Sleep(100 * time.Millisecond) // reader is now blocked on a full channel
	done := make(chan struct{})
	go func() { c.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close blocked on a stalled consumer")
	}
	var last Event
	for ev := range c.Events() {
		last = ev
	}
	if last.Kind != EvEnded || last.Reason != proto.EndClientQuit {
		t.Fatalf("%+v", last)
	}
}

func TestSlowServerFirstByte(t *testing.T) {
	s := fakemush.New(t, fakemush.WithFirstByteDelay(300*time.Millisecond), fakemush.WithGreeting("welcome\r\n"))
	start := time.Now()
	c := dialT(t, cfgFor(s))
	expectLine(t, c, `[{"s":"welcome"}]`, false)
	if time.Since(start) < 250*time.Millisecond {
		t.Fatal("delay not applied")
	}
}

func TestBadMCCPEndsConnection(t *testing.T) {
	s := fakemush.New(t)
	c := dialT(t, cfgFor(s))
	s.Will(fakemush.OptMCCP2)
	s.WaitNeg(fakemush.DO, fakemush.OptMCCP2)
	s.Send([]byte{fakemush.IAC, fakemush.SB, fakemush.OptMCCP2, fakemush.IAC, fakemush.SE, 0x78, 0x9c, 0xff, 0xff, 0xff, 0xff})
	if e := ended(t, c); e.Reason != proto.EndRemoteClosed || !strings.Contains(e.Detail, "mccp2") {
		t.Fatalf("%+v", e)
	}
}

func TestPromptEscapeHeldBack(t *testing.T) {
	s := fakemush.New(t)
	c := dialT(t, cfgFor(s))
	s.SendString("go> \x1b[3")
	expectLine(t, c, `[{"s":"go\u003e "}]`, true)
	s.SendString("1mred\r\n")
	expectLine(t, c, `[{"s":"red","fg":1}]`, false)
}

func TestNoGoroutineLeaks(t *testing.T) {
	settle := func() int {
		for i := 0; i < 50; i++ {
			runtime.GC()
			time.Sleep(20 * time.Millisecond)
		}
		return runtime.NumGoroutine()
	}
	s := fakemush.New(t)
	before := settle()
	for i := 0; i < 5; i++ {
		c, err := Dial(context.Background(), cfgFor(s))
		if err != nil {
			t.Fatal(err)
		}
		peer := s.Accept()
		go func() {
			for range c.Events() {
			}
		}()
		c.Send("hi")
		if i%2 == 0 {
			c.Close()
		} else {
			peer.Close()
			<-c.Done()
		}
		c.Close()
	}
	// failed connects too
	for i := 0; i < 3; i++ {
		c, _ := Open(context.Background(), Config{World: proto.World{Host: "127.0.0.1", Port: 1}})
		<-c.Done()
	}
	deadline := time.Now().Add(3 * time.Second)
	after := settle()
	for after > before && time.Now().Before(deadline) {
		after = settle()
	}
	if after > before {
		buf := make([]byte, 1<<16)
		n := runtime.Stack(buf, true)
		t.Fatalf("goroutines before=%d after=%d\n%s", before, after, buf[:n])
	}
}

func TestTLSPromptIdleFlushAndContinue(t *testing.T) {
	s := fakemush.New(t, fakemush.WithTLS())
	cfg := cfgFor(s)
	cfg.World.TLS, cfg.World.TLSVerify = true, "insecure"
	c := dialT(t, cfg)
	for i := 0; i < 3; i++ { // read deadlines must not poison the TLS stream
		s.SendString("prompt> ")
		expectLine(t, c, `[{"s":"prompt\u003e "}]`, true)
	}
	s.SendLine("done")
	expectLine(t, c, `[{"s":"done"}]`, false)
}
