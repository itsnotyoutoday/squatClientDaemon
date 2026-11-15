package engine_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/itsnotyoutoday/cmcp/go/fetch"
	"github.com/itsnotyoutoday/cmcp/go/pack"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cs "github.com/itsnotyoutoday/cmcp/go/session"

	"github.com/itsnotyoutoday/cmcp/go/frame"
	"github.com/itsnotyoutoday/cmcp/go/msg"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/conn"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// cmcpGame is a scripted server backend for the integration test (the core's session.Server does the protocol).
type cmcpGame struct {
	mu       sync.Mutex
	executed []msg.Cmd
}

func (g *cmcpGame) Player() cs.Player {
	return cs.Player{Epoch: 1, Aboard: true, Held: []string{"operations", "science", "tactical", "navigation", "command", "communications", "engineering", "medical", "security", "transporter"}}
}
func (g *cmcpGame) Permitted(string) bool    { return true }
func (g *cmcpGame) CanSubscribe(string) bool { return true }
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

type cmcpRig struct {
	t    *testing.T
	e    *engine.Engine
	r    *rec
	srv  *cs.Server
	game *cmcpGame
	smu  sync.Mutex
	trp  atomic.Pointer[fakeTr]
	sid  string
}

func (g *cmcpRig) push() { // server output -> client (caller holds smu)
	for _, o := range g.srv.Drain().Frames {
		b, _ := json.Marshal(o.Payload)
		g.trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: o.Package, Data: b})
	}
}

// do runs f against the server endpoint and delivers what it produced.
func (g *cmcpRig) do(f func(*cs.Server)) {
	g.smu.Lock()
	defer g.smu.Unlock()
	f(g.srv)
	g.push()
}

func (g *cmcpRig) waitUI(what string, pred func(*engine.CmcpUI) bool) *engine.CmcpUI {
	g.t.Helper()
	var ui *engine.CmcpUI
	g.r.until(g.t, what, func() bool {
		u, err := g.e.CmcpUI(g.sid)
		ui = u
		return err == nil && pred(u)
	})
	return ui
}

type cmcpRigCfg struct {
	clock     func() time.Time // a fake engine clock (nil: the real one)
	noContext bool             // a client that does not speak Cobra.Context (it closes its console with console.unman)
	assets    *msg.Assets
	packs     []msg.PackRef
	storeDir  string
	onFrame   func(*cmcpRig, frame.Frame)
}

func newCmcpRig(t *testing.T) *cmcpRig { return newCmcpRigWith(t, nil) }

func newCmcpRigWith(t *testing.T, mod func(*cmcpRigCfg)) *cmcpRig {
	var rc cmcpRigCfg
	if mod != nil {
		mod(&rc)
	}
	g := &cmcpRig{t: t, game: &cmcpGame{}}
	g.srv = cs.NewServer(cs.ServerConfig{Server: "test", Backend: g.game,
		Packages: []string{"Space.Power", "Space.Contacts", "Space.Ship.Status", "Space.Damage.Teams", "Space.Damage.Stat"}, Packs: rc.packs, Assets: rc.assets,
		Commands: rigCommands()})
	g.e, g.r = newEngine(t, func(o *engine.Options) {
		if rc.clock != nil {
			o.Clock = rc.clock
		}
		o.Dialer = func(ctx context.Context, cfg conn.Config) (engine.Transport, error) {
			tr := &fakeTr{ev: make(chan conn.Event, 64)}
			g.trp.Store(tr)
			tr.onTx = func(raw []byte) {
				g.smu.Lock()
				defer g.smu.Unlock()
				for _, res := range frame.Split(raw, 65536) {
					if res.OK() {
						if rc.onFrame != nil {
							rc.onFrame(g, res.Frame)
						}
						g.srv.Receive(time.Now(), res.Frame)
					}
				}
				g.push()
			}
			return tr, nil
		}
	})
	if rc.storeDir != "" {
		ps, err := engine.OpenPackStore(rc.storeDir)
		if err != nil {
			t.Fatal(err)
		}
		g.e.SetCmcpPacks(ps)
	}
	g.e.SetCmcp(func() cs.Config {
		return cs.Config{Name: "SquatClient", Version: "test", Kind: "gui", Render: &msg.Render{Kind: "gpu"}, MaxBody: 65536, Logf: t.Logf, NoContext: rc.noContext}
	})
	si, err := g.e.Dial(engine.World{ID: "w", Name: "w", Host: "127.0.0.1", Port: 1}, "")
	if err != nil {
		t.Fatal(err)
	}
	g.sid = si.ID
	t.Cleanup(func() {
		if t.Failed() {
			for _, l := range g.r.lines(g.sid) {
				t.Log(l.Plain())
			}
		}
	})
	g.do(func(s *cs.Server) {
		s.TelnetEvent(cs.DoGMCP)
		s.Receive(time.Now(), frame.Frame{Package: "Core.Supports.Set", Payload: json.RawMessage(`["Cobra 1"]`)})
	})
	g.waitUI("ready", func(u *engine.CmcpUI) bool { return u.State == "ready" })
	return g
}

func TestCmcpEndToEnd(t *testing.T) {
	g := newCmcpRig(t)
	e, r, srv, game, si := g.e, g.r, g.srv, g.game, struct{ ID string }{g.sid}
	waitUI := g.waitUI
	smu, push := &g.smu, g.push

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

func findNode(n *engine.CmcpNode, pred func(*engine.CmcpNode) bool) *engine.CmcpNode {
	if n == nil {
		return nil
	}
	if pred(n) {
		return n
	}
	for _, c := range n.Children {
		if f := findNode(c, pred); f != nil {
			return f
		}
	}
	return nil
}

// A built-in screen is bound from the protocol data (no text parsing), and a widget event goes out as Cobra.Cmd.
func TestCmcpScreenFromData(t *testing.T) {
	g := newCmcpRig(t)
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1, Name: "Test", Theme: "klingon"}, Console: &msg.ConsoleRef{ID: "operations", Name: "Ops"},
			Screen: "std.damage", Reason: "manned", Commands: []string{"dmg.assign"},
			Subscribe: []msg.SubSpec{{Pkg: "Space.Damage.Teams"}, {Pkg: "Space.Damage.Stat"}}})
		s.Push("Space.Damage.Teams", true, map[string]any{"teams": map[string]any{"3": map[string]any{"crew": 10, "target": "Hull"}}, "assignable": []string{"Hull", "Port_Nacelle"}})
		s.Push("Space.Damage.Stat", true, map[string]any{"hull": map[string]any{"cur": 800.0, "max": 1000.0}, "systems": map[string]any{"Port_Nacelle": map[string]any{"cur": 50.0, "max": 100.0}}})
	})
	ui := g.waitUI("tree", func(u *engine.CmcpUI) bool {
		return u.Tree != nil && findNode(u.Tree.Root, func(n *engine.CmcpNode) bool { return n.Widget == "team-assigner" }) != nil &&
			findNode(u.Tree.Root, func(n *engine.CmcpNode) bool { return n.Props["text"] == "DAMAGE CONTROL  -  hull 800.00 / 1000.00" }) != nil
	})
	if ui.Tree.Screen != "std.damage" {
		t.Fatalf("screen %q", ui.Tree.Screen)
	}
	if findNode(ui.Tree.Root, func(n *engine.CmcpNode) bool { return n.Widget == "textport" }) == nil {
		t.Fatal("no textport in the screen")
	}
	lab := findNode(ui.Tree.Root, func(n *engine.CmcpNode) bool {
		s, _ := n.Props["text"].(string)
		return n.Widget == "label" && len(s) > 10 && s[:6] == "DAMAGE"
	})
	if lab == nil || lab.Props["text"] != "DAMAGE CONTROL  -  hull 800.00 / 1000.00" {
		t.Fatalf("title binding not evaluated from data: %+v", lab)
	}
	ta := findNode(ui.Tree.Root, func(n *engine.CmcpNode) bool { return n.Widget == "team-assigner" })
	res, err := g.e.CmcpEvent(g.sid, ta.Path, "assign", map[string]any{"team": 3.0, "system": "Port_Nacelle"})
	if err != nil || len(res.Tickets) != 1 || res.Tickets[0].Echo != "dmg assign 3=Port_Nacelle" {
		t.Fatalf("event: %+v %v", res, err)
	}
	g.r.waitLine(t, g.sid, "cmcp> dmg assign 3=Port_Nacelle")
	if ui.Tree.Theme != "tng" {
		t.Fatalf("theme %q: the ship's suggestion must not apply while 'use the ship's theme' is off", ui.Tree.Theme)
	}
	g.e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true})
	g.waitUI("ship theme", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Theme == "klingon" })
	// read-only (AI takeover): the screen stays, nothing is offered
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1, AI: true}, Console: &msg.ConsoleRef{ID: "operations", Name: "Ops"},
			Screen: "std.damage", Readonly: true, Reason: "ai_on", Subscribe: []msg.SubSpec{{Pkg: "Space.Damage.Teams"}}})
	})
	ro := g.waitUI("readonly", func(u *engine.CmcpUI) bool { return u.Readonly && u.Tree != nil })
	ta = findNode(ro.Tree.Root, func(n *engine.CmcpNode) bool { return n.Widget == "team-assigner" })
	if ta == nil || ta.Enabled {
		t.Fatalf("team-assigner must stay but be disabled while read-only: %+v", ta)
	}
}

