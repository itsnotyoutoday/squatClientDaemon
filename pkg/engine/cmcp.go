package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/itsnotyoutoday/cmcp/frame"
	"github.com/itsnotyoutoday/cmcp/msg"
	cs "github.com/itsnotyoutoday/cmcp/session"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// CMCP (the Cobra console protocol, cmcp/spec) in the engine. One cs.Client per game session
// (so per-session isolation is the same as for the graphics); the protocol logic is the shared Go core,
// this file only carries bytes in and out, throttles UI updates and publishes events:
//
//	game GMCP frame -> onGmcp -> Client.Receive -> Drain -> frames out (Transport.SendFrame), notes -> EvCmcpNote
//	user action     -> Engine.CmcpCommand / CmcpAcquire ... -> Client -> Drain -> frames out
//	model changes   -> EvCmcp (CmcpUI snapshot, at most every cmcpThrottle)
//
// Direct sessions only: sessions held by a squatter need the "cmcp" SWP extension (spec 2.7, workstream W).

const (
	cmcpThrottle = 80 * time.Millisecond
	cmcpTick     = 500 * time.Millisecond
	// the offer is repeated once if the server stays silent (spec 1.6)
	cmcpRedeclare = 10 * time.Second
)

// ErrCmcpOff is returned when the session does not speak CMCP (yet).
var ErrCmcpOff = errors.New("engine: this session does not speak CMCP")

// frameSender is implemented by transports that can write raw telnet bytes (*conn.Conn).
type frameSender interface{ SendFrame(b []byte) error }

// SetCmcp enables CMCP for sessions opened from now on; cfg is called once per session to build its Hello
// description (nil disables). The Hello can change later through CmcpUpdateConfig.
func (e *Engine) SetCmcp(cfg func() cs.Config) {
	e.cmcpMu.Lock()
	e.cmcpCfg = cfg
	e.cmcpMu.Unlock()
}

// cmcpOn reports whether CMCP is enabled (SetCmcp was given a config source).
func (e *Engine) cmcpOn() bool {
	e.cmcpMu.Lock()
	defer e.cmcpMu.Unlock()
	return e.cmcpCfg != nil
}

// CmcpUI is the UI model of one session as the front end receives it (snake_case inside the nested
// protocol messages, camelCase for the fields the client adds).
type CmcpUI struct {
	Session    string                     `json:"session"`
	Rev        uint64                     `json:"rev"`
	Generation int                        `json:"generation"`
	State      string                     `json:"state"`
	Proto      int                        `json:"proto"`
	Server     *msg.Server                `json:"server,omitempty"`
	Welcome    *msg.Welcome               `json:"welcome,omitempty"`
	Ship       *msg.ShipInfo              `json:"ship,omitempty"`
	Console    *msg.ConsoleRef            `json:"console,omitempty"`
	ConsoleRev int                        `json:"consoleRev"`
	Reason     string                     `json:"reason,omitempty"`
	Readonly   bool                       `json:"readonly"`
	AI         bool                       `json:"ai"`
	Title      string                     `json:"title,omitempty"`
	ShipTheme  string                     `json:"shipTheme,omitempty"`
	ConTheme   string                     `json:"consoleTheme,omitempty"`
	Screen     CmcpScreen                 `json:"screen"`
	Commands   []string                   `json:"commands"`
	Catalogue  map[string]msg.CommandSpec `json:"catalogue,omitempty"`
	Subs       []CmcpSub                  `json:"subs"`
	Docs       map[string]any             `json:"docs"`
	Seqs       map[string]int64           `json:"seqs"`
	Alert      string                     `json:"alert,omitempty"`
	Pending    int                        `json:"pending"`
}

// CmcpScreen is session.ScreenRef for the UI.
type CmcpScreen struct {
	ID       string         `json:"id"`
	Resolved string         `json:"resolved"`
	PackID   string         `json:"packId,omitempty"`
	Pack     *msg.PackIdent `json:"pack,omitempty"`
	Fallback bool           `json:"fallback"`
}

// CmcpSub is session.Sub for the UI.
type CmcpSub struct {
	Pkg    string `json:"pkg"`
	RateMS int    `json:"rateMs,omitempty"`
	Source string `json:"source"`
	OK     bool   `json:"ok"`
	Err    string `json:"err,omitempty"`
}

