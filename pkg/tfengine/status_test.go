package tfengine

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// engineCommandNames reads the list of every real tf builtin from the engine
// module's source (tfnames.go).
func engineCommandNames(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/itsnotyoutoday/gtfparser").Output()
	if err != nil {
		t.Skipf("cannot locate the engine module: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(out)), "tfnames.go"))
	if err != nil {
		t.Skip(err)
	}
	s := string(src)
	i, j := strings.Index(s, "tfCommands = []string{"), strings.Index(s, "tfFunctions")
	if i < 0 || j < i {
		t.Skip("tfnames.go has another shape")
	}
	var names []string
	for _, m := range regexp.MustCompile(`"([a-z_0-9]+)"`).FindAllStringSubmatch(s[i:j], -1) {
		names = append(names, m[1])
	}
	return names
}

// Every builtin real tf has must have a documented status.
func TestEveryRealTFCommandHasAStatus(t *testing.T) {
	have := map[string]bool{}
	for _, c := range CommandStatuses() {
		if have[c.Name] {
			t.Errorf("duplicate status for %s", c.Name)
		}
		have[c.Name] = true
		if c.Status == Unsupported && c.Note == "" {
			t.Errorf("%s: an unsupported command needs its reason", c.Name)
		}
	}
	names := engineCommandNames(t)
	if len(names) < 50 {
		t.Fatalf("only %d names read", len(names))
	}
	for _, n := range names {
		if !have[n] {
			t.Errorf("/%s (a real tf builtin) has no entry in commandStatuses", n)
		}
	}
}

// A command documented as working or mapped must not fall through to the
// engine's "not supported by this client" stub, and an unsupported one must
// say so instead of silently succeeding.
func TestStatusTableMatchesBehaviour(t *testing.T) {
	skip := map[string]bool{"sh": true, "quote": true, "lcd": true, "load": true, "require": true, "exit": true,
		"repeat": true, "kill": true, "ps": true, "restrict": true, "set": true, "setenv": true, "for": true,
		"break": true, "return": true, "eval": true, "def": true, "let": true, "test": true, "shift": true,
		"export": true, "unset": true, "loaded": true, "version": true, "send": true, "quit": true}
	for _, c := range CommandStatuses() {
		if skip[c.Name] {
			continue
		}
		e, h := newTest(t)
		_ = e.Exec("w", "/"+c.Name)
		out := h.dump()
		stub := strings.Contains(out, "not supported by this client") || strings.Contains(out, "not available in this client")
		switch c.Status {
		case Works, Mapped:
			if stub || strings.Contains(out, "no such command") {
				t.Errorf("/%s is documented as %s but: %s", c.Name, c.Status, out)
			}
		case Unsupported:
			if !stub && !strings.Contains(out, "no such command") {
				t.Errorf("/%s is unsupported but did not say so (silent): %q", c.Name, out)
			}
		}
	}
}
