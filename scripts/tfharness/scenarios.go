package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

type env struct {
	rep      *report
	work     string
	tf       *tfProc
	addr     string // host:port/tfsp
	admin    *remote
	oper     *remote
	cobra    *gameServer
	evennia  *gameServer
	logf     func(string, ...any)
	cobraSID string
	evSID    string
	bridgeP  int
}

func tctx(d time.Duration) context.Context {
	c, cancel := context.WithTimeout(context.Background(), d)
	_ = cancel // released when the timeout fires; the harness is short-lived
	return c
}

func (e *env) check(group, name string, ok bool, format string, args ...any) {
	d := ""
	if format != "" {
		d = fmt.Sprintf(format, args...)
	}
	e.rep.add(result{group: group, name: name, ok: ok, detail: d})
	mark := "PASS"
	if !ok {
		mark = "FAIL"
	}
	e.logf("  %s  %-10s %s %s", mark, group, name, map[bool]string{true: "", false: "-- " + d}[ok])
}

func (e *env) skip(group, name, why string) {
	e.rep.add(result{group: group, name: name, skipped: true, detail: why})
	e.logf("  SKIP  %-10s %s -- %s", group, name, why)
}

func protoCode(err error) string {
	var pe *wire.ProtoError
	if errors.As(err, &pe) {
		return pe.Code
	}
	if err == nil {
		return ""
	}
	return "other: " + err.Error()
}

func hasFeature(c *wire.Client, f string) bool { return c.HasFeature(f) }

// ---------------------------------------------------------------- 1. handshake

func (e *env) handshake() {
	g := "handshake"
	c := e.admin.c
	e.check(g, "server kind is tf-squatterd", c.Kind() == proto.KindTFSquatterd, "kind=%q", c.Kind())
	e.check(g, "device role comes from the pairing code (admin)", c.Role() == "admin", "role=%q", c.Role())
	e.check(g, "welcome carries a TinyFugue instance id", c.Instance() != "", "")
	want := []string{"tf", "control", "worlds", "worldedit", "gmcp", "tfexec", "noEcho"}
	var missing []string
	for _, f := range want {
		if !hasFeature(c, f) {
			missing = append(missing, f)
		}
	}
	e.check(g, "advertises features "+strings.Join(want, ","), len(missing) == 0, "missing %v (have %v)", missing, c.Features())

	// a tf-squatter record pointing at a plain squatterd (or the reverse) is refused before the key is sent
	_, err := wire.Dial(tctx(5*time.Second), "ws://"+e.addr, wire.ClientOptions{Key: newKey(), InsecureLoopback: true,
		ExpectKind: proto.KindSquatterd, Caps: proto.DefaultCaps()})
	var km *wire.KindMismatchError
	e.check(g, "wrong squatter kind is refused before hello", errors.As(err, &km) && km.Got == proto.KindTFSquatterd, "err=%v", err)

	_, err = wire.Dial(tctx(5*time.Second), "ws://"+e.addr, wire.ClientOptions{Key: newKey(), PairCode: "AAAA-BBBB",
		InsecureLoopback: true, ExpectKind: proto.KindTFSquatterd, Caps: proto.DefaultCaps()})
	e.check(g, "a wrong pairing code is rejected", protoCode(err) == proto.ErrAuthFailed, "err=%v", err)

	code, err := e.tf.pairCode("viewer")
	if err != nil {
		e.check(g, "viewer pairing code", false, "%v", err)
		return
	}
	v, err := dial(e.addr, code, "harness-viewer", newKey())
	if err != nil {
		e.check(g, "viewer can pair", false, "%v", err)
		return
	}
	defer v.close()
	_, werr := v.c.Worlds(tctx(5 * time.Second))
	e.check(g, "viewer may list worlds", werr == nil, "%v", werr)
	_, perr := v.c.PutWorld(tctx(5*time.Second), wire.WorldPutReq{World: proto.TFWorld{Name: "viewer-try", Host: "h", Port: "1"}})
	e.check(g, "viewer may not edit worlds", protoCode(perr) == proto.ErrForbidden, "err=%v", perr)
	_, xerr := v.c.Exec(tctx(5*time.Second), "/echo no")
	e.check(g, "viewer may not run commands", protoCode(xerr) == proto.ErrForbidden, "err=%v", xerr)
}

// ---------------------------------------------------------------- 2. world editing

func hasWorld(ws []proto.TFWorld, name string) (proto.TFWorld, bool) {
	for _, w := range ws {
		if w.Name == name {
			return w, true
		}
	}
	return proto.TFWorld{}, false
}

func (e *env) worlds() {
	g := "worlds"
	a, o := e.admin.c, e.oper.c
	o2 := e.oper
	o2.mark()
	ch, err := a.PutWorld(tctx(5*time.Second), wire.WorldPutReq{World: proto.TFWorld{Name: "hw-test", Host: "127.0.0.1", Port: "1",
		Type: "tiny", Char: "bob"}, Password: "hunter2-secret"})
	e.check(g, "create a world remotely", err == nil && ch.Created && ch.Name == "hw-test", "err=%v ch=%+v", err, ch)
	_, got := o2.waitEvent(3*time.Second, func(ev wire.Event) bool {
		w, ok := ev.(wire.WorldChanged)
		return ok && w.Name == "hw-test"
	})
	e.check(g, "other clients are told (world.changed)", got, "")

	ws, err := o.Worlds(tctx(5 * time.Second))
	w, ok := hasWorld(ws, "hw-test")
	e.check(g, "the new world is listed with its fields", err == nil && ok && w.Host == "127.0.0.1" && w.Type == "tiny" && w.Char == "bob" && w.PassSet,
		"err=%v w=%+v", err, w)
	raw, _ := json.Marshal(ws)
	e.check(g, "the password is never sent back", !strings.Contains(string(raw), "hunter2"), "")
	tfView, _ := a.Exec(tctx(5*time.Second), "/listworlds hw-test")
	e.check(g, "TinyFugue itself has the world", strings.Contains(strings.Join(tfView.Out, "\n"), "hw-test"), "out=%v", tfView.Out)

	_, err = a.PutWorld(tctx(5*time.Second), wire.WorldPutReq{World: proto.TFWorld{Name: "hw-test", Host: "127.0.0.1", Port: "2", Type: "tiny", Char: "bob"}})
	ws, _ = o.Worlds(tctx(5 * time.Second))
	w, _ = hasWorld(ws, "hw-test")
	e.check(g, "edit a world: fields change, stored password is kept", err == nil && w.Port == "2" && w.PassSet, "err=%v w=%+v", err, w)

	_, err = a.PutWorld(tctx(5*time.Second), wire.WorldPutReq{World: proto.TFWorld{Name: "hw-test", Host: "127.0.0.1", Port: "2"}, ClearPassword: true})
	ws, _ = o.Worlds(tctx(5 * time.Second))
	w, _ = hasWorld(ws, "hw-test")
	e.check(g, "fields and password can be cleared", err == nil && !w.PassSet && w.Char == "" && w.Type == "", "err=%v w=%+v", err, w)

	_, err = a.PutWorld(tctx(5*time.Second), wire.WorldPutReq{World: proto.TFWorld{Name: "hw-test", Host: "127.0.0.1", Port: "2", Charset: "no-such-charset"}})
	e.check(g, "an unknown charset is refused (TinyFugue would abort on connect)", protoCode(err) == proto.ErrBadRequest, "err=%v", err)
	_, err = a.PutWorld(tctx(5*time.Second), wire.WorldPutReq{World: proto.TFWorld{Name: "bad name", Host: "h", Port: "1"}})
	e.check(g, "an illegal name is refused", protoCode(err) == proto.ErrBadRequest, "err=%v", err)
	_, err = a.PutWorld(tctx(5*time.Second), wire.WorldPutReq{World: proto.TFWorld{Name: "half", Host: "only-host"}})
	e.check(g, "host without port is refused", protoCode(err) == proto.ErrBadRequest, "err=%v", err)

	_, err = o.PutWorld(tctx(5*time.Second), wire.WorldPutReq{World: proto.TFWorld{Name: "hw-test", Host: "h", Port: "1", MFile: "/tmp/x.tf"}, SetMFile: true})
	e.check(g, "an operator cannot set a macro file (code)", protoCode(err) == proto.ErrForbidden, "err=%v", err)
	_, err = a.PutWorld(tctx(5*time.Second), wire.WorldPutReq{World: proto.TFWorld{Name: "hw-test", Host: "127.0.0.1", Port: "2", MFile: "/tmp/harness-x.tf"}, SetMFile: true})
	ws, _ = a.Worlds(tctx(5 * time.Second))
	w, _ = hasWorld(ws, "hw-test")
	e.check(g, "an admin can, and sees it", err == nil && w.MFile == "/tmp/harness-x.tf", "err=%v w=%+v", err, w)
	ws, _ = o.Worlds(tctx(5 * time.Second))
	w, _ = hasWorld(ws, "hw-test")
	e.check(g, "an operator does not see the macro file", w.MFile == "", "mfile=%q", w.MFile)
	_, err = o.PutWorld(tctx(5*time.Second), wire.WorldPutReq{World: proto.TFWorld{Name: "hw-test", Host: "127.0.0.1", Port: "3"}})
	ws, _ = a.Worlds(tctx(5 * time.Second))
	w, _ = hasWorld(ws, "hw-test")
	e.check(g, "an operator's edit keeps the macro file it cannot see", err == nil && w.MFile == "/tmp/harness-x.tf", "err=%v mfile=%q", err, w.MFile)

	_, err = a.DeleteWorld(tctx(5*time.Second), "hw-test")
	ws, _ = a.Worlds(tctx(5 * time.Second))
	_, still := hasWorld(ws, "hw-test")
	e.check(g, "delete a world", err == nil && !still, "err=%v", err)
	_, err = a.DeleteWorld(tctx(5*time.Second), "hw-test")
	e.check(g, "deleting a missing world is an error", err != nil, "")
}

