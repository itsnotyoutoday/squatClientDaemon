package tf

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Expression language. Precedence, lowest to highest:
//
//	,                       sequence
//	= := += -= *= /=        assignment (right assoc, target must be a variable)
//	?:                      conditional
//	| ||                    logical or (short-circuit)
//	& &&                    logical and (short-circuit)
//	== != =~ !~ =/ !/       equality: numeric when both sides are numbers,
//	                        else string; =~ !~ exact string compare;
//	                        =/ !/ glob match (right side is the pattern)
//	< > <= >=
//	+ -
//	* /                     integer division when both operands are integers
//	! - + ++ --             unary / prefix
//	x++ x--                 postfix
//
// Barewords are variables; name(args) calls a function; {name} {1} {*} {#}
// and %name %1 %* %# %{name} read variables and positional parameters
// directly (their value is never re-parsed as expression text, so script
// input cannot inject operators). There is no modulo operator: use mod().
// '%' is not interpolated inside string literals; use strcat().

type evalFn func(i *Interp) (Value, error)

type tokKind uint8

const (
	tEOF tokKind = iota
	tNum
	tStr
	tID
	tRef
	tOp
)

type token struct {
	k tokKind
	s string
	v Value
}

func lexExpr(src string) ([]token, error) {
	var out []token
	ops3 := []string{":=", "+=", "-=", "*=", "/=", "==", "!=", "<=", ">=", "=~", "!~", "=/", "!/", "&&", "||", "++", "--"}
	for k := 0; k < len(src); {
		c := src[k]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			k++
		case isDigit(c) || (c == '.' && k+1 < len(src) && isDigit(src[k+1])):
			e := k
			if c == '0' && k+1 < len(src) && (src[k+1] == 'x' || src[k+1] == 'X') {
				e = k + 2
				for e < len(src) && strings.IndexByte("0123456789abcdefABCDEF", src[e]) >= 0 {
					e++
				}
				n, err := strconv.ParseInt(src[k+2:e], 16, 64)
				if err != nil {
					return nil, fmt.Errorf("bad hex number %q", src[k:e])
				}
				out = append(out, token{k: tNum, v: I(n)})
				k = e
				continue
			}
			for e < len(src) && (isDigit(src[e]) || src[e] == '.') {
				e++
			}
			if e < len(src) && (src[e] == 'e' || src[e] == 'E') {
				j := e + 1
				if j < len(src) && (src[j] == '+' || src[j] == '-') {
					j++
				}
				if j < len(src) && isDigit(src[j]) {
					for j < len(src) && isDigit(src[j]) {
						j++
					}
					e = j
				}
			}
			v := S(src[k:e])
			if !v.num {
				return nil, fmt.Errorf("bad number %q", src[k:e])
			}
			if !v.isInt {
				v = F(v.f)
			}
			out = append(out, token{k: tNum, v: v})
			k = e
		case c == '"' || c == '\'':
			var sb strings.Builder
			e := k + 1
			for ; e < len(src) && src[e] != c; e++ {
				if src[e] == '\\' && e+1 < len(src) {
					e++
					switch src[e] {
					case 'n':
						sb.WriteByte('\n')
					case 't':
						sb.WriteByte('\t')
					case 'r':
						sb.WriteByte('\r')
					case 'e':
						sb.WriteByte(0x1b)
					default:
						sb.WriteByte(src[e])
					}
					continue
				}
				sb.WriteByte(src[e])
			}
			if e >= len(src) {
				return nil, fmt.Errorf("unterminated string in expression")
			}
			out = append(out, token{k: tStr, s: sb.String(), v: S(sb.String())})
			k = e + 1
		case isIdentStart(c):
			e := k
			for e < len(src) && isIdentChar(src[e]) {
				e++
			}
			out = append(out, token{k: tID, s: src[k:e]})
			k = e
		case c == '%' && k+1 < len(src):
			// %name, %1, %*, %# and %{name} are variable references (not text substitution)
			n := src[k+1]
			switch {
			case n == '{':
				e := strings.IndexByte(src[k:], '}')
				if e < 0 {
					return nil, fmt.Errorf("unterminated %%{ in expression")
				}
				out = append(out, token{k: tRef, s: strings.TrimSpace(src[k+2 : k+e])})
				k += e + 1
			case n == '*' || n == '#':
				out = append(out, token{k: tRef, s: string(n)})
				k += 2
			case isIdentChar(n):
				e := k + 1
				for e < len(src) && isIdentChar(src[e]) {
					e++
				}
				out = append(out, token{k: tRef, s: src[k+1 : e]})
				k = e
			default:
				return nil, fmt.Errorf("unexpected %% in expression")
			}
		case c == '{':
			e := strings.IndexByte(src[k:], '}')
			if e < 0 {
				return nil, fmt.Errorf("unterminated { in expression")
			}
			out = append(out, token{k: tRef, s: strings.TrimSpace(src[k+1 : k+e])})
			k += e + 1
		default:
			matched := false
			for _, o := range ops3 {
				if strings.HasPrefix(src[k:], o) {
					out = append(out, token{k: tOp, s: o})
					k += 2
					matched = true
					break
				}
			}
			if matched {
				continue
			}
			if strings.IndexByte("=+-*/<>!&|?:(),", c) < 0 {
				return nil, fmt.Errorf("unexpected character %q in expression", string(c))
			}
			out = append(out, token{k: tOp, s: string(c)})
			k++
		}
	}
	return append(out, token{k: tEOF}), nil
}

