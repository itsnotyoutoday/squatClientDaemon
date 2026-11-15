package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
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
	Panes             []CmcpPane                 `json:"panes,omitempty"`  // CMCP 2: contexts presented on a panel, dock or float, in addition to the screen

	// ---- protocol 2 (spec chapter 20); V2 is false and the rest empty for a v1 session ----
	V2         bool                 `json:"v2,omitempty"`
	Def        *msg.DefRef          `json:"def,omitempty"`        // the applied definition
	TextClient bool                 `json:"textClient,omitempty"` // the definition was refused: no contexts are shown (a notice says why)
	TextReason string               `json:"textReason,omitempty"`
	Scopes     map[string]CmcpScope `json:"scopes,omitempty"`
	Ctx        *CmcpCtx             `json:"ctx,omitempty"` // the active context
	Ambience   bool                 `json:"ambience,omitempty"`
}

// CmcpScope is one scope of the definition with its current owner (nil: nobody).
type CmcpScope struct {
	Title string          `json:"title,omitempty"`
	Owner *msg.ScopeOwner `json:"owner,omitempty"`
}

// CmcpCtx is the active context of a v2 session: its type and scope, the layout in force and where it is presented (screen, panel, dock, float).
type CmcpCtx struct {
	Type    string         `json:"type"`
	Scope   string         `json:"scope"`
	Key     string         `json:"key"`
	ID      string         `json:"id,omitempty"`
	Layout  string         `json:"layout"`
	Surface string         `json:"surface"`
	Ambient bool           `json:"ambient"`
	Data    map[string]any `json:"data,omitempty"` // $ctx.data
	Layouts []string       `json:"layouts,omitempty"`
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
	pubTmr                      *time.Timer
	tick                        *time.Timer
	stopped                     bool
	lastRev                     atomic.Uint64
	ss                          *screenState
	ps                          *cmcpPackState
	soundPack                   *audio.Pack    // guarded by mu
	tv                          *textport.View // guarded by mu
	userClosed                  bool           // the player closed the last screen: an empty context stack then does not mean the ship was left (guarded by mu)
	themeKey, themeID, themeSrc string         // the sticky per-ship theme (guarded by mu)
	themeShip                   bool
	alertMode                   string // the effective alert_recolour (full|edge|off) of the theme in effect
	pulsePack                   *theme.Pack
	pulseCfg                    cs.PulseConfig
	pulseSet                    bool
	pulseSeq, ambSeq, shkSeq    int        // the latest requested core configuration of each kind (guarded by mu)
	fxApply                     sync.Mutex // serializes the core configuration calls
	fxSet                       bool
	ambCfg                      cs.AmbientConfig
	shkCfg                      cs.ShakeConfig
	pulseLast                   CmcpPulse
	pulseTmr                    *time.Timer
	vp                          cmcpViewport // the web view's viewport (guarded by mu)
	aboard                      bool         // the last Aboard() seen (guarded by mu)
	ctxLast                     string       // the last context line logged (guarded by mu)
	ctxLines                    int
	streams                     map[string]*streamStat // per-package counters of the high-volume streams (guarded by mu)
	streamAt                    time.Time              // when they were last written
	themeSeen                   string                 // id|source|version last announced on the notice line
	th                          *themeState            // downloaded themes (guarded by mu)
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
	cfg := s.e.cmcpConfigFor(mk, s.world.Name)
	if cfg.ScreenCatalog == nil {
		cfg.ScreenCatalog = pack.SessionCatalog
	}
	cfg.LayerSendsGate = true // the telnet layer sends Core.Hello and Core.Supports.Set ["Cobra 1"] when GMCP turns on
	cm = &cmcpSess{s: s, c: cs.NewClient(cfg), tx: tx, refs: map[string]map[string]int{}, started: s.e.now(), ss: &screenState{packs: map[string]*pack.Pack{}}}
	if ents := cm.cacheEntries(); len(ents) > 0 {
		cm.c.SetPackCache(s.e.now(), ents)
	}
	cm.holdCachedDefs() // v2: the definitions this host sent before are listed in Hello.defs
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
	cm.streamFlush()
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
	// the connection is gone (it ended, or "Logged in from elsewhere"): one last model with no screen and no alert look, so the view does not keep
	// drawing the last console of a dead session
	d := cmcpDTO(cm.s.id, cm.c.UI())
	d.State, d.Tree, d.Notice = "ended", nil, nil
	cm.s.e.bus.publish(Event{Kind: EvCmcp, Session: cm.s.id, Cmcp: d})
	cm.publishPulse(CmcpPulse{Session: cm.s.id, Level: 1, Phase: "on"})
	if f := cm.s.e.cmcpLogFn; f != nil {
		f(fmt.Sprintf("ctx %s: the connection ended: no screen", cm.s.id))
	}
}

