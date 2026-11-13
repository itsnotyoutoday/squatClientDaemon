package engine_test

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

// pobj is one object of the properties game. For an exit, loc is its
// DESTINATION and home its SOURCE room (Penn semantics).
type pobj struct {
	name, typ, owner, loc, home, parent, zone string
	flags, powers                             []string
	locks                                     map[string]string
	money                                     int
	attrs                                     map[string]string
	aflags                                    map[string][]string
	lflags                                    map[string][]string
	alocked                                   map[string]bool
	aowner                                    map[string]string
	warnings                                  string
}

var flagLetters = map[string]string{
	"WIZARD": "W", "ABODE": "A", "DARK": "D", "STICKY": "S", "SAFE": "s", "VISUAL": "V", "ROYALTY": "Z", "OPAQUE": "O",
}

// pgame is a scripted PennMUSH-ish game that understands the softcode and the
// commands of the property operations, with permission rules.
type pgame struct {
	t    *testing.T
	p    *fakemush.Peer
	mu   sync.Mutex
	db   map[string]*pobj
	me   string
	wiz  bool
	log  []string
	stop chan struct{}
	wg   sync.WaitGroup
	// hooks
	onCmd       func(cmd string) []string // extra lines BEFORE the normal reply
	override    func(cmd string) ([]string, bool)
	noLsearch   bool   // lsearch(all,...) answers #-1 PERMISSION DENIED
	catalogFmt  string // "table" (default), "paren", "dash", "none"
	atVersion   string // the reply of @version
	version     string // the reply of version() (empty: the function does not exist)
	noReplyCmds bool   // mutation commands are accepted silently but do nothing
}

func newPGame(t *testing.T, srv *fakemush.Server) *pgame {
	g := &pgame{t: t, p: srv.Peer(), me: "#45", stop: make(chan struct{}), catalogFmt: "table"}
	g.db = map[string]*pobj{
		"#10": {name: "Lobby", typ: "ROOM", owner: "#1", loc: "#-1", flags: []string{"ABODE"}, attrs: map[string]string{"DESCRIBE": "A bright lobby.%rBenches line the walls."}, locks: map[string]string{}},
		"#11": {name: "Garden", typ: "ROOM", owner: "#45", loc: "#-1", attrs: map[string]string{"DESCRIBE": "Green."}, locks: map[string]string{}},
		"#12": {name: "Garden Shed", typ: "ROOM", owner: "#9", loc: "#-1", attrs: map[string]string{}, locks: map[string]string{}},
		"#1":  {name: "Wizard", typ: "PLAYER", owner: "#1", loc: "#10", home: "#10", flags: []string{"WIZARD"}, attrs: map[string]string{}, locks: map[string]string{}},
		"#45": {name: "Nveid", typ: "PLAYER", owner: "#45", loc: "#10", home: "#10", money: 120, attrs: map[string]string{}, locks: map[string]string{}},
		"#123": {name: "Widget", typ: "THING", owner: "#45", loc: "#10", home: "#10", parent: "#130", zone: "#131", money: 5,
			flags: []string{"DARK", "SAFE"}, powers: []string{"SEE_ALL"},
			locks: map[string]string{"Basic": "#45|flag^WIZARD", "Use": "=#45"}, lflags: map[string][]string{"Basic": {"visual", "no_inherit"}},
			attrs: map[string]string{"DESC": "A widget.", "CMD": "$x:say hi"}, aflags: map[string][]string{"CMD": {"no_command"}}},
		"#130": {name: "Base Widget", typ: "THING", owner: "#45", loc: "#10", home: "#10", attrs: map[string]string{}, locks: map[string]string{}},
		"#131": {name: "Zone Master", typ: "THING", owner: "#45", loc: "#10", home: "#10", attrs: map[string]string{}, locks: map[string]string{}},
		"#200": {name: "Vault Key", typ: "THING", owner: "#1", loc: "#10", home: "#10", flags: []string{"WIZARD"}, attrs: map[string]string{}, locks: map[string]string{}},
		"#300": {name: "North;N", typ: "EXIT", owner: "#45", loc: "#11", home: "#10", attrs: map[string]string{}, locks: map[string]string{}},
		"#301": {name: "Back", typ: "EXIT", owner: "#45", loc: "#10", home: "#11", attrs: map[string]string{}, locks: map[string]string{}},
	}
	g.wg.Add(1)
	go g.loop()
	t.Cleanup(func() { close(g.stop); g.wg.Wait() })
	return g
}

