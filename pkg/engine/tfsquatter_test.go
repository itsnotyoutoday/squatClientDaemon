package engine_test

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/auth"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/msdp"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/testutil/tftest"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/wire"
)

// tfEnv is a REAL tf-squatterd bridge + wire server (TLS) in front of a FAKE
// TinyFugue (pkg/testutil/tftest).
type tfEnv struct {
	t    *testing.T
	sim  *tftest.Sim
	as   *auth.Store
	fake *tftest.Fake
	fp   string
	host string
	port int
	key  ed25519.PrivateKey
}

func newTFEnv(t *testing.T) *tfEnv {
	t.Helper()
	sim := tftest.Start(t)
	return &tfEnv{t: t, sim: sim, as: sim.Auth, fake: sim.Fake, fp: sim.Fingerprint, host: sim.Host, port: sim.Port, key: sim.Key}
}

func (v *tfEnv) cfg(id, role string) engine.SquatterConfig {
	v.t.Helper()
	code, _, err := v.as.NewPairingRole(0, role)
	if err != nil {
		v.t.Fatal(err)
	}
	return engine.SquatterConfig{ID: id, Name: "tinyfugue", Host: v.host, Port: v.port, Key: v.key, DeviceName: "test-device",
		Fingerprint: v.fp, PairCode: code, Kind: proto.KindTFSquatterd}
}

// connect builds an engine with a client-side trigger and connects the squatter.
func (v *tfEnv) connect(t *testing.T, mod ...func(*engine.SquatterConfig)) (*engine.Engine, *rec) {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, dir+"/startup.tf", "/def -t'*FIREME*' fire = reply-ok\n")
	e, r := newEngine(t, func(o *engine.Options) { o.ConfigDir = dir })
	_ = e.LoadScripts()
	cfg := v.cfg("tf", "admin")
	for _, m := range mod {
		m(&cfg)
	}
	if err := e.AddSquatter(cfg); err != nil {
		t.Fatal(err)
	}
	if err := e.ConnectSquatter(ctxT(t), "tf"); err != nil {
		t.Fatal(err)
	}
	return e, r
}

const tsid = "w-alpha-1-1"

func (v *tfEnv) session(t *testing.T, e *engine.Engine, r *rec) string {
	t.Helper()
	v.fake.AddSession(tsid, "alpha", "alpha.example", "4201")
	id := "tf:" + tsid
	r.until(t, "the session is mirrored", func() bool { _, err := e.Session(id); return err == nil })
	// wait for the attach to finish: what arrives before is replayed history, which
	// (by design) does not fire GMCP/MSDP events or triggers again
	r.until(t, "the session is attached", func() bool {
		si, err := e.Session(id)
		return err == nil && si.Attached >= 1
	})
	return id
}

func TestTFSquatterKindRoleAndSessions(t *testing.T) {
	v := newTFEnv(t)
	e, r := v.connect(t)
	st, _ := e.SquatterStatus("tf")
	if st.State != engine.SqConnected || st.Kind != proto.KindTFSquatterd || st.Role != "admin" || st.Instance != "inst-1" {
		t.Fatalf("status %+v", st)
	}
	has := map[string]bool{}
	for _, f := range st.Features {
		has[f] = true
	}
	for _, want := range []string{"tf", "control", "worlds", "worldedit", "gmcp", "msdp", "tfexec", "state", "noEcho"} {
		if !has[want] {
			t.Errorf("feature %q missing from %v", want, st.Features)
		}
	}
	id := v.session(t, e, r)
	si, _ := e.Session(id)
	if si.TF == nil || si.TF.World != "alpha" || si.TF.Input != "tf" || si.TF.Automation != "tf" || si.Via != "tf" {
		t.Fatalf("session info %+v", si)
	}
}

