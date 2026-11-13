package lua

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

func pc(n uint8) *proto.Color        { c := proto.Palette(n); return &c }
func rgb(r, g, b uint8) *proto.Color { c := proto.RGB(r, g, b); return &c }

func TestTriggerGlobCapturesAndSend(t *testing.T) {
	r, h := newRT(t, Options{})
	mustLoad(t, r, `
	  squat.trigger{match = "You are hit by * for * damage.", fn = function(m)
	    squat.send("heal " .. m[1] .. " " .. m[2])
	    squat.echo("ok " .. m.text)
	  end}
	  squat.trigger{match = "^HP: (?P<hp>\\d+)/(\\d+)", regex = true, fn = function(m)
	    squat.var.set("hp", tonumber(m.named.hp)); squat.send("hp=" .. m[1] .. "," .. m[2])
	  end}
	`)
	r.OnLine("w1", line("You are hit by a rock for 12 damage."), true)
	r.OnLine("w1", line("HP: 40/100"), true)
	want := []sent{{"w1", "heal a rock 12"}, {"w1", "hp=40,100"}}
	if !reflect.DeepEqual(h.sends, want) {
		t.Fatalf("sends = %v", h.sends)
	}
	if v, _ := r.GetVar("hp"); v != 40.0 {
		t.Fatalf("hp = %v", v)
	}
	if len(h.echoes) != 1 || h.echoes[0][0].S != "ok You are hit by a rock for 12 damage." {
		t.Fatalf("echo = %v", h.echoes)
	}
}

func TestGlobIsAnchoredAndEscaped(t *testing.T) {
	r, h := newRT(t, Options{})
	mustLoad(t, r, `
	  squat.trigger{match = "a?c", fn = function(m) squat.send("q") end}
	  squat.trigger{match = "cost \\*5", fn = function(m) squat.send("star") end}
	  squat.trigger{match = "A.B", icase = true, fn = function(m) squat.send("ci") end}
	`)
	for _, s := range []string{"abc", "xabc", "abcx", "cost *5", "cost 55", "a.b"} {
		r.OnLine("w", line(s), true)
	}
	got := []string{}
	for _, s := range h.sends {
		got = append(got, s.text)
	}
	if !reflect.DeepEqual(got, []string{"q", "star", "ci"}) {
		t.Fatalf("got %v", got)
	}
}

func TestGagSpamAndHilite(t *testing.T) {
	r, _ := newRT(t, Options{})
	mustLoad(t, r, `
	  squat.trigger{match = "*spam*", gag = true}
	  squat.trigger{match = "Alert!", regex = true, fg = "#ff8800", bg = 17, b = true}
	  squat.trigger{match = "danger", regex = true, hilite = "match", fg = 196}
	`)
	if res := r.OnLine("w", line("buy spam now"), true); !res.Gag {
		t.Fatal("expected gag")
	}
	res := r.OnLine("w", line("Alert! and more"), true)
	if res.Gag || !res.Modified || len(res.Spans) != 1 {
		t.Fatalf("res = %+v", res)
	}
	s := res.Spans[0]
	if *s.Fg != proto.RGB(0xff, 0x88, 0) || *s.Bg != proto.Palette(17) || !s.B {
		t.Fatalf("span = %+v", s)
	}
	res = r.OnLine("w", line("a danger here"), true)
	want := []proto.Span{{S: "a "}, {S: "danger", Fg: pc(196)}, {S: " here"}}
	if !reflect.DeepEqual(res.Spans, want) {
		t.Fatalf("spans = %+v", res.Spans)
	}
}

