package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

func main() {
	var (
		tfBin       = flag.String("tf-bin", os.Getenv("TF_BIN"), "path to the TinyFugue binary built from the squatterbridge branch")
		tfLib       = flag.String("tf-lib", os.Getenv("TF_LIB"), "path to TinyFugue's lib/tf directory")
		bridgeBin   = flag.String("bridge-bin", "", "tf-squatterd binary (default: build ./cmd/tf-squatterd)")
		cobraGame   = flag.String("cobra", defaultPath("CobraMUSH-Fspace/game"), "CobraMUSH game directory to copy and run ('' to skip)")
		evenniaDir  = flag.String("evennia", defaultPath("evennia"), "directory holding venv/ and testgame/ ('' to skip)")
		evenniaPort = flag.Int("evennia-port", 4391, "telnet port of the Evennia test game")
		keep        = flag.Bool("keep", false, "keep the work directory")
		interactive = flag.Bool("interactive", false, "after the checks, leave everything running and open a remote-client console")
		verbose     = flag.Bool("v", false, "print TinyFugue's screen at the end")
		serve       = flag.String("serve", "", "start the stack (TLS, like a real install), write how to reach it as JSON to this file, and run until interrupted; no checks")
	)
	flag.Parse()
	if *tfBin == "" || *tfLib == "" {
		fmt.Fprintln(os.Stderr, "usage: tfharness --tf-bin /path/to/tf --tf-lib /path/to/lib/tf   (or set TF_BIN and TF_LIB)")
		os.Exit(2)
	}
	serveFile = *serve
	os.Exit(run(*tfBin, *tfLib, *bridgeBin, *cobraGame, *evenniaDir, *evenniaPort, *keep, *interactive, *verbose))
}

// serveFile is set by --serve: TLS bridge, no checks, details written for an outside client test.
var serveFile string

