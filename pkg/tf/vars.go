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
	"textdiv": "", "version": "",
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

// lookup resolves a name: positional parameters, match parts, locals, globals.
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
		case name == "L" && fr.match != nil:
			return S(fr.match.left), true
		case name == "R" && fr.match != nil:
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
		if v, ok := fr.locals[name]; ok {
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
	if len(i.frames) > 0 {
		if fr := i.top(); fr.locals != nil {
			if _, ok := fr.locals[name]; ok {
				fr.locals[name] = v
				return nil
			}
		}
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
