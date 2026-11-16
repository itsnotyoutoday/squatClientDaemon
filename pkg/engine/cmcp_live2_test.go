package engine_test

import (
	"cmp"
	"os"
	"testing"
	"time"

	"github.com/itsnotyoutoday/cmcp/go/msg"
	cs "github.com/itsnotyoutoday/cmcp/go/session"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// TestCmcp2LiveConcourse: at the concourse the game sends no context at all, so the client draws nothing there (no screen, no pane), and
// boarding a ship gives the ship scope an owner. SQUAT_LIVE_CMCP2=1.
func TestCmcp2LiveConcourse(t *testing.T) {
	if os.Getenv("SQUAT_LIVE_CMCP2") == "" {
		t.Skip("set SQUAT_LIVE_CMCP2=1 with the v2 testgame on 127.0.0.1:4101 (and its asset server on 4102)")
	}
	t.Setenv("SQUATCLIENT_DEV_LOOPBACK_HTTP", "1")
	e, r := newEngine(t)
	ps, err := engine.OpenPackStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.SetCmcpPacks(ps)
	e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true, PackConsent: "always", RoomScreens: true})
	e.SetCmcp(func() cs.Config {
		return cs.Config{Name: "SquatClient", Version: "live", Kind: "gui", MaxBody: 65536, Protos: []int{2},
			Render: &msg.Render{Kind: "gpu", Surfaces: []string{"screen", "panel", "dock", "float"}}, Themes: []string{"tng"}, Theme: "tng",
			Packs: &msg.HelloPacks{Formats: []int{1}, Inline: true, HTTPS: true}}
	})
	si, err := e.Dial(engine.World{Name: "poc2", Host: "127.0.0.1", Port: 4101}, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	time.Sleep(1500 * time.Millisecond)
	_ = e.Input(si.ID, "connect alice ember-tide-91")
	time.Sleep(800 * time.Millisecond)
	_ = e.Input(si.ID, "home")
	var ui *engine.CmcpUI
	get := func() *engine.CmcpUI { u, _ := e.CmcpUI(si.ID); ui = u; return u }
	r.until(t, "CMCP 2 ready with a definition", func() bool {
		u := get()
		return u != nil && u.State == "ready" && u.V2 && u.Def != nil
	})
	time.Sleep(1500 * time.Millisecond)
	// at the concourse: nothing to draw. No active context, no screen, no pane, and the ship scope has nobody
	u := get()
	if u.Ctx != nil {
		t.Errorf("the concourse sends no context: %+v", u.Ctx)
	}
	if u.Tree != nil {
		t.Errorf("nothing is drawn at the concourse: screen %q", u.Tree.Screen)
	}
	if len(u.Panes) != 0 {
		t.Errorf("no pane at the concourse: %+v", u.Panes)
	}
	if u.Scopes["ship"].Owner != nil {
		t.Errorf("nobody owns the ship scope at the concourse: %+v", u.Scopes["ship"].Owner)
	}
	t.Logf("concourse: ctx=%v tree=%v panes=%d scopes=%v", u.Ctx, u.Tree, len(u.Panes), u.Scopes)
	// boarding gives the ship scope an owner, and leaving takes everything away again
	_ = e.Input(si.ID, "board resolute")
	r.until(t, "aboard: the ship scope has an owner", func() bool {
		u := get()
		return u != nil && u.Scopes["ship"].Owner != nil
	})
	t.Logf("aboard: owner %+v ctx %+v", ui.Scopes["ship"].Owner, ui.Ctx)
	_ = e.Input(si.ID, "home")
	r.until(t, "home: nothing is left of the ship", func() bool {
		u := get()
		return u != nil && u.Scopes["ship"].Owner == nil && u.Ctx == nil && u.Tree == nil && len(u.Panes) == 0
	})
}

