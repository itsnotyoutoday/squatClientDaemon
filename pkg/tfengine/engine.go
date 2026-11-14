package tfengine

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itsnotyoutoday/gtfparser"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// Restrict is the /restrict level a deployment starts at. The numbers are the
// engine's own: a script can raise the level (/restrict) but never lower it.
type Restrict int

const (
	// RestrictNone: scripts may run shell commands, read and write files and
	// open connections. The default for the desktop app (the user's own
	// machine, the user's own scripts).
	RestrictNone Restrict = Restrict(gtfparser.RestrictNone)
	// RestrictShell forbids the shell (/sh, /quote !, popen). The default for
	// squatterd, which runs other people's scripts on a server.
	RestrictShell Restrict = Restrict(gtfparser.RestrictShell)
	// RestrictFile additionally forbids file access.
	RestrictFile Restrict = Restrict(gtfparser.RestrictFile)
	// RestrictWorld additionally forbids connection and world-book changes.
	RestrictWorld Restrict = Restrict(gtfparser.RestrictWorld)
)

// ParseRestrict reads "none", "shell", "file" or "world".
func ParseRestrict(s string) (Restrict, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none", "off", "":
		return RestrictNone, true
	case "shell":
		return RestrictShell, true
	case "file":
		return RestrictFile, true
	case "world":
		return RestrictWorld, true
	}
	return 0, false
}

func (r Restrict) String() string {
	return [...]string{"none", "shell", "file", "world"}[r]
}

// Limits are hard ceilings on what a script run may consume (zero = the
// engine's default, which is real tf's own).
type Limits struct {
	MaxRecur    int   // nested macro calls
	MaxInstr    int64 // statements per top-level run
	MaxLoadDeep int   // nested /load
	MaxOutput   int   // echoed lines per top-level run (0 = unlimited)
}

// Options configures an Engine. The zero value is usable.
type Options struct {
	// Restrict is the initial /restrict level (default RestrictNone).
	Restrict Restrict
	Limits   Limits
	// Policy, if set, can veto a privileged operation on top of /restrict:
	// op is "shell", "file read", "file write", "directory change" or "new
	// world", detail names the target. Return nil to allow.
	Policy func(op, detail string) error
	// LibDirs are searched by /load and /require for relative names (after
	// the current directory). The engine passes its ConfigDir here.
	LibDirs []string
	// MaxTime is the wall-clock budget of one typed line, received line, hook
	// or key (default 2s, negative disables): a script that runs longer is
	// interrupted. Loading a file is exempt. (The engine's own ceilings,
	// Limits, count statements, not time.)
	MaxTime time.Duration
	// StdLib is real tf's standard library: a directory containing stdlib.tf
	// (or the file itself). When set it is loaded at start, as tf does, which
	// defines tf's library commands (/wrap /nowrap /visual /alias ...). The
	// library is not bundled; without it the commands the client supplies
	// itself still work.
	StdLib string
	// LogsDir is where /log puts relative file names (default: the current directory).
	LogsDir string
	// Version is reported by /version.
	Version string
	// WithoutEnvironment keeps the process environment out of script
	// variables (squatterd uses it: a server's environment is not the
	// user's business).
	WithoutEnvironment bool
}

// LineResult is the outcome of running the trigger stage over one line.
type LineResult struct {
	// Gag means the line must not be displayed.
	Gag bool
	// Spans is the line's content with hilites applied (nil = unchanged).
	Spans []proto.Span
	// Fired lists the names of macros that matched (trigger order).
	Fired []string
	// Hidden means the world's /limit filter hides the line (it is still recorded).
	Hidden bool
	// Live echoes the live flag the result was computed with. When false only
	// display effects (Gag, Spans) were computed; nothing was executed.
	Live bool
}

// Engine runs the TinyFugue scripting language for every session of a client
// engine. Its methods are safe for use from any goroutine, except from inside
// a Host callback (which already runs on the interpreter's goroutine): those
// must use Post.
type Engine struct {
	host Host
	opts Options
	in   *gtfparser.Interp
	r    *gtfparser.Runner

	mu     sync.RWMutex // guards closed; held shared while a call is in flight
	closed bool

	// Everything below is only touched on the interpreter's goroutine.
	cur         string          // the world the running call acts for
	inSend      bool            // a SEND hook is running
	worldSig    string          // signature of the host's world list as last synced into the engine's database
	synced      map[string]bool // lower-cased names of the worlds that came from the host
	renames     [][2]string     // /renameworld events of the running call
	statusDirty bool
	muted       bool // world sync is running: its chatter is not shown
	inKey       bool
}

