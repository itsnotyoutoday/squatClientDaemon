package ansi

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

type vectorFile struct {
	Cases []struct {
		Name   string         `json:"name"`
		Inputs []string       `json:"inputs"`
		Spans  [][]proto.Span `json:"spans"`
	} `json:"cases"`
}

func TestVectors(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/ansi-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vf vectorFile
	if err := json.Unmarshal(raw, &vf); err != nil {
		t.Fatal(err)
	}
	if len(vf.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range vf.Cases {
		t.Run(c.Name, func(t *testing.T) {
			p := NewParser()
			for i, in := range c.Inputs {
				got := p.ParseLine(in)
				want := c.Spans[i]
				if len(got) == 0 && len(want) == 0 {
					continue
				}
				if !reflect.DeepEqual(got, want) {
					g, _ := json.Marshal(got)
					w, _ := json.Marshal(want)
					t.Errorf("line %d %q:\n got %s\nwant %s", i, in, g, w)
				}
			}
		})
	}
}

func TestBlankIsNonNil(t *testing.T) {
	if got := NewParser().ParseLine(""); got == nil || len(got) != 0 {
		t.Fatalf("got %#v", got)
	}
}

func TestExtras(t *testing.T) {
	p := NewParser()
	check := func(in, wantJSON string) {
		t.Helper()
		g, _ := json.Marshal(p.ParseLine(in))
		if string(g) != wantJSON {
			t.Errorf("%q: got %s want %s", in, g, wantJSON)
		}
		p.Reset()
	}
	check("\x1b[38:2::1:2:3mX", `[{"s":"X","fg":"#010203"}]`)
	check("\x1b[38:5:9mX", `[{"s":"X","fg":9}]`)
	check("\x1b[4:3mX\x1b[4:0mY", `[{"s":"X","u":true},{"s":"Y"}]`)
	check("\x1b[?25lX", `[{"s":"X"}]`)
	check("A\u009bB", `[{"s":"AB"}]`)
	check("\x1b]8;id=1;http://a\x07L\x1b]8;;\x07", `[{"s":"L","link":"http://a"}]`)
	check("\x1b[31m\x1b[0m\x1b[31mX", `[{"s":"X","fg":1}]`)
	check("A\x1b[31m\x1b[0mB", `[{"s":"AB"}]`)
	check("\x1b[38;5mX", `[{"s":"X"}]`)
	check("\x1b[1;22mX", `[{"s":"X"}]`)
}

func TestHyperlinkPersistsAcrossLines(t *testing.T) {
	p := NewParser()
	p.ParseLine("\x1b]8;;http://x\x1b\\a")
	got := p.ParseLine("b")
	if len(got) != 1 || got[0].Link != "http://x" {
		t.Fatalf("%#v", got)
	}
	p.Reset()
	if got := p.ParseLine("c"); got[0].Link != "" {
		t.Fatal("reset must clear link")
	}
}

func TestStripAndPlain(t *testing.T) {
	if got := StripANSI("\x1b[1;31mhi\x1b[0m \x1b]0;t\x07there\x07"); got != "hi there" {
		t.Fatal(got)
	}
	if PlainText(nil) != "" {
		t.Fatal("nil")
	}
}

func TestIncompleteSuffix(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"abc", 3}, {"abc\x1b", 3}, {"abc\x1b[3", 3}, {"abc\x1b[31m", 8}, {"abc\x1b]8;;http://x", 3},
		{"abc\x1b]8;;x\x1b\\", 11}, {"abc\x1b]8;;x\x1b", 3},
	}
	for _, c := range cases {
		if got := IncompleteSuffix(c.in); got != c.want {
			t.Errorf("%q: got %d want %d", c.in, got, c.want)
		}
	}
}

func TestInvariants(t *testing.T) {
	// merge + non-empty on a stress input
	p := NewParser()
	in := strings.Repeat("\x1b[31m\x1b[0mA\x1b[1m\x1b[22m", 20000)
	spans := p.ParseLine(in)
	if len(spans) != 1 || len(spans[0].S) != 20000 {
		t.Fatalf("spans=%d", len(spans))
	}
}

func FuzzParseLine(f *testing.F) {
	for _, s := range []string{"", "plain", "\x1b[31mX", "\x1b[38;2;1;2;3mX", "\x1b]8;;u\x1b\\t", "\x1b[", "\x1b]", "\x1b[38;5;", "\xff\xfe\x1b[;;;m"} {
		f.Add(s, s)
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		p := NewParser()
		for _, line := range []string{a, b} {
			spans := p.ParseLine(line)
			if spans == nil {
				t.Fatal("nil spans")
			}
			for i, s := range spans {
				if s.S == "" {
					t.Fatalf("empty span %d", i)
				}
				if i > 0 && spans[i-1].SameStyle(s) {
					t.Fatalf("unmerged spans %d", i)
				}
				if strings.ContainsRune(s.S, esc) {
					t.Fatalf("ESC leaked: %q", s.S)
				}
			}
			_ = IncompleteSuffix(line)
			_ = StripANSI(line)
		}
	})
}
