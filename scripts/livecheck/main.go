// Command livecheck drives a running squatterd through the SWP client library:
// pair, open a real session, receive the banner, detach, re-attach with since,
// verify nothing is duplicated, disconnect. Used by scripts/verify-live.sh.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

func die(f string, a ...any) { fmt.Fprintf(os.Stderr, "FAIL: "+f+"\n", a...); os.Exit(1) }

func main() {
	url := flag.String("url", "", "swp url")
	code := flag.String("code", "", "pairing code")
	fp := flag.String("fp", "", "pinned TLS fingerprint")
	host := flag.String("host", "startrekkin.net", "game host")
	port := flag.Int("port", 1701, "game port")
	flag.Parse()

	key, _ := auth.GenerateKey()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c, err := wire.Dial(ctx, *url, wire.ClientOptions{Key: key, PairCode: *code, DeviceName: "livecheck", Caps: proto.DefaultCaps(), PinnedFingerprint: *fp})
	if err != nil {
		die("dial/pair: %v", err)
	}
	defer c.Close()
	fmt.Printf("paired: device %s, server %s, fingerprint %s\n", c.DeviceID(), c.ServerName(), c.ServerFingerprint())

	s, err := c.Connect(ctx, proto.World{Name: "live", Host: *host, Port: *port, Encoding: "utf-8"})
	if err != nil {
		die("connect: %v", err)
	}
	fmt.Printf("session %s created (state %s)\n", s.ID, s.State)
	if err := c.Attach(ctx, s.ID, nil, 0); err != nil {
		die("attach: %v", err)
	}

	// Collect until the banner has arrived and the stream went quiet for 4 s.
	banner := 0
	hard := time.After(45 * time.Second)
	quiet := time.NewTimer(time.Hour)
collect:
	for {
		select {
		case ev := <-c.Events():
			switch e := ev.(type) {
			case wire.SessionState:
				fmt.Printf("state: %s endReason=%q %s\n", e.Info.State, e.Info.EndReason, e.Info.EndDetail)
				if e.Info.State == proto.StateEnded {
					die("session ended early: %s %s", e.Info.EndReason, e.Info.EndDetail)
				}
			case wire.Backlog:
				for _, en := range e.Entries {
					if en.Line != nil {
						fmt.Printf("backlog seq=%d kind=%s plain=%q\n", en.Line.Seq, en.Line.Kind, en.Line.Plain())
					}
				}
			case wire.Line:
				b, _ := json.Marshal(e.Spans)
				fmt.Printf("line seq=%d kind=%s prompt=%v plain=%q\n      spans=%s\n", e.Seq, e.Kind, e.Prompt, e.Plain(), b)
				if e.Kind == proto.KindGame {
					banner++
					quiet.Reset(4 * time.Second)
				}
			case wire.Error:
				die("daemon error: %s %s", e.Code, e.Message)
			}
		case <-quiet.C:
			break collect
		case <-hard:
			break collect
		}
	}
	if banner == 0 {
		die("no banner received")
	}
	last := c.LastSeq(s.ID)
	fmt.Printf("banner lines received: %d, lastSeq=%d\n", banner, last)

	if err := c.Detach(ctx, s.ID); err != nil {
		die("detach: %v", err)
	}
	if err := c.Attach(ctx, s.ID, &last, 0); err != nil {
		die("re-attach: %v", err)
	}
	got, dups := 0, 0
	timeout := time.After(5 * time.Second)
	for done := false; !done; {
		select {
		case ev := <-c.Events():
			switch e := ev.(type) {
			case wire.Attached:
				fmt.Printf("re-attached: firstSeq=%d lastSeq=%d replayFrom=%d gap=%v\n", e.FirstSeq, e.LastSeq, e.ReplayFrom, e.Gap)
			case wire.Backlog:
				got += len(e.Entries)
				for _, en := range e.Entries {
					if en.Seq() <= last {
						dups++
					}
				}
				done = e.Done
			}
		case <-timeout:
			die("no backlog after re-attach")
		}
	}
	fmt.Printf("replay after since=%d: %d entries, duplicates: %d\n", last, got, dups)
	if dups != 0 || got != 0 {
		die("expected an empty replay with no duplicates (got=%d dups=%d)", got, dups)
	}

	if err := c.Disconnect(ctx, s.ID); err != nil {
		die("disconnect: %v", err)
	}
	for {
		select {
		case ev := <-c.Events():
			if st, ok := ev.(wire.SessionState); ok && st.Info.State == proto.StateEnded {
				fmt.Printf("disconnected: state=%s endReason=%s\n", st.Info.State, st.Info.EndReason)
				if st.Info.EndReason != proto.EndClientQuit {
					die("expected client_quit")
				}
				fmt.Println("OK")
				return
			}
		case <-time.After(10 * time.Second):
			die("no ended state after disconnect")
		}
	}
}
