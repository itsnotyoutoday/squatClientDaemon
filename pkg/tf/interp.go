package tf

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

const maxErrorsPerRun = 20

var (
	errUnterminated = errors.New("unterminated quote or bracket")
	errBreak        = errors.New("/break outside loop")
	errReturn       = errors.New("/return")
)

// frame is one macro invocation (or one top-level run): positional
// parameters, local variables and the current match.
type frame struct {
	name       string
	args       string
	params     []string
	locals     map[string]Value
	match      *matchInfo
	world      string
	expandText bool // expand % in non-command statements (macro bodies)
	ret        Value
}

type matchInfo struct {
	text        string
	start, end  int
	left, right string
	parts       []string // P0.. (P0 is the whole match)
	idx         [][2]int // byte ranges of parts (-1 when unset)
}

// Interp is a TinyFugue-style script interpreter. It is safe for use from
// multiple goroutines (calls are serialised) but host callbacks must not
// re-enter it.
type Interp struct {
	mu   sync.Mutex
	host Host
	opts Options

	vars     map[string]Value
	exported map[string]bool
	macros   *macroStore
	cmds     map[string]*command
	funcs    map[string]*funcDef
	restrict Restrict
	rng      func(n int64) int64
	loaded   map[string]bool
	timers   map[int]*timerRec
	nextTmr  int
	exprs    map[string]evalFn

	frames   []*frame
	runDepth int
	steps    int
	deadline time.Time
	live     bool // false while replaying: side effects are forbidden
	capture  []*strings.Builder
	lineCtx  *lineCtx
	last     Value
	inSend   bool
	macroDep int
	errCount int

	lastInput map[string]time.Time
	lastSend  map[string]time.Time
	lastAny   time.Time
	noRecover bool
}

// New creates an interpreter bound to host.
func New(host Host, opts Options) *Interp {
	if opts.MaxSteps <= 0 {
		opts.MaxSteps = 50000
	}
	if opts.MaxTime == 0 {
		opts.MaxTime = 2 * time.Second
	}
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 64
	}
	if opts.Version == "" {
		opts.Version = "squat-tf 1.0 (TinyFugue-style)"
	}
	if host == nil {
		host = NopHost{}
	}
	i := &Interp{
		host:      host,
		opts:      opts,
		vars:      map[string]Value{},
		exported:  map[string]bool{},
		macros:    newMacroStore(),
		restrict:  opts.Restrict,
		loaded:    map[string]bool{},
		timers:    map[int]*timerRec{},
		exprs:     map[string]evalFn{},
		live:      true,
		lastInput: map[string]time.Time{},
		lastSend:  map[string]time.Time{},
	}
	if opts.Rand != nil {
		i.rng = opts.Rand
	} else {
		r := rand.New(rand.NewSource(host.Now().UnixNano()))
		i.rng = r.Int63n
	}
	i.lastAny = host.Now()
	i.initCommands()
	i.initFuncs()
	i.initVars()
	return i
}

// ---- running, budget ------------------------------------------------------

// run executes fn as one budgeted top-level run (or nested inside one).
func (i *Interp) run(world string, live bool, fn func() error) (err error) {
	top := i.runDepth == 0
	if top {
		i.steps = 0
		i.deadline = time.Time{}
		if i.opts.MaxTime > 0 {
			i.deadline = i.host.Now().Add(i.opts.MaxTime)
		}
		i.frames = nil
		i.capture = nil
		i.macroDep = 0
		i.errCount = 0
		i.live = live
	}
	i.runDepth++
	saveFrames, saveLive := len(i.frames), i.live
	i.frames = append(i.frames, &frame{world: world, locals: map[string]Value{}})
	defer func() {
		if r := recover(); r != nil && !i.noRecover {
			err = fmt.Errorf("internal error: %v", r)
			i.report(err)
		} else if r != nil {
			panic(r)
		}
		i.runDepth--
		if i.frames = i.frames[:saveFrames]; top {
			i.frames, i.capture, i.live = nil, nil, true
		} else {
			i.live = saveLive
		}
	}()
	err = fn()
	if errors.Is(err, errBreak) || errors.Is(err, errReturn) {
		err = nil
	}
	return err
}

