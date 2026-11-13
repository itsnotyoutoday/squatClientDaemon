package tf

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// Attr is a parsed attribute string such as "Cred,u" or "BC#202020,C#ff8800,h".
//
// Grammar: comma-separated items; each item is a run of flag letters
// optionally followed by one colour clause:
//
//	n normal (reset)   g gag        h or B bold   d dim   i italic
//	u underline        s strike     f flash/blink r reverse
//	C<colour>          foreground
//	BC<colour>         background
//
// A colour is a name (black red green yellow blue magenta cyan white, with an
// optional "bright" prefix), a palette index 0-255, "rgbRGB" (xterm 6x6x6
// cube, digits 0-5), "gray0".."gray23", or truecolour "#rrggbb"/"#rgb".
type Attr struct {
	Fg, Bg                                       *proto.Color
	Bold, Dim, Italic, Under, Strike, Blink, Rev bool
	Normal                                       bool
	Gag                                          bool
}

var colorNames = map[string]uint8{
	"black": 0, "red": 1, "green": 2, "yellow": 3, "blue": 4, "magenta": 5, "cyan": 6, "white": 7,
}

// ParseColor parses a colour name, index or #rrggbb.
func ParseColor(s string) (proto.Color, error) {
	ls := strings.ToLower(s)
	if strings.HasPrefix(ls, "#") {
		h := ls[1:]
		if len(h) == 3 {
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		}
		if len(h) != 6 {
			return proto.Color{}, fmt.Errorf("bad colour %q", s)
		}
		n, err := strconv.ParseUint(h, 16, 32)
		if err != nil {
			return proto.Color{}, fmt.Errorf("bad colour %q", s)
		}
		return proto.RGB(uint8(n>>16), uint8(n>>8), uint8(n)), nil
	}
	if n, err := strconv.Atoi(ls); err == nil {
		if n < 0 || n > 255 {
			return proto.Color{}, fmt.Errorf("colour index %d out of range", n)
		}
		return proto.Palette(uint8(n)), nil
	}
	if strings.HasPrefix(ls, "rgb") && len(ls) == 6 {
		r, g, b := int(ls[3]-'0'), int(ls[4]-'0'), int(ls[5]-'0')
		if r >= 0 && r < 6 && g >= 0 && g < 6 && b >= 0 && b < 6 {
			return proto.Palette(uint8(16 + 36*r + 6*g + b)), nil
		}
	}
	if strings.HasPrefix(ls, "gray") || strings.HasPrefix(ls, "grey") {
		if n, err := strconv.Atoi(ls[4:]); err == nil && n >= 0 && n < 24 {
			return proto.Palette(uint8(232 + n)), nil
		}
	}
	if strings.HasPrefix(ls, "bright") {
		if n, ok := colorNames[ls[6:]]; ok {
			return proto.Palette(n + 8), nil
		}
	}
	if n, ok := colorNames[ls]; ok {
		return proto.Palette(n), nil
	}
	return proto.Color{}, fmt.Errorf("unknown colour %q", s)
}

// ParseAttr parses an attribute string.
func ParseAttr(s string) (Attr, error) {
	var a Attr
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		for k := 0; k < len(item); {
			c := item[k]
			switch {
			case c == 'B' && k+1 < len(item) && item[k+1] == 'C':
				col, err := ParseColor(item[k+2:])
				if err != nil {
					return Attr{}, err
				}
				a.Bg = &col
				k = len(item)
			case c == 'C':
				col, err := ParseColor(item[k+1:])
				if err != nil {
					return Attr{}, err
				}
				a.Fg = &col
				k = len(item)
			case c == 'n':
				a.Normal = true
				k++
			case c == 'g':
				a.Gag = true
				k++
			case c == 'h' || c == 'B':
				a.Bold = true
				k++
			case c == 'd':
				a.Dim = true
				k++
			case c == 'i':
				a.Italic = true
				k++
			case c == 'u':
				a.Under = true
				k++
			case c == 's':
				a.Strike = true
				k++
			case c == 'f':
				a.Blink = true
				k++
			case c == 'r':
				a.Rev = true
				k++
			default:
				return Attr{}, fmt.Errorf("bad attribute %q in %q", string(c), s)
			}
		}
	}
	return a, nil
}

func colorString(c proto.Color) string {
	if c.IsTrueColor() {
		return c.Hex()
	}
	n := c.Index()
	if n < 8 {
		for name, v := range colorNames {
			if v == n {
				return name
			}
		}
	}
	return strconv.Itoa(int(n))
}

// String renders the attribute in a form ParseAttr accepts.
func (a Attr) String() string {
	var items []string
	flags := ""
	for _, f := range []struct {
		on bool
		c  string
	}{{a.Normal, "n"}, {a.Gag, "g"}, {a.Bold, "h"}, {a.Dim, "d"}, {a.Italic, "i"}, {a.Under, "u"}, {a.Strike, "s"}, {a.Blink, "f"}, {a.Rev, "r"}} {
		if f.on {
			flags += f.c
		}
	}
	if flags != "" {
		items = append(items, flags)
	}
	if a.Fg != nil {
		items = append(items, "C"+colorString(*a.Fg))
	}
	if a.Bg != nil {
		items = append(items, "BC"+colorString(*a.Bg))
	}
	return strings.Join(items, ",")
}

// IsZero reports whether the attribute changes nothing.
func (a Attr) IsZero() bool { return a.String() == "" }