func TestColourRoundTrip(t *testing.T) {
	r, h := newRT(t, Options{})
	// Echo with every kind of colour; each must arrive byte-for-byte.
	mustLoad(t, r, `
	  squat.echo({
	    {s = "p0", fg = 0}, {s = "p15", fg = 15}, {s = "p16", fg = 16, bg = 231},
	    {s = "p255", fg = 255, bg = 232}, {s = "tc", fg = "#010203", bg = "#FFfe00"},
	    {s = "attrs", b = true, dim = true, i = true, u = true, st = true, blink = true, rev = true, link = "http://x"},
	  }, "w")`)
	got := h.echoes[0]
	want := []proto.Span{
		{S: "p0", Fg: pc(0)}, {S: "p15", Fg: pc(15)}, {S: "p16", Fg: pc(16), Bg: pc(231)},
		{S: "p255", Fg: pc(255), Bg: pc(232)}, {S: "tc", Fg: rgb(1, 2, 3), Bg: rgb(255, 254, 0)},
		{S: "attrs", B: true, Dim: true, I: true, U: true, St: true, Blink: true, Rev: true, Link: "http://x"},
	}
	if !reflect.DeepEqual(got, want) {
		bj, _ := json.Marshal(got)
		t.Fatalf("got %s", bj)
	}
	// Incoming spans -> Lua -> back unchanged, for all 256 palette indices and truecolor.
	var in []proto.Span
	for i := 0; i < 256; i++ {
		in = append(in, proto.Span{S: "x", Fg: pc(uint8(i)), Bg: pc(uint8(255 - i))})
	}
	in = append(in, proto.Span{S: "t", Fg: rgb(0xab, 0xcd, 0xef), Bg: rgb(0, 0, 1)}, proto.Span{S: "plain"})
	mustLoad(t, r, `squat.on("line", function(l)
	    local copy = {}
	    for i, s in ipairs(l.spans) do copy[i] = s end
	    return {spans = copy}
	  end)`)
	res := r.OnLine("w", proto.Line{Spans: in}, true)
	if !reflect.DeepEqual(res.Spans, in) {
		t.Fatal("spans did not round-trip through Lua")
	}
	// Invalid colours are rejected.
	for _, bad := range []string{`fg = 256`, `fg = -1`, `fg = 1.5`, `fg = "#12345"`, `fg = "red"`, `fg = "#gggggg"`} {
		if err := r.LoadString("bad", `squat.echo({{s="x", `+bad+`}}, "w")`); err == nil {
			t.Fatalf("expected error for %s", bad)
		}
	}
}

func TestLineHandlerSpansAndGag(t *testing.T) {
	r, _ := newRT(t, Options{})
	mustLoad(t, r, `
	  squat.on("line", function(l)
	    if l.text:find("secret") then return {gag = true} end
	    if l.text:find("shiny") then
	      l.spans = {{s = "very ", fg = 3}, {s = l.text, fg = "#102030"}}
	    end
	  end)
	`)
	if !r.OnLine("w", line("a secret"), true).Gag {
		t.Fatal("gag failed")
	}
	res := r.OnLine("w", line("shiny"), true)
	want := []proto.Span{{S: "very ", Fg: pc(3)}, {S: "shiny", Fg: rgb(0x10, 0x20, 0x30)}}
	if !reflect.DeepEqual(res.Spans, want) || !res.Modified {
		t.Fatalf("res = %+v", res)
	}
	res = r.OnLine("w", line("plain"), true)
	if res.Modified || res.Gag || res.Spans[0].S != "plain" {
		t.Fatalf("res = %+v", res)
	}
}

func TestHandlerSeesFields(t *testing.T) {
	r, h := newRT(t, Options{})
	mustLoad(t, r, `squat.on("line", function(l)
	  squat.log(string.format("%s|%s|%s|%s", l.text, l.world, tostring(l.prompt), tostring(l.live)))
	  squat.log(squat.world())
	end)`)
	l := line("Name:")
	l.Prompt = true
	r.OnLine("w9", l, true)
	if !strings.Contains(h.logText(), "Name:|w9|true|true") || !strings.Contains(h.logText(), "World-w9") {
		t.Fatalf("logs: %s", h.logText())
	}
}

