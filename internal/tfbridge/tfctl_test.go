package tfbridge

import (
	"bytes"
	"strings"
	"testing"
)

func TestEscapeRoundTrip(t *testing.T) {
	cases := []string{"", "plain", "tab\there", "nl\nx", "cr\rx", `back\slash`, "\x00\x01\x1b[31mred\x1b[0m", "\x7f", "héllo wörld ✓", "\xff\xfe raw",
		`ends with \`, `\x41 literal`, `\\n`}
	for _, c := range cases {
		if got := unescapeField(escapeField(c)); got != c {
			t.Errorf("round trip %q -> %q", c, got)
		}
		if esc := escapeField(c); strings.ContainsAny(esc, "\t\n\r") {
			t.Errorf("escaped field %q still contains a separator", esc)
		}
	}
}

func TestEncodeDecode(t *testing.T) {
	line := Encode("LINE", "w-x-1", "g", "", "game", "123", "a\tb\nc")
	if !bytes.HasSuffix(line, []byte("\n")) || bytes.Count(line, []byte("\n")) != 1 {
		t.Fatalf("bad line %q", line)
	}
	m := Decode(strings.TrimSuffix(string(line), "\n"))
	want := []string{"LINE", "w-x-1", "g", "", "game", "123", "a\tb\nc"}
	if len(m) != len(want) {
		t.Fatalf("got %q", m)
	}
	for i := range want {
		if m[i] != want[i] {
			t.Errorf("field %d = %q want %q", i, m[i], want[i])
		}
	}
}

// What squat.c emits must decode: the C escaper writes lowercase \xHH.
func TestDecodeCEscapes(t *testing.T) {
	got := Decode(`LINE` + "\t" + `\x1b[31mred\x1b[0m\x00` + "\t" + `a\\b`)
	if got[1] != "\x1b[31mred\x1b[0m\x00" || got[2] != `a\b` {
		t.Fatalf("got %q", got)
	}
}

func FuzzEscape(f *testing.F) {
	for _, s := range []string{"", "a\tb", "\\", "\x00\xff", `\x4`, `\`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if got := unescapeField(escapeField(s)); got != s {
			t.Fatalf("round trip %q -> %q", s, got)
		}
		_ = Decode(s) // must never panic on arbitrary input
	})
}
