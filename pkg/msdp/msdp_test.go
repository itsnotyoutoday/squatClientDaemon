package msdp

import (
	"reflect"
	"strings"
	"testing"
)

func b(parts ...any) []byte {
	var out []byte
	for _, p := range parts {
		switch x := p.(type) {
		case byte:
			out = append(out, x)
		case int:
			out = append(out, byte(x))
		case string:
			out = append(out, x...)
		}
	}
	return out
}

func TestDecodeScalarArrayTable(t *testing.T) {
	// what Evennia sends for LIST COMMANDS (captured from a real server)
	p := b(Var, "commands", Val, ArrayOpen, Val, "report", Val, "send", Val, "list", ArrayClose)
	ps, err := Decode(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []Pair{{"commands", []any{"report", "send", "list"}}}
	if !reflect.DeepEqual(ps, want) {
		t.Fatalf("got %#v", ps)
	}
	p = b(Var, "ROOM", Val, TableOpen, Var, "VNUM", Val, "6008", Var, "NAME", Val, "The forest", Var, "EXITS", Val, TableOpen, Var, "n", Val, "6011", TableClose, TableClose,
		Var, "HEALTH", Val, "100")
	ps, err = Decode(p)
	if err != nil {
		t.Fatal(err)
	}
	m := ToMap(ps)
	room := m["ROOM"].(map[string]any)
	if room["NAME"] != "The forest" || room["EXITS"].(map[string]any)["n"] != "6011" || m["HEALTH"] != "100" {
		t.Fatalf("got %#v", m)
	}
}

func TestDecodeLeniency(t *testing.T) {
	// Evennia answers an unknown SEND with "VAR send VAL" and nothing after it
	ps, err := Decode(b(Var, "send", Val))
	if err != nil || len(ps) != 1 || ps[0].Value != "" {
		t.Fatalf("%v %#v", err, ps)
	}
	// a VAR with no VAL at all
	ps, err = Decode(b(Var, "ready", Var, "x", Val, "1"))
	if err != nil || len(ps) != 2 || ps[0].Value != "" || ps[1].Value != "1" {
		t.Fatalf("%v %#v", err, ps)
	}
	// UTF-8 passes through untouched
	ps, err = Decode(b(Var, "NAME", Val, "Zoë ✓"))
	if err != nil || ps[0].Value != "Zoë ✓" {
		t.Fatalf("%v %#v", err, ps)
	}
}

func TestDecodeErrors(t *testing.T) {
	bad := map[string][]byte{
		"not starting with VAR":  b("junk"),
		"unterminated table":     b(Var, "a", Val, TableOpen, Var, "b", Val, "c"),
		"unterminated array":     b(Var, "a", Val, ArrayOpen, Val, "b"),
		"array item without VAL": b(Var, "a", Val, ArrayOpen, "oops", ArrayClose),
	}
	for name, p := range bad {
		if _, err := Decode(p); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	deep := []byte{}
	for i := 0; i < MaxDepth+3; i++ {
		deep = append(deep, Var, 'a', Val, TableOpen)
	}
	if _, err := Decode(deep); err != ErrTooDeep {
		t.Errorf("deep nesting: %v", err)
	}
	if _, err := Decode(make([]byte, MaxPayload+1)); err != ErrTooLarge {
		t.Errorf("large payload: %v", err)
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	in := map[string]any{
		"REPORT": []any{"HEALTH", "MANA"},
		"ROOM":   map[string]any{"NAME": "hall", "EXITS": []any{"n", "s"}},
		"LEVEL":  float64(7),
		"OK":     true,
		"EMPTY":  nil,
	}
	p, err := Encode(FromMap(in))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decode(p)
	if err != nil {
		t.Fatal(err)
	}
	got := ToMap(out)
	want := map[string]any{
		"REPORT": []any{"HEALTH", "MANA"},
		"ROOM":   map[string]any{"NAME": "hall", "EXITS": []any{"n", "s"}},
		"LEVEL":  "7", "OK": "true", "EMPTY": "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
	// deterministic: same input, same bytes
	p2, _ := Encode(FromMap(in))
	if string(p) != string(p2) {
		t.Fatal("encoding is not deterministic")
	}
}

func TestEncodeWireBytes(t *testing.T) {
	p, err := Encode([]Pair{{"LIST", "COMMANDS"}})
	if err != nil || string(p) != string(b(Var, "LIST", Val, "COMMANDS")) {
		t.Fatalf("%v %q", err, p)
	}
	p, _ = Encode([]Pair{{"REPORT", []string{"HEALTH", "MANA"}}})
	if string(p) != string(b(Var, "REPORT", Val, ArrayOpen, Val, "HEALTH", Val, "MANA", ArrayClose)) {
		t.Fatalf("%q", p)
	}
}

func TestEncodeRejectsReservedBytes(t *testing.T) {
	for _, bad := range []any{"a\x01b", "tab\x03", "iac\xff", map[string]any{"k\x02": "v"}, []any{"\x06"}} {
		if _, err := Encode([]Pair{{"X", bad}}); err == nil {
			t.Errorf("%#v was encoded", bad)
		}
	}
	if _, err := Encode([]Pair{{"", "v"}}); err == nil {
		t.Error("an empty name was encoded")
	}
	if _, err := Encode([]Pair{{"X", struct{}{}}}); err == nil {
		t.Error("an unsupported type was encoded")
	}
}

func TestString(t *testing.T) {
	ps, _ := Decode(b(Var, "A", Val, ArrayOpen, Val, "x", ArrayClose, Var, "T", Val, TableOpen, Var, "k", Val, "v", TableClose))
	if got := String(ps); !strings.Contains(got, `A=["x"]`) || !strings.Contains(got, `T={k="v"}`) {
		t.Fatalf("%s", got)
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(b(Var, "a", Val, "b"))
	f.Add(b(Var, "a", Val, TableOpen, Var, "b", Val, ArrayOpen, Val, "c", ArrayClose, TableClose))
	f.Add([]byte{Var, Val, TableOpen, ArrayOpen})
	f.Fuzz(func(t *testing.T, p []byte) {
		ps, err := Decode(p) // must never panic or loop forever
		if err != nil {
			return
		}
		// whatever decodes cleanly and can be re-encoded must decode to the same thing
		enc, err := Encode(ps)
		if err != nil {
			return // e.g. an empty name decoded from sloppy input
		}
		again, err := Decode(enc)
		if err != nil || !reflect.DeepEqual(ToMap(ps), ToMap(again)) {
			t.Fatalf("round trip changed the data: %#v -> %q -> %#v (%v)", ps, enc, again, err)
		}
	})
}