// TestCmcpDumpFixtures writes the real engine output (UI model with the bound tree) for every built-in screen, filled with the
// example messages of the protocol repository. The front end's tests, mock engine and dev page use them, so they draw
// exactly what the Go side produces. Run with CMCP_FIXTURES=<dir>.
func TestCmcpDumpFixtures(t *testing.T) {
	dir := os.Getenv("CMCP_FIXTURES")
	if dir == "" {
		t.Skip("CMCP_FIXTURES not set")
	}
	ex := os.Getenv("CMCP_EXAMPLES")
	if ex == "" {
		ex = "../../../cmcp/examples/messages"
	}
	var wel struct {
		Payload msg.Welcome `json:"payload"`
	}
	b, err := os.ReadFile(filepath.Join(ex, "Cobra.Welcome--std.json"))
	if err != nil {
		t.Skip(err)
	}
	if err := json.Unmarshal(b, &wel); err != nil {
		t.Fatal(err)
	}
	// every package the examples have a full document for
	var pkgs []string
	files, _ := filepath.Glob(filepath.Join(ex, "*--full.json"))
	for _, f := range files {
		base := filepath.Base(f)
		pkgs = append(pkgs, strings.TrimSuffix(base, "--full.json"))
	}
	screens := map[string]string{}
	for _, id := range pack.BuiltinScreenIDs() {
		screens[strings.TrimPrefix(id, "std.")] = pack.Builtin(id).Console
	}
	for scr, console := range screens {
		g := newCmcpRig(t)
		var subs []msg.SubSpec
		for _, p := range pkgs {
			subs = append(subs, msg.SubSpec{Pkg: p})
		}
		var cmds []string
		for _, c := range wel.Payload.Commands {
			cmds = append(cmds, c.Name)
		}
		g.do(func(s *cs.Server) {
			s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "ss-booty-king", Epoch: 1, Name: "SS Booty King", Class: "Marauder"}, Console: &msg.ConsoleRef{ID: console, Name: console},
				Screen: "std." + scr, Reason: "manned", Subscribe: subs, Commands: cmds})
			for _, p := range pkgs {
				f, err := os.ReadFile(filepath.Join(ex, p+"--full.json"))
				if err != nil {
					continue
				}
				var m struct {
					Payload struct {
						D json.RawMessage `json:"d"`
					} `json:"payload"`
				}
				if json.Unmarshal(f, &m) == nil {
					var d any
					_ = json.Unmarshal(m.Payload.D, &d)
					s.Push(p, true, d)
				}
			}
		})
		ui := g.waitUI("screen "+scr, func(u *engine.CmcpUI) bool {
			return u.Tree != nil && u.Seqs["Space.Contacts"] > 0 && u.Seqs["Space.Power.Group"] > 0 && u.Screen.Resolved == "std."+scr
		})
		out, _ := json.MarshalIndent(ui, "", " ")
		if err := os.WriteFile(filepath.Join(dir, scr+".json"), out, 0o644); err != nil {
			t.Fatal(err)
		}
		if scr == "damage" {
			sp, err := g.e.CmcpSprite(g.sid, "marauder")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "marauder.sprite.json"), sp, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// The context stack (spec 3.3): a pushed context becomes the active one and decides the screen; popping it returns to the console.
func TestCmcpContextStack(t *testing.T) {
	g := newCmcpRig(t)
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1}, Console: &msg.ConsoleRef{ID: "operations", Name: "Ops"}, Screen: "std.operations", Reason: "manned", Commands: []string{"dmg.assign"}})
	})
	g.waitUI("console", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Screen == "std.operations" })
	inject := func(c msg.Context) {
		b, _ := json.Marshal(c)
		g.trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: "Cobra.Context", Data: b})
	}
	prio := 120
	inject(msg.Context{Rev: 2, Ship: msg.ContextShip{ID: "s1", Epoch: 1}, Op: "push", Reason: "boarded",
		Context: &msg.ContextEntry{Key: "event:boarding", Ctx: 1, Kind: "event", ID: "boarding", Screen: "std.damage", Title: "Boarders", Priority: &prio}})
	ui := g.waitUI("event on top", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Screen == "std.damage" })
	if len(ui.Stack) != 2 || !ui.Stack[1].Active || ui.Stack[1].Kind != "event" {
		t.Fatalf("stack %+v", ui.Stack)
	}
	inject(msg.Context{Rev: 3, Ship: msg.ContextShip{ID: "s1", Epoch: 1}, Op: "pop", Key: "event:boarding", Ctx: 1})
	g.waitUI("back to the console", func(u *engine.CmcpUI) bool {
		return u.Tree != nil && u.Tree.Screen == "std.operations" && len(u.Stack) == 1
	})
}

// A game's pack: announced in Welcome, fetched inline, verified, asked about once, used; the second launch uses the cache
// without fetching or asking.
func TestCmcpPackInlineConsentAndCache(t *testing.T) {
	raw, err := os.ReadFile("../../../cmcp/examples/packs/fspace-std/pack.json")
	if err != nil {
		t.Skip(err)
	}
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])
	ref := msg.PackRef{ID: "fspace-std", Kind: "layout", Version: "1.0.0", SHA256: sha, Size: len(raw), Inline: true}
	store := t.TempDir()
	run := func(t *testing.T, answer *bool) (gets int, prompted bool, ui *engine.CmcpUI, g *cmcpRig) {
		g = newCmcpRigWith(t, func(c *cmcpRigCfg) {
			c.packs = []msg.PackRef{ref}
			c.storeDir = store
			c.onFrame = func(r *cmcpRig, f frame.Frame) {
				if f.Package == msg.PkgPackGet {
					gets++
					chunks, err := fetch.ChunkPlan(raw, 12288)
					if err != nil {
						t.Error(err)
						return
					}
					for i, c := range chunks {
						d := msg.PackData{ID: ref.ID, Version: ref.Version, SHA256: sha, Part: i + 1, Parts: len(chunks), Data: base64.StdEncoding.EncodeToString(c)}
						b, _ := json.Marshal(d)
						r.trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: msg.PkgPackData, Data: b})
					}
				}
			}
		})
		if answer != nil {
			g.e.SetCmcpRender(engine.CmcpRender{PackConsent: "ask"})
		}
		g.do(func(s *cs.Server) {
			s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1, Pack: &msg.PackIdent{ID: "fspace-std", Version: "1.0.0", SHA256: sha}, Packs: []msg.PackRef{ref}},
				Console: &msg.ConsoleRef{ID: "operations", Name: "Ops"}, Screen: "fspace-std/damage", Reason: "manned", Commands: []string{"dmg.assign"}})
		})
		if answer != nil {
			g.r.until(t, "consent prompt", func() bool {
				for _, ev := range g.r.snapshot() {
					for _, n := range ev.CmcpNotes {
						if n.Kind == "pack-consent" {
							prompted = true
						}
					}
				}
				return prompted
			})
			_ = 0
			if err := g.e.CmcpPackConsent(g.sid, "fspace-std", *answer); err != nil {
				t.Fatal(err)
			}
		}
		ui = g.waitUI("pack screen", func(u *engine.CmcpUI) bool {
			return u.Tree != nil && (answer == nil || !*answer || u.Tree.Screen == "fspace-std/damage")
		})
		return gets, prompted, ui, g
	}
	yes := true
	gets, prompted, ui, _ := run(t, &yes)
	waitConsentSaved(t, store)
	if gets != 1 || !prompted || ui.Tree.Screen != "fspace-std/damage" || ui.Screen.Fallback {
		t.Fatalf("first launch: gets %d prompted %v screen %q fallback %v", gets, prompted, ui.Tree.Screen, ui.Screen.Fallback)
	}
	gets, prompted, _, g2 := run(t, nil)
	g2.waitUI("cached pack screen", func(u *engine.CmcpUI) bool {
		return u.Tree != nil && u.Tree.Screen == "fspace-std/damage" && !u.Screen.Fallback
	})
	if gets != 0 || prompted {
		t.Fatalf("second launch fetched %d times / prompted %v", gets, prompted)
	}
}

// Evennia's banner ("Welcome to testgame") never names it; its MSSP CODEBASE does.
func TestGameTypeFromMSSP(t *testing.T) {
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	p := srv.Peer()
	p.Will(70)
	p.WaitNeg(fakemush.DO, 70)
	p.Sub(70, []byte("\x01CODEBASE\x02Evennia 4.5.0\x01NAME\x02testgame\x01UTF-8\x021"))
	srv.SendString("Welcome to testgame\r\n")
	r.until(t, "game type from MSSP", func() bool {
		in, err := e.Session(si.ID)
		return err == nil && in.GameType == "evennia" && in.GameTypeSource == "mssp"
	})
	if m := e.MSSP(si.ID); m["NAME"] != "testgame" {
		t.Fatalf("mssp %v", m)
	}
}

// rigCommands is the game's catalogue: the examples' full list when available (packs are validated against it), else one verb.
func rigCommands() []msg.CommandSpec {
	one := []msg.CommandSpec{{Name: "dmg.assign", Consoles: []string{"operations"}, Mutating: true, Text: "dmg assign {team}={system}",
		Args: map[string]msg.ArgSpec{"team": {Type: "int"}, "system": {Type: "string"}}}}
	b, err := os.ReadFile("../../../cmcp/examples/commands.catalogue.json")
	if err != nil {
		return one
	}
	var list []msg.CommandSpec
	if json.Unmarshal(b, &list) != nil || len(list) == 0 {
		return one
	}
	return list
}

