package tf

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// builtinVars are the behavioural variables the interpreter itself reads or
// that scripts commonly expect to exist. Others from TF's list are accepted
// as ordinary variables (see docs/TF-COMPAT.md).
var builtinVars = map[string]string{
	"gag": "on", "hilite": "on", "matching": "glob", "mecho": "off", "quiet": "off",
	"sub": "off", "max_recur": "100", "histsize": "1000", "login": "on",
	"lpflag": "off", "more": "off", "wrap": "on", "wraplog": "off", "wrapspace": "4",
	"scroll": "off", "sockmload": "off", "background": "on", "bamf": "off", "borg": "on",
	"cleardone": "off", "clock": "on", "emulation": "ansi_attr", "insert": "on",
	"isize": "3", "kecho": "off", "kbnum": "", "lpquote": "off", "meta_esc": "nonprint",
	"ansi_log": "off", "auto_fg": "on", "redef": "on", "refreshtime": "100000",
	"proxy_host": "", "shpause": "on", "visual": "off", "time_format": "%H:%M",
	"TZ": "", "LANG": "en_US.UTF-8", "prompt_sec": "0", "prompt_usec": "250000",
	"textdiv": "on", "version": "", "hiliteattr": "B", "max_iter": "0", "tabsize": "8",
	"quitdone": "off", "snarf": "off", "qecho": "off", "wrapsize": "79", "kprefix": "", "bg_output": "on",
	"expand_tabs": "on", "ptime": "1.0", "telopt": "off", "textencode": "utf-8", "backslash": "on",
	"interactive": "off", "wordpunct": "_", "gethostbyname": "nonblocking", "TERM": "xterm-256color",
	"hook": "on", "max_trig": "1000", "max_hook": "1000", "gpri": "0", "hpri": "0", "maildelay": "0:01",
	"atcp": "off", "gmcp": "off", "warn_curly_re": "on", "oldslash": "on", "defcompile": "off",
	"status_pad": "_", "status_height": "1",
}

// flagVars are TF's on/off ("enumerated") variables. Assigning 1/on (or any
// non-zero number) stores "on"; 0/off stores "off". In an expression their
// numeric value is 1 or 0 and their text is "on" or "off".
var flagVars = map[string]bool{
	"gag": true, "hilite": true, "mecho": true, "quiet": true, "login": true, "lpflag": true,
	"more": true, "wrap": true, "wraplog": true, "scroll": true, "sockmload": true, "background": true,
	"bamf": true, "borg": true, "cleardone": true, "clock": true, "insert": true, "kecho": true,
	"lpquote": true, "ansi_log": true, "auto_fg": true, "redef": true, "shpause": true, "visual": true,
	"textdiv": true, "quitdone": true, "snarf": true, "qecho": true, "bg_output": true,
	"expand_tabs": true, "telopt": true, "backslash": true, "interactive": true, "hook": true,
	"atcp": true, "gmcp": true, "warn_curly_re": true, "oldslash": true, "defcompile": true,
}

// flagValue normalises v for a flag variable.
func flagValue(v Value) Value {
	on := false
	switch strings.ToLower(strings.TrimSpace(v.s)) {
	case "on", "yes", "true":
		on = true
	case "off", "no", "false", "":
	default:
		n, _ := numPrefix(v)
		on = n.Float() != 0
	}
	if on {
		return Value{s: "on", num: true, isInt: true, i: 1, f: 1}
	}
	return Value{s: "off", num: true, isInt: true}
}

// flag reports whether a flag variable is on.
func (i *Interp) flag(name string) bool {
	v, ok := i.vars[name]
	return ok && flagValue(v).i == 1
}

func (i *Interp) initVars() {
	for k, v := range builtinVars {
		i.vars[k] = S(v)
		if flagVars[k] {
			i.vars[k] = flagValue(S(v))
		}
	}
	i.vars["version"] = S(i.opts.Version)
}

func (i *Interp) getVarStr(name string) string {
	v, _ := i.lookup(name)
	return v.s
}

var partRe = regexp.MustCompile(`^P[0-9]+$`)
var lastRe = regexp.MustCompile(`^-?L[0-9]*$`)
var skipRe = regexp.MustCompile(`^-[0-9]+$`)

