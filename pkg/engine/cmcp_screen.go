package engine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/itsnotyoutoday/cmcp/pack"
	cs "github.com/itsnotyoutoday/cmcp/session"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// The screen layer of CMCP: which screen is up, its evaluated widget tree, the player's $ui state. All evaluation is
// the shared core's (pack.Bind); the front end receives plain evaluated props and draws them (spec 6.10, class gpu).

// CmcpRender says how screens are themed.
type CmcpRender struct {
	Theme        string                            // the player's theme (tng, soft, ...)
	UseShipTheme bool                              // follow the console/ship/screen suggestion (spec 6.6)
	Tokens       func(theme string) map[string]any // design tokens of a theme (nil: client defaults)
}

// SetCmcpRender sets the theming used by all sessions; the next UI update uses it.
func (e *Engine) SetCmcpRender(r CmcpRender) {
	e.cmcpMu.Lock()
	e.cmcpRender = r
	e.cmcpMu.Unlock()
	e.cmcpRepublish()
}

func (e *Engine) cmcpRepublish() {
	e.mu.Lock()
	var ss []*session
	for _, s := range e.sessions {
		ss = append(ss, s)
	}
	e.mu.Unlock()
	for _, s := range ss {
		s.mu.Lock()
		cm := s.cm
		s.mu.Unlock()
		if cm != nil {
			cm.schedule()
		}
	}
}

// CmcpLayout is a node's layout block for the front end (abstract units, spec 6.3).
type CmcpLayout struct {
	Dir     string  `json:"dir,omitempty"`
	Gap     float64 `json:"gap,omitempty"`
	Pad     float64 `json:"pad,omitempty"`
	Weight  float64 `json:"weight,omitempty"`
	Size    float64 `json:"size,omitempty"`
	HasSize bool    `json:"hasSize,omitempty"`
	Cols    int     `json:"cols,omitempty"`
	Rows    int     `json:"rows,omitempty"`
	Col     int     `json:"col,omitempty"`
	Row     int     `json:"row,omitempty"`
	ColSpan int     `json:"colspan,omitempty"`
	RowSpan int     `json:"rowspan,omitempty"`
	Align   string  `json:"align,omitempty"`
}

// CmcpNode is one visible, evaluated widget.
type CmcpNode struct {
	Path     string         `json:"path"`
	ID       string         `json:"id,omitempty"`
	Widget   string         `json:"widget"`
	Label    string         `json:"label,omitempty"`
	Props    map[string]any `json:"props"`
	Layout   CmcpLayout     `json:"layout"`
	Enabled  bool           `json:"enabled"`
	Events   []string       `json:"events,omitempty"` // the events the pack handles on this node
	Children []*CmcpNode    `json:"children,omitempty"`
}

// CmcpTree is the bound screen.
type CmcpTree struct {
	Session    string         `json:"session"`
	Generation int            `json:"generation"`
	Screen     string         `json:"screen"` // the id being drawn ("std.damage", "fspace-std/damage")
	Title      string         `json:"title"`
	Console    string         `json:"console,omitempty"`
	Theme      string         `json:"theme"`   // the theme in force after the precedence rules of 6.6
	Tokens     map[string]any `json:"tokens"`  // resolved design tokens for that theme and pack
	PackKey    string         `json:"packKey"` // changes when the pack or tokens change (sprite cache key part)
	Exhausted  bool           `json:"exhausted,omitempty"`
	Root       *CmcpNode      `json:"root"`
	UI         map[string]any `json:"ui"` // the screen's $ui state, for highlighting a selection
}

type screenState struct {
	mu       sync.Mutex
	packs    map[string]*pack.Pack // layout packs by id (the built-ins are separate)
	override string                // a `show` action: a screen id local to this client
	overFor  string                // the console the override was made for
	ui       map[string]any
	uiFor    string // screen id the $ui belongs to
	uiGen    int
	last     *pack.BoundNode
	lastEnv  *pack.Env
	lastTree *CmcpTree
	lastScr  *pack.Screen
}

func (cm *cmcpSess) screens() *screenState {
	if cm.ss == nil {
		cm.ss = &screenState{packs: map[string]*pack.Pack{}}
	}
	return cm.ss
}

// CmcpLoadPack parses a layout pack that was fetched and verified (hash and consent are the fetcher's job) and makes it
// available: a console that names its screens switches from the built-in to it (spec 16.6).
func (e *Engine) CmcpLoadPack(id string, raw []byte) error {
	cm, err := e.cmcpOf(id)
	if err != nil {
		return err
	}
	return cm.loadPack(raw)
}

