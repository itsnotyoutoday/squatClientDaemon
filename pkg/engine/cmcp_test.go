package engine_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cs "github.com/itsnotyoutoday/cmcp/session"

	"github.com/itsnotyoutoday/cmcp/frame"
	"github.com/itsnotyoutoday/cmcp/msg"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/conn"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// cmcpGame is a scripted server backend for the integration test (the core's session.Server does the protocol).
type cmcpGame struct {
	mu       sync.Mutex
	executed []msg.Cmd
}

func (g *cmcpGame) ShipEpoch() (int, bool, bool) { return 1, true, false }
func (g *cmcpGame) ConsoleFor() (string, bool)   { return "operations", false }
func (g *cmcpGame) Permitted(string) bool        { return true }
func (g *cmcpGame) CanSubscribe(string) bool     { return true }
func (g *cmcpGame) Execute(c *msg.Cmd) msg.Reply {
	g.mu.Lock()
	g.executed = append(g.executed, *c)
	g.mu.Unlock()
	return msg.Reply{ID: c.ID, OK: true}
}
func (g *cmcpGame) Snapshot(pkg string) (any, bool) {
	switch pkg {
	case "Space.Power":
		return map[string]any{"main": 5.4}, true
	case "Space.Contacts":
		return map[string]any{"contacts": map[string]any{"c1": map[string]any{"name": "X"}}}, true
	}
	return nil, false
}

// fakeTr is a Transport that is also a frame sender: the engine writes frames into the server endpoint, the
// server's answers come back as GMCP events.
type fakeTr struct {
	ev   chan conn.Event
	onTx func(raw []byte)
	mu   sync.Mutex
	shut bool
}

func (f *fakeTr) deliver(ev conn.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.shut {
		f.ev <- ev
	}
}

func (f *fakeTr) Events() <-chan conn.Event { return f.ev }
func (f *fakeTr) Send(string) error         { return nil }
func (f *fakeTr) Resize(int, int)           {}
func (f *fakeTr) SetCaps(proto.Caps)        {}
func (f *fakeTr) RemoteEcho() bool          { return false }
func (f *fakeTr) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.shut {
		f.shut = true
		close(f.ev)
	}
}
func (f *fakeTr) SendFrame(b []byte) error { f.onTx(b); return nil }

