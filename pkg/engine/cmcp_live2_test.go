package engine_test

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/cmcp/go/msg"
	cs "github.com/itsnotyoutoday/cmcp/go/session"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// TestCmcp2LiveConcourse runs this client against the local CMCP 2 test game on 127.0.0.1:4101 (the Evennia testgame in v2 mode, a throwaway
// game): the definition is fetched, the concourse (world.place) is drawn as a screen, boarding a ship adds the ship scope's owner, and going
// home takes it away again. SQUAT_LIVE_CMCP2=1 turns it on. Never point it at any other game.
func TestCmcp2LiveConcourse(t *testing.T) {
	if os.Getenv("SQUAT_LIVE_CMCP2") == "" {
		t.Skip("set SQUAT_LIVE_CMCP2=1 with the v2 testgame on 127.0.0.1:4101 (and its asset server on 4102)")
	}
	t.Setenv("SQUATCLIENT_DEV_LOOPBACK_HTTP", "1") // the game's definition and packs are served over plain http on loopback
	e, r := newEngine(t)
	ps, err := engine.OpenPackStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.SetCmcpPacks(ps)
	e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true, PackConsent: "always"})
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
	_ = e.Input(si.ID, "home") // a test before this one may have left the account aboard a ship
	var ui *engine.CmcpUI
	get := func() *engine.CmcpUI { u, _ := e.CmcpUI(si.ID); ui = u; return u }
	r.until(t, "CMCP 2 ready with a definition", func() bool {
		u := get()
		return u != nil && u.State == "ready" && u.V2 && u.Def != nil
	})
	t.Logf("ready: proto %d def %+v scopes %v", ui.Proto, ui.Def, ui.Scopes)
	r.until(t, "the concourse is drawn", func() bool {
		u := get()
		return u != nil && u.Ctx != nil && u.Tree != nil
	})
	t.Logf("ctx %+v screen %q tree %q title %q", ui.Ctx, ui.Screen.Resolved, ui.Tree.Screen, ui.Tree.Title)
	if ui.Ctx.Type != "world.place" || ui.Ctx.Surface != "screen" {
		t.Fatalf("the player lands on the concourse as a screen: %+v", ui.Ctx)
	}
	_ = e.Input(si.ID, "board resolute")
	r.until(t, "aboard: the ship scope has an owner", func() bool {
		u := get()
		return u != nil && u.Scopes["ship"].Owner != nil
	})
	t.Logf("aboard: owner %+v ctx %+v tree %v", ui.Scopes["ship"].Owner, ui.Ctx, ui.Tree != nil)
	_ = e.Input(si.ID, "home")
	r.until(t, "home: the ship scope has no owner again", func() bool {
		u := get()
		return u != nil && u.Scopes["ship"].Owner == nil && u.Ctx != nil && u.Ctx.Type == "world.place"
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
	e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true, PackConsent: "always"})
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
	r.until(t, "ready", func() bool { u := get(); return u != nil && u.State == "ready" && u.V2 && u.Tree != nil })
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
	r.until(t, "a console screen", func() bool {
		u := get()
		return u != nil && u.Ctx != nil && u.Ctx.Type == "ship.console" && u.Tree != nil
	})
	t.Logf("console ctx %+v screen %q tree %q title %q", ui.Ctx, ui.Screen.Resolved, ui.Tree.Screen, ui.Tree.Title)
	time.Sleep(2 * time.Second)
	defer func() { _ = e.Input(si.ID, "home"); time.Sleep(500 * time.Millisecond) }() // leave the account on the concourse for the next test
	if sp := e.CmcpSoundPack(si.ID); sp == nil {
		t.Logf("no sound pack loaded (the game may announce none)")
	} else {
		t.Logf("sound pack loaded")
	}
}