type exprParser struct {
	t []token
	p int
}

func (p *exprParser) peek() token { return p.t[p.p] }
func (p *exprParser) next() token { t := p.t[p.p]; p.p++; return t }
func (p *exprParser) isOp(s string) bool {
	t := p.t[p.p]
	return t.k == tOp && t.s == s
}
func (p *exprParser) accept(s string) bool {
	if p.isOp(s) {
		p.p++
		return true
	}
	return false
}

// evalExprText parses (with a small cache) and evaluates an expression.
func (i *Interp) evalExprText(src string) (Value, error) {
	if err := i.tick(); err != nil {
		return Value{}, err
	}
	fn, ok := i.exprs[src]
	if !ok {
		toks, err := lexExpr(src)
		if err != nil {
			return Value{}, err
		}
		p := &exprParser{t: toks}
		if p.peek().k == tEOF {
			fn = func(*Interp) (Value, error) { return Value{}, nil }
		} else {
			if fn, err = p.comma(); err != nil {
				return Value{}, err
			}
			if p.peek().k != tEOF {
				return Value{}, unexpected(p.peek())
			}
		}
		if len(i.exprs) > 2048 {
			i.exprs = map[string]evalFn{}
		}
		i.exprs[src] = fn
	}
	return fn(i)
}

func unexpected(t token) error {
	switch t.k {
	case tEOF:
		return fmt.Errorf("unexpected end of expression")
	case tNum:
		return fmt.Errorf("unexpected %q in expression", t.v.s)
	}
	return fmt.Errorf("unexpected %q in expression", t.s)
}

func (p *exprParser) comma() (evalFn, error) {
	first, err := p.assign()
	if err != nil {
		return nil, err
	}
	fns := []evalFn{first}
	for p.accept(",") {
		f, err := p.assign()
		if err != nil {
			return nil, err
		}
		fns = append(fns, f)
	}
	if len(fns) == 1 {
		return first, nil
	}
	return func(i *Interp) (Value, error) {
		var v Value
		var err error
		for _, f := range fns {
			if v, err = f(i); err != nil {
				return v, err
			}
		}
		return v, nil
	}, nil
}

var assignOps = map[string]bool{"=": true, ":=": true, "+=": true, "-=": true, "*=": true, "/=": true}

