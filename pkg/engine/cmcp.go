package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/itsnotyoutoday/cmcp/go/audio"
	"github.com/itsnotyoutoday/cmcp/go/frame"
	"github.com/itsnotyoutoday/cmcp/go/msg"
	"github.com/itsnotyoutoday/cmcp/go/notice"
	"github.com/itsnotyoutoday/cmcp/go/pack"
	cs "github.com/itsnotyoutoday/cmcp/go/session"
	"github.com/itsnotyoutoday/cmcp/go/textport"
	"github.com/itsnotyoutoday/cmcp/go/theme"
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
	Session           string                     `json:"session"`
	Rev               uint64                     `json:"rev"`
	Generation        int                        `json:"generation"`
	State             string                     `json:"state"`
	Proto             int                        `json:"proto"`
	Server            *msg.Server                `json:"server,omitempty"`
	Welcome           *msg.Welcome               `json:"welcome,omitempty"`
	Ship              *msg.ShipInfo              `json:"ship,omitempty"`
	Console           *msg.ConsoleRef            `json:"console,omitempty"`
	ConsoleRev        int                        `json:"consoleRev"`
	Reason            string                     `json:"reason,omitempty"`
	Readonly          bool                       `json:"readonly"`
	AI                bool                       `json:"ai"`
	Title             string                     `json:"title,omitempty"`
	ShipTheme         string                     `json:"shipTheme,omitempty"`
	ConTheme          string                     `json:"consoleTheme,omitempty"`
	Screen            CmcpScreen                 `json:"screen"`
	Commands          []string                   `json:"commands"`
	Catalogue         map[string]msg.CommandSpec `json:"catalogue,omitempty"`
	Subs              []CmcpSub                  `json:"subs"`
	Docs              map[string]any             `json:"docs"`
	Seqs              map[string]int64           `json:"seqs"`
	Alert             string                     `json:"alert,omitempty"`
	Pending           int                        `json:"pending"`
	Stack             []CmcpStackEntry           `json:"stack"` // held contexts, lowest priority first; the last is active (spec 3.3)
	CatalogueComplete bool                       `json:"catalogueComplete"`
	Notice            *CmcpNoticeLine            `json:"notice,omitempty"` // the one notice line (pack downloads, accepted packs, theme in use, warnings)
	AllScreens        []CmcpScreenOption         `json:"allScreens"`       // every screen with its state and `man <console> to use` hint (the "more..." list)
	Directory         []string                   `json:"directory"`        // the core's DirectoryLines of those rows (the /cmcp screens listing)
	Screens           []CmcpScreenOption         `json:"screens"`          // the screen menu (console-switch column)
	LocalScreen       string                     `json:"localScreen,omitempty"`
	Pinned            bool                       `json:"pinned,omitempty"`
	Status            map[string]any             `json:"status,omitempty"` // Space.Ship.Status (name, alert, hull, core, lock) for the frame
	Tree              *CmcpTree                  `json:"tree"`             // the bound screen, nil = no console screen
}

// CmcpStackEntry is one held context (a console, a room, an away team, an event, a dock).
type CmcpStackEntry struct {
	Key      string `json:"key"`
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Title    string `json:"title,omitempty"`
	Screen   string `json:"screen"`
	Priority int    `json:"priority"`
	Active   bool   `json:"active"`
	Readonly bool   `json:"readonly,omitempty"`
	MannedBy string `json:"mannedBy,omitempty"`
	Expires  int64  `json:"expiresMs,omitempty"` // unix ms, 0 = no ttl
}

