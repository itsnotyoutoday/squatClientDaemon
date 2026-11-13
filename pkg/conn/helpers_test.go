package conn

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

func cfgFor(s *fakemush.Server) Config {
	return Config{World: proto.World{Name: "t", Host: s.Host(), Port: s.Port(), Encoding: "utf-8"}}
}

func dialT(t *testing.T, cfg Config) *Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, cfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

func next(t *testing.T, c *Conn) Event {
	t.Helper()
	select {
	case ev, ok := <-c.Events():
		if !ok {
			t.Fatal("events closed")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for event")
	}
	return Event{}
}

// nextKind skips events of other kinds.
func nextKind(t *testing.T, c *Conn, k EventKind) Event {
	t.Helper()
	for {
		if ev := next(t, c); ev.Kind == k {
			return ev
		}
	}
}

func spansJSON(sp []proto.Span) string {
	b, _ := json.Marshal(sp)
	return string(b)
}

func expectLine(t *testing.T, c *Conn, wantJSON string, prompt bool) {
	t.Helper()
	ev := nextKind(t, c, EvLine)
	if got := spansJSON(ev.Spans); got != wantJSON || ev.Prompt != prompt {
		t.Fatalf("line mismatch (prompt=%v want %v)\n got  %s\n want %s", ev.Prompt, prompt, got, wantJSON)
	}
}

func ended(t *testing.T, c *Conn) Event {
	t.Helper()
	for {
		ev := next(t, c)
		if ev.Kind == EvEnded {
			if _, ok := <-c.Events(); ok {
				t.Fatal("events not closed after Ended")
			}
			return ev
		}
	}
}

func tlsWith(s *fakemush.Server) *tls.Config {
	return &tls.Config{RootCAs: s.CertPool()}
}
