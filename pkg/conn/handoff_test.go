package conn

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/ansi"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/telnet"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

func plainLine(t *testing.T, c *Conn) (string, Event) {
	t.Helper()
	ev := nextKind(t, c, EvLine)
	return ansi.PlainText(ev.Spans), ev
}

// handOver stops c and continues the connection in a new Conn, through JSON as a new process would.
func handOver(t *testing.T, c *Conn, cfg Config) *Conn {
	t.Helper()
	st, f, err := c.Handoff()
	if err != nil {
		t.Fatalf("handoff: %v", err)
	}
	defer f.Close()
	// the old Conn's events end without an Ended event
	for {
		select {
		case ev, ok := <-c.Events():
			if !ok {
				goto closed
			}
			if ev.Kind == EvEnded {
				t.Fatal("a handed-over connection reported that it ended")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("events not closed after handoff")
		}
	}
closed:
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var st2 HandoffState
	if err := json.Unmarshal(b, &st2); err != nil {
		t.Fatal(err)
	}
	n, err := Adopt(cfg, st2, f)
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	t.Cleanup(n.Close)
	return n
}

// The game's connection survives: the same TCP connection goes on working both ways, a half-received
// line is completed by the new Conn, and a colour opened before the handoff still applies after it.
func TestHandoffKeepsTheConnectionAndTheStream(t *testing.T) {
	srv := fakemush.New(t, fakemush.WithGreeting("welcome\r\n"))
	cfg := cfgFor(srv)
	c := dialT(t, cfg)
	if l, _ := plainLine(t, c); l != "welcome" {
		t.Fatalf("%q", l)
	}

	srv.Send([]byte("\x1b[31mred line\r\n"), []byte("half a li"))
	if l, _ := plainLine(t, c); l != "red line" {
		t.Fatalf("%q", l)
	}
	time.Sleep(20 * time.Millisecond) // the partial line is in the splitter, not yet shown as a prompt
	n := handOver(t, c, cfg)

	// the rest of the line arrives at the NEW conn, still red
	srv.Send([]byte("ne\r\nnext\r\n"))
	l, ev := plainLine(t, n)
	if l != "half a line" {
		t.Fatalf("partial line not completed: %q", l)
	}
	if len(ev.Spans) == 0 || ev.Spans[0].Fg == nil {
		t.Fatalf("colour lost across the handoff: %+v", ev.Spans)
	}
	if l, ev := plainLine(t, n); l != "next" || ev.Spans[0].Fg == nil {
		t.Fatalf("%q %+v", l, ev.Spans)
	}
	// the very same connection still carries our input to the game
	if err := n.Send("ping"); err != nil {
		t.Fatal(err)
	}
	srv.ExpectLine("ping")
	// and the old Conn can no longer send
	if err := c.Send("x"); !errors.Is(err, ErrClosed) {
		t.Fatalf("old conn still sends: %v", err)
	}
	// ending is still reported by the new one
	srv.CloseConn()
	if ev := ended(t, n); ev.Reason == "" {
		t.Fatalf("%+v", ev)
	}
}

// A handoff in the middle of an MCCP2 compressed stream.
func TestHandoffMidMCCP(t *testing.T) {
	srv := fakemush.New(t, fakemush.WithGreeting("hi\r\n"))
	cfg := cfgFor(srv)
	c := dialT(t, cfg)
	plainLine(t, c)
	srv.Will(telnet.OptMCCP2)
	srv.StartMCCP2()
	srv.SendLine("first compressed")
	if l, _ := plainLine(t, c); l != "first compressed" {
		t.Fatalf("%q", l)
	}
	n := handOver(t, c, cfg)
	srv.SendLine("second compressed")
	if l, _ := plainLine(t, n); l != "second compressed" {
		t.Fatalf("compression state lost: %q", l)
	}
}

// A TLS connection cannot be handed over; it is left running.
func TestHandoffRefusesTLS(t *testing.T) {
	srv := fakemush.New(t, fakemush.WithTLS(), fakemush.WithGreeting("secure\r\n"))
	cfg := cfgFor(srv)
	cfg.World.TLS = true
	cfg.TLSConfig = tlsWith(srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if l, _ := plainLine(t, c); !strings.Contains(l, "secure") {
		t.Fatalf("%q", l)
	}
	if _, _, err := c.Handoff(); !errors.Is(err, ErrNotHandoffable) {
		t.Fatalf("want ErrNotHandoffable, got %v", err)
	}
	srv.SendLine("still here")
	if l, _ := plainLine(t, c); l != "still here" {
		t.Fatalf("a refused handoff stopped the connection: %q", l)
	}
	if _, err := Adopt(cfg, HandoffState{}, nil); !errors.Is(err, ErrNotHandoffable) {
		t.Fatalf("adopt tls: %v", err)
	}
}
