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

func cockpitLua(t *testing.T) string {
	b, err := os.ReadFile("../../../squatClient-layouts/internal/layouts/starters/evennia-cockpit.layout")
	if err != nil {
		t.Skip("layout file not next to this repo")
	}
	return string(regexp.MustCompile(`(?s)<lua>\n(.*?)</lua>`).FindSubmatch(b)[1])
}

// the real output of an Evennia game's WHO and HELP, captured and turned into windows
func TestCockpitWhoAndHelp(t *testing.T) {
	r, h := newRT(t, Options{})
	if err := r.LoadString("layout:cockpit", cockpitLua(t)); err != nil {
		t.Fatal(err)
	}
	r.FireEventFor("w", "gmcp", map[string]any{"package": "Room.Info", "data": map[string]any{"name": "Limbo"}, "world": "w"}, true)
	win := func(id string) map[string]any {
		v, _ := r.GetVar("ui.windows")
		m, _ := v.(map[string]any)
		w, _ := m[id].(map[string]any)
		return w
	}
	if win("trail") == nil {
		t.Fatal("no trail panel")
	}
	feed := func(lines ...string) (gagged int) {
		for _, l := range lines {
			if r.OnLine("w", line(l), true).Gag {
				gagged++
			}
		}
		h.advance(600 * time.Millisecond)
		return
	}

	// WHO
	if !r.OnInput("w", "/who") || len(h.sends) == 0 || h.sends[len(h.sends)-1].text != "who" {
		t.Fatalf("who not requested: %v", h.sends)
	}
	n := feed("Accounts:",
		"+--------------+--------+------+",
		"| Account Name | On for | Idle |",
		"+--------------+--------+------+",
		"| god          | 00:00  | 1s   |",
		"| ann          | 02:10  | 5m   |",
		"+--------------+--------+------+",
		"2 unique accounts logged in.")
	if n != 8 {
		t.Fatalf("who output not hidden: %d gagged", n)
	}
	if v, _ := r.GetVar("who_count"); v != 2.0 {
		t.Fatalf("who_count %v", v)
	}
	w := win("who")
	if w == nil {
		t.Fatal("no who window")
	}
	body := w["body"].(map[string]any)["children"].([]any)
	tbl := body[0].(map[string]any)
	rows := tbl["rows"].([]any)
	if tbl["type"] != "table" || len(rows) != 2 || rows[1].(map[string]any)["account_name"] != "ann" {
		t.Fatalf("who table: %v", tbl)
	}
	// text after the capture is not hidden any more
	if r.OnLine("w", line("You hear a bell."), true).Gag {
		t.Fatal("still gagging")
	}

	// HELP index
	r.OnInput("w", "/helpbrowser")
	feed("-----------------------------------Commands-----------------------------------",
		"                                                                              ",
		"-- Admin ---------------------------------------------------------------------",
		"ban          boot         emit         perm         unban                     ",
		"-- Building ------------------------------------------------------------------",
		"alias        batchcode    desc         dig          examine                   ",
		"-- General -------------------------------------------------------------------",
		"get          help         look         who                                    ",
		"---------------------------------Game & World---------------------------------",
		"evennia")
	hw := win("help")
	if hw == nil {
		t.Fatal("no help window")
	}
	kids := hw["body"].(map[string]any)["children"].([]any)
	if len(kids) != 5 { // the filter box and four categories
		t.Fatalf("help window: %d children: %v", len(kids), kids)
	}
	// filter
	r.OnInput("w", "/hfind di")
	kids = win("help")["body"].(map[string]any)["children"].([]any)
	if len(kids) != 2 {
		t.Fatalf("filtered: %d", len(kids))
	}

	// one topic
	r.OnInput("w", "/htopic dig")
	feed("------------------------------------------------------------------------------",
		"Help for dig",
		"",
		"build new rooms and connect them to the current location",
		"",
		"Usage:",
		"  dig[/switches] <roomname>[;alias] = <exit_to_there>",
		"",
		"Switches:",
		"   tel or teleport - move yourself to the new room",
		"",
		"This command is a convenient way to build rooms quickly; use look and examine to check them.",
		"------------------------------------------------------------------------------")
	tw := win("helptopic")
	if tw == nil {
		t.Fatal("no topic window")
	}
	var types []string
	var seeAlso []string
	for _, k := range tw["body"].(map[string]any)["children"].([]any) {
		km := k.(map[string]any)
		types = append(types, km["type"].(string)+":"+toS(km["value"]))
		if km["type"] == "grid" {
			for _, b := range km["children"].([]any) {
				seeAlso = append(seeAlso, b.(map[string]any)["text"].(string))
			}
		}
	}
	// "alias" appears in the usage line, then look and examine in the text; the topic itself is not listed
	if len(seeAlso) != 3 || seeAlso[0] != "alias" || seeAlso[1] != "look" || seeAlso[2] != "examine" {
		t.Fatalf("see also %v (%v)", seeAlso, types)
	}
	if types[0] != "heading:dig" {
		t.Fatalf("types %v", types)
	}
}

func toS(v any) string {
	s, _ := v.(string)
	if len(s) > 20 {
		s = s[:20]
	}
	return s
}
