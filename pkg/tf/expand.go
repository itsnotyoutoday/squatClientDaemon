package tf

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

func digitIn(c byte, base int) bool {
	switch {
	case c >= '0' && c <= '9':
		return int(c-'0') < base
	case base == 16 && (c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'):
		return true
	}
	return false
}

// expand performs TF substitution on s:
//
//	%%            a literal %
//	%;            a literal ; (statement separators are split before expansion)
//	%name %{name} variable (local, then global); unset expands to nothing
//	%1..%9 %{N}   positional parameters; %* all arguments; %# their count
//	%0            the macro name
//	%-N %L %Ln %-L %R  parameters after the first N / last / all but the last / random
//	%P0.. %PL %PR  match parts, text left/right of the match
//	%{sel-dflt}   default when the selector is empty; %? last return value
//	%{?name}      1 if the variable is set, else 0
//	$[expr]       value of an expression
//	$(command)    output (or return value) of a command
//	$$            a literal $
func (i *Interp) expand(s string) (string, error) {
	if !strings.ContainsAny(s, "%$\\") {
		return s, nil
	}
	if err := i.tick(); err != nil {
		return "", err
	}
	var sb strings.Builder
	for k := 0; k < len(s); k++ {
		c := s[k]
		if c == '\\' && k+1 < len(s) {
			// \n: the character with code n (0x hex, 0 octal, else decimal);
			// \c: the character c, stripped of any special meaning.
			if isDigit(s[k+1]) {
				e := k + 1
				base := 10
				switch {
				case strings.HasPrefix(s[e:], "0x") || strings.HasPrefix(s[e:], "0X"):
					base, e = 16, e+2
				case s[e] == '0':
					base = 8
				}
				st := e
				for e < len(s) && digitIn(s[e], base) {
					e++
				}
				if n, err := strconv.ParseInt(s[st:e], base, 32); err == nil && e > st {
					sb.WriteRune(rune(n))
					k = e - 1
					continue
				}
			}
			_, w := utf8.DecodeRuneInString(s[k+1:])
			sb.WriteString(s[k+1 : k+1+w])
			k += w
			continue
		}
		if c != '%' && c != '$' || k+1 >= len(s) {
			sb.WriteByte(c)
			continue
		}
		n := s[k+1]
		if c == '%' {
			switch {
			case n == '%':
				sb.WriteByte('%')
				k++
			case n == ';':
				sb.WriteByte(';')
				k++
			case n == '{':
				e := closerFor(s, k+1, '{', '}')
				if e < 0 {
					return "", fmt.Errorf("unterminated %%{ in %q", clip(s))
				}
				name := s[k+2 : e-1]
				if strings.IndexByte(name, '%') >= 0 || strings.IndexByte(name, '$') >= 0 {
					var err error
					if name, err = i.expand(name); err != nil {
						return "", err
					}
				}
				if strings.HasPrefix(name, "?") && len(name) > 1 {
					_, ok := i.lookup(strings.TrimSpace(name[1:]))
					sb.WriteString(B(ok).s)
					k = e - 1
					break
				}
				// %{selector-default}: the default applies when the
				// selector expands to nothing. A leading '-' belongs to
				// selectors such as -1 and -L.
				sel, dflt, hasDflt := name, "", false
				from := 0
				if strings.HasPrefix(name, "-") {
					from = 1
				}
				if d := strings.IndexByte(name[from:], '-'); d >= 0 {
					sel, dflt, hasDflt = name[:from+d], name[from+d+1:], true
				}
				v, _ := i.lookup(strings.TrimSpace(sel))
				if v.s == "" && hasDflt {
					sb.WriteString(dflt)
				} else {
					sb.WriteString(v.s)
				}
				k = e - 1
			case n == '-' && k+2 < len(s) && (isDigit(s[k+2]) || s[k+2] == 'L'):
				e := k + 2
				for e < len(s) && isIdentChar(s[e]) {
					e++
				}
				if v, ok := i.lookup(s[k+1 : e]); ok {
					sb.WriteString(v.s)
				}
				k = e - 1
			case n == '*' || n == '#' || n == '?':
				v, _ := i.lookup(string(n))
				sb.WriteString(v.s)
				k++
			case isDigit(n):
				if v, ok := i.lookup(string(n)); ok {
					sb.WriteString(v.s)
				}
				k++
			case isIdentStart(n):
				e := k + 1
				for e < len(s) && isIdentChar(s[e]) {
					e++
				}
				if v, ok := i.lookup(s[k+1 : e]); ok {
					sb.WriteString(v.s)
				}
				k = e - 1
			default:
				sb.WriteByte('%')
			}
		} else { // '$'
			switch n {
			case '$':
				sb.WriteByte('$')
				k++
			case '{':
				// ${name}: the body of macro name
				e := closerFor(s, k+1, '{', '}')
				if e < 0 {
					return "", fmt.Errorf("unterminated ${ in %q", clip(s))
				}
				name := s[k+2 : e-1]
				m := i.macros.byName(name)
				if m == nil {
					return "", fmt.Errorf("macro not defined: %s", name)
				}
				sb.WriteString(m.Body)
				k = e - 1
			case '[':
				e := closerFor(s, k+1, '[', ']')
				if e < 0 {
					return "", fmt.Errorf("unterminated $[ in %q", clip(s))
				}
				v, err := i.evalExprText(s[k+2 : e-1])
				if err != nil {
					return "", err
				}
				sb.WriteString(v.s)
				k = e - 1
			case '(':
				e := closerFor(s, k+1, '(', ')')
				if e < 0 {
					return "", fmt.Errorf("unterminated $( in %q", clip(s))
				}
				out, err := i.captureCmd(s[k+2 : e-1])
				if err != nil {
					return "", err
				}
				sb.WriteString(out)
				k = e - 1
			default:
				sb.WriteByte('$')
			}
		}
		if sb.Len() > maxValueLen {
			return "", fmt.Errorf("expansion too long")
		}
	}
	return sb.String(), nil
}

func clip(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// captureCmd runs a command line and returns what it echoed (lines joined by
// spaces), or its /return value if it echoed nothing.
func (i *Interp) captureCmd(cmd string) (string, error) {
	var buf strings.Builder
	i.capture = append(i.capture, &buf)
	i.last = Value{}
	err := i.execBody(cmd)
	i.capture = i.capture[:len(i.capture)-1]
	if err != nil && !isNonFatal(err) {
		return "", err
	}
	out := strings.TrimRight(buf.String(), "\n")
	out = strings.ReplaceAll(out, "\n", " ")
	if out == "" {
		out = i.last.s
	}
	return out, nil
}

// execBody parses and runs a statement list in the current frame.
func (i *Interp) execBody(body string) error {
	nodes, err := parseNodes(splitBody(body), true)
	if err != nil {
		return err
	}
	err = i.execNodes(nodes)
	if err == errReturn {
		err = nil
	}
	return err
}