// CmcpScreenOption is session.ScreenOption: one entry of the screen menu.
// CmcpNoticeLine is the core's notice queue head: one line, sticky while a transfer is active.
type CmcpNoticeLine struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`  // download | accepted | using | warning | error | info
	State string `json:"state"` // active | done | failed
	Text  string `json:"text"`  // the line, with the progress
}

type CmcpScreenOption struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	HintCmd   string `json:"hintCmd,omitempty"`
	Active    bool   `json:"active"`
	State     string `json:"state,omitempty"` // own | alternate | viewable | context | locked
	Hint      string `json:"hint,omitempty"`  // "man <console> to use" for a screen this console cannot show (the "more..." list only)
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
	Params  map[string]any  `json:"params,omitempty"`
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

	mu                          sync.Mutex
	refs                        map[string]map[string]int // package -> owner -> rate_ms
	dirty                       bool
	pubTmr                      *time.Timer
	tick                        *time.Timer
	stopped                     bool
	lastRev                     atomic.Uint64
	ss                          *screenState
	ps                          *cmcpPackState
	soundPack                   *audio.Pack    // guarded by mu
	tv                          *textport.View // guarded by mu
	themeKey, themeID, themeSrc string         // the sticky per-ship theme (guarded by mu)
	themeShip                   bool
	alertMode                   string // the effective alert_recolour (full|edge|off) of the theme in effect
	pulsePack                   *theme.Pack
	pulseCfg                    cs.PulseConfig
	pulseSet                    bool
	fxSet                       bool
	ambCfg                      cs.AmbientConfig
	shkCfg                      cs.ShakeConfig
	pulseLast                   CmcpPulse
	pulseTmr                    *time.Timer
	vp                          cmcpViewport // the web view's viewport (guarded by mu)
	themeSeen                   string       // id|source|version last announced on the notice line
	th                          *themeState  // downloaded themes (guarded by mu)
	started                     time.Time
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
	if cfg.ScreenCatalog == nil {
		cfg.ScreenCatalog = pack.SessionCatalog
	}
	cfg.LayerSendsGate = true // the telnet layer sends Core.Hello and Core.Supports.Set ["Cobra 1"] when GMCP turns on
	cm = &cmcpSess{s: s, c: cs.NewClient(cfg), tx: tx, refs: map[string]map[string]int{}, started: s.e.now(), ss: &screenState{packs: map[string]*pack.Pack{}}}
	if ents := cm.cacheEntries(); len(ents) > 0 {
		cm.c.SetPackCache(s.e.now(), ents)
	}
	cm.c.TelnetEvent(cs.WillGMCP, s.e.now())
	s.mu.Lock()
	if s.cm != nil {
		cm = s.cm
	} else {
		s.cm = cm
	}
	s.mu.Unlock()
	cm.arm()
	cm.armPulse()
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
	if cm.pulseTmr != nil {
		cm.pulseTmr.Stop()
	}
	cm.mu.Unlock()
}

func (cm *cmcpSess) receive(pkg string, data json.RawMessage) {
	if cm.packFrame(pkg, data) {
		return
	}
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
	if ui := cm.c.UI(); ui.Rev != cm.lastRev.Load() || len(out.Notes) > 0 {
		cm.schedule()
	}
}

// sendRaw writes one frame outside the Client's own output (pack transfer messages).
func (cm *cmcpSess) sendRaw(pkg string, payload any) {
	if b, err := frame.Encode(pkg, payload); err == nil {
		_ = cm.tx.SendFrame(b)
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
	cm.lastRev.Store(ui.Rev)
	d := cmcpDTO(cm.s.id, ui)
	if st, ok := ui.Docs[msg.PkgShipStatus].(map[string]any); ok {
		d.Status = st
	}
	d.Docs = nil // the screen tree carries the evaluated data; CmcpUI() returns the documents
	d.Tree = cm.tree(ui)
	cm.s.e.bus.publish(Event{Kind: EvCmcp, Session: cm.s.id, Cmcp: d})
}

func (cm *cmcpSess) notes(out cs.Output) []CmcpNote {
	var ns []CmcpNote
	for _, n := range out.Notes {
		switch n.Kind {
		case cs.NoteReady, cs.NoteConsole:
			go cm.loadCached()
		case cs.NoteEvent:
			ns = append(ns, CmcpNote{Kind: "event", Event: n.Event})
			if n.Event != nil && n.Event.Type == "hit" {
				cm.kickPulse() // a hit starts a shake now, not at the next idle sample
			}
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
				cm.c.Notify(cm.s.e.now(), notice.Notice{ID: "cmd:" + r.Cmd, Kind: notice.Error, Text: strings.TrimPrefix(txt, "cmcp: ")})
			}
			ns = append(ns, CmcpNote{Kind: k, Reply: rep})
		case cs.NotePackWanted:
			ns = append(ns, CmcpNote{Kind: "pack", Pack: n.PackRef})
			if n.PackRef != nil {
				go cm.wantPack(*n.PackRef)
			}
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

// CmcpNotify posts a line on the notice queue (command errors with hints, warnings, info); state is active | done | failed.
func (e *Engine) CmcpNotify(id, nid, kind, state, text string) error {
	cm, err := e.cmcpOf(id)
	if err != nil {
		return err
	}
	cm.c.Notify(e.now(), notice.Notice{ID: nid, Kind: kind, State: state, Text: text})
	cm.flush()
	return nil
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
	d.CatalogueComplete = u.CatalogueComplete
	if n := u.Notice; n != nil {
		st := n.State
		if st == "" {
			st = notice.Done
		}
		d.Notice = &CmcpNoticeLine{ID: n.ID, Kind: n.Kind, State: st, Text: n.Line()}
	}
	d.Screens, d.LocalScreen, d.Pinned = []CmcpScreenOption{}, u.LocalScreen, u.Pinned
	opt := func(o cs.ScreenOption) CmcpScreenOption {
		return CmcpScreenOption{ID: o.ID, Title: o.Title, Available: o.Available, Reason: o.Reason, HintCmd: o.HintCmd, Active: o.Active, State: o.State, Hint: o.Hint}
	}
	d.AllScreens, d.Directory = []CmcpScreenOption{}, []string{}
	var rows []cs.DirectoryRow
	for _, o := range u.Screens {
		d.AllScreens = append(d.AllScreens, opt(o))
		label := cs.ScreenLabels[o.ID]
		if label == "" {
			label = o.Title
		}
		rows = append(rows, cs.DirectoryRow{Screen: o.ID, Label: label, Available: o.Available, Hint: o.Hint})
	}
	d.Directory = cs.DirectoryLines(rows)
	// the menu lists only the screens this console can show (the core's MenuScreens): nothing dim, never a locked one
	for _, o := range u.MenuScreens() {
		d.Screens = append(d.Screens, opt(o))
	}
	d.Stack = []CmcpStackEntry{}
	for _, e := range u.Stack {
		x := CmcpStackEntry{Key: e.Key, Kind: e.Kind, ID: e.ID, Name: e.Name, Title: e.Title, Screen: e.Screen, Priority: e.Priority, Active: e.Active, Readonly: e.Readonly, MannedBy: e.MannedBy}
		if !e.Expires.IsZero() {
			x.Expires = e.Expires.UnixMilli()
		}
		d.Stack = append(d.Stack, x)
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
	ui := cm.c.UI()
	d := cmcpDTO(id, ui)
	if st, ok := ui.Docs[msg.PkgShipStatus].(map[string]any); ok {
		d.Status = st
	}
	d.Tree = cm.tree(ui)
	return d, nil
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

// CmcpClose asks the game to close the session's active context (the X of the console window, a close key or command): Cobra.Context.Close
// with reason "user" or "client". It NEVER hides the screen: the game decides (a manned console is unmanned and popped, a special room is hidden
// for the visit) and its update takes the screen away; a refusal or a missing answer comes back as a notice. A context-less connection sends the
// console.unman verb instead.
func (e *Engine) CmcpClose(id, reason string) error {
	cm, err := e.cmcpOf(id)
	if err != nil {
		return err
	}
	now := e.now()
	_, err = cm.c.CloseContext(now, reason)
	switch {
	case err == nil:
	case errors.Is(err, cs.ErrContextlessClient):
		if _, cerr := cm.c.Command(now, "console.unman", nil); cerr != nil {
			cm.c.Notify(now, notice.Notice{ID: "close", Kind: notice.Error, Text: "Could not leave the console: " + cerr.Error()})
		}
		err = nil
	case errors.Is(err, cs.ErrNoActiveContext):
		cm.c.Notify(now, notice.Notice{ID: "close", Kind: notice.Info, Text: "There is nothing to close here."})
		err = nil
	case errors.Is(err, cs.ErrCloseThrottled):
		err = nil // two requests per second: a quick second click is dropped
	}
	cm.flush()
	return err
}

// CmcpCloseAll tells every connected CMCP session's game that the client window is closing (reason "client"); the game decides what to do.
func (e *Engine) CmcpCloseAll() {
	for _, si := range e.Sessions() {
		if ok, _ := cs.CloseSends("client_window_closed"); !ok {
			return
		}
		_ = e.CmcpClose(si.ID, cs.CloseClient)
	}
}
