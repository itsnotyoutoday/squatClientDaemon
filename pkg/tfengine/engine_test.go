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
	socks   []SocketInfo
	fg      string
	hidden  func(line, attrs string) bool
	status  []StatusField
	vars    map[string]string
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
func (h *fakeHost) DisplayLine(w, t string) { h.rec("display %s: %s", w, t) }
func (h *fakeHost) Connect(w WorldInfo, o ConnectOptions) error {
	h.rec("connect %s %s:%s %+v", w.Name, w.Host, w.Port, o)
	return nil
}
func (h *fakeHost) Disconnect(w string) error { h.rec("dc %s", w); return nil }
func (h *fakeHost) Foreground(w string, q bool) error {
	h.rec("fg %s %v", w, q)
	h.fg = w
	return nil
}
func (h *fakeHost) ForegroundWorld() string { return h.fg }
func (h *fakeHost) Sockets() []SocketInfo   { return h.socks }
func (h *fakeHost) Worlds() []WorldInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]WorldInfo(nil), h.worlds...)
}
func (h *fakeHost) PutWorld(w WorldInfo) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.worlds {
		if strings.EqualFold(h.worlds[i].Name, w.Name) {
			h.worlds[i] = w
			h.log = append(h.log, "putworld "+w.Name)
			return nil
		}
	}
	h.worlds = append(h.worlds, w)
	h.log = append(h.log, "putworld "+w.Name)
	return nil
}
func (h *fakeHost) DeleteWorld(n string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.worlds {
		if strings.EqualFold(h.worlds[i].Name, n) {
			h.worlds = append(h.worlds[:i], h.worlds[i+1:]...)
			break
		}
	}
	h.log = append(h.log, "deleteworld "+n)
	return nil
}
func (h *fakeHost) RenameWorld(o, n string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.worlds {
		if strings.EqualFold(h.worlds[i].Name, o) {
			h.worlds[i].Name = n
		}
	}
	h.log = append(h.log, "renameworld "+o+" "+n)
	return nil
}
func (h *fakeHost) Bell()                       { h.rec("bell") }
func (h *fakeHost) LocalEcho(w string, on bool) { h.rec("localecho %s %v", w, on) }
func (h *fakeHost) Input(t string)              { h.rec("input %q", t) }
func (h *fakeHost) Quit()                       { h.rec("quit") }
func (h *fakeHost) Screen() (int, int)          { return 100, 30 }
func (h *fakeHost) SetPrompt(w, t string)       { h.rec("prompt %s %q", w, t) }
func (h *fakeHost) SetStatus(f []StatusField)   { h.mu.Lock(); h.status = f; h.mu.Unlock() }
func (h *fakeHost) SetLimit(w string, f func(string, string) bool) int {
	h.mu.Lock()
	h.hidden = f
	h.mu.Unlock()
	return 1
}
func (h *fakeHost) Var(n, v string) {
	h.mu.Lock()
	if h.vars == nil {
		h.vars = map[string]string{}
	}
	h.vars[n] = v
	h.mu.Unlock()
}
func (h *fakeHost) Dialog(n string) { h.rec("dialog %s", n) }

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
	for _, w := range []string{"w", "a", "b"} {
		e.SessionOpened(w, "game.example", 4000, false)
	}
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

func TestConnectionCommandsGoThroughTheEnginesWorldDatabase(t *testing.T) {
	e, h := newTest(t)
	h.worlds = []WorldInfo{{Name: "alpha", Host: "a.example", Port: "4000"}, {Name: "beta", Host: "b.example", Port: "5"}}
	h.socks = []SocketInfo{{World: "alpha", Host: "a.example", Port: "4000", State: SockConnected, Unseen: 2}, {World: "beta", Host: "b.example", Port: "5", State: SockConnected}}
	for _, c := range []string{"/connect alpha", "/connect -x gamma.example 99", "/dc alpha", "/fg beta", "/listworlds", "/listsockets"} {
		_ = e.Exec("w", c)
	}
	got := h.dump()
	for _, want := range []string{"connect alpha a.example:4000", "connect (unnamed1) gamma.example:99", "TLS:true",
		"dc alpha", "fg beta", "alpha", "beta"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "not supported by this client") {
		t.Errorf("a connection command fell through to the engine's stub:\n%s", got)
	}
}

func TestWorldBookIsSyncedBothWays(t *testing.T) {
	e, h := newTest(t)
	h.worlds = []WorldInfo{{Name: "alpha", Host: "a.example", Port: "4000"}}
	_ = e.Exec("w", "/listworlds -s")
	if !strings.Contains(h.dump(), "alpha") {
		t.Fatalf("the client's world is not in the engine's database: %q", h.dump())
	}
	// a script defines a world: the host (the client's store) gets it
	_ = e.Exec("w", "/addworld -x gamma g.example 99")
	if ws := h.Worlds(); len(ws) != 2 || ws[1].Name != "gamma" || !ws[1].TLS || ws[1].Port != "99" {
		t.Fatalf("script's /addworld did not reach the host: %+v", ws)
	}
	_ = e.Exec("w", "/addworld hero hunter secret h.example 7")
	if ws := h.Worlds(); ws[2].Character != "hunter" || ws[2].Password != "secret" {
		t.Fatalf("%+v", ws)
	}
	_ = e.Exec("w", "/renameworld gamma gamma2")
	_ = e.Exec("w", "/unworld alpha")
	names := ""
	for _, w := range h.Worlds() {
		names += w.Name + ","
	}
	if names != "gamma2,hero," {
		t.Fatalf("after rename and unworld the host has %q\n%s", names, h.dump())
	}
	// the store changes behind the engine's back (the Worlds dialog): the next call sees it
	h.mu.Lock()
	h.worlds = append(h.worlds, WorldInfo{Name: "delta", Host: "d.example", Port: "1"})
	h.mu.Unlock()
	_ = e.Exec("w", "/listworlds -s")
	if !strings.Contains(h.dump(), "delta") {
		t.Fatalf("a world added in the UI is not visible to scripts:\n%s", h.dump())
	}
	// no churn: reading again does not write
	n := strings.Count(h.dump(), "putworld")
	_ = e.Exec("w", "/listworlds -s")
	if strings.Count(h.dump(), "putworld") != n {
		t.Fatal("sync wrote back what it had just read")
	}
}

