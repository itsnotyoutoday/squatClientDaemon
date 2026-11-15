package engine_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/cmcp/msg"
	cs "github.com/itsnotyoutoday/cmcp/session"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// TestCmcpLiveEvennia runs this client against the Evennia proof of concept on 127.0.0.1:4101 (a local throwaway game).
// SQUAT_LIVE_CMCP=1 turns it on; SQUAT_LIVE_CMCP_MAN=1 also mans the operations console and gives it up again.
// Never point it at any other game.
func TestCmcpLiveEvennia(t *testing.T) {
	if os.Getenv("SQUAT_LIVE_CMCP") == "" {
		t.Skip("set SQUAT_LIVE_CMCP=1 with the Evennia PoC on 127.0.0.1:4101")
	}
	e, r := newEngine(t)
	ps, err := engine.OpenPackStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.SetCmcpPacks(ps)
	e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true}) // the app's default: follow the ship
	e.SetCmcp(func() cs.Config {
		return cs.Config{Name: "SquatClient", Version: "live", Kind: "gui", MaxBody: 65536, Render: &msg.Render{Kind: "gpu"}, Themes: []string{"tng"}, Theme: "tng"}
	})
	si, err := e.Dial(engine.World{Name: "poc", Host: "127.0.0.1", Port: 4101}, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	time.Sleep(1500 * time.Millisecond)
	login := "connect alice ember-tide-91"
	if a := os.Getenv("SQUAT_LIVE_CMCP_ACCOUNT"); a != "" { // e.g. "bob cobalt-fern-52"
		login = "connect " + a
	}
	_ = e.Input(si.ID, login)
	var ui *engine.CmcpUI
	r.until(t, "CMCP ready", func() bool {
		u, err := e.CmcpUI(si.ID)
		ui = u
		return err == nil && u.State == "ready"
	})
	t.Logf("ready: proto %d server %+v packs %d commands %d", ui.Proto, ui.Server, len(ui.Welcome.Packs), len(ui.Welcome.Commands))
	if os.Getenv("SQUAT_LIVE_CMCP_MAN") == "" {
		return
	}
	go func() { // the player says yes to the game's interface
		answered := map[string]bool{}
		for i := 0; i < 400; i++ {
			for _, ev := range r.snapshot() {
				for _, n := range ev.CmcpNotes {
					if n.Kind == "pack-consent" && n.Pack != nil && !answered[n.Pack.ID] {
						answered[n.Pack.ID] = true
						_ = e.CmcpPackConsent(si.ID, n.Pack.ID, true)
					}
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()
	if b := os.Getenv("SQUAT_LIVE_CMCP_BOARD"); b != "" { // e.g. "ship.board drakhar"
		_ = e.Input(si.ID, b)
		time.Sleep(1500 * time.Millisecond)
	}
	_ = e.Input(si.ID, "man opera")
	r.until(t, "console + screen", func() bool {
		u, _ := e.CmcpUI(si.ID)
		ui = u
		return u != nil && u.Tree != nil
	})
	t.Logf("screen %s title %q theme %s data %d docs seqs %v", ui.Tree.Screen, ui.Tree.Title, ui.Tree.Theme, len(ui.Docs), ui.Seqs)
	time.Sleep(4 * time.Second)
	u, _ := e.CmcpUI(si.ID)
	if dir := os.Getenv("SQUAT_LIVE_CMCP_DUMP"); dir != "" {
		b, _ := json.MarshalIndent(u, "", " ")
		_ = os.WriteFile(filepath.Join(dir, "live-operations.json"), b, 0o644)
		if sp, err := e.CmcpSprite(si.ID, "marauder"); err == nil {
			_ = os.WriteFile(filepath.Join(dir, "live.sprite.json"), sp, 0o644)
		}
	}
	if u2, _ := e.CmcpUI(si.ID); u2 != nil && u2.Ship != nil {
		t.Logf("ship %s empire %q theme %q pack %+v; tree theme %q (%s) screen %s fallback %v", u2.Ship.ID, u2.Ship.Empire, u2.Ship.Theme, u2.Ship.Pack, u2.Tree.Theme, u2.Tree.ThemeSource, u2.Tree.Screen, u2.Screen.Fallback)
	}
	t.Logf("after 2s seqs %v pending %d fallback %v", u.Seqs, u.Pending, u.Screen.Fallback)
	for _, l := range r.lines(si.ID) {
		if strings.Contains(l.Plain(), "cmcp") {
			t.Log(l.Plain())
		}
	}
	_ = e.Input(si.ID, "unman")
}

// TestCmcpLiveThemes boards the ships named in SQUAT_LIVE_CMCP_SHIPS and mans the consoles in SQUAT_LIVE_CMCP_CONSOLES, logging the theme id of
// every screen (one theme per ship is expected). Local throwaway game on 127.0.0.1:4101 only.
func TestCmcpLiveThemes(t *testing.T) {
	ships := os.Getenv("SQUAT_LIVE_CMCP_SHIPS")
	if os.Getenv("SQUAT_LIVE_CMCP") == "" || ships == "" {
		t.Skip("set SQUAT_LIVE_CMCP=1 SQUAT_LIVE_CMCP_SHIPS=drakhar,... SQUAT_LIVE_CMCP_CONSOLES=opera,...")
	}
	e, r := newEngine(t)
	ps, _ := engine.OpenPackStore(t.TempDir())
	e.SetCmcpPacks(ps)
	e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true})
	e.SetCmcp(func() cs.Config {
		return cs.Config{Name: "SquatClient", Version: "live", Kind: "gui", MaxBody: 65536, Render: &msg.Render{Kind: "gpu"}, Themes: []string{"tng"}, Theme: "tng"}
	})
	si, err := e.Dial(engine.World{Name: "poc", Host: "127.0.0.1", Port: 4101}, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	time.Sleep(1500 * time.Millisecond)
	acct := os.Getenv("SQUAT_LIVE_CMCP_ACCOUNT")
	if acct == "" {
		acct = "bob cobalt-fern-52"
	}
	_ = e.Input(si.ID, "connect "+acct)
	r.until(t, "ready", func() bool { u, err := e.CmcpUI(si.ID); return err == nil && u.State == "ready" })
	go func() { // consent: yes
		answered := map[string]bool{}
		for i := 0; i < 2000; i++ {
			for _, ev := range r.snapshot() {
				for _, n := range ev.CmcpNotes {
					if n.Kind == "pack-consent" && n.Pack != nil && !answered[n.Pack.ID] {
						answered[n.Pack.ID] = true
						_ = e.CmcpPackConsent(si.ID, n.Pack.ID, true)
					}
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()
	for _, sh := range strings.Split(ships, ",") {
		_ = e.Input(si.ID, "board "+sh)
		time.Sleep(1500 * time.Millisecond)
		seen := map[string]bool{}
		for _, c := range strings.Split(os.Getenv("SQUAT_LIVE_CMCP_CONSOLES"), ",") {
			_ = e.Input(si.ID, "man "+c)
			time.Sleep(1800 * time.Millisecond)
			u, _ := e.CmcpUI(si.ID)
			if u == nil || u.Tree == nil || u.Ship == nil {
				t.Logf("%s / man %s: no screen", sh, c)
				continue
			}
			seen[u.Tree.Theme] = true
			if dir := os.Getenv("SQUAT_LIVE_CMCP_DUMP"); dir != "" { // the bound UI of this ship's console, for the screenshot script
				_ = e.CmcpViewport(si.ID, 1300, 1, 14) // a 1300 px wide window: the core decides the side layout and the cap
				time.Sleep(600 * time.Millisecond)
				u, _ = e.CmcpUI(si.ID)
				b, _ := json.MarshalIndent(u, "", " ")
				_ = os.WriteFile(filepath.Join(dir, "live-"+sh+"-"+c+".json"), b, 0o644)
				if sp, err := e.CmcpSprite(si.ID, "marauder"); err == nil {
					_ = os.WriteFile(filepath.Join(dir, "live.sprite.json"), sp, 0o644)
				}
			}
			t.Logf("%s / man %-6s ship=%s shipTheme=%q consoleTheme=%q -> screen %s theme %s (%s)", sh, c, u.Ship.ID, u.ShipTheme, u.ConTheme, u.Tree.Screen, u.Tree.Theme, u.Tree.ThemeSource)
		}
		t.Logf("%s: distinct themes %v", sh, seen)
		_ = e.Input(si.ID, "unman")
	}
}

// TestCmcpLiveSound: a live check of the sound chain against the Evennia PoC (SQUAT_LIVE_CMCP_SOUND=1, SQUATCLIENT_DEV_LOOPBACK_HTTP=1):
// Hello declares audio, the sound pack is announced, fetched and parsed, a red alert makes the game send Sound.Play and the engine
// publishes the sound notes. It logs every step so a break in the chain shows where.
func TestCmcpLiveSound(t *testing.T) {
	if os.Getenv("SQUAT_LIVE_CMCP_SOUND") == "" {
		t.Skip("set SQUAT_LIVE_CMCP_SOUND=1 with the Evennia PoC on 127.0.0.1:4101")
	}
	e, r := newEngine(t)
	ps, err := engine.OpenPackStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.SetCmcpPacks(ps)
	e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true})
	e.SetCmcp(func() cs.Config {
		return cs.Config{Name: "SquatClient", Version: "live", Kind: "gui", MaxBody: 65536, Render: &msg.Render{Kind: "gpu"}, Themes: []string{"tng"}, Theme: "tng",
			Audio: &msg.Audio{Play: true, Formats: []string{"wav", "ogg", "mp3"}, Recipes: true, Voices: 16}, Logf: t.Logf}
	})
	si, err := e.Dial(engine.World{Name: "poc", Host: "127.0.0.1", Port: 4101}, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	time.Sleep(1500 * time.Millisecond)
	login := "connect alice ember-tide-91"
	if a := os.Getenv("SQUAT_LIVE_CMCP_ACCOUNT"); a != "" {
		login = "connect " + a
	}
	_ = e.Input(si.ID, login)
	var ui *engine.CmcpUI
	r.until(t, "CMCP ready", func() bool { u, err := e.CmcpUI(si.ID); ui = u; return err == nil && u.State == "ready" })
	for _, p := range ui.Welcome.Packs {
		t.Logf("announced pack %s kind %s version %s inline %v size %d", p.ID, p.Kind, p.Version, p.Inline, p.Size)
	}
	if b := os.Getenv("SQUAT_LIVE_CMCP_BOARD"); b != "" {
		_ = e.Input(si.ID, b)
		time.Sleep(1500 * time.Millisecond)
	}
	_ = e.Input(si.ID, "man opera")
	r.until(t, "console", func() bool { u, _ := e.CmcpUI(si.ID); return u != nil && u.Tree != nil })
	time.Sleep(3 * time.Second)
	t.Logf("sound pack loaded: %v", e.CmcpSoundPack(si.ID) != nil)
	_ = e.Input(si.ID, "alert red")
	time.Sleep(3 * time.Second)
	n := 0
	for _, ev := range r.snapshot() {
		for _, note := range ev.CmcpNotes {
			if note.Kind == "sound" || note.Kind == "ui-sound" {
				n++
				if note.Sound != nil {
					t.Logf("sound note: event %q sound %q loop %v key %q", note.Sound.Event, note.Sound.Sound, note.Sound.Loop, note.Sound.Key)
				} else {
					t.Logf("sound note: stop %+v stopAll %v", note.Stop, note.StopAll)
				}
			}
		}
	}
	t.Logf("%d sound notes; pack loaded %v", n, e.CmcpSoundPack(si.ID) != nil)
	if sp := e.CmcpSoundPack(si.ID); sp != nil {
		t.Logf("sound pack %s %s: %d files, %d recipes, %d events", sp.ID, sp.Version, len(sp.Files), len(sp.Recipes), len(sp.Events))
		for name := range sp.Files {
			b, ct, err := e.CmcpSample(si.ID, name)
			t.Logf("sample %-22s %6d bytes %s err=%v", name, len(b), ct, err)
		}
	}
}

// TestCmcpLiveShake watches a live session for `hit` events on the own ship and the shake samples the engine publishes for them
// (SQUAT_LIVE_CMCP_SHAKE=<seconds>, SQUATCLIENT_DEV_LOOPBACK_HTTP=1; Evennia PoC on 127.0.0.1:4101 only).
func TestCmcpLiveShake(t *testing.T) {
	secs := os.Getenv("SQUAT_LIVE_CMCP_SHAKE")
	if secs == "" {
		t.Skip("set SQUAT_LIVE_CMCP_SHAKE=60 with the Evennia PoC on 127.0.0.1:4101")
	}
	n, _ := strconv.Atoi(secs)
	e, r := newEngine(t)
	ps, _ := engine.OpenPackStore(t.TempDir())
	e.SetCmcpPacks(ps)
	e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true})
	e.SetCmcp(func() cs.Config {
		return cs.Config{Name: "SquatClient", Version: "live", Kind: "gui", MaxBody: 65536, Render: &msg.Render{Kind: "gpu"}, Themes: []string{"tng"}, Theme: "tng"}
	})
	si, err := e.Dial(engine.World{Name: "poc", Host: "127.0.0.1", Port: 4101}, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	time.Sleep(1500 * time.Millisecond)
	_ = e.Input(si.ID, "connect bob cobalt-fern-52")
	r.until(t, "ready", func() bool { u, err := e.CmcpUI(si.ID); return err == nil && u.State == "ready" })
	_ = e.Input(si.ID, "board "+envOr("SQUAT_LIVE_CMCP_BOARD", "resolute"))
	time.Sleep(1500 * time.Millisecond)
	_ = e.Input(si.ID, "man opera")
	time.Sleep(2 * time.Second)
	hits, shakes := 0, 0
	for i := 0; i < n*4; i++ {
		time.Sleep(250 * time.Millisecond)
	}
	for _, ev := range r.snapshot() {
		for _, note := range ev.CmcpNotes {
			if note.Kind == "event" && note.Event != nil {
				t.Logf("event %s %v", note.Event.Type, note.Event.Data)
				if note.Event.Type == "hit" {
					hits++
				}
			}
		}
		if ev.CmcpPulse != nil && ev.CmcpPulse.Shake != nil {
			shakes++
		}
	}
	t.Logf("%d hit events, %d shake samples", hits, shakes)
	if dir := os.Getenv("SQUAT_LIVE_CMCP_DUMP"); dir != "" { // the live samples (shake offsets and the ambience) for the screenshot script
		var out []engine.CmcpPulse
		for _, ev := range r.snapshot() {
			if p := ev.CmcpPulse; p != nil && (p.Shake != nil || p.Ambient != nil) {
				out = append(out, *p)
			}
		}
		b, _ := json.MarshalIndent(out, "", " ")
		_ = os.WriteFile(filepath.Join(dir, "live-pulses.json"), b, 0o644)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// TestCmcpLiveRoom: what the game sends for a player who is aboard a ship but not manning a console (a room, the dock): the context stack, the
// screen and whether the engine draws a CMCP screen (SQUAT_LIVE_CMCP_ROOM=1; Evennia PoC on 127.0.0.1:4101 only).
func TestCmcpLiveRoom(t *testing.T) {
	if os.Getenv("SQUAT_LIVE_CMCP_ROOM") == "" {
		t.Skip("set SQUAT_LIVE_CMCP_ROOM=1")
	}
	e, r := newEngine(t)
	ps, _ := engine.OpenPackStore(t.TempDir())
	e.SetCmcpPacks(ps)
	e.SetCmcpRender(engine.CmcpRender{Theme: "tng", UseShipTheme: true})
	e.SetCmcp(func() cs.Config {
		return cs.Config{Name: "SquatClient", Version: "live", Kind: "gui", MaxBody: 65536, Render: &msg.Render{Kind: "gpu"}, Themes: []string{"tng"}, Theme: "tng"}
	})
	si, err := e.Dial(engine.World{Name: "poc", Host: "127.0.0.1", Port: 4101}, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	time.Sleep(1500 * time.Millisecond)
	_ = e.Input(si.ID, "connect bob cobalt-fern-52")
	r.until(t, "ready", func() bool { u, err := e.CmcpUI(si.ID); return err == nil && u.State == "ready" })
	show := func(label string) {
		time.Sleep(2 * time.Second)
		u, _ := e.CmcpUI(si.ID)
		var stack []string
		for _, s := range u.Stack {
			stack = append(stack, s.Kind+":"+s.ID+"->"+s.Screen)
		}
		scr := ""
		if u.Tree != nil {
			scr = u.Tree.Screen
		}
		t.Logf("%s: console=%v screen=%q resolved=%q tree=%q readonly=%v stack=%v alert=%q", label, u.Console != nil, u.Screen.ID, u.Screen.Resolved, scr, u.Readonly, stack, u.Alert)
	}
	show("logged in")
	_ = e.Input(si.ID, "board "+envOr("SQUAT_LIVE_CMCP_BOARD", "resolute"))
	show("boarded, no console")
	if dir := os.Getenv("SQUAT_LIVE_CMCP_DUMP"); dir != "" { // the room as the game and the engine produce it, and the shakes of the hits that arrive
		_ = e.CmcpViewport(si.ID, 1300, 1, 14)
		for _, lvl := range []string{"yellow", "red"} {
			_ = e.Input(si.ID, "alert "+lvl)
			time.Sleep(2500 * time.Millisecond)
			u, _ := e.CmcpUI(si.ID)
			b, _ := json.MarshalIndent(u, "", " ")
			_ = os.WriteFile(filepath.Join(dir, "live-room-"+lvl+".json"), b, 0o644)
		}
		var shakes []engine.CmcpPulse
		for i := 0; i < 240 && len(shakes) < 12; i++ {
			time.Sleep(250 * time.Millisecond)
		}
		for _, ev := range r.snapshot() {
			if ev.CmcpPulse != nil && ev.CmcpPulse.Shake != nil && (ev.CmcpPulse.Shake.X != 0 || ev.CmcpPulse.Shake.Y != 0) {
				shakes = append(shakes, *ev.CmcpPulse)
			}
		}
		b, _ := json.MarshalIndent(shakes, "", " ")
		_ = os.WriteFile(filepath.Join(dir, "live-room-shakes.json"), b, 0o644)
		t.Logf("room: %d shake samples from live hits", len(shakes))
	}
	_ = e.Input(si.ID, "look")
	show("look")
	_ = e.Input(si.ID, "man opera")
	show("manned")
	_ = e.Input(si.ID, "unman")
	show("unmanned")
}
