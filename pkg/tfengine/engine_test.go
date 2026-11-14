package tfengine

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/ansi"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// fakeHost records what the engine asked of its host.
type fakeHost struct {
	NopHost
	mu      sync.Mutex
	log     []string
	sent    []string
	sendErr error
	worlds  []WorldInfo
	fg      string
}

func (h *fakeHost) rec(f string, a ...any) {
	h.mu.Lock()
	h.log = append(h.log, fmt.Sprintf(f, a...))
	h.mu.Unlock()
}

func (h *fakeHost) Echo(world string, spans []proto.Span) {
	var sb strings.Builder
	for _, sp := range spans {
		attr := ""
		if sp.Fg != nil {
			attr += "fg=" + sp.Fg.String()
		}
		if sp.B {
			attr += "B"
		}
		if attr != "" {
			sb.WriteString("<" + attr + ">" + sp.S + "</>")
		} else {
			sb.WriteString(sp.S)
		}
	}
	h.rec("echo %s: %s", world, sb.String())
}
func (h *fakeHost) SendToWorld(world, text string) error {
	h.mu.Lock()
	h.sent = append(h.sent, world+":"+text)
	h.mu.Unlock()
	return h.sendErr
}
func (h *fakeHost) Connect(w string) error      { h.rec("connect %s", w); return nil }
func (h *fakeHost) Disconnect(w string) error   { h.rec("dc %s", w); return nil }
func (h *fakeHost) Foreground(w string) error   { h.rec("fg %s", w); h.fg = w; return nil }
func (h *fakeHost) ForegroundWorld() string     { return h.fg }
func (h *fakeHost) ListWorlds() []WorldInfo     { return h.worlds }
func (h *fakeHost) IsConnected(w string) bool   { return w == "up" }
func (h *fakeHost) AddWorld(w WorldInfo) error  { h.rec("addworld %+v", w); return nil }
func (h *fakeHost) RemoveWorld(n string) error  { h.rec("unworld %s", n); return nil }
func (h *fakeHost) SaveWorlds() error           { h.rec("saveworld"); return nil }
func (h *fakeHost) Bell()                       { h.rec("bell") }
func (h *fakeHost) LocalEcho(w string, on bool) { h.rec("localecho %s %v", w, on) }
func (h *fakeHost) Limit(w, op, p string)       { h.rec("%s %s %q", op, w, p) }
func (h *fakeHost) Recall(w, a string)          { h.rec("recall %s %q", w, a) }
func (h *fakeHost) Log(w string, on bool, p string) error {
	h.rec("log %s %v %s", w, on, p)
	return nil
}
func (h *fakeHost) Status(op, f string, a ...string) { h.rec("%s %s %v", op, f, a) }
func (h *fakeHost) Input(w, t string)                { h.rec("input %s %q", w, t) }
func (h *fakeHost) Quit()                            { h.rec("quit") }
func (h *fakeHost) Screen() (int, int)               { return 100, 30 }

func (h *fakeHost) dump() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.log, "\n")
}

func (h *fakeHost) sentTexts() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.sent...)
}

func newTest(t *testing.T, mod ...func(*Options)) (*Engine, *fakeHost) {
	t.Helper()
	h := &fakeHost{}
	o := Options{}
	for _, m := range mod {
		m(&o)
	}
	e := New(h, o)
	t.Cleanup(e.Close)
	return e, h
}

func line(s string) proto.Line {
	return proto.Line{V: 1, T: "line", Kind: proto.KindGame, Spans: []proto.Span{{S: s}}}
}

func TestEchoAttrsReachHost(t *testing.T) {
	e, h := newTest(t)
	_ = e.Exec("w", "/echo -aC#112233,BC200 hi")
	_ = e.Exec("w", "/echo -p x@{Cbrightcyan}y")
	_ = e.Exec("w", "/echo plain")
	if got := h.dump(); got != "echo w: <fg=#112233>hi</>\necho w: x<fg=14>y</>\necho w: plain" {
		t.Errorf("%q", got)
	}
}

func TestEchoToOtherWorld(t *testing.T) {
	e, h := newTest(t)
	_ = e.Exec("w", "/echo -wother there")
	if got := h.dump(); got != "echo other: there" {
		t.Errorf("%q", got)
	}
}

func TestPlainTextSendsToCurrentWorldThroughSendHook(t *testing.T) {
	e, h := newTest(t)
	_ = e.Exec("w", "/def -hSEND sh = /echo hooked %*")
	_ = e.Exec("w", "say hi")
	if got := h.sentTexts(); len(got) != 1 || got[0] != "w:say hi" {
		t.Fatalf("sent %v", got)
	}
	if !strings.Contains(h.dump(), "hooked say hi") {
		t.Fatalf("SEND hook did not run: %q", h.dump())
	}
	_ = e.Exec("w", "//lit")
	if got := h.sentTexts(); got[1] != "w:/lit" {
		t.Fatalf("%v", got)
	}
}