// ---------------------------------------------------------------- 3. CobraMUSH

func (e *env) connectWorld(g, name string, gs *gameServer) (string, bool) {
	a := e.admin.c
	_, err := a.PutWorld(tctx(5*time.Second), wire.WorldPutReq{World: proto.TFWorld{Name: name, Host: "127.0.0.1", Port: fmt.Sprint(gs.port), Type: "tiny"}})
	if err != nil {
		e.check(g, "define the world remotely", false, "%v", err)
		return "", false
	}
	e.admin.mark()
	info, err := a.ConnectRef(tctx(10*time.Second), name)
	e.check(g, "connect by world record (session.created)", err == nil && info.TF != nil && info.TF.World == name, "err=%v info=%+v", err, info)
	if err != nil {
		return "", false
	}
	if err := a.Attach(tctx(5*time.Second), info.ID, nil, 0); err != nil {
		e.check(g, "attach", false, "%v", err)
		return "", false
	}
	return info.ID, true
}

func (e *env) cobraScenario() {
	g := "cobra"
	if e.cobra == nil {
		e.skip(g, "CobraMUSH scenario", "no CobraMUSH")
		return
	}
	a, r := e.admin.c, e.admin
	sid, ok := e.connectWorld(g, "cobra", e.cobra)
	if !ok {
		return
	}
	_, ok = r.waitLine(sid, "CobraMUSH", 10*time.Second)
	e.check(g, "the game's banner arrives", ok, "")
	_, err := a.ControlPreset(tctx(5*time.Second), sid, "takeover")
	e.check(g, "take control (preset takeover)", err == nil, "%v", err)

	r.mark()
	err = a.SendSecret(tctx(5*time.Second), sid, "connect #1 potrzebie")
	_, ok = r.waitLine(sid, "Welcome to CobraMUSH", 10*time.Second)
	e.check(g, "log in with a secret line", err == nil && ok, "err=%v ok=%v", err, ok)
	e.check(g, "the secret is not on TinyFugue's screen", !strings.Contains(e.tf.out.String(), "potrzebie"), "")
	for _, ev := range r.snapshot() {
		for _, en := range entriesOf(ev) {
			if en.Line != nil && strings.Contains(lineText(*en.Line), "potrzebie") {
				e.check(g, "the secret is not in any scrollback entry", false, "found in %q", lineText(*en.Line))
			}
		}
	}

	// colour
	r.mark()
	_ = a.Send(tctx(5*time.Second), sid, "@set me=ANSI")
	_ = a.Send(tctx(5*time.Second), sid, "@set me=COLOR")
	_ = a.Send(tctx(5*time.Second), sid, "think [ansi(r,REDWORD)] after")
	l, ok := r.waitLine(sid, "REDWORD", 8*time.Second)
	red := false
	for _, sp := range l.Spans {
		if strings.Contains(sp.S, "REDWORD") && sp.Fg != nil && !sp.Fg.IsTrueColor() && sp.Fg.Index() == 1 {
			red = true
		}
	}
	e.check(g, "ANSI colour reaches the client as spans", ok && red, "line=%q spans=%+v", lineText(l), l.Spans)

	// echo with origin
	r.mark()
	_ = a.Send(tctx(5*time.Second), sid, "think echo-origin-check")
	ev, ok := r.waitEvent(5*time.Second, func(ev wire.Event) bool {
		for _, en := range entriesOf(ev) {
			if en.Line != nil && en.Line.Kind == proto.KindEcho && strings.Contains(lineText(*en.Line), "echo-origin-check") {
				return true
			}
		}
		return false
	})
	origin := ""
	if ok {
		for _, en := range entriesOf(ev) {
			if en.Line != nil && en.Line.Kind == proto.KindEcho {
				origin = en.Line.Origin
			}
		}
	}
	e.check(g, "our typing is echoed with our device as origin", ok && origin == a.DeviceID(), "origin=%q want=%q", origin, a.DeviceID())

	// NAWS and TTYPE as the GAME sees them
	r.mark()
	_ = a.Resize(tctx(5*time.Second), sid, 120, 40)
	time.Sleep(500 * time.Millisecond)
	_ = a.Send(tctx(5*time.Second), sid, "think SIZE:[width(me)]x[height(me)]")
	_, ok = r.waitLine(sid, "SIZE:120x40", 8*time.Second)
	e.check(g, "window size follows the controller (NAWS, as the game reports it)", ok, "")
	r.mark()
	_ = a.Send(tctx(5*time.Second), sid, "think TERM:[terminfo(me)]")
	l, ok = r.waitLine(sid, "TERM:", 8*time.Second)
	e.check(g, "the game sees TinyFugue's terminal type", ok && strings.Contains(strings.ToUpper(lineText(l)), "TINYFUGUE"), "line=%q", lineText(l))

	// automation: TinyFugue's trigger fires by default, and stops while a client owns automation
	_, _ = a.Exec(tctx(5*time.Second), `/def -t"*FIREME*" harness_trig = think trigger-reply-ok`)
	count := func() int {
		n := 0
		for _, ev := range r.snapshot() {
			for _, en := range entriesOf(ev) {
				if en.Line != nil && en.Line.Kind == proto.KindGame && lineText(*en.Line) == "trigger-reply-ok" {
					n++
				}
			}
		}
		return n
	}
	before := count()
	_ = a.Send(tctx(5*time.Second), sid, "think FIREME")
	time.Sleep(1500 * time.Millisecond)
	e.check(g, "TinyFugue runs its trigger by default", count() == before+1, "fired %d times", count()-before)
	_, err = a.ControlSet(tctx(5*time.Second), sid, "automation", "client")
	before = count()
	_ = a.Send(tctx(5*time.Second), sid, "think FIREME")
	time.Sleep(1500 * time.Millisecond)
	e.check(g, "automation=client stops TinyFugue's trigger for that world", err == nil && count() == before, "fired %d more times", count()-before)
	_, _ = a.ControlSet(tctx(5*time.Second), sid, "automation", "tf")
	_ = a.Send(tctx(5*time.Second), sid, "think FIREME")
	time.Sleep(1500 * time.Millisecond)
	e.check(g, "and it runs again when automation goes back to tf", count() == before+1, "fired %d more times", count()-before)

	// gag annotation and post-trigger display text
	_, _ = a.Exec(tctx(5*time.Second), `/def -ag -t"*GAGME*" harness_gag`)
	r.mark()
	_ = a.Send(tctx(5*time.Second), sid, "think GAGME-line")
	l, ok = r.waitLine(sid, "GAGME-line", 5*time.Second)
	why := ""
	if !(ok && l.Gag) {
		for _, ev := range r.snapshot() {
			for _, en := range entriesOf(ev) {
				if en.Line != nil && strings.Contains(lineText(*en.Line), "GAGME") {
					why += fmt.Sprintf(" [kind=%s gag=%v prompt=%v seq=%d %q]", en.Line.Kind, en.Line.Gag, en.Line.Prompt, en.Line.Seq, lineText(*en.Line))
				}
			}
		}
		res, _ := a.Exec(tctx(5*time.Second), "/list -s harness_gag")
		why += " macro: " + strings.Join(res.Out, " | ")
	}
	e.check(g, "a line TinyFugue gags is still sent, flagged gag", ok && l.Gag, "ok=%v gag=%v lines:%s", ok, l.Gag, why)
	_, _ = a.Exec(tctx(5*time.Second), `/def -ah -t"*HILITEME*" harness_hl`)
	r.mark()
	_ = a.Send(tctx(5*time.Second), sid, "think HILITEME-line")
	l, ok = r.waitLine(sid, "HILITEME-line", 5*time.Second)
	bold := false
	for _, sp := range l.Disp {
		bold = bold || sp.B
	}
	plain := true
	for _, sp := range l.Spans {
		plain = plain && !sp.B
	}
	e.check(g, "post-trigger display text (disp) carries TinyFugue's hilite", ok && bold && plain, "ok=%v disp=%+v spans=%+v", ok, l.Disp, l.Spans)

	// the TF keyboard is locked while the remote controls the session
	e.tf.typ("/fg cobra") // the keyboard types into the foreground world
	r.mark()
	e.tf.typ("think KBDLOCK-should-not-arrive")
	_, arrived := r.waitLine(sid, "KBDLOCK-should-not-arrive", 2500*time.Millisecond)
	e.check(g, "the TinyFugue keyboard is locked while a remote controls", !arrived && strings.Contains(e.tf.out.String(), "controlled by a remote client"), "arrived=%v", arrived)

	// control reverts when the controlling device goes away
	o := e.oper.c
	_, err = o.ControlSet(tctx(5*time.Second), sid, "input", "remote")
	e.admin.mark()
	e.oper.close()
	_, ok = e.admin.waitEvent(6*time.Second, func(ev wire.Event) bool {
		cs, isCS := ev.(wire.ControlState)
		return isCS && cs.Session == sid && cs.Input == "tf"
	})
	e.check(g, "control returns to TinyFugue when the controlling device disconnects", err == nil && ok, "err=%v ok=%v", err, ok)
	e.oper = nil
	e.cobraSID = sid
}