// lookup resolves a name: positional parameters and their selectors (%*, %#,
// %0, %N, %-N, %L, %Ln, %-L, %R), match parts (%P0.., %PL, %PR), %?,
// locals, globals.
func (i *Interp) lookup(name string) (Value, bool) {
	var fr *frame
	if len(i.frames) > 0 {
		fr = i.top()
	}
	if fr != nil {
		switch {
		case name == "*":
			return S(fr.args), true
		case name == "#":
			return I(int64(len(fr.params))), true
		case name == "0":
			return S(fr.name), true
		case name == "?":
			return i.last, true
		case name == "R":
			if len(fr.params) == 0 {
				return Value{}, false
			}
			r := int64(0)
			if i.rng != nil {
				r = i.rng(int64(len(fr.params)))
			}
			return S(fr.params[r]), true
		case lastRe.MatchString(name):
			// L, L2: the last, second-to-last... parameter; -L, -L2: all
			// except the last n.
			neg := name[0] == '-'
			d := strings.TrimPrefix(strings.TrimPrefix(name, "-"), "L")
			n := 1
			if d != "" {
				n, _ = strconv.Atoi(d)
			}
			np := len(fr.params)
			if neg {
				if n >= np {
					return Value{}, false
				}
				return S(strings.Join(fr.params[:np-n], " ")), true
			}
			if n < 1 || n > np {
				return Value{}, false
			}
			return S(fr.params[np-n]), true
		case skipRe.MatchString(name):
			n, _ := strconv.Atoi(name[1:])
			if n >= len(fr.params) {
				return Value{}, false
			}
			return S(strings.Join(fr.params[n:], " ")), true
		case name == "PL" || name == "PR":
			if fr.match == nil {
				return Value{}, false
			}
			if name == "PL" {
				return S(fr.match.left), true
			}
			return S(fr.match.right), true
		case partRe.MatchString(name):
			if fr.match != nil {
				if n, err := strconv.Atoi(name[1:]); err == nil && n < len(fr.match.parts) {
					return S(fr.match.parts[n]), true
				}
			}
			return Value{}, false
		}
		if name != "" && isDigit(name[0]) {
			if n, err := strconv.Atoi(name); err == nil && n >= 1 && n <= len(fr.params) {
				return S(fr.params[n-1]), true
			}
			return Value{}, false
		}
	}
	// Locals are dynamically scoped: the nearest running macro (outward
	// through its callers) that /let the name wins over a global.
	for k := len(i.frames) - 1; k >= 0; k-- {
		if v, ok := i.frames[k].locals[name]; ok {
			return v, true
		}
	}
	v, ok := i.vars[name]
	return v, ok
}

// setVar assigns to the innermost local of that name, else the global.
func (i *Interp) setVar(name string, v Value) error {
	if !validName(name) {
		return fmt.Errorf("invalid variable name %q", name)
	}
	if len(v.s) > maxValueLen {
		return fmt.Errorf("value too long")
	}
	for k := len(i.frames) - 1; k >= 0; k-- {
		if _, ok := i.frames[k].locals[name]; ok {
			i.frames[k].locals[name] = v
			return nil
		}
	}
	return i.setGlobal(name, v)
}

// setGlobal assigns a global variable (what /set does, even when a local of
// the same name shadows it).
func (i *Interp) setGlobal(name string, v Value) error {
	if !validName(name) {
		return fmt.Errorf("invalid variable name %q", name)
	}
	if len(v.s) > maxValueLen {
		return fmt.Errorf("value too long")
	}
	if !i.live {
		return fmt.Errorf("global assignment is not allowed during replay")
	}
	if flagVars[name] {
		v = flagValue(v)
	}
	i.vars[name] = v
	if name == "visual" {
		if u, ok := i.host.(UIControl); ok {
			u.SetVisual(v.i == 1)
		}
	}
	return nil
}

func (i *Interp) setLocal(name string, v Value) error {
	if !validName(name) {
		return fmt.Errorf("invalid variable name %q", name)
	}
	if len(v.s) > maxValueLen {
		return fmt.Errorf("value too long")
	}
	fr := i.top()
	if fr.locals == nil {
		fr.locals = map[string]Value{}
	}
	fr.locals[name] = v
	return nil
}

// ---- public variable API ----------------------------------------------------

// Vars returns a copy of all global variables as strings.
func (i *Interp) Vars() map[string]string {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make(map[string]string, len(i.vars))
	for k, v := range i.vars {
		out[k] = v.s
	}
	return out
}

// GetVar returns a global variable.
func (i *Interp) GetVar(name string) (string, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	v, ok := i.vars[name]
	return v.s, ok
}

// SetVar sets a global variable (empty value is allowed).
func (i *Interp) SetVar(name, value string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !validName(name) {
		return fmt.Errorf("invalid variable name %q", name)
	}
	i.vars[name] = S(value)
	return nil
}

type varDump struct {
	Vars     map[string]string `json:"vars"`
	Exported []string          `json:"exported,omitempty"`
}

// ExportVars serialises the global variable store (and the exported set) to JSON.
func (i *Interp) ExportVars() ([]byte, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	d := varDump{Vars: map[string]string{}}
	for k, v := range i.vars {
		d.Vars[k] = v.s
	}
	for k := range i.exported {
		d.Exported = append(d.Exported, k)
	}
	sort.Strings(d.Exported)
	return json.MarshalIndent(d, "", "  ")
}

// ImportVars replaces the global variable store from ExportVars output.
// Built-in defaults that are absent from the data are restored.
func (i *Interp) ImportVars(data []byte) error {
	var d varDump
	if err := json.Unmarshal(data, &d); err != nil {
		return fmt.Errorf("tf: import vars: %w", err)
	}
	for k := range d.Vars {
		if !validName(k) {
			return fmt.Errorf("tf: import vars: invalid name %q", k)
		}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.vars = map[string]Value{}
	i.initVars()
	for k, v := range d.Vars {
		i.vars[k] = S(v)
	}
	i.exported = map[string]bool{}
	for _, k := range d.Exported {
		i.exported[k] = true
	}
	return nil
}

// listVars returns sorted names of globals matching glob ("" = all).
func (i *Interp) listVars(glob string) []string {
	var out []string
	for k := range i.vars {
		if glob == "" || globMatch(glob, k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func quoteVal(v Value) string {
	if strings.ContainsAny(v.s, "\n") {
		return strconv.Quote(v.s)
	}
	return v.s
}
