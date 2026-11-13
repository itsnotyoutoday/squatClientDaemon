package engine

import (
	"errors"
	"strings"
)

// ErrAmbiguousEscape: CompressEscapes met a newline right after an unpaired
// '%', which cannot be written as "%r" without changing what the text means.
var ErrAmbiguousEscape = errors.New("engine: a lone '%' before a newline cannot be encoded; write %% or remove it")

// ExpandEscapes shows the MUSH escapes %r and %t (lower case) as a real
// newline and tab for editing. Scanning is left to right and "%%" is a
// literal percent: "%%r" stays "%%r". Everything else ( %b, %R, %q0, %s ... )
// is returned untouched (%b is not expanded: it cannot be told from a space,
// which would make the round trip lossy).
func ExpandEscapes(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case 'r':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		default:
			b.WriteByte('%')
			b.WriteByte(s[i+1])
		}
		i++
	}
	return b.String()
}

// CompressEscapes is the inverse for saving: newline (also "\r\n" and a lone
// "\r") becomes %r and tab becomes %t. No other character is altered, so
// CompressEscapes(ExpandEscapes(x)) == x for every x that contains no raw
// newline or tab (a raw newline returned by the game comes back as %r).
func CompressEscapes(s string) (string, error) {
	if !strings.ContainsAny(s, "\r\n\t") {
		return s, nil
	}
	var b strings.Builder
	pct := 0 // length of the run of '%' just written
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\r', '\n':
			if c == '\r' && i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			if pct%2 == 1 {
				return "", ErrAmbiguousEscape
			}
			b.WriteString("%r")
			pct = 0
		case '\t':
			if pct%2 == 1 {
				return "", ErrAmbiguousEscape
			}
			b.WriteString("%t")
			pct = 0
		case '%':
			pct++
			b.WriteByte(c)
		default:
			pct = 0
			b.WriteByte(c)
		}
	}
	return b.String(), nil
}
