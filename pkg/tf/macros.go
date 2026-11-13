package tf

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Match modes for -m.
const (
	ModeGlob   = "glob"
	ModeSimple = "simple"
	ModeRegexp = "regexp"
)

type partAttr struct {
	n int
	a Attr
}

// Macro is one /def definition (macro, trigger, hook, key binding or any
// combination of those).
type Macro struct {
	Num       int
	Name      string
	Body      string
	Trigger   string
	Mode      string
	Hook      string // upper-case hook name
	HookPat   string // optional glob over the hook's arguments
	Priority  int
	Shots     int // 0 = unlimited
	Fall      bool
	Gag       bool
	Attr      Attr
	parts     []partAttr
	Key       string
	World     string
	Cond      string
	Chance    int // percent; 0 = always
	Invisible bool
	Disabled  bool

	left    int
	deleted bool
	glob    *globPat
	re      *regexp.Regexp
	nodes   []node
	ready   bool
}

// MacroInfo is the read-only view returned by Interp.Macros.
type MacroInfo struct {
	Num      int
	Name     string
	Body     string
	Trigger  string
	Mode     string
	Hook     string
	Priority int
	Shots    int
	Fall     bool
	Gag      bool
	Attrs    string
	Key      string
	World    string
	Cond     string
	Chance   int
	Disabled bool
	Def      string // the /def line that recreates it
}

type macroStore struct {
	list []*Macro
	next int
}

func newMacroStore() *macroStore { return &macroStore{next: 1} }

func (s *macroStore) byName(name string) *Macro {
	for _, m := range s.list {
		if m.Name == name && !m.Disabled {
			return m
		}
	}
	return nil
}

