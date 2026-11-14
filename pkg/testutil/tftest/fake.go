// Package tftest is a fake TinyFugue for tests: it speaks TFCTL to the real
// tf-squatterd bridge (internal/tfbridge) the way squat.c does, and Start wraps
// a bridge, a TLS wire server and a Fake into one simulator, so a client can be
// tested against the real bridge and protocol without TinyFugue.
package tftest

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/tfbridge"
)

// Pair returns the two ends of a local stream connection: give the first to
// New (the fake TinyFugue) and the second to tfbridge.New.
func Pair(t testing.TB) (tfEnd, bridgeEnd net.Conn) {
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

type sess struct {
	world, host, port, state string
	input, automation        string
	started                  int64
}

// Fake is the TinyFugue end. It answers the bridge's requests like squat.c.
type Fake struct {
	t    testing.TB
	conn net.Conn
	wmu  sync.Mutex

	mu     sync.Mutex
	cond   *sync.Cond
	got    [][]string
	worlds map[string][]string // name -> WORLD fields (without the leading type)
	sess   map[string]*sess
	// Ask, when true, makes CTRL_SET wait for Answer instead of applying at once
	// (TinyFugue's squat_takeover=ask).
	Ask    bool
	asks   []ask
	ExecOn bool
	// ScriptsOn: TinyFugue accepts script uploads (squat_scripts). Set before use.
	ScriptsOn bool
	loaded    []string
	unnamed   int
}

type ask struct{ m []string }

// New starts a Fake on conn. It does not send HELLO: call Hello.
func New(t testing.TB, conn net.Conn) *Fake {
	f := &Fake{t: t, conn: conn, worlds: map[string][]string{}, sess: map[string]*sess{}, ExecOn: true, ScriptsOn: true}
	f.cond = sync.NewCond(&f.mu)
	go f.readLoop()
	t.Cleanup(func() { conn.Close() })
	return f
}

// Hello announces TinyFugue (features as squat.c would).
func (f *Fake) Hello(instance, features string) {
	f.Send("HELLO", "1", "tf", instance, "4242", features)
}

// Send writes one TFCTL message to the bridge.
func (f *Fake) Send(fields ...string) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	_, _ = f.conn.Write(tfbridge.Encode(fields...))
}

// AddSession announces a connected game session and returns its id.
func (f *Fake) AddSession(sid, world, host, port string) {
	f.mu.Lock()
	f.sess[sid] = &sess{world: world, host: host, port: port, state: "connected", input: "tf", automation: "tf", started: time.Now().UnixMilli()}
	f.mu.Unlock()
	f.Send("SESS", sid, world, host, port, "0", "connected", "0", "1", strconv.FormatInt(time.Now().UnixMilli(), 10), "")
	f.Send("CTRL", sid, "tf", "tf", "")
}

// RenameSessionWorld announces the new world name of every session of the old one.
func (f *Fake) RenameSessionWorld(oldName, newName string) {
	f.mu.Lock()
	type upd struct {
		sid string
		s   sess
	}
	var ups []upd
	for sid, ss := range f.sess {
		if ss.world == oldName {
			ss.world = newName
			ups = append(ups, upd{sid, *ss})
		}
	}
	f.mu.Unlock()
	for _, u := range ups {
		f.Send("SESS", u.sid, u.s.world, u.s.host, u.s.port, "0", u.s.state, "0", "1", strconv.FormatInt(u.s.started, 10), "")
	}
}

// WorldsChanged tells the bridge the world list changed in TinyFugue (a change made at its keyboard).
func (f *Fake) WorldsChanged() { f.Send("WCHG") }

// Line sends one LINE (kind g/e/s, flags gag=g prompt=p).
func (f *Fake) Line(sid, kind, flags, origin, text string, disp ...string) {
	m := []string{"LINE", sid, kind, flags, origin, strconv.FormatInt(time.Now().UnixMilli(), 10), text}
	m = append(m, disp...)
	f.Send(m...)
}