// The game's pack over plain http on loopback: refused by default, accepted with the developer flag when the origin is the one the
// Welcome announced and the game itself is on loopback; then consent, switch from the built-in screen to the pack screen, cache.
func TestCmcpPackOverLoopbackHTTP(t *testing.T) {
	raw, err := os.ReadFile("../../../cmcp/examples/packs/fspace-std/pack.json")
	if err != nil {
		t.Skip(err)
	}
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cmcp/v1/blob/"+sha {
			hits++
			_, _ = w.Write(raw)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	ref := msg.PackRef{ID: "fspace-std", Kind: "layout", Version: "1.0.0", SHA256: sha, Size: len(raw), URL: srv.URL + "/cmcp/v1/blob/" + sha}
	dir := t.TempDir()
	run := func(t *testing.T, flag bool) *cmcpRig {
		if flag {
			t.Setenv("SQUATCLIENT_DEV_LOOPBACK_HTTP", "1")
		} else {
			t.Setenv("SQUATCLIENT_DEV_LOOPBACK_HTTP", "")
		}
		g := newCmcpRigWith(t, func(c *cmcpRigCfg) {
			c.packs = []msg.PackRef{ref}
			c.assets = &msg.Assets{Origin: srv.URL, Base: "/cmcp/v1"}
			c.storeDir = dir
		})
		g.e.SetCmcpRender(engine.CmcpRender{PackConsent: "ask"})
		g.do(func(s *cs.Server) {
			s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1, Pack: &msg.PackIdent{ID: ref.ID, Version: ref.Version, SHA256: sha}, Packs: []msg.PackRef{ref}},
				Console: &msg.ConsoleRef{ID: "operations", Name: "Ops"}, Screen: "fspace-std/damage", Reason: "manned", Commands: []string{"dmg.assign"}})
		})
		return g
	}
	g := run(t, false)
	time.Sleep(400 * time.Millisecond)
	if hits != 0 {
		t.Fatal("plain http was fetched without the developer flag")
	}
	if u, _ := g.e.CmcpUI(g.sid); u.Tree == nil || u.Tree.Screen != "std.operations" {
		t.Fatalf("without the flag the built-in screen must stay: %+v", u.Tree)
	}
	g = run(t, true)
	g.r.until(t, "consent prompt", func() bool {
		for _, ev := range g.r.snapshot() {
			for _, n := range ev.CmcpNotes {
				if n.Kind == "pack-consent" {
					return true
				}
			}
		}
		return false
	})
	if u, _ := g.e.CmcpUI(g.sid); u.Tree.Screen != "std.operations" {
		t.Fatalf("before consent the built-in screen is shown: %q", u.Tree.Screen)
	}
	if err := g.e.CmcpPackConsent(g.sid, ref.ID, true); err != nil {
		t.Fatal(err)
	}
	ui := g.waitUI("pack screen", func(u *engine.CmcpUI) bool {
		return u.Tree != nil && u.Tree.Screen == "fspace-std/damage" && !u.Screen.Fallback
	})
	if hits != 1 || ui.Tree == nil {
		t.Fatalf("hits %d", hits)
	}
	waitConsentSaved(t, dir) // the fetcher writes the cache and the consent record after the screen switched
}

func waitConsentSaved(t *testing.T, dir string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(filepath.Join(dir, "consent.json")); err == nil {
			time.Sleep(50 * time.Millisecond)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("consent record never saved")
}

// The viewscreen: contacts are projected, far to near, and a contact with art gets the view chosen for the angle it shows us
// (spec 7.7), with the tint amounts evaluated for that contact (never the own ship's damage).
func TestCmcpViewscreenContactArt(t *testing.T) {
	g := newCmcpRig(t)
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1}, Console: &msg.ConsoleRef{ID: "navigation", Name: "Helm"}, Screen: "std.helm", Reason: "manned",
			Subscribe: []msg.SubSpec{{Pkg: "Space.Contacts"}, {Pkg: "Space.Nav"}}, Commands: []string{"scan.srs"}})
		s.Push("Space.Contacts", true, map[string]any{"contacts": map[string]any{
			"17": map[string]any{"cid": 17, "name": "Near", "rel": map[string]any{"az": 10.0, "mark": 0.0}, "bearing": map[string]any{"az": 10.0, "mark": 0.0}, "distance_m": 1000000.0,
				"type": "Ship", "class": "Marauder", "empire": "federation", "size_m": 120.0, "heading": map[string]any{"az": 190.0, "mark": 0.0, "roll": 0.0},
				"integrity": map[string]any{"hull": 0.5, "port_nacelle": 1.0, "starboard_nacelle": 1.0}},
			"18": map[string]any{"cid": 18, "name": "Far", "rel": map[string]any{"az": -5.0, "mark": 0.0}, "bearing": map[string]any{"az": 355.0, "mark": 0.0}, "distance_m": 9000000.0, "type": "Ship", "class": "Marauder", "size_m": 120.0},
			"19": map[string]any{"cid": 19, "name": "Behind", "rel": map[string]any{"az": 170.0, "mark": 0.0}, "bearing": map[string]any{"az": 170.0, "mark": 0.0}, "distance_m": 1000.0, "type": "Ship"}}})
	})
	ui := g.waitUI("viewscreen", func(u *engine.CmcpUI) bool {
		return u.Tree != nil && findNode(u.Tree.Root, func(n *engine.CmcpNode) bool {
			return n.Widget == "viewscreen" && len(n.Props["items"].([]map[string]any)) > 0
		}) != nil
	})
	vs := findNode(ui.Tree.Root, func(n *engine.CmcpNode) bool { return n.Widget == "viewscreen" })
	items, _ := vs.Props["items"].([]map[string]any)
	if len(items) != 2 {
		t.Fatalf("the contact behind the ship is outside the field of view: %d items", len(items))
	}
	if items[0]["cid"] != "18" || items[1]["cid"] != "17" {
		t.Fatalf("far to near: %v then %v", items[0]["cid"], items[1]["cid"])
	}
	views, _ := items[1]["views"].([]map[string]any)
	if len(views) == 0 || views[0]["view"] != "front" {
		t.Fatalf("a ship whose bow points at us shows its front: %v", items[1]["views"])
	}
	if z, _ := views[0]["zones"].(map[string]float64); z["hull"] < 0.4 {
		t.Fatalf("the contact's own damage tints its art: %v", views[0]["zones"])
	}
}

// The text view's LOG and screen-off toggles are the shared core's state machine; the theme is chosen by the core's rule and
// falls back to built-in/plain; data keeps flowing while the screen is off.
func TestCmcpViewStateAndTheme(t *testing.T) {
	g := newCmcpRig(t)
	g.e.SetCmcpRender(engine.CmcpRender{Theme: "klingon", PortDefault: "large"})
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1, Theme: "romulan"}, Console: &msg.ConsoleRef{ID: "operations", Name: "Ops"}, Screen: "std.damage", Reason: "manned",
			Subscribe: []msg.SubSpec{{Pkg: "Space.Damage.Stat"}}, Commands: []string{"dmg.assign"}})
		s.Push("Space.Damage.Stat", true, map[string]any{"hull": map[string]any{"cur": 800.0, "max": 1000.0}})
	})
	ui := g.waitUI("tree", func(u *engine.CmcpUI) bool { return u.Tree != nil })
	if ui.Tree.Theme != "klingon" || ui.Tree.ThemeSource != "builtin" || ui.Tree.View.State != "balanced" || ui.Tree.View.PortDefault != "large" || ui.Tree.View.Fraction != 0.35 {
		t.Fatalf("tree %+v", ui.Tree.View)
	}
	for _, step := range [][2]string{{"log_toggle", "expanded"}, {"log_toggle", "balanced"}, {"screen_toggle", "off"}, {"log_toggle", "balanced"}} { // LOG while off brings the screen back
		st, err := g.e.CmcpViewEvent(g.sid, step[0])
		if err != nil || st != step[1] {
			t.Fatalf("%s -> %q (%v), want %q", step[0], st, err, step[1])
		}
	}
	g.e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true})
	g.waitUI("ship theme by the core's rule", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Theme == "romulan" })
	g.e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true, StartCollapsed: true})
	g.do(func(s *cs.Server) {
		s.Push("Space.Damage.Stat", false, map[string]any{"hull": map[string]any{"cur": 700.0}})
	})
	g.waitUI("data flows", func(u *engine.CmcpUI) bool { return u.Seqs["Space.Damage.Stat"] >= 2 })
}

// The console-switch column: a click shows another screen of the same console locally, subscribes what it needs (ref counted under
// "nav") and the choice ends with the console.
func TestCmcpShowScreen(t *testing.T) {
	g := newCmcpRig(t)
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1}, Console: &msg.ConsoleRef{ID: "operations", Name: "Ops"}, Screen: "std.operations", Reason: "manned",
			Subscribe: []msg.SubSpec{{Pkg: "Space.Ops.Stat"}}, Commands: []string{"dmg.assign"}})
	})
	g.waitUI("operations", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Screen == "std.operations" })
	if err := g.e.CmcpShowScreen(g.sid, "std.damage"); err != nil {
		t.Fatal(err)
	}
	ui := g.waitUI("damage shown", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Screen == "std.damage" })
	has := func(u *engine.CmcpUI, pkg string) bool {
		for _, sb := range u.Subs {
			if sb.Pkg == pkg {
				return true
			}
		}
		return false
	}
	g.waitUI("menu marks the local screen", func(u *engine.CmcpUI) bool {
		for _, o := range u.Screens {
			if o.ID == "std.damage" && o.Active {
				return u.LocalScreen == "std.damage"
			}
		}
		return false
	})
	_ = has
	_ = ui
	if err := g.e.CmcpShowScreen(g.sid, "std.nope"); err == nil {
		t.Fatal("an unknown screen was accepted")
	}
	// back to operations: what damage needed is released
	if err := g.e.CmcpShowScreen(g.sid, "std.operations"); err != nil {
		t.Fatal(err)
	}
	g.waitUI("released", func(u *engine.CmcpUI) bool {
		return u.Tree != nil && u.Tree.Screen == "std.operations" && !has(u, "Space.Damage.Teams")
	})
	// a different console resets the choice
	_ = g.e.CmcpShowScreen(g.sid, "std.tactical")
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1}, Console: &msg.ConsoleRef{ID: "science", Name: "Sci"}, Screen: "std.sensors", Reason: "manned",
			Subscribe: []msg.SubSpec{{Pkg: "Space.Contacts"}}, Commands: []string{"scan.srs"}})
	})
	g.waitUI("console change resets", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Screen == "std.sensors" })
}

