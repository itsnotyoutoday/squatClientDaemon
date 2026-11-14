package tfengine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tfLibDir finds a copy of real tf's lib/tf (TF_LIB), or skips.
func tfLibDir(t *testing.T) string {
	t.Helper()
	for _, d := range []string{os.Getenv("TF_LIB"), os.Getenv("GTF_TF_LIB"), "/Users/rock/mush/tinyfugue/lib/tf"} {
		if d != "" {
			if _, err := os.Stat(filepath.Join(d, "stdlib.tf")); err == nil {
				return d
			}
		}
	}
	t.Skip("no copy of real tf's lib/tf (set TF_LIB)")
	return ""
}

// Real tf's own library (lib/tf, unmodified) must load and run through the
// client's engine: nothing in it may hit a missing command or function.
func TestRealLibraryLoadsThroughTheClientEngine(t *testing.T) {
	dir := tfLibDir(t)
	files := []string{"stdlib.tf", "tools.tf", "textutil.tf", "alias.tf", "grep.tf", "at.tf", "color.tf", "factoral.tf",
		"hanoi.tf", "spedwalk.tf", "stack-q.tf", "world-q.tf", "kbfunc.tf", "self.tf", "relog.tf", "tick.tf", "watch.tf",
		"textencode.tf", "complete.tf", "quoter.tf"}
	for _, f := range files {
		e, h := newTest(t, func(o *Options) { o.LibDirs = []string{dir} })
		if err := e.LoadFile(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		for _, l := range strings.Split(h.dump(), "\n") {
			if strings.Contains(l, "no such") || strings.Contains(l, "not supported by this client") || strings.Contains(l, "invalid") {
				if strings.Contains(l, "local.tf") { // stdlib loads an optional per-user file
					continue
				}
				t.Errorf("%s: %s", f, l)
			}
		}
	}
}

// lib/tf/alias.tf is built from a SEND hook with the gag attribute: the alias
// replaces the text instead of adding to it.
func TestAliasLibraryOnSendHook(t *testing.T) {
	dir := tfLibDir(t)
	e, h := newTest(t, func(o *Options) { o.LibDirs = []string{dir} })
	for _, f := range []string{"stdlib.tf", "alias.tf"} { // real tf loads stdlib.tf itself at start
		if err := e.LoadFile(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	_ = e.Exec("w", "/alias hi say hello %1")
	_ = e.Exec("w", "hi there")
	_ = e.Exec("w", "other text")
	got := h.sentTexts()
	if len(got) != 2 || got[0] != "w:say hello there" || got[1] != "w:other text" {
		t.Fatalf("sent %v", got)
	}
	_ = e.Exec("w", "/unalias hi")
	_ = e.Exec("w", "hi again")
	if got := h.sentTexts(); got[len(got)-1] != "w:hi again" {
		t.Fatalf("after /unalias: %v", got)
	}
}
