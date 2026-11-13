package tf

import (
	"fmt"
	"strings"
)

// expand performs TF substitution on s:
//
//	%%            a literal %
//	%;            a literal ; (statement separators are split before expansion)
//	%name %{name} variable (local, then global); unset expands to nothing
//	%1..%9 %{N}   positional parameters; %* all arguments; %# their count
//	%0            the macro name
//	%L %R         text left/right of the match; %P0..%P9 match parts
//	%{?name}      1 if the variable is set, else 0
//	$[expr]       value of an expression
//	$(command)    output (or return value) of a command
//	$$            a literal $
func (i *Interp) expand(s string) (string, error) {
	if strings.IndexByte(s, '%') < 0 && strings.IndexByte(s, '$') < 0 {
		return s, nil
	}
	if err := i.tick(); err != nil {
		return "", err
	}
	var sb strings.Builder
	for k := 0; k < len(s); k++ {
		c := s[k]
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
				if strings.HasPrefix(name, "?") {
					_, ok := i.lookup(strings.TrimSpace(name[1:]))
					sb.WriteString(B(ok).s)
				} else if v, ok := i.lookup(strings.TrimSpace(name)); ok {
					sb.WriteString(v.s)
				}
				k = e - 1
			case n == '*' || n == '#':
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