func TestPromptEventOnlyOnPrompts(t *testing.T) {
	r, h := newRT(t, Options{})
	mustLoad(t, r, `squat.on("prompt", function(l) squat.send("p:" .. l.text) end)`)
	r.OnLine("w", line("not a prompt"), true)
	p := line("> ")
	p.Prompt = true
	r.OnLine("w", p, true)
	if len(h.sends) != 1 || h.sends[0].text != "p:> " {
		t.Fatalf("sends = %v", h.sends)
	}
}

func TestLiveFalseSuppressesSideEffects(t *testing.T) {
	r, h := newRT(t, Options{})
	mustLoad(t, r, `
	  fired = 0
	  squat.trigger{match = "*boom*", gag = true, fg = 1, fn = function(m) squat.send("fired") end}
	  squat.on("line", function(l)
	    local a = squat.send("x")
	    local b = squat.echo("y")
	    local c = squat.notify("t", "b")
	    local d = squat.tf("/x")
	    local e = squat.var.set("k", 1)
	    local f = squat.after(1, function() end)
	    local g = squat.on("line", function() end)
	    local i = squat.trigger{match="z", fn=function() end}
	    squat.log(string.format("%s %s %s %s %s %s %s %s", tostring(a), tostring(b), tostring(c), tostring(d), tostring(e), tostring(f), tostring(g), tostring(i)))
	    return {gag = l.text:find("secret") ~= nil, spans = {{s = "R:" .. l.text, fg = "#abcdef"}}}
	  end)
	`)
	res := r.OnLine("w", line("boom secret"), false)
	if !res.Gag || len(res.Spans) != 1 || res.Spans[0].S != "R:boom secret" || *res.Spans[0].Fg != proto.RGB(0xab, 0xcd, 0xef) {
		t.Fatalf("display effects must still apply on replay: %+v", res)
	}
	if len(h.sends)+len(h.echoes)+len(h.notifies)+len(h.tf) != 0 {
		t.Fatalf("side effects on replay: %v %v %v %v", h.sends, h.echoes, h.notifies, h.tf)
	}
	if _, ok := r.GetVar("k"); ok {
		t.Fatal("var.set must not run on replay")
	}
	if len(h.timers) != 0 {
		t.Fatal("timer created on replay")
	}
	if !strings.Contains(h.logText(), "false false false false false false false false") {
		t.Fatalf("API must return false when suppressed: %s", h.logText())
	}
	// Same line live: trigger fn fires.
	r.OnLine("w", line("boom"), true)
	if len(h.sends) < 1 || h.sends[0].text != "fired" {
		t.Fatalf("live trigger did not fire: %v", h.sends)
	}
}

func TestPriorityOrderingDeterministic(t *testing.T) {
	r, h := newRT(t, Options{})
	mustLoad(t, r, `
	  local function mk(n) return function() squat.send(n) end end
	  squat.trigger{match = "*", priority = 0,  fn = mk("p0-first")}
	  squat.on("line", mk("ev-p5"), 5)
	  squat.trigger{match = "*", priority = 10, fn = mk("p10")}
	  squat.trigger{match = "*", priority = 0,  fn = mk("p0-second")}
	  squat.trigger{match = "*", priority = -3, fn = mk("m3")}
	  squat.trigger{match = "*", priority = 5,  fn = mk("p5-trigger")}
	`)
	for i := 0; i < 3; i++ {
		h.sends = nil
		r.OnLine("w", line("x"), true)
		var got []string
		for _, s := range h.sends {
			got = append(got, s.text)
		}
		want := []string{"p10", "ev-p5", "p5-trigger", "p0-first", "p0-second", "m3"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("order %v", got)
		}
	}
}

func TestStopAndGagContinues(t *testing.T) {
	r, h := newRT(t, Options{})
	mustLoad(t, r, `
	  squat.trigger{match = "*", priority = 3, gag = true}
	  squat.trigger{match = "*", priority = 2, fn = function() squat.send("after-gag") end}
	  squat.on("line", function() return {stop = true} end, 1)
	  squat.trigger{match = "*", priority = 0, fn = function() squat.send("never") end}
	`)
	res := r.OnLine("w", line("x"), true)
	if !res.Gag || len(h.sends) != 1 || h.sends[0].text != "after-gag" {
		t.Fatalf("res=%+v sends=%v", res, h.sends)
	}
}

