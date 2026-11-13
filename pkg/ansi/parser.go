package ansi

import (
	"strconv"
	"strings"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

type slot struct {
	set bool
	c   proto.Color
}

const (
	fBold uint8 = 1 << iota
	fDim
	fItalic
	fUnder
	fStrike
	fBlink
	fRev
)

// style is the complete, comparable rendering state.
type style struct {
	fg, bg slot
	flags  uint8
	link   string
}

// Parser converts lines to spans. Style state persists across ParseLine calls
// (a colour opened on one line applies to the next) until Reset. A Parser is
// not safe for concurrent use.
type Parser struct {
	st style
}

// NewParser returns a Parser in the default style.
func NewParser() *Parser { return &Parser{} }

// Reset returns the parser to the default style (including no hyperlink).
func (p *Parser) Reset() { p.st = style{} }

// ParseLine converts one line (without its newline) to spans. The result is
// never nil; a blank line yields an empty slice. Spans have non-empty text,
// identical adjacent styles are merged and default-style fields are omitted.
func (p *Parser) ParseLine(line string) []proto.Span {
	spans := make([]proto.Span, 0, 4)
	var run []byte
	cur := p.st

	flush := func() {
		if len(run) == 0 {
			return
		}
		sp := spanFor(cur, string(run))
		run = run[:0]
		if n := len(spans); n > 0 && spans[n-1].SameStyle(sp) {
			spans[n-1].S += sp.S
			return
		}
		spans = append(spans, sp)
	}
	apply := func() {
		if p.st != cur {
			flush()
			cur = p.st
		}
	}

	for i := 0; i < len(line); {
		c := line[i]
		switch {
		case c == esc:
			next, kind, payload, _ := scanEscape(line, i)
			switch kind {
			case kindSGR:
				p.sgr(payload)
				apply()
			case kindOSC:
				if rest, ok := strings.CutPrefix(payload, "8;"); ok {
					_, uri, _ := strings.Cut(rest, ";")
					p.st.link = uri
					apply()
				}
			}
			i = next
		case c < 0x20 && c != '\t', c == 0x7f:
			i++
		case c == 0xc2 && i+1 < len(line) && line[i+1] >= 0x80 && line[i+1] <= 0x9f:
			i += 2 // C1 control
		default:
			run = append(run, c)
			i++
		}
	}
	flush()
	return spans
}

func spanFor(st style, text string) proto.Span {
	sp := proto.Span{
		S: text, B: st.flags&fBold != 0, Dim: st.flags&fDim != 0, I: st.flags&fItalic != 0,
		U: st.flags&fUnder != 0, St: st.flags&fStrike != 0, Blink: st.flags&fBlink != 0,
		Rev: st.flags&fRev != 0, Link: st.link,
	}
	if st.fg.set {
		c := st.fg.c
		sp.Fg = &c
	}
	if st.bg.set {
		c := st.bg.c
		sp.Bg = &c
	}
	return sp
}

// parseNum parses a decimal SGR parameter. Empty means 0; values that are
// not plain digits or exceed 65535 return -1 (invalid).
func parseNum(s string) int {
	if s == "" {
		return 0
	}
	if len(s) > 5 {
		return -1
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 65535 {
		return -1
	}
	return n
}

func (p *Parser) sgr(params string) {
	if params == "" {
		p.st.fg, p.st.bg, p.st.flags = slot{}, slot{}, 0
		return
	}
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		if strings.IndexByte(part, ':') >= 0 {
			p.sgrColon(part)
			continue
		}
		code := parseNum(part)
		switch {
		case code == 38 || code == 48:
			consumed, c, ok := extColor(parts[i+1:])
			i += consumed
			if ok {
				p.setColor(code == 38, c)
			}
		default:
			p.simple(code)
		}
	}
}

func (p *Parser) setColor(fg bool, c proto.Color) {
	if fg {
		p.st.fg = slot{true, c}
	} else {
		p.st.bg = slot{true, c}
	}
}

// extColor decodes the arguments following 38/48 in the semicolon form. It
// returns how many parameters were consumed.
func extColor(rest []string) (consumed int, c proto.Color, ok bool) {
	if len(rest) == 0 {
		return 0, c, false
	}
	switch parseNum(rest[0]) {
	case 5:
		if len(rest) < 2 {
			return len(rest), c, false
		}
		n := parseNum(rest[1])
		if n < 0 || n > 255 {
			return 2, c, false
		}
		return 2, proto.Palette(uint8(n)), true
	case 2:
		if len(rest) < 4 {
			return len(rest), c, false
		}
		r, g, b := parseNum(rest[1]), parseNum(rest[2]), parseNum(rest[3])
		if r < 0 || r > 255 || g < 0 || g > 255 || b < 0 || b > 255 {
			return 4, c, false
		}
		return 4, proto.RGB(uint8(r), uint8(g), uint8(b)), true
	default:
		return 1, c, false
	}
}

// sgrColon handles the ITU T.416 colon sub-parameter form (38:2::r:g:b,
// 38:5:n, 4:3 ...).
func (p *Parser) sgrColon(part string) {
	sub := strings.Split(part, ":")
	code := parseNum(sub[0])
	switch code {
	case 38, 48:
		if len(sub) < 3 {
			return
		}
		switch parseNum(sub[1]) {
		case 5:
			if n := parseNum(sub[2]); n >= 0 && n <= 255 {
				p.setColor(code == 38, proto.Palette(uint8(n)))
			}
		case 2:
			v := sub[2:]
			if len(v) >= 4 { // colour-space id present
				v = v[1:]
			}
			if len(v) < 3 {
				return
			}
			r, g, b := parseNum(v[0]), parseNum(v[1]), parseNum(v[2])
			if r < 0 || r > 255 || g < 0 || g > 255 || b < 0 || b > 255 {
				return
			}
			p.setColor(code == 38, proto.RGB(uint8(r), uint8(g), uint8(b)))
		}
	case 4:
		if len(sub) >= 2 && parseNum(sub[1]) == 0 {
			p.st.flags &^= fUnder
		} else {
			p.st.flags |= fUnder
		}
	}
}

func (p *Parser) simple(code int) {
	s := &p.st
	switch {
	case code == 0:
		s.fg, s.bg, s.flags = slot{}, slot{}, 0
	case code == 1:
		s.flags |= fBold
	case code == 2:
		s.flags |= fDim
	case code == 3:
		s.flags |= fItalic
	case code == 4:
		s.flags |= fUnder
	case code == 5 || code == 6:
		s.flags |= fBlink
	case code == 7:
		s.flags |= fRev
	case code == 9:
		s.flags |= fStrike
	case code == 22:
		s.flags &^= fBold | fDim
	case code == 23:
		s.flags &^= fItalic
	case code == 24:
		s.flags &^= fUnder
	case code == 25:
		s.flags &^= fBlink
	case code == 27:
		s.flags &^= fRev
	case code == 29:
		s.flags &^= fStrike
	case code >= 30 && code <= 37:
		s.fg = slot{true, proto.Palette(uint8(code - 30))}
	case code == 39:
		s.fg = slot{}
	case code >= 40 && code <= 47:
		s.bg = slot{true, proto.Palette(uint8(code - 40))}
	case code == 49:
		s.bg = slot{}
	case code >= 90 && code <= 97:
		s.fg = slot{true, proto.Palette(uint8(code - 90 + 8))}
	case code >= 100 && code <= 107:
		s.bg = slot{true, proto.Palette(uint8(code - 100 + 8))}
	}
}

// PlainText concatenates the text of spans.
func PlainText(spans []proto.Span) string {
	if len(spans) == 1 {
		return spans[0].S
	}
	var sb strings.Builder
	for _, s := range spans {
		sb.WriteString(s.S)
	}
	return sb.String()
}

// StripANSI removes escape sequences and control characters from s exactly as
// ParseLine would, returning the visible text.
func StripANSI(s string) string {
	return PlainText(NewParser().ParseLine(s))
}