func TestTFSquatterWrongKindIsRefusedBeforeHello(t *testing.T) {
	v := newTFEnv(t)
	e, _ := newEngine(t)
	cfg := v.cfg("wrong", "admin")
	cfg.Kind = "squatterd" // the record says squatterd, the server is TinyFugue
	cfg.URL = "wss://" + net.JoinHostPort(v.host, strconv.Itoa(v.port)) + "/tfsp"
	if err := e.AddSquatter(cfg); err != nil {
		t.Fatal(err)
	}
	err := e.ConnectSquatter(ctxT(t), "wrong")
	var km *wire.KindMismatchError
	if !errors.As(err, &km) {
		t.Fatalf("err = %v, want a kind mismatch", err)
	}
	st, _ := e.SquatterStatus("wrong")
	if st.State != engine.SqRefused || st.Problem != engine.ProblemWrongKind || !strings.Contains(st.Message, "Nothing was sent") {
		t.Fatalf("status %+v", st)
	}
	// the pairing code must still be unused: a correct record pairs fine
	cfg2 := cfg
	cfg2.ID, cfg2.Kind, cfg2.URL = "right", proto.KindTFSquatterd, ""
	if err := e.AddSquatter(cfg2); err != nil {
		t.Fatal(err)
	}
	if err := e.ConnectSquatter(ctxT(t), "right"); err != nil {
		t.Fatalf("the code was consumed by the refused attempt: %v", err)
	}
}

func TestSquatterdRecordRefusesATinyFugueServerAndTheReverse(t *testing.T) {
	sq := newSqEnv(t)
	e, _ := newEngine(t)
	cfg := sq.cfg("x")
	cfg.Kind = proto.KindTFSquatterd // the record says TinyFugue, the server is a plain squatterd
	cfg.URL = "wss://" + net.JoinHostPort(sq.host, strconv.Itoa(sq.port)) + "/swp"
	if err := e.AddSquatter(cfg); err != nil {
		t.Fatal(err)
	}
	var km *wire.KindMismatchError
	if err := e.ConnectSquatter(ctxT(t), "x"); !errors.As(err, &km) || km.Got != proto.KindSquatterd {
		t.Fatalf("err = %v", err)
	}
	// existing records (no kind) keep working against a squatterd
	e2, _ := sq.connected(t)
	st, _ := e2.SquatterStatus("sq")
	if st.State != engine.SqConnected || st.Kind != proto.KindSquatterd {
		t.Fatalf("legacy record: %+v", st)
	}
}

func TestTFLinesGagDispAndEchoOrigin(t *testing.T) {
	v := newTFEnv(t)
	e, r := v.connect(t)
	id := v.session(t, e, r)
	v.fake.Line(tsid, "g", "", "game", "plain line")
	v.fake.Line(tsid, "g", "g", "game", "a gagged line")
	v.fake.Line(tsid, "g", "", "game", "shout", "\x1b[1mshout\x1b[0m")
	v.fake.Line(tsid, "e", "", "tf", "typed at the TF keyboard")
	v.fake.Line(tsid, "e", "", "someone-else", "typed by another device")
	r.until(t, "lines arrive", func() bool { return len(r.lines(id)) >= 4 })
	var texts []string
	var shout proto.Line
	for _, l := range r.lines(id) {
		texts = append(texts, l.Plain())
		if strings.Contains(l.Plain(), "shout") {
			shout = l
		}
	}
	all := strings.Join(texts, "|")
	if strings.Contains(all, "gagged") {
		t.Errorf("a line TinyFugue gagged was shown: %v", texts)
	}
	if !strings.Contains(all, "[tf] typed at the TF keyboard") || !strings.Contains(all, "[remote] typed by another device") {
		t.Errorf("echo origin tags missing: %v", texts)
	}
	if len(shout.Spans) == 0 || !shout.Spans[0].B {
		t.Errorf("TinyFugue's display text (hilite) was not used: %+v", shout.Spans)
	}
	// the user can choose to see gagged lines
	e.SetShowGagged(true)
	v.fake.Line(tsid, "g", "g", "game", "second gagged line")
	r.until(t, "gagged line shown when asked", func() bool { return r.has(id, "second gagged line") })
}