// CmcpNote is one notification that is not part of the model: a sound request, an event, a reply.
type CmcpNote struct {
	Kind    string          `json:"kind"` // state ready reset console data event scan reply timeout pack bye error log sound
	Pkg     string          `json:"pkg,omitempty"`
	Text    string          `json:"text,omitempty"`
	Event   *msg.Event      `json:"event,omitempty"`
	Scan    *msg.ScanReport `json:"scan,omitempty"`
	Reply   *CmcpReply      `json:"reply,omitempty"`
	Pack    *msg.PackRef    `json:"pack,omitempty"`
	Sound   *msg.SoundPlay  `json:"sound,omitempty"`
	Stop    *msg.SoundStop  `json:"stop,omitempty"`
	StopAll bool            `json:"stopAll,omitempty"`
	FadeMS  int             `json:"fadeMs,omitempty"`
}

// CmcpReply is session.CmdResult for the UI.
type CmcpReply struct {
	ID      uint32         `json:"id"`
	Cmd     string         `json:"cmd"`
	OK      bool           `json:"ok"`
	Err     string         `json:"err,omitempty"`
	Hint    string         `json:"hint,omitempty"`
	HintCmd string         `json:"hintCmd,omitempty"`
	Result  map[string]any `json:"result,omitempty"`
}

// CmcpTicket is what CmcpCommand returns.
type CmcpTicket struct {
	ID       uint32 `json:"id"`
	Cmd      string `json:"cmd"`
	Mutating bool   `json:"mutating"`
	Echo     string `json:"echo"`
}

type cmcpSess struct {
	s  *session
	c  *cs.Client
	tx frameSender

	mu      sync.Mutex
	refs    map[string]map[string]int // package -> owner -> rate_ms
	dirty   bool
	pubTmr  *time.Timer
	tick    *time.Timer
	stopped bool
	lastRev uint64
	started time.Time
}

func (s *session) cmcpGmcp(pkg string, data json.RawMessage) {
	cm := s.cmcp(true)
	if cm == nil {
		return
	}
	cm.receive(pkg, data)
}

// cmcp returns the session's endpoint, creating it when create is set and CMCP is enabled.
func (s *session) cmcp(create bool) *cmcpSess {
	s.mu.Lock()
	cm := s.cm
	remote := s.remote != nil
	tr := s.tr
	s.mu.Unlock()
	if cm != nil || !create || remote || tr == nil {
		return cm
	}
	tx, ok := tr.(frameSender)
	if !ok {
		return nil
	}
	s.e.cmcpMu.Lock()
	mk := s.e.cmcpCfg
	s.e.cmcpMu.Unlock()
	if mk == nil {
		return nil
	}
	cfg := mk()
	cfg.LayerSendsGate = true // the telnet layer sends Core.Hello and Core.Supports.Set ["Cobra 1"] when GMCP turns on
	cm = &cmcpSess{s: s, c: cs.NewClient(cfg), tx: tx, refs: map[string]map[string]int{}, started: s.e.now()}
	cm.c.TelnetEvent(cs.WillGMCP, s.e.now())
	s.mu.Lock()
	if s.cm != nil {
		cm = s.cm
	} else {
		s.cm = cm
	}
	s.mu.Unlock()
	cm.arm()
	return cm
}

func (cm *cmcpSess) arm() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.tick != nil || cm.stopped {
		return
	}
	var f func()
	f = func() {
		cm.mu.Lock()
		if cm.stopped {
			cm.mu.Unlock()
			return
		}
		cm.tick = time.AfterFunc(cmcpTick, f)
		cm.mu.Unlock()
		now := cm.s.e.now()
		cm.c.Tick(now)
		if cm.c.State() == cs.StateDeclared && now.Sub(cm.started) > cmcpRedeclare {
			cm.started = now
			cm.c.Redeclare(now)
		}
		cm.flush()
	}
	cm.tick = time.AfterFunc(cmcpTick, f)
}

func (s *session) cmcpStop() {
	s.mu.Lock()
	cm := s.cm
	s.mu.Unlock()
	if cm == nil {
		return
	}
	cm.mu.Lock()
	cm.stopped = true
	if cm.tick != nil {
		cm.tick.Stop()
	}
	if cm.pubTmr != nil {
		cm.pubTmr.Stop()
	}
	cm.mu.Unlock()
}

func (cm *cmcpSess) receive(pkg string, data json.RawMessage) {
	f := frame.Frame{Package: pkg}
	if len(data) > 0 && string(data) != "null" {
		f.Payload = data
	}
	cm.c.Receive(cm.s.e.now(), f)
	cm.flush()
}

