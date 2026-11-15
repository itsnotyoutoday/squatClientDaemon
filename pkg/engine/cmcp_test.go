package engine_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/itsnotyoutoday/cmcp/fetch"
	"github.com/itsnotyoutoday/cmcp/pack"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	assets   *msg.Assets
	packs    []msg.PackRef
	storeDir string
	onFrame  func(*cmcpRig, frame.Frame)
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
		return cs.Config{Name: "SquatClient", Version: "test", Kind: "gui", Render: &msg.Render{Kind: "gpu"}, MaxBody: 65536, Logf: t.Logf}
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
		return u.Tree != nil && findNode(u.Tree.Root, func(n *engine.CmcpNode) bool { return n.Widget == "team-assigner" }) != nil
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
	run := func(t *testing.T, flag bool) *cmcpRig {
		if flag {
			t.Setenv("SQUATCLIENT_DEV_LOOPBACK_HTTP", "1")
		} else {
			t.Setenv("SQUATCLIENT_DEV_LOOPBACK_HTTP", "")
		}
		g := newCmcpRigWith(t, func(c *cmcpRigCfg) {
			c.packs = []msg.PackRef{ref}
			c.assets = &msg.Assets{Origin: srv.URL, Base: "/cmcp/v1"}
			c.storeDir = t.TempDir()
		})
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
}

// The viewscreen: contacts are projected, far to near, and a contact with art gets the view chosen for the angle it shows us
// (spec 7.7), with the tint amounts evaluated for that contact (never the own ship's damage).
func TestCmcpViewscreenContactArt(t *testing.T) {
	g := newCmcpRig(t)
	g.do(func(s *cs.Server) {
		s.SetConsole(msg.Console{Ship: msg.ShipInfo{ID: "s1", Epoch: 1}, Console: &msg.ConsoleRef{ID: "science", Name: "Science"}, Screen: "std.sensors", Reason: "manned",
			Subscribe: []msg.SubSpec{{Pkg: "Space.Contacts"}, {Pkg: "Space.Nav"}}, Commands: []string{"scan.srs"}})
		s.Push("Space.Contacts", true, map[string]any{"contacts": map[string]any{
			"17": map[string]any{"cid": 17, "name": "Near", "rel": map[string]any{"az": 10.0, "mark": 0.0}, "bearing": map[string]any{"az": 10.0, "mark": 0.0}, "distance_m": 1000000.0,
				"type": "Ship", "class": "Marauder", "empire": "federation", "size_m": 120.0, "heading": map[string]any{"az": 190.0, "mark": 0.0, "roll": 0.0},
				"integrity": map[string]any{"hull": 0.5, "port_nacelle": 1.0, "starboard_nacelle": 1.0}},
			"18": map[string]any{"cid": 18, "name": "Far", "rel": map[string]any{"az": -5.0, "mark": 0.0}, "bearing": map[string]any{"az": 355.0, "mark": 0.0}, "distance_m": 9000000.0, "type": "Ship", "class": "Marauder", "size_m": 120.0},
			"19": map[string]any{"cid": 19, "name": "Behind", "rel": map[string]any{"az": 170.0, "mark": 0.0}, "bearing": map[string]any{"az": 170.0, "mark": 0.0}, "distance_m": 1000.0, "type": "Ship"}}})
	})
	ui := g.waitUI("viewscreen", func(u *engine.CmcpUI) bool {
		return u.Tree != nil && findNode(u.Tree.Root, func(n *engine.CmcpNode) bool { return n.Widget == "viewscreen" && n.Props["items"] != nil }) != nil
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