// AddWorld stores a world record that WORLDS will list.
func (f *Fake) AddWorld(name, host, port string) {
	f.mu.Lock()
	f.worlds[name] = []string{name, host, port, "tiny", "", "0", "0", "0", "UTF-8", "", "0"}
	f.mu.Unlock()
}

// Messages returns everything the bridge sent so far.
func (f *Fake) Messages() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.got...)
}

// Count returns how many messages of a type the bridge sent.
func (f *Fake) Count(typ string) int {
	n := 0
	for _, m := range f.Messages() {
		if m[0] == typ {
			n++
		}
	}
	return n
}

// Wait returns the first message of typ at or after index from, and the next index.
func (f *Fake) Wait(typ string, from int) ([]string, int) {
	f.t.Helper()
	end := time.Now().Add(8 * time.Second)
	f.mu.Lock()
	defer f.mu.Unlock()
	for {
		for i := from; i < len(f.got); i++ {
			if f.got[i][0] == typ {
				return f.got[i], i + 1
			}
		}
		if time.Now().After(end) {
			f.t.Fatalf("timed out waiting for %s from the bridge; got %q", typ, f.got)
		}
		go func() { time.Sleep(40 * time.Millisecond); f.cond.Broadcast() }()
		f.cond.Wait()
	}
}

// SendTexts returns the text of every SEND the bridge forwarded.
func (f *Fake) SendTexts() []string {
	var out []string
	for _, m := range f.Messages() {
		if m[0] == "SEND" && len(m) >= 6 {
			out = append(out, m[5])
		}
	}
	return out
}

// Answer resolves the oldest waiting takeover request (Ask mode).
func (f *Fake) Answer(allow bool) {
	f.mu.Lock()
	if len(f.asks) == 0 {
		f.mu.Unlock()
		f.t.Fatal("no takeover request is waiting")
	}
	a := f.asks[0]
	f.asks = f.asks[1:]
	f.mu.Unlock()
	if allow {
		f.applyCtrl(a.m)
	} else {
		f.Send("ERR", a.m[1], "denied", "the TinyFugue user said no")
	}
}

func (f *Fake) readLoop() {
	sc := bufio.NewScanner(f.conn)
	sc.Buffer(make([]byte, 0, 64<<10), tfbridge.MaxCtlLine)
	for sc.Scan() {
		m := tfbridge.Decode(sc.Text())
		f.mu.Lock()
		f.got = append(f.got, m)
		f.cond.Broadcast()
		f.mu.Unlock()
		f.respond(m)
	}
}

func (f *Fake) applyCtrl(m []string) {
	// CTRL_SET reqid sid axis mode device
	f.mu.Lock()
	targets := []string{}
	if m[2] == "*" {
		for id := range f.sess {
			targets = append(targets, id)
		}
	} else if _, ok := f.sess[m[2]]; ok {
		targets = []string{m[2]}
	}
	f.mu.Unlock()
	for _, id := range targets {
		f.mu.Lock()
		s := f.sess[id]
		if m[3] == "input" {
			s.input = m[4]
		} else {
			s.automation = m[4]
		}
		in, au := s.input, s.automation
		f.mu.Unlock()
		f.Send("CTRL", id, in, au, m[5])
	}
	f.Send("OK", m[1])
}