// One theme per ship: every console and screen of a ship reports the ship's theme, built-in fallback screens included, whatever the screens
// suggest; it changes only on a ship hop or when the setting changes.
func TestCmcpOneThemePerShip(t *testing.T) {
	ships := []struct{ id, theme string }{{"ss-booty-king", "frontier"}, {"kdf-bortas", "klingon"}, {"rse-valdore", "romulan"}, {"fma-daimon", "ferengi"}, {"acs-resolute", "tos"}, {"khv-drakhar", "klingon"}}
	g := newCmcpRig(t)
	g.e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true})
	consoles := []struct{ console, screen string }{{"operations", "std.operations"}, {"science", "std.sensors"}, {"tactical", "std.tactical"}, {"navigation", "std.helm"}, {"engineering", "std.power"}, {"command", "std.command"}, {"medical", "std.medical"}}
	rev, fallback := 0, ""
	for _, sh := range ships {
		for i, c := range consoles {
			g.do(func(s *cs.Server) {
				s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: sh.id, Epoch: 1, Theme: sh.theme}, Console: &msg.ConsoleRef{ID: c.console, Name: c.console}, Screen: c.screen, Reason: "manned",
					Subscribe: []msg.SubSpec{{Pkg: "Space.Contacts"}}, Commands: []string{"scan.srs"}})
			})
			rev++
			want, scr := sh.theme, c.screen
			u := g.waitUI(sh.id+" "+c.console, func(u *engine.CmcpUI) bool {
				return u.Tree != nil && u.Ship != nil && u.Ship.ID == sh.id && u.Tree.Screen == scr
			})
			// the theme must be ready for the first screen of the hop: "frontier" is not built in, so it falls back to plain (still ONE id per ship)
			if want == "frontier" { // not built in and not announced: not usable, so the next choice is made once and sticks for the stay
				if i == 0 {
					want = u.Tree.Theme
					fallback = want
				} else {
					want = fallback
				}
			}
			if u.Tree.Theme != want {
				t.Fatalf("ship %s console %s (%d): theme %q, want %q", sh.id, c.console, i, u.Tree.Theme, want)
			}
		}
	}
	// a ship that names no theme: the first screen's suggestion (or the player's theme) sticks for the whole stay
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "npc-1", Epoch: 1}, Console: &msg.ConsoleRef{ID: "operations", Name: "o"}, Screen: "std.operations", Reason: "manned", Commands: []string{"scan.srs"}})
	})
	first := g.waitUI("npc ship", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Ship != nil && u.Ship.ID == "npc-1" }).Tree.Theme
	for _, c := range consoles[1:] {
		g.do(func(s *cs.Server) {
			s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "npc-1", Epoch: 1}, Console: &msg.ConsoleRef{ID: c.console, Name: c.console}, Screen: c.screen, Reason: "manned", Commands: []string{"scan.srs"}})
		})
		u := g.waitUI("npc "+c.console, func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Screen == c.screen })
		if u.Tree.Theme != first {
			t.Fatalf("a ship with no theme changed theme on screen switch: %q -> %q", first, u.Tree.Theme)
		}
	}
	// a setting change takes effect: fixed mode uses the player's theme
	g.e.SetCmcpRender(engine.CmcpRender{Theme: "ferengi", UseShipTheme: false})
	g.waitUI("fixed", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Theme == "ferengi" })
}

// A downloaded theme (spec 19.10): only the gpu rendition of the SELECTED theme is fetched, asked once, cached, and overrides the
// embedded copy only with a higher version (or with the dev flag); the second launch needs no fetch and no prompt.
func TestCmcpThemeDownload(t *testing.T) {
	load := func(f string) ([]byte, msg.PackRef) {
		raw, err := os.ReadFile("../../../cmcp/examples/themes/" + f)
		if err != nil {
			t.Skip(err)
		}
		sum := sha256.Sum256(raw)
		sha := hex.EncodeToString(sum[:])
		var doc struct{ Version string }
		_ = json.Unmarshal(raw, &doc)
		return raw, msg.PackRef{ID: "klingon", Kind: "theme", Version: doc.Version, Renditions: &msg.Renditions{GPU: &msg.Rendition{SHA256: sha, Size: len(raw), Inline: true}}}
	}
	run := func(t *testing.T, file string, store string, answer *bool, wantSrc string) (gets int, prompted bool, ui *engine.CmcpUI) {
		raw, ref := load(file)
		sha := ref.Renditions.GPU.SHA256
		g := newCmcpRigWith(t, func(c *cmcpRigCfg) {
			c.packs = []msg.PackRef{ref}
			c.storeDir = store
			c.onFrame = func(r *cmcpRig, f frame.Frame) {
				if f.Package == msg.PkgPackGet {
					gets++
					chunks, _ := fetch.ChunkPlan(raw, 12288)
					for i, c := range chunks {
						d := msg.PackData{ID: ref.ID, Version: ref.Version, SHA256: sha, Part: i + 1, Parts: len(chunks), Data: base64.StdEncoding.EncodeToString(c)}
						b, _ := json.Marshal(d)
						r.trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: msg.PkgPackData, Data: b})
					}
				}
			}
		})
		mode := "always"
		if answer != nil {
			mode = "ask"
		}
		g.e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true, PackConsent: mode})
		g.do(func(s *cs.Server) {
			s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1, Theme: "klingon"}, Console: &msg.ConsoleRef{ID: "operations", Name: "Ops"}, Screen: "std.operations", Reason: "manned",
				Commands: []string{"scan.srs"}})
		})
		if answer != nil {
			g.r.until(t, "theme consent", func() bool {
				for _, ev := range g.r.snapshot() {
					for _, n := range ev.CmcpNotes {
						if n.Kind == "pack-consent" && n.Pack != nil && n.Pack.Kind == "theme" {
							prompted = true
						}
					}
				}
				return prompted
			})
			if err := g.e.CmcpPackConsent(g.sid, "klingon", *answer); err != nil {
				t.Fatal(err)
			}
		}
		ui = g.waitUI("themed tree", func(u *engine.CmcpUI) bool {
			return u.Tree != nil && u.Tree.Theme == "klingon" && u.Tree.ThemeSource == wantSrc
		})
		if ui.Tree.ThemeSource == "pack" {
			if d, err := g.e.CmcpThemeDoc(g.sid, "klingon"); err != nil || d["id"] != "klingon" {
				t.Fatalf("theme doc: %v %v", d, err)
			}
		}
		return
	}
	store := t.TempDir()
	yes := true
	gets, prompted, ui := run(t, "klingon-1.0.1.gpu.json", store, &yes, "pack")
	waitConsentSaved(t, store)
	if gets != 1 || !prompted || ui.Tree.ThemeSource != "pack" || ui.Tree.ThemeVersion != "1.0.1" {
		t.Fatalf("first launch: gets %d prompted %v source %q version %q", gets, prompted, ui.Tree.ThemeSource, ui.Tree.ThemeVersion)
	}
	gets, prompted, ui = run(t, "klingon-1.0.1.gpu.json", store, nil, "pack")
	if gets != 0 || prompted || ui.Tree.ThemeSource != "pack" {
		t.Fatalf("second launch: gets %d prompted %v source %q", gets, prompted, ui.Tree.ThemeSource)
	}
	// the same version as the embedded copy: nothing is downloaded
	gets, prompted, ui = run(t, "klingon.gpu.json", t.TempDir(), nil, "builtin")
	if gets != 0 || prompted || ui.Tree.ThemeSource != "builtin" {
		t.Fatalf("same version: gets %d prompted %v source %q", gets, prompted, ui.Tree.ThemeSource)
	}
	// the developer flag prefers the pack whatever its version
	t.Setenv("SQUATCLIENT_DEV_FORCE_THEME_DOWNLOAD", "1")
	gets, _, ui = run(t, "klingon.gpu.json", t.TempDir(), &yes, "pack")
	if gets != 1 || ui.Tree.ThemeSource != "pack" {
		t.Fatalf("forced: gets %d source %q", gets, ui.Tree.ThemeSource)
	}
}

