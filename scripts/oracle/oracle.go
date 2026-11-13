// Command oracle drives differential tests between pkg/tf and a real
// TinyFugue binary used as a black-box oracle (see docs/TF-ORACLE.md).
package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tf"
)

// Case file format (testdata/tf-oracle/NAME.tf):
//
//	; comment lines and blank lines are ignored
//	<setup lines>     run at top level, one command per line (defs, sets...)
//	---               optional separator
//	<body lines>      blank-line separated paragraphs; each paragraph is
//	                  joined with " %; " into one macro that is then run
//
// Without a "---" line every line is body. Running the body inside a macro
// is what makes %-substitution behave identically in both interpreters, and
// one paragraph per probe keeps an error in one from hiding the rest.

// GenScript turns a case into the script both interpreters execute.
// logPath, when non-empty, is where real tf logs its output.
func GenScript(src, logPath string, port int) string {
	net := strings.Contains(src, ";@net")
	var setup []string
	var paras [][]string
	var body []string
	flush := func() {
		if len(body) > 0 {
			paras = append(paras, body)
			body = nil
		}
	}
	for _, l := range strings.Split(src, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case t == "---":
			flush()
			for _, p := range paras {
				setup = append(setup, p...)
			}
			paras = nil
		case t == "":
			flush()
		case strings.HasPrefix(t, ";"):
		default:
			body = append(body, t)
		}
	}
	flush()
	var sb strings.Builder
	if logPath != "" {
		sb.WriteString("/log -g " + logPath + "\n")
	}
	sb.WriteString("/echo ORACLE_BEGIN\n")
	for _, l := range setup {
		sb.WriteString(l + "\n")
	}
	var calls []string
	for k, p := range paras {
		fmt.Fprintf(&sb, "/def -i oracle_case%d = %s\n", k, strings.Join(p, " %; "))
		calls = append(calls, fmt.Sprintf("/oracle_case%d", k))
	}
	if net {
		// The cases run from the CONNECT hook of a world connected to a
		// local dummy server, so fake_recv() has a socket to feed.
		calls = append(calls, "/echo ORACLE_END", "/log off", "/quit")
		fmt.Fprintf(&sb, "/def -i oracle_run = %s\n", strings.Join(calls, " %; "))
		// let lines the dummy server sends on connect arrive before the cases
		sb.WriteString("/def -i -hCONNECT oracle_go = /repeat -1 1 /oracle_run\n")
		fmt.Fprintf(&sb, "/addworld ow 127.0.0.1 %d\n/connect ow\n", port)
		return sb.String()
	}
	for _, c := range calls {
		sb.WriteString(c + "\n")
	}
	sb.WriteString("/echo ORACLE_END\n")
	if logPath != "" {
		sb.WriteString("/log off\n/quit\n")
	}
	return sb.String()
}

// Sends returns the ";@send TEXT" lines of a case: what the dummy server
// transmits when tf connects, and what RunOurs feeds through OnLine first.
func Sends(src string) []string {
	var out []string
	for _, l := range strings.Split(src, "\n") {
		if t, ok := strings.CutPrefix(l, ";@send "); ok {
			out = append(out, t)
		}
	}
	return out
}

// NormScreen extracts canonical output from a raw pty transcript of tf, for
// ";@screen" cases whose point is what is DISPLAYED (tf logs gagged lines, so
// the log cannot show a gag). Lines between ORACLE_BEGIN and ORACLE_END are
// kept; carriage-return/clear-line noise is dropped.
func NormScreen(raw string) string {
	var out []string
	on := false
	for _, l := range strings.Split(raw, "\n") {
		if k := strings.LastIndex(strings.TrimRight(l, "\r"), "\r"); k >= 0 {
			l = strings.TrimRight(l, "\r")[k+1:]
		}
		l = strings.TrimRight(l, " \t\r")
		switch {
		case l == "ORACLE_BEGIN":
			on = true
		case !on:
		case l == "ORACLE_END":
			return strings.Join(out, "\n") + "\nORACLE_END\n"
		case strings.HasPrefix(l, "% Trying to connect") || strings.HasPrefix(l, "% Connected to") || strings.HasPrefix(l, "% Visual mode") || strings.Contains(l, "Warning:"):
		case l == "":
			// the terminal repaints blank lines; blank echoes cannot be told apart
		default:
			out = append(out, canonLine(l))
		}
	}
	return strings.Join(out, "\n") + "\n"
}

