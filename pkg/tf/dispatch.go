package tf

import (
	"strings"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// lineCtx is the line currently going through the trigger stage.
type lineCtx struct {
	text  string
	spans []proto.Span
	gag   bool
}

// passes evaluates -E and -c for a candidate macro.
func (i *Interp) passes(m *Macro, world, text string, mi *matchInfo, params []string) bool {
	if m.Cond != "" {
		fr := &frame{name: m.Name, args: text, params: params, match: mi, world: world, locals: map[string]Value{}}
		i.frames = append(i.frames, fr)
		ok, err := i.evalCond(m.Cond)
		i.frames = i.frames[:len(i.frames)-1]
		if err != nil || !ok {
			return false
		}
	}
	if m.Chance > 0 && m.Chance < 100 && i.live {
		if i.rng(100) >= int64(m.Chance) {
			return false
		}
	}
	return true
}

func (i *Interp) consumeShot(m *Macro) {
	if !i.live || m.Shots == 0 {
		return
	}
	if m.left--; m.left <= 0 {
		i.macros.remove(m)
	}
}

// OnLine runs the trigger stage for one received line. With live=false (a
// backlog replay) only display effects (Gag, hilite) are computed: no macro
// body runs, no shot is consumed and nothing is sent.
func (i *Interp) OnLine(world string, line proto.Line, live bool) LineResult {
	i.mu.Lock()
	defer i.mu.Unlock()
	res := LineResult{Live: live, Spans: line.Spans}
	_ = i.run(world, live, func() error {
		lc := i.dispatch(world, line.Plain(), line.Spans, -1<<31, &res)
		res.Gag, res.Spans = lc.gag, lc.spans
		return nil
	})
	return res
}

// dispatch matches text against trigger macros with priority >= minPri.
func (i *Interp) dispatch(world, text string, spans []proto.Span, minPri int, res *LineResult) *lineCtx {
	lc := &lineCtx{text: text, spans: append([]proto.Span(nil), spans...)}
	prev := i.lineCtx
	i.lineCtx = lc
	defer func() { i.lineCtx = prev }()
	cands := i.macros.matching(func(m *Macro) bool {
		return m.Trigger != "" && m.Priority >= minPri && worldOK(m, world)
	})
	for _, m := range cands {
		if m.deleted {
			continue
		}
		mi, ok := m.match(text)
		if !ok {
			continue
		}
		params := strings.Fields(text)
		if !i.passes(m, world, text, mi, params) {
			continue
		}
		if res != nil {
			res.Fired = append(res.Fired, m.Name)
		}
		if m.Gag && i.getVarStr("gag") != "0" {
			lc.gag = true
		}
		if i.getVarStr("hilite") != "0" {
			lc.spans = applyRange(lc.spans, 0, len(text), m.Attr)
			for _, p := range m.parts {
				if p.n < len(mi.idx) && mi.idx[p.n][0] >= 0 {
					lc.spans = applyRange(lc.spans, mi.idx[p.n][0], mi.idx[p.n][1], p.a)
				}
			}
		}
		if i.live {
			i.consumeShot(m)
			_ = i.callMacroWith(m, text, params, world, mi)
		}
		if !m.Fall {
			break
		}
	}
	return lc
}

// Key runs the macro bound (-b/-k, /bind) to key in world. It reports
// whether a binding existed.
func (i *Interp) Key(world, key string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	cands := i.macros.matching(func(m *Macro) bool { return m.Key == key && worldOK(m, world) })
	if len(cands) == 0 {
		return false
	}
	_ = i.run(world, true, func() error { return i.callMacroWith(cands[0], "", nil, world, nil) })
	return true
}

// callMacro runs a named macro with args.
func (i *Interp) callMacro(m *Macro, args, world string) error {
	return i.callMacroWith(m, args, strings.Fields(args), world, nil)
}

func (i *Interp) callMacroWith(m *Macro, args string, params []string, world string, mi *matchInfo) error {
	if i.macroDep >= i.opts.MaxDepth {
		return i.errf("macro nesting too deep (limit %d) in %s", i.opts.MaxDepth, m.Name)
	}
	if !m.ready {
		nodes, err := parseNodes(splitBody(m.Body), true)
		if err != nil {
			return i.errf("macro %s: %v", m.Name, err)
		}
		m.nodes, m.ready = nodes, true
	}
	fr := &frame{name: m.Name, args: args, params: params, match: mi, world: world,
		expandText: true, locals: map[string]Value{}}
	i.frames = append(i.frames, fr)
	i.macroDep++
	err := i.execNodes(m.nodes)
	i.macroDep--
	i.frames = i.frames[:len(i.frames)-1]
	switch {
	case err == errReturn:
		i.last = fr.ret
		return nil
	case err == errBreak:
		return nil
	}
	i.last = Value{}
	return err
}