func (g *pgame) commands() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.log...)
}

func (g *pgame) loop() {
	defer g.wg.Done()
	done := 0
	for {
		select {
		case <-g.stop:
			return
		case <-time.After(time.Millisecond):
		}
		txt := stripIAC(g.p.Received())
		for {
			i := bytes.IndexByte(txt[done:], '\n')
			if i < 0 {
				break
			}
			line := strings.TrimSuffix(string(txt[done:done+i]), "\r")
			done += i + 1
			g.handle(line)
		}
	}
}

func (g *pgame) handle(cmd string) {
	g.mu.Lock()
	g.log = append(g.log, cmd)
	hook, ov := g.onCmd, g.override
	g.mu.Unlock()
	if hook != nil {
		for _, l := range hook(cmd) {
			g.p.SendLine(l)
		}
	}
	if ov != nil {
		if out, ok := ov(cmd); ok {
			for _, l := range out {
				g.p.SendLine(l)
			}
			return
		}
	}
	for _, l := range g.reply(cmd) {
		g.p.SendLine(l)
	}
}

func (g *pgame) controls(d string) bool {
	o := g.db[d]
	return o != nil && (g.wiz || o.owner == g.me)
}

func has(l []string, n string) bool {
	for _, x := range l {
		if strings.EqualFold(x, n) {
			return true
		}
	}
	return false
}

func without(l []string, n string) []string {
	var out []string
	for _, x := range l {
		if !strings.EqualFold(x, n) {
			out = append(out, x)
		}
	}
	return out
}

var knownFlags = []string{"ABODE", "DARK", "ROYALTY", "SAFE", "STICKY", "VISUAL", "WIZARD", "OPAQUE"}
var knownPowers = []string{"SEE_ALL", "TPORT_ANYTHING", "NO_QUOTA"}

