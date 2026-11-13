package lua

import (
	"fmt"
	"regexp"
	"strings"

	glua "github.com/yuin/gopher-lua"
)

type ruleKind int

const (
	kEvent ruleKind = iota // squat.on(event, fn): line, prompt, connect, ...
	kTrigger
	kAlias
)

// rule is one registered handler, trigger or alias.
type rule struct {
	id       int
	seq      int
	kind     ruleKind
	event    string // for kEvent
	owner    string
	fn       *glua.LFunction
	priority int
	world    string

	// trigger / alias
	re     *regexp.Regexp
	gag    bool
	style  styleMask
	region string // "line" (default) or "match"

	removed bool
}

// ruleBefore reports whether a sorts before b: higher priority first, then
// registration order. This is the single, deterministic ordering rule.
func ruleBefore(a, b *rule) bool {
	if a.priority != b.priority {
		return a.priority > b.priority
	}
	return a.seq < b.seq
}

// addRule inserts ru keeping r.rules sorted. Caller holds mu.
func (r *Runtime) addRule(ru *rule) int {
	r.nextID++
	r.seq++
	ru.id, ru.seq = r.nextID, r.seq
	i := len(r.rules)
	for i > 0 && ruleBefore(ru, r.rules[i-1]) {
		i--
	}
	r.rules = append(r.rules, nil)
	copy(r.rules[i+1:], r.rules[i:])
	r.rules[i] = ru
	return ru.id
}

// remove deletes a rule or timer by id.
func (r *Runtime) remove(id int) bool {
	for i, ru := range r.rules {
		if ru.id == id {
			ru.removed = true
			r.rules = append(r.rules[:i:i], r.rules[i+1:]...)
			return true
		}
	}
	if t, ok := r.timers[id]; ok {
		t.removed = true
		if t.cancel != nil {
			t.cancel()
		}
		delete(r.timers, id)
		return true
	}
	return false
}

// removeOwner unregisters everything a script registered.
func (r *Runtime) removeOwner(name string) {
	kept := r.rules[:0:0]
	for _, ru := range r.rules {
		if ru.owner == name {
			ru.removed = true
			continue
		}
		kept = append(kept, ru)
	}
	r.rules = kept
	for id, t := range r.timers {
		if t.owner == name {
			t.removed = true
			if t.cancel != nil {
				t.cancel()
			}
			delete(r.timers, id)
		}
	}
}

// snapshot returns the rules for which keep is true, safe to iterate while
// handlers register or remove rules.
func (r *Runtime) snapshot(keep func(*rule) bool) []*rule {
	out := make([]*rule, 0, len(r.rules))
	for _, ru := range r.rules {
		if keep(ru) {
			out = append(out, ru)
		}
	}
	return out
}

// ---------------------------------------------------------------- patterns

const maxPatternLen = 4096

// compilePattern builds the matcher for a trigger or alias. Regex patterns use
// Go RE2 syntax (unanchored search). Non-regex patterns are globs matching the
// whole line: * captures any run, ? matches one character, \* and \? are
// literal.
func (r *Runtime) compilePattern(pat string, isRegex, icase bool) (*regexp.Regexp, error) {
	if len(pat) > maxPatternLen {
		return nil, fmt.Errorf("pattern longer than %d bytes", maxPatternLen)
	}
	expr := pat
	if !isRegex {
		expr = globToRegex(pat)
	}
	if icase {
		expr = "(?i)" + expr
	}
	return r.cachedRegex(expr)
}

func (r *Runtime) cachedRegex(expr string) (*regexp.Regexp, error) {
	if re, ok := r.regexes[expr]; ok {
		return re, nil
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, err
	}
	if len(r.regexes) >= 256 {
		r.regexes = map[string]*regexp.Regexp{}
	}
	r.regexes[expr] = re
	return re, nil
}

func globToRegex(g string) string {
	var sb strings.Builder
	sb.WriteString(`(?s)\A`)
	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*':
			sb.WriteString(`(.*?)`)
		case '?':
			sb.WriteString(`.`)
		case '\\':
			if i+1 < len(g) {
				i++
				sb.WriteString(regexp.QuoteMeta(string(g[i])))
			} else {
				sb.WriteString(`\\`)
			}
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	sb.WriteString(`\z`)
	return sb.String()
}

// matchTable builds the table passed to trigger/alias functions:
// m.text, m[0] (whole match), m[1..n] (captures), m.named, m.world, m.live,
// plus m.spans for triggers.
func matchTable(L *glua.LState, re *regexp.Regexp, text string, idx []int, world string, live bool) *glua.LTable {
	t := L.CreateTable(len(idx)/2, 5)
	t.RawSetString("text", glua.LString(text))
	t.RawSetString("world", glua.LString(world))
	t.RawSetString("live", glua.LBool(live))
	named := L.NewTable()
	names := re.SubexpNames()
	for g := 0; g*2+1 < len(idx); g++ {
		v := glua.LString("")
		if idx[2*g] >= 0 {
			v = glua.LString(text[idx[2*g]:idx[2*g+1]])
		}
		t.RawSetInt(g, v)
		if g > 0 && names[g] != "" {
			named.RawSetString(names[g], v)
		}
	}
	t.RawSetString("named", named)
	return t
}
