package ansi

const (
	esc = 0x1b
	bel = 0x07
)

// escape kinds returned by scanEscape.
const (
	kindOther = iota // dropped sequence
	kindSGR          // CSI ... m, payload = parameter string
	kindOSC          // OSC ... ST|BEL, payload = text between introducer and terminator
)

// scanEscape examines the escape sequence that starts at s[i] (s[i] == ESC).
// It returns the index just after the sequence. complete is false when the
// string ended inside the sequence (next == len(s)). When a malformed byte
// aborts a sequence, next points at that byte so it is processed normally.
func scanEscape(s string, i int) (next, kind int, payload string, complete bool) {
	n := len(s)
	if i+1 >= n {
		return n, kindOther, "", false
	}
	switch c := s[i+1]; {
	case c == '[':
		j := i + 2
		for j < n && s[j] >= 0x30 && s[j] <= 0x3f {
			j++
		}
		pEnd := j
		for j < n && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		if j >= n {
			return n, kindOther, "", false
		}
		if s[j] >= 0x40 && s[j] <= 0x7e {
			if s[j] == 'm' && j == pEnd && !hasPrivateMarker(s[i+2:pEnd]) {
				return j + 1, kindSGR, s[i+2 : pEnd], true
			}
			return j + 1, kindOther, "", true
		}
		return j, kindOther, "", true // malformed: abort, reprocess byte j
	case c == ']':
		for j := i + 2; j < n; j++ {
			switch s[j] {
			case bel:
				return j + 1, kindOSC, s[i+2 : j], true
			case esc:
				if j+1 >= n {
					return n, kindOther, "", false
				}
				if s[j+1] == '\\' {
					return j + 2, kindOSC, s[i+2 : j], true
				}
				return j, kindOther, "", true // aborted by a new ESC
			}
		}
		return n, kindOther, "", false
	case c == 'P' || c == 'X' || c == '^' || c == '_': // DCS, SOS, PM, APC: skip to ST
		for j := i + 2; j < n; j++ {
			if s[j] == esc {
				if j+1 >= n {
					return n, kindOther, "", false
				}
				if s[j+1] == '\\' {
					return j + 2, kindOther, "", true
				}
				return j, kindOther, "", true
			}
		}
		return n, kindOther, "", false
	case c >= 0x20 && c <= 0x2f: // nF sequence: intermediates then a final byte
		j := i + 2
		for j < n && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		if j >= n {
			return n, kindOther, "", false
		}
		if s[j] >= 0x30 && s[j] <= 0x7e {
			return j + 1, kindOther, "", true
		}
		return j, kindOther, "", true
	case c >= 0x30 && c <= 0x7e: // two-byte sequence
		return i + 2, kindOther, "", true
	default: // ESC followed by a control or 8-bit byte: drop only the ESC
		return i + 1, kindOther, "", true
	}
}

func hasPrivateMarker(p string) bool {
	for k := 0; k < len(p); k++ {
		if p[k] >= '<' && p[k] <= '?' {
			return true
		}
	}
	return false
}

// IncompleteSuffix returns the byte index at which an unfinished escape
// sequence begins at the end of s, or len(s) when s does not end inside one.
// pkg/conn uses it to avoid cutting a sequence in half when flushing a
// partial line (prompt) or force-splitting an over-long line.
func IncompleteSuffix(s string) int {
	for i := 0; i < len(s); {
		if s[i] != esc {
			i++
			continue
		}
		next, _, _, complete := scanEscape(s, i)
		if !complete {
			return i
		}
		i = next
	}
	return len(s)
}