func (g *pgame) reply(cmd string) []string {
	if strings.HasPrefix(cmd, "think ") {
		return strings.Split(g.eval(cmd[6:]), "\n")
	}
	if cmd == "@list flags" || cmd == "@list powers" {
		return g.catalogLines(strings.TrimPrefix(cmd, "@list "))
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case strings.HasPrefix(cmd, "@set "):
		return g.cmdSet(cmd[5:])
	case strings.HasPrefix(cmd, "@power "):
		return g.cmdPower(cmd[7:])
	case strings.HasPrefix(cmd, "@name "):
		d, v, _ := strings.Cut(cmd[6:], "=")
		return g.guard(d, func(o *pobj) []string { o.name = v; return []string{"Name set."} })
	case strings.HasPrefix(cmd, "@chown "):
		d, v, _ := strings.Cut(cmd[7:], "=")
		if g.db[v] == nil {
			return []string{"I can't find that player."}
		}
		return g.guard(d, func(o *pobj) []string { o.owner = v; return []string{"Owner changed to " + g.db[v].name + "."} })
	case strings.HasPrefix(cmd, "@parent "):
		d, v, has := strings.Cut(cmd[8:], "=")
		return g.guard(d, func(o *pobj) []string {
			if !has {
				o.parent = ""
				return []string{"Parent cleared."}
			}
			if g.db[v] == nil {
				return []string{"No such object."}
			}
			o.parent = v
			return []string{"Parent changed."}
		})
	case strings.HasPrefix(cmd, "@chzone "):
		d, v, _ := strings.Cut(cmd[8:], "=")
		return g.guard(d, func(o *pobj) []string {
			if v == "none" {
				o.zone = ""
			} else if g.db[v] == nil {
				return []string{"No such object."}
			} else {
				o.zone = v
			}
			return []string{"Zone changed."}
		})
	case strings.HasPrefix(cmd, "@lock/"):
		h, v, _ := strings.Cut(cmd[6:], "=")
		ty, d, _ := strings.Cut(h, " ")
		return g.guard(d, func(o *pobj) []string { o.locks[ty] = v; return []string{"Locked."} })
	case strings.HasPrefix(cmd, "@lset "):
		h, v, _ := strings.Cut(cmd[6:], "=")
		d, ty, _ := strings.Cut(h, "/")
		return g.guard(d, func(o *pobj) []string {
			if _, ok := o.locks[ty]; !ok {
				return []string{"No such lock."}
			}
			neg := strings.HasPrefix(v, "!")
			f := strings.ToLower(strings.TrimPrefix(v, "!"))
			if f != "visual" && f != "no_inherit" && f != "no_clone" && f != "wizard" && f != "locked" {
				return []string{"Unknown lock flag."}
			}
			if o.lflags == nil {
				o.lflags = map[string][]string{}
			}
			if neg {
				o.lflags[ty] = without(o.lflags[ty], f)
				return []string{"Lock flag reset."}
			}
			if !has(o.lflags[ty], f) {
				o.lflags[ty] = append(o.lflags[ty], f)
			}
			return []string{"Lock flag set."}
		})
	case strings.HasPrefix(cmd, "@atrlock "):
		h, v, _ := strings.Cut(cmd[9:], "=")
		d, at, _ := strings.Cut(h, "/")
		return g.guard(d, func(o *pobj) []string {
			if _, ok := o.attrs[at]; !ok {
				return []string{"No such attribute."}
			}
			if o.alocked == nil {
				o.alocked = map[string]bool{}
			}
			o.alocked[at] = v == "on"
			return []string{"Attribute lock changed."}
		})
	case strings.HasPrefix(cmd, "@atrchown "):
		h, v, _ := strings.Cut(cmd[10:], "=")
		d, at, _ := strings.Cut(h, "/")
		return g.guard(d, func(o *pobj) []string {
			if g.db[v] == nil {
				return []string{"I can't find that player."}
			}
			if !g.wiz && v != g.me {
				return []string{"Permission denied."}
			}
			if o.aowner == nil {
				o.aowner = map[string]string{}
			}
			o.aowner[at] = v
			return []string{"Attribute owner changed."}
		})
	case strings.HasPrefix(cmd, "@warnings "):
		d, v, _ := strings.Cut(cmd[10:], "=")
		return g.guard(d, func(o *pobj) []string { o.warnings = v; return []string{"Warnings set."} })
	case strings.HasPrefix(cmd, "@wcheck "):
		return []string{"Warning 'exit-unlinked' for Back(#301).", "1 warning."}
	case strings.HasPrefix(cmd, "examine"):
		_, d, _ := strings.Cut(cmd, " ")
		o := g.db[d]
		if o == nil {
			return []string{"I can't see that here."}
		}
		return []string{"\x1b[1m" + o.name + "(" + d + ")\x1b[0m", "Owner: " + o.owner, "Type: " + o.typ}
	case cmd == "@version":
		if g.atVersion != "" {
			return []string{g.atVersion}
		}
		return []string{"Huh?  Type \"help\" for help."}
	case strings.HasPrefix(cmd, "&"):
		a, rest, _ := strings.Cut(cmd[1:], " ")
		d, v, hasEq := strings.Cut(rest, "=")
		return g.guard(d, func(o *pobj) []string {
			if o.alocked[a] {
				return []string{"Permission denied."}
			}
			if hasEq && v == "" {
				o.attrs[a] = ""
				return []string{"Set."}
			}
			if v == "" {
				delete(o.attrs, a)
				return []string{"Cleared."}
			}
			o.attrs[a] = v
			return []string{"Set."}
		})
	case strings.HasPrefix(cmd, "@unlock/"):
		ty, d, _ := strings.Cut(cmd[8:], " ")
		return g.guard(d, func(o *pobj) []string { delete(o.locks, ty); return []string{"Unlocked."} })
	case strings.HasPrefix(cmd, "@link "):
		d, v, _ := strings.Cut(cmd[6:], "=")
		return g.guard(d, func(o *pobj) []string {
			if r := g.db[v]; r == nil || r.typ != "ROOM" {
				return []string{"That's not a room!"}
			}
			if o.typ == "EXIT" {
				o.loc = v
			} else {
				o.home = v
			}
			return []string{"Linked."}
		})
	case strings.HasPrefix(cmd, "@tel "):
		d, v, _ := strings.Cut(cmd[5:], "=")
		return g.guard(d, func(o *pobj) []string {
			if g.db[v] == nil {
				return []string{"I can't find that."}
			}
			if v == "#200" {
				return []string{"Permission denied."}
			}
			if o.typ == "EXIT" {
				o.home = v
			} else {
				o.loc = v
			}
			return []string{"Teleported."}
		})
	}
	return []string{"Huh?  (Type \"help\" for help.)"}
}