func TestSendErrorIsShownTheTFWay(t *testing.T) {
	e, h := newTest(t)
	h.sendErr = fmt.Errorf("not connected yet")
	_ = e.Exec("w", "hello")
	if !strings.Contains(h.dump(), "% not connected yet") {
		t.Fatalf("%q", h.dump())
	}
}

func TestUnknownCommandMessage(t *testing.T) {
	e, h := newTest(t)
	_ = e.Exec("w", "/nosuch")
	if got := h.dump(); !strings.Contains(got, "% /nosuch: no such command. Type /help for help.") {
		t.Fatalf("%q", got)
	}
}

func TestTriggerGagHiliteAndPartial(t *testing.T) {
	e, _ := newTest(t)
	_ = e.Exec("w", "/def -t'danger*' -g -ahCred dg = /echo run")
	_ = e.Exec("w", "/def -mregexp -t'(gold) coins' -P1BCblue part")
	r := e.OnLine("w", line("danger ahead"), false)
	if !r.Gag || len(r.Spans) == 0 || r.Spans[0].Fg == nil || !r.Spans[0].B || r.Live {
		t.Fatalf("display-only result: %+v", r)
	}
	r = e.OnLine("w", line("5 gold coins"), false)
	if r.Gag || len(r.Spans) != 3 || r.Spans[1].S != "gold" || r.Spans[1].Bg == nil {
		t.Fatalf("partial hilite: %+v", r.Spans)
	}
	// -ag gags too
	_ = e.Exec("w", "/def -t'spam*' -ag sp")
	if !e.OnLine("w", line("spam spam"), false).Gag {
		t.Fatal("-ag must gag")
	}
	// %gag off disables gagging, %hilite off hilites
	_ = e.Exec("w", "/set gag=off")
	if e.OnLine("w", line("spam spam"), false).Gag {
		t.Fatal("gag=off must not gag")
	}
}

func TestReplayRunsNothingLiveRunsBodyAndShots(t *testing.T) {
	e, h := newTest(t)
	_ = e.Exec("w", "/def -t'ding*' -n1 dd = /send reply")
	e.OnLine("w", line("ding"), false)
	if len(h.sentTexts()) != 0 {
		t.Fatal("replay sent")
	}
	r := e.OnLine("w", line("ding"), true)
	if len(h.sentTexts()) != 1 || len(r.Fired) != 1 {
		t.Fatalf("live: %v %+v", h.sentTexts(), r)
	}
	e.OnLine("w", line("ding"), true)
	if len(h.sentTexts()) != 1 {
		t.Fatal("the -n1 shot was not consumed")
	}
}

func TestWorldScopedMacroAndHooks(t *testing.T) {
	e, h := newTest(t)
	_ = e.Exec("a", "/def -t'x' -wa ma = /echo in-a")
	_ = e.Exec("a", "/def -hCONNECT hc = /echo connected %*")
	e.OnLine("b", line("x"), true)
	if strings.Contains(h.dump(), "in-a") {
		t.Fatal("macro for world a fired for world b")
	}
	e.OnLine("a", line("x"), true)
	if !strings.Contains(h.dump(), "echo a: in-a") {
		t.Fatalf("%q", h.dump())
	}
	e.FireHookWorld("b", "CONNECT", "Bee")
	if !strings.Contains(h.dump(), "echo b: connected Bee") {
		t.Fatalf("%q", h.dump())
	}
}

