package tf

import (
	"strings"
	"unicode"
)

// closerFor returns the index just past the bracket group that starts at
// s[start] (which must be the opener), honouring nested brackets of the same
// kind and quoted strings. It returns -1 when unterminated.
func closerFor(s string, start int, open, close byte) int {
	depth := 0
	var quote byte
	for k := start; k < len(s); k++ {
		c := s[k]
		if quote != 0 {
			if c == '\\' {
				k++
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			if open == '[' || open == '(' {
				quote = c
			}
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return k + 1
			}
		}
	}
	return -1
}

// splitStatements splits a command line into statements at "%;". Separators
// inside $[...], $(...) and %{...} groups are not separators. A plain ";" is
// ordinary text, as in TinyFugue (so "/echo a;b" prints a;b). A statement that
// begins with a "body" command (/def, /repeat, /eval) takes the rest of the
// line, keeping any "%;" intact so the body is split when it eventually runs.
func splitStatements(line string) []string {
	var out []string
	rest := line
	for {
		if isBodyCmd(rest) {
			return append(out, rest)
		}
		k := sepIndex(rest)
		if k < 0 {
			return append(out, rest)
		}
		out = append(out, strings.TrimRight(rest[:k], " \t"))
		rest = strings.TrimLeft(rest[k+2:], " \t")
	}
}

// splitBody splits a macro body at "%;".
func splitBody(body string) []string { return splitAt(body, true) }

func isBodyCmd(s string) bool {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	for _, p := range []string{"/def", "/repeat", "/eval"} {
		if strings.HasPrefix(s, p) && (len(s) == len(p) || s[len(p)] == ' ' || s[len(p)] == '\t') {
			return true
		}
	}
	return false
}

// sepIndex returns the index of the first "%;" separator outside groups, or -1.
func sepIndex(s string) int {
	for k := 0; k < len(s); k++ {
		switch c := s[k]; {
		case c == '%' && k+1 < len(s):
			switch s[k+1] {
			case '%':
				k++
			case ';':
				return k
			case '{':
				if e := closerFor(s, k+1, '{', '}'); e > 0 {
					k = e - 1
				}
			}
		case c == '$' && k+1 < len(s):
			switch s[k+1] {
			case '[':
				if e := closerFor(s, k+1, '[', ']'); e > 0 {
					k = e - 1
				}
			case '(':
				if e := closerFor(s, k+1, '(', ')'); e > 0 {
					k = e - 1
				}
			}
		}
	}
	return -1
}

// splitAt splits s at every "%;" outside groups (pct is kept for callers that
// pass false to mean "do not split").
func splitAt(s string, pct bool) []string {
	if !pct {
		return []string{s}
	}
	var out []string
	for {
		k := sepIndex(s)
		if k < 0 {
			return append(out, s)
		}
		out = append(out, strings.TrimRight(s[:k], " \t"))
		s = s[k+2:]
	}
}

// words splits s on runs of whitespace.
func words(s string) []string { return strings.Fields(s) }

// firstWord returns the first whitespace-delimited word and the remainder
// with leading whitespace removed.
func firstWord(s string) (string, string) {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	k := strings.IndexFunc(s, unicode.IsSpace)
	if k < 0 {
		return s, ""
	}
	return s[:k], strings.TrimLeftFunc(s[k:], unicode.IsSpace)
}

// optArg reads an option value starting at s: a quoted ('..' or ".."),
// parenthesised, braced, or bare (to whitespace) token. It returns the value
// and the rest of s.
func optArg(s string) (val, rest string, err error) {
	if s == "" {
		return "", "", nil
	}
	switch s[0] {
	case '\'', '"':
		q := s[0]
		var sb strings.Builder
		for k := 1; k < len(s); k++ {
			if s[k] == '\\' && k+1 < len(s) && s[k+1] == q {
				sb.WriteByte(q)
				k++
				continue
			}
			if s[k] == q {
				return sb.String(), s[k+1:], nil
			}
			sb.WriteByte(s[k])
		}
		return "", "", errUnterminated
	case '{':
		if e := closerFor(s, 0, '{', '}'); e > 0 {
			return s[1 : e-1], s[e:], nil
		}
		return "", "", errUnterminated
	case '(':
		if e := closerFor(s, 0, '(', ')'); e > 0 {
			return s[:e], s[e:], nil
		}
		return "", "", errUnterminated
	}
	k := strings.IndexFunc(s, unicode.IsSpace)
	if k < 0 {
		return s, "", nil
	}
	return s[:k], s[k:], nil
}

// parseOpts parses leading "-x[value]" options. Only letters listed in
// valued (take a value) or bools (boolean) are recognised; anything else ends
// option parsing and is left in rest. Boolean options map to "\x00".
func parseOpts(s, valued, bools string) (opts map[byte]string, rest string, err error) {
	opts = map[byte]string{}
	rest = strings.TrimLeftFunc(s, unicode.IsSpace)
	for len(rest) > 1 && rest[0] == '-' {
		c := rest[1]
		switch {
		case strings.IndexByte(valued, c) >= 0:
			var v string
			v, rest, err = optArg(rest[2:])
			if err != nil {
				return
			}
			opts[c] = v
		case strings.IndexByte(bools, c) >= 0:
			opts[c] = "\x00"
			rest = rest[2:]
		default:
			if rest[1] == '-' && (len(rest) == 2 || rest[2] == ' ') {
				rest = strings.TrimLeftFunc(rest[2:], unicode.IsSpace)
			}
			return
		}
		rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	}
	return
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentChar(c byte) bool { return isIdentStart(c) || isDigit(c) }

func validName(s string) bool {
	if s == "" || !isIdentStart(s[0]) {
		return false
	}
	for k := 1; k < len(s); k++ {
		if !isIdentChar(s[k]) {
			return false
		}
	}
	return true
}
