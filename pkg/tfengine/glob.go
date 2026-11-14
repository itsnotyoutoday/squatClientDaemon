package tfengine

import "strings"

// globMatch is TF's glob (* ? [set] and \ escapes), case-insensitive, used by
// the listing commands.
func globMatch(pat, s string) bool {
	return glob([]rune(strings.ToLower(pat)), []rune(strings.ToLower(s)))
}

func glob(p, s []rune) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			for len(p) > 0 && p[0] == '*' {
				p = p[1:]
			}
			if len(p) == 0 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if glob(p, s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
		case '[':
			end := -1
			for k := 1; k < len(p); k++ {
				if p[k] == ']' && k > 1 {
					end = k
					break
				}
			}
			if end < 0 {
				if len(s) == 0 || s[0] != '[' {
					return false
				}
				break
			}
			if len(s) == 0 {
				return false
			}
			set, neg := p[1:end], false
			if len(set) > 0 && set[0] == '^' {
				neg, set = true, set[1:]
			}
			in := false
			for k := 0; k < len(set); k++ {
				if k+2 < len(set) && set[k+1] == '-' {
					if s[0] >= set[k] && s[0] <= set[k+2] {
						in = true
					}
					k += 2
				} else if set[k] == s[0] {
					in = true
				}
			}
			if in == neg {
				return false
			}
			p, s = p[end+1:], s[1:]
			continue
		case '\\':
			if len(p) > 1 {
				p = p[1:]
			}
			fallthrough
		default:
			if len(s) == 0 || s[0] != p[0] {
				return false
			}
		}
		p, s = p[1:], s[1:]
	}
	return len(s) == 0
}
