// Command gmcpdump connects to a MUD with the squatterd's own connection code, optionally logs in and
// sends a few commands, and prints what arrives: text lines, GMCP and MSDP. With -out it also writes the
// events as JSON lines (a fixture for tests and for a UI's replay mode).
//
//	gmcpdump -addr 127.0.0.1:4392 -login 'connect USER PASSWORD' -send 'look;hurt 20;say hi' -for 20s -out fixture.jsonl
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/ansi"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/conn"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4392", "host:port")
	login := flag.String("login", "", "line to send after the greeting (e.g. 'connect god pw')")
	send := flag.String("send", "", "semicolon-separated commands to send, one second apart, after login")
	dur := flag.Duration("for", 15*time.Second, "how long to listen")
	out := flag.String("out", "", "write events as JSON lines to this file")
	flag.Parse()
	host, port := *addr, 0
	if i := strings.LastIndex(*addr, ":"); i >= 0 {
		host = (*addr)[:i]
		fmt.Sscan((*addr)[i+1:], &port)
	}
	cfg := conn.Config{World: proto.World{Name: "gmcpdump", Host: host, Port: port, Encoding: "utf-8"}, ClientName: "gmcpdump"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	c, err := conn.Dial(ctx, cfg)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer c.Close()
	var f *os.File
	if *out != "" {
		if f, err = os.Create(*out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer f.Close()
	}
	start := time.Now()
	rec := func(v map[string]any) {
		v["t"] = time.Since(start).Milliseconds()
		if f != nil {
			b, _ := json.Marshal(v)
			fmt.Fprintln(f, string(b))
		}
	}
	go func() {
		time.Sleep(1500 * time.Millisecond)
		if *login != "" {
			_ = c.Send(*login)
			time.Sleep(2 * time.Second)
		}
		for _, cmd := range strings.Split(*send, ";") {
			if cmd = strings.TrimSpace(cmd); cmd != "" {
				_ = c.Send(cmd)
				time.Sleep(1200 * time.Millisecond)
			}
		}
	}()
	deadline := time.After(*dur)
	for {
		select {
		case ev, ok := <-c.Events():
			if !ok {
				return
			}
			switch ev.Kind {
			case conn.EvLine:
				txt := ansi.PlainText(ev.Spans)
				fmt.Printf("TEXT  %s\n", txt)
				rec(map[string]any{"kind": "line", "text": txt})
			case conn.EvGmcp:
				fmt.Printf("GMCP  %s %s\n", ev.Package, string(ev.Data))
				var d any
				_ = json.Unmarshal(ev.Data, &d)
				rec(map[string]any{"kind": "gmcp", "package": ev.Package, "data": d})
			case conn.EvEnded:
				fmt.Printf("ENDED %s %s\n", ev.Reason, ev.Detail)
				return
			}
		case <-deadline:
			return
		}
	}
}
