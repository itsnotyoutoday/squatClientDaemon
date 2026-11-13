package lua

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

func loadExample(t *testing.T, r *Runtime, name string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "lua", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.LoadString(name, string(b)); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func TestExampleScripts(t *testing.T) {
	r, h := newRT(t, Options{})
	for _, n := range []string{"autoheal.lua", "highlight.lua", "gagspam.lua", "alias.lua", "timer.lua", "tf.lua"} {
		loadExample(t, r, n)
	}
	// auto-heal
	r.OnLine("w", line("HP: 90/100"), true)
	r.OnLine("w", line("HP: 30/100"), true)
	r.OnLine("w", line("HP: 20/100"), true) // inside the 3s cooldown
	if len(h.sends) != 1 || h.sends[0].text != "cast heal" {
		t.Fatalf("sends=%v", h.sends)
	}
	h.advance(4 * time.Second)
	r.OnLine("w", line("HP: 10/100"), true)
	if len(h.sends) != 2 {
		t.Fatalf("cooldown should have expired: %v", h.sends)
	}
	// highlight
	res := r.OnLine("w", line("Bob tells you hi"), true)
	if res.Spans[0].Fg == nil || res.Spans[0].Fg.Hex() != "#ff8800" || !res.Spans[0].B {
		t.Fatalf("%+v", res.Spans)
	}
	res = r.OnLine("w", line("a treasure!"), true)
	want := []proto.Span{{S: "a "}, {S: "treasure", Fg: rgb(0xff, 0xd7, 0), Bg: pc(52)}, {S: "!"}}
	if !reflect.DeepEqual(res.Spans, want) {
		t.Fatalf("%+v", res.Spans)
	}
	// gag spam
	if !r.OnLine("w", line("<Spam> buy now"), true).Gag {
		t.Fatal("not gagged")
	}
	r.OnLine("w", line("<Spam> again"), false) // replay: gagged but not counted
	if v, _ := r.GetVar("spam_count"); v != 1.0 {
		t.Fatalf("spam_count=%v", v)
	}
	// alias
	if !r.OnInput("w", "k rat") || h.sends[len(h.sends)-1].text != "kill rat" {
		t.Fatalf("sends=%v", h.sends)
	}
	// timer
	h.sends = nil
	r.FireEvent("connect", map[string]any{"world": "w"})
	h.advance(601 * time.Second)
	if len(h.sends) != 2 {
		t.Fatalf("anti-idle sends=%v", h.sends)
	}
	// tf
	r.OnLine("w", line("Ann arrives."), true)
	if len(h.tf) != 1 || h.tf[0] != "w:/echo -aB Welcome, Ann!" {
		t.Fatalf("tf=%v", h.tf)
	}
}