// TestCmcp2LiveThemesAndConsoles checks that what worked the v1 way still works the v2 way: the ship's own theme (a downloaded pack) is used,
// a console is drawn with its screen, and the sound pack is loaded. SQUAT_LIVE_CMCP2=1; the ship to board is SQUAT_LIVE_CMCP2_SHIP (default resolute: the SquatClient tests use that ship only, others use theirs).
func TestCmcp2LiveThemesAndConsoles(t *testing.T) {
	if os.Getenv("SQUAT_LIVE_CMCP2") == "" {
		t.Skip("set SQUAT_LIVE_CMCP2=1 with the v2 testgame on 127.0.0.1:4101")
	}
	t.Setenv("SQUATCLIENT_DEV_LOOPBACK_HTTP", "1")
	e, r := newEngine(t)
	ps, err := engine.OpenPackStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.SetCmcpPacks(ps)
	e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true, PackConsent: "always", RoomScreens: true})
	e.SetCmcp(func() cs.Config {
		return cs.Config{Name: "SquatClient", Version: "live", Kind: "gui", MaxBody: 65536, Protos: []int{2},
			Render: &msg.Render{Kind: "gpu", Surfaces: []string{"screen"}}, Themes: []string{"tng", "klingon", "romulan", "tos", "ferengi", "soft", "ops"}, Theme: "tng",
			Audio: &msg.Audio{Play: true, Formats: []string{"wav", "ogg", "mp3"}, Recipes: true, Voices: 16},
			Packs: &msg.HelloPacks{Formats: []int{1}, Inline: true, HTTPS: true}}
	})
	si, err := e.Dial(engine.World{Name: "poc2", Host: "127.0.0.1", Port: 4101}, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	time.Sleep(1500 * time.Millisecond)
	_ = e.Input(si.ID, "connect alice ember-tide-91")
	time.Sleep(800 * time.Millisecond)
	_ = e.Input(si.ID, "home") // a test before this one may have left the account aboard a ship
	var ui *engine.CmcpUI
	get := func() *engine.CmcpUI { u, _ := e.CmcpUI(si.ID); ui = u; return u }
	r.until(t, "ready", func() bool { u := get(); return u != nil && u.State == "ready" && u.V2 && u.Def != nil })
	ship := os.Getenv("SQUAT_LIVE_CMCP2_SHIP")
	if ship == "" {
		ship = "resolute"
	}
	_ = e.Input(si.ID, "board "+ship)
	r.until(t, "aboard "+ship, func() bool { u := get(); return u != nil && u.Scopes["ship"].Owner != nil })
	t.Logf("ship owner: %+v", ui.Scopes["ship"].Owner)
	r.until(t, "the ship's theme is in use", func() bool {
		u := get()
		return u != nil && u.Tree != nil && u.Scopes["ship"].Owner != nil && u.Tree.Theme == u.Scopes["ship"].Owner.Theme
	})
	t.Logf("theme %q source %q version %q screen %q", ui.Tree.Theme, ui.Tree.ThemeSource, ui.Tree.ThemeVersion, ui.Tree.Screen)
	_ = e.Input(si.ID, "man helm")
	ok := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(150 * time.Millisecond) {
		if u := get(); u != nil && u.Ctx != nil && u.Ctx.Type == "ship.console" && u.Tree != nil {
			ok = true
			break
		}
	}
	if !ok {
		u := get()
		ls := r.lines(si.ID)
		tail := ""
		for i := max(0, len(ls)-4); i < len(ls); i++ {
			tail += "\n    " + ls[i].Plain()
		}
		t.Fatalf("no console screen: ctx=%s readonly=%v tree=%v; the game said:%s", ctxType(u), u.Readonly, u.Tree != nil, tail)
	}
	t.Logf("console ctx %+v screen %q tree %q title %q", ui.Ctx, ui.Screen.Resolved, ui.Tree.Screen, ui.Tree.Title)
	time.Sleep(2 * time.Second)
	defer func() { _ = e.Input(si.ID, "home"); time.Sleep(500 * time.Millisecond) }() // leave the account on the concourse for the next test
	if sp := e.CmcpSoundPack(si.ID); sp == nil {
		t.Logf("no sound pack loaded (the game may announce none)")
	} else {
		t.Logf("sound pack loaded")
	}
}