func (cm *cmcpSess) loadPack(raw []byte) error {
	ui := cm.c.UI()
	var cmds map[string]struct{}
	if ui.Catalogue != nil {
		cmds = map[string]struct{}{}
		for n := range ui.Catalogue {
			cmds[n] = struct{}{}
		}
	}
	p, err := pack.Parse(raw, pack.Options{Commands: cmds})
	if err != nil {
		return err
	}
	ss := cm.screens()
	ss.mu.Lock()
	ss.packs[p.ID] = p
	ss.mu.Unlock()
	cm.c.PackAvailable(p.ID, p.SHA256, true)
	cm.flush()
	return nil
}

func (cm *cmcpSess) pickScreen(ui *cs.UI) (*pack.Screen, string) {
	ss := cm.screens()
	id := ui.Screen.Resolved
	ss.mu.Lock()
	if ss.override != "" && ss.overFor == consoleID(ui) {
		id = ss.override
	}
	ss.mu.Unlock()
	if strings.HasPrefix(id, "std.") {
		if s := pack.Builtin(id); s != nil {
			return s, id
		}
	} else if pid, sid, ok := strings.Cut(id, "/"); ok {
		ss.mu.Lock()
		p := ss.packs[pid]
		ss.mu.Unlock()
		if p != nil && p.Screens[sid] != nil {
			return p.Screens[sid], id
		}
	}
	if ui.Console != nil {
		if s := pack.Builtin("std." + ui.Console.ID); s != nil { // the built-in screen of the same console
			return s, "std." + ui.Console.ID
		}
	}
	return nil, ""
}

func consoleID(ui *cs.UI) string {
	if ui.Console == nil {
		return ""
	}
	return ui.Console.ID
}

func (cm *cmcpSess) theme(ui *cs.UI, scr *pack.Screen) string {
	cm.s.e.cmcpMu.Lock()
	r := cm.s.e.cmcpRender
	cm.s.e.cmcpMu.Unlock()
	th := r.Theme
	if th == "" {
		th = "tng"
	}
	if r.UseShipTheme {
		for _, c := range []string{ui.ConsoleTheme, ui.ShipTheme, scr.SuggestTheme} {
			if c != "" {
				return c
			}
		}
	}
	return th
}

