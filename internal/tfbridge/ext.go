package tfbridge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/msdp"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// presets maps a preset name to (input, automation).
var presets = map[string][2]string{
	"viewer":   {wire.ModeTF, wire.ModeTF},
	"copilot":  {wire.ModeBoth, wire.ModeTF},
	"takeover": {wire.ModeRemote, wire.ModeTF},
	"full":     {wire.ModeRemote, wire.ModeClient},
}

// async runs a slow request off the client's read loop, so the client can keep
// sending (text, resize, other requests) while it waits. An error becomes an
// error frame carrying the request id, exactly as for a synchronous handler.
func (b *Bridge) async(p wire.Peer, reqID string, fn func() error) {
	go func() {
		err := fn()
		if err == nil {
			return
		}
		var pe *wire.ProtoError
		if errors.As(err, &pe) {
			p.Send(wire.NewError(reqID, pe.Code, pe.Message))
			return
		}
		b.log.Error("tfbridge: request failed", "err", err)
		p.Send(wire.NewError(reqID, proto.ErrInternal, "internal error"))
	}()
}

// HandleExt implements wire.ExtBackend: control.* and world.*.
func (b *Bridge) HandleExt(p wire.Peer, t, reqID string, raw []byte) (bool, error) {
	switch t {
	case wire.TControlGet:
		return true, b.controlGet(p, reqID, raw)
	case wire.TControlSet:
		// may wait for the TinyFugue user (squat_takeover=ask): never block the client's read loop
		b.async(p, reqID, func() error { return b.controlSet(p, reqID, raw) })
		return true, nil
	case wire.TWorldList:
		return true, b.worldList(p, reqID)
	case wire.TWorldPut:
		return true, b.worldPut(p, reqID, raw)
	case wire.TWorldDelete:
		return true, b.worldDelete(p, reqID, raw)
	case wire.TGmcpSend:
		return true, b.gmcpSend(p, reqID, raw)
	case wire.TStateGet:
		return true, b.stateGet(p, reqID, raw)
	case wire.TMsdpSend:
		return true, b.msdpSend(p, reqID, raw)
	case wire.TExec:
		b.async(p, reqID, func() error { return b.tfExec(p, reqID, raw) })
		return true, nil
	case wire.TScriptPut:
		b.async(p, reqID, func() error { return b.scriptPut(p, reqID, raw) })
		return true, nil
	case wire.TScriptList:
		return true, b.scriptList(p, reqID)
	case wire.TScriptDelete:
		return true, b.scriptDelete(p, reqID, raw)
	}
	return false, nil
}

func (b *Bridge) controlGet(p wire.Peer, reqID string, raw []byte) error {
	var req wire.ControlGetReq
	if err := json.Unmarshal(raw, &req); err != nil {
		return wire.Errf(proto.ErrBadRequest, "bad control.get")
	}
	if req.Session != "" {
		s := b.session(req.Session)
		if s == nil {
			return wire.Errf(proto.ErrNoSuchSession, "no such session")
		}
		p.Send(s.controlMsg(reqID))
		return nil
	}
	for _, s := range b.sessionList() {
		p.Send(s.controlMsg(reqID))
	}
	return nil
}

