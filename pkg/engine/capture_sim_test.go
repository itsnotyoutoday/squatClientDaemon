package engine_test

import (
	"bytes"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/fakemush"
)

// simObj is one object of the scripted MUSH.
type simObj struct {
	name, owner, flags, typ, loc string
	attrs                        map[string]string
	noRead                       bool // get() answers #-1 PERMISSION DENIED
	noWrite                      bool // &ATTR answers "Permission denied."
}

// sim is a scripted PennMUSH-ish game on top of a fakemush peer: it answers
// think / say / @pemit me= with a tiny softcode evaluator and understands &ATTR.
type sim struct {
	t    *testing.T
	p    *fakemush.Peer
	mu   sync.Mutex
	db   map[string]*simObj
	log  []string // every command line received, in order
	xtra []string // commands that ran after a ';' in an &ATTR line (injection witnesses)
	// hooks
	override  func(cmd string) ([]string, bool) // replaces the normal reply when it returns true
	blackhole bool                              // swallow think lines (no reply)
	onCmd     func(cmd string) []string         // extra lines sent BEFORE the normal reply
	mangle    func(v string) string             // transforms values stored by &ATTR
	stop      chan struct{}
	wg        sync.WaitGroup
}

func newSimDB() map[string]*simObj {
	return map[string]*simObj{
		"#123": {name: "Test Widget", owner: "#45", flags: "WD", typ: "THING", loc: "#10", attrs: map[string]string{
			"DESC": "A shiny widget.", "MULTI": "line one%rline two%tindented", "REALNL": "first\nsecond", "EMPTY": "",
			"CMD": "$go:@pemit %#=ok;@pemit %#=again"}},
		"#200": {name: "Vault", owner: "#1", flags: "WIZARD", typ: "THING", loc: "#10", noRead: true, noWrite: true,
			attrs: map[string]string{"SECRET": "x"}},
		"#10": {name: "Lobby", owner: "#1", flags: "R", typ: "ROOM", loc: "#-1", attrs: map[string]string{}},
	}
}

// startSim attaches to the game's first connection and answers until cleanup.
func startSim(t *testing.T, srv *fakemush.Server) *sim {
	t.Helper()
	s := &sim{t: t, p: srv.Peer(), db: newSimDB(), stop: make(chan struct{})}
	s.wg.Add(1)
	go s.loop()
	t.Cleanup(func() { close(s.stop); s.wg.Wait() })
	return s
}

func stripIAC(b []byte) []byte {
	var out []byte
	for i := 0; i < len(b); i++ {
		if b[i] != 0xff {
			out = append(out, b[i])
			continue
		}
		if i+1 < len(b) && b[i+1] == 250 { // SB ... IAC SE
			for i += 2; i+1 < len(b) && !(b[i] == 0xff && b[i+1] == 240); i++ {
			}
			i++
			continue
		}
		i += 2
	}
	return out
}

func (s *sim) loop() {
	defer s.wg.Done()
	done := 0
	for {
		select {
		case <-s.stop:
			return
		case <-time.After(time.Millisecond):
		}
		txt := stripIAC(s.p.Received())
		for {
			i := bytes.IndexByte(txt[done:], '\n')
			if i < 0 {
				break
			}
			line := strings.TrimSuffix(string(txt[done:done+i]), "\r")
			done += i + 1
			s.handle(line)
		}
	}
}

func (s *sim) handle(cmd string) {
	s.mu.Lock()
	s.log = append(s.log, cmd)
	hook, black, ov := s.onCmd, s.blackhole, s.override
	s.mu.Unlock()
	if hook != nil {
		for _, l := range hook(cmd) {
			s.p.SendLine(l)
		}
	}
	if ov != nil {
		if out, ok := ov(cmd); ok {
			for _, l := range out {
				s.p.SendLine(l)
			}
			return
		}
	}
	for _, l := range s.reply(cmd, black) {
		s.p.SendLine(l)
	}
}

func (s *sim) reply(cmd string, black bool) []string {
	for _, pre := range []string{"think ", "say ", "@pemit me="} {
		if !strings.HasPrefix(cmd, pre) {
			continue
		}
		if black {
			return nil
		}
		out := strings.Split(s.eval(cmd[len(pre):]), "\n")
		if pre == "say " {
			return []string{"You say, \"" + strings.Join(out, " ") + "\""}
		}
		return out
	}
	if strings.HasPrefix(cmd, "&") {
		return s.set(cmd)
	}
	if strings.HasPrefix(cmd, "SECRETSQUAT") {
		return nil
	}
	return []string{"Huh?  (Type \"help\" for help.)"}
}