func (f *Fake) respond(m []string) {
	switch m[0] {
	case "SEND":
		// SEND reqid sid device noecho text
		f.mu.Lock()
		s := f.sess[m[2]]
		f.mu.Unlock()
		if s == nil {
			f.Send("ERR", m[1], "no_such_session", "no such session")
		} else if s.input == "tf" {
			f.Send("ERR", m[1], "not_controller", "TinyFugue has control of this session")
		} else {
			f.Send("OK", m[1])
		}
	case "CTRL_SET":
		isTakeover := (m[3] == "input" && m[4] != "tf") || (m[3] == "automation" && m[4] == "client")
		if f.Ask && isTakeover {
			f.mu.Lock()
			f.asks = append(f.asks, ask{m})
			f.mu.Unlock()
			f.Send("ASK", m[2], m[3], m[4], m[5])
			return
		}
		f.applyCtrl(m)
	case "WORLDS":
		f.mu.Lock()
		names := make([]string, 0, len(f.worlds))
		for n := range f.worlds {
			names = append(names, n)
		}
		f.mu.Unlock()
		for _, n := range names {
			f.mu.Lock()
			w := f.worlds[n]
			f.mu.Unlock()
			f.Send(append([]string{"WORLD"}, w...)...)
		}
		f.Send("OK", m[1])
	case "WORLD_PUT":
		// WORLD_PUT reqid name host port type char passmode pass ssl echo charset mfile mfilemode save
		pass := "0"
		if m[7] == "set" {
			pass = "1"
		}
		f.mu.Lock()
		_, existed := f.worlds[m[2]]
		if m[7] == "keep" && existed {
			pass = f.worlds[m[2]][5]
		}
		f.worlds[m[2]] = []string{m[2], m[3], m[4], m[5], m[6], pass, m[9], m[10], m[11], m[12], "0"}
		f.mu.Unlock()
		if existed {
			f.Send("OK", m[1], "updated")
		} else {
			f.Send("OK", m[1], "created")
		}
	case "WORLD_REN":
		// WORLD_REN reqid old new save
		f.mu.Lock()
		w, ok := f.worlds[m[2]]
		_, clash := f.worlds[m[3]]
		if ok && !clash {
			delete(f.worlds, m[2])
			w[0] = m[3]
			w[len(w)-1] = "0" // a temporary world becomes a kept one
			f.worlds[m[3]] = w
		}
		f.mu.Unlock()
		switch {
		case !ok:
			f.Send("ERR", m[1], "no_such_world", "no such world")
		case clash:
			f.Send("ERR", m[1], "bad_request", "a world named "+m[3]+" already exists")
		default:
			f.RenameSessionWorld(m[2], m[3]) // like TinyFugue: sessions of the world carry the new name
			f.Send("OK", m[1])
			f.Send("WCHG")
		}
	case "WORLD_DEL":
		f.mu.Lock()
		_, ok := f.worlds[m[2]]
		delete(f.worlds, m[2])
		f.mu.Unlock()
		if ok {
			f.Send("OK", m[1])
		} else {
			f.Send("ERR", m[1], "no_such_world", "no such world")
		}
	case "CONNECT":
		// CONNECT reqid world host port ssl
		world, host, port := m[2], m[3], m[4]
		if world != "" {
			f.mu.Lock()
			w, ok := f.worlds[world]
			f.mu.Unlock()
			if !ok {
				f.Send("ERR", m[1], "connect_failed", "no such world")
				return
			}
			host, port = w[1], w[2]
		} else {
			// like TinyFugue: a connection by address makes a temporary world "(unnamedN)"
			f.mu.Lock()
			f.unnamed++
			world = "(unnamed" + strconv.Itoa(f.unnamed) + ")"
			f.worlds[world] = []string{world, host, port, "", "", "0", "0", "0", "", "", "1"}
			f.mu.Unlock()
		}
		sid := "w-" + strings.ReplaceAll(world, " ", "_") + "-1-" + strconv.Itoa(len(f.Messages()))
		f.AddSession(sid, world, host, port)
		f.Send("OK", m[1], sid)
	case "DISC", "DISMISS":
		f.Send("OK", m[1])
	case "EXEC":
		if !f.ExecOn {
			f.Send("ERR", m[1], "forbidden", "remote commands are off")
			return
		}
		f.Send("OK", m[1], "1", "ran: "+m[2])
	case "SCRIPT":
		if !f.ScriptsOn {
			f.Send("ERR", m[1], "forbidden", "script upload is off")
			return
		}
		if len(m) > 2 && m[2] == "load" {
			f.mu.Lock()
			f.loaded = append(f.loaded, m[3])
			f.mu.Unlock()
			f.Send("OK", m[1], "1", "% Loading commands from "+m[3]+".")
			return
		}
		f.Send("OK", m[1])
	case "GMCP_SEND", "MSDP_SEND":
		f.Send("OK", m[1])
	}
}

// LoadedScripts returns the script paths TinyFugue was asked to load.
func (f *Fake) LoadedScripts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.loaded...)
}