// TestCmcp2LiveEveryConsole mans each console of a ship in turn and reports what SquatClient draws for it. SQUAT_LIVE_CMCP2=1.
func TestCmcp2LiveEveryConsole(t *testing.T) {
	if os.Getenv("SQUAT_LIVE_CMCP2") == "" {
		t.Skip("set SQUAT_LIVE_CMCP2=1 with the v2 testgame on 127.0.0.1:4101")
	}
	t.Setenv("SQUATCLIENT_DEV_LOOPBACK_HTTP", "1")
	e, r := newEngine(t)
	ps, err := engine.OpenPackStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.SetCmcpPacks(ps)
	e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true, PackConsent: "always", RoomScreens: true})
	e.SetCmcp(func() cs.Config {
		return cs.Config{Name: "SquatClient", Version: "live", Kind: "gui", MaxBody: 65536, Protos: []int{2},
			Render: &msg.Render{Kind: "gpu", Surfaces: []string{"screen", "panel", "dock", "float"}}, Themes: []string{"tng", "klingon", "romulan", "tos", "ferengi", "soft", "ops"}, Theme: "tng",
			Packs: &msg.HelloPacks{Formats: []int{1}, Inline: true, HTTPS: true}}
	})
	si, err := e.Dial(engine.World{Name: "poc2", Host: "127.0.0.1", Port: 4101}, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	time.Sleep(1500 * time.Millisecond)
	_ = e.Input(si.ID, "connect alice ember-tide-91")
	time.Sleep(800 * time.Millisecond)
	_ = e.Input(si.ID, "home")
	var ui *engine.CmcpUI
	get := func() *engine.CmcpUI { u, _ := e.CmcpUI(si.ID); ui = u; return u }
	r.until(t, "ready", func() bool { u := get(); return u != nil && u.State == "ready" && u.V2 && u.Def != nil })
	_ = e.Input(si.ID, "board resolute")
	r.until(t, "aboard", func() bool { u := get(); return u != nil && u.Scopes["ship"].Owner != nil })
	defer func() { _ = e.Input(si.ID, "home"); time.Sleep(500 * time.Millisecond) }()
	// the bridge: engineering and damage control are in the engineering room, medical in sickbay, security in the brig
	for _, c := range []string{"helm", "ops", "tac", "sci", "command", "comms"} {
		_ = e.Input(si.ID, "man "+c)
		ok := false
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			u := get()
			if u != nil && u.Ctx != nil && u.Ctx.Type == "ship.console" && u.Tree != nil && u.Tree.Root != nil {
				ok = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !ok {
			ctx := "none"
			if u := get(); u != nil && u.Ctx != nil {
				ctx = u.Ctx.Type + " " + u.Ctx.Layout
			}
			ls := r.lines(si.ID)
			tail := ""
			for i := max(0, len(ls)-6); i < len(ls); i++ {
				tail += " | " + ls[i].Plain()
			}
			t.Errorf("man %s: no console screen (active context: %s); the game said:%s", c, ctx, tail)
			continue
		}
		t.Logf("man %-8s -> ctx %s layout %s screen %q title %q alternates %d readonly %v", c, ui.Ctx.ID, ui.Ctx.Layout, ui.Tree.Screen, ui.Tree.Title, len(ui.Screens), ui.Readonly)
		_ = e.Input(si.ID, "unman")
		time.Sleep(400 * time.Millisecond)
	}
}

// TestCmcp2LiveHitShake: a hit on your own ship shakes the console screen (spec 6.18), at a manned console and in a plain ship room.
// SQUAT_LIVE_CMCP2=1. SQUAT_PREMAN=- skips the warm-up man/unman and then shows the game-side bug: straight after boarding, `man command`
// prints the station in the text but pushes no Cobra.Context, so there is no console screen and nothing to shake.
func TestCmcp2LiveHitShake(t *testing.T) {
	if os.Getenv("SQUAT_LIVE_CMCP2") == "" {
		t.Skip("set SQUAT_LIVE_CMCP2=1 with the v2 testgame on 127.0.0.1:4101")
	}
	t.Setenv("SQUATCLIENT_DEV_LOOPBACK_HTTP", "1")
	e, r := newEngine(t)
	ps, err := engine.OpenPackStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.SetCmcpPacks(ps)
	if os.Getenv("SQUAT_TRAIL") != "" {
		e.SetCmcpLog(func(l string) { t.Log("TRAIL " + l) })
	}

	e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true, PackConsent: "always", HitShake: cmp.Or(os.Getenv("SQUAT_HITSHAKE"), "theme")})
	e.SetCmcp(func() cs.Config {
		return cs.Config{Name: "SquatClient", Version: "live", Kind: "gui", MaxBody: 65536, Protos: []int{2},
			Render: &msg.Render{Kind: "gpu", Surfaces: []string{"screen"}}, Themes: []string{"tng"}, Theme: "tng",
			Packs: &msg.HelloPacks{Formats: []int{1}, Inline: true, HTTPS: true}}
	})
	si, err := e.Dial(engine.World{Name: "poc2", Host: "127.0.0.1", Port: 4101}, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	time.Sleep(1500 * time.Millisecond)
	_ = e.Input(si.ID, "connect alice ember-tide-91")
	time.Sleep(800 * time.Millisecond)
	_ = e.Input(si.ID, "home")
	get := func() *engine.CmcpUI { u, _ := e.CmcpUI(si.ID); return u }
	r.until(t, "ready", func() bool { u := get(); return u != nil && u.State == "ready" && u.V2 && u.Def != nil })
	_ = e.Input(si.ID, "board resolute")
	r.until(t, "aboard", func() bool { u := get(); return u != nil && u.Scopes["ship"].Owner != nil })
	// man another console first and give it up again: straight after boarding the game does not push a context for the command console
	// (see the note on this test), so this makes the test about the shake and not about that
	if pre := cmp.Or(os.Getenv("SQUAT_PREMAN"), "helm"); pre != "-" {
		_ = e.Input(si.ID, "man "+pre)
		time.Sleep(900 * time.Millisecond)
		_ = e.Input(si.ID, "unman")
		time.Sleep(900 * time.Millisecond)
	}
	_ = e.Input(si.ID, "man "+cmp.Or(os.Getenv("SQUAT_CONSOLE"), "command"))
	ok := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(150 * time.Millisecond) {
		if u := get(); u != nil && u.Ctx != nil && u.Ctx.Type == "ship.console" && u.Tree != nil {
			ok = true
			break
		}
	}
	if !ok {
		u := get()
		ls := r.lines(si.ID)
		tail := ""
		for i := max(0, len(ls)-22); i < len(ls); i++ {
			tail += " | " + ls[i].Plain()
		}
		t.Fatalf("no console screen: ctx=%s readonly=%v tree=%v; the game said:%s", ctxType(u), u.Readonly, u.Tree != nil, tail)
	}
	defer func() {
		_ = e.Input(si.ID, "unman")
		time.Sleep(300 * time.Millisecond)
		_ = e.Input(si.ID, "home")
		time.Sleep(500 * time.Millisecond)
	}()
	u := get()
	t.Logf("aboard at a console: ctx=%s ambience=%v tree=%q", u.Ctx.Type, u.Ambience, u.Tree.Screen)
	before := len(r.snapshot())
	_ = e.Input(si.ID, "simulatehit")
	var hits, shakes int
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		hits, shakes = 0, 0
		for _, ev := range r.snapshot()[before:] {
			for _, n := range ev.CmcpNotes {
				if n.Kind == "event" && n.Event != nil && n.Event.Type == "hit" {
					hits++
				}
			}
			if ev.Kind == engine.EvCmcpPulse && ev.CmcpPulse != nil && ev.CmcpPulse.Shake != nil && ev.CmcpPulse.Shake.Active {
				shakes++
			}
		}
		if hits > 0 && shakes > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("at a console: %d hit event(s), %d shake sample(s)", hits, shakes)
	if hits == 0 {
		t.Fatal("the game sent no hit event")
	}
	if shakes == 0 {
		t.Error("a hit on your own ship shakes the screen: no shake sample was published")
	}
	// the same hit in a plain ship room (no console, and the player's room screens are off, so nothing is drawn)
	_ = e.Input(si.ID, "unman")
	time.Sleep(700 * time.Millisecond)
	u = get()
	t.Logf("in the ship room: ctx=%v ambience=%v tree=%v", ctxType(u), u.Ambience, u.Tree != nil)
	before = len(r.snapshot())
	_ = e.Input(si.ID, "simulatehit")
	hits, shakes = 0, 0
	deadline = time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		hits, shakes = 0, 0
		for _, ev := range r.snapshot()[before:] {
			for _, n := range ev.CmcpNotes {
				if n.Kind == "event" && n.Event != nil && n.Event.Type == "hit" {
					hits++
				}
			}
			if ev.Kind == engine.EvCmcpPulse && ev.CmcpPulse != nil && ev.CmcpPulse.Shake != nil && ev.CmcpPulse.Shake.Active {
				shakes++
			}
		}
		if hits > 0 && shakes > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("in a ship room: %d hit event(s), %d shake sample(s)", hits, shakes)
}

func ctxType(u *engine.CmcpUI) string {
	if u == nil || u.Ctx == nil {
		return "(none)"
	}
	return u.Ctx.Type
}