func (s *macroStore) anyByName(name string) *Macro {
	for _, m := range s.list {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// add inserts m, replacing any macro with the same name; it reports whether
// it replaced one.
func (s *macroStore) add(m *Macro) (replaced bool) {
	if m.Name != "" {
		for k, o := range s.list {
			if o.Name == m.Name {
				o.deleted = true
				s.list = append(s.list[:k], s.list[k+1:]...)
				replaced = true
				break
			}
		}
	}
	m.Num = s.next
	s.next++
	if m.Name == "" {
		m.Name = "#" + strconv.Itoa(m.Num)
	}
	m.left = m.Shots
	s.list = append(s.list, m)
	return replaced
}

func (s *macroStore) remove(m *Macro) {
	for k, o := range s.list {
		if o == m {
			o.deleted = true
			s.list = append(s.list[:k], s.list[k+1:]...)
			return
		}
	}
}

// matching returns macros passing pred, ordered by priority (high first) then
// definition order.
func (s *macroStore) matching(pred func(*Macro) bool) []*Macro {
	var out []*Macro
	for _, m := range s.list {
		if !m.Disabled && pred(m) {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Priority > out[b].Priority })
	return out
}

func worldOK(m *Macro, world string) bool { return m.World == "" || m.World == world }

// ---- /def parsing -----------------------------------------------------------

func (i *Interp) parseDef(args string) (*Macro, error) {
	m := &Macro{Mode: i.getVarStr("matching")}
	if m.Mode != ModeSimple && m.Mode != ModeRegexp {
		m.Mode = ModeGlob
	}
	rest := strings.TrimLeftFunc(args, unicode.IsSpace)
	named := false
	for rest != "" {
		switch {
		case rest[0] == '=':
			m.Body = strings.TrimLeftFunc(rest[1:], unicode.IsSpace)
			rest = ""
		case rest[0] == '-' && len(rest) > 1 && rest[1] != ' ':
			c := rest[1]
			rest = rest[2:]
			var val string
			if strings.IndexByte("tmhpnaPbkwEcT", c) >= 0 {
				var err error
				if val, rest, err = optArg(rest); err != nil {
					return nil, fmt.Errorf("/def -%c: %w", c, err)
				}
			}
			if err := m.setOpt(c, val); err != nil {
				return nil, err
			}
		default:
			end := strings.IndexFunc(rest, func(r rune) bool { return unicode.IsSpace(r) || r == '=' })
			if end < 0 {
				end = len(rest)
			}
			if named {
				return nil, fmt.Errorf("/def: unexpected %q (use '=' before the body)", rest[:end])
			}
			m.Name = rest[:end]
			named = true
			rest = rest[end:]
		}
		rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	}
	if m.Name != "" && strings.ContainsAny(m.Name, "/ \t") {
		return nil, fmt.Errorf("/def: bad macro name %q", m.Name)
	}
	if m.Trigger != "" && m.Hook != "" {
		return nil, fmt.Errorf("/def: -t and -h cannot be combined")
	}
	return m, m.compile()
}

func (m *Macro) setOpt(c byte, v string) error {
	switch c {
	case 't':
		m.Trigger = v
	case 'm':
		switch strings.ToLower(v) {
		case ModeGlob, ModeSimple, ModeRegexp:
			m.Mode = strings.ToLower(v)
		default:
			return fmt.Errorf("/def -m: unknown match mode %q (glob, simple, regexp)", v)
		}
	case 'h':
		name, pat := firstWord(v)
		name = strings.ToUpper(name)
		if !isHook(name) {
			return fmt.Errorf("/def -h: unknown hook %q", name)
		}
		m.Hook, m.HookPat = name, pat
	case 'p', 'n', 'c':
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 0 && c != 'p' {
			return fmt.Errorf("/def -%c: bad number %q", c, v)
		}
		switch c {
		case 'p':
			m.Priority = n
		case 'n':
			m.Shots = n
		case 'c':
			if n > 100 {
				return fmt.Errorf("/def -c: chance must be 0-100")
			}
			m.Chance = n
		}
	case '1':
		m.Shots = 1
	case 'F':
		m.Fall = true
	case 'g':
		m.Gag = true
	case 'i':
		m.Invisible = true
	case 'a':
		a, err := ParseAttr(v)
		if err != nil {
			return fmt.Errorf("/def -a: %w", err)
		}
		m.Attr = a
		if a.Gag {
			m.Gag = true
		}
	case 'P':
		k := 0
		for k < len(v) && isDigit(v[k]) {
			k++
		}
		if k == 0 {
			return fmt.Errorf("/def -P: expected part number, e.g. -P1Cred")
		}
		n, _ := strconv.Atoi(v[:k])
		a, err := ParseAttr(v[k:])
		if err != nil {
			return fmt.Errorf("/def -P: %w", err)
		}
		m.parts = append(m.parts, partAttr{n, a})
	case 'b', 'k':
		m.Key = v
	case 'w':
		m.World = v
	case 'E':
		m.Cond = v
	case 's', 'T':
	default:
		return fmt.Errorf("/def: unknown option -%c", c)
	}
	return nil
}

func (m *Macro) compile() error {
	m.glob, m.re = nil, nil
	if m.Trigger == "" {
		return nil
	}
	switch m.Mode {
	case ModeRegexp:
		re, err := regexp.Compile(m.Trigger)
		if err != nil {
			return fmt.Errorf("/def: bad regexp %q: %v", m.Trigger, err)
		}
		m.re = re
	case ModeGlob:
		m.glob = compileGlob(m.Trigger)
	}
	return nil
}

// match tests text against the trigger.
func (m *Macro) match(text string) (*matchInfo, bool) {
	switch {
	case m.re != nil:
		idx := m.re.FindStringSubmatchIndex(text)
		if idx == nil {
			return nil, false
		}
		mi := &matchInfo{text: text, start: idx[0], end: idx[1], left: text[:idx[0]], right: text[idx[1]:]}
		for k := 0; k+1 < len(idx); k += 2 {
			if idx[k] < 0 {
				mi.parts = append(mi.parts, "")
				mi.idx = append(mi.idx, [2]int{-1, -1})
			} else {
				mi.parts = append(mi.parts, text[idx[k]:idx[k+1]])
				mi.idx = append(mi.idx, [2]int{idx[k], idx[k+1]})
			}
		}
		return mi, true
	case m.glob != nil:
		ok, caps := m.glob.match(text)
		if !ok {
			return nil, false
		}
		mi := &matchInfo{text: text, start: 0, end: len(text), parts: []string{text}, idx: [][2]int{{0, len(text)}}}
		for _, c := range caps {
			mi.parts = append(mi.parts, text[c[0]:c[1]])
			mi.idx = append(mi.idx, c)
		}
		return mi, true
	case m.Mode == ModeSimple:
		if text != m.Trigger {
			return nil, false
		}
		return &matchInfo{text: text, end: len(text), parts: []string{text}, idx: [][2]int{{0, len(text)}}}, true
	}
	return nil, false
}

// ---- formatting -------------------------------------------------------------

func quoteOpt(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"{}()=") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
}

// Def returns a /def line that recreates the macro.
func (m *Macro) Def() string {
	var b strings.Builder
	b.WriteString("/def")
	opt := func(c byte, v string) { fmt.Fprintf(&b, " -%c%s", c, quoteOpt(v)) }
	if m.Trigger != "" {
		if m.Mode != ModeGlob {
			opt('m', m.Mode)
		}
		opt('t', m.Trigger)
	}
	if m.Hook != "" {
		h := m.Hook
		if m.HookPat != "" {
			h += " " + m.HookPat
		}
		opt('h', h)
	}
	if m.Priority != 0 {
		opt('p', strconv.Itoa(m.Priority))
	}
	if m.Shots != 0 {
		opt('n', strconv.Itoa(m.Shots))
	}
	if m.Fall {
		b.WriteString(" -F")
	}
	if m.Gag {
		b.WriteString(" -g")
	}
	if a := m.Attr; !a.IsZero() {
		a.Gag = false
		if !a.IsZero() {
			opt('a', a.String())
		}
	}
	for _, p := range m.parts {
		opt('P', strconv.Itoa(p.n)+p.a.String())
	}
	if m.Key != "" {
		opt('b', m.Key)
	}
	if m.World != "" {
		opt('w', m.World)
	}
	if m.Cond != "" {
		opt('E', m.Cond)
	}
	if m.Chance != 0 {
		opt('c', strconv.Itoa(m.Chance))
	}
	if m.Invisible {
		b.WriteString(" -i")
	}
	if !strings.HasPrefix(m.Name, "#") {
		b.WriteString(" " + m.Name)
	}
	b.WriteString(" = " + m.Body)
	return b.String()
}

func (m *Macro) info() MacroInfo {
	a := m.Attr
	a.Gag = false
	return MacroInfo{Num: m.Num, Name: m.Name, Body: m.Body, Trigger: m.Trigger, Mode: m.Mode,
		Hook: m.Hook, Priority: m.Priority, Shots: m.Shots, Fall: m.Fall, Gag: m.Gag,
		Attrs: a.String(), Key: m.Key, World: m.World, Cond: m.Cond, Chance: m.Chance,
		Disabled: m.Disabled, Def: m.Def()}
}

// Macros lists all macros in definition order.
func (i *Interp) Macros() []MacroInfo {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]MacroInfo, 0, len(i.macros.list))
	for _, m := range i.macros.list {
		out = append(out, m.info())
	}
	return out
}