func (r *remote) snapshot() []wire.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]wire.Event(nil), r.evs...)
}

// ---------------------------------------------------------------- 4. Evennia (GMCP)

func (e *env) evenniaScenario() {
	g := "evennia"
	if e.evennia == nil {
		e.skip(g, "Evennia scenario", "no Evennia")
		return
	}
	a, r := e.admin.c, e.admin
	sid, ok := e.connectWorld(g, "evennia", e.evennia)
	if !ok {
		return
	}
	_, ok = r.waitLine(sid, "Welcome to testgame", 15*time.Second)
	e.check(g, "the game's banner arrives (MCCP2 offered by the game)", ok, "")
	_, _ = a.ControlPreset(tctx(5*time.Second), sid, "takeover")
	e.evSID = sid
	r.mark()
	_ = a.SendSecret(tctx(5*time.Second), sid, "connect god godpw")
	_, ok = r.waitLine(sid, "You become god", 15*time.Second)
	e.check(g, "log in", ok, "")
	_, ok = r.waitGmcp(sid, "Logged.In", 10*time.Second)
	e.check(g, "GMCP from the game is forwarded (Logged.In)", ok, "")

	r.mark()
	_ = a.Send(tctx(5*time.Second), sid, `py me.msg(char_vitals=((), {"hp": 10, "mp": 3}))`)
	m, ok := r.waitGmcp(sid, "Char.Vitals", 10*time.Second)
	var data map[string]float64
	_ = json.Unmarshal(m.Data, &data)
	e.check(g, "GMCP data arrives parsed as JSON (Char.Vitals)", ok && data["hp"] == 10 && data["mp"] == 3, "ok=%v data=%s", ok, m.Data)

	r.mark()
	err := a.SendGmcp(tctx(5*time.Second), sid, "echo", []string{"hello-gmcp"})
	_, ok = r.waitLine(sid, "hello-gmcp", 10*time.Second)
	e.check(g, "GMCP to the game round-trips (gmcp.send -> Evennia echo)", err == nil && ok, "err=%v ok=%v", err, ok)
	err = a.SendGmcp(tctx(5*time.Second), sid, "bad package!", nil)
	e.check(g, "a malformed GMCP package name is refused", protoCode(err) == proto.ErrBadRequest, "err=%v", err)

	// negotiated terminal facts as Evennia stores them
	_, _ = a.Resize(tctx(5*time.Second), sid, 101, 33), error(nil)
	time.Sleep(600 * time.Millisecond)
	r.mark()
	_ = a.Send(tctx(5*time.Second), sid, `py print("FLAGS:", me.sessions.all()[0].protocol_flags)`)
	l, ok := r.waitLine(sid, "'SCREENWIDTH'", 10*time.Second)
	txt := lineText(l)
	e.check(g, "NAWS reaches Evennia (screen width 101)", ok && strings.Contains(txt, "101"), "flags=%s", txt)
	e.check(g, "TTYPE reaches Evennia (TINYFUGUE)", ok && strings.Contains(strings.ToUpper(txt), "TINYFUGUE"), "flags=%s", txt)
}

// ---------------------------------------------------------------- 5. takeover requests