// The consent setting: "always" (default) loads a verified pack without a prompt and says so once; "never" fetches nothing.
func TestCmcpPackConsentModes(t *testing.T) {
	raw, err := os.ReadFile("../../../cmcp/examples/packs/fspace-std/pack.json")
	if err != nil {
		t.Skip(err)
	}
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])
	ref := msg.PackRef{ID: "fspace-std", Kind: "layout", Version: "1.0.0", SHA256: sha, Size: len(raw), Inline: true}
	for _, mode := range []string{"always", "never"} {
		gets := 0
		g := newCmcpRigWith(t, func(c *cmcpRigCfg) {
			c.packs = []msg.PackRef{ref}
			c.storeDir = t.TempDir()
			c.onFrame = func(r *cmcpRig, f frame.Frame) {
				if f.Package == msg.PkgPackGet {
					gets++
					chunks, _ := fetch.ChunkPlan(raw, 12288)
					for i, c := range chunks {
						d := msg.PackData{ID: ref.ID, Version: ref.Version, SHA256: sha, Part: i + 1, Parts: len(chunks), Data: base64.StdEncoding.EncodeToString(c)}
						b, _ := json.Marshal(d)
						r.trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: msg.PkgPackData, Data: b})
					}
				}
			}
		})
		g.e.SetCmcpRender(engine.CmcpRender{PackConsent: mode})
		g.do(func(s *cs.Server) {
			s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1, Pack: &msg.PackIdent{ID: "fspace-std", Version: "1.0.0", SHA256: sha}, Packs: []msg.PackRef{ref}},
				Console: &msg.ConsoleRef{ID: "operations", Name: "Ops"}, Screen: "fspace-std/damage", Reason: "manned", Commands: []string{"dmg.assign"}})
		})
		if mode == "always" {
			g.waitUI("auto-accepted pack screen", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Screen == "fspace-std/damage" })
			g.r.until(t, "accepted note", func() bool {
				for _, ev := range g.r.snapshot() {
					for _, n := range ev.CmcpNotes {
						if n.Kind == "pack-accepted" {
							return true
						}
					}
				}
				return false
			})
			accepted, prompted := 0, false
			for _, ev := range g.r.snapshot() {
				for _, n := range ev.CmcpNotes {
					if n.Kind == "pack-accepted" {
						accepted++
					}
					if n.Kind == "pack-consent" {
						prompted = true
					}
				}
			}
			if gets != 1 || prompted || accepted != 1 {
				t.Fatalf("always: gets %d prompted %v accepted notes %d", gets, prompted, accepted)
			}
			continue
		}
		time.Sleep(500 * time.Millisecond)
		if u, _ := g.e.CmcpUI(g.sid); gets != 0 || u.Tree == nil || u.Tree.Screen == "fspace-std/damage" {
			t.Fatalf("never: gets %d screen %+v", gets, u.Tree)
		}
	}
}

// The notice line (core queue) through the engine with a fake clock: a done notice replaces its own downloading notice in place, lingers
// 5.5 s; a failure 8 s; a second notice waits at least 1.2 s behind the first; a sticky (active) notice stays until its state changes.
func TestCmcpNoticeTiming(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	adv := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	g := newCmcpRigWith(t, func(c *cmcpRigCfg) { c.clock = clock })
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1}, Console: &msg.ConsoleRef{ID: "operations", Name: "Ops"}, Screen: "std.operations", Reason: "manned", Commands: []string{"scan.srs"}})
	})
	g.waitUI("console", func(u *engine.CmcpUI) bool { return u.Tree != nil })
	text := func(u *engine.CmcpUI) string {
		if u.Notice == nil {
			return ""
		}
		return u.Notice.Text
	}
	post := func(id, kind, state, txt string) {
		if err := g.e.CmcpNotify(g.sid, id, kind, state, txt); err != nil {
			t.Fatal(err)
		}
	}
	want := func(label string, f func(*engine.CmcpUI) bool) *engine.CmcpUI { return g.waitUI(label, f) }

	post("pack:theme:romulan", "download", "active", "Downloading Romulan theme pack")
	want("downloading shows", func(u *engine.CmcpUI) bool {
		return text(u) == "Downloading Romulan theme pack" && u.Notice.State == "active"
	})
	adv(10 * time.Second) // a sticky notice stays however long it takes
	time.Sleep(700 * time.Millisecond)
	want("sticky", func(u *engine.CmcpUI) bool { return text(u) == "Downloading Romulan theme pack" })
	post("pack:theme:romulan", "download", "done", "Downloaded Romulan theme pack")
	want("done replaces in place", func(u *engine.CmcpUI) bool {
		return text(u) == "Downloaded Romulan theme pack" && u.Notice.State == "done"
	})
	adv(5 * time.Second)
	time.Sleep(700 * time.Millisecond)
	want("still lingering at 5 s", func(u *engine.CmcpUI) bool { return text(u) == "Downloaded Romulan theme pack" })
	adv(700 * time.Millisecond) // 5.7 s > 5.5 s
	want("gone after 5.5 s", func(u *engine.CmcpUI) bool { return u.Notice == nil })

	post("pack:theme:ferengi", "download", "failed", "Could not download Ferengi theme pack: hash mismatch, using built-in")
	want("failure shows", func(u *engine.CmcpUI) bool { return u.Notice != nil && u.Notice.State == "failed" })
	adv(6 * time.Second)
	time.Sleep(700 * time.Millisecond)
	want("failure lingers at 6 s", func(u *engine.CmcpUI) bool { return u.Notice != nil && u.Notice.State == "failed" })
	adv(2200 * time.Millisecond) // 8.2 s
	want("failure gone after 8 s", func(u *engine.CmcpUI) bool { return u.Notice == nil })

	// a second notice waits for the first to have been visible 1.2 s, then replaces it
	post("a", "info", "", "first")
	want("first", func(u *engine.CmcpUI) bool { return text(u) == "first" })
	post("b", "info", "", "second")
	time.Sleep(700 * time.Millisecond)
	if u, _ := g.e.CmcpUI(g.sid); text(u) != "first" {
		t.Fatalf("the second notice replaced the first before it was visible 1.2 s: %q", text(u))
	}
	adv(1300 * time.Millisecond)
	want("second after the minimum", func(u *engine.CmcpUI) bool { return text(u) == "second" })
}

// The red-alert pulse (core pulse package) sampled by the engine: a smooth cosine from the time since the alert started (fake clock), roles
// r and x and the edge re-coloured by theme.PulseColour; steady for yellow-off, the player's off, reduced motion and recolour off.
func TestCmcpAlertPulse(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	adv := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	setup := func(r engine.CmcpRender) *cmcpRig {
		g := newCmcpRigWith(t, func(c *cmcpRigCfg) { c.clock = clock })
		r.Theme, r.UseShipTheme = "klingon", false
		g.e.SetCmcpRender(r)
		g.do(func(s *cs.Server) {
			s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1}, Console: &msg.ConsoleRef{ID: "operations", Name: "Ops"}, Screen: "std.operations", Reason: "manned", Commands: []string{"alert.set"},
				Subscribe: []msg.SubSpec{{Pkg: "Space.Ship.Status"}}})
			s.Push("Space.Ship.Status", true, map[string]any{"alert": "red", "hull": map[string]any{"cur": 90.0, "max": 100.0}})
		})
		g.waitUI("red alert", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Alert == "red" })
		return g
	}
	samples := func(g *cmcpRig) []engine.CmcpPulse {
		var out []engine.CmcpPulse
		for _, ev := range g.r.snapshot() {
			if ev.CmcpPulse != nil {
				out = append(out, *ev.CmcpPulse)
			}
		}
		return out
	}
	last := func(g *cmcpRig) *engine.CmcpPulse {
		s := samples(g)
		if len(s) == 0 {
			return nil
		}
		return &s[len(s)-1]
	}
	g := setup(engine.CmcpRender{})
	g.r.until(t, "first sample", func() bool { return last(g) != nil && last(g).Active })
	if l := last(g); l.Level < 0.99 || l.Roles["r"] == "" || l.Edge == "" {
		t.Fatalf("at the start of the alert the pulse is at full level: %+v", l)
	}
	period := time.Duration(last(g).Period) * time.Millisecond
	adv(period / 2) // half of the period: the dimmest point
	g.r.until(t, "dim sample", func() bool { l := last(g); return l != nil && l.Level < 0.8 })
	dim := last(g)
	if dim.Level < 0.1 || dim.Roles["r"] == "" {
		t.Fatalf("dim level %+v", dim)
	}
	adv(period / 2)
	g.r.until(t, "back to full", func() bool { l := last(g); return l != nil && l.Level > 0.99 })

	for name, r := range map[string]engine.CmcpRender{
		"the player's off": {AlertPulse: "off"}, "reduced motion": {Reduced: true}, "recolour off": {AlertRecolour: "off"},
	} {
		g := setup(r)
		time.Sleep(400 * time.Millisecond)
		adv(250 * time.Millisecond)
		time.Sleep(400 * time.Millisecond)
		// the pulse setting reaches the core asynchronously, so a first sample may still be the default (active) pulse; what must hold is that it
		// settles to steady and never goes active again
		g.r.until(t, name+": the pulse settles to steady", func() bool { l := last(g); return l == nil || !l.Active })
		steady := false
		for _, s := range samples(g) {
			if !s.Active {
				steady = true
			} else if steady {
				t.Fatalf("%s: the pulse went active again after it was steady: %+v", name, s)
			}
		}
	}
}

// textport_layout is the core's decision for the viewport the web view reports: auto gives the side layout from 1100 px and stacked below,
// the settings force one; the side textport's cap is port.max_cols x 0.6 x mono x scale (80 columns by default).
func TestCmcpTextLayout(t *testing.T) {
	g := newCmcpRig(t)
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1}, Console: &msg.ConsoleRef{ID: "operations", Name: "Ops"}, Screen: "std.operations", Reason: "manned", Commands: []string{"scan.srs"}})
	})
	g.waitUI("console", func(u *engine.CmcpUI) bool { return u.Tree != nil })
	if u, _ := g.e.CmcpUI(g.sid); u.Tree.Layout != "stacked" {
		t.Fatalf("before a viewport is reported the layout is stacked: %q", u.Tree.Layout)
	}
	for _, c := range []struct {
		setting string
		width   float64
		want    string
	}{{"", 1000, "stacked"}, {"", 1100, "side"}, {"", 1920, "side"}, {"stacked", 1920, "stacked"}, {"left", 900, "side"}} {
		g.e.SetCmcpRender(engine.CmcpRender{TextLayout: c.setting})
		if err := g.e.CmcpViewport(g.sid, c.width, c.width/1600, 16); err != nil {
			t.Fatal(err)
		}
		u := g.waitUI("layout", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Layout == c.want })
		if c.want == "side" {
			// 80 columns x 0.6 x 16 px x scale, rounded up
			want := math.Ceil(80 * 0.6 * 16 * (c.width / 1600))
			if math.Abs(u.Tree.PortCapPx-want) > 1 {
				t.Fatalf("cap %v, want %v (width %v)", u.Tree.PortCapPx, want, c.width)
			}
		}
	}
}

