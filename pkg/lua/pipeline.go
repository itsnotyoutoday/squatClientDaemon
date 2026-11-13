package lua

import (
	"encoding/json"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	glua "github.com/yuin/gopher-lua"
)

// lineState accumulates the effect of the handlers run over one line.
type lineState struct {
	spans    []proto.Span
	text     string
	gag      bool
	modified bool
	stop     bool
	tbl      *glua.LTable // shared table passed to line/prompt handlers
	tblSpans *glua.LTable // spans table currently stored in tbl
	dirty    bool         // Go-side spans changed since tbl was built
}

func (s *lineState) setSpans(sp []proto.Span) {
	s.spans = sp
	s.text = plainOf(sp)
	s.modified = true
	s.dirty = true
}

// OnLine runs one received line (or flushed prompt) through the script
// pipeline and returns its display effect. world is the engine's id for the
// world the line belongs to.
//
// live=false marks replayed backlog: handlers still run so that display effects
// (gag, hilite, spans) are recomputed, but trigger functions are not called and
// every side-effect API (send, echo, notify, tf, var.set, timers, registering
// rules) is a no-op returning false. live=true performs everything.
//
// Order: all matching rules sorted by priority (higher first), then
// registration order. A gag does not stop later rules; a rule can end
// processing by returning {stop=true}.
func (r *Runtime) OnLine(world string, line proto.Line, live bool) LineResult {
	st := &lineState{spans: line.Spans, text: line.Plain()}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return LineResult{Spans: line.Spans}
	}
	rules := r.snapshot(func(ru *rule) bool {
		switch ru.kind {
		case kTrigger:
			return true
		case kEvent:
			return ru.event == "line" || (ru.event == "prompt" && line.Prompt)
		}
		return false
	})
	for _, ru := range rules {
		if st.stop {
			break
		}
		if ru.removed || (ru.world != "" && !r.worldMatches(ru.world, world)) {
			continue
		}
		if ru.kind == kTrigger {
			r.runTrigger(ru, world, line.Prompt, live, st)
		} else {
			r.runLineHandler(ru, world, line.Prompt, live, st)
		}
	}
	return LineResult{Gag: st.gag, Spans: st.spans, Modified: st.modified}
}

func (r *Runtime) runTrigger(ru *rule, world string, prompt, live bool, st *lineState) {
	idx := ru.re.FindStringSubmatchIndex(st.text)
	if idx == nil {
		return
	}
	if ru.gag {
		st.gag = true
	}
	if !ru.style.empty() {
		from, to := 0, len(st.text)
		if ru.region == "match" {
			from, to = idx[0], idx[1]
		}
		st.setSpans(styleRange(st.spans, ru.style, from, to))
	}
	if ru.fn == nil || !live {
		return
	}
	L := r.L
	m := matchTable(L, ru.re, st.text, idx, world, live)
	m.RawSetString("spans", spansToLua(L, st.spans))
	m.RawSetString("prompt", glua.LBool(prompt))
	ret, err := r.call(ru.owner, world, live, ru.fn, 1, m)
	if err != nil {
		return
	}
	r.absorb(ru.owner, ret, st)
}

func (r *Runtime) runLineHandler(ru *rule, world string, prompt, live bool, st *lineState) {
	L := r.L
	if st.tbl == nil {
		st.tbl = L.CreateTable(0, 6)
		st.tbl.RawSetString("world", glua.LString(world))
		st.tbl.RawSetString("prompt", glua.LBool(prompt))
		st.tbl.RawSetString("live", glua.LBool(live))
		st.dirty = true
	}
	if st.dirty {
		st.tblSpans = spansToLua(L, st.spans)
		st.tbl.RawSetString("spans", st.tblSpans)
		st.tbl.RawSetString("text", glua.LString(st.text))
		st.dirty = false
	}
	ret, err := r.call(ru.owner, world, live, ru.fn, 1, st.tbl)
	if err != nil {
		return
	}
	// The handler may have assigned t.gag / t.spans, or returned a table.
	r.absorb(ru.owner, st.tbl, st)
	if ret != glua.LNil {
		r.absorb(ru.owner, ret, st)
	}
}

