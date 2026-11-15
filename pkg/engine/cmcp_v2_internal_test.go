package engine

import (
	"testing"

	"github.com/itsnotyoutoday/cmcp/go/defs"
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

// The status strip reads the always-subscribed package of a scope with an owner as the definition names it, not a hard-coded document.
func TestStatusDocFromTheDefinition(t *testing.T) {
	doc, err := defs.Parse([]byte(`{"format":1,"id":"g","version":"1.0.0","scopes":{"ship":{"owner":{"id":"id","epoch":true}},"world":{"owner":{"fixed":"w"}}},
	 "packages":{"Hull.State":{"scope":"ship","always":true},"Town.Weather":{"scope":"world","always":true},"Ship.Other":{"scope":"ship"}},
	 "context_types":{"world.place":{"scope":"world","layout":"std.frame"}}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	u := &cs.UI{V2: true, Definition: doc, CtxScope: "world",
		Scopes: map[string]cs.ScopeView{"world": {Name: "world", Owner: &msg.ScopeOwner{ID: "w", Epoch: 1}}, "ship": {Name: "ship"}},
		Docs:   map[string]any{"Hull.State": map[string]any{"alert": "red"}, "Town.Weather": map[string]any{"sky": "clear"}, "Ship.Other": map[string]any{"x": 1}}}
	if st := statusDoc(u); st["sky"] != "clear" {
		t.Fatalf("no ship owner: the world scope's always package: %v", st)
	}
	u.Scopes["ship"] = cs.ScopeView{Name: "ship", Owner: &msg.ScopeOwner{ID: "s", Epoch: 1}}
	u.CtxScope = "ship"
	if st := statusDoc(u); st["alert"] != "red" {
		t.Fatalf("the active context's scope wins: %v", st)
	}
	u.Docs["Space.Ship.Status"] = map[string]any{"alert": "no"}
	if st := statusDoc(u); st["alert"] != "red" {
		t.Fatalf("v2 does not read the v1 document name: %v", st)
	}
	if st := statusDoc(&cs.UI{Docs: map[string]any{msg.PkgShipStatus: map[string]any{"alert": "green"}}}); st["alert"] != "green" {
		t.Fatalf("v1 keeps its own document: %v", st)
	}
}

// A world marked as a local development game allows plain http for its own packs (the per-world form of SQUATCLIENT_DEV_LOOPBACK_HTTP); others do not.
func TestWorldDevHTTP(t *testing.T) {
	e := &Engine{}
	e.SetWorldDevHTTP(map[string]bool{"Local Test": true, "off": false})
	if !e.devHTTPFor("local test") || e.devHTTPFor("off") || e.devHTTPFor("other") {
		t.Fatalf("only the marked world: %v %v %v", e.devHTTPFor("local test"), e.devHTTPFor("off"), e.devHTTPFor("other"))
	}
}