func (b *Bridge) controlSet(p wire.Peer, reqID string, raw []byte) error {
	if err := need(p, rankOperator, "changing control"); err != nil {
		return err
	}
	var req wire.ControlSetReq
	if err := json.Unmarshal(raw, &req); err != nil || req.Session == "" {
		return wire.Errf(proto.ErrBadRequest, "bad control.set")
	}
	type change struct{ axis, mode string }
	var changes []change
	if req.Preset != "" {
		pr, ok := presets[req.Preset]
		if !ok {
			return wire.Errf(proto.ErrBadRequest, "unknown preset")
		}
		changes = []change{{wire.AxisInput, pr[0]}, {wire.AxisAutomation, pr[1]}}
	} else {
		axis := req.Axis
		if axis == "" {
			axis = wire.AxisInput
		}
		changes = []change{{axis, req.Mode}}
	}
	var targets []*session
	if req.Session == "*" {
		targets = b.sessionList()
	} else {
		s := b.session(req.Session)
		if s == nil {
			return wire.Errf(proto.ErrNoSuchSession, "no such session")
		}
		targets = []*session{s}
	}
	// Record who is taking control BEFORE asking TinyFugue: its answer (and the
	// control.state it triggers) can reach the client before this function
	// returns, and that client may resize or type immediately. Roll back on error.
	type prev struct {
		s          *session
		ctl, cname string
	}
	var prevs []prev
	takes := false
	for _, c := range changes {
		takes = takes || c.mode != wire.ModeTF
	}
	if takes {
		for _, s := range targets {
			s.mu.Lock()
			prevs = append(prevs, prev{s, s.controller, s.ctrlName})
			s.controller, s.ctrlName = p.DeviceID(), p.DeviceName()
			s.mu.Unlock()
		}
	}
	var pairs [][2]string
	for _, c := range changes {
		pairs = append(pairs, [2]string{c.axis, c.mode})
	}
	for _, s := range targets {
		s.expectControl(pairs, 50*time.Second)
	}
	for _, c := range changes {
		// TinyFugue may ask its user first (squat_takeover=ask): give them time to answer
		if _, err := b.requestT(45*time.Second, "CTRL_SET", req.Session, c.axis, c.mode, p.DeviceName()); err != nil {
			for _, s := range targets {
				s.dropExpect()
			}
			for _, pv := range prevs {
				pv.s.mu.Lock()
				pv.s.controller, pv.s.ctrlName = pv.ctl, pv.cname
				pv.s.mu.Unlock()
			}
			return err
		}
	}
	for _, s := range targets {
		s.endExpect(1500 * time.Millisecond) // late reports of the final state still match; stale earlier steps are ignored
		// TinyFugue confirms with a CTRL event on its next loop pass, which can
		// arrive after this reply: apply the change we just had acknowledged so the
		// reply never shows the old state (the later CTRL event then changes nothing).
		s.mu.Lock()
		for _, c := range changes {
			if c.axis == wire.AxisInput {
				s.input = c.mode
			} else {
				s.automation = c.mode
			}
		}
		if s.input == wire.ModeTF && s.automation == wire.ModeTF {
			s.controller, s.ctrlName = "", ""
		}
		s.broadcastLocked()
		s.mu.Unlock()
		msg := s.controlMsg(reqID)
		for _, q := range b.peerList() {
			q.Send(msg)
		}
	}
	return nil
}

func (b *Bridge) worldList(p wire.Peer, reqID string) error {
	b.worldsMu.Lock()
	defer b.worldsMu.Unlock()
	b.mu.Lock()
	b.worldAcc = nil
	b.mu.Unlock()
	if _, err := b.request("WORLDS"); err != nil {
		return err
	}
	b.mu.Lock()
	ws := b.worldAcc
	b.worldAcc = nil
	b.mu.Unlock()
	if ws == nil {
		ws = []proto.TFWorld{}
	}
	if rank(p) < rankAdmin {
		for i := range ws {
			ws[i].MFile = "" // a macro file is code TinyFugue loads: admins only
		}
	}
	p.Send(wire.WorldListMsg{V: proto.Version, T: wire.TWorldList, ReqID: reqID, Worlds: ws})
	return nil
}

// ---------------------------------------------------------------- worlds

func validWorldName(n string) bool {
	return n != "" && len(n) <= 128 && !strings.ContainsAny(n, " \t\r\n") && !strings.HasPrefix(n, "(")
}