// TestCmcp2LivePanes: on the concourse the game also pushes world.briefing (a dock): the client gets the main screen AND a pane with its own bound tree,
// without a text port. SQUAT_LIVE_CMCP2=1.
func TestCmcp2LivePanes(t *testing.T) {
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
	e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true, PackConsent: "always"})
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
	_ = e.Input(si.ID, "home") // a test before this one may have left the account aboard a ship
	var ui *engine.CmcpUI
	get := func() *engine.CmcpUI { u, _ := e.CmcpUI(si.ID); ui = u; return u }
	r.until(t, "the main screen and a dock pane with a tree", func() bool {
		u := get()
		return u != nil && u.Tree != nil && len(u.Panes) > 0 && u.Panes[0].Tree != nil
	})
	p := ui.Panes[0]
	t.Logf("main screen %q; pane %s on %s (%s) layout %q title %q dock %q size %v readonly %v commands %v", ui.Tree.Screen, p.Key, p.Surface, p.Type, p.Layout, p.Title, p.Dock, p.Size, p.Readonly, p.Commands)
	var walk func(n *engine.CmcpNode, f func(*engine.CmcpNode))
	walk = func(n *engine.CmcpNode, f func(*engine.CmcpNode)) {
		if n == nil {
			return
		}
		f(n)
		for _, c := range n.Children {
			walk(c, f)
		}
	}
	buttons := 0
	walk(p.Tree.Root, func(n *engine.CmcpNode) {
		if n.Widget == "textport" {
			t.Errorf("a pane draws no second text port")
		}
		if n.Widget == "button" {
			buttons++
		}
	})
	t.Logf("pane widgets: %d buttons", buttons)
	if p.Surface == "screen" {
		t.Fatalf("an off-screen context is not presented on screen: %+v", p)
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
	e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true, PackConsent: "always"})
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
	r.until(t, "ready", func() bool { u := get(); return u != nil && u.State == "ready" && u.V2 && u.Tree != nil })
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

// TestCmcp2LivePaneTabs: the guide pane's tab buttons write $ui of THAT pane and its list re-binds to the chosen page. SQUAT_LIVE_CMCP2=1.
func TestCmcp2LivePaneTabs(t *testing.T) {
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
	e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true, PackConsent: "always"})
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
	r.until(t, "the guide pane with a tree", func() bool {
		u := get()
		return u != nil && len(u.Panes) > 0 && u.Panes[0].Tree != nil
	})
	walk := func(n *engine.CmcpNode, f func(*engine.CmcpNode)) {
		var rec func(*engine.CmcpNode)
		rec = func(x *engine.CmcpNode) {
			if x == nil {
				return
			}
			f(x)
			for _, c := range x.Children {
				rec(c)
			}
		}
		rec(n)
	}
	// what the pane holds: the buttons (one per tab) and the list of the page in view
	buttons := map[string]string{} // text -> path
	var listPath string
	rowsOf := func(p *engine.CmcpPane) []string {
		var out []string
		walk(p.Tree.Root, func(n *engine.CmcpNode) {
			if n.Widget != "list" {
				return
			}
			listPath = n.Path
			// rows are a list, or a map keyed "01", "02", ... (the client's own entries() takes either)
			switch rows := n.Props["rows"].(type) {
			case []any:
				for _, x := range rows {
					if m, ok := x.(map[string]any); ok {
						out = append(out, fmt.Sprint(m["text"]))
					}
				}
			case map[string]any:
				keys := make([]string, 0, len(rows))
				for k := range rows {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					if m, ok := rows[k].(map[string]any); ok {
						out = append(out, fmt.Sprint(m["text"]))
					}
				}
			}
		})
		return out
	}
	p := &ui.Panes[0]
	walk(p.Tree.Root, func(n *engine.CmcpNode) {
		if n.Widget == "button" {
			buttons[strings.ToLower(fmt.Sprint(n.Props["text"]))] = n.Path
		}
		if n.Widget == "textport" {
			t.Error("the guide pane draws no text port")
		}
	})
	first := rowsOf(p)
	t.Logf("pane %q: %d buttons %v, list %q with %d rows; first row %q", p.Title, len(buttons), keysOf(buttons), listPath, len(first), firstOf(first))
	if len(buttons) < 6 {
		t.Fatalf("the six tab buttons are there: %v", keysOf(buttons))
	}
	// each tab button writes $ui of this pane and the list re-binds to that page
	for _, tab := range []string{"test", "clients", "welcome"} {
		path, ok := buttons[tab]
		if !ok {
			t.Errorf("no %q tab", tab)
			continue
		}
		if _, err := e.CmcpPaneEvent(si.ID, p.Key, path, "click", nil); err != nil {
			t.Fatalf("%s tab: %v", tab, err)
		}
		var rows []string
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if u := get(); u != nil && len(u.Panes) > 0 && u.Panes[0].Tree != nil {
				rows = rowsOf(&u.Panes[0])
				if len(rows) > 0 && (tab == "welcome") == (strings.Join(rows, "|") == strings.Join(first, "|")) {
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		if len(rows) == 0 {
			t.Errorf("tab %q: the list is empty after the click", tab)
			continue
		}
		same := strings.Join(rows, "|") == strings.Join(first, "|")
		if tab == "welcome" && !same {
			t.Errorf("tab welcome: back to the first page, got %q", firstOf(rows))
		}
		if tab != "welcome" && same {
			t.Errorf("tab %q: the list did not change (still %q)", tab, firstOf(rows))
		}
		t.Logf("tab %-8s -> %d rows, first %q", tab, len(rows), firstOf(rows))
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func firstOf(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
