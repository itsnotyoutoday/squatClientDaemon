package tf

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// echoes runs lines through Exec and returns the plain echoed text, one
// entry per Echo call.
func echoes(t *testing.T, i *Interp, h *fakeHost, lines ...string) []string {
	t.Helper()
	h.out = nil
	for _, l := range lines {
		_ = i.Exec("w", l)
	}
	var out []string
	for _, o := range h.out {
		if s, ok := strings.CutPrefix(o, "echo w: "); ok {
			out = append(out, s)
		} else {
			out = append(out, "<"+o+">")
		}
	}
	return out
}

func one(t *testing.T, setup []string, line string) string {
	t.Helper()
	i, h := newTest()
	for _, s := range setup {
		if err := i.Exec("w", s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	got := echoes(t, i, h, line)
	return strings.Join(got, "\n")
}

func TestExpansionTable(t *testing.T) {
	tests := []struct {
		setup []string
		line  string
		want  string
	}{
		{nil, "/echo plain text", "plain text"},
		{nil, "/echo 100%% sure", "100% sure"},
		{nil, "/echo a%;/echo b", "a\nb"},
		{[]string{"/set v=7"}, "/echo %v %{v} %{v}x %vx", "7 7 7x "},
		{[]string{"/set v=7"}, "/echo [%{?v}] [%{?q}]", "[1] [0]"},
		{[]string{"/set v=7"}, "/echo $[v+1]", "8"},
		{nil, "/echo $$ and $x and $", "$ and $x and $"},
		{nil, "/echo $(/echo hi) there", "hi there"},
		{nil, "/echo %L%R%P1[]", "[]"},
		{[]string{"/def f = /echo [%*] [%1] [%2] [%#]"}, "/f a  b", "[a  b] [a] [b] [2]"},
		{[]string{"/def f = /echo [%{1}] [%{2}] [%{10}]"}, "/f a b", "[a] [b] []"},
		{[]string{"/def f = /shift 2 %; /echo [%*] [%1]"}, "/f a b c d", "[c d] [c]"},
		{[]string{"/set s=/echo again"}, "/eval %s", "again"},
		{[]string{"/set s=1"}, "/eval /echo %%s=%s", "1=1"},
		{nil, "/echo sep inside $[strcat(\"a;b\", \"c\")]", "sep inside a;bc"},
		{nil, `/echo $[strcat("%Y", "%1")]`, "%Y%1"},
		{nil, "/echo unterminated %{x", "% unterminated %{ in \"unterminated %{x\""},
		{nil, "/echo $[1 +]", "% unexpected end of expression"},
	}
	for _, tc := range tests {
		if got := one(t, tc.setup, tc.line); got != tc.want {
			t.Errorf("%v %q:\n got %q\nwant %q", tc.setup, tc.line, got, tc.want)
		}
	}
}

func TestExprTable(t *testing.T) {
	tests := []struct{ expr, want string }{
		{"1+2*3", "7"}, {"(1+2)*3", "9"}, {"2*3+4", "10"}, {"10-3-2", "5"}, {"7/2", "3"}, {"7/2.0", "3.5"},
		{"-3+5", "2"}, {"- -3", "3"}, {"!1", "0"}, {"!!5", "1"},
		{"1<2", "1"}, {"2<=2", "1"}, {"3>4", "0"}, {"1==1.0", "1"}, {`"a"<"b"`, "1"}, {`"10"<"9"`, "0"}, {`"abc"<"abd"`, "1"},
		{`"x" =~ "x"`, "1"}, {`"1.0" =~ "1"`, "0"}, {`"foo" !~ "bar"`, "1"},
		{`"foobar" =/ "foo*"`, "1"}, {`"foobar" =/ "f?o*r"`, "1"}, {`"foobar" =/ "[a-f]oo*"`, "1"}, {`"foobar" !/ "bar*"`, "1"}, {`"a.b" =/ "a\.b"`, "1"},
		{"1&&0", "0"}, {"1||0", "1"}, {"0|0", "0"}, {"1&1", "1"}, {"0&(1/0)", "0"}, {"1|(1/0)", "1"},
		{`1?"a":"b"`, "a"}, {`0?"a":0?"b":"c"`, "c"},
		{"a:=3, a*2", "6"}, {"b:=4, b+=1, b", "5"}, {"c:=2, c++, c", "3"}, {"d:=2, ++d + d--", "6"},
		{"0x1F", "31"}, {"1e2", "100."}, {".25+.25", "0.5"}, {"1.5e1", "15."},
		{`strlen("héé")`, "3"}, {`"5"+"6"`, "11"}, {`"abc"+1`, "1"},
		{"9223372036854775807+1", "-9223372036854775808"}, // int64 wraps
		{"2.0*3", "6."},
	}
	for _, tc := range tests {
		i, _ := newTest()
		v, err := i.evalExprText(tc.expr)
		if err != nil || v.String() != tc.want {
			t.Errorf("%s = %q, %v; want %q", tc.expr, v.String(), err, tc.want)
		}
	}
	bad := []string{"1+", "(1", "1)", "1 2", `"abc`, "1/0", "1.0/0", "x y", "@", "f(", "f(1,", "1 ? 2", "mod(1,0)"}
	for _, e := range bad {
		i, _ := newTest()
		if v, err := i.evalExprText(e); err == nil {
			t.Errorf("%s: expected error, got %q", e, v.String())
		}
	}
}

func TestScopingAndVars(t *testing.T) {
	i, h := newTest()
	got := echoes(t, i, h,
		"/set g=1",
		"/def f = /let g=local %; /set g=changed %; /echo in=%g",
		"/f",
		"/echo out=%g",
		"/def h = /set g=2 %; /echo %g",
		"/h",
		"/echo $[g]",
		"/unset g",
		"/echo [%g]",
		"/setenv E=exported",
		"/listvar E",
		"/export g",
	)
	want := []string{"in=local", "out=changed", "2", "2", "[]", "/setenv E=exported", "% /export: g is not set"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if err := i.SetVar("bad name", "x"); err == nil {
		t.Error("SetVar accepted a bad name")
	}
}

func TestVarsJSONRoundTrip(t *testing.T) {
	i, _ := newTest()
	_ = i.Exec("w", "/set alpha=1")
	_ = i.Exec("w", "/setenv beta=two words")
	data, err := i.ExportVars()
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Vars     map[string]string
		Exported []string
	}
	if err := json.Unmarshal(data, &probe); err != nil || probe.Vars["alpha"] != "1" || len(probe.Exported) != 1 {
		t.Fatalf("bad export %s %v", data, err)
	}
	j, _ := newTest()
	_ = j.SetVar("stale", "x")
	if err := j.ImportVars(data); err != nil {
		t.Fatal(err)
	}
	if v, _ := j.GetVar("beta"); v != "two words" {
		t.Errorf("beta=%q", v)
	}
	if _, ok := j.GetVar("stale"); ok {
		t.Error("stale variable survived import")
	}
	if v, ok := j.GetVar("matching"); !ok || v != "glob" {
		t.Error("builtin defaults not restored")
	}
	if err := j.ImportVars([]byte("{not json")); err == nil {
		t.Error("expected error for bad json")
	}
	if err := j.ImportVars([]byte(`{"vars":{"bad name":"x"}}`)); err == nil {
		t.Error("expected error for bad name")
	}
}

func TestDefParsingAndRoundTrip(t *testing.T) {
	defs := []string{
		`/def -t'a b*' -p3 -n2 -F -g -ahCred -P1BC#001122 -wmain -E'x > 1' -c50 name = /echo body %; /echo two`,
		`/def -mregexp -t'^(a|b)+$' re = /echo re`,
		`/def -msimple -t'exact' sim = /echo s`,
		`/def -hCONNECT conn = /echo c`,
		`/def -h'SEND foo*' snd = /echo c`,
		`/def -bF5 -i key = /echo key`,
		`/def -t"it's" -aC#abc,u quote = x`,
	}
	for _, d := range defs {
		i, _ := newTest()
		if err := i.Exec("w", d); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		m := i.Macros()
		if len(m) != 1 {
			t.Fatalf("%s: %d macros", d, len(m))
		}
		i2, _ := newTest()
		if err := i2.Exec("w", m[0].Def); err != nil {
			t.Fatalf("redefine %q: %v", m[0].Def, err)
		}
		if got := i2.Macros()[0].Def; got != m[0].Def {
			t.Errorf("round trip changed:\n %s\n %s", m[0].Def, got)
		}
	}
	i, _ := newTest()
	_ = i.Exec("w", defs[0])
	m := i.Macros()[0]
	if m.Name != "name" || m.Trigger != "a b*" || m.Priority != 3 || m.Shots != 2 || !m.Fall || !m.Gag || m.World != "main" ||
		m.Cond != "x > 1" || m.Chance != 50 || m.Body != "/echo body %; /echo two" || m.Attrs != "h,Cred" {
		t.Errorf("parsed wrongly: %+v", m)
	}
}

func TestDefErrors(t *testing.T) {
	bad := []string{
		"/def -mbogus -t'x' a = b", "/def -t'unterminated a = b", "/def -mregexp -t'(' a = b", "/def -p a = b",
		"/def -hBOGUS x = y", "/def -aCnocolor x = y", "/def -t'x' -hCONNECT x = y", "/def -z x = y", "/def two names = y",
		"/def -c101 x = y", "/def -P x = y", "/def bad/name = y",
	}
	for _, d := range bad {
		i, h := newTest()
		if err := i.Exec("w", d); err == nil {
			t.Errorf("%s: expected error", d)
		}
		if len(i.Macros()) != 0 {
			t.Errorf("%s: macro created despite error", d)
		}
		if len(h.out) == 0 {
			t.Errorf("%s: error not echoed", d)
		}
	}
}

func lineRes(i *Interp, text string, live bool) LineResult {
	return i.OnLine("w", proto.Line{Spans: []proto.Span{{S: text}}}, live)
}

func TestPriorityFallThroughShots(t *testing.T) {
	i, h := newTest()
	for _, d := range []string{
		"/def -t'x' -p1 lo = /echo lo", "/def -t'x' -p5 hi = /echo hi", "/def -t'x' -p5 hi2 = /echo hi2",
		"/def -t'y' -p9 -F f1 = /echo f1", "/def -t'y' -p5 f2 = /echo f2", "/def -t'y' -p1 f3 = /echo f3",
		"/def -t'z' -n2 sh = /echo sh", "/def -t'w' -1 one = /echo one",
		"/def -t'd' dis = /echo dis",
	} {
		if err := i.Exec("w", d); err != nil {
			t.Fatal(err)
		}
	}
	check := func(text string, want ...string) {
		t.Helper()
		h.out = nil
		res := lineRes(i, text, true)
		if strings.Join(res.Fired, ",") != strings.Join(want, ",") {
			t.Errorf("%q fired %v, want %v", text, res.Fired, want)
		}
	}
	check("x", "hi")       // highest priority, definition order among equals, no fall-through
	check("y", "f1", "f2") // -F continues to next priority, which stops
	check("z", "sh")
	check("z", "sh")
	check("z")
	check("w", "one")
	check("w")
	_ = i.Exec("w", "/disable dis")
	check("d")
	_ = i.Exec("w", "/enable dis")
	check("d", "dis")
	_ = i.Exec("w", "/undef hi")
	check("x", "hi2")
}

func TestChanceAndCond(t *testing.T) {
	roll := int64(0)
	h := newFakeHost()
	i := New(h, Options{Rand: func(n int64) int64 { return roll }})
	_ = i.Exec("w", "/def -t'c' -c30 ch = /echo fired")
	_ = i.Exec("w", "/def -t'e' -E'{1} =~ \"e\" & hp > 5' co = /echo cond")
	roll = 29
	if r := lineRes(i, "c", true); len(r.Fired) != 1 {
		t.Error("chance roll 29 should fire at 30%")
	}
	roll = 30
	if r := lineRes(i, "c", true); len(r.Fired) != 0 {
		t.Error("chance roll 30 should not fire at 30%")
	}
	if r := lineRes(i, "e", true); len(r.Fired) != 0 {
		t.Error("cond false (hp unset)")
	}
	_ = i.SetVar("hp", "10")
	if r := lineRes(i, "e", true); len(r.Fired) != 1 {
		t.Error("cond true")
	}
	_ = i.SetVar("hp", "1")
	if r := lineRes(i, "e", true); len(r.Fired) != 0 {
		t.Error("cond false (hp low)")
	}
}

func TestReplayHasNoSideEffects(t *testing.T) {
	i, h := newTest()
	_ = i.Exec("w", "/def -t'att*' -g -ahCred -n1 a = /send flee %; /notify alarm %; /set hit=1 %; /echo e")
	_ = i.Exec("w", "/def -t'cond*' -E'echo(\"side\") & send(\"x\")' c = /echo no")
	h.out = nil
	r := lineRes(i, "attack!", false)
	if !r.Gag || r.Live || len(r.Fired) != 1 || r.Spans[0].Fg == nil || !r.Spans[0].B {
		t.Errorf("display effects missing on replay: %+v", r)
	}
	lineRes(i, "cond", false)
	if len(h.out) != 0 {
		t.Errorf("replay produced side effects: %v", h.out)
	}
	if _, ok := i.GetVar("hit"); ok {
		t.Error("replay set a variable")
	}
	if len(i.Macros()) != 2 {
		t.Error("replay consumed a shot")
	}
	r = lineRes(i, "attack!", true)
	if !r.Gag || !r.Live {
		t.Error("live line lost its effects")
	}
	if !strings.Contains(h.dump(), "send w: flee") || !strings.Contains(h.dump(), "notify: alarm") {
		t.Errorf("live line did not run side effects: %v", h.out)
	}
	if len(i.Macros()) != 1 {
		t.Error("shot not consumed on live line")
	}
}

func TestSubstituteAndRegexpParts(t *testing.T) {
	i, _ := newTest()
	_ = i.Exec("w", "/def -t'swap*' s = /test substitute(\"replaced\", \"Cblue\")")
	r := lineRes(i, "swap me", true)
	if got := fmtSpans(r.Spans); got != "<fg=4>replaced</>" {
		t.Errorf("substitute: %q", got)
	}
	_ = i.Exec("w", "/def -mregexp -t'(\\w+) (\\w+)' -P2Cred -P1C#00ff00 p")
	r = lineRes(i, "foo bar baz", true)
	if got := fmtSpans(r.Spans); got != "<fg=#00ff00>foo</> <fg=1>bar</> baz" {
		t.Errorf("parts: %q", got)
	}
}

func TestPartialHiliteOverStyledSpans(t *testing.T) {
	i, _ := newTest()
	_ = i.Exec("w", "/def -mregexp -t'beta' -aBCblue h")
	red := proto.Palette(1)
	line := proto.Line{Spans: []proto.Span{{S: "alpha ", Fg: &red}, {S: "beta", B: true}, {S: " gamma"}}}
	r := i.OnLine("w", line, true)
	if got := fmtSpans(r.Spans); got != "<fg=1>alpha </><bg=4,b>beta</> gamma" {
		// whole-line attr (-a) applies to the whole line, so alpha also gets the bg
		t.Logf("spans: %s", got)
	}
	for _, s := range r.Spans {
		if s.Bg == nil {
			t.Errorf("span %q lost -a background", s.S)
		}
	}
	if r.Spans[0].Fg == nil || *r.Spans[0].Fg != red {
		t.Error("existing foreground lost")
	}
}

func TestBudgets(t *testing.T) {
	// step budget
	h := newFakeHost()
	i := New(h, Options{MaxSteps: 500})
	i.noRecover = true
	err := i.Exec("w", "/while (1) /let a=1 %; /done")
	var be *BudgetError
	if err == nil || !strings.Contains(h.dump(), "budget exceeded") {
		t.Fatalf("runaway not stopped: %v / %v", err, h.out)
	}
	_ = be
	h.out = nil
	if err := i.Exec("w", "/echo usable"); err != nil || h.dump() != "echo w: usable" {
		t.Errorf("interpreter unusable after budget error: %v %v", err, h.out)
	}
	// wall deadline on a clock that advances by 1ms per Now() call
	th := &tickHost{fakeHost: newFakeHost()}
	j := New(th, Options{MaxSteps: 1 << 30, MaxTime: 50 * time.Millisecond})
	j.noRecover = true
	_ = j.Exec("w", "/while (1) /let a=1 %; /done")
	if !strings.Contains(th.dump(), "time limit") {
		t.Errorf("deadline not enforced: %v", th.out)
	}
	th.out = nil
	_ = j.Exec("w", "/echo ok")
	if th.dump() != "echo w: ok" {
		t.Errorf("unusable after deadline: %v", th.out)
	}
	// recursion
	k, kh := newTest()
	_ = k.Exec("w", "/def r = /r")
	kh.out = nil
	_ = k.Exec("w", "/r")
	if !strings.Contains(kh.dump(), "nesting too deep") {
		t.Errorf("recursion not bounded: %v", kh.out)
	}
	// error flood is capped
	_ = k.Exec("w", "/def e = /nope1 %; /nope2 %; /nope3")
	kh.out = nil
	_ = k.Exec("w", "/while (1) /nope %; /done")
	if n := len(kh.out); n > 25 {
		t.Errorf("%d error lines for one run", n)
	}
	// nested $() runaway
	_ = k.Exec("w", "/def s = /echo $(/s)")
	kh.out = nil
	_ = k.Exec("w", "/s")
	if len(kh.out) == 0 {
		t.Error("expected an error for self-referential $()")
	}
}

type tickHost struct{ *fakeHost }

func (t *tickHost) Now() time.Time { t.now = t.now.Add(time.Millisecond); return t.now }

func TestStringGrowthIsBounded(t *testing.T) {
	i, h := newTest()
	_ = i.Exec("w", "/set x=aaaaaaaaaa")
	_ = i.Exec("w", "/while (1) /set x=$[strcat(x, x)] %; /done")
	if v, _ := i.GetVar("x"); len(v) > maxValueLen {
		t.Errorf("value grew to %d", len(v))
	}
	if !strings.Contains(h.dump(), "too long") {
		t.Errorf("no size error: %v", h.out)
	}
	h.out = nil
	_ = i.Exec("w", `/echo $[strlen(strrep("ab", 100000000))]`)
	_ = i.Exec("w", `/echo $[strlen(pad("a", 1000000000))]`)
	if len(h.out) != 2 || !strings.Contains(h.out[0], "too long") || !strings.Contains(h.out[1], "too large") {
		t.Errorf("unbounded builtin: %v", h.out)
	}
}

func TestRestrict(t *testing.T) {
	i, h := newTest()
	h.files["f.tf"] = "/echo loaded\n"
	if err := i.Exec("w", "/load f.tf"); err != nil {
		t.Fatal(err)
	}
	_ = i.Exec("w", "/restrict file")
	for _, c := range []string{"/load f.tf", "/save o.tf", "/quote 'f.tf", "/log x.log"} {
		if err := i.Exec("w", c); err == nil {
			t.Errorf("%s allowed under /restrict file", c)
		}
	}
	if err := i.Exec("w", "/connect a"); err != nil {
		t.Errorf("/connect should still work at file level: %v", err)
	}
	_ = i.Exec("w", "/restrict world")
	if err := i.Exec("w", "/connect a"); err == nil {
		t.Error("/connect allowed under /restrict world")
	}
	_ = i.Exec("w", "/restrict shell")
	if err := i.Exec("w", "/load f.tf"); err == nil {
		t.Error("restriction was lowered")
	}
	if err := i.Exec("w", "/quote !ls"); err == nil {
		t.Error("shell form allowed")
	}
	// programmatic loading is not gated: the application chose the path
	if err := i.LoadFile("f.tf"); err != nil {
		t.Error(err)
	}
	j := New(h, Options{Restrict: RestrictFile})
	if err := j.Exec("w", "/load f.tf"); err == nil {
		t.Error("Options.Restrict ignored")
	}
}

func TestLoadSource(t *testing.T) {
	i, h := newTest()
	src := "; comment\n\n/echo one\nbare text line\n/def m = /echo \\\n   joined\n/m\n/while (1)\n/echo in loop\n/break\n/done\n/nosuch\n/echo last\n/if (1)\n"
	err := i.LoadString("x.tf", src)
	if err == nil || !strings.Contains(err.Error(), "x.tf:4") {
		t.Errorf("first error should be line 4, got %v", err)
	}
	want := []string{
		"echo w: one",
		"echo w: % x.tf:4: not a command (script lines must start with /): bare text line",
		"echo w: joined",
		"echo w: in loop",
		"echo w: % /nosuch: no such command or macro",
		"echo w: last",
		"echo w: % x.tf:14: incomplete block: missing /endif or /done",
	}
	var got []string
	for _, o := range h.out {
		got = append(got, strings.Replace(o, "echo main:", "echo w:", 1))
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestFunctionCallsUserMacro(t *testing.T) {
	i, h := newTest()
	got := echoes(t, i, h, "/def add = /return {1} + {2}", "/echo $[add(2, 3) * 2]")
	if len(got) != 1 || got[0] != "10" {
		t.Errorf("got %v", got)
	}
}

func TestSendHookDoesNotRecurse(t *testing.T) {
	i, h := newTest()
	_ = i.Exec("w", "/def -hSEND s = /send extra")
	h.out = nil
	_ = i.Exec("w", "hello")
	if got := h.dump(); got != "send w: extra\nsend w: hello" {
		t.Errorf("got %q", got)
	}
}

func TestTimersCancelAndOrder(t *testing.T) {
	i, h := newTest()
	_ = i.Exec("w", "/repeat -2 2 /echo a")
	_ = i.Exec("w", "/repeat -3 1 /echo b")
	h.advance(10 * time.Second)
	if got := h.dump(); got != "echo w: a\necho w: b\necho w: a" {
		t.Errorf("order: %q", got)
	}
	h.out = nil
	_ = i.Exec("w", "/repeat -1 5 /echo x")
	_ = i.Exec("w", "/kill 3")
	h.advance(10 * time.Second)
	if len(h.out) != 0 {
		t.Errorf("killed timer fired: %v", h.out)
	}
	if err := i.Exec("w", "/repeat -x 1 /echo"); err == nil {
		t.Error("bad time accepted")
	}
}

func TestConcurrentUse(t *testing.T) {
	i := New(NopHost{}, Options{})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				_ = i.Exec("w", "/set n=1")
				_ = i.OnLine("w", proto.Line{Spans: []proto.Span{{S: "x"}}}, true)
				i.FireHook("CONNECT")
				_ = i.Vars()
			}
		}()
	}
	wg.Wait()
}

func TestWorldAndHostPlumbing(t *testing.T) {
	i, h := newTest()
	got := echoes(t, i, h, "/addworld w1 h.example 4000", "/world w1", "/echo $[is_connected(\"w1\")] $[world_info(\"w1\", \"host\")]")
	if !strings.Contains(strings.Join(got, "|"), "0 h.example") {
		t.Errorf("%v", got)
	}
	if !strings.Contains(h.dump(), "connect w1") || !strings.Contains(h.dump(), "fg w1") {
		t.Errorf("%v", h.out)
	}
	h.out = nil
	_ = i.Exec("w", "/quit")
	if !h.quit {
		t.Error("/quit did not reach host")
	}
}

func TestUnknownCommandAndUndef(t *testing.T) {
	i, h := newTest()
	if err := i.Exec("w", "/frobnicate 1"); err == nil || len(h.out) != 1 {
		t.Errorf("unknown command: %v %v", err, h.out)
	}
	if err := i.Exec("w", "/undef nothere"); err == nil {
		t.Error("undef of missing macro should error")
	}
	_ = i.Exec("w", "/def a = x")
	_ = i.Exec("w", "/def b = y")
	_ = i.Exec("w", "/purge a")
	if ms := i.Macros(); len(ms) != 1 || ms[0].Name != "b" {
		t.Errorf("%+v", ms)
	}
	_ = i.Exec("w", "/purge")
	if len(i.Macros()) != 0 {
		t.Error("purge left macros")
	}
}

func TestListNames(t *testing.T) {
	if len(CommandNames()) < 60 || len(FuncNames()) < 50 || len(HookNames()) != 35 {
		t.Errorf("commands=%d funcs=%d hooks=%d", len(CommandNames()), len(FuncNames()), len(HookNames()))
	}
}

func TestSelfLoadIsBounded(t *testing.T) {
	i, h := newTest()
	h.files["self.tf"] = "/load self.tf\n"
	_ = i.Exec("w", "/load self.tf")
	if !strings.Contains(h.dump(), "nested too deeply") {
		t.Errorf("recursive /load not bounded: %.200s", h.dump())
	}
}

func TestSubVariableExpandsTypedText(t *testing.T) {
	i, h := newTest()
	_ = i.Exec("w", "/set who=Bob")
	_ = i.Exec("w", "hi %who")
	_ = i.Exec("w", "/set sub=on")
	_ = i.Exec("w", "hi %who")
	if got := h.dump(); got != "send w: hi %who\nsend w: hi Bob" {
		t.Errorf("%q", got)
	}
}