func TestHistoryRecallLimitLogStatusAndFriends(t *testing.T) {
	logs := t.TempDir()
	e, h := newTest(t, func(o *Options) { o.LogsDir = logs })
	_ = e.Type("w", "first typed", false)
	e.OnLine("w", line("from the game"), true)
	e.OnLine("w", line("gagged text"), true)
	_ = e.Exec("w", "/recall -w /3")
	_ = e.Exec("w", "/recall -i /3")
	got := h.dump()
	if !strings.Contains(got, "from the game") {
		t.Errorf("/recall does not see received lines:\n%s", got)
	}
	if !strings.Contains(got, "first typed") {
		t.Errorf("/recall -i does not see typed lines:\n%s", got)
	}
	// /limit: the host gets the engine's filter and the view's line count
	_ = e.Exec("w", "/limit -mglob *game*")
	h.mu.Lock()
	f := h.hidden
	h.mu.Unlock()
	if f == nil || f("from the game", "") || !f("other", "") {
		t.Fatalf("limit filter not handed to the host")
	}
	_ = e.Exec("w", "/unlimit")
	// /log writes recorded lines under the logs directory
	_ = e.Exec("w", "/log mylog.txt")
	e.OnLine("w", line("logged line"), true)
	_ = e.Exec("w", "/log off")
	b, err := os.ReadFile(logs + "/mylog.txt")
	if err != nil || !strings.Contains(string(b), "logged line") {
		t.Fatalf("log: %v %q", err, b)
	}
	// status line fields reach the host
	_ = e.Exec("w", "/status_add -A @world:10")
	h.mu.Lock()
	st := h.status
	h.mu.Unlock()
	if len(st) == 0 || st[len(st)-1].Name != "world" {
		t.Fatalf("status: %+v", st)
	}
	// localecho, beep, input
	for _, c := range []string{"/localecho on", "/beep", "/input hello"} {
		_ = e.Exec("w", c)
	}
	for _, want := range []string{"localecho w true", "bell", `input "hello"`} {
		if !strings.Contains(h.dump(), want) {
			t.Errorf("missing %q in\n%s", want, h.dump())
		}
	}
}

func TestWrapVariablesReachTheGUI(t *testing.T) {
	e, h := newTest(t)
	_ = e.Exec("w", "/nowrap")
	_ = e.Exec("w", "/wrap 72")
	_ = e.Exec("w", "/wrapspace 2")
	_ = e.Exec("w", "/set wrappunct=5")
	_ = e.Exec("w", "/visual on")
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.vars["wrap"] != "on" || h.vars["wrapsize"] != "72" || h.vars["wrapspace"] != "2" || h.vars["wrappunct"] != "5" || h.vars["visual"] != "on" {
		t.Fatalf("%v", h.vars)
	}
}

func TestKeyBindingsRunWithTheInputLine(t *testing.T) {
	e, h := newTest(t)
	_ = e.Exec("w", `/def -ib'^A' = /test kbgoto(0)`)
	_ = e.Exec("w", `/def -ib'^K' = /dokey deol`)
	_ = e.Exec("w", `/def -ib'^T' = /send -w w typed=%{kbhead}`)
	if b := e.KeyBindings(); len(b) < 2 {
		t.Fatalf("bindings %v", b)
	}
	ok, text, cur := e.Key("w", "^K", "hello world", 5)
	if !ok || text != "hello" || cur != 5 {
		t.Fatalf("^K: %v %q %d", ok, text, cur)
	}
	ok, text, cur = e.Key("w", "^A", "hello", 5)
	if !ok || text != "hello" || cur != 0 {
		t.Fatalf("^A: %v %q %d", ok, text, cur)
	}
	if ok, _, _ := e.Key("w", "^Q", "x", 1); ok {
		t.Fatal("an unbound key must not be handled")
	}
	_ = h
}

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

func TestRestrictBlocksWorldAndFileCommands(t *testing.T) {
	e, h := newTest(t, func(o *Options) { o.Restrict = RestrictWorld })
	_ = e.Exec("w", "/connect new.example 99")
	_ = e.Exec("w", "/log foo.txt")
	_ = e.Exec("w", "/sh true")
	got := h.dump()
	if strings.Contains(got, "connect (unnamed") || strings.Contains(got, "log w") {
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
	h.worlds = []WorldInfo{{Name: "alpha", Host: "a.example", Port: "4000"}}
	h.socks = []SocketInfo{{World: "alpha", State: SockConnected}}
	e.ExecFull("w", "/echo $[fg_world()] $[is_connected(\"alpha\")] $[columns()] $[lines()] $[world_info(\"alpha\",\"host\")] $[nactive()]")
	got := h.dump()
	if !strings.Contains(got, "front 1 100 30 a.example 0") {
		t.Fatalf("%q", got)
	}
}

var _ = ansi.PlainText
