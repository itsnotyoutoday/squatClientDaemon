package lua

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	glua "github.com/yuin/gopher-lua"
)

// callCtx describes the call currently executing in the VM.
type callCtx struct {
	world string // world id the call acts for ("" = none)
	live  bool   // false: display effects only, side-effect APIs are no-ops
	owner string // plugin / script name, for error attribution
}

// unit is something Reload re-runs.
type unit struct {
	kind string // "string", "file", "plugins"
	name string
	src  string
}

// Runtime is one sandboxed Lua state plus its handler registry. Create it with
// New. All methods are safe for concurrent use; execution is serialised (see
// the package documentation).
type Runtime struct {
	host Host
	opts Options

	mu      sync.Mutex
	L       *glua.LState
	closed  bool
	gen     uint64
	cur     callCtx
	running bool
	started time.Time

	base map[string]glua.LValue
	libs map[string]*glua.LTable

	rules   []*rule
	nextID  int
	seq     int
	timers  map[int]*timerRec
	vars    map[string]any
	regexes map[string]*regexp.Regexp

	console *scriptEnv
	units   []unit
	plugins []PluginInfo
}

// New creates a Runtime. host must not be nil.
func New(host Host, opts Options) *Runtime {
	r := &Runtime{host: host, opts: opts.withDefaults(), vars: map[string]any{}}
	r.reset()
	return r
}

// reset (re)creates the Lua state and clears all registries except vars/units.
// Caller holds mu (or is the constructor).
func (r *Runtime) reset() {
	r.started = r.host.Now()
	r.L = r.newState()
	r.rules = nil
	r.timers = map[int]*timerRec{}
	r.regexes = map[string]*regexp.Regexp{}
	r.console = nil
	r.plugins = nil
	r.gen++
}

// teardown cancels timers and closes the state. Caller holds mu.
func (r *Runtime) teardown() {
	for _, t := range r.timers {
		t.removed = true
		if t.cancel != nil {
			t.cancel()
		}
	}
	r.timers = map[int]*timerRec{}
	r.rules = nil
	if r.L != nil {
		r.L.Close()
	}
}

// Close stops all timers and releases the Lua state. Further calls are no-ops
// (OnLine returns the input unchanged).
func (r *Runtime) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	r.gen++
	r.teardown()
}

// Reload discards all handlers, timers and script state, builds a fresh Lua
// state and re-runs every previously loaded script and the plugin set. The
// variable store is kept.
func (r *Runtime) Reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	r.teardown()
	r.reset()
	var errs []error
	for _, u := range r.units {
		var err error
		switch u.kind {
		case "string":
			err = r.runChunk(u.name, "", []byte(u.src))
		case "file":
			err = r.runFile(u.name)
		case "plugins":
			_, err = r.discoverAndLoadPlugins()
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *Runtime) logf(level LogLevel, owner, format string, a ...any) {
	defer func() { _ = recover() }()
	r.host.Log(level, owner, fmt.Sprintf(format, a...))
}

// call runs fn under a fresh budget with panic recovery. Caller holds mu.
// Errors are logged through Host.Log and returned. At most one result is
// returned (nret 0 or 1).
func (r *Runtime) call(owner, world string, live bool, fn *glua.LFunction, nret int, args ...glua.LValue) (ret glua.LValue, err error) {
	if r.closed {
		return glua.LNil, ErrClosed
	}
	b := newBudget(r.opts)
	prev, prevRunning := r.cur, r.running
	r.cur = callCtx{world: world, live: live, owner: owner}
	r.running = true
	L := r.L
	L.SetContext(b)
	top := L.GetTop()
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("lua: panic: %v", rec)
			ret = glua.LNil
		}
		if te := b.tripped(); te != nil {
			err = fmt.Errorf("%s: %w", owner, te)
			ret = glua.LNil
		}
		L.RemoveContext()
		b.stop()
		if L.GetTop() > top {
			L.SetTop(top)
		}
		r.cur, r.running = prev, prevRunning
		if err != nil {
			r.logf(LogError, owner, "%v", err)
		}
	}()
	L.Push(fn)
	for _, a := range args {
		L.Push(a)
	}
	if err = L.PCall(len(args), nret, nil); err != nil {
		return glua.LNil, err
	}
	ret = glua.LNil
	if nret > 0 {
		ret = L.Get(-1)
	}
	return ret, nil
}