// New creates the engine and starts its interpreter goroutine. Call Close to
// stop it.
func New(host Host, opts Options) *Engine {
	if host == nil {
		host = NopHost{}
	}
	if opts.MaxTime == 0 {
		opts.MaxTime = 2 * time.Second
	}
	a := &Engine{host: host, opts: opts, synced: map[string]bool{}}
	gl := gtfparser.Limits{MaxRecur: opts.Limits.MaxRecur, MaxInstr: opts.Limits.MaxInstr,
		MaxLoadDeep: opts.Limits.MaxLoadDeep, MaxOutput: opts.Limits.MaxOutput}
	d := gtfparser.DefaultLimits()
	if gl.MaxRecur == 0 {
		gl.MaxRecur = d.MaxRecur
	}
	if gl.MaxInstr == 0 {
		gl.MaxInstr = d.MaxInstr
	}
	if gl.MaxLoadDeep == 0 {
		gl.MaxLoadDeep = d.MaxLoadDeep
	}
	gopts := []gtfparser.Option{
		gtfparser.WithLimits(gl),
		gtfparser.WithRestriction(int(opts.Restrict)),
		gtfparser.WithPolicy(policy{opts.Policy}),
		gtfparser.WithLibDirs(opts.LibDirs...),
	}
	if opts.WithoutEnvironment {
		gopts = append(gopts, gtfparser.WithoutEnvironment())
	}
	ch := &coreHost{a: a}
	a.in = gtfparser.New(ch, gopts...)
	a.in.Now = host.Now
	a.in.Warn = a.warn
	if opts.Version != "" {
		a.in.SetVar("version", opts.Version)
	}
	if err := a.in.Load(clientExtension(a)); err != nil {
		panic("tfengine: " + err.Error()) // a malformed table is a programming error
	}
	for _, f := range []string{"256colors", "ssl", "IPv6", "core"} {
		a.in.SetFeature(f, true)
	}
	a.r = gtfparser.NewRunner(a.in)
	a.r.Do(func(in *gtfparser.Interp) { a.start(in) })
	return a
}

// Close stops the interpreter goroutine after queued work has run.
func (a *Engine) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		a.closed = true
		a.r.Close()
	}
}

// Interrupt asks the running script (if any) to stop, like TF's Ctrl-C. Safe
// from any goroutine; it does not wait.
func (a *Engine) Interrupt() { a.r.Interrupt() }

// Post queues fn on the interpreter goroutine and returns at once.
func (a *Engine) Post(fn func()) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.closed {
		a.r.Post(func(*gtfparser.Interp) { fn() })
	}
}

// do runs fn as the given world on the interpreter goroutine and waits, with
// the wall-clock budget.
func (a *Engine) do(world string, fn func(in *gtfparser.Interp)) { a.run(world, true, fn) }

func (a *Engine) run(world string, budget bool, fn func(in *gtfparser.Interp)) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return
	}
	a.r.Do(func(in *gtfparser.Interp) {
		prev := a.cur
		a.cur = world
		in.SetWorld(world)
		defer func() { a.cur = prev; in.SetWorld(prev) }()
		if budget && a.opts.MaxTime > 0 {
			t := time.AfterFunc(a.opts.MaxTime, in.Interrupt)
			defer func() {
				if !t.Stop() {
					a.echoText("% script budget exceeded: wall time limit " + a.opts.MaxTime.String())
				}
			}()
		}
		a.syncWorlds(in)
		fn(in)
		a.afterCall(in)
	})
}

// Exec processes one line of user input for a world: a line starting with "/"
// is a command, anything else is text sent to the world (through the SEND
// hook). Errors are shown to the user in the TF style ("% message") by the
// interpreter itself.
func (a *Engine) Exec(world, text string) error {
	a.do(world, func(in *gtfparser.Interp) { in.Exec(text) })
	return nil
}

// Type processes a line the user typed: it goes into the input history
// (secret lines marked so they are not kept) and then runs like Exec.
func (a *Engine) Type(world, text string, secret bool) error {
	a.do(world, func(in *gtfparser.Interp) {
		if secret {
			in.RecordInputAttr(text, "g")
		} else {
			in.RecordInput(text)
		}
		in.Exec(text)
	})
	return nil
}

// ExecFull is Exec with full %-substitution (like /eval).
func (a *Engine) ExecFull(world, text string) {
	a.do(world, func(in *gtfparser.Interp) { in.ExecFull(text) })
}