func defaultPath(rel string) string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(h, "mush", rel)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func run(tfBin, tfLib, bridgeBin, cobraGame, evenniaDir string, evenniaPort int, keep, interactive, verbose bool) int {
	logf := func(f string, a ...any) { fmt.Printf(f+"\n", a...) }
	work, err := os.MkdirTemp("", "tfharness-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !keep && !interactive {
		defer os.RemoveAll(work)
	} else {
		logf("work directory: %s", work)
	}

	// --- build the bridge
	if bridgeBin == "" {
		root, err := moduleRoot()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		bridgeBin = filepath.Join(work, "tf-squatterd")
		logf("building tf-squatterd ...")
		cmd := exec.Command("go", "build", "-o", bridgeBin, "./cmd/tf-squatterd")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "go build failed: %v\n%s", err, out)
			return 1
		}
	}

	e := &env{rep: &report{}, work: work, logf: logf}

	// --- games
	if cobraGame != "" {
		logf("starting a private CobraMUSH ...")
		if gs, err := startCobra(work, cobraGame); err != nil {
			logf("  (CobraMUSH unavailable: %v)", err)
		} else {
			e.cobra = gs
			defer gs.stop()
			logf("  CobraMUSH on port %d", gs.port)
		}
	}
	if evenniaDir != "" {
		logf("checking Evennia ...")
		if gs, err := startEvennia(evenniaDir, evenniaPort); err != nil {
			logf("  (Evennia unavailable: %v)", err)
		} else {
			e.evennia = gs
			defer gs.stop()
			logf("  Evennia on port %d", gs.port)
		}
	}

	// --- TinyFugue with the bridge
	home := filepath.Join(work, "home")
	_ = os.MkdirAll(home, 0o755)
	tf, err := startTF(tfBin, tfLib, home)
	if err != nil {
		fmt.Fprintln(os.Stderr, "starting TinyFugue:", err)
		return 1
	}
	defer tf.stop()
	e.tf = tf
	e.bridgeP = freePort()
	e.addr = fmt.Sprintf("127.0.0.1:%d/tfsp", e.bridgeP)
	tf.typ("/set squat_bin=" + bridgeBin)
	tf.typ(fmt.Sprintf("/set squat_port=%d", e.bridgeP))
	if serveFile == "" {
		tf.typ("/set squat_plain=1")
	}
	tf.typ("/set squat_dir=" + filepath.Join(work, "sq"))
	tf.typ("/set squat_exec=on")
	if os.Getenv("HARNESS_DEBUG") != "" {
		tf.typ("/set squat_log=debug")
	}
	tf.typ("/set squat_disp=on")
	tf.typ("/squat on")
	if !waitPort(e.bridgeP, 15*time.Second) {
		fmt.Fprintf(os.Stderr, "the bridge never listened.\nTinyFugue said:\n%s\n", tf.out.String())
		return 1
	}
	code, err := tf.pairCode("admin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if serveFile != "" {
		return serveStack(e, code)
	}
	if e.admin, err = dial(e.addr, code, "harness-admin", newKey()); err != nil {
		fmt.Fprintln(os.Stderr, "pairing the admin client:", err)
		return 1
	}
	code, _ = tf.pairCode("operator")
	if e.oper, err = dial(e.addr, code, "harness-operator", newKey()); err != nil {
		fmt.Fprintln(os.Stderr, "pairing the operator client:", err)
		return 1
	}
	logf("bridge on 127.0.0.1:%d, admin + operator clients paired\n", e.bridgeP)

	// --- the checks
	only := strings.Split(os.Getenv("HARNESS_ONLY"), ",")
	want := func(name string) bool {
		if os.Getenv("HARNESS_ONLY") == "" {
			return true
		}
		for _, o := range only {
			if o == name {
				return true
			}
		}
		return false
	}
	steps := []struct {
		name string
		fn   func()
	}{
		{"handshake", e.handshake}, {"worlds", e.worlds}, {"cobra", e.cobraScenario}, {"rename", e.renameScenario}, {"evennia", e.evenniaScenario}, {"msdp", e.msdpScenario}, {"state", e.stateScenario}, {"mtts", e.mttsScenario},
		{"ask", e.askScenario}, {"exec", e.execScenario}, {"scripts", e.scriptsScenario}, {"restart", e.restartScenario},
	}
	for _, s := range steps {
		if !want(s.name) {
			continue
		}
		logf("== %s", s.name)
		s.fn()
	}

	failed := summarize(e.rep, logf)
	if verbose {
		fmt.Println("\n----- TinyFugue's screen -----")
		fmt.Println(tf.out.String())
	}
	if interactive {
		console(e)
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// serveStack publishes how to reach the running stack and waits for SIGINT/SIGTERM.
// The client test (squatClient internal/app, TF_LIVE) reads the file.
func serveStack(e *env, adminCode string) int {
	info := map[string]any{"host": "127.0.0.1", "port": e.bridgeP, "adminCode": adminCode}
	if e.cobra != nil {
		info["cobraPort"] = e.cobra.port
	}
	b, _ := json.Marshal(info)
	tmp := serveFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = os.Rename(tmp, serveFile) // appears atomically: the reader waits for the file
	fmt.Printf("serving: %s\n", b)
	// further pairing codes on request: touch <file>.code-<role> and read <file>.code-<role>.out
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	for {
		select {
		case <-sig:
			return 0
		case <-time.After(200 * time.Millisecond):
			for _, role := range []string{"viewer", "operator", "admin"} {
				req := serveFile + ".code-" + role
				if _, err := os.Stat(req); err == nil {
					_ = os.Remove(req)
					if c, err := e.tf.pairCode(role); err == nil {
						_ = os.WriteFile(req+".out", []byte(c), 0o600)
					}
				}
			}
		}
	}
}

func summarize(r *report, logf func(string, ...any)) int {
	pass, fail, skip := 0, 0, 0
	for _, x := range r.all {
		switch {
		case x.skipped:
			skip++
		case x.ok:
			pass++
		default:
			fail++
		}
	}
	logf("\n==== %d passed, %d failed, %d skipped ====", pass, fail, skip)
	for _, x := range r.all {
		if !x.ok && !x.skipped {
			logf("FAILED  [%s] %s\n        %s", x.group, x.name, x.detail)
		}
	}
	return fail
}

// console is a tiny remote client for poking at the running stack.
func console(e *env) {
	fmt.Println(`
Interactive console (you are the admin remote client). Commands:
  sessions                       list sessions
  attach <n>                     attach to session n and print what arrives
  say <n> <text>                 send a line to session n      secret <n> <text>   send without echo
  control <n> <input|automation> <tf|remote|both|client>     preset <n> <viewer|copilot|takeover|full>
  worlds | put <name> <host> <port> | del <name> | connect <world>   sleep <ms>
  exec </command>                run a TinyFugue command        gmcp <n> <package> [json]
  tf <text>                      type at TinyFugue's keyboard   screen   show TinyFugue's recent screen
  quit`)
	a := e.admin.c
	list := func() []proto.SessionInfo {
		e.admin.mu.Lock()
		defer e.admin.mu.Unlock()
		out := make([]proto.SessionInfo, 0, len(e.admin.ses))
		for _, s := range e.admin.ses {
			out = append(out, s)
		}
		sort.Slice(out, func(i, j int) bool {
			return out[i].StartedAt < out[j].StartedAt || (out[i].StartedAt == out[j].StartedAt && out[i].ID < out[j].ID)
		})
		return out
	}
	ses := func(n string) string {
		var i int
		fmt.Sscan(n, &i)
		ss := list()
		if i < 0 || i >= len(ss) {
			return ""
		}
		return ss[i].ID
	}
	go func() {
		for {
			e.admin.mu.Lock()
			for e.admin.pos >= len(e.admin.evs) {
				e.admin.cond.Wait()
			}
			evs := e.admin.evs[e.admin.pos:]
			e.admin.pos = len(e.admin.evs)
			e.admin.mu.Unlock()
			for _, ev := range evs {
				for _, en := range entriesOf(ev) {
					if en.Line != nil {
						fmt.Printf("  [%s] %s\n", en.Line.Kind, lineText(*en.Line))
					} else if en.Gmcp != nil {
						fmt.Printf("  [gmcp] %s %s\n", en.Gmcp.Package, en.Gmcp.Data)
					}
				}
			}
		}
	}()
	sc := bufio.NewScanner(os.Stdin)
	for fmt.Print("> "); sc.Scan(); fmt.Print("> ") {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		rest := func(n int) string { return strings.Join(f[n:], " ") }
		switch f[0] {
		case "bg": // bg <command...>: run a console command in the background (it may block)
			if len(f) > 2 && f[1] == "control" && len(f) > 4 {
				go func(id, axis, mode string) {
					_, err := a.ControlSet(tctx(60*time.Second), id, axis, mode)
					fmt.Printf("  [bg control %s %s done: err=%v]\n", axis, mode, err)
				}(ses(f[2]), f[3], f[4])
			}
		case "quit", "exit":
			return
		case "sessions":
			for i, s := range list() {
				in := ""
				if s.TF != nil {
					in = fmt.Sprintf("input=%s automation=%s", s.TF.Input, s.TF.Automation)
				}
				fmt.Printf("  %d  %-28s %-10s %s\n", i, s.ID, s.State, in)
			}
		case "attach":
			if len(f) > 1 {
				fmt.Println(a.Attach(tctx(5*time.Second), ses(f[1]), nil, 0))
			}
		case "say":
			if len(f) > 2 {
				fmt.Println(a.Send(tctx(5*time.Second), ses(f[1]), rest(2)))
			}
		case "secret":
			if len(f) > 2 {
				fmt.Println(a.SendSecret(tctx(5*time.Second), ses(f[1]), rest(2)))
			}
		case "control":
			if len(f) > 3 {
				fmt.Println(a.ControlSet(tctx(50*time.Second), ses(f[1]), f[2], f[3]))
			}
		case "preset":
			if len(f) > 2 {
				fmt.Println(a.ControlPreset(tctx(50*time.Second), ses(f[1]), f[2]))
			}
		case "connect":
			if len(f) > 1 {
				fmt.Println(a.ConnectRef(tctx(10*time.Second), f[1]))
			}
		case "sleep":
			if len(f) > 1 {
				var ms int
				fmt.Sscan(f[1], &ms)
				time.Sleep(time.Duration(ms) * time.Millisecond)
			}
		case "worlds":
			ws, err := a.Worlds(tctx(5 * time.Second))
			fmt.Println(err)
			for _, w := range ws {
				fmt.Printf("  %-16s %s:%s type=%s char=%s pass=%v\n", w.Name, w.Host, w.Port, w.Type, w.Char, w.PassSet)
			}
		case "put":
			if len(f) > 3 {
				fmt.Println(a.PutWorld(tctx(5*time.Second), putReq(f[1], f[2], f[3])))
			}
		case "del":
			if len(f) > 1 {
				fmt.Println(a.DeleteWorld(tctx(5*time.Second), f[1]))
			}
		case "exec":
			res, err := a.Exec(tctx(10*time.Second), rest(1))
			fmt.Println(err, res.OK)
			for _, l := range res.Out {
				fmt.Println("  |", l)
			}
		case "gmcp":
			if len(f) > 2 {
				var data any
				if len(f) > 3 {
					data = jsonOrString(rest(3))
				}
				fmt.Println(a.SendGmcp(tctx(5*time.Second), ses(f[1]), f[2], data))
			}
		case "tf":
			e.tf.typ(rest(1))
		case "screen":
			s := e.tf.out.String()
			if len(s) > 3000 {
				s = s[len(s)-3000:]
			}
			fmt.Println(s)
		default:
			fmt.Println("?")
		}
	}
}