func (g *pgame) guard(d string, f func(o *pobj) []string) []string {
	o := g.db[d]
	if o == nil {
		return []string{"I don't see that here."}
	}
	if g.noReplyCmds {
		return nil
	}
	if !g.controls(d) {
		return []string{"Permission denied."}
	}
	return f(o)
}

func (g *pgame) cmdSet(a string) []string {
	h, v, _ := strings.Cut(a, "=")
	if obj, attr, ok := strings.Cut(h, "/"); ok {
		return g.guard(obj, func(o *pobj) []string {
			neg := strings.HasPrefix(v, "!")
			f := strings.ToLower(strings.TrimPrefix(v, "!"))
			if _, has := o.attrs[attr]; !has {
				return []string{"No such attribute."}
			}
			if f != "no_command" && f != "visual" && f != "safe" && f != "no_inherit" {
				return []string{"Unknown attribute flag."}
			}
			cur := o.aflags[attr]
			if o.aflags == nil {
				o.aflags = map[string][]string{}
			}
			if neg {
				o.aflags[attr] = without(cur, f)
				return []string{"Attribute flag reset."}
			}
			if !has(cur, f) {
				o.aflags[attr] = append(cur, f)
			}
			return []string{"Attribute flag set."}
		})
	}
	return g.guard(h, func(o *pobj) []string {
		neg := strings.HasPrefix(v, "!")
		f := strings.ToUpper(strings.TrimPrefix(v, "!"))
		if !has(knownFlags, f) {
			return []string{"I don't recognize that flag."}
		}
		if f == "WIZARD" && !g.wiz {
			return []string{"Permission denied."}
		}
		if neg {
			o.flags = without(o.flags, f)
			return []string{"Flag reset."}
		}
		if !has(o.flags, f) {
			o.flags = append(o.flags, f)
		}
		return []string{"Flag set."}
	})
}

func (g *pgame) cmdPower(a string) []string {
	h, v, _ := strings.Cut(a, "=")
	return g.guard(h, func(o *pobj) []string {
		if !g.wiz {
			return []string{"Permission denied."}
		}
		neg := strings.HasPrefix(v, "!")
		f := strings.ToUpper(strings.TrimPrefix(v, "!"))
		if !has(knownPowers, f) {
			return []string{"I don't recognize that power."}
		}
		if neg {
			o.powers = without(o.powers, f)
			return []string{"Power removed."}
		}
		if !has(o.powers, f) {
			o.powers = append(o.powers, f)
		}
		return []string{"Power granted."}
	})
}

func (g *pgame) catalogLines(kind string) []string {
	names, letters := knownFlags, flagLetters
	if kind == "powers" {
		names, letters = knownPowers, map[string]string{}
	}
	switch g.catalogFmt {
	case "none":
		return []string{"Huh?  (Type \"help\" for help.)"}
	case "paren":
		var out []string
		for _, n := range names {
			out = append(out, n+"("+letters[n]+") the "+strings.ToLower(n)+" thing")
		}
		return out
	case "dash":
		var out []string
		for _, n := range names {
			out = append(out, n+" - does "+strings.ToLower(n))
		}
		return out
	}
	out := []string{"Name                  Char Type"}
	for _, n := range names {
		l := letters[n]
		if l == "" {
			l = "-"
		}
		out = append(out, fmt.Sprintf("%-21s %s    ROOM THING", n, l))
	}
	return out
}