// LoadFile runs a TF script file (the startup file, /load).
func (a *Engine) LoadFile(path string) error {
	var err error
	a.run("", false, func(in *gtfparser.Interp) { err = in.LoadFile(path) })
	return err
}

// LoadString runs script source as a file would be.
func (a *Engine) LoadString(name, src string) error {
	f, err := os.CreateTemp("", "tfscript-*.tf")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(src); err != nil {
		f.Close()
		return err
	}
	f.Close()
	return a.LoadFile(f.Name())
}

// FireHookWorld fires the macros hooked on an event (CONNECT, DISCONNECT,
// ...) for a world; args become %1 ... It must not be called from a Host
// callback (use Post).
func (a *Engine) FireHookWorld(world, event string, args ...string) {
	a.do(world, func(in *gtfparser.Interp) { in.Hook(strings.ToUpper(event), strings.Join(args, " ")) })
}

// GetVar returns a TF variable's value as text.
func (a *Engine) GetVar(name string) string {
	var v string
	a.do("", func(in *gtfparser.Interp) { v = in.Var(name) })
	return v
}

// SetVar sets a global TF variable.
func (a *Engine) SetVar(name, val string) {
	a.do("", func(in *gtfparser.Interp) { in.SetVar(name, val) })
}

// MacroInfo is the read-only view of a macro.
type MacroInfo struct {
	Num      int
	Name     string
	Body     string
	Trigger  string
	Hook     string
	Bind     string
	Matching string
	Priority int
	Shots    int
	Attrs    string
	World    string
	Gag      bool
}

// Macros lists the defined macros in definition order.
func (a *Engine) Macros() []MacroInfo {
	var out []MacroInfo
	a.do("", func(in *gtfparser.Interp) {
		for _, m := range in.Macros() {
			out = append(out, MacroInfo{Num: m.ID, Name: m.Name, Body: m.Body, Trigger: m.Trigger, Hook: m.Hook,
				Bind: m.Bind, Matching: m.Matching, Priority: m.Priority, Shots: m.Shots, Attrs: m.Attrs,
				World: m.World, Gag: m.Gag || attrsGag(m.Attrs)})
		}
	})
	return out
}

// KeyBindings lists the key sequences bound by scripts (/bind, /def -b), in
// the notation /bind uses ("^A", "^[x"). The input line asks for them to know
// which keystrokes to forward to Key.
func (a *Engine) KeyBindings() []string {
	var out []string
	a.do("", func(in *gtfparser.Interp) {
		for _, b := range in.Bindings() {
			out = append(out, gtfparser.AsciiToPrint(b.Seq))
		}
	})
	return out
}

// Key feeds one keystroke (a sequence in /bind notation, "^A") of the input
// line to the engine. text and cursor (a byte offset) are the input line as it
// is now. handled is false when nothing is bound to the key (the input line
// then does its own thing); otherwise the bound macro ran and the (possibly
// edited, by /input, /dokey, kb*()) line is returned.
func (a *Engine) Key(world, seq, text string, cursor int) (handled bool, newText string, newCursor int) {
	a.do(world, func(in *gtfparser.Interp) {
		code := gtfparser.PrintToAscii(seq)
		if _, ok := in.FindKey(code); !ok {
			return
		}
		handled = true
		b := in.Input()
		b.Set(text, cursor)
		a.inKey = true
		in.KeyPressed(code)
		in.FlushKeys()
		a.inKey = false
		newText, newCursor = b.Text(), b.Cursor()
		b.Clear()
	})
	return
}

// SessionOpened defines a session's script name as a world of the engine's
// database (when it is not one already), so per-world state such as /recall's
// history works for connections that are not in the world book (quick
// connect, "#2" duplicates). Safe from any goroutine; it does not wait.
func (a *Engine) SessionOpened(world, host string, port int, tls bool) {
	a.Post(func() {
		if _, ok := a.in.World(world); ok {
			return
		}
		a.muted = true
		a.in.DefineWorld(gtfparser.WorldInfo{Name: world, Host: host, Port: strconv.Itoa(port), SSL: tls, Temp: true})
		a.muted = false
	})
}

// WorldClosed tells the engine a session's connection ended (a temporary
// world made by /connect host port is forgotten). Safe from any goroutine.
func (a *Engine) WorldClosed(world string) {
	a.Post(func() { a.in.WorldClosed(world) })
}

// timeNow is the clock the interpreter uses (the host's).
func (a *Engine) timeNow() time.Time { return a.host.Now() }