// tree binds the current screen; nil when no console screen is up (the normal text UI).
func (cm *cmcpSess) tree(ui *cs.UI) *CmcpTree {
	if ui.State != cs.StateReady || (ui.Console == nil && !ui.Readonly) {
		return nil
	}
	scr, id := cm.pickScreen(ui)
	if scr == nil {
		return nil
	}
	ss := cm.screens()
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.uiFor != id || ss.uiGen != ui.Generation || ss.ui == nil {
		ss.ui, ss.uiFor, ss.uiGen = map[string]any{}, id, ui.Generation
	}
	env := pack.EnvFromUI(ui, ss.ui)
	b := scr.Bind(env, pack.Caps{})
	theme := cm.theme(ui, scr)
	cm.s.e.cmcpMu.Lock()
	tf := cm.s.e.cmcpRender.Tokens
	cm.s.e.cmcpMu.Unlock()
	var tt map[string]any
	if tf != nil {
		tt = tf(theme)
	}
	rs := scr.Pack.Resolver(tt)
	toks := map[string]any{}
	for k := range pack.DefaultTokens() {
		toks[k], _ = rs.Resolve(k)
	}
	for k := range scr.Pack.Tokens {
		toks[k], _ = rs.Resolve(k)
	}
	for k := range tt {
		toks[k] = tt[k]
	}
	ss.last, ss.lastEnv, ss.lastScr = b, env, scr
	t := &CmcpTree{Session: cm.s.id, Generation: ui.Generation, Screen: id, Title: scr.Title, Console: scr.Console, Theme: theme, Tokens: toks,
		PackKey: scr.Pack.SHA256 + "/" + theme, Exhausted: b.Exhausted, Root: dtoNode(b, scr, ui), UI: copyMap(ss.ui)}
	if ui.Title != "" {
		t.Title = ui.Title
	}
	ss.lastTree = t
	return t
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func dtoNode(b *pack.BoundNode, scr *pack.Screen, ui *cs.UI) *CmcpNode {
	if b == nil || !b.Visible {
		return nil
	}
	l := b.Layout
	n := &CmcpNode{Path: b.Node.Path, ID: b.Node.ID, Widget: b.Widget, Label: b.Node.Label, Props: b.Props, Enabled: b.Enabled,
		Layout: CmcpLayout{Dir: l.Dir, Gap: l.Gap, Pad: l.Pad, Weight: l.Weight, Size: l.Size, HasSize: l.HasSize, Cols: l.Cols, Rows: l.Rows,
			Col: l.Col, Row: l.Row, ColSpan: l.ColSpan, RowSpan: l.RowSpan, Align: l.Align}}
	if n.Props == nil {
		n.Props = map[string]any{}
	}
	for ev := range b.Node.On {
		n.Events = append(n.Events, ev)
		if !b.Node.CanPerform(ev, ui) {
			n.Enabled = false
		}
	}
	if sid, ok := n.Props["sprite"].(string); ok && scr.Pack.Sprites[sid] != nil {
		sp := scr.Pack.Sprites[sid]
		env := &pack.Env{Docs: ui.Docs}
		am := sp.Evaluate(sp.DefaultView, env)
		n.Props["sprite_amounts"] = map[string]any{"zones": am.Zones, "glows": am.Glows}
		n.Props["sprite_key"] = scr.Pack.SHA256 + "/" + sid
	}
	if n.Widget == "viewscreen" {
		n.Props["items"] = viewItems(n.Props, scr)
	}
	for _, c := range b.Children {
		if d := dtoNode(c, scr, ui); d != nil {
			n.Children = append(n.Children, d)
		}
	}
	return n
}

// CmcpSprite returns a sprite of the screen's pack with the @tokens of the current theme replaced (JSON of the
// shared sprite format, spec 7.2), for Canvas2D drawing. Views are by name; "" is a single-view sprite.
func (e *Engine) CmcpSprite(id, spriteID string) (json.RawMessage, error) {
	cm, err := e.cmcpOf(id)
	if err != nil {
		return nil, err
	}
	ss := cm.screens()
	ss.mu.Lock()
	scr, tree := ss.lastScr, ss.lastTree
	ss.mu.Unlock()
	if scr == nil || tree == nil {
		return nil, fmt.Errorf("engine: no screen")
	}
	sp := scr.Pack.Sprites[spriteID]
	if sp == nil {
		return nil, fmt.Errorf("engine: no sprite %q in pack %q", spriteID, scr.Pack.ID)
	}
	tk := scr.Pack.Resolver(tree.Tokens)
	out := map[string]any{"id": sp.ID, "kind": sp.Kind, "size_m": sp.SizeM, "default_view": sp.DefaultView, "views": map[string]json.RawMessage{}}
	views := out["views"].(map[string]json.RawMessage)
	for name := range sp.Views {
		rs, err := sp.Resolve(name, tk)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(rs)
		if err != nil {
			return nil, err
		}
		views[name] = raw
	}
	return json.Marshal(out)
}

// CmcpEventResult is what a widget event did.
type CmcpEventResult struct {
	Tickets []CmcpTicket `json:"tickets"`
	Errors  []string     `json:"errors,omitempty"`
	Show    string       `json:"show,omitempty"`
}

// CmcpEvent performs the actions the pack attached to a widget event (spec 6.9): `cmd` actions go out as Cobra.Cmd and
// are echoed as text, `set` writes $ui, `sound` plays locally, `show` switches screens within the console. path is the
// node path of the CmcpNode; value is the widget's output ($value).
func (e *Engine) CmcpEvent(id, path, event string, value any) (CmcpEventResult, error) {
	cm, err := e.cmcpOf(id)
	if err != nil {
		return CmcpEventResult{}, err
	}
	ss := cm.screens()
	ss.mu.Lock()
	var node *pack.BoundNode
	if ss.last != nil {
		ss.last.Walk(func(b *pack.BoundNode) {
			if node == nil && b.Node.Path == path {
				node = b
			}
		})
	}
	env, uiState := ss.lastEnv, ss.ui
	ss.mu.Unlock()
	if node == nil || env == nil {
		return CmcpEventResult{}, fmt.Errorf("engine: no such widget %q on the current screen", path)
	}
	if !node.Enabled {
		return CmcpEventResult{}, fmt.Errorf("engine: widget %q is disabled", path)
	}
	ss.mu.Lock()
	res := node.Node.Perform(event, value, env, uiState, cm.c, e.now())
	ss.mu.Unlock()
	out := CmcpEventResult{Show: res.Show}
	for _, t := range res.Tickets {
		if t.Mutating && t.Echo != "" {
			cm.s.addLine(proto.KindEcho, []proto.Span{{S: "cmcp> " + t.Echo}})
		}
		out.Tickets = append(out.Tickets, CmcpTicket{ID: t.ID, Cmd: t.Cmd, Mutating: t.Mutating, Echo: t.Echo})
	}
	for _, er := range res.Errs {
		out.Errors = append(out.Errors, er.Error())
	}
	if res.Show != "" {
		ui := cm.c.UI()
		ss.mu.Lock()
		ss.override, ss.overFor = res.Show, consoleID(ui)
		ss.mu.Unlock()
	}
	if len(res.Sounds) > 0 {
		var ns []CmcpNote
		for _, s := range res.Sounds {
			ns = append(ns, CmcpNote{Kind: "ui-sound", Text: s.Event, Params: s.Params})
		}
		e.bus.publish(Event{Kind: EvCmcpNote, Session: id, CmcpNotes: ns})
	}
	cm.flush()
	cm.schedule() // $ui or the screen changed even if the model did not
	return out, nil
}

// CmcpSetUI writes a key of the screen's $ui state on behalf of a widget (the team a team-assigner has picked is
// `$ui.team`, so a schematic click can use it); nil deletes. Keys are dotted identifiers.
func (e *Engine) CmcpSetUI(id, key string, value any) error {
	cm, err := e.cmcpOf(id)
	if err != nil {
		return err
	}
	for _, c := range key {
		if !(c == '.' || c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') || key == "" {
			return fmt.Errorf("engine: bad ui key %q", key)
		}
	}
	ss := cm.screens()
	ss.mu.Lock()
	if ss.ui == nil {
		ss.ui = map[string]any{}
	}
	setUIPath(ss.ui, key, value)
	ss.mu.Unlock()
	cm.schedule()
	return nil
}

func setUIPath(m map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	if v == nil {
		delete(m, parts[len(parts)-1])
		return
	}
	m[parts[len(parts)-1]] = v
}

// viewItems places the contacts on the forward view with the shared core's projection and apparent size (spec 7.8).
func viewItems(props map[string]any, scr *pack.Screen) []map[string]any {
	fov, minPx := 60.0, 3.0
	art := map[string]string{}
	def := "marker"
	if sc := scr.Pack.Scenes[fmt.Sprint(props["scene"])]; sc != nil {
		var raw struct {
			FOV      float64 `json:"fov_deg"`
			Contacts struct {
				Art   map[string]string `json:"art"`
				Def   string            `json:"default"`
				MinPx float64           `json:"min_px"`
			} `json:"contacts"`
		}
		if json.Unmarshal(sc.Raw, &raw) == nil {
			if raw.FOV > 0 {
				fov = raw.FOV
			}
			art, def = raw.Contacts.Art, raw.Contacts.Def
			if raw.Contacts.MinPx > 0 {
				minPx = raw.Contacts.MinPx
			}
		}
	}
	cs, _ := props["contacts"].(map[string]any)
	items := []map[string]any{}
	for k, v := range cs {
		c, _ := v.(map[string]any)
		rel, _ := c["rel"].(map[string]any)
		az, ok1 := rel["az"].(float64)
		mk, _ := rel["mark"].(float64)
		d, ok2 := c["distance_m"].(float64)
		if !ok1 || !ok2 || d <= 0 {
			continue
		}
		x, y, vis := pack.Project(az, mk, fov, 0.5)
		if !vis {
			continue
		}
		typ, _ := c["type"].(string)
		class, _ := c["class"].(string)
		size := 120.0
		if typ == "Planet" {
			size = 1.2e7
		}
		if sz, ok := c["size_m"].(float64); ok && sz > 0 {
			size = sz
		}
		empire, _ := c["empire"].(string)
		items = append(items, map[string]any{"cid": k, "name": c["name"], "x": x, "y": y, "size": pack.ApparentFraction(size, d, fov),
			"type": typ, "class": class, "art": pack.LookupArt(art, empire, class, typ, def), "min_px": minPx})
	}
	sort.Slice(items, func(i, j int) bool { return fmt.Sprint(items[i]["cid"]) < fmt.Sprint(items[j]["cid"]) })
	return items
}
