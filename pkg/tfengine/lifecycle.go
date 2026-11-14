package tfengine

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/itsnotyoutoday/gtfparser"
)

// start runs once, on the interpreter goroutine, before the first call: it
// loads real tf's standard library when one is configured, the way tf loads
// stdlib.tf itself at start-up.
func (a *Engine) start(in *gtfparser.Interp) {
	a.syncWorlds(in)
	if a.opts.StdLib == "" {
		return
	}
	p := a.opts.StdLib
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		p = filepath.Join(p, "stdlib.tf")
	}
	if _, err := os.Stat(p); err != nil {
		return
	}
	a.muted = true
	in.SetVar("TFLIBDIR", filepath.Dir(p))
	if err := in.LoadFile(p); err != nil {
		a.warn("stdlib.tf: " + err.Error())
	}
	a.muted = false
	a.pushKeys(in)
}

// afterCall is the housekeeping every call ends with.
func (a *Engine) afterCall(in *gtfparser.Interp) {
	in.PollVars() // the engine reports variable changes (wrap, status_*, ...) at its entry points and here
	a.reconcileWorlds(in)
	if a.statusDirty {
		a.statusDirty = false
		a.pushStatus(in)
	}
	a.pushKeys(in)
	if in.Quit {
		in.Quit = false
		a.host.Quit()
	}
	if !a.inKey {
		// text a script put into the input buffer (/input) goes to the input line
		if b := in.Input(); b.Len() > 0 {
			t := b.Text()
			b.Clear()
			a.host.Input(t)
		}
	}
}

// pushStatus renders the status area and hands the named fields to the host.
func (a *Engine) pushStatus(in *gtfparser.Interp) {
	cols, _ := a.host.Screen()
	var out []StatusField
	for _, row := range in.StatusRows(cols) {
		for _, sp := range row.Spans {
			if sp.Field.Kind == gtfparser.StatusLiteral || sp.Field.Kind == gtfparser.StatusPad {
				continue
			}
			out = append(out, StatusField{Name: sp.Field.Name, Text: strings.TrimSpace(sp.Text), Attrs: sp.Attrs})
		}
	}
	a.host.SetStatus(out)
}

// pushKeys tells the host when the set of bound keys changed.
func (a *Engine) pushKeys(in *gtfparser.Interp) {
	var seqs []string
	for _, b := range in.Bindings() {
		seqs = append(seqs, gtfparser.AsciiToPrint(b.Seq))
	}
	sig := strings.Join(seqs, "\x00")
	if sig == a.keySig {
		return
	}
	a.keySig = sig
	a.host.KeyBindings(seqs)
}