func (e *env) askScenario() {
	g := "ask"
	sid := e.cobraSID
	if sid == "" {
		e.skip(g, "takeover requests", "no live session")
		return
	}
	a, r := e.admin.c, e.admin
	_, _ = a.Exec(tctx(5*time.Second), "/set squat_takeover=ask")
	_, _ = a.Exec(tctx(5*time.Second), "/set squat_ask_timeout=3")
	_, _ = a.ControlSet(tctx(5*time.Second), sid, "input", "tf")

	request := func() chan error {
		ch := make(chan error, 1)
		go func() { _, err := a.ControlSet(tctx(15*time.Second), sid, "input", "remote"); ch <- err }()
		return ch
	}
	r.mark()
	ch := request()
	_, pending := r.waitEvent(5*time.Second, func(ev wire.Event) bool { _, ok := ev.(wire.ControlPending); return ok })
	time.Sleep(300 * time.Millisecond)
	e.check(g, "clients are told a request is waiting (control.pending)", pending, "")
	e.check(g, "TinyFugue's user is asked on screen", strings.Contains(e.tf.out.String(), "wants input control"), "")
	e.check(g, "the status indicator shows the waiting request", strings.Contains(e.statusVar(), "?1"), "status=%q", e.statusVar())
	e.tf.typ("/control allow")
	select {
	case err := <-ch:
		e.check(g, "/control allow grants the takeover", err == nil, "%v", err)
	case <-time.After(8 * time.Second):
		e.check(g, "/control allow grants the takeover", false, "no answer")
	}

	_, _ = a.ControlSet(tctx(5*time.Second), sid, "input", "tf")
	ch = request()
	time.Sleep(600 * time.Millisecond)
	e.tf.typ("/control deny")
	select {
	case err := <-ch:
		e.check(g, "/control deny refuses it (error denied)", protoCode(err) == proto.ErrDenied, "%v", err)
	case <-time.After(8 * time.Second):
		e.check(g, "/control deny refuses it", false, "no answer")
	}

	ch = request()
	select {
	case err := <-ch:
		e.check(g, "an unanswered request times out (error denied)", protoCode(err) == proto.ErrDenied, "%v", err)
	case <-time.After(10 * time.Second):
		e.check(g, "an unanswered request times out", false, "no answer")
	}

	_, _ = a.Exec(tctx(5*time.Second), "/set squat_takeover=off")
	_, err := a.ControlSet(tctx(5*time.Second), sid, "input", "remote")
	e.check(g, "squat_takeover=off refuses every takeover", protoCode(err) == proto.ErrDenied, "%v", err)
	_, err = a.ControlSet(tctx(5*time.Second), sid, "input", "tf")
	e.check(g, "but giving control back to TinyFugue is always allowed", err == nil, "%v", err)
	_, _ = a.Exec(tctx(5*time.Second), "/set squat_takeover=auto")
}

func (e *env) bridgePort() int { return e.bridgeP }

func (e *env) statusVar() string {
	res, err := e.admin.c.Exec(tctx(5*time.Second), "/eval /echo STATUS=%{squat_status}=END")
	if err != nil {
		return "exec failed: " + err.Error()
	}
	s := strings.Join(res.Out, " ")
	m := regexp.MustCompile(`STATUS=(.*)=END`).FindStringSubmatch(s)
	if m == nil {
		return s
	}
	return m[1]
}

// ---------------------------------------------------------------- 6. commands and status

func (e *env) execScenario() {
	g := "exec"
	a := e.admin.c
	res, err := a.Exec(tctx(5*time.Second), "/echo hello-from-exec")
	e.check(g, "run a command and get its output", err == nil && res.OK && strings.Contains(strings.Join(res.Out, "\n"), "hello-from-exec"), "err=%v res=%+v", err, res)
	res, err = a.Exec(tctx(5*time.Second), "/no_such_command_at_all")
	e.check(g, "a failing command reports ok=false with TinyFugue's message", err == nil && !res.OK && len(res.Out) > 0, "err=%v res=%+v", err, res)
	_, err = a.Exec(tctx(5*time.Second), "echo no-slash")
	e.check(g, "a line without / is refused", protoCode(err) == proto.ErrBadRequest, "%v", err)
	e.oper2Exec()
	_, _ = a.Exec(tctx(5*time.Second), "/set squat_exec=off")
	_, err = a.Exec(tctx(5*time.Second), "/echo should-not-run")
	e.check(g, "squat_exec=off in TinyFugue refuses even an admin", protoCode(err) == proto.ErrForbidden, "%v", err)
	e.tf.typ("/set squat_exec=on")
	res, err = a.Exec(tctx(5*time.Second), "/echo back-on")
	e.check(g, "and it can be switched on again", err == nil && res.OK, "%v", err)

	// squat_exec=allow: only a short list of read-only commands, nothing that can change TinyFugue
	e.tf.typ("/set squat_exec=allow")
	res, err = a.Exec(tctx(5*time.Second), "/version")
	e.check(g, "allow: a listed command (/version) runs", err == nil && res.OK && len(res.Out) > 0, "err=%v res=%+v", err, res)
	res, err = a.Exec(tctx(5*time.Second), "/VERSION")
	e.check(g, "allow: command names are not case sensitive", err == nil && res.OK, "err=%v res=%+v", err, res)
	for _, line := range []string{"/set squat_exec=on", "/eval /set squat_exec=on", "/sh echo hi", "/quote !echo hi", "/def evil = /echo x", "/load /etc/passwd", "/version %; /set squat_exec=on", "/version\n/set squat_exec=on"} {
		_, err = a.Exec(tctx(5*time.Second), line)
		e.check(g, fmt.Sprintf("allow: %q is refused", line), protoCode(err) == proto.ErrForbidden, "%v", err)
	}
	e.check(g, "allow: squat_exec was not changed by the refused lines", e.varValue("squat_exec") == "allow", "squat_exec=%q", e.varValue("squat_exec"))
	e.tf.typ("/set squat_exec_allow=echo")
	res, err = a.Exec(tctx(5*time.Second), "/echo custom-list")
	e.check(g, "allow: squat_exec_allow replaces the default list", err == nil && res.OK && strings.Contains(strings.Join(res.Out, "\n"), "custom-list"), "err=%v res=%+v", err, res)
	_, err = a.Exec(tctx(5*time.Second), "/version")
	e.check(g, "allow: a command not in the custom list is refused", protoCode(err) == proto.ErrForbidden, "%v", err)
	_, err = a.PutWorld(tctx(5*time.Second), wire.WorldPutReq{World: proto.TFWorld{Name: "hw-allow-mfile", Host: "127.0.0.1", Port: "1", MFile: "/tmp/x.tf"}, SetMFile: true})
	e.check(g, "allow: a macro file cannot be set (needs squat_exec=on)", protoCode(err) == proto.ErrForbidden, "%v", err)
	// world fields are data: command-looking text in them must not run, and squat_exec stays as it was
	inj := proto.TFWorld{Name: "hw-inject", Host: "127.0.0.1", Port: "1", Char: "x%;/set squat_exec=on", Type: "tiny%;/set squat_exec=on"}
	_, err = a.PutWorld(tctx(5*time.Second), wire.WorldPutReq{World: inj, Password: "p%;/set squat_exec=on", Save: false})
	e.check(g, "world fields with command text are stored as data, not run", e.varValue("squat_exec") == "allow", "squat_exec=%q err=%v", e.varValue("squat_exec"), err)
	_, _ = a.DeleteWorld(tctx(5*time.Second), "hw-inject")
	e.tf.typ("/set squat_exec_allow=")
	e.tf.typ("/set squat_exec=on")
	res, err = a.Exec(tctx(5*time.Second), "/echo full-again")
	e.check(g, "and on is full power again", err == nil && res.OK, "%v", err)

	st := e.statusVar()
	e.check("status", "squat_status variable reports the bridge ("+st+")", regexp.MustCompile(`^sq:\d+`).MatchString(st), "status=%q", st)
}