func TestClientCommands(t *testing.T) {
	e, h := newTest(t)
	h.worlds = []WorldInfo{{Name: "alpha", Host: "a.example", Port: "4000", Connected: true}, {Name: "beta", Host: "b.example", Port: "5"}}
	for _, c := range []string{
		"/connect alpha", "/dc alpha", "/fg beta", "/addworld gamma g.example 99", "/unworld gamma", "/saveworld",
		"/listworlds", "/listsockets", "/beep", "/localecho off", "/log -wxyz out.txt", "/log off",
		"/limit foo*", "/unlimit", "/relimit", "/recall -w2 5", "/status_add -A x", "/status_rm x", "/input hello",
	} {
		_ = e.Exec("w", c)
	}
	got := h.dump()
	for _, want := range []string{
		"connect alpha", "dc alpha", "fg beta", "addworld {Name:gamma Host:g.example Port:99", "unworld gamma", "saveworld",
		"echo w: /addworld alpha a.example 4000", "echo w: /addworld beta b.example 5", "echo w:   alpha  a.example 4000", "bell", "localecho w false",
		"log xyz true out.txt", "log w false ", `limit w "foo*"`, `unlimit w ""`, `relimit w ""`, `recall 2 "5"`,
		"status_add x [-A]", "status_rm x []", `input w "hello"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "not supported by this client") {
		t.Errorf("a client command fell through to the engine's stub:\n%s", got)
	}
}

func TestRestrictBlocksWorldAndFileCommands(t *testing.T) {
	e, h := newTest(t, func(o *Options) { o.Restrict = RestrictWorld })
	_ = e.Exec("w", "/connect alpha")
	_ = e.Exec("w", "/log foo.txt")
	_ = e.Exec("w", "/sh true")
	got := h.dump()
	if strings.Contains(got, "connect alpha") || strings.Contains(got, "log w") {
		t.Fatalf("restricted command ran:\n%s", got)
	}
	if strings.Count(got, "restricted") < 3 {
		t.Fatalf("each refusal must be explained:\n%s", got)
	}
}

func TestShellRestrictedByDefaultForServers(t *testing.T) {
	e, h := newTest(t, func(o *Options) { o.Restrict = RestrictShell })
	_ = e.Exec("w", "/sh echo hi")
	if !strings.Contains(h.dump(), "restricted") {
		t.Fatalf("%q", h.dump())
	}
	_ = e.Exec("w", "/restrict none") // can only be raised, never lowered
	_ = e.Exec("w", "/sh echo hi")
	if strings.Count(h.dump(), "restricted") < 2 {
		t.Fatalf("restriction was lowered:\n%s", h.dump())
	}
}

func TestPolicyVetoesOperations(t *testing.T) {
	e, h := newTest(t, func(o *Options) {
		o.Policy = func(op, detail string) error {
			if op == "new world" {
				return fmt.Errorf("worlds are managed by the admin")
			}
			return nil
		}
	})
	_ = e.Exec("w", "/addworld x x.example 1")
	if !strings.Contains(h.dump(), "worlds are managed by the admin") || strings.Contains(h.dump(), "addworld {") {
		t.Fatalf("%q", h.dump())
	}
}

func TestRunawayScriptIsStoppedByTheTimeBudgetAndByInterrupt(t *testing.T) {
	e, h := newTest(t, func(o *Options) { o.MaxTime = 200 * time.Millisecond })
	done := make(chan struct{})
	go func() { _ = e.Exec("w", "/while (1) /test 1 %; /done"); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runaway loop was not stopped")
	}
	if !strings.Contains(h.dump(), "budget") {
		t.Fatalf("%q", h.dump())
	}
	e2, _ := newTest(t, func(o *Options) { o.MaxTime = -1 })
	done = make(chan struct{})
	go func() { _ = e2.Exec("w", "/while (1) /test 1 %; /done"); close(done) }()
	time.Sleep(100 * time.Millisecond)
	e2.Interrupt()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Interrupt did not stop the script")
	}
}

func TestLibraryMacroCommands(t *testing.T) {
	e, h := newTest(t)
	_ = e.Exec("w", "/def -t'a*' ta = /echo x")
	_ = e.Exec("w", "/def -t'b*' -ag tb")
	_ = e.Exec("w", "/def -hCONNECT hk = /echo y")
	_ = e.Exec("w", "/untrig a*")
	_ = e.Exec("w", "/nogag b*")
	_ = e.Exec("w", "/unhook CONNECT")
	for _, m := range e.Macros() {
		if m.Name == "ta" || m.Name == "tb" || m.Name == "hk" {
			t.Errorf("%s survived", m.Name)
		}
	}
	_ = e.Exec("w", "/partial gold")
	found := false
	for _, m := range e.Macros() {
		if m.Trigger == "gold" {
			found = true
		}
	}
	if !found {
		t.Fatalf("/partial defined nothing: %q", h.dump())
	}
	_ = e.Exec("w", "/undeft gold")
	for _, m := range e.Macros() {
		if m.Trigger == "gold" {
			t.Fatal("/undeft left it")
		}
	}
}

func TestFunctionsAnswerFromTheClient(t *testing.T) {
	e, h := newTest(t)
	h.fg = "front"
	h.worlds = []WorldInfo{{Name: "alpha", Host: "a.example", Port: "4000", Connected: true}}
	h.NopHost = NopHost{}
	e.ExecFull("w", "/echo %{fg}")
	e.ExecFull("w", "/echo $[fg_world()] $[is_connected(\"up\")] $[columns()] $[lines()]")
	_ = e.Exec("w", "/echo -w w-info $[world_info(\"alpha\",\"host\")]")
	got := h.dump()
	if !strings.Contains(got, "front 1 100 30") {
		t.Fatalf("%q", got)
	}
}

var _ = ansi.PlainText

func TestSaveWritesMacroDefinitions(t *testing.T) {
	e, _ := newTest(t)
	_ = e.Exec("w", "/def -t'hello*' -ahCred greet = /echo hi")
	path := t.TempDir() + "/macros.tf"
	_ = e.Exec("w", "/save "+path+" greet")
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), "/def") || !strings.Contains(string(b), "greet") || !strings.Contains(string(b), "hello*") {
		t.Fatalf("%v %q", err, b)
	}
}