// ---- evaluator ----

func (g *pgame) eval(t string) string {
	var b strings.Builder
	for i := 0; i < len(t); i++ {
		switch t[i] {
		case '[':
			j := matchClose(t, i)
			b.WriteString(g.call(t[i+1 : j]))
			i = j
		case '%':
			if i+1 < len(t) {
				i++
				switch t[i] {
				case 'r':
					b.WriteByte('\n')
				case 'b':
					b.WriteByte(' ')
				default:
					b.WriteByte(t[i])
				}
			}
		default:
			b.WriteByte(t[i])
		}
	}
	return b.String()
}

// evArg evaluates a function argument: a bare call like name(#1) counts as a
// call (MUSH evaluates function arguments), everything else as text.
func (g *pgame) evArg(a string) string {
	if p := strings.IndexByte(a, '('); p > 0 && strings.HasSuffix(a, ")") && !strings.ContainsAny(a[:p], " /#[]%~") && matchClose(a, p) == len(a)-1 {
		return g.call(a)
	}
	return g.eval(a)
}

func (g *pgame) call(expr string) string {
	p := strings.IndexByte(expr, '(')
	if p < 0 || !strings.HasSuffix(expr, ")") {
		return g.eval(expr)
	}
	fn := strings.ToLower(expr[:p])
	raw := splitArgs(expr[p+1 : len(expr)-1])
	arg := func(i int) string {
		if i < len(raw) {
			return g.evArg(raw[i])
		}
		return ""
	}
	switch fn {
	case "version":
		if g.version != "" {
			return g.version
		}
		return "#-1 FUNCTION (VERSION) NOT FOUND"
	case "iter":
		items := strings.Fields(arg(0))
		osep := " "
		if len(raw) > 3 && raw[3] != "" {
			osep = g.evArg(raw[3])
		}
		var out []string
		for _, it := range items {
			out = append(out, g.evArg(strings.ReplaceAll(raw[1], "##", it)))
		}
		return strings.Join(out, osep)
	case "if":
		if c := arg(0); c != "" && c != "0" {
			return arg(1)
		}
		return arg(2)
	}
	args := []string{arg(0), arg(1), arg(2)}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.fn(fn, args)
}

func (g *pgame) ref(a string) *pobj {
	if a == "me" {
		a = g.me
	}
	if a == "here" {
		a = g.db[g.me].loc
	}
	return g.db[a]
}

const noMatch = "#-1 NO MATCH"

func (g *pgame) sorted() []string {
	var ks []string
	for k := range g.db {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool {
		a, _ := strconv.Atoi(ks[i][1:])
		b, _ := strconv.Atoi(ks[j][1:])
		return a < b
	})
	return ks
}