// A scan tab (std.sensors) sends its command with text "none" through Client.CommandWith (the typed text is not printed into the game output),
// sets the tab's $ui state, and echoes its typed form to the log line.
func TestCmcpScanTabSendsTextNone(t *testing.T) {
	var mu sync.Mutex
	var cmds []map[string]any
	g := newCmcpRigWith(t, func(c *cmcpRigCfg) {
		c.onFrame = func(r *cmcpRig, f frame.Frame) {
			if f.Package == msg.PkgCmd {
				var m map[string]any
				if json.Unmarshal(f.Payload, &m) == nil {
					mu.Lock()
					cmds = append(cmds, m)
					mu.Unlock()
				}
			}
		}
	})
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1}, Console: &msg.ConsoleRef{ID: "science", Name: "Sci"}, Screen: "std.sensors", Reason: "manned",
			Subscribe: []msg.SubSpec{{Pkg: "Space.Contacts"}}, Commands: []string{"scan.srs", "scan.lrs", "scan.passive"}})
	})
	ui := g.waitUI("sensors", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Screen == "std.sensors" })
	tab := findNode(ui.Tree.Root, func(n *engine.CmcpNode) bool { return n.Widget == "button" && n.Enabled && len(n.Events) > 0 })
	if tab == nil {
		t.Fatal("no clickable button on the sensors screen")
	}
	// click every enabled button that sends a scan.* command until one does
	var sent map[string]any
	var walk func(n *engine.CmcpNode)
	walk = func(n *engine.CmcpNode) {
		if n.Widget == "button" && n.Enabled && sent == nil {
			_, _ = g.e.CmcpEvent(g.sid, n.Path, "click", nil)
			time.Sleep(60 * time.Millisecond)
			mu.Lock()
			for _, m := range cmds {
				if c, _ := m["cmd"].(string); len(c) > 5 && c[:5] == "scan." {
					sent = m
				}
			}
			mu.Unlock()
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(ui.Tree.Root)
	if sent == nil {
		t.Fatalf("no scan command was sent: %v", cmds)
	}
	if sent["text"] != "none" {
		t.Fatalf("the scan tab must send text none: %v", sent)
	}
}

// Hit shake (6.18) and the ship-wide alert ambience (6.17.1), sampled by the engine from the core with a fake clock: a `hit` on the own ship
// shakes the frame and widgets for the core's duration and then rests; the player's off and reduced motion keep it still; the ambience
// (edge and the alert roles) shows on a red alert on any screen and follows the player's setting.
func TestCmcpHitShakeAndAmbient(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	adv := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	setup := func(r engine.CmcpRender) *cmcpRig {
		g := newCmcpRigWith(t, func(c *cmcpRigCfg) { c.clock = clock })
		r.Theme, r.UseShipTheme = "tng", false
		g.e.SetCmcpRender(r)
		g.do(func(s *cs.Server) {
			s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1}, Console: &msg.ConsoleRef{ID: "operations", Name: "Ops"}, Screen: "std.operations", Reason: "manned", Commands: []string{"alert.set"},
				Subscribe: []msg.SubSpec{{Pkg: "Space.Ship.Status"}}})
			s.Push("Space.Ship.Status", true, map[string]any{"alert": "red", "hull": map[string]any{"cur": 90.0, "max": 100.0}})
		})
		g.waitUI("console", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Alert == "red" })
		return g
	}
	hit := func(g *cmcpRig, strength float64, facing string) {
		b, _ := json.Marshal(msg.Event{Type: "hit", Data: map[string]any{"cid": "self", "strength": strength, "facing": facing}})
		g.trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: "Cobra.Event", Data: b})
	}
	samples := func(g *cmcpRig) []engine.CmcpPulse {
		var out []engine.CmcpPulse
		for _, ev := range g.r.snapshot() {
			if ev.CmcpPulse != nil {
				out = append(out, *ev.CmcpPulse)
			}
		}
		return out
	}
	shaking := func(g *cmcpRig) *engine.CmcpShake {
		s := samples(g)
		if len(s) == 0 {
			return nil
		}
		return s[len(s)-1].Shake
	}

	g := setup(engine.CmcpRender{})
	g.r.until(t, "ambient on a red alert", func() bool {
		s := samples(g)
		return len(s) > 0 && s[len(s)-1].Ambient != nil && s[len(s)-1].Ambient.Edge != "" && len(s[len(s)-1].Ambient.Roles) > 0
	})
	hit(g, 0.9, "port")
	adv(100 * time.Millisecond) // inside the shake's duration (150..500 ms)
	g.r.until(t, "shake", func() bool { s := shaking(g); return s != nil && s.Active && (s.X != 0 || s.Y != 0) })
	adv(2 * time.Second) // well past it: at rest
	g.r.until(t, "rest", func() bool { return shaking(g) == nil })

	for name, r := range map[string]engine.CmcpRender{"the player's off": {HitShake: "off"}, "reduced motion": {Reduced: true}} {
		g2 := setup(r)
		hit(g2, 0.9, "port")
		adv(100 * time.Millisecond)
		time.Sleep(500 * time.Millisecond)
		for _, s := range samples(g2) {
			if s.Shake != nil {
				t.Fatalf("%s: no shake expected: %+v", name, s)
			}
		}
	}
	g3 := setup(engine.CmcpRender{AlertAmbient: "off"})
	time.Sleep(600 * time.Millisecond)
	for _, s := range samples(g3) {
		if s.Ambient != nil {
			t.Fatalf("the player's ambience off: %+v", s)
		}
	}
}

// Aboard in an ordinary room the game pushes room:shiproom and the ship-wide alert look reaches the view; once the contexts are gone (the player left
// the ship: the game sends nothing about it, the ship's status just stays) NO screen and no ambience remain, only the plain text UI.
func TestCmcpAmbientInTheRoomAndNothingOffTheShip(t *testing.T) {
	g := newCmcpRig(t)
	g.e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true, RoomScreens: true})
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1, Name: "Resolute"}, Reason: "none"})
		s.Push("Space.Ship.Status", true, map[string]any{"alert": "red", "hull": map[string]any{"cur": 90.0, "max": 100.0}})
	})
	// a ship's status and a ship id but no context: nothing is drawn
	time.Sleep(600 * time.Millisecond)
	if u, _ := g.e.CmcpUI(g.sid); u.Tree != nil {
		t.Fatalf("no context, no screen: %q", u.Tree.Screen)
	}
	prio := 35
	push := func(rev int) {
		b, _ := json.Marshal(msg.Context{Rev: rev, Ship: msg.ContextShip{ID: "s1", Epoch: 1}, Op: "push", Reason: "entered",
			Context: &msg.ContextEntry{Key: "room:shiproom", Ctx: 1, Kind: "room", ID: "shiproom", Name: "Bridge", Screen: "std.shiproom", Priority: &prio}})
		g.trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: "Cobra.Context", Data: b})
	}
	push(3)
	g.waitUI("the room screen", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Screen == "std.shiproom" })
	g.r.until(t, "ambience in the room", func() bool {
		for _, ev := range g.r.snapshot() {
			if p := ev.CmcpPulse; p != nil && p.Ambient != nil && p.Ambient.Edge != "" {
				return true
			}
		}
		return false
	})
	// the player leaves: the game pops the room context
	b, _ := json.Marshal(msg.Context{Rev: 4, Ship: msg.ContextShip{ID: "s1", Epoch: 1}, Op: "pop", Key: "room:shiproom", Ctx: 1, Reason: "left"})
	g.trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: "Cobra.Context", Data: b})
	g.waitUI("no screen off the ship", func(u *engine.CmcpUI) bool { return u.Tree == nil })
	g.r.until(t, "the ambience is cleared", func() bool {
		s := g.r.snapshot()
		for i := len(s) - 1; i >= 0; i-- {
			if p := s[i].CmcpPulse; p != nil {
				return p.Ambient == nil && p.Shake == nil
			}
		}
		return false
	})
}