// dbgPayload truncates one logged payload (the byte count is appended); control-plane payloads are logged in full up to this size.
const dbgPayload = 8000

// dbg writes one trail line to the client diagnostics sink (the diagnostics log): what the game sent, what the client sent back and what the player did
// to the view, so a report shows both sides of the conversation. The sink decides what to keep; the engine caps nothing (high-volume streams are
// summarized by streamNote instead).
func (cm *cmcpSess) dbg(format string, args ...any) {
	if !cm.s.e.cmcpTrail.Load() {
		return
	}
	cm.s.e.cmcpMu.Lock()
	f := cm.s.e.cmcpLogFn
	cm.s.e.cmcpMu.Unlock()
	if f == nil {
		return
	}
	f(fmt.Sprintf("dbg %s: ", cm.s.id) + fmt.Sprintf(format, args...))
}

func dbgPayloadText(b []byte) string {
	t := strings.Join(strings.Fields(string(b)), " ")
	if len(t) > dbgPayload {
		t = t[:dbgPayload] + "...(" + strconv.Itoa(len(b)) + " bytes)"
	}
	return t
}

// dbgStream names the packages that arrive many times a second (ship status, space/crew/mission deltas, pulse, ping): they are counted per session and
// summarized every few seconds instead of logged one by one, unless the payload is a full document, a resync or an error.
func dbgStream(pkg string) bool {
	for _, p := range []string{"Space.", "Crew.", "Mission.", "Cobra.Ship.Status", "Cobra.Pulse", "Cobra.Ping"} {
		if strings.HasPrefix(pkg, p) {
			return true
		}
	}
	return false
}

// dbgNotable is true for a stream payload that must be logged in full.
func dbgNotable(data []byte) bool {
	return notableRe.Match(data)
}

var notableRe = regexp.MustCompile(`"full"\s*:\s*true|resync|"error"`)

// streamEvery is how often the per-package counters are written.
const streamEvery = 5 * time.Second

type streamStat struct {
	n, bytes int
	lastSeq  int64
}

var seqRe = regexp.MustCompile(`"seq"\s*:\s*(\d+)`)

// streamNote counts one high-volume payload and writes the summary lines when the interval has passed.
func (cm *cmcpSess) streamNote(dir, pkg string, data []byte) {
	if !cm.s.e.cmcpTrail.Load() {
		return
	}
	var seq int64 = -1
	if m := seqRe.FindSubmatch(data); m != nil {
		seq, _ = strconv.ParseInt(string(m[1]), 10, 64)
	}
	cm.mu.Lock()
	if cm.streams == nil {
		cm.streams = map[string]*streamStat{}
		cm.streamAt = cm.s.e.now()
	}
	k := dir + " " + pkg
	st := cm.streams[k]
	if st == nil {
		st = &streamStat{lastSeq: -1}
		cm.streams[k] = st
	}
	st.n++
	st.bytes += len(data)
	if seq >= 0 {
		st.lastSeq = seq
	}
	due := cm.s.e.now().Sub(cm.streamAt) >= streamEvery
	cm.mu.Unlock()
	if due {
		cm.streamFlush()
	}
}