func TestTFInputNeedsControl(t *testing.T) {
	v := newTFEnv(t)
	e, r := v.connect(t)
	id := v.session(t, e, r)
	_ = e.Input(id, "look") // errors are shown in the session, like every send error
	r.until(t, "the user is told TinyFugue has the keyboard", func() bool { return r.has(id, "has the keyboard") })
	if got := v.fake.SendTexts(); len(got) != 0 {
		t.Fatalf("text reached TinyFugue although it holds the keyboard: %v", got)
	}
	if err := e.SetControl(id, "input", "remote"); err != nil {
		t.Fatal(err)
	}
	si, _ := e.Session(id)
	if si.TF.Input != "remote" || si.TF.ControllerDevice == "" {
		t.Fatalf("session info after takeover: %+v", si.TF)
	}
	_ = e.Input(id, "look")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(v.fake.SendTexts()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := v.fake.SendTexts(); len(got) != 1 || got[0] != "look" {
		t.Fatalf("TinyFugue received %v", got)
	}
}

func TestTFAutomationGateNoDoubleFiring(t *testing.T) {
	v := newTFEnv(t)
	e, r := v.connect(t)
	id := v.session(t, e, r)
	// TinyFugue owns automation: this client has a trigger for FIREME but must NOT fire it
	v.fake.Line(tsid, "g", "", "game", "FIREME")
	r.until(t, "the line is displayed", func() bool { return r.has(id, "FIREME") })
	time.Sleep(300 * time.Millisecond)
	if n := len(v.fake.SendTexts()); n != 0 {
		t.Fatalf("the client fired its own trigger while TinyFugue owns automation (%d sends)", n)
	}
	// a full takeover (input remote + automation client): now the client's trigger runs
	if err := e.ControlPreset(id, "full"); err != nil {
		t.Fatal(err)
	}
	v.fake.Line(tsid, "g", "", "game", "FIREME")
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && len(v.fake.SendTexts()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := v.fake.SendTexts(); len(got) != 1 || got[0] != "reply-ok" {
		t.Fatalf("the client's trigger did not fire after taking automation: %v", got)
	}
	// another device takes automation: this client must stay display-only
	code, _, _ := v.as.NewPairingRole(0, "operator")
	other, err := wire.Dial(ctxT(t), "wss://"+net.JoinHostPort(v.host, strconv.Itoa(v.port))+"/tfsp", wire.ClientOptions{
		Key: mustKey(t), PairCode: code, DeviceName: "other", PinnedFingerprint: v.fp, ExpectKind: proto.KindTFSquatterd, Caps: proto.DefaultCaps()})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.ControlSet(ctxT(t), tsid, "automation", "client"); err != nil {
		t.Fatal(err)
	}
	me := ""
	if st, _ := e.SquatterStatus("tf"); st.DeviceID != "" {
		me = st.DeviceID
	}
	r.until(t, "control moved to the other device", func() bool {
		si, _ := e.Session(id)
		return si.TF != nil && si.TF.ControllerDevice != "" && si.TF.ControllerDevice != me
	})
	// losing triggers to someone else is announced, never silent
	r.until(t, "a warning that the triggers moved", func() bool {
		for _, ev := range r.kinds(engine.EvNotice) {
			if ev.Session == id && ev.Level == engine.LevelWarn && strings.Contains(ev.Text, "another device") {
				return true
			}
		}
		return false
	})
	v.fake.Line(tsid, "g", "", "game", "FIREME")
	time.Sleep(400 * time.Millisecond)
	if n := len(v.fake.SendTexts()); n != 1 {
		t.Fatalf("fired while another device owns automation (%d sends)", n)
	}
}

func TestTFAutomationPreferenceClaimsOnAttach(t *testing.T) {
	v := newTFEnv(t)
	e, r := v.connect(t, func(c *engine.SquatterConfig) { c.Automation = "client" })
	id := v.session(t, e, r)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if si, _ := e.Session(id); si.TF != nil && si.TF.Automation == "client" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a squatter record with automation=client did not ask TinyFugue for automation")
}

func TestTFWorldsEditAndConnectByRef(t *testing.T) {
	v := newTFEnv(t)
	e, r := v.connect(t)
	if _, err := e.PutTFWorld("tf", wire.WorldPutReq{World: proto.TFWorld{Name: "newmud", Host: "mud.example", Port: "4000", Type: "tiny"}, Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	ws, err := e.TFWorlds("tf")
	if err != nil || len(ws) != 1 || ws[0].Name != "newmud" || !ws[0].PassSet {
		t.Fatalf("worlds %v %+v", err, ws)
	}
	raw, _ := json.Marshal(ws)
	if strings.Contains(string(raw), "\"pw\"") {
		t.Fatalf("a password came back: %s", raw)
	}
	// connect from the record with no Login given here: TinyFugue's own record logs in (autologin)
	si, err := e.Dial(engine.World{ID: "x", Name: "newmud", Via: "tf", Ref: "newmud"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if si.TF == nil || si.TF.World != "newmud" {
		t.Fatalf("session %+v", si)
	}
	_ = r
	if got := v.fake.SendTexts(); len(got) != 0 {
		t.Fatalf("the client sent a login line %v although TinyFugue logs in itself", got)
	}
	v.fake.Wait("CONNECT", 0)
	if err := e.DeleteTFWorld("tf", "newmud"); err != nil {
		t.Fatal(err)
	}
	if ws, _ := e.TFWorlds("tf"); len(ws) != 0 {
		t.Fatalf("world still listed: %+v", ws)
	}
	if err := e.DeleteTFWorld("tf", "newmud"); err == nil {
		t.Fatal("deleting a missing world did not fail")
	}
}

func TestTFOwnControlChangesAreNotAnnouncedAsLosses(t *testing.T) {
	v := newTFEnv(t)
	e, r := v.connect(t)
	id := v.session(t, e, r)
	if err := e.ControlPreset(id, "full"); err != nil {
		t.Fatal(err)
	}
	if err := e.ControlPreset(id, "viewer"); err != nil { // we gave it back ourselves
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	for _, ev := range r.kinds(engine.EvNotice) {
		if ev.Session == id && ev.Level == engine.LevelWarn {
			t.Fatalf("our own change was reported as a loss: %q", ev.Text)
		}
	}
}

func TestTFPlainHostPortConnectGetsTheClientsLoginLine(t *testing.T) {
	v := newTFEnv(t)
	e, _ := v.connect(t)
	// no world record in TinyFugue: nothing there logs in, so the client does (as with squatterd)
	if _, err := e.Dial(engine.World{ID: "x", Name: "adhoc", Host: "mud.example", Port: 4000, Via: "tf",
		Login: &engine.Login{Template: "connect {name} {password}", CharName: "bob"}}, "hunter2"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && len(v.fake.SendTexts()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := v.fake.SendTexts(); len(got) != 1 || got[0] != "connect bob hunter2" {
		t.Fatalf("login line sent: %v", got)
	}
}

func TestTFOperationsRefuseAPlainSquatter(t *testing.T) {
	sq := newSqEnv(t)
	e, _ := sq.connected(t)
	if _, err := e.TFWorlds("sq"); !errors.Is(err, engine.ErrNotTF) {
		t.Fatalf("TFWorlds on a squatterd: %v", err)
	}
	if _, err := e.TFExec("sq", "/echo x"); !errors.Is(err, engine.ErrNotTF) {
		t.Fatalf("TFExec on a squatterd: %v", err)
	}
	if err := e.SetControl("nope", "input", "tf"); err == nil {
		t.Fatal("SetControl on an unknown session did not fail")
	}
}

func TestTFExecAndForbidden(t *testing.T) {
	v := newTFEnv(t)
	e, _ := v.connect(t)
	res, err := e.TFExec("tf", "/echo hi")
	if err != nil || !res.OK || len(res.Out) != 1 || res.Out[0] != "ran: /echo hi" {
		t.Fatalf("%v %+v", err, res)
	}
	v.fake.ExecOn = false
	if _, err := e.TFExec("tf", "/echo hi"); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("err = %v, want forbidden", err)
	}
}

func TestTFGmcpMsdpEventsAndSending(t *testing.T) {
	v := newTFEnv(t)
	e, r := v.connect(t)
	id := v.session(t, e, r)
	v.fake.Send("GMCP", tsid, "Char.Vitals", `{"hp":7}`)
	p, _ := msdpPayload(map[string]any{"HEALTH": "88", "ROOM": map[string]any{"NAME": "hall"}})
	v.fake.Send("MSDP", tsid, p)
	r.until(t, "gmcp and msdp events", func() bool { return len(r.kinds(engine.EvGmcp)) == 1 && len(r.kinds(engine.EvMsdp)) == 1 })
	g := r.kinds(engine.EvGmcp)[0].Gmcp
	if g.Package != "Char.Vitals" || string(g.Data) != `{"hp":7}` {
		t.Fatalf("gmcp %+v", g)
	}
	m := r.kinds(engine.EvMsdp)[0].Msdp
	if m.Vars["HEALTH"] != "88" || m.Vars["ROOM"].(map[string]any)["NAME"] != "hall" {
		t.Fatalf("msdp %+v", m)
	}
	// sending needs control
	if err := e.SendMsdp(id, map[string]any{"REPORT": []string{"HEALTH"}}); err != nil {
		t.Fatal(err) // accepted by the bridge; TinyFugue answers (the fake answers OK)
	}
	if err := e.SendGmcp(id, "Core.Hello", map[string]any{"client": "x"}); err != nil {
		t.Fatal(err)
	}
	d, err := e.SessionData(id)
	if err != nil || d.Msdp["HEALTH"] != "88" || !strings.Contains(string(d.Gmcp["Char.Vitals"]), "7") {
		t.Fatalf("session data %v %+v", err, d)
	}
}

func TestTFControlPendingAndAsk(t *testing.T) {
	v := newTFEnv(t)
	v.fake.Ask = true
	e, r := v.connect(t)
	id := v.session(t, e, r)
	done := make(chan error, 1)
	go func() { done <- e.SetControl(id, "input", "remote") }()
	r.until(t, "control.pending reaches the UI", func() bool { return len(r.kinds(engine.EvControlPending)) == 1 })
	p := r.kinds(engine.EvControlPending)[0].Pending
	if p.Session != id || p.Axis != "input" || p.Mode != "remote" {
		t.Fatalf("pending %+v", p)
	}
	v.fake.Answer(false)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err = %v, want denied", err)
	}
	go func() { done <- e.SetControl(id, "input", "remote") }()
	r.until(t, "second request pending", func() bool { return len(r.kinds(engine.EvControlPending)) == 2 })
	v.fake.Answer(true)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if si, _ := e.Session(id); si.TF.Input != "remote" {
		t.Fatalf("after allow: %+v", si.TF)
	}
}

func TestTFWorldChangedByAnotherDeviceNotifies(t *testing.T) {
	v := newTFEnv(t)
	e, r := v.connect(t)
	// another device edits a world through the same bridge
	code, _, _ := v.as.NewPairingRole(0, "operator")
	other, err := wire.Dial(ctxT(t), "wss://"+net.JoinHostPort(v.host, strconv.Itoa(v.port))+"/tfsp", wire.ClientOptions{
		Key: mustKey(t), PairCode: code, DeviceName: "other", PinnedFingerprint: v.fp, ExpectKind: proto.KindTFSquatterd, Caps: proto.DefaultCaps()})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.PutWorld(ctxT(t), wire.WorldPutReq{World: proto.TFWorld{Name: "viaother", Host: "h", Port: "1"}}); err != nil {
		t.Fatal(err)
	}
	r.until(t, "the UI is told the world list changed", func() bool {
		for _, ev := range r.kinds(engine.EvTFWorlds) {
			if ev.Text == "tf" {
				return true
			}
		}
		return false
	})
	_ = e
}

func mustKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	k, err := auth.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func msdpPayload(vars map[string]any) (string, error) {
	p, err := msdp.Encode(msdp.FromMap(vars))
	return string(p), err
}

func TestTFScriptsUploadListDelete(t *testing.T) {
	v := newTFEnv(t)
	e, _ := v.connect(t)
	res, err := e.PutTFScript("tf", "hello.tf", "/def x = /echo hi\n", true)
	if err != nil || !res.Stored || !res.Loaded {
		t.Fatalf("put: %v %+v", err, res)
	}
	if got := v.fake.LoadedScripts(); len(got) != 1 || !strings.HasSuffix(got[0], "/scripts/hello.tf") {
		t.Fatalf("TinyFugue was asked to load %v", got)
	}
	ls, err := e.TFScripts("tf")
	if err != nil || len(ls) != 1 || ls[0].Name != "hello.tf" || ls[0].Size == 0 {
		t.Fatalf("list: %v %+v", err, ls)
	}
	if _, err := e.PutTFScript("tf", "../evil.tf", "x", false); err == nil {
		t.Fatal("a path in the name was accepted")
	}
	if err := e.DeleteTFScript("tf", "hello.tf"); err != nil {
		t.Fatal(err)
	}
	if ls, _ := e.TFScripts("tf"); len(ls) != 0 {
		t.Fatalf("still listed: %+v", ls)
	}
	v.fake.ScriptsOn = false
	if _, err := e.PutTFScript("tf", "off.tf", "x", false); err == nil {
		t.Fatal("upload accepted while TinyFugue has scripts off")
	}
	if ls, _ := e.TFScripts("tf"); len(ls) != 0 {
		t.Fatalf("a refused upload left a file: %+v", ls)
	}
}

func TestTFWorldRename(t *testing.T) {
	v := newTFEnv(t)
	e, _ := v.connect(t)
	if _, err := e.PutTFWorld("tf", wire.WorldPutReq{World: proto.TFWorld{Name: "old", Host: "h", Port: "1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PutTFWorld("tf", wire.WorldPutReq{World: proto.TFWorld{Name: "taken", Host: "h", Port: "2"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.RenameTFWorld("tf", "old", "new", false); err != nil {
		t.Fatal(err)
	}
	ws, _ := e.TFWorlds("tf")
	names := map[string]bool{}
	for _, w := range ws {
		names[w.Name] = true
	}
	if names["old"] || !names["new"] || !names["taken"] || len(ws) != 2 {
		t.Fatalf("after rename: %+v", ws)
	}
	if err := e.RenameTFWorld("tf", "new", "taken", false); err == nil {
		t.Fatal("renaming onto an existing world was accepted")
	}
	if err := e.RenameTFWorld("tf", "ghost", "x", false); err == nil {
		t.Fatal("renaming a missing world was accepted")
	}
	for _, bad := range []string{"has space", "(paren", ""} {
		if err := e.RenameTFWorld("tf", "new", bad, false); err == nil {
			t.Fatalf("name %q was accepted", bad)
		}
	}
}

func TestTFRenameIsFollowedByTheSessionAndAnnounced(t *testing.T) {
	v := newTFEnv(t)
	e, r := v.connect(t)
	id := v.session(t, e, r)
	// renamed from this client
	if _, err := e.PutTFWorld("tf", wire.WorldPutReq{World: proto.TFWorld{Name: "alpha", Host: "h", Port: "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.RenameTFWorld("tf", "alpha", "alpha2", true); err != nil {
		t.Fatal(err)
	}
	r.until(t, "the tab follows the new name", func() bool {
		si, _ := e.Session(id)
		return si.Name == "alpha2" && si.TF != nil && si.TF.World == "alpha2"
	})
	r.until(t, "the rename is announced with both names", func() bool {
		for _, ev := range r.kinds(engine.EvTFWorlds) {
			if ev.OldName == "alpha" && ev.NewName == "alpha2" {
				return true
			}
		}
		return false
	})
	// renamed on TinyFugue's side
	v.fake.RenameSessionWorld("alpha2", "alpha3")
	r.until(t, "a rename made in TinyFugue reaches the tab too", func() bool {
		si, _ := e.Session(id)
		return si.Name == "alpha3"
	})
	// and a change at its keyboard (add/delete/redefine) tells us to re-list
	n := len(r.kinds(engine.EvTFWorlds))
	v.fake.WorldsChanged()
	r.until(t, "a world-list change in TinyFugue is announced", func() bool { return len(r.kinds(engine.EvTFWorlds)) > n })
}

func TestTFRecordConnectWithALoginHereSendsTheClientsLogin(t *testing.T) {
	v := newTFEnv(t)
	e, _ := v.connect(t)
	if _, err := e.PutTFWorld("tf", wire.WorldPutReq{World: proto.TFWorld{Name: "mirrored", Host: "h", Port: "1"}}); err != nil {
		t.Fatal(err)
	}
	// a world of the app that is mirrored into TinyFugue without credentials: this client logs in
	if _, err := e.Dial(engine.World{ID: "x", Name: "mirrored", Via: "tf", Ref: "mirrored",
		Login: &engine.Login{Template: "connect {name} {password}", CharName: "bob"}}, "hunter2"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && len(v.fake.SendTexts()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := v.fake.SendTexts(); len(got) != 1 || got[0] != "connect bob hunter2" {
		t.Fatalf("login line sent: %v", got)
	}
}

// The game type of a TinyFugue session that was already running is found by one probe when the user
// takes the keyboard; while TinyFugue holds it nothing is sent and no attempt is used up.
func TestTFGameTypeIsProbedWhenTheKeyboardIsTaken(t *testing.T) {
	v := newTFEnv(t)
	v.fake.OnSend = func(sid, text string) {
		switch {
		case text == "think [version()]":
			v.fake.Line(sid, "g", "", "game", "PennMUSH version 1.8.8 patchlevel 3 [11/01/2025]")
		case strings.HasPrefix(text, "think "):
			v.fake.Line(sid, "g", "", "game", strings.TrimPrefix(text, "think "))
		}
	}
	e, r := v.connect(t)
	id := v.session(t, e, r)
	time.Sleep(600 * time.Millisecond)
	if got := v.fake.SendTexts(); len(got) != 0 {
		t.Fatalf("something was sent while TinyFugue holds the keyboard: %v", got)
	}
	if si, _ := e.Session(id); si.GameType != "" {
		t.Fatalf("type %q without any evidence", si.GameType)
	}
	if err := e.SetControl(id, "input", "both"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 12*time.Second, "the probe finds the server", func() bool {
		si, _ := e.Session(id)
		return si.GameType == "pennmush" && si.GameTypeSource == "probe"
	})
}

func TestTFProbeAnswerThatIsNotAMUSHLeavesTheTypeUnknown(t *testing.T) {
	v := newTFEnv(t)
	v.fake.OnSend = func(sid, text string) {
		switch {
		case strings.HasPrefix(text, "think [version()]"), text == "@version":
			v.fake.Line(sid, "g", "", "game", "Huh?  I don't understand that.")
		case strings.HasPrefix(text, "think "):
			v.fake.Line(sid, "g", "", "game", strings.TrimPrefix(text, "think "))
		}
	}
	e, r := v.connect(t)
	id := v.session(t, e, r)
	if err := e.SetControl(id, "input", "both"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 12*time.Second, "the probe was sent", func() bool {
		for _, s := range v.fake.SendTexts() {
			if s == "think [version()]" {
				return true
			}
		}
		return false
	})
	time.Sleep(800 * time.Millisecond)
	if si, _ := e.Session(id); si.GameType != "" {
		t.Fatalf("an unclear answer made it %q; it must stay unknown", si.GameType)
	}
}

func waitFor(t *testing.T, d time.Duration, what string, pred func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if pred() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}