func TestCmcpEndToEnd(t *testing.T) {
	game := &cmcpGame{}
	one := false
	srv := cs.NewServer(cs.ServerConfig{Server: "test", Backend: game,
		Packages: []string{"Space.Power", "Space.Contacts", "Space.Ship.Status"},
		Commands: []msg.CommandSpec{{Name: "dmg.assign", Consoles: []string{"operations"}, Mutating: true, Text: "dmg assign {team}={system}",
			Args: map[string]msg.ArgSpec{"team": {Type: "int"}, "system": {Type: "string"}}}}})
	var trp atomic.Pointer[fakeTr]
	var smu sync.Mutex
	push := func() { // server output -> client
		for _, o := range srv.Drain().Frames {
			b, _ := json.Marshal(o.Payload)
			trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: o.Package, Data: b})
		}
	}
	e, r := newEngine(t, func(o *engine.Options) {
		o.Dialer = func(ctx context.Context, cfg conn.Config) (engine.Transport, error) {
			tr := &fakeTr{ev: make(chan conn.Event, 64)}
			trp.Store(tr)
			tr.onTx = func(raw []byte) {
				smu.Lock()
				defer smu.Unlock()
				for _, res := range frame.Split(raw, 65536) {
					if res.OK() {
						srv.Receive(time.Now(), res.Frame)
					}
				}
				push()
			}
			return tr, nil
		}
	})
	e.SetCmcp(func() cs.Config {
		return cs.Config{Name: "SquatClient", Version: "test", Kind: "gui", Render: &msg.Render{Kind: "gpu"}}
	})
	si, err := e.Dial(engine.World{ID: "w", Name: "w", Host: "127.0.0.1", Port: 1}, "")
	if err != nil {
		t.Fatal(err)
	}
	// the server offers: DO GMCP, then Cobra.Server
	smu.Lock()
	srv.TelnetEvent(cs.DoGMCP)
	srv.Receive(time.Now(), frame.Frame{Package: "Core.Supports.Set", Payload: json.RawMessage(`["Cobra 1"]`)})
	push()
	smu.Unlock()
	_ = one

	waitUI := func(what string, pred func(*engine.CmcpUI) bool) *engine.CmcpUI {
		t.Helper()
		var ui *engine.CmcpUI
		r.until(t, what, func() bool {
			u, err := e.CmcpUI(si.ID)
			ui = u
			return err == nil && pred(u)
		})
		return ui
	}
	waitUI("ready", func(u *engine.CmcpUI) bool { return u.State == "ready" })

	smu.Lock()
	srv.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1, Name: "Test"}, Console: &msg.ConsoleRef{ID: "operations", Name: "Ops"},
		Screen: "std.operations", Reason: "manned", Subscribe: []msg.SubSpec{{Pkg: "Space.Power"}}, Commands: []string{"dmg.assign"}})
	srv.SendSnapshots("Space.Power")
	push()
	smu.Unlock()
	ui := waitUI("console+data", func(u *engine.CmcpUI) bool { return u.Console != nil && u.Docs["Space.Power"] != nil })
	if ui.Screen.Resolved != "std.operations" || ui.Ship.ID != "s1" {
		t.Fatalf("screen %+v ship %+v", ui.Screen, ui.Ship)
	}
	// the UI event is published (throttled) and carries the model
	r.until(t, "EvCmcp", func() bool {
		for _, ev := range r.snapshot() {
			if ev.Kind == engine.EvCmcp && ev.Cmcp.Console != nil {
				return true
			}
		}
		return false
	})

	// reference-counted subscription: two owners, one Subscribe; the package leaves when the last owner does
	if err := e.CmcpAcquire(si.ID, "a", []msg.SubSpec{{Pkg: "Space.Contacts"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.CmcpAcquire(si.ID, "b", []msg.SubSpec{{Pkg: "Space.Contacts"}}); err != nil {
		t.Fatal(err)
	}
	smu.Lock()
	srv.SendSnapshots("Space.Contacts")
	push()
	smu.Unlock()
	waitUI("contacts doc", func(u *engine.CmcpUI) bool { return u.Docs["Space.Contacts"] != nil })
	_ = e.CmcpRelease(si.ID, "a")
	if u, _ := e.CmcpUI(si.ID); u.Docs["Space.Contacts"] == nil {
		t.Fatal("package dropped while another owner still needs it")
	}
	_ = e.CmcpRelease(si.ID, "b")
	waitUI("contacts gone", func(u *engine.CmcpUI) bool { return u.Docs["Space.Contacts"] == nil })

	// a click -> Cobra.Cmd, echoed as text, answered
	tk, err := e.CmcpCommand(si.ID, "dmg.assign", map[string]any{"team": 3, "system": "Port_Nacelle"})
	if err != nil || tk.Echo != "dmg assign 3=Port_Nacelle" {
		t.Fatalf("ticket %+v err %v", tk, err)
	}
	r.waitLine(t, si.ID, "cmcp> dmg assign 3=Port_Nacelle")
	r.until(t, "server executed the command", func() bool {
		game.mu.Lock()
		defer game.mu.Unlock()
		return len(game.executed) == 1
	})
	r.until(t, "reply note", func() bool {
		for _, ev := range r.snapshot() {
			for _, n := range ev.CmcpNotes {
				if n.Kind == "reply" && n.Reply.OK {
					return true
				}
			}
		}
		return false
	})
	// a verb outside the console is refused locally and never sent
	if _, err := e.CmcpCommand(si.ID, "tractor.lock", nil); err == nil {
		t.Fatal("unknown verb was accepted")
	}
	// a session that never spoke CMCP reports it
	if _, err := e.CmcpUI("nope"); err == nil {
		t.Fatal("expected an error for an unknown session")
	}
}
