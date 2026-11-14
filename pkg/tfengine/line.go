package tfengine

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/itsnotyoutoday/gtfparser"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// OnLine runs the trigger stage for one received line of a world. With live
// false (a backlog replay) only display effects (Gag, hilite) are computed:
// no macro body runs, no shot is consumed, nothing is sent.
func (a *Engine) OnLine(world string, line proto.Line, live bool) LineResult {
	var res LineResult
	a.do(world, func(in *gtfparser.Interp) { res = a.process(in, line.Plain(), line.Spans, live) })
	return res
}

// process is the trigger stage on the interpreter goroutine. spans nil means
// the plain text.
func (a *Engine) process(in *gtfparser.Interp, text string, spans []proto.Span, live bool) LineResult {
	if spans == nil {
		spans = []proto.Span{{S: text}}
	}
	res := LineResult{Live: live, Spans: spans}
	gagOn := flagOn(in.Var("gag"), true)
	hiliteOn := flagOn(in.Var("hilite"), true)
	r := in.Process(text, live)
	out := append([]proto.Span(nil), spans...)
	for _, m := range r.Matches {
		res.Fired = append(res.Fired, m.Macro.Name)
		whole, parts := parseMatchAttrs(m.Macro.Attrs)
		if gagOn && (m.Macro.Gag || whole.Gag) {
			res.Gag = true
		}
		if !hiliteOn {
			continue
		}
		out = applyRange(out, 0, len(text), whole)
		for _, p := range parts {
			if p.n < len(m.Groups) && m.Groups[p.n][0] >= 0 {
				out = applyRange(out, m.Groups[p.n][0], m.Groups[p.n][1], p.a)
			}
		}
	}
	res.Spans = out
	return res
}

func flagOn(v string, def bool) bool {
	switch strings.ToLower(v) {
	case "":
		return def
	case "0", "off", "no":
		return false
	}
	return true
}

type partAttr struct {
	n int
	a Attr
}

var rePart = regexp.MustCompile(`P(\d+)`)

// parseMatchAttrs splits a macro's attribute text as the engine stores it: the
// -a attributes, followed by one "P<n><attrs>" for each -P option.
func parseMatchAttrs(s string) (whole Attr, parts []partAttr) {
	idx := rePart.FindAllStringSubmatchIndex(s, -1)
	head := s
	if len(idx) > 0 {
		head = s[:idx[0][0]]
	}
	whole, _ = ParseAttr(head)
	for k, m := range idx {
		end := len(s)
		if k+1 < len(idx) {
			end = idx[k+1][0]
		}
		n, _ := strconv.Atoi(s[m[2]:m[3]])
		if at, err := ParseAttr(s[m[1]:end]); err == nil {
			parts = append(parts, partAttr{n, at})
		}
	}
	return
}

// attrsGag reports whether an attribute text contains the g (gag) attribute.
func attrsGag(s string) bool {
	whole, _ := parseMatchAttrs(s)
	return whole.Gag
}