// varValue reads one TinyFugue variable from its own keyboard (not through the bridge).
func (e *env) varValue(name string) string {
	n := len(e.tf.out.String())
	e.tf.typ("/eval /echo VAR=%{" + name + "}=END")
	all := regexp.MustCompile(`VAR=([^=\s]*)=END`).FindAllStringSubmatch(e.tf.out.String()[n:], -1)
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1][1] // the last one: the first may be the echo of what was typed
}

// ---------------------------------------------------------------- 5b. renaming worlds

func (e *env) renameScenario() {
	g := "rename"
	a := e.admin.c
	r := e.admin
	ctx := func() context.Context { return tctx(10 * time.Second) }
	names := func() map[string]proto.TFWorld {
		ws, _ := a.Worlds(ctx())
		m := map[string]proto.TFWorld{}
		for _, w := range ws {
			m[w.Name] = w
		}
		return m
	}
	_, err := a.PutWorld(ctx(), wire.WorldPutReq{World: proto.TFWorld{Name: "hw-ren", Host: "127.0.0.1", Port: "1", Char: "bob", Type: "tiny"}, Password: "pw"})
	e.check(g, "set up: a world with a password", err == nil, "%v", err)
	var other *remote // a second device: the requester gets the reply, others the broadcast
	if code, cerr := e.tf.pairCode("operator"); cerr == nil {
		other, _ = dial(e.addr, code, "harness-rename-watcher", newKey())
	}
	if other != nil {
		defer other.close()
		other.mark()
	}
	r.mark()
	_, err = a.RenameWorld(ctx(), "hw-ren", "hw-renamed", false)
	m := names()
	e.check(g, "rename: the old name is gone and the new one is there", err == nil && m["hw-ren"].Name == "" && m["hw-renamed"].Name == "hw-renamed", "err=%v %v", err, m)
	e.check(g, "rename: host, port, character and password are kept", m["hw-renamed"].Host == "127.0.0.1" && m["hw-renamed"].Port == "1" && m["hw-renamed"].Char == "bob" && m["hw-renamed"].PassSet, "%+v", m["hw-renamed"])
	ev, ok := other.waitEvent(3*time.Second, func(ev wire.Event) bool {
		w, is := ev.(wire.WorldChanged)
		return is && w.OldName == "hw-ren" && w.Name == "hw-renamed"
	})
	_ = ev
	e.check(g, "rename: other devices are told (world.changed with oldName)", ok, "")
	// a change made at TinyFugue's own keyboard is announced to clients too
	if other != nil {
		other.mark()
		e.tf.typ("/addworld hw-kbd 127.0.0.1 9")
		_, ok = other.waitEvent(5*time.Second, func(ev wire.Event) bool { _, is := ev.(wire.WorldChanged); return is })
		m = names()
		e.check(g, "a world added at TinyFugue's keyboard is announced and listed", ok && m["hw-kbd"].Name == "hw-kbd", "ok=%v %v", ok, m["hw-kbd"])
		other.mark()
		e.tf.typ("/unworld hw-kbd")
		_, ok = other.waitEvent(5*time.Second, func(ev wire.Event) bool { _, is := ev.(wire.WorldChanged); return is })
		e.check(g, "a world deleted at TinyFugue's keyboard is announced", ok && names()["hw-kbd"].Name == "", "ok=%v", ok)
	}
	_, _ = a.PutWorld(ctx(), wire.WorldPutReq{World: proto.TFWorld{Name: "hw-other", Host: "127.0.0.1", Port: "2"}})
	_, err = a.RenameWorld(ctx(), "hw-renamed", "hw-other", false)
	e.check(g, "rename onto an existing world is refused", protoCode(err) == proto.ErrBadRequest, "%v", err)
	_, err = a.RenameWorld(ctx(), "hw-nope", "hw-x", false)
	e.check(g, "rename of a missing world is refused", err != nil, "%v", err)
	for _, bad := range []string{"has space", "(paren", "default"} {
		_, err = a.RenameWorld(ctx(), "hw-renamed", bad, false)
		e.check(g, fmt.Sprintf("rename to %q is refused", bad), err != nil, "%v", err)
	}
	_, _ = a.DeleteWorld(ctx(), "hw-renamed")
	_, _ = a.DeleteWorld(ctx(), "hw-other")

	// a connection made by address (a temporary world, "(unnamedN)") can be turned into a named, kept world
	{
		cobraPort := fmt.Sprint(e.cobra.port)
		info, cerr := a.Connect(ctx(), proto.World{Name: "adhoc", Host: "127.0.0.1", Port: e.cobra.port})
		e.check(g, "set up: a connection by address (temporary world)", cerr == nil && info.TF != nil && strings.HasPrefix(info.TF.World, "("), "err=%v %+v", cerr, info.TF)
		if cerr == nil && info.TF != nil {
			tmp := info.TF.World
			_, err = a.RenameWorld(ctx(), tmp, "hw-adopted", true)
			m := names()
			e.check(g, "adopt: the temporary world becomes a named one, not temporary", err == nil && m["hw-adopted"].Name == "hw-adopted" && !m["hw-adopted"].Temp && m["hw-adopted"].Port == cobraPort, "err=%v %+v", err, m["hw-adopted"])
			_, ok := r.waitEvent(5*time.Second, func(ev wire.Event) bool {
				st, is := ev.(wire.SessionState)
				return is && st.Info.ID == info.ID && st.Info.TF != nil && st.Info.TF.World == "hw-adopted"
			})
			e.check(g, "adopt: the connection keeps running under the new name", ok, "")
			_ = a.Disconnect(ctx(), info.ID)
			_ = a.Dismiss(ctx(), info.ID)
			time.Sleep(300 * time.Millisecond)
			e.check(g, "adopt: the world is still there after the connection closed", names()["hw-adopted"].Name == "hw-adopted", "")
			_, _ = a.DeleteWorld(ctx(), "hw-adopted")
		}
		// the same from TinyFugue's own keyboard
		info2, cerr := a.Connect(ctx(), proto.World{Name: "adhoc2", Host: "127.0.0.1", Port: e.cobra.port})
		if cerr == nil && info2.TF != nil {
			e.tf.typ("/renameworld " + info2.TF.World + " hw-typed")
			time.Sleep(400 * time.Millisecond)
			m := names()
			e.check(g, "/renameworld at TinyFugue's keyboard turns a temporary world into a named one", m["hw-typed"].Name == "hw-typed" && !m["hw-typed"].Temp, "%+v", m["hw-typed"])
			e.tf.typ("/renameworld hw-typed hw-typed2") // two arguments: rename any world
			time.Sleep(300 * time.Millisecond)
			e.check(g, "/renameworld <old> <new> renames a world", names()["hw-typed2"].Name == "hw-typed2" && names()["hw-typed"].Name == "", "")
			_ = a.Disconnect(ctx(), info2.ID)
			_ = a.Dismiss(ctx(), info2.ID)
			time.Sleep(300 * time.Millisecond)
			_, _ = a.DeleteWorld(ctx(), "hw-typed2")
		}
	}

	// a world that is connected can be renamed; the session keeps working
	if e.cobraSID == "" {
		e.skip(g, "rename a connected world", "no live session")
		return
	}
	sid := e.cobraSID
	r.mark()
	_, err = a.RenameWorld(ctx(), "cobra", "cobra-renamed", false)
	e.check(g, "rename while connected is accepted", err == nil, "%v", err)
	_, ok = r.waitEvent(5*time.Second, func(ev wire.Event) bool {
		s, is := ev.(wire.SessionState)
		return is && s.Info.ID == sid && s.Info.TF != nil && s.Info.TF.World == "cobra-renamed"
	})
	e.check(g, "the session reports its new world name", ok, "")
	_, err = a.ControlPreset(ctx(), sid, "takeover")
	r.mark()
	err2 := a.Send(ctx(), sid, "think after-rename")
	_, got := r.waitLine(sid, "after-rename", 8*time.Second)
	e.check(g, "the connection still works after the rename", err == nil && err2 == nil && got, "%v %v", err, err2)
	_, err = a.RenameWorld(ctx(), "cobra-renamed", "cobra", false)
	e.check(g, "and it can be renamed back", err == nil, "%v", err)
}

