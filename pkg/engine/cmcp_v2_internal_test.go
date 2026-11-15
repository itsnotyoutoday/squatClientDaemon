package engine

import (
	"testing"

	"github.com/itsnotyoutoday/cmcp/go/msg"
	cs "github.com/itsnotyoutoday/cmcp/go/session"
)

// The gate the telnet layer declares follows the versions the client was configured to speak (spec 20.2.1): "Cobra 2" for a v2 client,
// both (highest first) for a bridging one, and nothing special (the telnet layer's own default) when the config names none.
func TestCmcpGate(t *testing.T) {
	cases := []struct {
		protos []int
		want   string
	}{{nil, ""}, {[]int{1}, `["Cobra 1"]`}, {[]int{2}, `["Cobra 2"]`}, {[]int{1, 2}, `["Cobra 2","Cobra 1"]`}}
	for _, c := range cases {
		e := &Engine{}
		protos := c.protos
		e.SetCmcp(func() cs.Config { return cs.Config{Protos: protos} })
		if got := e.cmcpGate("w"); got != c.want {
			t.Errorf("protos %v: gate %q, want %q", c.protos, got, c.want)
		}
	}
	if got := (&Engine{}).cmcpGate("w"); got != "" {
		t.Errorf("CMCP off: no gate, got %q", got)
	}
}

// A v2 snapshot reaches the window as scopes with their owners, the active context and its stack (kind = the context type); a v1 snapshot has none of it.
func TestCmcpDTOV2(t *testing.T) {
	u := &cs.UI{V2: true, State: cs.StateReady, Proto: 2, Def: &msg.DefRef{ID: "fspace", Version: "2.0.0", SHA256: "abc"},
		Scopes:  map[string]cs.ScopeView{"world": {Name: "world", Owner: &msg.ScopeOwner{ID: "testgame", Epoch: 1}}, "ship": {Name: "ship"}},
		CtxType: "world.place", CtxScope: "world", CtxKey: "place:concourse", CtxID: "concourse", CtxLayout: "fspace-concourse/concourse", CtxSurface: "screen",
		CtxData: map[string]any{"name": "Spacedock Concourse"}, Layouts: []string{"fspace-concourse/concourse-compact"},
		Stack2: []cs.StackEntryV2{{ContextEntryV2: msg.ContextEntryV2{Key: "place:concourse", Type: "world.place", ID: "concourse"}, Active: true, EffectiveLayout: "fspace-concourse/concourse"}}}
	d := cmcpDTO("s1", u)
	if !d.V2 || d.Def == nil || d.Def.ID != "fspace" || d.Proto != 2 {
		t.Fatalf("v2 and the definition: %+v", d)
	}
	if d.Scopes["world"].Owner == nil || d.Scopes["ship"].Owner != nil {
		t.Fatalf("the world scope has an owner, the ship scope has none: %+v", d.Scopes)
	}
	if d.Ctx == nil || d.Ctx.Type != "world.place" || d.Ctx.Surface != "screen" || d.Ctx.Data["name"] != "Spacedock Concourse" || len(d.Ctx.Layouts) != 1 {
		t.Fatalf("the active context: %+v", d.Ctx)
	}
	if len(d.Stack) != 1 || d.Stack[0].Kind != "world.place" || !d.Stack[0].Active || d.Stack[0].Screen != "fspace-concourse/concourse" {
		t.Fatalf("the v2 stack: %+v", d.Stack)
	}
	if v1 := cmcpDTO("s1", &cs.UI{State: cs.StateReady}); v1.V2 || v1.Scopes != nil || v1.Ctx != nil {
		t.Fatalf("a v1 snapshot carries none of it: %+v", v1)
	}
}

// A world declares exactly the one version it was set to; a world with no choice uses the client's config.
func TestCmcpGatePerWorld(t *testing.T) {
	e := &Engine{}
	e.SetCmcp(func() cs.Config { return cs.Config{Protos: []int{2}} })
	e.SetWorldCmcpProto(map[string]int{"Old Game": 1, "junk": 7})
	if got := e.cmcpGate("old game"); got != `["Cobra 1"]` {
		t.Errorf("a world set to 1 declares only Cobra 1 (the name is case-insensitive): %q", got)
	}
	if got := e.cmcpGate("New Game"); got != `["Cobra 2"]` {
		t.Errorf("a world with no choice follows the config: %q", got)
	}
	if got := e.cmcpGate("junk"); got != `["Cobra 2"]` {
		t.Errorf("an invalid version is ignored: %q", got)
	}
}
