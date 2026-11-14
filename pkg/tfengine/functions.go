package tfengine

import (
	"fmt"
	"strings"

	"github.com/itsnotyoutoday/gtfparser"
)

func (a *Engine) fn(name string, f func(in *gtfparser.Interp, args []gtfparser.Value) (gtfparser.Value, error)) {
	a.in.RegisterFunction(name, f)
}

func argStr(args []gtfparser.Value, i int) string {
	if i < len(args) {
		return args[i].String()
	}
	return ""
}

func b2v(b bool) gtfparser.Value { return gtfparser.Bool(b) }

// registerFunctions installs the functions that answer from the client's own
// state (worlds, sessions, window).
func (a *Engine) registerFunctions() {
	a.fn("features", func(in *gtfparser.Interp, args []gtfparser.Value) (gtfparser.Value, error) {
		return b2v(supportedFeatures[strings.ToLower(argStr(args, 0))]), nil
	})
	a.fn("world_info", func(in *gtfparser.Interp, args []gtfparser.Value) (gtfparser.Value, error) {
		w, field := a.cur, "name"
		switch len(args) {
		case 1:
			field = args[0].String()
		case 2:
			w, field = args[0].String(), args[1].String()
		}
		wi, ok := a.host.WorldInfo(w)
		if !ok {
			return gtfparser.Str(""), nil
		}
		switch field {
		case "name":
			return gtfparser.Str(wi.Name), nil
		case "host":
			return gtfparser.Str(wi.Host), nil
		case "port":
			return gtfparser.Str(wi.Port), nil
		case "character":
			return gtfparser.Str(wi.Character), nil
		case "type":
			return gtfparser.Str(wi.Type), nil
		case "tls", "ssl":
			return b2v(wi.TLS), nil
		case "connected":
			return b2v(wi.Connected), nil
		}
		return gtfparser.Int(0), fmt.Errorf("world_info(): unknown field %q", field)
	})
	isConn := func(in *gtfparser.Interp, args []gtfparser.Value) (gtfparser.Value, error) {
		w := a.cur
		if len(args) > 0 {
			w = args[0].String()
		}
		return b2v(a.host.IsConnected(w)), nil
	}
	a.fn("is_connected", isConn)
	a.fn("is_open", isConn)
	a.fn("fg_world", func(*gtfparser.Interp, []gtfparser.Value) (gtfparser.Value, error) {
		return gtfparser.Str(a.host.ForegroundWorld()), nil
	})
	a.fn("nactive", func(*gtfparser.Interp, []gtfparser.Value) (gtfparser.Value, error) {
		return gtfparser.Int(int64(a.host.ActiveCount())), nil
	})
	a.fn("nlog", func(*gtfparser.Interp, []gtfparser.Value) (gtfparser.Value, error) {
		return gtfparser.Int(int64(a.nlogs)), nil
	})
	a.fn("winlines", func(*gtfparser.Interp, []gtfparser.Value) (gtfparser.Value, error) {
		_, l := a.host.Screen()
		return gtfparser.Int(int64(l)), nil
	})
	a.fn("isatty", func(*gtfparser.Interp, []gtfparser.Value) (gtfparser.Value, error) {
		return gtfparser.Int(1), nil
	})
	a.fn("prompt", func(in *gtfparser.Interp, args []gtfparser.Value) (gtfparser.Value, error) {
		a.host.SetPrompt(a.cur, argStr(args, 0))
		return gtfparser.Int(1), nil
	})
	a.fn("encode_attr", func(in *gtfparser.Interp, args []gtfparser.Value) (gtfparser.Value, error) {
		return gtfparser.Str(EncodeAttr(argStr(args, 0), argStr(args, 1))), nil
	})
	a.fn("decode_attr", func(in *gtfparser.Interp, args []gtfparser.Value) (gtfparser.Value, error) {
		return gtfparser.Str(stripMarkup(argStr(args, 0))), nil
	})
	a.fn("strip_attr", func(in *gtfparser.Interp, args []gtfparser.Value) (gtfparser.Value, error) {
		return gtfparser.Str(stripMarkup(argStr(args, 0))), nil
	})
	// echo(text [, attrs [, inline [, dest]]])
	a.fn("echo", func(in *gtfparser.Interp, args []gtfparser.Value) (gtfparser.Value, error) {
		if len(args) == 0 {
			return gtfparser.Int(0), fmt.Errorf("echo(): needs a text")
		}
		(&coreHost{a}).EchoTo(gtfparser.EchoOpts{World: argStr(args, 3), Attrs: argStr(args, 1),
			Markup: len(args) > 2 && args[2].Truth()}, args[0].String())
		return gtfparser.Int(1), nil
	})
	// fake_recv(text [, world]): process text as if received from the world:
	// triggers run, and the line is shown unless a trigger gags it.
	a.fn("fake_recv", func(in *gtfparser.Interp, args []gtfparser.Value) (gtfparser.Value, error) {
		w := a.cur
		if v := argStr(args, 1); v != "" {
			w = v
		}
		prev := a.in.World()
		a.in.SetWorld(w)
		text := argStr(args, 0)
		res := a.process(in, text, nil, true)
		a.in.SetWorld(prev)
		if !res.Gag {
			a.echo(w, res.Spans)
		}
		return gtfparser.Int(1), nil
	})
}

// supportedFeatures are the names features() answers 1 for (and /features lists).
var supportedFeatures = map[string]bool{
	"256colors": true, "float": true, "process": true, "ssl": true, "regexp": true, "history": true,
}
