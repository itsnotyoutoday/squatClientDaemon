package tfengine

import "github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"

// Attr is the client's display attribute set: what the engine's attributes
// (tf's: line.go fromEngineAttr) become in a proto.Span.
type Attr struct {
	Fg, Bg                                       *proto.Color
	Bold, Dim, Italic, Under, Strike, Blink, Rev bool
	Normal                                       bool
	Gag                                          bool
}

// IsZero reports whether the attribute changes nothing.
func (a Attr) IsZero() bool {
	return a.Fg == nil && a.Bg == nil && !a.Bold && !a.Dim && !a.Italic && !a.Under && !a.Strike && !a.Blink && !a.Rev && !a.Normal
}

// Apply returns sp with the attribute applied.
func (a Attr) Apply(sp proto.Span) proto.Span {
	if a.Normal {
		sp = proto.Span{S: sp.S, Link: sp.Link}
	}
	if a.Fg != nil {
		c := *a.Fg
		sp.Fg = &c
	}
	if a.Bg != nil {
		c := *a.Bg
		sp.Bg = &c
	}
	sp.B = sp.B || a.Bold
	sp.Dim = sp.Dim || a.Dim
	sp.I = sp.I || a.Italic
	sp.U = sp.U || a.Under
	sp.St = sp.St || a.Strike
	sp.Blink = sp.Blink || a.Blink
	sp.Rev = sp.Rev || a.Rev
	return sp
}

// applyRange applies a to the byte range [from,to) of the concatenated span
// text, splitting spans as needed.
func applyRange(spans []proto.Span, from, to int, a Attr) []proto.Span {
	if from >= to || a.IsZero() {
		return spans
	}
	var out []proto.Span
	pos := 0
	for _, sp := range spans {
		end := pos + len(sp.S)
		if end <= from || pos >= to {
			out = append(out, sp)
			pos = end
			continue
		}
		lo, hi := max(from, pos)-pos, min(to, end)-pos
		if lo > 0 {
			l := sp
			l.S = sp.S[:lo]
			out = append(out, l)
		}
		m := sp
		m.S = sp.S[lo:hi]
		out = append(out, a.Apply(m))
		if hi < len(sp.S) {
			r := sp
			r.S = sp.S[hi:]
			out = append(out, r)
		}
		pos = end
	}
	return out
}