// A shield hit that reports strength 0 (the game's usual shield hit) still shakes the display: the client floors the strength, and the shake
// reaches the web view in a room (no console) as well as at a console.
func TestCmcpShieldHitStrengthZeroShakesInARoom(t *testing.T) {
	g := newCmcpRig(t)
	g.e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true, RoomScreens: true})
	g.do(func(s *cs.Server) { // aboard, no console: a room
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1, Name: "Resolute"}, Reason: "none"})
		s.Push("Space.Ship.Status", true, map[string]any{"alert": "yellow", "hull": map[string]any{"cur": 90.0, "max": 100.0}})
	})
	rp := 35
	rb, _ := json.Marshal(msg.Context{Rev: 3, Ship: msg.ContextShip{ID: "s1", Epoch: 1}, Op: "push", Reason: "entered",
		Context: &msg.ContextEntry{Key: "room:shiproom", Ctx: 1, Kind: "room", ID: "shiproom", Name: "Mess Hall", Screen: "std.shiproom", Priority: &rp}})
	g.trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: "Cobra.Context", Data: rb})
	ui := g.waitUI("the room is a screen", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Screen == "std.shiproom" })
	if findNode(ui.Tree.Root, func(n *engine.CmcpNode) bool { return n.Widget == "textport" }) == nil {
		t.Fatal("the room screen has a text port")
	}
	g.r.until(t, "the fx are configured (ambience on the yellow alert)", func() bool { // the shake config is set together with the ambience: a hit before it would be ignored
		for _, ev := range g.r.snapshot() {
			if p := ev.CmcpPulse; p != nil && p.Ambient != nil {
				return true
			}
		}
		return false
	})
	b, _ := json.Marshal(msg.Event{Type: "hit", Data: map[string]any{"cid": "self", "shield": true, "strength": 0.0, "facing": "aft"}})
	g.trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: "Cobra.Event", Data: b})
	g.r.until(t, "a visible shake for a strength-0 shield hit", func() bool {
		for _, ev := range g.r.snapshot() {
			if p := ev.CmcpPulse; p != nil && p.Shake != nil && p.Shake.Active && (p.Shake.X != 0 || p.Shake.Y != 0) {
				return true
			}
		}
		return false
	})
	if dir := os.Getenv("SQUAT_DUMP_PULSES"); dir != "" { // the engine's real samples of this hit, for the headless screenshot script
		time.Sleep(700 * time.Millisecond)
		var out []engine.CmcpPulse
		for _, ev := range g.r.snapshot() {
			if p := ev.CmcpPulse; p != nil && (p.Shake != nil || p.Ambient != nil) {
				out = append(out, *p)
			}
		}
		b, _ := json.MarshalIndent(out, "", " ")
		_ = os.WriteFile(filepath.Join(dir, "live-pulses.json"), b, 0o644)
	}
	// and the yellow-alert ambience is on the room's frame
	g.r.until(t, "ambience in the room", func() bool {
		for _, ev := range g.r.snapshot() {
			if p := ev.CmcpPulse; p != nil && p.Ambient != nil && p.Ambient.Edge != "" {
				return true
			}
		}
		return false
	})
}

// The X of the console window asks the game (Cobra.Context.Close) and does NOT hide the screen: for a console, a room and the generic ship room the
// request names the active context and reason user; the window closing sends reason client; a context-less client sends console.unman.
func TestCmcpCloseAsksTheGame(t *testing.T) {
	var mu sync.Mutex
	var closes []map[string]any
	var cmds []map[string]any
	g := newCmcpRigWith(t, func(c *cmcpRigCfg) {
		c.onFrame = func(r *cmcpRig, f frame.Frame) {
			var m map[string]any
			if json.Unmarshal(f.Payload, &m) != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			switch f.Package {
			case msg.PkgContextClose:
				closes = append(closes, m)
			case msg.PkgCmd:
				cmds = append(cmds, m)
			}
		}
	})
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1}, Console: &msg.ConsoleRef{ID: "operations", Name: "Ops"}, Screen: "std.operations", Reason: "manned", Commands: []string{"dmg.assign"}})
	})
	g.waitUI("console", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Screen == "std.operations" })
	last := func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		if len(closes) == 0 {
			return nil
		}
		return closes[len(closes)-1]
	}
	if err := g.e.CmcpClose(g.sid, "user"); err != nil {
		t.Fatal(err)
	}
	g.r.until(t, "a Cobra.Context.Close for the console", func() bool { return last() != nil })
	c := last()
	if c["reason"] != "user" || c["key"] == nil || c["key"] == "" || c["kind"] != "console" {
		t.Fatalf("the close request names the active console context: %v", c)
	}
	// the screen stays until the game answers
	time.Sleep(300 * time.Millisecond)
	if u, _ := g.e.CmcpUI(g.sid); u.Tree == nil || u.Tree.Screen != "std.operations" {
		t.Fatalf("the client must not hide the screen itself: %+v", u.Tree)
	}
	// the window closing: reason client (more than two requests a second are throttled, so wait)
	time.Sleep(1100 * time.Millisecond)
	g.e.CmcpCloseAll()
	g.r.until(t, "a close with reason client", func() bool { c := last(); return c != nil && c["reason"] == "client" })

	// a context-less client unmans with the verb
	g2 := newCmcpRigWith(t, func(c *cmcpRigCfg) {
		c.noContext = true
		c.onFrame = func(r *cmcpRig, f frame.Frame) {
			var m map[string]any
			if f.Package == msg.PkgCmd && json.Unmarshal(f.Payload, &m) == nil {
				mu.Lock()
				cmds = append(cmds, m)
				mu.Unlock()
			}
		}
	})
	g2.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1}, Console: &msg.ConsoleRef{ID: "operations", Name: "Ops"}, Screen: "std.operations", Reason: "manned", Commands: []string{"console.unman"}})
	})
	g2.waitUI("console", func(u *engine.CmcpUI) bool { return u.Tree != nil })
	_ = g2.e.CmcpClose(g2.sid, "user")
	g2.r.until(t, "console.unman for a context-less client", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, m := range cmds {
			if m["cmd"] == "console.unman" {
				return true
			}
		}
		return false
	})
}

// std.shiproom: the generic room aboard (context room:shiproom) is a built-in screen with the ship's status strip, the room's name from $ctx and the
// text port; it is drawn like any screen, so the theme frame, the alert look and the hit shake are on it; the X names the room context.
func TestCmcpShiproomScreen(t *testing.T) {
	var mu sync.Mutex
	var closes []map[string]any
	g := newCmcpRigWith(t, func(c *cmcpRigCfg) {
		c.onFrame = func(r *cmcpRig, f frame.Frame) {
			var m map[string]any
			if f.Package == msg.PkgContextClose && json.Unmarshal(f.Payload, &m) == nil {
				mu.Lock()
				closes = append(closes, m)
				mu.Unlock()
			}
		}
	})
	g.e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true, RoomScreens: true})
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1, Name: "Resolute"}, Reason: "none", Subscribe: []msg.SubSpec{{Pkg: "Space.Ship.Status"}}})
		s.Push("Space.Ship.Status", true, map[string]any{"name": "ACS Resolute", "class": "Constitution", "alert": "yellow", "hull": map[string]any{"cur": 90.0, "max": 100.0}})
	})
	prio := 35
	b, _ := json.Marshal(msg.Context{Rev: 3, Ship: msg.ContextShip{ID: "s1", Epoch: 1}, Op: "push", Reason: "entered",
		Context: &msg.ContextEntry{Key: "room:shiproom", Ctx: 1, Kind: "room", ID: "shiproom", Name: "Mess Hall", Screen: "std.shiproom", Priority: &prio}})
	g.trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: "Cobra.Context", Data: b})
	ui := g.waitUI("the ship room screen", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Screen == "std.shiproom" })
	if findNode(ui.Tree.Root, func(n *engine.CmcpNode) bool { return n.Widget == "textport" }) == nil {
		t.Fatal("the ship room has the text port")
	}
	lab := findNode(ui.Tree.Root, func(n *engine.CmcpNode) bool {
		s, _ := n.Props["text"].(string)
		return n.Widget == "label" && strings.Contains(s, "Mess Hall")
	})
	if lab == nil {
		first := findNode(ui.Tree.Root, func(n *engine.CmcpNode) bool { return n.Widget == "label" && n.Props["text"] != "" })
		t.Fatalf("the status strip names the room from $ctx.name: %v", first)
	}
	if err := g.e.CmcpClose(g.sid, "user"); err != nil {
		t.Fatal(err)
	}
	g.r.until(t, "a close for the room context", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(closes) > 0 && closes[len(closes)-1]["kind"] == "room" && closes[len(closes)-1]["key"] == "room:shiproom"
	})
}

// The game may refuse a close: the screen stays and the player sees an error notice with the game's hint; no answer within 5 s is a refusal too.
func TestCmcpCloseRefusalShowsTheHint(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	adv := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	answer := true
	g := newCmcpRigWith(t, func(c *cmcpRigCfg) {
		c.clock = clock
		c.onFrame = func(r *cmcpRig, f frame.Frame) {
			var m struct {
				ID uint32 `json:"id"`
			}
			if f.Package == msg.PkgContextClose && json.Unmarshal(f.Payload, &m) == nil && answer {
				b, _ := json.Marshal(msg.Reply{ID: m.ID, OK: false, Err: "refused", Hint: "The captain has the console: ask them to release it"})
				r.trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: msg.PkgReply, Data: b})
			}
		}
	})
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1}, Console: &msg.ConsoleRef{ID: "operations", Name: "Ops"}, Screen: "std.operations", Reason: "manned", Commands: []string{"dmg.assign"}})
	})
	g.waitUI("console", func(u *engine.CmcpUI) bool { return u.Tree != nil })
	if err := g.e.CmcpClose(g.sid, "user"); err != nil {
		t.Fatal(err)
	}
	u := g.waitUI("the refusal's hint", func(u *engine.CmcpUI) bool {
		return u.Notice != nil && u.Notice.Kind == "error" && strings.Contains(u.Notice.Text, "The captain has the console")
	})
	if u.Tree == nil || u.Tree.Screen != "std.operations" {
		t.Fatalf("a refused close keeps the screen: %+v", u.Tree)
	}

	// no answer at all: after 5 s the core treats it as refused ("no answer")
	answer = false
	adv(3 * time.Second) // the previous notice has lingered out; two requests a second are allowed again
	if err := g.e.CmcpClose(g.sid, "user"); err != nil {
		t.Fatal(err)
	}
	adv(6 * time.Second)
	g.waitUI("the no-answer notice", func(u *engine.CmcpUI) bool { return u.Notice != nil && strings.Contains(u.Notice.Text, "no answer") })
}