// runChunk compiles and runs src in a new environment named name.
func (r *Runtime) runChunk(name, dir string, src []byte) error {
	r.removeOwner(name)
	so := r.newEnv(name, dir)
	return r.runIn(so, name, string(src))
}

func (r *Runtime) runIn(so *scriptEnv, chunk, src string) error {
	if strings.HasPrefix(src, "\x1b") {
		err := fmt.Errorf("%s: %w", so.name, errBinaryChunk)
		r.logf(LogError, so.name, "%v", err)
		return err
	}
	fn, err := r.L.Load(strings.NewReader(src), chunk)
	if err != nil {
		err = fmt.Errorf("%s: %w", so.name, err)
		r.logf(LogError, so.name, "%v", err)
		return err
	}
	fn.Env = so.env
	_, err = r.call(so.name, "", true, fn, 0)
	return err
}

func (r *Runtime) runFile(name string) error {
	data, err := r.host.ReadScript(name)
	if err != nil {
		err = fmt.Errorf("lua: read %s: %w", name, err)
		r.logf(LogError, name, "%v", err)
		return err
	}
	return r.runChunk(name, "", data)
}

func (r *Runtime) addUnit(u unit) {
	for i := range r.units {
		if r.units[i].kind == u.kind && r.units[i].name == u.name {
			r.units[i] = u
			return
		}
	}
	r.units = append(r.units, u)
}

// LoadString runs src as a script named name in its own environment. A script
// of the same name loaded earlier is unloaded first (its handlers and timers
// removed). Errors are returned and also reported via Host.Log. The script is
// remembered and re-run by Reload.
func (r *Runtime) LoadString(name, src string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	r.addUnit(unit{kind: "string", name: name, src: src})
	return r.runChunk(name, "", []byte(src))
}

// Unload removes a script loaded with LoadString: its handlers, timers and rules go, and Reload no longer
// re-runs it. Unknown names are ignored.
func (r *Runtime) Unload(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	kept := r.units[:0]
	for _, u := range r.units {
		if !(u.kind == "string" && u.name == name) {
			kept = append(kept, u)
		}
	}
	r.units = kept
	r.removeOwner(name)
	r.closeUIOwned(name)
}

// LoadFile reads name through Host.ReadScript and runs it like LoadString.
// Reload re-reads the file.
func (r *Runtime) LoadFile(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	r.addUnit(unit{kind: "file", name: name})
	return r.runFile(name)
}

// Exec runs code in the persistent "console" environment (the REPL behind a
// `/lua` command) as live input for world, under the usual budget.
func (r *Runtime) Exec(world, code string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	if r.console == nil {
		r.console = r.newEnv("console", "")
	}
	fn, err := r.L.Load(strings.NewReader(code), "=console")
	if err != nil {
		r.logf(LogError, "console", "%v", err)
		return err
	}
	fn.Env = r.console.env
	_, err = r.call("console", world, true, fn, 0)
	return err
}

// ExecInCallback runs code in the console environment from inside a Host
// callback made by the running script (the TF `/lua` command reached through
// squat.tf -> Host.RunTF). It must be called synchronously on the goroutine
// that is inside that Host method; it does not take the lock and shares the
// outer call's budget. It returns an error if no script is running.
func (r *Runtime) ExecInCallback(code string) error {
	if !r.running {
		return errors.New("lua: ExecInCallback outside a running script")
	}
	if r.console == nil {
		r.console = r.newEnv("console", "")
	}
	fn, err := r.L.Load(strings.NewReader(code), "=console")
	if err != nil {
		return err
	}
	fn.Env = r.console.env
	r.L.Push(fn)
	return r.L.PCall(0, 0, nil)
}

// worldMatches reports whether rule filter w names world id.
func (r *Runtime) worldMatches(w, id string) bool {
	return w == id || w == r.host.WorldName(id)
}
