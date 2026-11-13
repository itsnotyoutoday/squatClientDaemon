package tf

import (
	"fmt"
	"strings"
)

func (i *Interp) initMiscFuncs() {
	i.def("eval", 1, 1, func(i *Interp, a []Value) (Value, error) {
		ex, err := i.expand(a[0].s)
		if err != nil {
			return Value{}, err
		}
		out, err := i.captureCmd(ex)
		return S(out), err
	})
	i.def("test", 1, 1, func(i *Interp, a []Value) (Value, error) { return i.evalExprText(a[0].s) })
	i.def("whatis", 1, 1, func(_ *Interp, a []Value) (Value, error) { return S(a[0].typeName()), nil })
	i.def("getopts", 1, 2, fnGetopts)
	i.def("echo", 1, 4, func(i *Interp, a []Value) (Value, error) {
		if !i.live {
			return I(0), nil
		}
		var attr Attr
		if len(a) > 1 && a[1].s != "" {
			var err error
			if attr, err = ParseAttr(a[1].s); err != nil {
				return Value{}, err
			}
		}
		spans := []protoSpan{attr.Apply(protoSpan{S: a[0].s})}
		if len(a) > 2 && a[2].Truth() {
			spans = SpansFromMarkup(a[0].s, attr)
		}
		w := i.curWorld()
		if len(a) > 3 && a[3].s != "" {
			w = a[3].s
		}
		i.echoSpans(w, spans)
		return I(1), nil
	})
	// fake_recv(text [, world]): process text as if received from the world:
	// triggers run, and the line is displayed unless a trigger gags it.
	i.def("fake_recv", 1, 3, func(i *Interp, a []Value) (Value, error) {
		w := i.curWorld()
		if len(a) > 1 && a[1].s != "" {
			w = a[1].s
		}
		spans := []protoSpan{{S: a[0].s}}
		lc := i.dispatch(w, a[0].s, spans, -1<<31, nil)
		if !lc.gag {
			i.echoSpans(w, lc.spans)
		}
		return I(1), nil
	})
	i.def("send", 1, 2, func(i *Interp, a []Value) (Value, error) {
		if !i.live {
			return I(0), nil
		}
		w := i.curWorld()
		if len(a) > 1 && a[1].s != "" {
			w = a[1].s
		}
		return B(i.sendText(w, a[0].s) == nil), nil
	})
	i.def("prompt", 1, 1, func(i *Interp, a []Value) (Value, error) {
		if i.live {
			i.host.SetPrompt(i.curWorld(), a[0].s)
		}
		return I(1), nil
	})
	i.def("world_info", 0, 2, func(i *Interp, a []Value) (Value, error) {
		w, field := i.curWorld(), "name"
		switch len(a) {
		case 1:
			field = a[0].s
		case 2:
			w, field = a[0].s, a[1].s
		}
		wi, ok := i.host.WorldInfo(w)
		if !ok {
			return S(""), nil
		}
		switch field {
		case "name":
			return S(wi.Name), nil
		case "host":
			return S(wi.Host), nil
		case "port":
			return S(wi.Port), nil
		case "character":
			return S(wi.Character), nil
		case "type":
			return S(wi.Type), nil
		case "tls":
			return B(wi.TLS), nil
		case "connected":
			return B(wi.Connected), nil
		}
		return Value{}, fmt.Errorf("world_info(): unknown field %q", field)
	})
	isConn := func(i *Interp, a []Value) (Value, error) {
		w := i.curWorld()
		if len(a) > 0 {
			w = a[0].s
		}
		return B(i.host.IsConnected(w)), nil
	}
	i.def("is_connected", 0, 1, isConn)
	i.def("is_open", 0, 1, isConn)
	i.def("fg_world", 0, 0, func(i *Interp, _ []Value) (Value, error) { return S(i.host.ForegroundWorld()), nil })
	i.def("nactive", 0, 1, func(i *Interp, _ []Value) (Value, error) { return I(int64(i.host.ActiveCount())), nil })
	i.def("columns", 0, 0, func(i *Interp, _ []Value) (Value, error) { c, _ := i.host.Screen(); return I(int64(c)), nil })
	i.def("lines", 0, 0, func(i *Interp, _ []Value) (Value, error) { _, l := i.host.Screen(); return I(int64(l)), nil })
	i.def("winlines", 0, 0, func(i *Interp, _ []Value) (Value, error) { _, l := i.host.Screen(); return I(int64(l)), nil })
	i.def("isatty", 0, 0, func(*Interp, []Value) (Value, error) { return I(0), nil })
	i.def("systype", 0, 0, func(*Interp, []Value) (Value, error) { return S("unix"), nil })
}

// fnGetopts implements getopts(optstring [, args]). It sets local variables
// opt_<letter> (value "1" for flags, the argument for "x:" options) and
// returns the remaining arguments; args defaults to the macro's %*.
func fnGetopts(i *Interp, a []Value) (Value, error) {
	spec := a[0].s
	args := i.top().args
	if len(a) > 1 {
		args = a[1].s
	}
	fr := i.top()
	if fr.locals == nil {
		fr.locals = map[string]Value{}
	}
	takes := map[byte]bool{}
	for k := 0; k < len(spec); k++ {
		if spec[k] == ':' {
			continue
		}
		takes[spec[k]] = k+1 < len(spec) && spec[k+1] == ':'
		delete(fr.locals, "opt_"+string(spec[k]))
	}
	w := strings.Fields(args)
	for len(w) > 0 && len(w[0]) > 1 && w[0][0] == '-' {
		cur := w[0]
		w = w[1:]
		if cur == "--" {
			break
		}
		for k := 1; k < len(cur); k++ {
			c := cur[k]
			arg, known := takes[c]
			if !known {
				return Value{}, fmt.Errorf("getopts(): unknown option -%c", c)
			}
			if !arg {
				fr.locals["opt_"+string(c)] = I(1)
				continue
			}
			val := cur[k+1:]
			if val == "" && len(w) > 0 {
				val, w = w[0], w[1:]
			}
			fr.locals["opt_"+string(c)] = S(val)
			break
		}
	}
	return S(strings.Join(w, " ")), nil
}
