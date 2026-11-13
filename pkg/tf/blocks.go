package tf

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Control flow: /if /elseif /else /endif, /while ... /done (an optional /do
// right after the header is accepted), /do ... /while (cond), /for var a b
// ... /done, /break, /return.

type nodeKind uint8

const (
	nSimple nodeKind = iota
	nIf
	nWhile
	nDo
	nFor
	nForLine // TF form: /for var start end command (one statement, expanded first)
)

type clause struct {
	cond string
	body []node
}

type node struct {
	kind    nodeKind
	text    string // nSimple: the statement
	clauses []clause
	els     []node
	cond    string // loops
	body    []node
	fvar    string
	ffrom   string
	fto     string
}

var errIncomplete = errors.New("incomplete block: missing /endif or /done")

type nparser struct {
	st      []string
	p       int
	lenient bool
}

// parseNodes turns statements into a tree. With lenient set, running out of
// statements closes open blocks (used for single typed lines and macro
// bodies); otherwise errIncomplete is returned so file loading can keep
// accumulating lines.
func parseNodes(stmts []string, lenient bool) ([]node, error) {
	np := &nparser{st: append([]string(nil), stmts...), lenient: lenient}
	ns, term, err := np.block(nil)
	if err != nil {
		return nil, err
	}
	if term != "" {
		return nil, fmt.Errorf("unexpected /%s", term)
	}
	return ns, nil
}

func keyword(s string) string {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	if !strings.HasPrefix(s, "/") {
		return ""
	}
	w := s[1:]
	if k := strings.IndexFunc(w, func(r rune) bool { return unicode.IsSpace(r) || r == '(' }); k >= 0 {
		w = w[:k]
	}
	return strings.ToLower(w)
}

func afterKeyword(s string) string {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	k := strings.IndexFunc(s[1:], func(r rune) bool { return unicode.IsSpace(r) || r == '(' })
	if k < 0 {
		return ""
	}
	return strings.TrimLeftFunc(s[1+k:], unicode.IsSpace)
}

// condHeader splits "(cond) rest".
func condHeader(s string) (cond, rest string, err error) {
	if !strings.HasPrefix(s, "(") {
		return "", "", fmt.Errorf("expected (expression) after control keyword")
	}
	e := closerFor(s, 0, '(', ')')
	if e < 0 {
		return "", "", fmt.Errorf("missing ) in condition")
	}
	return s[1 : e-1], strings.TrimLeftFunc(s[e:], unicode.IsSpace), nil
}

// inject replaces the statement at p by rest (when non-empty) or skips it.
func (np *nparser) inject(rest string) {
	if rest != "" {
		np.st[np.p] = rest
	} else {
		np.p++
	}
}

func in(k string, set []string) bool {
	for _, s := range set {
		if s == k {
			return true
		}
	}
	return false
}

// block parses statements until one of the terminators (keyword names) is
// seen; the terminator statement is left at np.p and its keyword returned.
func (np *nparser) block(until []string) ([]node, string, error) {
	var out []node
	for np.p < len(np.st) {
		s := np.st[np.p]
		kw := keyword(s)
		if kw != "" && in(kw, until) {
			return out, kw, nil
		}
		switch kw {
		case "if":
			cond, rest, err := condHeader(afterKeyword(s))
			if err != nil {
				return nil, "", err
			}
			np.inject(rest)
			n := node{kind: nIf}
			inElse := false
			for {
				body, term, err := np.block([]string{"elseif", "else", "endif"})
				if err != nil {
					return nil, "", err
				}
				if term == "" && !np.lenient {
					return nil, "", errIncomplete
				}
				if inElse {
					n.els = body
				} else {
					n.clauses = append(n.clauses, clause{cond, body})
				}
				if term == "elseif" && !inElse {
					if cond, rest, err = condHeader(afterKeyword(np.st[np.p])); err != nil {
						return nil, "", err
					}
					np.inject(rest)
					continue
				}
				if term == "else" && !inElse {
					inElse = true
					np.inject(afterKeyword(np.st[np.p]))
					continue
				}
				if term == "endif" {
					np.p++
				}
				break
			}
			out = append(out, n)
		case "while":
			cond, rest, err := condHeader(afterKeyword(s))
			if err != nil {
				return nil, "", err
			}
			np.inject(rest)
			if np.p < len(np.st) && keyword(np.st[np.p]) == "do" && strings.TrimSpace(afterKeyword(np.st[np.p])) == "" {
				np.p++
			}
			body, term, err := np.block([]string{"done"})
			if err != nil {
				return nil, "", err
			}
			if term == "" && !np.lenient {
				return nil, "", errIncomplete
			}
			if term == "done" {
				np.p++
			}
			out = append(out, node{kind: nWhile, cond: cond, body: body})
		case "do":
			np.inject(afterKeyword(s))
			body, term, err := np.block([]string{"while"})
			if err != nil {
				return nil, "", err
			}
			if term == "" {
				if !np.lenient {
					return nil, "", errIncomplete
				}
				return nil, "", fmt.Errorf("/do without closing /while (expression)")
			}
			cond, rest, err := condHeader(afterKeyword(np.st[np.p]))
			if err != nil {
				return nil, "", err
			}
			np.inject(rest)
			out = append(out, node{kind: nDo, cond: cond, body: body})
		case "for":
			a := afterKeyword(s)
			v, r1 := firstWord(a)
			from, r2 := firstWord(r1)
			to, r3 := firstWord(r2)
			if v == "" || from == "" || to == "" {
				return nil, "", fmt.Errorf("usage: /for var start end body... /done")
			}
			hasDone := false
			for j := np.p + 1; j < len(np.st); j++ {
				if keyword(np.st[j]) == "done" {
					hasDone = true
				}
			}
			if strings.TrimSpace(r3) != "" && !hasDone {
				// TinyFugue form: the command is the rest of this statement.
				np.p++
				out = append(out, node{kind: nForLine, fvar: v, ffrom: from, fto: to, text: r3})
				continue
			}
			np.inject(r3)
			body, term, err := np.block([]string{"done"})
			if err != nil {
				return nil, "", err
			}
			if term == "" && !np.lenient {
				return nil, "", errIncomplete
			}
			if term == "done" {
				np.p++
			}
			out = append(out, node{kind: nFor, fvar: v, ffrom: from, fto: to, body: body})
		case "endif", "done", "else", "elseif":
			return nil, "", fmt.Errorf("unexpected /%s", kw)
		default:
			out = append(out, node{kind: nSimple, text: s})
			np.p++
		}
	}
	return out, "", nil
}

func isNonFatal(err error) bool {
	var be *BudgetError
	return !errors.As(err, &be) && !errors.Is(err, errBreak) && !errors.Is(err, errReturn)
}

// execNodes runs a node list. Ordinary command errors are reported and
// execution continues; the first is returned at the end. Budget errors,
// /break and /return stop immediately.
func (i *Interp) execNodes(ns []node) error {
	var first error
	for k := range ns {
		if err := i.execNode(&ns[k]); err != nil {
			if !isNonFatal(err) {
				return err
			}
			if first == nil {
				first = err
			}
		}
	}
	return first
}

func (i *Interp) evalCond(cond string) (bool, error) {
	v, err := i.evalExprText(cond)
	if err != nil {
		return false, i.fail(err)
	}
	return v.Truth(), nil
}

func (i *Interp) execNode(n *node) error {
	if err := i.tick(); err != nil {
		return err
	}
	switch n.kind {
	case nSimple:
		return i.execStmt(n.text)
	case nIf:
		for _, c := range n.clauses {
			ok, err := i.evalCond(c.cond)
			if err != nil {
				return err
			}
			if ok {
				return i.execNodes(c.body)
			}
		}
		return i.execNodes(n.els)
	case nWhile, nDo:
		first := n.kind == nDo
		for {
			if err := i.tick(); err != nil {
				return err
			}
			if !first {
				ok, err := i.evalCond(n.cond)
				if err != nil {
					return err
				}
				if !ok {
					return nil
				}
			}
			first = false
			err := i.execNodes(n.body)
			if errors.Is(err, errBreak) {
				return nil
			}
			if err != nil {
				return err // an error inside a loop body ends the loop
			}
			if n.kind == nDo {
				ok, err := i.evalCond(n.cond)
				if err != nil {
					return err
				}
				if !ok {
					return nil
				}
			}
		}
	case nForLine:
		return i.execForLine(n)
	case nFor:
		from, err := i.evalExprText(n.ffrom)
		if err != nil {
			return i.fail(err)
		}
		to, err := i.evalExprText(n.fto)
		if err != nil {
			return i.fail(err)
		}
		defer i.restoreLocal(n.fvar)()
		for x := from.Int(); x <= to.Int(); x++ {
			if err := i.tick(); err != nil {
				return err
			}
			if err := i.setLocal(n.fvar, I(x)); err != nil {
				return i.fail(err)
			}
			err := i.execNodes(n.body)
			if errors.Is(err, errBreak) {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// execStmt runs one statement: a /command, a macro call, or text to send.
func (i *Interp) execStmt(text string) error {
	text = strings.TrimLeftFunc(text, unicode.IsSpace)
	if text == "" {
		return nil
	}
	if text[0] != '/' || len(text) == 1 {
		fr := i.top()
		if fr.expandText {
			var err error
			if text, err = i.expand(text); err != nil {
				return i.fail(err)
			}
		}
		return i.fail(i.sendText(fr.world, text))
	}
	name, args := firstWord(text[1:])
	if k := strings.IndexByte(name, '('); k >= 0 { // "/name(...)" -> name + rest
		args = name[k:] + " " + args
		name = name[:k]
	}
	if m := i.macros.byName(name); m != nil {
		ex, err := i.expand(args)
		if err != nil {
			return i.fail(err)
		}
		return i.callMacro(m, ex, i.top().world)
	}
	c := i.cmds[name]
	if c == nil {
		return i.errf("%s: no such command or macro", "/"+name)
	}
	if !c.raw {
		var err error
		if args, err = i.expand(args); err != nil {
			return i.fail(err)
		}
	}
	return i.fail(c.fn(i, args))
}

// execForLine runs the TinyFugue form "/for var start end command". As in TF
// the whole line is substituted once before the loop, so a loop body that
// wants the counter written %%var (or {var}); the body is substituted again
// on every pass.
func (i *Interp) execForLine(n *node) error {
	var parts [3]string
	for k, t := range []string{n.fvar, n.ffrom, n.fto} {
		ex, err := i.expand(t)
		if err != nil {
			return i.fail(err)
		}
		parts[k] = ex
	}
	body, err := i.expand(n.text)
	if err != nil {
		return i.fail(err)
	}
	from, err := i.evalExprText(parts[1])
	if err != nil {
		return i.fail(err)
	}
	to, err := i.evalExprText(parts[2])
	if err != nil {
		return i.fail(err)
	}
	defer i.restoreLocal(parts[0])()
	for x := from.Int(); x <= to.Int(); x++ {
		if err := i.tick(); err != nil {
			return err
		}
		if err := i.setLocal(parts[0], I(x)); err != nil {
			return i.fail(err)
		}
		err := i.execBody(body)
		if errors.Is(err, errBreak) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// restoreLocal returns a func that puts the current frame's local variable
// name back as it is now (a /for counter does not outlive its loop).
func (i *Interp) restoreLocal(name string) func() {
	fr := i.top()
	old, had := fr.locals[name]
	return func() {
		if had {
			fr.locals[name] = old
		} else {
			delete(fr.locals, name)
		}
	}
}
