package lua

import "testing"

func TestUIWindows(t *testing.T) {
	r, _ := newRT(t, Options{})
	if err := r.LoadString("a", `
squat.ui.window{id = "who", title = "Who", x = 40, y = 60, body = {type = "column", children = {{type = "text", value = "hi"}}}}
squat.ui.panel{id = "side", title = "Side", body = {type = "text", value = "x"}}
squat.ui.update("who", {title = "Who (2)"})
`); err != nil {
		t.Fatal(err)
	}
	m, _ := r.GetVar("ui.windows")
	ws := m.(map[string]any)
	who := ws["who"].(map[string]any)
	if who["title"] != "Who (2)" || who["kind"] != "window" || who["x"] != 40.0 || who["owner"] != "a" {
		t.Fatalf("who: %v", who)
	}
	if ws["side"].(map[string]any)["dock"] != "right" {
		t.Fatalf("panel: %v", ws["side"])
	}
	mustLoad(t, r, `assert(squat.ui.close("who") == true); assert(#squat.ui.list() == 1); assert(squat.ui.get("side").title == "Side")`)
	if err := r.LoadString("b", `squat.ui.window{}`); err == nil {
		t.Fatal("window without id accepted")
	}
	r.Unload("a") // takes its windows with it
	if v, ok := r.GetVar("ui.windows"); ok && v != nil {
		t.Fatalf("left over: %v", v)
	}
}
