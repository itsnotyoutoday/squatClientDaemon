package engine

import "github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"

// pipeResult is the display outcome of the trigger stage for one line.
type pipeResult struct {
	Gag   bool
	Spans []proto.Span
}

// runPipeline is the automation site of docs/AUTOMATION.md: tf.OnLine, then
// lua.OnLine. live=true performs side effects; live=false (replay) computes
// display effects only. Both engines see the line even when the first one
// gags it (so counters keep working); the gag of either wins. No engine lock
// is held here.
func (e *Engine) runPipeline(s *session, line proto.Line, live bool) pipeResult {
	tr := e.tf.OnLine(s.tfName, line, live)
	spans := line.Spans
	if tr.Spans != nil {
		spans = tr.Spans
	}
	l2 := line
	l2.Spans = spans
	lr := e.lua.OnLine(s.id, l2, live)
	if lr.Spans != nil {
		spans = lr.Spans
	}
	if spans == nil {
		spans = []proto.Span{}
	}
	return pipeResult{Gag: tr.Gag || lr.Gag, Spans: spans}
}

// Restyle recomputes the DISPLAY effects (gag, hilite) of the scripts for a
// line from history. It runs with live=false: no macro body, trigger
// function, send, timer or notification is executed. gagged reports whether
// the line would be hidden. Use it for any replayed history; never feed
// history through live processing.
func (e *Engine) Restyle(sessionID string, line proto.Line) (out proto.Line, gagged bool, err error) {
	s := e.get(sessionID)
	if s == nil {
		return line, false, ErrNoSession
	}
	r := e.runPipeline(s, line, false)
	line.Spans = r.Spans
	return line, r.Gag, nil
}