func TestTriggerWorldFilter(t *testing.T) {
	r, h := newRT(t, Options{})
	mustLoad(t, r, `squat.trigger{match="*", world="World-b", fn=function() squat.send("only-b") end}
	  squat.trigger{match="*", world="c", fn=function() squat.send("only-c") end}`)
	r.OnLine("a", line("x"), true)
	r.OnLine("b", line("x"), true)
	r.OnLine("c", line("x"), true)
	if len(h.sends) != 2 || h.sends[0].text != "only-b" || h.sends[1].text != "only-c" {
		t.Fatalf("sends=%v", h.sends)
	}
}

func TestAlias(t *testing.T) {
	r, h := newRT(t, Options{})
	mustLoad(t, r, `
	  squat.alias{match = "k *", fn = function(m) squat.send("kill " .. m[1]) end}
	  squat.alias{match = "^go (\\w+)$", regex = true, priority = 5, fn = function(m) if m[1] == "away" then return false end squat.send("walk " .. m[1]) end}
	  squat.on("input", function(i) if i.text == "/magic" then squat.echo("magic!") return true end end)
	`)
	if !r.OnInput("w", "k orc") || h.sends[0].text != "kill orc" {
		t.Fatalf("alias not handled: %v", h.sends)
	}
	if !r.OnInput("w", "go north") || h.sends[1].text != "walk north" {
		t.Fatalf("regex alias: %v", h.sends)
	}
	if r.OnInput("w", "go away") {
		t.Fatal("alias returning false must not mark handled")
	}
	if !r.OnInput("w", "/magic") || len(h.echoes) != 1 {
		t.Fatal("input handler failed")
	}
	if r.OnInput("w", "plain text") {
		t.Fatal("unmatched input must pass through")
	}
}

func TestTimersWithFakeClock(t *testing.T) {
	r, h := newRT(t, Options{})
	mustLoad(t, r, `squat.on("connect", function() n = 0
	  squat.timer{every = 2, count = 3, fn = function() n = n + 1; squat.send("tick" .. n) end}
	  squat.after(5, function() squat.send("once") end)
	  forever = squat.timer{every = 1, fn = function() squat.send("f") end}
	  squat.after(3.5, function() squat.cancel(forever) end)
	end)`)
	r.FireEvent("connect", map[string]any{"world": "w"})
	h.sends = nil
	h.advance(1 * time.Second)
	h.advance(10 * time.Second)
	var got []string
	for _, s := range h.sends {
		got = append(got, s.text)
	}
	want := []string{"f", "tick1", "f", "f", "tick2", "once", "tick3"}
	// order by time: f@1, tick1@2, f@2, f@3, (cancel@3.5), tick2@4, once@5, tick3@6
	// f fires at 1,2,3 only (cancelled at 3.5); at t=2 tick1 was armed before f's re-arm.
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if len(r.timers) != 0 {
		t.Fatalf("timers left: %d", len(r.timers))
	}
}

func TestTimerWorldCapturedAndErrorsIsolated(t *testing.T) {
	r, h := newRT(t, Options{})
	mustLoad(t, r, `squat.on("connect", function(e) squat.after(1, function() squat.send("hi") end) end)
	  squat.timer{every = 1, count = 2, fn = function() error("boom") end}`)
	r.FireEvent("connect", map[string]any{"world": "wz"})
	h.advance(3 * time.Second)
	if len(h.sends) != 1 || h.sends[0].world != "wz" {
		t.Fatalf("sends=%v", h.sends)
	}
	if n := len(h.errorsLogged()); n != 2 {
		t.Fatalf("want 2 timer errors, got %d", n)
	}
}