func (g *pgame) fn(fn string, a []string) string {
	o := g.ref(a[0])
	switch fn {
	case "num":
		if o == nil {
			return noMatch
		}
		if a[0] == "me" {
			return g.me
		}
		if a[0] == "here" {
			return g.db[g.me].loc
		}
		return a[0]
	case "name":
		if o == nil {
			return noMatch
		}
		return o.name
	case "type":
		if o == nil {
			return noMatch
		}
		return o.typ
	case "owner":
		if obj, attr, ok := strings.Cut(a[0], "/"); ok {
			if g.db[obj] == nil || g.db[obj].attrs[attr] == "" {
				return "#-1 NO SUCH ATTRIBUTE"
			}
			if ao := g.db[obj].aowner[attr]; ao != "" {
				return ao
			}
			return g.db[obj].owner
		}
		if o == nil {
			return noMatch
		}
		return o.owner
	case "parent", "zone":
		if o == nil {
			return noMatch
		}
		v := o.parent
		if fn == "zone" {
			v = o.zone
		}
		if v == "" {
			return "#-1"
		}
		return v
	case "loc":
		if o == nil {
			return noMatch
		}
		return o.loc
	case "home":
		if o == nil {
			return noMatch
		}
		if o.home == "" {
			return "#-1"
		}
		return o.home
	case "money":
		if o == nil {
			return noMatch
		}
		return strconv.Itoa(o.money)
	case "lattr":
		if o == nil {
			return noMatch
		}
		var n []string
		for k := range o.attrs {
			n = append(n, k)
		}
		sort.Strings(n)
		return strings.Join(n, " ")
	case "words":
		return strconv.Itoa(len(strings.Fields(a[0])))
	case "ctime":
		if o == nil {
			return noMatch
		}
		return "Mon Jan 02 03:04:05 2023"
	case "mtime":
		if o == nil {
			return noMatch
		}
		return "Tue Feb 03 04:05:06 2024"
	case "lflags":
		if obj, attr, ok := strings.Cut(a[0], "/"); ok {
			if g.db[obj] == nil || g.db[obj].attrs[attr] == "" {
				return "#-1 NO SUCH ATTRIBUTE"
			}
			fl := append([]string(nil), g.db[obj].aflags[attr]...)
			if g.db[obj].alocked[attr] {
				fl = append(fl, "locked")
			}
			return strings.Join(fl, " ")
		}
		if o == nil {
			return noMatch
		}
		var u []string
		for _, f := range o.flags {
			u = append(u, strings.ToUpper(f))
		}
		return strings.Join(u, " ")
	case "flags":
		if obj, attr, ok := strings.Cut(a[0], "/"); ok {
			if g.db[obj] == nil || g.db[obj].attrs[attr] == "" {
				return "#-1 NO SUCH ATTRIBUTE"
			}
			var l string
			for _, f := range g.db[obj].aflags[attr] {
				l += strings.ToUpper(f[:1])
			}
			return l
		}
		if o == nil {
			return noMatch
		}
		l := map[string]string{"ROOM": "R", "THING": "", "PLAYER": "P", "EXIT": "E"}[o.typ]
		for _, f := range o.flags {
			l += flagLetters[strings.ToUpper(f)]
		}
		return l
	case "lpowers":
		if o == nil {
			return noMatch
		}
		return strings.Join(o.powers, " ")
	case "llocks":
		if o == nil {
			return noMatch
		}
		var n []string
		for k := range o.locks {
			n = append(n, k)
		}
		sort.Strings(n)
		return strings.Join(n, " ")
	case "lockflags":
		obj, ty, _ := strings.Cut(a[0], "/")
		if g.db[obj] == nil {
			return noMatch
		}
		m := map[string]string{"visual": "v", "no_inherit": "i", "no_clone": "c", "wizard": "w", "locked": "l"}
		var l string
		for _, f := range g.db[obj].lflags[ty] {
			l += m[f]
		}
		return l
	case "elock":
		obj, ty, _ := strings.Cut(a[0], "/")
		if g.db[obj] == nil || g.db[a[1]] == nil {
			return noMatch
		}
		expr, ok := g.db[obj].locks[ty]
		if !ok {
			return "#-1 NO SUCH LOCK"
		}
		return map[bool]string{true: "1", false: "0"}[strings.Contains(expr, a[1]) || expr == ""]
	case "atrlock":
		obj, at, _ := strings.Cut(a[0], "/")
		if g.db[obj] == nil {
			return noMatch
		}
		return map[bool]string{true: "1", false: "0"}[g.db[obj].alocked[at]]
	case "warnings":
		if o == nil {
			return noMatch
		}
		return o.warnings
	case "channels":
		if o == nil || o.typ != "PLAYER" {
			return "#-1 NOT A PLAYER"
		}
		return "Public Guild"
	case "lock":
		obj, ty, _ := strings.Cut(a[0], "/")
		if g.db[obj] == nil {
			return noMatch
		}
		return g.db[obj].locks[ty]
	case "lcon", "lexits":
		if o == nil {
			return noMatch
		}
		var out []string
		for _, k := range g.sorted() {
			x := g.db[k]
			if fn == "lcon" && x.typ != "EXIT" && x.typ != "ROOM" && x.loc == a[0] {
				out = append(out, k)
			}
			if fn == "lexits" && x.typ == "EXIT" && x.home == a[0] {
				out = append(out, k)
			}
		}
		return strings.Join(out, " ")
	case "strmatch":
		return map[bool]string{true: "1", false: "0"}[wild(a[0], a[1])]
	case "lsearch":
		who := a[0]
		var out []string
		for _, k := range g.sorted() {
			x := g.db[k]
			if x.typ == "ROOM" && (who == "all" && !g.noLsearch || who == "me" && x.owner == g.me) {
				out = append(out, k)
			}
		}
		if who == "all" && g.noLsearch {
			return "#-1 PERMISSION DENIED"
		}
		return strings.Join(out, " ")
	case "locate":
		var hits []string
		for _, k := range g.sorted() {
			x := g.db[k]
			if x.typ != "EXIT" && strings.EqualFold(x.name, a[1]) && (x.loc == g.db[g.me].loc || x.loc == g.me || k == g.me) {
				hits = append(hits, k)
			}
		}
		if len(hits) == 1 {
			return hits[0]
		}
		if len(hits) > 1 {
			return "#-2 AMBIGUOUS"
		}
		return noMatch
	case "controls":
		if g.db[a[1]] == nil {
			return noMatch
		}
		return map[bool]string{true: "1", false: "0"}[g.controls(a[1])]
	case "hasattr":
		if o == nil {
			return "#-1 NO SUCH OBJECT VISIBLE"
		}
		return map[bool]string{true: "1", false: "0"}[o.attrs[strings.ToUpper(a[1])] != ""]
	case "get":
		obj, at, _ := strings.Cut(a[0], "/")
		if g.db[obj] == nil {
			return noMatch
		}
		return g.db[obj].attrs[strings.ToUpper(at)]
	case "edit":
		return strings.ReplaceAll(a[0], a[1], a[2])
	case "list":
		switch a[0] {
		case "flags":
			if g.catalogFmt == "none" {
				return "#-1 FUNCTION (LIST) NOT FOUND"
			}
			return strings.Join(knownFlags, " ")
		case "powers":
			if g.catalogFmt == "none" {
				return "#-1 FUNCTION (LIST) NOT FOUND"
			}
			return strings.Join(knownPowers, " ")
		}
	}
	return "#-1 FUNCTION (" + strings.ToUpper(fn) + ") NOT FOUND"
}