// flush sends what the client produced and publishes notes; the model snapshot is throttled.
func (cm *cmcpSess) flush() {
	out := cm.c.Drain()
	for _, o := range out.Frames {
		b, err := frame.Encode(o.Package, o.Payload)
		if err != nil {
			cm.s.systemLine(fmt.Sprintf("cmcp: cannot encode %s: %v", o.Package, err))
			continue
		}
		if err := cm.tx.SendFrame(b); err != nil {
			break
		}
	}
	if notes := cm.notes(out); len(notes) > 0 {
		cm.s.e.bus.publish(Event{Kind: EvCmcpNote, Session: cm.s.id, CmcpNotes: notes})
	}
	if ui := cm.c.UI(); ui.Rev != cm.lastRev || len(out.Notes) > 0 {
		cm.schedule()
	}
}

func (cm *cmcpSess) schedule() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.pubTmr != nil || cm.stopped {
		return
	}
	cm.pubTmr = time.AfterFunc(cmcpThrottle, func() {
		cm.mu.Lock()
		cm.pubTmr = nil
		stopped := cm.stopped
		cm.mu.Unlock()
		if stopped {
			return
		}
		cm.publish()
	})
}

func (cm *cmcpSess) publish() {
	ui := cm.c.UI()
	cm.lastRev = ui.Rev
	cm.s.e.bus.publish(Event{Kind: EvCmcp, Session: cm.s.id, Cmcp: cmcpDTO(cm.s.id, ui)})
}

func (cm *cmcpSess) notes(out cs.Output) []CmcpNote {
	var ns []CmcpNote
	for _, n := range out.Notes {
		switch n.Kind {
		case cs.NoteEvent:
			ns = append(ns, CmcpNote{Kind: "event", Event: n.Event})
		case cs.NoteScanReport:
			ns = append(ns, CmcpNote{Kind: "scan", Scan: n.Scan})
		case cs.NoteReply, cs.NoteCmdTimeout:
			r := n.Result
			rep := &CmcpReply{ID: r.ID, Cmd: r.Cmd, OK: r.OK, Err: r.Err, Hint: r.Hint, HintCmd: r.HintCmd, Result: r.Result}
			k := "reply"
			if n.Kind == cs.NoteCmdTimeout {
				k = "timeout"
			}
			if !r.OK && (r.Hint != "" || r.Err != "") {
				txt := "cmcp: " + r.Cmd + ": " + r.Err
				if r.Hint != "" {
					txt += " (" + r.Hint + ")"
				}
				cm.s.systemLine(txt)
			}
			ns = append(ns, CmcpNote{Kind: k, Reply: rep})
		case cs.NotePackWanted:
			ns = append(ns, CmcpNote{Kind: "pack", Pack: n.PackRef})
		case cs.NoteBye:
			ns = append(ns, CmcpNote{Kind: "bye", Text: n.Text})
		case cs.NoteError:
			ns = append(ns, CmcpNote{Kind: "error", Text: n.Text})
		case cs.NoteReset:
			ns = append(ns, CmcpNote{Kind: "reset"})
		case cs.NoteLog:
			ns = append(ns, CmcpNote{Kind: "log", Text: n.Text})
		}
	}
	for _, sr := range out.Sounds {
		ns = append(ns, CmcpNote{Kind: "sound", Sound: sr.Play, Stop: sr.Stop, StopAll: sr.Kind == cs.SoundStopAll, FadeMS: sr.FadeMS})
	}
	return ns
}

func cmcpDTO(id string, u *cs.UI) *CmcpUI {
	d := &CmcpUI{Session: id, Rev: u.Rev, Generation: u.Generation, State: string(u.State), Proto: u.Proto,
		Server: u.Server, Welcome: u.Welcome, Console: u.Console, ConsoleRev: u.ConsoleRev, Reason: u.Reason,
		Readonly: u.Readonly, AI: u.AI, Title: u.Title, ShipTheme: u.ShipTheme, ConTheme: u.ConsoleTheme,
		Screen:   CmcpScreen{ID: u.Screen.ID, Resolved: u.Screen.Resolved, PackID: u.Screen.PackID, Pack: u.Screen.PackWanted, Fallback: u.Screen.Fallback},
		Commands: append([]string{}, u.Commands...), Catalogue: u.Catalogue, Docs: u.Docs, Seqs: u.Seqs, Alert: u.Alert, Pending: u.Pending}
	if u.HaveShip {
		sh := u.Ship
		d.Ship = &sh
	}
	if d.Docs == nil {
		d.Docs = map[string]any{}
	}
	d.Subs = []CmcpSub{}
	for _, s := range u.Subs {
		d.Subs = append(d.Subs, CmcpSub{Pkg: s.Pkg, RateMS: s.RateMS, Source: s.Source, OK: s.OK, Err: s.Err})
	}
	return d
}

