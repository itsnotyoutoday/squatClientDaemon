package tfengine

import (
	"os"
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
	cur       string // the world the running call acts for
	inSend    bool   // a SEND hook is running
	nlogs     int
	lastPrmpt map[string]string
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
	a := &Engine{host: host, opts: opts, lastPrmpt: map[string]string{}}
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
	a.registerCommands()
	a.registerFunctions()
	a.r = gtfparser.NewRunner(a.in)
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
		fn(in)
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

// Key runs the macro bound to key (a /bind or /def -b key) for a world and
// reports whether there was one.
func (a *Engine) Key(world, key string) bool {
	found := false
	a.do(world, func(in *gtfparser.Interp) {
		for _, m := range in.Macros() {
			if m.Bind == key {
				found = true
				in.ExecFull(m.Body)
				break
			}
		}
	})
	return found
}

// timeNow is the clock the interpreter uses (the host's).
func (a *Engine) timeNow() time.Time { return a.host.Now() }