// wild matches a glob with * (case-insensitive).
func wild(s, pat string) bool {
	s, pat = strings.ToLower(s), strings.ToLower(pat)
	parts := strings.Split(pat, "*")
	if len(parts) == 1 {
		return s == pat
	}
	i := 0
	for k, p := range parts {
		j := strings.Index(s[i:], p)
		if j < 0 || (k == 0 && j != 0) {
			return false
		}
		i += j + len(p)
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// ---- environments ----

type penv struct {
	e   *engine.Engine
	r   *rec
	sid string
	g   *pgame
}

func pDirect(t *testing.T) penv {
	t.Helper()
	t.Cleanup(func() { leakCheck(t) })
	srv := fakemush.New(t)
	e, r := newEngine(t)
	si := dial(t, e, r, srv, nil, "")
	return penv{e, r, si.ID, newPGame(t, srv)}
}

func pSquat(t *testing.T) penv {
	t.Helper()
	noLeaksAll(t)
	env := newSqEnv(t)
	game := fakemush.New(t)
	e, r := env.connected(t)
	si, err := e.Dial(env.world("g", game.Host(), game.Port(), nil), "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(t, e, si.ID, proto.StateConnected)
	return penv{e, r, si.ID, newPGame(t, game)}
}

func pboth(t *testing.T, f func(t *testing.T, c penv)) {
	t.Run("direct", func(t *testing.T) { f(t, pDirect(t)) })
	t.Run("squatter", func(t *testing.T) { f(t, pSquat(t)) })
}

// clean asserts that no capture text or echo reached the user's output.
func (c penv) clean(t *testing.T) {
	t.Helper()
	for _, l := range c.r.lines(c.sid) {
		if strings.Contains(l.Plain(), "SQUATCAP") || l.Kind == proto.KindEcho {
			t.Fatalf("capture leaked to the user: %q", l.Plain())
		}
	}
}

// shown is the plain text of the user's output.
func (c penv) shown() []string { return capPlains(c.r.lines(c.sid)) }
