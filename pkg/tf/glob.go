package tf

import (
	"strings"
	"unicode/utf8"
)

// A glob pattern supports * ? [set] [^set] {alt1|alt2} and backslash escapes.
// Each wildcard (*, ?, [..], {..}) yields one capture, in order.

type gtokKind uint8

const (
	gLit gtokKind = iota
	gAny
	gStar
	gClass
	gAlt
)

type gtok struct {
	kind gtokKind
	r    rune
	neg  bool
	set  []rune // class: pairs lo,hi
	alts []string
}

type globPat struct{ toks []gtok }

func compileGlob(p string) *globPat {
	g := &globPat{}
	rs := []rune(p)
	for k := 0; k < len(rs); k++ {
		switch c := rs[k]; c {
		case '*':
			if n := len(g.toks); n > 0 && g.toks[n-1].kind == gStar {
				continue
			}
			g.toks = append(g.toks, gtok{kind: gStar})
		case '?':
			g.toks = append(g.toks, gtok{kind: gAny})
		case '\\':
			if k+1 < len(rs) {
				k++
			}
			g.toks = append(g.toks, gtok{kind: gLit, r: rs[k]})
		case '[':
			end := -1
			for j := k + 1; j < len(rs); j++ {
				if rs[j] == ']' && j > k+1 {
					end = j
					break
				}
			}
			if end < 0 {
				g.toks = append(g.toks, gtok{kind: gLit, r: c})
				continue
			}
			t := gtok{kind: gClass}
			body := rs[k+1 : end]
			if len(body) > 0 && (body[0] == '^' || body[0] == '!') {
				t.neg = true
				body = body[1:]
			}
			for j := 0; j < len(body); j++ {
				lo := body[j]
				hi := lo
				if j+2 < len(body) && body[j+1] == '-' {
					hi = body[j+2]
					j += 2
				}
				t.set = append(t.set, lo, hi)
			}
			g.toks = append(g.toks, t)
			k = end
		case '{':
			end := -1
			for j := k + 1; j < len(rs); j++ {
				if rs[j] == '}' {
					end = j
					break
				}
			}
			if end < 0 {
				g.toks = append(g.toks, gtok{kind: gLit, r: c})
				continue
			}
			g.toks = append(g.toks, gtok{kind: gAlt, alts: strings.Split(string(rs[k+1:end]), "|")})
			k = end
		default:
			g.toks = append(g.toks, gtok{kind: gLit, r: c})
		}
	}
	return g
}

const globBudget = 200000

// match reports whether the whole of s matches; caps are byte ranges of each
// wildcard.
func (g *globPat) match(s string) (bool, [][2]int) {
	budget := globBudget
	var caps [][2]int
	var rec func(ti, si int) bool
	rec = func(ti, si int) bool {
		budget--
		if budget < 0 {
			return false
		}
		for ti < len(g.toks) {
			t := g.toks[ti]
			switch t.kind {
			case gLit:
				if si >= len(s) {
					return false
				}
				r, n := utf8.DecodeRuneInString(s[si:])
				if r != t.r {
					return false
				}
				si += n
				ti++
			case gAny:
				if si >= len(s) {
					return false
				}
				_, n := utf8.DecodeRuneInString(s[si:])
				caps = append(caps, [2]int{si, si + n})
				if rec(ti+1, si+n) {
					return true
				}
				caps = caps[:len(caps)-1]
				return false
			case gClass:
				if si >= len(s) {
					return false
				}
				r, n := utf8.DecodeRuneInString(s[si:])
				in := false
				for j := 0; j+1 < len(t.set); j += 2 {
					if r >= t.set[j] && r <= t.set[j+1] {
						in = true
						break
					}
				}
				if in == t.neg {
					return false
				}
				caps = append(caps, [2]int{si, si + n})
				if rec(ti+1, si+n) {
					return true
				}
				caps = caps[:len(caps)-1]
				return false
			case gAlt:
				for _, a := range t.alts {
					if strings.HasPrefix(s[si:], a) {
						caps = append(caps, [2]int{si, si + len(a)})
						if rec(ti+1, si+len(a)) {
							return true
						}
						caps = caps[:len(caps)-1]
					}
				}
				return false
			case gStar:
				// try longest first
				for e := len(s); e >= si; e-- {
					if e < len(s) && !utf8.RuneStart(s[e]) {
						continue
					}
					caps = append(caps, [2]int{si, e})
					if rec(ti+1, e) {
						return true
					}
					caps = caps[:len(caps)-1]
					if budget < 0 {
						return false
					}
				}
				return false
			}
		}
		return si == len(s)
	}
	if rec(0, 0) {
		return true, caps
	}
	return false, nil
}

// globMatch is a convenience for one-off matches.
func globMatch(pat, s string) bool {
	ok, _ := compileGlob(pat).match(s)
	return ok
}