func (p *exprParser) assign() (evalFn, error) {
	t := p.peek()
	if (t.k == tID || t.k == tRef) && p.p+1 < len(p.t) {
		if n := p.t[p.p+1]; n.k == tOp && assignOps[n.s] {
			p.p += 2
			rhs, err := p.assign()
			if err != nil {
				return nil, err
			}
			name, op := t.s, n.s
			return func(i *Interp) (Value, error) {
				v, err := rhs(i)
				if err != nil {
					return v, err
				}
				if op != "=" && op != ":=" {
					cur, _ := i.lookup(name)
					if v, err = arith(op[:1], cur, v); err != nil {
						return v, err
					}
				}
				return v, i.setVar(name, v)
			}, nil
		}
	}
	return p.ternary()
}

func (p *exprParser) ternary() (evalFn, error) {
	c, err := p.binary(0)
	if err != nil {
		return nil, err
	}
	if !p.accept("?") {
		return c, nil
	}
	a, err := p.assign()
	if err != nil {
		return nil, err
	}
	if !p.accept(":") {
		return nil, fmt.Errorf("expected ':' in ?: expression")
	}
	b, err := p.assign()
	if err != nil {
		return nil, err
	}
	return func(i *Interp) (Value, error) {
		v, err := c(i)
		if err != nil {
			return v, err
		}
		if v.Truth() {
			return a(i)
		}
		return b(i)
	}, nil
}

var binLevels = [][]string{
	{"|", "||"},
	{"&", "&&"},
	{"==", "!=", "=~", "!~", "=/", "!/"},
	{"<", ">", "<=", ">="},
	{"+", "-"},
	{"*", "/"},
}

func (p *exprParser) binary(level int) (evalFn, error) {
	if level >= len(binLevels) {
		return p.unary()
	}
	left, err := p.binary(level + 1)
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		op := ""
		if t.k == tOp {
			for _, o := range binLevels[level] {
				if t.s == o {
					op = o
				}
			}
		}
		if op == "" {
			return left, nil
		}
		p.p++
		right, err := p.binary(level + 1)
		if err != nil {
			return nil, err
		}
		l, r := left, right
		switch op {
		case "|", "||":
			left = func(i *Interp) (Value, error) {
				a, err := l(i)
				if err != nil || a.Truth() {
					return B(err == nil), err
				}
				b, err := r(i)
				return B(b.Truth()), err
			}
		case "&", "&&":
			left = func(i *Interp) (Value, error) {
				a, err := l(i)
				if err != nil || !a.Truth() {
					return B(false), err
				}
				b, err := r(i)
				return B(b.Truth()), err
			}
		default:
			o := op
			left = func(i *Interp) (Value, error) {
				a, err := l(i)
				if err != nil {
					return a, err
				}
				b, err := r(i)
				if err != nil {
					return b, err
				}
				return binop(o, a, b)
			}
		}
	}
}

func binop(op string, a, b Value) (Value, error) {
	switch op {
	case "==":
		return B(cmpValues(a, b) == 0), nil
	case "!=":
		return B(cmpValues(a, b) != 0), nil
	case "=~":
		return B(a.s == b.s), nil
	case "!~":
		return B(a.s != b.s), nil
	case "=/":
		return B(globMatch(b.s, a.s)), nil
	case "!/":
		return B(!globMatch(b.s, a.s)), nil
	case "<":
		return B(cmpValues(a, b) < 0), nil
	case ">":
		return B(cmpValues(a, b) > 0), nil
	case "<=":
		return B(cmpValues(a, b) <= 0), nil
	case ">=":
		return B(cmpValues(a, b) >= 0), nil
	}
	return arith(op, a, b)
}