// absorb applies a handler's result table: gag=true, spans=<spans> and
// stop=true. For the shared line table, a spans table identical to the one the
// runtime handed out is ignored (in-place edits are not detected; assign a new
// table instead).
func (r *Runtime) absorb(owner string, v glua.LValue, st *lineState) {
	t, ok := v.(*glua.LTable)
	if !ok {
		return
	}
	if glua.LVAsBool(t.RawGetString("gag")) {
		st.gag = true
	}
	if glua.LVAsBool(t.RawGetString("stop")) {
		st.stop = true
	}
	sv := t.RawGetString("spans")
	if sv == glua.LNil || sv == glua.LValue(st.tblSpans) {
		return
	}
	spans, err := spansFromLua(sv)
	if err != nil {
		r.logf(LogError, owner, "invalid spans returned: %v", err)
		return
	}
	st.setSpans(spans)
	if t == st.tbl {
		// Keep the table's view consistent with what we accepted.
		st.dirty = true
	}
}

// OnInput offers a line the user typed to the scripts: aliases and `input`
// handlers, ordered by priority. It reports whether a script handled the
// input (the engine must then not send it). An alias handles input unless its
// function returns false; an input handler handles it by returning true.
func (r *Runtime) OnInput(world, text string) (handled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	rules := r.snapshot(func(ru *rule) bool {
		return ru.kind == kAlias || (ru.kind == kEvent && ru.event == "input")
	})
	for _, ru := range rules {
		if ru.removed || (ru.world != "" && !r.worldMatches(ru.world, world)) {
			continue
		}
		L := r.L
		if ru.kind == kAlias {
			idx := ru.re.FindStringSubmatchIndex(text)
			if idx == nil {
				continue
			}
			m := matchTable(L, ru.re, text, idx, world, true)
			ret, err := r.call(ru.owner, world, true, ru.fn, 1, m)
			if err != nil || ret != glua.LFalse {
				return true
			}
			continue
		}
		t := L.CreateTable(0, 2)
		t.RawSetString("text", glua.LString(text))
		t.RawSetString("world", glua.LString(world))
		ret, err := r.call(ru.owner, world, true, ru.fn, 1, t)
		if err == nil && ret == glua.LTrue {
			return true
		}
	}
	return false
}

// FireEvent delivers a non-line event (connect, disconnect, gmcp, timer, ...)
// to its handlers with live=true. The acting world is taken from the payload:
// a map with a "world" key or a proto.GmcpMsg (its Session). Handler return
// values are ignored; errors are isolated per handler.
func (r *Runtime) FireEvent(name string, payload any) {
	world := ""
	switch p := payload.(type) {
	case map[string]any:
		world, _ = p["world"].(string)
	case map[string]string:
		world = p["world"]
	case proto.GmcpMsg:
		world = p.Session
	case *proto.GmcpMsg:
		if p != nil {
			world = p.Session
		}
	case json.RawMessage:
	}
	r.FireEventFor(world, name, payload, true)
}

// FireEventFor is FireEvent with an explicit world and live flag. With
// live=false handlers run with side-effect APIs disabled.
func (r *Runtime) FireEventFor(world, name string, payload any, live bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	rules := r.snapshot(func(ru *rule) bool { return ru.kind == kEvent && ru.event == name })
	if len(rules) == 0 {
		return
	}
	arg := toLua(r.L, payload)
	for _, ru := range rules {
		if ru.removed || (ru.world != "" && !r.worldMatches(ru.world, world)) {
			continue
		}
		_, _ = r.call(ru.owner, world, live, ru.fn, 0, arg)
	}
}
