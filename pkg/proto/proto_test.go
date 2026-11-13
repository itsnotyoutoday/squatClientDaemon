package proto

import (
	"encoding/json"
	"testing"
)

func TestColorJSON(t *testing.T) {
	cases := []struct {
		c    Color
		json string
	}{
		{Palette(0), `0`},
		{Palette(196), `196`},
		{Palette(255), `255`},
		{RGB(255, 128, 0), `"#ff8000"`},
		{RGB(1, 2, 3), `"#010203"`},
	}
	for _, tc := range cases {
		b, err := json.Marshal(tc.c)
		if err != nil || string(b) != tc.json {
			t.Fatalf("marshal %v: got %s err %v want %s", tc.c, b, err, tc.json)
		}
		var back Color
		if err := json.Unmarshal([]byte(tc.json), &back); err != nil || back != tc.c {
			t.Fatalf("unmarshal %s: got %v err %v", tc.json, back, err)
		}
	}
	for _, bad := range []string{`256`, `-1`, `"#12"`, `"red"`, `true`, `1.5`} {
		var c Color
		if err := json.Unmarshal([]byte(bad), &c); err == nil {
			t.Fatalf("expected error for %s, got %v", bad, c)
		}
	}
	// uppercase hex is normalised to lowercase
	var c Color
	if err := json.Unmarshal([]byte(`"#FF00AA"`), &c); err != nil || c.Hex() != "#ff00aa" {
		t.Fatalf("uppercase hex: %v %v", c, err)
	}
}

func TestSpanJSONOmitsDefaults(t *testing.T) {
	fg := RGB(255, 209, 102)
	bg := Palette(4)
	b, _ := json.Marshal(Span{S: "x", Fg: &fg, Bg: &bg, B: true})
	if string(b) != `{"s":"x","fg":"#ffd166","bg":4,"b":true}` {
		t.Fatalf("got %s", b)
	}
	b, _ = json.Marshal(Span{S: "plain"})
	if string(b) != `{"s":"plain"}` {
		t.Fatalf("got %s", b)
	}
}

func TestSameStyle(t *testing.T) {
	r1, r2, g := Palette(1), Palette(1), Palette(2)
	if !(Span{S: "a", Fg: &r1}).SameStyle(Span{S: "b", Fg: &r2}) {
		t.Fatal("equal colours should be same style")
	}
	if (Span{S: "a", Fg: &r1}).SameStyle(Span{S: "b", Fg: &g}) {
		t.Fatal("different colours should differ")
	}
	if (Span{S: "a", Fg: &r1}).SameStyle(Span{S: "b"}) {
		t.Fatal("colour vs none should differ")
	}
}

func TestLinePlainAndEnvelope(t *testing.T) {
	l := Line{Spans: []Span{{S: "ab"}, {S: "cd"}}}
	if l.Plain() != "abcd" {
		t.Fatal(l.Plain())
	}
	e, err := ParseEnvelope([]byte(`{"v":1,"t":"send","id":"7","session":"s1","text":"hi"}`))
	if err != nil || e.T != "send" || e.ID != "7" || len(e.Raw) == 0 {
		t.Fatalf("%+v %v", e, err)
	}
	if _, err := ParseEnvelope([]byte(`{"v":1}`)); err == nil {
		t.Fatal("missing type should fail")
	}
	if _, err := ParseEnvelope([]byte(`not json`)); err == nil {
		t.Fatal("garbage should fail")
	}
}

func TestDefaultCaps(t *testing.T) {
	c := DefaultCaps()
	if !c.Truecolor || !c.UTF8 || c.ScreenReader {
		t.Fatalf("%+v", c)
	}
}
