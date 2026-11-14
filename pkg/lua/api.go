package lua

import (
	"fmt"

	glua "github.com/yuin/gopher-lua"
)

// knownEvents are the names squat.on accepts.
var knownEvents = map[string]bool{
	"line": true, "prompt": true, "connect": true, "disconnect": true,
	"gmcp": true, "input": true, "timer": true,
}

// buildSquat constructs the `squat` API table for one script environment.
// Every function runs on the VM goroutine with r.mu held.
func (r *Runtime) buildSquat(so *scriptEnv) *glua.LTable {
	L := r.L
	sq := L.CreateTable(0, 24)
	fn := func(name string, f glua.LGFunction) { sq.RawSetString(name, L.NewFunction(f)) }

	// worldArg resolves the target world: explicit argument idx, else the
	// world of the current call.
	worldArg := func(L *glua.LState, idx int) string {
		if L.GetTop() >= idx && L.Get(idx) != glua.LNil {
			return L.CheckString(idx)
		}
		if r.cur.world == "" {
			L.RaiseError("no current world (pass a world name)")
		}
		return r.cur.world
	}
	// fail pushes (nil, err) for host errors.
	fail := func(L *glua.LState, err error) int {
		L.Push(glua.LNil)
		L.Push(glua.LString(err.Error()))
		return 2
	}
	// suppressed pushes false: side effects are disabled for replayed lines.
	suppressed := func(L *glua.LState) int {
		L.Push(glua.LFalse)
		return 1
	}

	fn("send", func(L *glua.LState) int {
		text := L.CheckString(1)
		if !r.cur.live {
			return suppressed(L)
		}
		if err := r.host.Send(worldArg(L, 2), text); err != nil {
			return fail(L, err)
		}
		L.Push(glua.LTrue)
		return 1
	})
	fn("echo", func(L *glua.LState) int {
		spans, err := spansFromLua(L.CheckAny(1))
		if err != nil {
			L.ArgError(1, err.Error())
		}
		if !r.cur.live {
			return suppressed(L)
		}
		r.host.Echo(worldArg(L, 2), spans)
		L.Push(glua.LTrue)
		return 1
	})
	fn("world", func(L *glua.LState) int {
		if r.cur.world == "" {
			L.Push(glua.LNil)
		} else {
			L.Push(glua.LString(r.host.WorldName(r.cur.world)))
		}
		return 1
	})
	fn("notify", func(L *glua.LState) int {
		title, body := L.CheckString(1), L.OptString(2, "")
		if !r.cur.live {
			return suppressed(L)
		}
		r.host.Notify(title, body)
		L.Push(glua.LTrue)
		return 1
	})
	fn("log", func(L *glua.LState) int {
		lvl := LogInfo
		switch L.OptString(2, "info") {
		case "warn", "warning":
			lvl = LogWarn
		case "error":
			lvl = LogError
		}
		r.logf(lvl, so.name, "%s", L.CheckString(1))
		return 0
	})
	fn("now", func(L *glua.LState) int {
		L.Push(glua.LNumber(r.nowSeconds()))
		return 1
	})
	fn("tf", func(L *glua.LState) int {
		code := L.CheckString(1)
		if !r.cur.live {
			return suppressed(L)
		}
		if err := r.host.RunTF(worldArg(L, 2), code); err != nil {
			return fail(L, err)
		}
		L.Push(glua.LTrue)
		return 1
	})
	fn("remove", r.apiRemove)
	fn("cancel", r.apiRemove)

	fn("on", func(L *glua.LState) int {
		ev, f := L.CheckString(1), L.CheckFunction(2)
		if !knownEvents[ev] {
			L.ArgError(1, fmt.Sprintf("unknown event %q (line, prompt, connect, disconnect, gmcp, input, timer)", ev))
		}
		if !r.cur.live {
			return suppressed(L)
		}
		id := r.addRule(&rule{kind: kEvent, event: ev, owner: so.name, fn: f, priority: L.OptInt(3, 0)})
		L.Push(glua.LNumber(id))
		return 1
	})
	fn("trigger", func(L *glua.LState) int { return r.apiPattern(L, so, kTrigger) })
	fn("alias", func(L *glua.LState) int { return r.apiPattern(L, so, kAlias) })
	fn("timer", func(L *glua.LState) int { return r.apiTimer(L, so, false) })
	fn("after", func(L *glua.LState) int { return r.apiTimer(L, so, true) })

	sq.RawSetString("var", r.varTable(L))
	sq.RawSetString("ui", r.uiTable(L, so))
	sq.RawSetString("regex", r.regexTable(L))
	return sq
}

func (r *Runtime) apiRemove(L *glua.LState) int {
	id := L.CheckInt(1)
	if !r.cur.live {
		L.Push(glua.LFalse)
		return 1
	}
	L.Push(glua.LBool(r.remove(id)))
	return 1
}

// apiPattern implements squat.trigger{...} and squat.alias{...}.
func (r *Runtime) apiPattern(L *glua.LState, so *scriptEnv, kind ruleKind) int {
	t := L.CheckTable(1)
	what := "trigger"
	if kind == kAlias {
		what = "alias"
	}
	pat, ok := t.RawGetString("match").(glua.LString)
	if !ok || pat == "" {
		L.ArgError(1, what+": match must be a non-empty string")
	}
	ru := &rule{kind: kind, owner: so.name}
	if f := t.RawGetString("fn"); f != glua.LNil {
		fv, ok := f.(*glua.LFunction)
		if !ok {
			L.ArgError(1, what+": fn must be a function")
		}
		ru.fn = fv
	}
	if kind == kAlias && ru.fn == nil {
		L.ArgError(1, "alias: fn is required")
	}
	re, err := r.compilePattern(string(pat), boolField(t, "regex"), boolField(t, "icase"))
	if err != nil {
		L.ArgError(1, what+": "+err.Error())
	}
	ru.re = re
	if w, ok := t.RawGetString("world").(glua.LString); ok {
		ru.world = string(w)
	}
	if p, ok := t.RawGetString("priority").(glua.LNumber); ok {
		ru.priority = int(p)
	}
	if kind == kTrigger {
		ru.gag = boolField(t, "gag")
		m := styleMask{b: boolField(t, "b"), dim: boolField(t, "dim"), i: boolField(t, "i"),
			u: boolField(t, "u"), st: boolField(t, "st"), blink: boolField(t, "blink"), rev: boolField(t, "rev")}
		if m.fg, err = colorFromLua(t.RawGetString("fg")); err != nil {
			L.ArgError(1, "trigger fg: "+err.Error())
		}
		if m.bg, err = colorFromLua(t.RawGetString("bg")); err != nil {
			L.ArgError(1, "trigger bg: "+err.Error())
		}
		ru.style = m
		ru.region = "line"
		if h, ok := t.RawGetString("hilite").(glua.LString); ok {
			switch string(h) {
			case "line", "match":
				ru.region = string(h)
			default:
				L.ArgError(1, `trigger: hilite must be "line" or "match"`)
			}
		}
	}
	if !r.cur.live {
		L.Push(glua.LFalse)
		return 1
	}
	L.Push(glua.LNumber(r.addRule(ru)))
	return 1
}
