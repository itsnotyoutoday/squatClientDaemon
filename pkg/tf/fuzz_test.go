package tf

import (
	"strings"
	"testing"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

func fuzzInterp() *Interp {
	i := New(NopHost{}, Options{MaxSteps: 3000, MaxTime: -1, MaxDepth: 16, Rand: func(n int64) int64 { return 0 }})
	i.noRecover = true
	return i
}

// FuzzExpand feeds arbitrary text to the expander, the expression engine and
// the command dispatcher. It must neither panic nor hang (budgets apply).
func FuzzExpand(f *testing.F) {
	for _, s := range []string{
		"", "%", "%%", "%;", "%{", "%{x", "%{?x}", "%1 %* %# %L %R %P0", "$[", "$[1+", "$[x]", "$(/echo hi)", "$$",
		`$[strcat("a", "b")]`, `$[regmatch("(a)", "a")] %P1`, "/echo %{x} $[1/0]", "$[strrep(\"a\", 99999999)]",
		"/def a = /a", "$(/while (1) /let x=1 %; /done)", "/if (1) /echo x %; /endif", "%{%{%{x}}}", "$[{1}{2}{*}]",
		"/repeat -1 3 /echo x", "/quote /echo #echo hi", "$[x := x ? x : 1, x++]",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		i := fuzzInterp()
		i.mu.Lock()
		_ = i.run("w", true, func() error {
			i.top().params = []string{"p1", "p2"}
			i.top().args = "p1 p2"
			_, _ = i.expand(s)
			return nil
		})
		i.mu.Unlock()
		_ = i.Exec("w", "/echo "+s)
		_ = i.Exec("w", s)
		_ = i.LoadString("fuzz", s)
		_ = i.OnLine("w", proto.Line{Spans: []proto.Span{{S: s}}}, true)
	})
}

// FuzzParseDef feeds arbitrary /def argument text to the macro parser. Any
// definition that parses must print back to a /def line that parses again.
func FuzzParseDef(f *testing.F) {
	for _, s := range []string{
		"", "-t'a b' foo = /echo hi", "-mregexp -t'(' x", "-hCONNECT = x", "-p5 -n2 -F -g -ahCred -P1Cblue x = y",
		"-t\"unterminated", "-E(x > 1) -c50 a", "-bF1 -wmain k = /x", "= body only", "-a", "-P", "-P9C#fff -t* z", "-hSEND foo* s = /x",
		"name with spaces = x", "-t{a b} q = c", "-mglob -t'[' bad", "-t'' e = x",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		i := fuzzInterp()
		m, err := i.parseDef(s)
		if err != nil {
			return
		}
		def := m.Def()
		m2, err := i.parseDef(def[len("/def "):])
		if err != nil && strings.Contains(s, "'") && strings.Contains(s, `"`) {
			return // documented limitation of quoteOpt
		}
		if err != nil {
			t.Fatalf("Def() output does not parse: %q -> %q: %v", s, def, err)
		}
		if m2.Def() != def {
			t.Fatalf("Def() not stable: %q vs %q", def, m2.Def())
		}
	})
}
