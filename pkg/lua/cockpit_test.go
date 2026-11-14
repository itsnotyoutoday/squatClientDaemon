package lua

import (
	"os"
	"regexp"
	"testing"
	"time"
)

// The Evennia cockpit layout's <lua> block (in the SquatClient repo) run against simulated game data.
func TestCockpitLayoutLua(t *testing.T) {
	b, err := os.ReadFile("../../../squatClient-layouts/internal/layouts/starters/evennia-cockpit.layout")
	if err != nil {
		t.Skip("layout file not next to this repo")
	}
	m := regexp.MustCompile(`(?s)<lua>\n(.*?)</lua>`).FindSubmatch(b)
	if m == nil {
		t.Fatal("no lua block")
	}
	r, h := newRT(t, Options{})
	if err := r.LoadString("layout:cockpit", string(m[1])); err != nil {
		t.Fatal(err)
	}
	vars := func(k string) any { v, _ := r.GetVar(k); return v }
	gm := func(pkg string, d map[string]any) {
		r.FireEventFor("w", "gmcp", map[string]any{"package": pkg, "data": d, "world": "w"}, true)
	}
	gm("Char.Vitals", map[string]any{"hp": 90.0, "maxhp": 100.0})
	if vars("danger") != "ok" || vars("alert") != "" {
		t.Fatalf("healthy: %v %v", vars("danger"), vars("alert"))
	}
	gm("Char.Vitals", map[string]any{"hp": 30.0})
	if vars("danger") != "low" || vars("alert") == "" {
		t.Fatalf("low: %v %v", vars("danger"), vars("alert"))
	}
	if len(h.sends) != 0 {
		t.Fatal("auto-heal fired while off")
	}
	// the layout (JavaScript side) turns auto-heal on: Lua sees it
	if err := r.SetVar("autoheal", true); err != nil {
		t.Fatal(err)
	}
	gm("char.vitals", map[string]any{"hp": 10.0})
	if vars("danger") != "critical" || len(h.sends) != 1 || h.sends[0].text != "heal" {
		t.Fatalf("critical: %v sends=%v", vars("danger"), h.sends)
	}
	if len(h.notifies) == 0 {
		t.Fatal("no notification")
	}
	gm("Room.Info", map[string]any{"name": "Hall"})
	gm("Room.Info", map[string]any{"name": "Hall"})
	gm("Room.Info", map[string]any{"name": "Yard"})
	if vars("rooms") != 2.0 {
		t.Fatalf("rooms %v", vars("rooms"))
	}
	for _, s := range []string{"You take 7 damage.", "You take 5 damage.", "A warm light mends your wounds.", "You feel stronger: level 2!"} {
		r.OnLine("w", line(s), true)
	}
	if vars("damage_taken") != 12.0 || vars("heals") != 1.0 || vars("levelups") != 1.0 {
		t.Fatalf("counters %v %v %v", vars("damage_taken"), vars("heals"), vars("levelups"))
	}
	if !r.OnInput("w", "/stats") || len(h.echoes) != 1 {
		t.Fatalf("alias: echoes=%d", len(h.echoes))
	}
	h.advance(3 * time.Second)
	if vars("uptime") != 3.0 {
		t.Fatalf("uptime %v", vars("uptime"))
	}
	// leaving the layout stops its timer
	r.Unload("layout:cockpit")
	h.advance(5 * time.Second)
	if vars("uptime") != 3.0 {
		t.Fatalf("timer kept running: %v", vars("uptime"))
	}
}