// ---- engine API -------------------------------------------------------------------------------

func (e *Engine) cmcpOf(id string) (*cmcpSess, error) {
	s := e.get(id)
	if s == nil {
		return nil, fmt.Errorf("engine: no session %q", id)
	}
	cm := s.cmcp(false)
	if cm == nil {
		return nil, ErrCmcpOff
	}
	return cm, nil
}

// CmcpUI returns the current CMCP model of a session (nil with ErrCmcpOff if it does not speak CMCP).
func (e *Engine) CmcpUI(id string) (*CmcpUI, error) {
	cm, err := e.cmcpOf(id)
	if err != nil {
		return nil, err
	}
	return cmcpDTO(id, cm.c.UI()), nil
}

// CmcpCommand sends a verb of the server's catalogue (spec 4). The equivalent typed command is shown in the
// session's text as a client-generated line before the reply arrives (spec 4.5).
func (e *Engine) CmcpCommand(id, name string, args map[string]any) (CmcpTicket, error) {
	cm, err := e.cmcpOf(id)
	if err != nil {
		return CmcpTicket{}, err
	}
	t, err := cm.c.Command(e.now(), name, args)
	if err != nil {
		cm.flush()
		return CmcpTicket{}, err
	}
	if t.Mutating && t.Echo != "" {
		cm.s.addLine(proto.KindEcho, []proto.Span{{S: "cmcp> " + t.Echo}})
	}
	cm.flush()
	return CmcpTicket{ID: t.ID, Cmd: t.Cmd, Mutating: t.Mutating, Echo: t.Echo}, nil
}

// CmcpAcquire sets the packages an owner (a widget group, a screen) needs beyond the console's implicit set.
// Subscriptions are reference counted: a package is subscribed when its first owner arrives and unsubscribed when
// its last one leaves. Acquiring again with the same owner replaces that owner's set.
func (e *Engine) CmcpAcquire(id, owner string, subs []msg.SubSpec) error {
	cm, err := e.cmcpOf(id)
	if err != nil {
		return err
	}
	cm.mu.Lock()
	var add []msg.SubSpec
	var drop []string
	want := map[string]int{}
	for _, s := range subs {
		want[s.Pkg] = s.RateMS
	}
	for pkg, owners := range cm.refs {
		if _, ok := owners[owner]; ok {
			if _, keep := want[pkg]; !keep {
				delete(owners, owner)
				if len(owners) == 0 {
					delete(cm.refs, pkg)
					drop = append(drop, pkg)
				}
			}
		}
	}
	for _, s := range subs {
		owners := cm.refs[s.Pkg]
		if owners == nil {
			owners = map[string]int{}
			cm.refs[s.Pkg] = owners
			add = append(add, s)
		}
		owners[owner] = s.RateMS
	}
	cm.mu.Unlock()
	var first error
	if len(add) > 0 {
		first = cm.c.Subscribe(e.now(), add...)
	}
	if len(drop) > 0 {
		if err := cm.c.Unsubscribe(e.now(), drop...); err != nil && first == nil {
			first = err
		}
	}
	cm.flush()
	return first
}

// CmcpRelease drops every package an owner acquired.
func (e *Engine) CmcpRelease(id, owner string) error { return e.CmcpAcquire(id, owner, nil) }

// CmcpUpdateConfig changes what the client says about itself (theme, audio, pack cache); the Client re-sends
// Hello when something the server needs to know changed.
func (e *Engine) CmcpUpdateConfig(id string, f func(*cs.Config)) error {
	cm, err := e.cmcpOf(id)
	if err != nil {
		return err
	}
	cm.c.UpdateConfig(e.now(), f)
	cm.flush()
	return nil
}

// CmcpDisable ends CMCP at the player's request (the screen goes, the text pane is untouched).
func (e *Engine) CmcpDisable(id string) error {
	cm, err := e.cmcpOf(id)
	if err != nil {
		return err
	}
	cm.c.Disable(e.now())
	cm.flush()
	return nil
}