func arith(op string, a, b Value) (Value, error) {
	if (!a.num || a.isInt) && (!b.num || b.isInt) {
		x, y := a.Int(), b.Int()
		switch op {
		case "+":
			return I(x + y), nil
		case "-":
			return I(x - y), nil
		case "*":
			return I(x * y), nil
		case "/":
			if y == 0 {
				return Value{}, fmt.Errorf("division by zero")
			}
			if x == math.MinInt64 && y == -1 {
				return I(x), nil
			}
			return I(x / y), nil
		}
	}
	x, y := a.Float(), b.Float()
	switch op {
	case "+":
		return F(x + y), nil
	case "-":
		return F(x - y), nil
	case "*":
		return F(x * y), nil
	case "/":
		if y == 0 {
			return Value{}, fmt.Errorf("division by zero")
		}
		return F(x / y), nil
	}
	return Value{}, fmt.Errorf("bad operator %q", op)
}

func (p *exprParser) unary() (evalFn, error) {
	t := p.peek()
	if t.k == tOp {
		switch t.s {
		case "!", "-", "+":
			p.p++
			f, err := p.unary()
			if err != nil {
				return nil, err
			}
			op := t.s
			return func(i *Interp) (Value, error) {
				v, err := f(i)
				if err != nil {
					return v, err
				}
				switch op {
				case "!":
					return B(!v.Truth()), nil
				case "-":
					if v.num && !v.isInt {
						return F(-v.f), nil
					}
					return I(-v.Int()), nil
				}
				return v, nil
			}, nil
		case "++", "--":
			p.p++
			id := p.next()
			if id.k != tID && id.k != tRef {
				return nil, fmt.Errorf("%s needs a variable", t.s)
			}
			return incdec(id.s, t.s, true), nil
		}
	}
	f, err := p.primary()
	if err != nil {
		return nil, err
	}
	if n := p.peek(); n.k == tOp && (n.s == "++" || n.s == "--") && p.p > 0 {
		if prev := p.t[p.p-1]; prev.k == tID || prev.k == tRef {
			p.p++
			return incdec(prev.s, n.s, false), nil
		}
	}
	return f, nil
}

func incdec(name, op string, prefix bool) evalFn {
	d := int64(1)
	if op == "--" {
		d = -1
	}
	return func(i *Interp) (Value, error) {
		old, _ := i.lookup(name)
		nv, err := arith("+", old, I(d))
		if err != nil {
			return nv, err
		}
		if err := i.setVar(name, nv); err != nil {
			return nv, err
		}
		if prefix {
			return nv, nil
		}
		if !old.num {
			return I(0), nil
		}
		return old, nil
	}
}

func (p *exprParser) primary() (evalFn, error) {
	t := p.next()
	switch t.k {
	case tNum, tStr:
		v := t.v
		return func(*Interp) (Value, error) { return v, nil }, nil
	case tRef:
		name := t.s
		return func(i *Interp) (Value, error) { v, _ := i.lookup(name); return v, nil }, nil
	case tID:
		if p.isOp("(") {
			p.p++
			var args []evalFn
			if !p.accept(")") {
				for {
					a, err := p.assign()
					if err != nil {
						return nil, err
					}
					args = append(args, a)
					if p.accept(")") {
						break
					}
					if !p.accept(",") {
						return nil, fmt.Errorf("expected ',' or ')' in call to %s", t.s)
					}
				}
			}
			name := t.s
			return func(i *Interp) (Value, error) {
				if err := i.tick(); err != nil {
					return Value{}, err
				}
				vals := make([]Value, len(args))
				for k, a := range args {
					v, err := a(i)
					if err != nil {
						return v, err
					}
					vals[k] = v
				}
				return i.callFunc(name, vals)
			}, nil
		}
		name := t.s
		return func(i *Interp) (Value, error) { v, _ := i.lookup(name); return v, nil }, nil
	case tOp:
		if t.s == "(" {
			f, err := p.comma()
			if err != nil {
				return nil, err
			}
			if !p.accept(")") {
				return nil, fmt.Errorf("missing ')' in expression")
			}
			return f, nil
		}
	}
	return nil, unexpected(t)
}