// Merge overlays b on a (b.Normal first clears a's styling).
func (a Attr) Merge(b Attr) Attr {
	if b.Normal {
		a = Attr{Gag: a.Gag}
	}
	if b.Fg != nil {
		a.Fg = b.Fg
	}
	if b.Bg != nil {
		a.Bg = b.Bg
	}
	a.Bold = a.Bold || b.Bold
	a.Dim = a.Dim || b.Dim
	a.Italic = a.Italic || b.Italic
	a.Under = a.Under || b.Under
	a.Strike = a.Strike || b.Strike
	a.Blink = a.Blink || b.Blink
	a.Rev = a.Rev || b.Rev
	a.Gag = a.Gag || b.Gag
	return a
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

// ---- inline markup: @{attr}text@{n} -----------------------------------------

// EncodeAttr wraps s in inline attribute markup ("@{attr}s@{n}"); literal
// '@' characters in s are doubled.
func EncodeAttr(s, attr string) string {
	return "@{" + attr + "}" + strings.ReplaceAll(s, "@", "@@") + "@{n}"
}

// SpansFromMarkup converts text containing @{attr} markup into styled spans.
// "@@" is a literal '@'. Bad attribute strings are left as text.
func SpansFromMarkup(s string, base Attr) []proto.Span {
	var spans []proto.Span
	cur := base
	var sb strings.Builder
	flush := func() {
		if sb.Len() > 0 {
			spans = append(spans, cur.Apply(proto.Span{S: sb.String()}))
			sb.Reset()
		}
	}
	for k := 0; k < len(s); k++ {
		if s[k] == '@' && k+1 < len(s) {
			if s[k+1] == '@' {
				sb.WriteByte('@')
				k++
				continue
			}
			if s[k+1] == '{' {
				if e := strings.IndexByte(s[k:], '}'); e > 0 {
					if a, err := ParseAttr(s[k+2 : k+e]); err == nil {
						flush()
						if a.Normal {
							cur = base
						}
						cur = cur.Merge(a)
						k += e
						continue
					}
				}
			}
		}
		sb.WriteByte(s[k])
	}
	flush()
	return mergeSpans(spans)
}

func mergeSpans(in []proto.Span) []proto.Span {
	var out []proto.Span
	for _, s := range in {
		if s.S == "" {
			continue
		}
		if n := len(out); n > 0 && out[n-1].SameStyle(s) {
			out[n-1].S += s.S
			continue
		}
		out = append(out, s)
	}
	return out
}

// stripMarkup removes @{..} markup (and ANSI CSI sequences) from s.
func stripMarkup(s string) string {
	var sb strings.Builder
	for k := 0; k < len(s); k++ {
		switch {
		case s[k] == '@' && k+1 < len(s) && s[k+1] == '@':
			sb.WriteByte('@')
			k++
		case s[k] == '@' && k+1 < len(s) && s[k+1] == '{':
			if e := strings.IndexByte(s[k:], '}'); e > 0 {
				if _, err := ParseAttr(s[k+2 : k+e]); err == nil {
					k += e
					continue
				}
			}
			sb.WriteByte('@')
		case s[k] == 0x1b && k+1 < len(s) && s[k+1] == '[':
			e := k + 2
			for e < len(s) && (s[e] < 0x40 || s[e] > 0x7e) {
				e++
			}
			k = e
		default:
			sb.WriteByte(s[k])
		}
	}
	return sb.String()
}

func sgrColor(c proto.Color, bg bool) string {
	if c.IsTrueColor() {
		r, g, b := c.Components()
		if bg {
			return fmt.Sprintf("48;2;%d;%d;%d", r, g, b)
		}
		return fmt.Sprintf("38;2;%d;%d;%d", r, g, b)
	}
	n := int(c.Index())
	switch {
	case n < 8 && bg:
		return strconv.Itoa(40 + n)
	case n < 8:
		return strconv.Itoa(30 + n)
	case n < 16 && bg:
		return strconv.Itoa(100 + n - 8)
	case n < 16:
		return strconv.Itoa(90 + n - 8)
	case bg:
		return fmt.Sprintf("48;5;%d", n)
	}
	return fmt.Sprintf("38;5;%d", n)
}

// ansiFor renders spans as an ANSI string using 16-colour, 256-colour and
// 24-bit SGR sequences.
func ansiFor(spans []proto.Span) string {
	var sb strings.Builder
	styled := false
	for _, sp := range spans {
		codes := []string{"0"}
		for _, f := range []struct {
			on bool
			c  string
		}{{sp.B, "1"}, {sp.Dim, "2"}, {sp.I, "3"}, {sp.U, "4"}, {sp.Blink, "5"}, {sp.Rev, "7"}, {sp.St, "9"}} {
			if f.on {
				codes = append(codes, f.c)
			}
		}
		if sp.Fg != nil {
			codes = append(codes, sgrColor(*sp.Fg, false))
		}
		if sp.Bg != nil {
			codes = append(codes, sgrColor(*sp.Bg, true))
		}
		if len(codes) > 1 {
			sb.WriteString("\x1b[" + strings.Join(codes, ";") + "m")
			styled = true
		} else if styled {
			sb.WriteString("\x1b[0m")
			styled = false
		}
		sb.WriteString(sp.S)
	}
	if styled {
		sb.WriteString("\x1b[0m")
	}
	return sb.String()
}