// ---------------------------------------------------------------- 6b. uploaded scripts

func (e *env) scriptsScenario() {
	g := "scripts"
	a := e.admin.c
	ctx := func() context.Context { return tctx(10 * time.Second) }
	mk := func(name string) string { return filepath.Join(e.work, name) }
	exists := func(name string) bool { _, err := os.Stat(mk(name)); return err == nil }
	e.tf.typ("/set squat_exec=on") // so the checks can ask TinyFugue what happened

	// off (the default): refused, and nothing is written
	_, err := a.PutScript(ctx(), "off.tf", "/echo x", false)
	e.check(g, "squat_scripts off (default) refuses an upload", protoCode(err) == proto.ErrForbidden, "%v", err)
	ls, _ := a.ListScripts(ctx())
	e.check(g, "and nothing was stored", len(ls) == 0, "%v", ls)

	// safe: a normal script loads and its trigger works
	e.tf.typ("/set squat_scripts=safe")
	res, err := a.PutScript(ctx(), "hello.tf", `/def -t"*HELLOSCRIPT*" hello_script = /echo hello-from-script`+"\n", true)
	e.check(g, "safe: a script is stored and loaded", err == nil && res.Stored && res.Loaded, "err=%v res=%+v", err, res)
	x, err := a.Exec(ctx(), "/hello_script")
	e.check(g, "safe: the macro it defined runs", err == nil && strings.Contains(strings.Join(x.Out, "\n"), "hello-from-script"), "err=%v x=%+v", err, x)

	// safe: every way out is closed, at load time and when its macros run later
	evil := strings.Join([]string{
		"/sh touch " + mk("sbx-shell"),
		"/quote -0 !touch " + mk("sbx-quote"),
		"/calllua os.execute('touch " + mk("sbx-lua") + "')",
		"/log " + mk("sbx-log"),
		"/lcd /",
		"/restrict none",
		"/set squat_exec=on",
		"/set squat_scripts=on",
		"/squat pair admin",
		`/def evil_later = /sh touch ` + mk("sbx-later"),
		`/def evil_lua = /calllua os.execute('touch ` + mk("sbx-lua2") + `')`,
		`/def evil_set = /set squat_scripts=on`,
		`/def evil_chain = /eval /sh touch ` + mk("sbx-chain"),
		"/echo evil-script-finished",
	}, "\n") + "\n"
	res, err = a.PutScript(ctx(), "evil.tf", evil, true)
	e.check(g, "safe: a hostile script is stored and loaded (its dangerous lines are refused one by one)", err == nil && res.Stored, "err=%v res=%+v", err, res)
	e.check(g, "safe: the refusals are reported", strings.Contains(strings.Join(res.Out, "\n"), "restricted"), "out=%v", res.Out)
	for _, name := range []string{"evil_later", "evil_lua", "evil_set", "evil_chain"} {
		_, _ = a.Exec(ctx(), "/"+name)
	}
	time.Sleep(300 * time.Millisecond)
	for _, f := range []string{"sbx-shell", "sbx-quote", "sbx-lua", "sbx-log", "sbx-later", "sbx-lua2", "sbx-chain"} {
		e.check(g, "safe: "+f+" was NOT created", !exists(f), "")
	}
	e.check(g, "safe: squat_scripts was not loosened by the script", e.varValue("squat_scripts") == "safe", "squat_scripts=%q", e.varValue("squat_scripts"))
	e.check(g, "safe: squat_exec was not loosened by the script", e.varValue("squat_exec") == "on", "squat_exec=%q", e.varValue("squat_exec"))
	x, _ = a.Exec(ctx(), "/restrict")
	e.check(g, "safe: TinyFugue's own restriction level is back to none", strings.Contains(strings.Join(x.Out, " "), "none"), "%v", x.Out)
	// and TinyFugue itself (not sandboxed) can still do all of it
	e.tf.typ("/sh touch " + mk("tf-own-shell"))
	time.Sleep(500 * time.Millisecond)
	e.check(g, "safe: TinyFugue's own keyboard can still run /sh", exists("tf-own-shell"), "")

	// names and content
	for _, bad := range []string{"../x.tf", "x.txt", "a b.tf", ".hidden.tf", "", "sub/x.tf"} {
		_, err = a.PutScript(ctx(), bad, "/echo x", false)
		e.check(g, fmt.Sprintf("name %q is refused", bad), protoCode(err) == proto.ErrBadRequest, "%v", err)
	}
	_, err = a.PutScript(ctx(), "nul.tf", "a\x00b", false)
	e.check(g, "a script with a NUL byte is refused", protoCode(err) == proto.ErrBadRequest, "%v", err)
	_, err = a.PutScript(ctx(), "big.tf", strings.Repeat("x", wire.MaxScriptBytes+1), false)
	e.check(g, "a too large script is refused", protoCode(err) == proto.ErrBadRequest, "%v", err)

	// list / replace / delete
	ls, err = a.ListScripts(ctx())
	names := map[string]bool{}
	for _, i := range ls {
		names[i.Name] = true
	}
	e.check(g, "list shows the stored scripts", err == nil && names["hello.tf"] && names["evil.tf"], "%v %v", ls, err)
	_, err = a.PutScript(ctx(), "hello.tf", `/def -t"*HELLOSCRIPT*" hello_script = /echo hello-v2`+"\n", true)
	x, _ = a.Exec(ctx(), "/hello_script")
	e.check(g, "putting the same name replaces it and reloads", err == nil && strings.Contains(strings.Join(x.Out, "\n"), "hello-v2"), "err=%v %v", err, x.Out)
	err = a.DeleteScript(ctx(), "evil.tf")
	ls, _ = a.ListScripts(ctx())
	e.check(g, "delete removes the file", err == nil && len(ls) == 1, "err=%v %v", err, ls)
	e.check(g, "deleting a missing script is an error", a.DeleteScript(ctx(), "evil.tf") != nil, "")

	// a script without a load flag is stored only
	e.tf.typ("/undef hello_script")
	_, err = a.PutScript(ctx(), "stored.tf", `/def stored_only = /echo x`+"\n", false)
	x, _ = a.Exec(ctx(), "/stored_only")
	e.check(g, "put without load stores but does not run it", err == nil && !x.OK, "err=%v %+v", err, x)

	// an operator may not use any of it
	if code, err := e.tf.pairCode("operator"); err == nil {
		if o, err := dial(e.addr, code, "harness-operator3", newKey()); err == nil {
			_, err = o.c.PutScript(ctx(), "op.tf", "/echo x", false)
			e.check(g, "an operator may not upload scripts", protoCode(err) == proto.ErrForbidden, "%v", err)
			_, err = o.c.ListScripts(ctx())
			e.check(g, "an operator may not list scripts", protoCode(err) == proto.ErrForbidden, "%v", err)
			o.close()
		}
	}

	// on: trusted, TinyFugue's own rights
	e.tf.typ("/set squat_scripts=on")
	res, err = a.PutScript(ctx(), "trusted.tf", "/sh touch "+mk("trusted-shell")+"\n", true)
	time.Sleep(500 * time.Millisecond)
	e.check(g, "on: the script runs with TinyFugue's own rights (shell works)", err == nil && res.Loaded && exists("trusted-shell"), "err=%v res=%+v", err, res)

	e.tf.typ("/set squat_scripts=off")
	_, err = a.PutScript(ctx(), "again.tf", "/echo x", false)
	e.check(g, "switching it off again refuses uploads", protoCode(err) == proto.ErrForbidden, "%v", err)
	e.tf.typ("/set squat_exec=on")
}

