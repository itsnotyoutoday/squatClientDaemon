package tf

import (
	"strings"
	"testing"
)

// Regression tests for behaviour verified against real TinyFugue with the
// oracle harness (docs/TF-ORACLE.md).

func evalOut(t *testing.T, lines ...string) string {
	t.Helper()
	i, h := newTest()
	for _, l := range lines {
		_ = i.Exec("w", l)
	}
	var out []string
	for _, l := range h.out {
		out = append(out, strings.TrimPrefix(l, "echo w: "))
	}
	return strings.Join(out, "|")
}

func TestCompatExpressions(t *testing.T) {
	cases := []struct{ expr, want string }{
		{`0.1 + 0.2`, "0.3"}, {`pow(2,10)`, "1024."}, {`pow(-2,3)`, "-8"}, {`1e20`, "1e+20"},
		{`3 & 4`, "4"}, {`0 | 5`, "5"}, {`"12abc" * 2`, "24"}, {`"abc" == 0`, "1"},
		{`5 = 5`, "1"}, {`trunc(3.9)`, "3"}, {`whatis(trunc(3.9))`, "int"},
		{`"Hello" =/ "hello"`, "1"}, {`"foo" =/ "{foo|bar}"`, "1"}, {`"foop" =/ "{foo}*"`, "0"},
		{`strcmp("a","b") < 0`, "1"}, {`replace("l","L","hello")`, "heLLo"},
		{`substr("hello",-2)`, "lo"}, {`substr("hello",1,-1)`, "ell"}, {`strstr("hello","l",-2)`, "3"},
		{`ascii("")`, "0"}, {`regmatch("(x)?y","y")`, "1"}, {`regmatch("(a)(b)","ab")`, "3"},
		{`regmatch("abc","ABC")`, "1"}, {`regmatch("ABC","abc")`, "0"},
	}
	for _, c := range cases {
		if got := evalOut(t, "/echo $["+c.expr+"]"); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}

func TestCompatSubstitution(t *testing.T) {
	got := evalOut(t,
		"/def sb = /echo [%-1] [%{-1}] [%L] [%{L2}] [%{9-d}] [%{1-x}]",
		"/sb a b c",
		`/eval /echo \%{x} \\ \65`,
		"/echo a;b",
		"/set x=1",
		"/def sc = /let x=2 %; /sd",
		"/def sd = /echo [%x]",
		"/sc",
		"/def se = /let n=5 %; /set n=6 %; /echo [%n]",
		"/se",
		"/echo [%n]",
	)
	want := "[b c] [b c] [c] [b] [d] [a]|A|a;b|[2]|[5]|[6]"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestCompatForLine(t *testing.T) {
	got := evalOut(t,
		"/for i 1 3 /echo n%%i",
		"/for i 1 2 /echo a%%i %; /echo b",
		"/echo after [%i]",
	)
	if want := "n1|n2|n3|a1|a2|b|after []"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestCompatTriggerFamily(t *testing.T) {
	i, h := newTest()
	for _, l := range []string{
		"/trig foo * = /echo t1 [%*]",
		"/trigp 5 bar = /echo t5",
		"/gag secret",
		"/hook REDEF = /echo hook [%*]",
		"/def x = 1",
		"/def x = 2",
		"/def x = 2",
		"/trigger foo a b",
		"/trigger bar",
		"/untrig bar",
		"/trigger bar",
	} {
		_ = i.Exec("w", l)
	}
	got := h.dump()
	for _, w := range []string{"echo w: hook [macro x]", "echo w: t1 [foo a b]", "echo w: t5"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
	if strings.Count(got, "t5") != 1 {
		t.Errorf("untrig did not remove the trigger:\n%s", got)
	}
	if strings.Count(got, "hook") != 1 {
		t.Errorf("REDEF must fire once (identical redefinition is silent):\n%s", got)
	}
	r := lineRes(i, "SECRET stuff", true)
	_ = r
}

// kbHost implements the optional InputBuffer and UIControl interfaces.
type kbHost struct {
	*fakeHost
	text    string
	pos     int
	visual  []bool
	redraws int
	suspend int
}

func (k *kbHost) InputBuffer() (string, int)     { return k.text, k.pos }
func (k *kbHost) SetInputBuffer(t string, p int) { k.text, k.pos = t, p }
func (k *kbHost) SetVisual(on bool)              { k.visual = append(k.visual, on) }
func (k *kbHost) Redraw()                        { k.redraws++ }
func (k *kbHost) Suspend()                       { k.suspend++ }

func TestInputBufferFunctions(t *testing.T) {
	k := &kbHost{fakeHost: newFakeHost()}
	i := New(k, Options{})
	for _, l := range []string{
		"/input hello world (x)",
		"/echo [$[kblen()]] [$[kbpoint()]] [$[kbgoto(5)]] [$[kbhead()]] [$[kbtail()]]",
		"/echo [$[kbwordright(6)]] [$[kbwordleft(11)]] [$[kbmatch(1)]] [$[kbdel(8)]] [$[kbhead()]]|[$[kbtail()]]",
		"/dokey end",
		"/dokey wleft",
		"/echo [$[kbpoint()]]",
		"/dokey dline",
		"/echo [$[kblen()]]",
		"/set visual=on",
		"/dokey redraw",
		"/suspend",
	} {
		_ = i.Exec("w", l)
	}
	want := []string{
		"echo w: [15] [15] [5] [hello] [ world (x)]",
		"echo w: [11] [6] [14] [5] [hello]|[rld (x)]",
		"echo w: [10]",
		"echo w: [0]",
	}
	if got := k.out; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(k.visual) != 1 || !k.visual[0] || k.redraws != 1 || k.suspend != 1 {
		t.Errorf("UIControl: visual=%v redraws=%d suspend=%d", k.visual, k.redraws, k.suspend)
	}
}

func TestShadowInputBuffer(t *testing.T) {
	// a Host without InputBuffer still gets a working buffer, and /input is
	// still forwarded to the host
	got := evalOut(t, "/input abc", "/echo [$[kbhead()]] [$[kbpoint()]]")
	if !strings.Contains(got, "[abc] [3]") {
		t.Errorf("shadow buffer: %q", got)
	}
}

func TestPsAndKill(t *testing.T) {
	got := evalOut(t,
		"/repeat -0:30 2 /echo x",
		"/ps",
		"/ps -s",
		"/kill 1",
		"/ps -s",
	)
	if !strings.Contains(got, "PID     NEXT T D WORLD       PTIME COUNT COMMAND") || !strings.Contains(got, "0:30:00     2 /echo x") {
		t.Errorf("ps format: %q", got)
	}
}
