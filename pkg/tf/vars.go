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
	"gag": "1", "hilite": "1", "matching": "glob", "mecho": "off", "quiet": "0",
	"sub": "off", "max_recur": "0", "histsize": "1000", "login": "1",
	"lpflag": "0", "more": "0", "wrap": "1", "wraplog": "0", "wrapspace": "0",
	"scroll": "1", "sockmload": "0", "background": "1", "bamf": "0", "borg": "1",
	"cleardone": "0", "clock": "1", "emulation": "ansi_attr", "insert": "1",
	"isize": "3", "kecho": "0", "kbnum": "", "lpquote": "0", "meta_esc": "1",
	"ansi_log": "0", "auto_fg": "1", "redef": "1", "refreshtime": "0",
	"proxy_host": "", "shpause": "0", "visual": "0", "time_format": "%H:%M",
	"TZ": "", "LANG": "en_US.UTF-8", "prompt_sec": "0", "prompt_usec": "250000",
	"textdiv": "", "version": "", "hiliteattr": "h", "max_iter": "0", "tabsize": "8",
	"quitdone": "0", "snarf": "0", "qecho": "0", "wrapsize": "0", "kprefix": "", "bg_output": "1",
	"expand_tabs": "1", "ptime": "1", "telopt": "0", "textencode": "utf-8", "backslash": "1",
	"interactive": "1", "wordpunct": "", "gethostbyname": "1", "TERM": "xterm-256color",
}

func (i *Interp) initVars() {
	for k, v := range builtinVars {
		i.vars[k] = S(v)
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
	i.vars[name] = v
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