func boolField(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func (b *Bridge) broadcastWorldChanged(msg wire.WorldChangedMsg) {
	for _, q := range b.peerList() {
		q.Send(msg)
	}
}

// worldPut creates or edits a TinyFugue world record. The password is write-only;
// a macro file (code TinyFugue loads on connect) is admin-only.
func (b *Bridge) worldPut(p wire.Peer, reqID string, raw []byte) error {
	if err := need(p, rankOperator, "editing worlds"); err != nil {
		return err
	}
	var req wire.WorldPutReq
	if err := json.Unmarshal(raw, &req); err != nil {
		return wire.Errf(proto.ErrBadRequest, "bad world.put")
	}
	w := req.World
	if !validWorldName(w.Name) {
		return wire.Errf(proto.ErrBadRequest, "illegal world name")
	}
	for _, v := range []string{w.Host, w.Port, w.Type, w.Char, w.Charset, w.MFile, req.Password} {
		if len(v) > 1024 || strings.ContainsAny(v, "\x00\r\n") {
			return wire.Errf(proto.ErrBadRequest, "a world field is too long or contains a control character")
		}
	}
	passMode := "keep"
	switch {
	case req.Password != "":
		passMode = "set"
	case req.ClearPassword:
		passMode = "clear"
	}
	mfile, mfileMode := "", "keep"
	if req.SetMFile {
		if rank(p) < rankAdmin {
			return wire.Errf(proto.ErrForbidden, "only an admin device may set a macro file")
		}
		mfile, mfileMode = w.MFile, "set"
	}
	m, err := b.request("WORLD_PUT", w.Name, w.Host, w.Port, w.Type, w.Char, passMode, req.Password,
		boolField(w.TLS), boolField(w.Echo), w.Charset, mfile, mfileMode, boolField(req.Save))
	if err != nil {
		return err
	}
	b.broadcastWorldChanged(wire.WorldChangedMsg{V: proto.Version, T: wire.TWorldChanged, ReqID: reqID, Name: w.Name,
		Created: field(m, 2) == "created"})
	return nil
}

func (b *Bridge) worldDelete(p wire.Peer, reqID string, raw []byte) error {
	if err := need(p, rankOperator, "deleting worlds"); err != nil {
		return err
	}
	var req wire.WorldDeleteReq
	if err := json.Unmarshal(raw, &req); err != nil || !validWorldName(req.Name) {
		return wire.Errf(proto.ErrBadRequest, "bad world.delete")
	}
	if _, err := b.request("WORLD_DEL", req.Name); err != nil {
		return err
	}
	b.broadcastWorldChanged(wire.WorldChangedMsg{V: proto.Version, T: wire.TWorldChanged, ReqID: reqID, Name: req.Name, Deleted: true})
	return nil
}

// ---------------------------------------------------------------- gmcp, exec

var pkgRE = regexp.MustCompile(`^[A-Za-z0-9_.]{1,128}$`)

// gmcpSend sends "Package {json}" to the game. TinyFugue enforces that the
// device controls the session.
func (b *Bridge) gmcpSend(p wire.Peer, reqID string, raw []byte) error {
	if err := need(p, rankOperator, "sending GMCP"); err != nil {
		return err
	}
	var req wire.GmcpSendReq
	if err := json.Unmarshal(raw, &req); err != nil || !pkgRE.MatchString(req.Package) {
		return wire.Errf(proto.ErrBadRequest, "bad gmcp.send")
	}
	if b.session(req.Session) == nil {
		return wire.Errf(proto.ErrNoSuchSession, "no such session")
	}
	text := req.Package
	if d := strings.TrimSpace(string(req.Data)); d != "" && d != "null" {
		if !json.Valid([]byte(d)) || len(d) > wire.MaxSendBytes {
			return wire.Errf(proto.ErrBadRequest, "gmcp data must be valid JSON")
		}
		text += " " + d
	}
	if _, err := b.request("GMCP_SEND", req.Session, text); err != nil {
		return err
	}
	p.Send(map[string]any{"v": proto.Version, "t": "ok", "reqId": reqID})
	return nil
}

// tfExec runs a TinyFugue command for an admin and returns what it printed.
func (b *Bridge) tfExec(p wire.Peer, reqID string, raw []byte) error {
	if err := need(p, rankAdmin, "running TinyFugue commands"); err != nil {
		return err
	}
	var req wire.ExecReq
	if err := json.Unmarshal(raw, &req); err != nil || req.Line == "" {
		return wire.Errf(proto.ErrBadRequest, "bad tf.exec")
	}
	m, err := b.requestT(20*time.Second, "EXEC", req.Line)
	if err != nil {
		return err
	}
	out := []string{}
	if len(m) > 3 {
		out = m[3:]
	}
	for i := range out {
		out[i] = strings.ToValidUTF8(out[i], "?")
	}
	p.Send(wire.ExecResultMsg{V: proto.Version, T: wire.TExecResult, ReqID: reqID, OK: field(m, 2) == "1", Out: out})
	return nil
}

// msdpSend encodes variables as an MSDP payload and has TinyFugue send them.
// TinyFugue enforces that the device controls the session.
func (b *Bridge) msdpSend(p wire.Peer, reqID string, raw []byte) error {
	if err := need(p, rankOperator, "sending MSDP"); err != nil {
		return err
	}
	var req wire.MsdpSendReq
	if err := json.Unmarshal(raw, &req); err != nil || len(req.Vars) == 0 {
		return wire.Errf(proto.ErrBadRequest, "bad msdp.send")
	}
	if b.session(req.Session) == nil {
		return wire.Errf(proto.ErrNoSuchSession, "no such session")
	}
	payload, err := msdp.Encode(msdp.FromMap(req.Vars))
	if err != nil {
		return wire.Errf(proto.ErrBadRequest, err.Error())
	}
	if _, err := b.request("MSDP_SEND", req.Session, string(payload)); err != nil {
		return err
	}
	p.Send(map[string]any{"v": proto.Version, "t": "ok", "reqId": reqID})
	return nil
}

// stateGet sends the latest GMCP/MSDP values of a session.
func (b *Bridge) stateGet(p wire.Peer, reqID string, raw []byte) error {
	var req wire.StateGetReq
	if err := json.Unmarshal(raw, &req); err != nil || req.Session == "" {
		return wire.Errf(proto.ErrBadRequest, "bad state.get")
	}
	s := b.session(req.Session)
	if s == nil {
		return wire.Errf(proto.ErrNoSuchSession, "no such session")
	}
	s.mu.Lock()
	msg := s.snapshotLocked(reqID)
	s.mu.Unlock()
	p.Send(msg)
	return nil
}

// ---------------------------------------------------------------- scripts

var scriptNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,59}\.tf$`)

func (b *Bridge) scriptsDir() string { return filepath.Join(b.cfg.DataDir, "scripts") }

func scriptName(_ []byte, name string) error {
	if !scriptNameRE.MatchString(name) {
		return wire.Errf(proto.ErrBadRequest, "a script name is letters, digits, . _ - and ends in .tf")
	}
	return nil
}

// scriptPut stores a script file and optionally has TinyFugue load it. TinyFugue
// decides whether scripts are allowed at all (squat_scripts) and loads them inside
// its sandbox; the bridge only ever writes into its own scripts directory.
func (b *Bridge) scriptPut(p wire.Peer, reqID string, raw []byte) error {
	if err := need(p, rankAdmin, "uploading scripts"); err != nil {
		return err
	}
	var req wire.ScriptPutReq
	if err := json.Unmarshal(raw, &req); err != nil {
		return wire.Errf(proto.ErrBadRequest, "bad tf.script.put")
	}
	if err := scriptName(raw, req.Name); err != nil {
		return err
	}
	if len(req.Content) > wire.MaxScriptBytes || !utf8.ValidString(req.Content) || strings.ContainsRune(req.Content, 0) {
		return wire.Errf(proto.ErrBadRequest, "a script must be UTF-8 text of at most 256 KiB")
	}
	// TinyFugue first: is uploading allowed right now? (nothing is written when it is not)
	if _, err := b.request("SCRIPT", "check"); err != nil {
		return err
	}
	dir := b.scriptsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return wire.Errf(proto.ErrInternal, "cannot create the scripts directory")
	}
	path := filepath.Join(dir, req.Name)
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return wire.Errf(proto.ErrInternal, "cannot write the script")
	}
	_, werr := tmp.WriteString(req.Content)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Chmod(tmp.Name(), 0o600) != nil || os.Rename(tmp.Name(), path) != nil {
		_ = os.Remove(tmp.Name())
		return wire.Errf(proto.ErrInternal, "cannot write the script")
	}
	res := wire.ScriptResultMsg{V: proto.Version, T: wire.TScriptResult, ReqID: reqID, Stored: true, Out: []string{}}
	if req.Load {
		m, err := b.requestT(30*time.Second, "SCRIPT", "load", path)
		if err != nil {
			return err
		}
		res.Loaded = field(m, 2) == "1"
		if len(m) > 3 {
			res.Out = m[3:]
		}
		for i := range res.Out {
			res.Out[i] = strings.ToValidUTF8(res.Out[i], "?")
		}
	}
	p.Send(res)
	return nil
}

func (b *Bridge) scriptList(p wire.Peer, reqID string) error {
	if err := need(p, rankAdmin, "listing scripts"); err != nil {
		return err
	}
	out := []wire.ScriptInfo{}
	ents, err := os.ReadDir(b.scriptsDir())
	if err != nil && !os.IsNotExist(err) {
		return wire.Errf(proto.ErrInternal, "cannot read the scripts directory")
	}
	for _, e := range ents {
		if !scriptNameRE.MatchString(e.Name()) {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.Mode().IsRegular() {
			out = append(out, wire.ScriptInfo{Name: e.Name(), Size: fi.Size(), Mtime: fi.ModTime().UnixMilli()})
		}
	}
	p.Send(wire.ScriptListMsg{V: proto.Version, T: wire.TScriptList, ReqID: reqID, Scripts: out})
	return nil
}

func (b *Bridge) scriptDelete(p wire.Peer, reqID string, raw []byte) error {
	if err := need(p, rankAdmin, "deleting scripts"); err != nil {
		return err
	}
	var req wire.ScriptDeleteReq
	if err := json.Unmarshal(raw, &req); err != nil {
		return wire.Errf(proto.ErrBadRequest, "bad tf.script.delete")
	}
	if err := scriptName(raw, req.Name); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(b.scriptsDir(), req.Name)); err != nil {
		return wire.Errf(proto.ErrBadRequest, "no such script")
	}
	p.Send(map[string]any{"v": proto.Version, "t": "ok", "reqId": reqID})
	return nil
}