// canonLine normalises one output line: error/warning text is replaced by a
// marker because the wording intentionally differs.
func canonLine(l string) string {
	if os.Getenv("ORACLE_RAWERR") != "" {
		return l
	}
	l = strings.TrimRight(l, " \t\r")
	if m := psRe.FindStringSubmatch(l); m != nil {
		return m[1] + " NEXT" + m[2] // time left depends on the real clock
	}
	if m := listRe.FindStringSubmatch(l); m != nil {
		return "%L: " + m[1] // /list line; the macro number is not comparable
	}
	if strings.HasPrefix(l, "% ") {
		return "%ERR"
	}
	return l
}

var psRe = regexp.MustCompile(`^( *\d+) +(?:\d+:\d\d:\d\d|[\d.]+)( [rq] .*)$`)
var listRe = regexp.MustCompile(`^(?:% )?(?:\d+: )?(/def .*)$`)
var tsRe = regexp.MustCompile(`^\d\d:\d\d:\d\d # `)

// NormTF converts real tf's log (with timestamps) to canonical output.
// Warning lines and connection progress are dropped (tf warns about style,
// we do not; ours has no socket).
func NormTF(log string) string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(log, "\n"), "\n") {
		l = tsRe.ReplaceAllString(l, "")
		l = strings.TrimRight(l, " \t\r")
		if l == "ORACLE_BEGIN" {
			continue
		}
		if strings.HasPrefix(l, "% ") && (strings.Contains(l, "Warning:") ||
			strings.HasPrefix(l, "% Trying to connect") || strings.HasPrefix(l, "% Connected to") || strings.HasPrefix(l, "% Visual mode") || strings.Contains(l, ": Redefined macro ")) {
			continue
		}
		out = append(out, canonLine(l))
	}
	return strings.Join(out, "\n") + "\n"
}

// oracleHost records echoed text.
type oracleHost struct {
	tf.NopHost
	world  string
	timers []func()
	out    []string
	now    time.Time
}

func (h *oracleHost) Echo(w string, spans []proto.Span) {
	var sb strings.Builder
	for _, s := range spans {
		sb.WriteString(s.S)
	}
	// a multi-line echo is logged by tf as separate lines
	for _, l := range strings.Split(sb.String(), "\n") {
		h.out = append(h.out, canonLine(l))
	}
}
func (h *oracleHost) SendToWorld(w, t string) error { return errors.New("not connected") }
func (h *oracleHost) AddWorld(tf.WorldInfo) error   { return nil }
func (h *oracleHost) Connect(string) error          { return nil }
func (h *oracleHost) Now() time.Time                { return h.now }
func (h *oracleHost) Schedule(d time.Duration, fn func()) func() {
	h.timers = append(h.timers, fn)
	return func() {}
}
func (h *oracleHost) ForegroundWorld() string        { return "main" }
func (h *oracleHost) Log(string, bool, string) error { return nil }

// RunOurs runs the case through pkg/tf and returns canonical output.
func RunOurs(src string) string {
	h := &oracleHost{world: "", now: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)}
	i := tf.New(h, tf.Options{Rand: func(n int64) int64 { return 0 }})
	_ = i.LoadString("oracle.tf", GenScript(src, "", 0))
	if strings.Contains(src, ";@net") {
		h.world = "ow"
		for _, t := range Sends(src) {
			res := i.OnLine("ow", proto.Line{Kind: proto.KindGame, Spans: []proto.Span{{S: t}}}, true)
			if !res.Gag {
				h.Echo("ow", res.Spans)
			}
		}
		i.FireHookWorld("ow", "CONNECT")
		for len(h.timers) > 0 { // the delayed run of the cases
			fn := h.timers[0]
			h.timers = h.timers[1:]
			fn()
		}
	}
	out := h.out
	if len(out) > 0 && out[0] == "ORACLE_BEGIN" {
		out = out[1:]
	}
	return strings.Join(out, "\n") + "\n"
}

// Diff returns a line diff summary of want (real tf) vs got (ours), "" if equal.
func Diff(want, got string) string {
	if want == got {
		return ""
	}
	w := strings.Split(strings.TrimRight(want, "\n"), "\n")
	g := strings.Split(strings.TrimRight(got, "\n"), "\n")
	var sb strings.Builder
	n := len(w)
	if len(g) > n {
		n = len(g)
	}
	for k := 0; k < n; k++ {
		var a, b string
		if k < len(w) {
			a = w[k]
		}
		if k < len(g) {
			b = g[k]
		}
		if a != b {
			fmt.Fprintf(&sb, "  line %d\n    tf:   %q\n    ours: %q\n", k+1, a, b)
		}
	}
	return sb.String()
}

var _ = sort.Strings
