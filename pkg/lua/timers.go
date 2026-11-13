package lua

import (
	"time"

	glua "github.com/yuin/gopher-lua"
)

// timerRec is one scheduled (possibly repeating) script timer.
type timerRec struct {
	id      int
	owner   string
	world   string
	fn      *glua.LFunction
	every   time.Duration
	left    int // remaining fires; -1 = unlimited
	cancel  func()
	removed bool
}

// apiTimer implements squat.timer{every=s, count=n, fn=f, world=w} and
// squat.after(seconds, fn). Timers go through Host.Schedule, so the host owns
// the clock; the world is captured at creation.
func (r *Runtime) apiTimer(L *glua.LState, so *scriptEnv, once bool) int {
	var secs float64
	var fn *glua.LFunction
	count := -1
	world := r.cur.world
	if once {
		secs = float64(L.CheckNumber(1))
		fn = L.CheckFunction(2)
		count = 1
	} else {
		t := L.CheckTable(1)
		ev, ok := t.RawGetString("every").(glua.LNumber)
		if !ok {
			L.ArgError(1, "timer: every (seconds) is required")
		}
		secs = float64(ev)
		f, ok := t.RawGetString("fn").(*glua.LFunction)
		if !ok {
			L.ArgError(1, "timer: fn is required")
		}
		fn = f
		if c, ok := t.RawGetString("count").(glua.LNumber); ok && c > 0 {
			count = int(c)
		}
		if w, ok := t.RawGetString("world").(glua.LString); ok {
			world = string(w)
		}
	}
	d := time.Duration(secs * float64(time.Second))
	if secs != secs || secs > 1e7 || d < r.opts.MinTimerInterval {
		L.ArgError(1, "timer interval must be between "+r.opts.MinTimerInterval.String()+" and 1e7 seconds")
	}
	if !r.cur.live {
		L.Push(glua.LFalse)
		return 1
	}
	if len(r.timers) >= r.opts.MaxTimers {
		L.RaiseError("too many timers (max %d)", r.opts.MaxTimers)
	}
	r.nextID++
	tr := &timerRec{id: r.nextID, owner: so.name, world: world, fn: fn, every: d, left: count}
	r.timers[tr.id] = tr
	r.arm(tr)
	L.Push(glua.LNumber(tr.id))
	return 1
}

// arm schedules the next tick. Caller holds mu.
func (r *Runtime) arm(tr *timerRec) {
	gen := r.gen
	tr.cancel = r.host.Schedule(tr.every, func() { r.fire(tr, gen) })
}

// fire is the Host.Schedule callback; it may run on any goroutine.
func (r *Runtime) fire(tr *timerRec, gen uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.gen != gen || tr.removed {
		return
	}
	_, _ = r.call(tr.owner, tr.world, true, tr.fn, 0)
	if tr.removed || r.closed || r.gen != gen {
		return
	}
	if tr.left > 0 {
		tr.left--
		if tr.left == 0 {
			tr.removed = true
			delete(r.timers, tr.id)
			return
		}
	}
	r.arm(tr)
}