func TestTimerMinInterval(t *testing.T) {
	r, _ := newRT(t, Options{MinTimerInterval: time.Second})
	if err := r.LoadString("t", `squat.after(0.1, function() end)`); err == nil {
		t.Fatal("expected min interval error")
	}
	if err := r.LoadString("t", `squat.timer{every = 0, fn = function() end}`); err == nil {
		t.Fatal("expected error")
	}
}

func TestEvents(t *testing.T) {
	r, h := newRT(t, Options{})
	mustLoad(t, r, `
	  squat.on("connect", function(e) squat.send("hello " .. e.host) end)
	  squat.on("disconnect", function(e) squat.log("bye " .. e.reason) end)
	  squat.on("gmcp", function(g) squat.log("gmcp " .. g.package .. " " .. g.data.hp .. " " .. g.data.tags[2]) end)
	  squat.on("timer", function(e) squat.log("tick " .. e.n) end)
	`)
	r.FireEvent("connect", map[string]any{"world": "w", "host": "mud.example"})
	r.FireEvent("disconnect", map[string]any{"world": "w", "reason": "closed"})
	r.FireEvent("gmcp", proto.GmcpMsg{Session: "w", Package: "Char.Vitals", Data: json.RawMessage(`{"hp":42,"tags":["a","b"]}`)})
	r.FireEvent("timer", map[string]any{"n": 7})
	if len(h.sends) != 1 || h.sends[0] != (sent{"w", "hello mud.example"}) {
		t.Fatalf("sends=%v", h.sends)
	}
	lt := h.logText()
	for _, w := range []string{"bye closed", "gmcp Char.Vitals 42 b", "tick 7"} {
		if !strings.Contains(lt, w) {
			t.Fatalf("missing %q in %s", w, lt)
		}
	}
}

func TestUnknownEventRejected(t *testing.T) {
	r, _ := newRT(t, Options{})
	if err := r.LoadString("t", `squat.on("nope", function() end)`); err == nil {
		t.Fatal("expected error")
	}
}

func TestTFCallAndReentry(t *testing.T) {
	r, h := newRT(t, Options{})
	mustLoad(t, r, `
	  squat.trigger{match = "go", fn = function()
	    assert(squat.tf("/echo hi"))
	    squat.tf("/lua squat.send('from-tf')")
	  end}
	`)
	// RunTF behaviour: second call re-enters Lua like TF's /lua would.
	fh := &reentrantTF{fakeHost: h}
	r.host = fh
	fh.rt = r
	r.OnLine("w", line("go"), true)
	if len(fh.tf) != 2 || fh.tf[0] != "w:/echo hi" {
		t.Fatalf("tf=%v", fh.tf)
	}
	if len(fh.sends) != 1 || fh.sends[0].text != "from-tf" {
		t.Fatalf("reentrant exec failed: sends=%v logs=%s", fh.sends, fh.logText())
	}
}

type reentrantTF struct{ *fakeHost }

func (f *reentrantTF) RunTF(w, l string) error {
	f.tf = append(f.tf, w+":"+l)
	if strings.HasPrefix(l, "/lua ") {
		return f.rt.ExecInCallback(strings.TrimPrefix(l, "/lua "))
	}
	return nil
}

func TestTFError(t *testing.T) {
	r, h := newRT(t, Options{})
	h.tfErr = errString("tf broke")
	mustLoad(t, r, `
	  local ok, err = squat.tf("/x", "w")
	  assert(ok == nil and err == "tf broke", tostring(err))`)
}

type errString string

func (e errString) Error() string { return string(e) }

