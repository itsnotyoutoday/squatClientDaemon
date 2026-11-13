package tf

import (
	"strings"
	"testing"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

func TestParseAttrColours(t *testing.T) {
	tests := []struct {
		in         string
		fg, bg     string // proto.Color.String(), "" = none
		wantFlags  string
		wantString string
	}{
		{"Cred", "1", "", "", "Cred"},
		{"Cbrightred", "9", "", "", "C9"},
		{"C196", "196", "", "", "C196"},
		{"C255", "255", "", "", "C255"},
		{"C#ff8800", "#ff8800", "", "", "C#ff8800"},
		{"C#F80", "#ff8800", "", "", "C#ff8800"},
		{"BC#001122,C#aabbcc", "#aabbcc", "#001122", "", "C#aabbcc,BC#001122"},
		{"BCblue,u", "", "4", "u", "u,BCblue"},
		{"hu", "", "", "hu", "hu"},
		{"n", "", "", "n", "n"},
		{"Crgb523", "", "", "", ""}, // checked separately below
		{"Cgray3", "235", "", "", "C235"},
		{"g", "", "", "g", "g"},
		{"Cwhite,BCblack,B,d,i,s,f,r,u", "7", "0", "hdisfru", ""},
	}
	for _, tc := range tests {
		a, err := ParseAttr(tc.in)
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		fg, bg := "", ""
		if a.Fg != nil {
			fg = a.Fg.String()
		}
		if a.Bg != nil {
			bg = a.Bg.String()
		}
		if tc.in != "Crgb523" && (fg != tc.fg || bg != tc.bg) {
			t.Errorf("%s: fg=%q bg=%q", tc.in, fg, bg)
		}
		if tc.wantString != "" && a.String() != tc.wantString {
			t.Errorf("%s: String()=%q want %q", tc.in, a.String(), tc.wantString)
		}
		// round trip
		b, err := ParseAttr(a.String())
		if err != nil || b.String() != a.String() {
			t.Errorf("%s: round trip failed: %q -> %v", tc.in, a.String(), err)
		}
	}
	a, _ := ParseAttr("Crgb523")
	if a.Fg == nil || a.Fg.Index() != 16+36*5+6*2+3 {
		t.Errorf("rgb cube: %+v", a.Fg)
	}
	for _, bad := range []string{"Cfoo", "C256", "C#12", "C#gggggg", "x", "Z", "Cred,Q", "BC"} {
		if _, err := ParseAttr(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if a, err := ParseAttr(""); err != nil || !a.IsZero() {
		t.Error("empty attr should be zero")
	}
}

func TestAttrApplyAndMerge(t *testing.T) {
	red := proto.Palette(1)
	sp := proto.Span{S: "x", Fg: &red, U: true, Link: "http://a"}
	a, _ := ParseAttr("n,BC#010203,h")
	out := a.Apply(sp)
	if out.Fg != nil || out.U || !out.B || out.Bg == nil || out.Bg.Hex() != "#010203" || out.Link != "http://a" {
		t.Errorf("normal+apply: %+v", out)
	}
	b, _ := ParseAttr("Cred,u")
	c, _ := ParseAttr("n,Cblue")
	m := b.Merge(c)
	if m.Under || m.Fg == nil || m.Fg.Index() != 4 {
		t.Errorf("merge with normal: %+v", m)
	}
}

func TestApplyRangeSplitsSpans(t *testing.T) {
	spans := []proto.Span{{S: "hello "}, {S: "wor", B: true}, {S: "ld"}}
	a, _ := ParseAttr("Cred")
	out := applyRange(spans, 3, 8, a)
	var got []string
	for _, s := range out {
		got = append(got, s.S)
	}
	if strings.Join(got, "|") != "hel|lo |wo|r|ld" {
		t.Errorf("split: %v", got)
	}
	if plain := spansPlainTest(out); plain != "hello world" {
		t.Errorf("text changed: %q", plain)
	}
	if out[1].Fg == nil || out[2].Fg == nil || out[2].B != true || out[0].Fg != nil || out[len(out)-1].Fg != nil {
		t.Errorf("styles wrong: %+v", out)
	}
	if got := applyRange(spans, 5, 5, a); len(got) != len(spans) {
		t.Error("empty range changed spans")
	}
}

func spansPlainTest(s []proto.Span) string {
	var b strings.Builder
	for _, x := range s {
		b.WriteString(x.S)
	}
	return b.String()
}

func TestMarkupAndANSI(t *testing.T) {
	sp := SpansFromMarkup("a@{Cred}b@{BC#102030,u}c@{n}d@@e", Attr{})
	if spansPlainTest(sp) != "abcd@e" || len(sp) != 4 {
		t.Fatalf("%+v", sp)
	}
	if sp[1].Fg == nil || sp[2].Bg == nil || sp[2].Bg.Hex() != "#102030" || !sp[2].U || sp[2].Fg == nil {
		t.Errorf("styles: %+v", sp)
	}
	if sp[3].Fg != nil || sp[3].U {
		t.Error("@{n} did not reset")
	}
	// bad attr stays literal
	if got := spansPlainTest(SpansFromMarkup("x@{bogus}y", Attr{})); got != "x@{bogus}y" {
		t.Errorf("%q", got)
	}
	ansi := ansiFor(SpansFromMarkup("@{C#ff0000,BC21}a@{C200}b@{n}c", Attr{}))
	want := "\x1b[0;38;2;255;0;0;48;5;21ma\x1b[0;38;5;200;48;5;21mb\x1b[0mc"
	if ansi != want {
		t.Errorf("ansi %q want %q", ansi, want)
	}
	if got := stripMarkup("\x1b[1;31m@{Cred}hi@@\x1b[0m"); got != "hi@" {
		t.Errorf("strip %q", got)
	}
	if got := EncodeAttr("a@b", "Cred"); got != "@{Cred}a@@b@{n}" {
		t.Errorf("encode %q", got)
	}
	if got := ansiFor([]proto.Span{{S: "x"}, {S: "y", B: true}, {S: "z"}}); got != "x\x1b[0;1my\x1b[0mz" {
		t.Errorf("%q", got)
	}
}

func TestEchoAttrsReachHost(t *testing.T) {
	i, h := newTest()
	_ = i.Exec("w", "/echo -aC#112233,BC200 hi")
	_ = i.Exec("w", "/echo -p x@{Cbrightcyan}y")
	if got := h.dump(); got != "echo w: <fg=#112233,bg=200>hi</>\necho w: x<fg=14>y</>" {
		t.Errorf("%q", got)
	}
}

func TestGlobMatcher(t *testing.T) {
	tests := []struct {
		pat, s string
		want   bool
	}{
		{"*", "", true}, {"a*", "abc", true}, {"*c", "abc", true}, {"a*c", "abc", true}, {"a*c", "ab", false},
		{"a?c", "abc", true}, {"a?c", "ac", false}, {"[abc]x", "bx", true}, {"[^abc]x", "bx", false},
		{"[a-c]x", "cx", true}, {"{foo|bar}baz", "barbaz", false}, {"{foo|bar} baz", "bar baz", true}, {"{storm|chup*}*", "Storm jiggles", true}, {"{storm|chup*}*", "stormette jiggles", false}, {"ABC", "abc", true}, {"{foo|bar}baz", "bazbaz", false},
		{`a\*b`, "a*b", true}, {`a\*b`, "axb", false}, {"héllo*", "héllo wörld", true}, {"?", "é", true},
		{"[", "[", true}, {"{x", "{x", true}, {"", "", true}, {"", "a", false},
		{"*a*a*a*a*a*a*a*a*b", strings.Repeat("a", 60), false}, // budget-bounded, must return
	}
	for _, tc := range tests {
		if got := globMatch(tc.pat, tc.s); got != tc.want {
			t.Errorf("glob(%q,%q)=%v want %v", tc.pat, tc.s, got, tc.want)
		}
	}
	ok, caps := compileGlob("a*c?").match("abbcd")
	if !ok || len(caps) != 2 || caps[0] != [2]int{1, 3} || caps[1] != [2]int{4, 5} {
		t.Errorf("captures %v", caps)
	}
}