func (i *Interp) tick() error {
	i.steps++
	if i.steps > i.opts.MaxSteps {
		return &BudgetError{Reason: fmt.Sprintf("more than %d steps", i.opts.MaxSteps)}
	}
	if i.steps&63 == 0 && !i.deadline.IsZero() && i.host.Now().After(i.deadline) {
		return &BudgetError{Reason: "time limit"}
	}
	return nil
}

func (i *Interp) top() *frame { return i.frames[len(i.frames)-1] }

func (i *Interp) curWorld() string {
	if len(i.frames) > 0 {
		if w := i.top().world; w != "" {
			return w
		}
	}
	return i.host.ForegroundWorld()
}

// ---- output ---------------------------------------------------------------

// echoSpans displays spans (or captures them for $(...) / /quote #).
func (i *Interp) echoSpans(world string, spans []proto.Span) {
	if n := len(i.capture); n > 0 {
		for _, s := range spans {
			i.capture[n-1].WriteString(s.S)
		}
		i.capture[n-1].WriteByte('\n')
		return
	}
	i.host.Echo(world, spans)
}

func (i *Interp) echoText(world, text string) {
	i.echoSpans(world, []proto.Span{{S: text}})
}

// report shows an error the TF way ("% message") on the current world.
func (i *Interp) report(err error) {
	if err == nil {
		return
	}
	i.errCount++
	if i.errCount > maxErrorsPerRun {
		if i.errCount == maxErrorsPerRun+1 {
			i.host.Echo(i.curWorld(), []proto.Span{{S: "% too many errors; further errors suppressed"}})
		}
		return
	}
	i.host.Echo(i.curWorld(), []proto.Span{{S: "% " + err.Error()}})
}

func (i *Interp) errf(format string, a ...any) error {
	err := fmt.Errorf(format, a...)
	i.report(err)
	return reported{err}
}

// reported marks an error already shown to the user.
type reported struct{ error }

func (r reported) Unwrap() error { return r.error }

func (i *Interp) fail(err error) error {
	if err == nil {
		return nil
	}
	var r reported
	if errors.As(err, &r) || errors.Is(err, errBreak) || errors.Is(err, errReturn) {
		return err
	}
	i.report(err)
	return reported{err}
}

// ---- public entry points --------------------------------------------------

// Exec processes one line of user input in world: a command if it begins
// with "/", otherwise text sent to the world. Errors are shown through
// Host.Echo and also returned.
func (i *Interp) Exec(world, line string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.execLine(world, line)
}

func (i *Interp) execLine(world, line string) error {
	now := i.host.Now()
	i.lastInput[world], i.lastAny = now, now
	return i.run(world, true, func() error {
		return i.fail(i.execTop(world, line))
	})
}

func (i *Interp) execTop(world, line string) error {
	t := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(t, "/") {
		if sub := i.getVarStr("sub"); sub == "on" || sub == "full" || sub == "1" {
			var err error
			if line, err = i.expand(line); err != nil {
				return err
			}
		}
		return i.sendText(world, line)
	}
	nodes, err := parseNodes(splitStatements(t), true)
	if err != nil {
		return err
	}
	return i.execNodes(nodes)
}

// sendText sends text to a world, firing the SEND hook first.
func (i *Interp) sendText(world, text string) error {
	if !i.live {
		return nil
	}
	if world == "" {
		world = i.host.ForegroundWorld()
	}
	if !i.inSend {
		i.inSend = true
		i.fireHook(world, "SEND", text)
		i.inSend = false
	}
	i.lastSend[world] = i.host.Now()
	return i.host.SendToWorld(world, text)
}

// ---- timers ---------------------------------------------------------------

type timerRec struct {
	id     int
	world  string
	cmd    string
	left   int
	every  time.Duration
	cancel func()
	next   time.Time
}

func (i *Interp) startTimer(t *timerRec) {
	t.next = i.host.Now().Add(t.every)
	t.cancel = i.host.Schedule(t.every, func() { i.fireTimer(t.id) })
}

func (i *Interp) fireTimer(id int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	t := i.timers[id]
	if t == nil {
		return
	}
	t.left--
	if t.left <= 0 {
		delete(i.timers, id)
	} else {
		i.startTimer(t)
	}
	_ = i.run(t.world, true, func() error {
		i.top().expandText = true
		nodes, err := parseNodes(splitBody(t.cmd), true)
		if err != nil {
			return i.fail(err)
		}
		return i.fail(i.execNodes(nodes))
	})
}