func (s *sim) set(cmd string) []string {
	head, rest, _ := strings.Cut(cmd[1:], "=")
	attr, obj, _ := strings.Cut(head, " ")
	val, braced := rest, false
	if strings.HasPrefix(val, "{") {
		// strip one balanced group when it spans the value
		depth := 0
		for i := 0; i < len(val); i++ {
			if val[i] == '{' {
				depth++
			} else if val[i] == '}' {
				depth--
				if depth == 0 {
					if i == len(val)-1 {
						val, braced = val[1:i], true
					}
					break
				}
			}
		}
	}
	if !braced {
		if i := strings.IndexByte(val, ';'); i >= 0 {
			s.mu.Lock()
			s.xtra = append(s.xtra, val[i+1:])
			s.mu.Unlock()
			val = val[:i]
		}
		val = strings.TrimLeft(val, " ")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.db[obj]
	if o == nil {
		return []string{"I don't see that here."}
	}
	if o.noWrite {
		return []string{"Permission denied."}
	}
	if s.mangle != nil {
		val = s.mangle(val)
	}
	if val == "" {
		delete(o.attrs, strings.ToUpper(attr))
		return []string{"Cleared."}
	}
	o.attrs[strings.ToUpper(attr)] = val
	return []string{"Set."}
}

func (s *sim) attrValue(obj, attr string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.db[obj]
	if o == nil {
		return "", false
	}
	v, ok := o.attrs[strings.ToUpper(attr)]
	return v, ok
}

func (s *sim) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}

func (s *sim) extra() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.xtra...)
}

// ---- softcode evaluator (just enough) ----

func (s *sim) eval(t string) string {
	var b strings.Builder
	for i := 0; i < len(t); i++ {
		switch t[i] {
		case '[':
			j := matchClose(t, i)
			b.WriteString(s.call(t[i+1 : j]))
			i = j
		case '%':
			if i+1 < len(t) {
				i++
				switch t[i] {
				case 'r':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
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

// matchClose returns the index of the bracket closing t[i].
func matchClose(t string, i int) int {
	d := 0
	for j := i; j < len(t); j++ {
		switch t[j] {
		case '[', '(':
			d++
		case ']', ')':
			if d--; d == 0 {
				return j
			}
		}
	}
	return len(t) - 1
}

func splitArgs(a string) []string {
	var out []string
	d, start := 0, 0
	for i := 0; i < len(a); i++ {
		switch a[i] {
		case '(', '[':
			d++
		case ')', ']':
			d--
		case ',':
			if d == 0 {
				out = append(out, a[start:i])
				start = i + 1
			}
		}
	}
	return append(out, a[start:])
}

// call evaluates "fn(args)".
func (s *sim) call(expr string) string {
	p := strings.IndexByte(expr, '(')
	if p < 0 || !strings.HasSuffix(expr, ")") {
		return s.eval(expr)
	}
	fn := strings.ToLower(expr[:p])
	var args []string
	for _, a := range splitArgs(expr[p+1 : len(expr)-1]) {
		if q := strings.IndexByte(a, '('); q > 0 && strings.HasSuffix(a, ")") && !strings.ContainsAny(a[:q], " /#") {
			args = append(args, s.call(a))
		} else {
			args = append(args, s.eval(a))
		}
	}
	for len(args) < 3 {
		args = append(args, "")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ref := func(a string) *simObj {
		if a == "me" || a == "here" {
			a = "#123"
		}
		return s.db[a]
	}
	const noObj = "#-1 NO MATCH"
	switch fn {
	case "num":
		if ref(args[0]) == nil {
			return noObj
		}
		if args[0] == "me" || args[0] == "here" {
			return "#123"
		}
		return args[0]
	case "name", "owner", "flags", "type", "loc":
		o := ref(args[0])
		if o == nil {
			return noObj
		}
		return map[string]string{"name": o.name, "owner": o.owner, "flags": o.flags, "type": o.typ, "loc": o.loc}[fn]
	case "lattr":
		o := ref(args[0])
		if o == nil {
			return noObj
		}
		var n []string
		for k := range o.attrs {
			n = append(n, k)
		}
		sort.Strings(n)
		return strings.Join(n, " ")
	case "hasattr":
		o := ref(args[0])
		if o == nil {
			return "#-1 NO SUCH OBJECT VISIBLE"
		}
		if _, ok := o.attrs[strings.ToUpper(args[1])]; ok {
			return "1"
		}
		return "0"
	case "get":
		obj, at, _ := strings.Cut(args[0], "/")
		o := ref(obj)
		if o == nil {
			return noObj
		}
		if o.noRead {
			return "#-1 PERMISSION DENIED"
		}
		return o.attrs[strings.ToUpper(at)]
	case "edit":
		return strings.ReplaceAll(args[0], args[1], args[2])
	}
	return "#-1 FUNCTION (" + fn + ") NOT FOUND"
}
