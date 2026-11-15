package engine_test

import (
	"os"
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