func (e *env) oper2Exec() {
	code, err := e.tf.pairCode("operator")
	if err != nil {
		return
	}
	o, err := dial(e.addr, code, "harness-operator2", newKey())
	if err != nil {
		return
	}
	defer o.close()
	_, err = o.c.Exec(tctx(5*time.Second), "/echo no")
	e.check("exec", "an operator may not run commands", protoCode(err) == proto.ErrForbidden, "%v", err)
}

// ---------------------------------------------------------------- 7. restart: inherit sessions

func (e *env) restartScenario() {
	g := "restart"
	if e.cobraSID == "" {
		e.skip(g, "inherit sessions after /squat off, /squat on", "no live session")
		return
	}
	oldInst := e.admin.c.Instance()
	e.tf.typ("/squat off")
	time.Sleep(700 * time.Millisecond)
	e.check(g, "/squat off closes remote clients", func() bool {
		select {
		case <-e.admin.c.Done():
			return true
		case <-time.After(3 * time.Second):
			return false
		}
	}(), "")
	e.tf.typ("/squat on")
	if !waitPort(e.bridgePort(), 15*time.Second) {
		e.check(g, "/squat on listens again", false, "")
		return
	}
	code, err := e.tf.pairCode("admin")
	if err != nil {
		e.check(g, "pair again", false, "%v", err)
		return
	}
	n, err := dial(e.addr, code, "harness-admin-2", newKey())
	if err != nil {
		e.check(g, "pair again", false, "%v", err)
		return
	}
	e.admin = n
	e.check(g, "a restart gives a new instance id", n.c.Instance() != "" && n.c.Instance() != oldInst, "old=%q new=%q", oldInst, n.c.Instance())
	var found *proto.SessionInfo
	for _, s := range n.c.Sessions() {
		if s.TF != nil && s.TF.World == "cobra" && s.State == proto.StateConnected {
			s := s
			found = &s
		}
	}
	e.check(g, "the still-connected game session is inherited by the new client", found != nil, "sessions=%d", len(n.c.Sessions()))
	if found == nil {
		return
	}
	_ = n.c.Attach(tctx(5*time.Second), found.ID, nil, 0)
	_, ok := n.waitLine(found.ID, "Welcome to CobraMUSH", 8*time.Second)
	e.check(g, "its history is replayed from TinyFugue", ok, "")
}

// ---------------------------------------------------------------- 8. MSDP

func arrayHas(v any, want string) bool {
	a, _ := v.([]any)
	for _, x := range a {
		if x == want {
			return true
		}
	}
	return false
}

func (e *env) msdpScenario() {
	g := "msdp"
	a, r := e.admin.c, e.admin
	e.check(g, "the bridge advertises msdp", a.HasFeature("msdp"), "features=%v", a.Features())

	// --- a deterministic mock game
	mock, err := startMockMSDP()
	if err != nil {
		e.check(g, "start the mock MSDP game", false, "%v", err)
		return
	}
	defer mock.stop()
	sid, ok := e.connectWorld(g, "mockmsdp", &gameServer{name: "mockmsdp", port: mock.port})
	if !ok {
		return
	}
	_, ok = r.waitLine(sid, "Mock MSDP game", 8*time.Second)
	e.check(g, "connect to a game that offers MSDP", ok, "")
	_, _ = a.ControlPreset(tctx(5*time.Second), sid, "takeover")
	time.Sleep(600 * time.Millisecond) // TinyFugue answers WILL MSDP with DO

	r.mark()
	err = a.MsdpList(tctx(5*time.Second), sid, "COMMANDS")
	vars, ok := r.waitMsdp(sid, func(v map[string]any) bool { return v["COMMANDS"] != nil }, 6*time.Second)
	e.check(g, "LIST COMMANDS: an array comes back as a JSON array", err == nil && ok && arrayHas(vars["COMMANDS"], "REPORT"), "err=%v vars=%v", err, vars)

	r.mark()
	_ = a.MsdpSend(tctx(5*time.Second), sid, "ROOM")
	vars, ok = r.waitMsdp(sid, func(v map[string]any) bool { return v["ROOM"] != nil }, 6*time.Second)
	room, _ := vars["ROOM"].(map[string]any)
	e.check(g, "SEND ROOM: a table with an array inside, UTF-8 intact", ok && room["NAME"] == "Café ☕" && room["VNUM"] == "42" && arrayHas(room["EXITS"], "s"), "room=%v", vars["ROOM"])

	r.mark()
	_ = a.MsdpReport(tctx(5*time.Second), sid, "HEALTH")
	first, ok1 := r.waitMsdp(sid, func(v map[string]any) bool { return v["HEALTH"] != nil }, 6*time.Second)
	seen := map[any]bool{}
	if ok1 {
		seen[first["HEALTH"]] = true
	}
	deadline := time.Now().Add(4 * time.Second)
	for len(seen) < 3 && time.Now().Before(deadline) {
		if v, ok := r.waitMsdp(sid, func(v map[string]any) bool { return v["HEALTH"] != nil && !seen[v["HEALTH"]] }, time.Second); ok {
			seen[v["HEALTH"]] = true
		}
	}
	e.check(g, "REPORT HEALTH: the value is sent at once and again as it changes", ok1 && len(seen) >= 3, "values=%v", seen)

	_ = a.MsdpUnreport(tctx(5*time.Second), sid, "HEALTH")
	time.Sleep(700 * time.Millisecond)
	r.mark()
	_, noisy := r.waitMsdp(sid, func(v map[string]any) bool { return v["HEALTH"] != nil }, 1500*time.Millisecond)
	e.check(g, "UNREPORT stops the updates", !noisy, "")

	// --- replay: MSDP entries are stored with sequence numbers like lines
	_ = a.Detach(tctx(5*time.Second), sid)
	r.mark()
	_ = a.Attach(tctx(5*time.Second), sid, nil, 0)
	_, ok = r.waitMsdp(sid, func(v map[string]any) bool { return v["COMMANDS"] != nil || v["ROOM"] != nil }, 6*time.Second)
	e.check(g, "MSDP messages are part of the replayed history", ok, "")

	// --- refusals
	err = a.SendMsdp(tctx(5*time.Second), sid, map[string]any{"X": "a\x01b"})
	e.check(g, "reserved bytes in a value are refused", protoCode(err) == proto.ErrBadRequest, "err=%v", err)
	err = a.SendMsdp(tctx(5*time.Second), sid, map[string]any{})
	e.check(g, "an empty request is refused", protoCode(err) == proto.ErrBadRequest, "err=%v", err)
	_, _ = a.ControlSet(tctx(5*time.Second), sid, "input", "tf")
	err = a.MsdpList(tctx(5*time.Second), sid, "COMMANDS")
	e.check(g, "sending is refused while TinyFugue controls the session", protoCode(err) == "not_controller", "err=%v", err)
	_, _ = a.ControlSet(tctx(5*time.Second), sid, "input", "remote")

	// --- a real server (Evennia answers LIST and SEND)
	if e.evennia == nil || e.evSID == "" {
		e.skip(g, "Evennia MSDP", "no logged-in Evennia session")
		return
	}
	es := e.evSID
	r.mark()
	_ = a.MsdpList(tctx(5*time.Second), es, "COMMANDS")
	vars, ok = r.waitMsdp(es, func(v map[string]any) bool { return v["commands"] != nil }, 8*time.Second)
	e.check(g, "Evennia: LIST COMMANDS (array)", ok && arrayHas(vars["commands"], "report"), "vars=%v", vars)
	r.mark()
	_ = a.MsdpList(tctx(5*time.Second), es, "REPORTABLE_VARIABLES")
	vars, ok = r.waitMsdp(es, func(v map[string]any) bool { return v["reportable_variables"] != nil }, 8*time.Second)
	e.check(g, "Evennia: LIST REPORTABLE_VARIABLES", ok && arrayHas(vars["reportable_variables"], "name"), "vars=%v", vars)
	r.mark()
	_ = a.MsdpSend(tctx(5*time.Second), es, "name")
	vars, ok = r.waitMsdp(es, func(v map[string]any) bool { return v["send"] != nil }, 8*time.Second)
	tbl, _ := vars["send"].(map[string]any)
	e.check(g, "Evennia: SEND name (a table)", ok && tbl["name"] != nil, "vars=%v", vars)
}

