package tfengine

import (
	"github.com/itsnotyoutoday/gtfparser"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// OnLine runs the trigger stage for one received line of a world. With live
// false (a backlog replay) only display effects (Gag, hilite) are computed:
// no macro body runs, no shot is consumed, nothing is sent.
func (a *Engine) OnLine(world string, line proto.Line, live bool) LineResult {
	var res LineResult
	a.do(world, func(in *gtfparser.Interp) {
		text := line.Plain()
		watchGag := false
		if live {
			watchGag = in.CheckWatch(world, text)
		}
		res = a.process(in, text, line.Spans, live)
		if watchGag {
			res.Gag = true
		}
		if live {
			attrs := ""
			if res.Gag {
				attrs = "g"
			}
			in.RecordWorldLineAttr(world, text, attrs)
		}
		res.Hidden = in.LimitedFor(world, text, "")
	})
	return res
}

// process is the trigger stage on the interpreter goroutine. spans nil means
// the plain text. The engine decides what matched, gags and which attributes
// apply (priority, fall-through, -P parts, %gag, %hilite); this converts its
// attributed result into the client's spans.
func (a *Engine) process(in *gtfparser.Interp, text string, spans []proto.Span, live bool) LineResult {
	if spans == nil {
		spans = []proto.Span{{S: text}}
	}
	res := LineResult{Live: live, Spans: spans}
	r := in.ProcessText(gtfparser.LineInput{Text: text, Dry: !live})
	for _, m := range r.Matched {
		res.Fired = append(res.Fired, m.Name)
	}
	res.Gag = r.Gag
	runs := r.Spans(in)
	out := append([]proto.Span(nil), spans...)
	if r.Substituted {
		// a trigger replaced the text: the original colours no longer line up
		out = nil
		for _, run := range runs {
			out = append(out, fromEngineAttr(run.Attr).Apply(proto.Span{S: run.Text}))
		}
	} else {
		pos := 0
		for _, run := range runs {
			out = applyRange(out, pos, pos+len(run.Text), fromEngineAttr(run.Attr))
			pos += len(run.Text)
		}
	}
	res.Spans = out
	return res
}

// fromEngineAttr converts the engine's attribute set (tf's) to this package's Attr.
func fromEngineAttr(a gtfparser.Attr) Attr {
	var o Attr
	o.Bold = a&gtfparser.AttrBold != 0
	o.Under = a&gtfparser.AttrUnderline != 0
	o.Rev = a&gtfparser.AttrReverse != 0
	o.Italic = a&gtfparser.AttrItalic != 0
	if a.HasFg() {
		c := proto.Palette(uint8(a.FgColor()))
		o.Fg = &c
	}
	if a.HasBg() {
		c := proto.Palette(uint8(a.BgColor()))
		o.Bg = &c
	}
	return o
}