func TestVarsExportImport(t *testing.T) {
	r, _ := newRT(t, Options{})
	mustLoad(t, r, `
	  squat.var.set("name", "bob")
	  squat.var.set("hp", 42.5)
	  squat.var.set("flags", {true, false})
	  squat.var.set("nested", {a = {b = {1, 2, 3}}, s = "x"})
	  squat.var.set("gone", 1); squat.var.del("gone")
	  assert(squat.var.get("name") == "bob" and squat.var.get("missing", "dflt") == "dflt")
	  assert(squat.var.get("nested").a.b[3] == 3)
	`)
	data, err := r.ExportVars()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil || m["hp"] != 42.5 || m["gone"] != nil || len(m) != 4 {
		t.Fatalf("export = %s (%v)", data, err)
	}
	r2, _ := newRT(t, Options{})
	if err := r2.ImportVars(data); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Vars(), r2.Vars()) {
		t.Fatalf("vars differ: %v vs %v", r.Vars(), r2.Vars())
	}
	if err := r2.LoadString("x", `assert(squat.var.get("nested").a.b[2] == 2); assert(squat.var.get("flags")[1] == true)`); err != nil {
		t.Fatal(err)
	}
	if err := r2.ImportVars([]byte(`not json`)); err == nil {
		t.Fatal("expected error")
	}
	if len(r2.Vars()) != 4 {
		t.Fatal("failed import must leave store unchanged")
	}
	// Non-serialisable values are rejected.
	if err := r2.LoadString("x", `squat.var.set("f", function() end)`); err == nil {
		t.Fatal("function value must be rejected")
	}
	if err := r2.LoadString("x", `local t = {} t.self = t squat.var.set("c", t)`); err == nil {
		t.Fatal("cycle must be rejected")
	}
}

func TestVarsSurviveReload(t *testing.T) {
	r, _ := newRT(t, Options{})
	mustLoad(t, r, `squat.var.set("k", "v")`)
	if err := r.Reload(); err != nil {
		t.Fatal(err)
	}
	if v, _ := r.GetVar("k"); v != "v" {
		t.Fatalf("v=%v", v)
	}
}

func TestRegex(t *testing.T) {
	r, _ := newRT(t, Options{})
	mustLoad(t, r, `
	  local m = squat.regex.match("(\\w+) says (?P<what>.*)", "Bob says hi there")
	  assert(m[0] == "Bob says hi there" and m[1] == "Bob" and m[2] == "hi there" and m.named.what == "hi there")
	  assert(squat.regex.match("^x", "abc") == nil)
	  assert(squat.regex.test("a+b", "caab") and not squat.regex.test("^a+b$", "caab"))
	  assert(squat.regex.replace("(\\d+)", "a1b22", "<$1>") == "a<1>b<22>")
	  assert(not pcall(squat.regex.match, "(", "x"))
	  assert(not pcall(squat.regex.match, "(a)\\1", "aa"))  -- RE2: no backreferences
	`)
}

func TestExecConsolePersists(t *testing.T) {
	r, _ := newRT(t, Options{})
	if err := r.Exec("w", `x = 5`); err != nil {
		t.Fatal(err)
	}
	if err := r.Exec("w", `assert(x == 5)`); err != nil {
		t.Fatal(err)
	}
	if err := r.Exec("w", `error("bad")`); err == nil {
		t.Fatal("expected error")
	}
}

func TestClosedRuntime(t *testing.T) {
	r, h := newRT(t, Options{})
	mustLoad(t, r, `squat.timer{every=1, fn=function() squat.send("t") end}
	  squat.trigger{match="*", gag=true}`)
	r.Close()
	h.advance(5 * time.Second)
	if len(h.sends) != 0 {
		t.Fatal("timer fired after close")
	}
	if res := r.OnLine("w", line("x"), true); res.Gag || res.Spans[0].S != "x" {
		t.Fatalf("closed runtime must pass through: %+v", res)
	}
	if err := r.LoadString("x", "return"); err != ErrClosed {
		t.Fatalf("err=%v", err)
	}
	r.Close()
}

func TestConcurrentTimersAndLines(t *testing.T) {
	r, h := newRT(t, Options{})
	mustLoad(t, r, `squat.timer{every = 0.05, fn = function() squat.var.set("a", squat.now()) end}
	  squat.trigger{match = "*", fn = function() squat.var.set("b", 1) end}`)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			h.advance(50 * time.Millisecond)
		}
		close(done)
	}()
	for i := 0; i < 200; i++ {
		r.OnLine("w", line("x"), true)
		r.OnInput("w", "y")
	}
	<-done
}
