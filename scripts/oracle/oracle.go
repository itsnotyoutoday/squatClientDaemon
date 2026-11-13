// Command oracle drives differential tests between pkg/tf and a real
// TinyFugue binary used as a black-box oracle (see docs/TF-ORACLE.md).
package main

import (
	"fmt"
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
func GenScript(src, logPath string) string {
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
	for _, l := range setup {
		sb.WriteString(l + "\n")
	}
	for k, p := range paras {
		fmt.Fprintf(&sb, "/def -i oracle_case%d = %s\n/oracle_case%d\n", k, strings.Join(p, " %; "), k)
	}
	sb.WriteString("/echo ORACLE_END\n")
	if logPath != "" {
		sb.WriteString("/log off\n/quit\n")
	}
	return sb.String()
}

// canonLine normalises one output line: error/warning text is replaced by a
// marker because the wording intentionally differs.
func canonLine(l string) string {
	l = strings.TrimRight(l, " \t\r")
	if strings.HasPrefix(l, "% ") {
		return "%ERR"
	}
	return l
}

var tsRe = regexp.MustCompile(`^\d\d:\d\d:\d\d # `)

// NormTF converts real tf's log (with timestamps) to canonical output.
// Warning lines are dropped (tf warns about style, we do not).
func NormTF(log string) string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(log, "\n"), "\n") {
		l = tsRe.ReplaceAllString(l, "")
		l = strings.TrimRight(l, " \t\r")
		if strings.HasPrefix(l, "% ") && strings.Contains(l, "Warning:") {
			continue
		}
		out = append(out, canonLine(l))
	}
	return strings.Join(out, "\n") + "\n"
}

// oracleHost records echoed text.
type oracleHost struct {
	tf.NopHost
	out    []string
	now    time.Time
	timers []func()
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
func (h *oracleHost) Now() time.Time { return h.now }
func (h *oracleHost) Schedule(d time.Duration, fn func()) func() {
	return func() {}
}
func (h *oracleHost) ForegroundWorld() string        { return "main" }
func (h *oracleHost) Log(string, bool, string) error { return nil }

// RunOurs runs the case through pkg/tf and returns canonical output.
func RunOurs(src string) string {
	h := &oracleHost{now: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)}
	i := tf.New(h, tf.Options{Rand: func(n int64) int64 { return 0 }})
	_ = i.LoadString("oracle.tf", GenScript(src, ""))
	return strings.Join(h.out, "\n") + "\n"
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