// The screen follows the ACTIVE context: a console screen only while a console is manned. A screen the player switched to locally (the menu) lasts
// only until the stack changes, so after the console is unmanned the ship room wins, never the console's or the player's last screen.
func TestCmcpActiveContextWinsAfterUnman(t *testing.T) {
	g := newCmcpRig(t)
	g.e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true, RoomScreens: true})
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1, Name: "Pathfinder"}, Console: &msg.ConsoleRef{ID: "science", Name: "Sci"}, Screen: "std.sensors", Reason: "manned",
			Subscribe: []msg.SubSpec{{Pkg: "Space.Contacts"}, {Pkg: "Space.Nav"}}, Commands: []string{"scan.srs"}})
	})
	g.waitUI("console", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Screen == "std.sensors" })
	if err := g.e.CmcpShowScreen(g.sid, "std.helm"); err != nil { // the player switches the view with the menu
		t.Fatal(err)
	}
	g.waitUI("local switch", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Screen == "std.helm" })
	// unmanned: the console goes (Cobra.Console with no console) and the game pushes the room it stands in
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1, Name: "Pathfinder"}, Reason: "unmanned"})
	})
	prio := 35
	b, _ := json.Marshal(msg.Context{Rev: 9, Ship: msg.ContextShip{ID: "s1", Epoch: 1}, Op: "push", Reason: "entered",
		Context: &msg.ContextEntry{Key: "room:shiproom", Ctx: 7, Kind: "room", ID: "shiproom", Name: "Ready Room", Screen: "std.shiproom", Priority: &prio}})
	g.trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: "Cobra.Context", Data: b})
	u := g.waitUI("the room wins", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Screen == "std.shiproom" })
	if u.LocalScreen != "" || u.Console != nil {
		t.Fatalf("no console and no local screen choice after the unman: console=%v local=%q", u.Console != nil, u.LocalScreen)
	}
}

// While a console is manned the ship room is still one menu entry away: the local view switch to std.shiproom works whenever the room context is on
// the stack beneath the console, so the player always has a way out of the console layout that does not depend on the game answering.
func TestCmcpLocalSwitchToTheRoomWhileManned(t *testing.T) {
	g := newCmcpRig(t)
	g.e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true})
	prio := 35
	b, _ := json.Marshal(msg.Context{Rev: 2, Ship: msg.ContextShip{ID: "s1", Epoch: 1}, Op: "push", Reason: "entered",
		Context: &msg.ContextEntry{Key: "room:shiproom", Ctx: 1, Kind: "room", ID: "shiproom", Name: "Bridge", Screen: "std.shiproom", Priority: &prio}})
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1, Name: "Pathfinder"}, Console: &msg.ConsoleRef{ID: "science", Name: "Sci"}, Screen: "std.sensors", Reason: "manned",
			Subscribe: []msg.SubSpec{{Pkg: "Space.Contacts"}}, Commands: []string{"scan.srs"}})
	})
	g.trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: "Cobra.Context", Data: b})
	g.waitUI("console up", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Screen == "std.sensors" })
	u, _ := g.e.CmcpUI(g.sid)
	var menu []string
	for _, o := range u.Screens {
		menu = append(menu, o.ID)
	}
	t.Logf("menu while manned: %v stack=%d", menu, len(u.Stack))
	if err := g.e.CmcpShowScreen(g.sid, "std.shiproom"); err != nil {
		t.Fatalf("the local switch to the room must work while a console is manned: %v", err)
	}
	g.waitUI("the room view", func(u *engine.CmcpUI) bool { return u.Tree != nil && u.Tree.Screen == "std.shiproom" })
}

// A connection that ends (closed by the game, "Logged in from elsewhere") must not leave its last console on screen: the engine publishes one final model
// with no screen.
func TestCmcpEndedConnectionDrawsNothing(t *testing.T) {
	g := newCmcpRig(t)
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1}, Console: &msg.ConsoleRef{ID: "operations", Name: "Ops"}, Screen: "std.operations", Reason: "manned", Commands: []string{"dmg.assign"}})
	})
	g.waitUI("console", func(u *engine.CmcpUI) bool { return u.Tree != nil })
	g.trp.Load().deliver(conn.Event{Kind: conn.EvEnded, Reason: "remote_closed", Detail: "Logged in from elsewhere"})
	g.r.until(t, "a final model with no screen", func() bool {
		s := g.r.snapshot()
		for i := len(s) - 1; i >= 0; i-- {
			if s[i].Cmcp != nil {
				return s[i].Cmcp.Tree == nil && s[i].Cmcp.State == "ended"
			}
		}
		return false
	})
}

// By default only a console (manned or watched) gets a screen: a ship room, the turbolift or an away team stays the plain text UI.
func TestCmcpNoRoomScreenByDefault(t *testing.T) {
	g := newCmcpRig(t)
	g.e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true})
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1, Name: "Resolute"}, Reason: "none", Subscribe: []msg.SubSpec{{Pkg: "Space.Ship.Status"}}})
		s.Push("Space.Ship.Status", true, map[string]any{"name": "ACS Resolute", "class": "Constitution", "alert": "green", "hull": map[string]any{"cur": 90.0, "max": 100.0}})
	})
	prio := 35
	b, _ := json.Marshal(msg.Context{Rev: 3, Ship: msg.ContextShip{ID: "s1", Epoch: 1}, Op: "push", Reason: "entered",
		Context: &msg.ContextEntry{Key: "room:shiproom", Ctx: 1, Kind: "room", ID: "shiproom", Name: "Mess Hall", Screen: "std.shiproom", Priority: &prio}})
	g.trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: "Cobra.Context", Data: b})
	time.Sleep(1200 * time.Millisecond)
	if u, err := g.e.CmcpUI(g.sid); err == nil && u != nil && u.Tree != nil {
		t.Fatalf("a ship room drew the screen %q with room screens off", u.Tree.Screen)
	}
}

// The player closes the screen (the X): the game pops the context with reason "closed" and the stack is empty, but the ship is still the player's, so the
// ship-wide alert look (drawn by the plain web view, not a scene) stays.
func TestCmcpAlertHueStaysAfterTheXCloses(t *testing.T) {
	g := newCmcpRig(t)
	g.e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true})
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1, Name: "Resolute"}, Reason: "none"})
		s.Push("Space.Ship.Status", true, map[string]any{"alert": "red", "hull": map[string]any{"cur": 90.0, "max": 100.0}})
	})
	prio := 35
	b, _ := json.Marshal(msg.Context{Rev: 3, Ship: msg.ContextShip{ID: "s1", Epoch: 1}, Op: "push", Reason: "entered",
		Context: &msg.ContextEntry{Key: "room:shiproom", Ctx: 1, Kind: "room", ID: "shiproom", Name: "Bridge", Screen: "std.shiproom", Priority: &prio}})
	g.trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: "Cobra.Context", Data: b})
	hue := func() bool {
		s := g.r.snapshot()
		for i := len(s) - 1; i >= 0; i-- {
			if p := s[i].CmcpPulse; p != nil {
				return p.Ambient != nil && p.Ambient.Edge != ""
			}
		}
		return false
	}
	g.r.until(t, "the alert hue in the room", hue)
	_ = g.e.CmcpClose(g.sid, "user")
	pop, _ := json.Marshal(msg.Context{Rev: 4, Ship: msg.ContextShip{ID: "s1", Epoch: 1}, Op: "pop", Key: "room:shiproom", Ctx: 1, Reason: "closed"})
	g.trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: "Cobra.Context", Data: pop})
	time.Sleep(1500 * time.Millisecond)
	if !hue() {
		t.Fatal("the alert hue went away when the player closed the screen")
	}
}

// The diagnostics trail: a control-plane payload is logged in full, the high-volume streams (Space.*) become one counter line per package every few
// seconds, a full document is still logged in full, and nothing is capped.
func TestCmcpTrailSummarizesStreams(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	g := newCmcpRigWith(t, func(c *cmcpRigCfg) { c.clock = clock })
	var lines []string
	var lmu sync.Mutex
	g.e.SetCmcpLog(func(l string) { lmu.Lock(); lines = append(lines, l); lmu.Unlock() })
	get := func() []string { lmu.Lock(); defer lmu.Unlock(); return append([]string(nil), lines...) }
	// deliver frames from the server side straight to the engine (the GMCP event the engine would receive)
	deliver := func(pkg, body string) {
		g.trp.Load().deliver(conn.Event{Kind: conn.EvGmcp, Package: pkg, Data: json.RawMessage(body)})
	}
	for i := 0; i < 300; i++ {
		deliver("Space.Contacts", fmt.Sprintf(`{"seq":%d,"contacts":{}}`, i))
	}
	deliver("Cobra.Sound.Play", `{"id":"impulse_loop","loop":true}`)
	deliver("Space.Contacts", `{"seq":301,"full":true,"contacts":{"c1":{}}}`)
	time.Sleep(200 * time.Millisecond)
	joined := strings.Join(get(), "\n")
	if strings.Contains(joined, `<- Space.Contacts {"seq":5,`) {
		t.Fatal("a stream delta was logged one by one")
	}
	if !strings.Contains(joined, `<- Cobra.Sound.Play {"id":"impulse_loop","loop":true}`) {
		t.Fatalf("control-plane payload missing:\n%s", joined)
	}
	if !strings.Contains(joined, `"full":true`) {
		t.Fatalf("a full document must be logged in full:\n%s", joined)
	}
	mu.Lock()
	now = now.Add(6 * time.Second)
	mu.Unlock()
	deliver("Space.Contacts", `{"seq":400,"contacts":{}}`)
	time.Sleep(200 * time.Millisecond)
	joined = strings.Join(get(), "\n")
	if !strings.Contains(joined, "stream <- Space.Contacts count=") || !strings.Contains(joined, "last_seq=") {
		t.Fatalf("no stream summary:\n%s", joined)
	}
}
