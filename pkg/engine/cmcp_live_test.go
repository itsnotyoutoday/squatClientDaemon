package engine_test

import (
	"os"
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
	e.SetCmcp(func() cs.Config {
		return cs.Config{Name: "SquatClient", Version: "live", Kind: "gui", MaxBody: 65536, Render: &msg.Render{Kind: "gpu"}, Themes: []string{"tng"}, Theme: "tng"}
	})
	si, err := e.Dial(engine.World{Name: "poc", Host: "127.0.0.1", Port: 4101}, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	time.Sleep(1500 * time.Millisecond)
	_ = e.Input(si.ID, "connect alice ember-tide-91")
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
	_ = e.Input(si.ID, "man opera")
	r.until(t, "console + screen", func() bool {
		u, _ := e.CmcpUI(si.ID)
		ui = u
		return u != nil && u.Tree != nil
	})
	t.Logf("screen %s title %q theme %s data %d docs seqs %v", ui.Tree.Screen, ui.Tree.Title, ui.Tree.Theme, len(ui.Docs), ui.Seqs)
	time.Sleep(2 * time.Second)
	u, _ := e.CmcpUI(si.ID)
	t.Logf("after 2s seqs %v pending %d fallback %v", u.Seqs, u.Pending, u.Screen.Fallback)
	for _, l := range r.lines(si.ID) {
		if strings.Contains(l.Plain(), "cmcp") {
			t.Log(l.Plain())
		}
	}
	_ = e.Input(si.ID, "unman")
}