// ---------------------------------------------------------------- 9. MTTS (opt-in)

// evenniaFlags opens a fresh connection (the terminal type is negotiated once,
// at connect), logs in and returns the protocol flags Evennia stored for it.
// Evennia allows one session per account, so this kicks any earlier login: run
// it after the scenarios that use the first session.
func (e *env) evenniaFlags(g, world string) (string, bool) {
	a, r := e.admin.c, e.admin
	sid, ok := e.connectWorld(g, world, e.evennia)
	if !ok {
		return "", false
	}
	if _, ok = r.waitLine(sid, "Welcome to testgame", 15*time.Second); !ok {
		return "", false
	}
	_, _ = a.ControlPreset(tctx(5*time.Second), sid, "takeover")
	r.mark()
	_ = a.SendSecret(tctx(5*time.Second), sid, "connect god godpw")
	if _, ok = r.waitLine(sid, "You become god", 15*time.Second); !ok {
		return "", false
	}
	r.mark()
	_ = a.Send(tctx(5*time.Second), sid, `py print("MTTS:", me.sessions.all()[-1].protocol_flags)`)
	l, ok := r.waitLine(sid, "'CLIENTNAME'", 10*time.Second)
	return lineText(l), ok
}

func (e *env) mttsScenario() {
	g := "mtts"
	if e.evennia == nil {
		e.skip(g, "MTTS", "no Evennia")
		return
	}
	a := e.admin.c
	set := func(v string) { _, _ = a.Exec(tctx(5*time.Second), "/set squat_mtts="+v) }

	set("off")
	f, ok := e.evenniaFlags(g, "ev-mtts-off")
	e.check(g, "default: TinyFugue answers as it always did (TINYFUGUE, then ANSI-ATTR), no truecolor", ok &&
		strings.Contains(f, "'CLIENTNAME': 'TINYFUGUE'") && strings.Contains(f, "'TERM': 'ANSI-ATTR'") && strings.Contains(f, "'TRUECOLOR': False"), "flags=%s", f)

	set("on")
	f, ok = e.evenniaFlags(g, "ev-mtts-on")
	e.check(g, "squat_mtts=on: MTTS is sent, the game learns UTF-8, no truecolor claimed", ok &&
		strings.Contains(f, "'CLIENTNAME': 'TINYFUGUE'") && strings.Contains(f, "'UTF-8': True") && strings.Contains(f, "'TRUECOLOR': False"), "flags=%s", f)

	set("client")
	f, ok = e.evenniaFlags(g, "ev-mtts-client")
	e.check(g, "squat_mtts=client: the game learns 256 colours, UTF-8 and truecolor", ok &&
		strings.Contains(f, "'TERM': 'XTERM-256COLOR'") && strings.Contains(f, "'UTF-8': True") && strings.Contains(f, "'TRUECOLOR': True"), "flags=%s", f)

	if e.cobra != nil {
		sid, ok := e.connectWorld(g, "cobra-mtts", e.cobra)
		if ok {
			r := e.admin
			_, _ = r.waitLine(sid, "CobraMUSH", 10*time.Second)
			_, _ = a.ControlPreset(tctx(5*time.Second), sid, "takeover")
			r.mark()
			_ = a.SendSecret(tctx(5*time.Second), sid, "connect #1 potrzebie")
			_, _ = r.waitLine(sid, "Welcome to CobraMUSH", 10*time.Second)
			r.mark()
			_ = a.Send(tctx(5*time.Second), sid, "think TERM2:[terminfo(me)]")
			l, ok := r.waitLine(sid, "TERM2:", 8*time.Second)
			e.check(g, "a game that reads only the first reply (CobraMUSH) is unaffected", ok && strings.Contains(strings.ToUpper(lineText(l)), "TINYFUGUE"), "line=%q", lineText(l))
		}
	}
	set("off")
}

// ---------------------------------------------------------------- 10. late attach: state snapshots

func (e *env) stateScenario() {
	g := "state"
	a, r := e.admin.c, e.admin
	if e.evSID == "" {
		e.skip(g, "snapshots", "no logged-in Evennia session")
		return
	}
	es := e.evSID
	r.mark()
	_ = a.Send(tctx(5*time.Second), es, `py me.msg(char_vitals=((), {"hp": 42}))`)
	_, ok := r.waitGmcp(es, "Char.Vitals", 8*time.Second)
	e.check(g, "set up: a GMCP value is on the wire", ok, "")
	_ = a.Detach(tctx(5*time.Second), es)
	r.mark()
	// attach with the smallest possible backlog: the data is far outside it
	_ = a.Attach(tctx(5*time.Second), es, nil, 1)
	ev, ok := r.waitEvent(6*time.Second, func(ev wire.Event) bool { _, is := ev.(wire.State); return is })
	var st wire.State
	if ok {
		st = ev.(wire.State)
	}
	var hp struct {
		HP float64 `json:"hp"`
	}
	_ = json.Unmarshal(st.Gmcp["Char.Vitals"], &hp)
	e.check(g, "attach with a 1-line backlog still delivers the latest GMCP values", ok && hp.HP == 42, "state=%+v", st.StateMsg)
	e.check(g, "the snapshot says which sequence number it is as of", ok && st.AsOf > 0 && st.Session == es, "asOf=%d", st.AsOf)
	got, err := a.GetState(tctx(5*time.Second), es)
	e.check(g, "state.get returns the same data on request", err == nil && string(got.Gmcp["Char.Vitals"]) == string(st.Gmcp["Char.Vitals"]), "err=%v", err)
}
