package main

import (
	"bytes"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/msdp"
)

// mockMSDP is a tiny game that speaks MSDP: it offers the option, answers LIST
// and SEND, and for REPORT sends the variable at once and again whenever it
// changes (HEALTH counts down every 300 ms). It exists because real servers
// implement MSDP only partly.
type mockMSDP struct {
	ln   net.Listener
	port int
}

const (
	iac   = 255
	dont  = 254
	doo   = 253
	wont  = 252
	will  = 251
	sb    = 250
	se    = 240
	tMSDP = 69
)

func startMockMSDP() (*mockMSDP, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	m := &mockMSDP{ln: ln, port: ln.Addr().(*net.TCPAddr).Port}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go m.serve(c)
		}
	}()
	return m, nil
}

func (m *mockMSDP) stop() { _ = m.ln.Close() }

func frame(payload []byte) []byte {
	return append(append([]byte{iac, sb, tMSDP}, payload...), iac, se)
}

func (m *mockMSDP) serve(c net.Conn) {
	defer c.Close()
	var wmu sync.Mutex
	send := func(b []byte) { wmu.Lock(); defer wmu.Unlock(); _, _ = c.Write(b) }
	sendVars := func(ps []msdp.Pair) {
		if p, err := msdp.Encode(ps); err == nil {
			send(frame(p))
		}
	}
	room := map[string]any{"NAME": "Café ☕", "VNUM": "42", "EXITS": []any{"n", "s"}}
	var mu sync.Mutex
	health := 100
	reported := map[string]bool{}
	stop := make(chan struct{})
	defer close(stop)

	send([]byte{iac, will, tMSDP})
	send([]byte("Mock MSDP game\r\n"))

	go func() {
		t := time.NewTicker(300 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				mu.Lock()
				health--
				if reported["HEALTH"] {
					sendVars([]msdp.Pair{{Name: "HEALTH", Value: fmt.Sprint(health)}})
				}
				mu.Unlock()
			}
		}
	}()

	names := func(v any) []string {
		switch x := v.(type) {
		case string:
			return []string{x}
		case []any:
			var out []string
			for _, e := range x {
				if s, ok := e.(string); ok {
					out = append(out, s)
				}
			}
			return out
		}
		return nil
	}
	current := func(n string) (any, bool) {
		mu.Lock()
		defer mu.Unlock()
		switch n {
		case "HEALTH":
			return fmt.Sprint(health), true
		case "ROOM":
			return room, true
		case "NAME":
			return "Zoë", true
		}
		return nil, false
	}

	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := c.Read(tmp)
		if err != nil {
			return
		}
		buf = append(buf, tmp[:n]...)
		for {
			i := bytes.Index(buf, []byte{iac, sb, tMSDP})
			if i < 0 {
				break
			}
			j := bytes.Index(buf[i:], []byte{iac, se})
			if j < 0 {
				break
			}
			payload := append([]byte(nil), buf[i+3:i+j]...)
			buf = buf[i+j+2:]
			ps, err := msdp.Decode(payload)
			if err != nil {
				continue
			}
			for _, p := range ps {
				switch p.Name {
				case "LIST":
					switch fmt.Sprint(p.Value) {
					case "COMMANDS":
						sendVars([]msdp.Pair{{Name: "COMMANDS", Value: []any{"LIST", "REPORT", "SEND", "UNREPORT"}}})
					case "REPORTABLE_VARIABLES", "SENDABLE_VARIABLES":
						sendVars([]msdp.Pair{{Name: fmt.Sprint(p.Value), Value: []any{"HEALTH", "ROOM", "NAME"}}})
					}
				case "SEND":
					for _, n := range names(p.Value) {
						if v, ok := current(n); ok {
							sendVars([]msdp.Pair{{Name: n, Value: v}})
						}
					}
				case "REPORT":
					for _, n := range names(p.Value) {
						if v, ok := current(n); ok {
							mu.Lock()
							reported[n] = true
							mu.Unlock()
							sendVars([]msdp.Pair{{Name: n, Value: v}})
						}
					}
				case "UNREPORT":
					mu.Lock()
					for _, n := range names(p.Value) {
						delete(reported, n)
					}
					mu.Unlock()
				}
			}
		}
	}
}