// streamFlush writes one counter line per package seen since the last flush.
func (cm *cmcpSess) streamFlush() {
	cm.mu.Lock()
	streams := cm.streams
	cm.streams = nil
	cm.streamAt = cm.s.e.now()
	cm.mu.Unlock()
	keys := make([]string, 0, len(streams))
	for k := range streams {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		st := streams[k]
		seq := ""
		if st.lastSeq >= 0 {
			seq = fmt.Sprintf(" last_seq=%d", st.lastSeq)
		}
		cm.dbg("stream %s count=%d bytes=%d%s (last %ds)", k, st.n, st.bytes, seq, int(streamEvery/time.Second))
	}
}

func (cm *cmcpSess) receive(pkg string, data json.RawMessage) {
	if !dbgStream(pkg) || dbgNotable(data) {
		cm.dbg("<- %s %s", pkg, dbgPayloadText(data))
	} else {
		cm.streamNote("<-", pkg, data)
	}
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
		if !dbgStream(o.Package) || dbgNotable(outPayload(o, b)) {
			cm.dbg("-> %s %s", o.Package, dbgPayloadText(outPayload(o, b)))
		} else {
			cm.streamNote("->", o.Package, outPayload(o, b))
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
	if st := statusDoc(ui); st != nil {
		d.Status = st
	}
	d.Docs = nil // the screen tree carries the evaluated data; CmcpUI() returns the documents
	d.Tree = cm.tree(ui)
	d.Panes = cm.paneTrees(ui)
	cm.logContext(ui, d)
	cm.aboardChanged(ui.Aboard())
	cm.s.e.bus.publish(Event{Kind: EvCmcp, Session: cm.s.id, Cmcp: d})
}

func (cm *cmcpSess) notes(out cs.Output) []CmcpNote {
	var ns []CmcpNote
	for _, n := range out.Notes {
		switch n.Kind {
		case cs.NoteReady, cs.NoteConsole:
			go cm.loadCached()
		case cs.NoteDefFetch:
			if n.Blob != nil {
				cm.defFetch(*n.Blob)
			}
		case cs.NoteDefApplied:
			cm.defApplied(n)
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
	if u.HaveShip && u.Aboard() && u.Ship.ID != "none" { // the "No ship" sentinel (spec 3.1.1) is the absence of a ship, never a ship named "No ship"
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
	if u.V2 {
		d.V2, d.Def, d.TextClient, d.TextReason, d.Ambience = true, u.Def, u.TextClient, u.TextReason, u.Ambience
		d.Scopes = map[string]CmcpScope{}
		for n, sv := range u.Scopes {
			d.Scopes[n] = CmcpScope{Title: sv.Title, Owner: sv.Owner}
		}
		if u.CtxType != "" {
			d.Ctx = &CmcpCtx{Type: u.CtxType, Scope: u.CtxScope, Key: u.CtxKey, ID: u.CtxID, Layout: u.CtxLayout, Surface: u.CtxSurface, Ambient: u.CtxAmbient, Data: u.CtxData, Layouts: u.Layouts}
		}
		for _, e := range u.Stack2 {
			x := CmcpStackEntry{Key: e.Key, Kind: e.Type, ID: e.ID, Name: e.Name, Title: e.Title, Screen: e.EffectiveLayout, Priority: e.Priority, Active: e.Active, Readonly: e.Readonly, MannedBy: e.MannedBy}
			if !e.Expires.IsZero() {
				x.Expires = e.Expires.UnixMilli()
			}
			d.Stack = append(d.Stack, x)
		}
	}
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
	d.Status = statusDoc(ui)
	d.Tree = cm.tree(ui)
	d.Panes = cm.paneTrees(ui)
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
	cm.mu.Lock()
	cm.userClosed = true
	cm.mu.Unlock()
	cm.dbg("player: close requested (reason %q)", reason)
	_, err = cm.c.CloseContext(now, reason)
	cm.dbg("player: close result err=%v", err)
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

// SetCmcpLog gives the engine a sink for the client's own diagnostics (the app writes them to client.log): every change of the context stack, the console
// and the screen the client chose, so a report shows what the client received from the game.
func (e *Engine) SetCmcpLog(f func(string)) {
	e.cmcpMu.Lock()
	e.cmcpLogFn = f
	e.cmcpMu.Unlock()
	e.cmcpTrail.Store(f != nil || dbgEnv)
}

// SetCmcpTrail turns the wire/action trail on or off (the diagnostics log being on or off); it needs a sink set with SetCmcpLog.
func (e *Engine) SetCmcpTrail(on bool) { e.cmcpTrail.Store(on || dbgEnv) }

// dbgEnv keeps the old switch: SQUAT_DEBUG=1 forces the trail on.
var dbgEnv = os.Getenv("SQUAT_DEBUG") == "1"

// logContext writes one line when the context stack, the console or the chosen screen changed (at most 400 lines per session).
func (cm *cmcpSess) logContext(ui *cs.UI, d *CmcpUI) {
	cm.s.e.cmcpMu.Lock()
	f := cm.s.e.cmcpLogFn
	cm.s.e.cmcpMu.Unlock()
	if f == nil {
		return
	}
	var stack []string
	for _, e := range d.Stack { // the DTO's stack: v1 contexts, or the v2 ones (kind = the context type)
		a := ""
		if e.Active {
			a = "*"
		}
		stack = append(stack, fmt.Sprintf("%s%s:%s(%s)", a, e.Kind, e.ID, e.Screen))
	}
	con, ship, tree := "", "", ""
	if ui.Console != nil {
		con = ui.Console.ID
	}
	if d.Ship != nil {
		ship = d.Ship.ID
	}
	if ui.V2 {
		owners := []string{}
		for n, sv := range d.Scopes {
			if sv.Owner != nil {
				owners = append(owners, n+"="+sv.Owner.ID)
			}
		}
		sort.Strings(owners)
		ship = "scopes=" + strings.Join(owners, ",") + " " + cm.defStatusLine(ui)
	}
	if d.Tree != nil {
		tree = d.Tree.Screen
	}
	line := fmt.Sprintf("ctx %s: state=%s ship=%q console=%q stack=%v screen=%q local=%q drawn=%q", cm.s.id, ui.State, ship, con, stack, ui.Screen.Resolved, ui.LocalScreen, tree)
	cm.mu.Lock()
	same := line == cm.ctxLast
	cm.ctxLast = line
	n := cm.ctxLines
	if !same {
		cm.ctxLines++
	}
	cm.mu.Unlock()
	if !same && n < 400 {
		f(line)
	}
}

// aboardChanged stops every sound of the session when the player leaves a ship (the core keeps no alert-sound state: a red-alert loop would go on
// sounding), and remembers the state for CmcpAboard.
func (cm *cmcpSess) aboardChanged(now bool) {
	cm.mu.Lock()
	was := cm.aboard
	cm.aboard = now
	cm.mu.Unlock()
	if was && !now {
		cm.s.e.bus.publish(Event{Kind: EvCmcpNote, Session: cm.s.id, CmcpNotes: []CmcpNote{{Kind: "sound", StopAll: true, FadeMS: 300}}})
	}
}

// CmcpAboard says whether the core thinks the session's player is aboard a ship now (false after the "No ship" sentinel).
func (e *Engine) CmcpAboard(id string) bool {
	cm, err := e.cmcpOf(id)
	if err != nil {
		return false
	}
	return cm.c.UI().Aboard()
}

// outPayload is the JSON a frame carries (the telnet framing bytes around it are not part of the trail); b is the framed bytes, the fallback.
func outPayload(o cs.Out, b []byte) []byte {
	if o.Payload == nil {
		return []byte("null")
	}
	if j, err := json.Marshal(o.Payload); err == nil {
		return j
	}
	return b
}
