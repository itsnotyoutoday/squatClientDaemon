package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/itsnotyoutoday/cmcp/msg"
	"github.com/itsnotyoutoday/cmcp/pack"
	cs "github.com/itsnotyoutoday/cmcp/session"
	"github.com/itsnotyoutoday/cmcp/textport"
	"github.com/itsnotyoutoday/cmcp/theme"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// The screen layer of CMCP: which screen is up, its evaluated widget tree, the player's $ui state. All evaluation is
// the shared core's (pack.Bind); the front end receives plain evaluated props and draws them (spec 6.10, class gpu).

// CmcpRender says how screens are themed.
type CmcpRender struct {
	PortDefault    string                            // compact | balanced (default) | large: the text view's default size (spec 6.13.1)
	Calm           bool                              // slower eased motion
	StartCollapsed bool                              // the console screen starts switched off
	Theme          string                            // the player's theme (tng, soft, ...)
	UseShipTheme   bool                              // follow the console/ship/screen suggestion (spec 6.6)
	Tokens         func(theme string) map[string]any // design tokens of a theme (nil: client defaults)
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

// CmcpView is the text view's state (shared core: textport.View) and the player's size choice.
type CmcpView struct {
	State       string  `json:"state"`       // default | expanded | off
	PortDefault string  `json:"portDefault"` // compact | balanced | large
	Fraction    float64 `json:"fraction"`    // the minimum share of the stacked height the textport has in this state
	Calm        bool    `json:"calm"`
}

// CmcpTree is the bound screen.
type CmcpTree struct {
	Session     string         `json:"session"`
	Generation  int            `json:"generation"`
	Screen      string         `json:"screen"` // the id being drawn ("std.damage", "fspace-std/damage")
	Title       string         `json:"title"`
	Console     string         `json:"console,omitempty"`
	Theme       string         `json:"theme"`       // the theme in force after the precedence rules of 6.6
	ThemeSource string         `json:"themeSource"` // pack | builtin | plain (19.10)
	View        CmcpView       `json:"view"`
	Tokens      map[string]any `json:"tokens"`  // resolved design tokens for that theme and pack
	PackKey     string         `json:"packKey"` // changes when the pack or tokens change (sprite cache key part)
	Exhausted   bool           `json:"exhausted,omitempty"`
	Root        *CmcpNode      `json:"root"`
	UI          map[string]any `json:"ui"` // the screen's $ui state, for highlighting a selection
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
	return cm.ss // created with the session
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

// validatePack parses a pack against the server's catalogue without loading it (whole-or-nothing, spec 10.2 rule 3).
func (cm *cmcpSess) validatePack(raw []byte) error {
	_, err := cm.parsePack(raw)
	return err
}

func (cm *cmcpSess) parsePack(raw []byte) (*pack.Pack, error) {
	ui := cm.c.UI()
	var cmds map[string]struct{}
	if ui.Catalogue != nil {
		cmds = map[string]struct{}{}
		for n := range ui.Catalogue {
			cmds[n] = struct{}{}
		}
	}
	return pack.Parse(raw, pack.Options{Commands: cmds})
}

func (cm *cmcpSess) loadPack(raw []byte) error {
	p, err := cm.parsePack(raw)
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

func (cm *cmcpSess) theme(ui *cs.UI, scr *pack.Screen) (string, string) {
	cm.s.e.cmcpMu.Lock()
	r := cm.s.e.cmcpRender
	cm.s.e.cmcpMu.Unlock()
	def := r.Theme
	if def == "" {
		def = "tng"
	}
	// precedence of spec 6.6 (the player's choice, then, only with "use the ship's theme", console, ship, screen): the shared core's rule
	want := theme.Select("", r.UseShipTheme, ui.ConsoleTheme, ui.ShipTheme, scr.SuggestTheme, def)
	if !r.UseShipTheme {
		want = def
	}
	id, src := theme.Resolve(want, nil, theme.BuiltinSet())
	return id, src
}

// view returns the session's text-view state machine (LOG and screen-off toggles).
func (cm *cmcpSess) view() *textport.View {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.tv == nil {
		cm.s.e.cmcpMu.Lock()
		sc := cm.s.e.cmcpRender.StartCollapsed
		cm.s.e.cmcpMu.Unlock()
		cm.tv = textport.NewView(sc)
	}
	return cm.tv
}

// CmcpViewEvent applies a view event (log_toggle, screen_toggle) of the text view and returns the new state.
func (e *Engine) CmcpViewEvent(id, ev string) (string, error) {
	cm, err := e.cmcpOf(id)
	if err != nil {
		return "", err
	}
	st := cm.view().Event(ev)
	cm.schedule()
	return string(st), nil
}

// tree binds the current screen; nil when no console screen is up (the normal text UI).
func (cm *cmcpSess) tree(ui *cs.UI) (out *CmcpTree) {
	defer func() {
		if r := recover(); r != nil { // a defect in screen evaluation must never take the client down: no console screen, the text stays
			cm.s.systemLine(fmt.Sprintf("cmcp: screen evaluation failed: %v", r))
			out = nil
		}
	}()
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
	b := scr.Bind(env, pack.Caps{Has: cmcpWidgets, Class: "gpu"})
	theme, themeSrc := cm.theme(ui, scr)
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
	cm.s.e.cmcpMu.Lock()
	rr := cm.s.e.cmcpRender
	cm.s.e.cmcpMu.Unlock()
	vs := cm.view().State()
	pd := rr.PortDefault
	if pd != "compact" && pd != "large" {
		pd = "balanced"
	}
	viewDTO := CmcpView{State: string(vs), PortDefault: pd, Fraction: textport.Fraction(textport.Stacked, vs), Calm: rr.Calm}
	t := &CmcpTree{Session: cm.s.id, Generation: ui.Generation, Screen: id, Title: scr.Title, Console: scr.Console, Theme: theme, ThemeSource: themeSrc, View: viewDTO, Tokens: toks,
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
		items, effects, backdrop := viewItems(n.Props, scr)
		n.Props["items"], n.Props["effects"], n.Props["backdrop"] = items, effects, backdrop
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
func viewItems(props map[string]any, scr *pack.Screen) ([]map[string]any, map[string]any, map[string]any) {
	fov, minPx := 60.0, 3.0
	art := map[string]string{}
	def := "marker"
	blend := false
	var effects, backdrop map[string]any
	if sc := scr.Pack.Scenes[fmt.Sprint(props["scene"])]; sc != nil {
		var raw struct {
			FOV      float64 `json:"fov_deg"`
			Contacts struct {
				Art   map[string]string `json:"art"`
				Def   string            `json:"default"`
				MinPx float64           `json:"min_px"`
				Views struct {
					Blend bool `json:"blend"`
				} `json:"views"`
			} `json:"contacts"`
			Effects  map[string]any `json:"effects"`
			Backdrop map[string]any `json:"backdrop"`
		}
		if json.Unmarshal(sc.Raw, &raw) == nil {
			if raw.FOV > 0 {
				fov = raw.FOV
			}
			art, def, blend = raw.Contacts.Art, raw.Contacts.Def, raw.Contacts.Views.Blend
			effects, backdrop = raw.Effects, raw.Backdrop
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
		artID := pack.LookupArt(art, empire, class, typ, def)
		item := map[string]any{"cid": k, "name": c["name"], "x": x, "y": y, "size": pack.ApparentFraction(size, d, fov),
			"type": typ, "class": class, "art": artID, "min_px": minPx, "dist": d}
		if sp := scr.Pack.Sprites[artID]; sp != nil && len(sp.ViewNames) > 0 {
			// the aspect: azimuth from the contact's bow to us, elevation of us above its plane (spec 7.7)
			br, _ := c["bearing"].(map[string]any)
			hd, _ := c["heading"].(map[string]any)
			baz, _ := br["az"].(float64)
			bmk, _ := br["mark"].(float64)
			haz, _ := hd["az"].(float64)
			hmk, _ := hd["mark"].(float64)
			roll, _ := hd["roll"].(float64)
			asp := math.Mod(baz+180-haz+720, 360)
			el := -bmk - hmk
			var vs []map[string]any
			view, mirror := pack.SelectView(sp.ViewNames, asp, el)
			if blend {
				for _, w := range pack.BlendViews(sp.ViewNames, asp) {
					am := sp.EvaluateContact(w.View, c)
					vs = append(vs, map[string]any{"view": w.View, "weight": w.Weight, "zones": am.Zones, "glows": am.Glows})
				}
				_, mirror = pack.SelectView(sp.ViewNames, asp, el)
			}
			if len(vs) == 0 {
				am := sp.EvaluateContact(view, c)
				vs = []map[string]any{{"view": view, "weight": 1.0, "zones": am.Zones, "glows": am.Glows}}
			}
			item["views"], item["mirror"], item["roll"] = vs, mirror, roll
		}
		items = append(items, item)
	}
	// far to near, at most 64 (the nearest), spec 7.8
	sort.Slice(items, func(i, j int) bool { return items[i]["dist"].(float64) > items[j]["dist"].(float64) })
	if len(items) > 64 {
		items = items[len(items)-64:]
	}
	return items, effects, backdrop
}

// cmcpWidgets is what this client draws: every core widget and the optional joystick, scope (as a radar) and lights.
func cmcpWidgets(w string) bool {
	return pack.IsCoreWidget(w) || w == "joystick" || w == "scope" || w == "lights"
}

var pkgRefRE = regexp.MustCompile(`@([A-Z][A-Za-z0-9]*(?:\.[A-Z][A-Za-z0-9]*)*):`)

// screenPackages lists the data packages a screen reads (the `@Package:path` references of its bindings).
func screenPackages(scr *pack.Screen) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(n *pack.Node)
	walk = func(n *pack.Node) {
		srcs := []string{}
		for _, v := range n.BindSrc {
			srcs = append(srcs, v)
		}
		for _, src := range srcs {
			for _, m := range pkgRefRE.FindAllStringSubmatch(src, -1) {
				if !seen[m[1]] {
					seen[m[1]] = true
					out = append(out, m[1])
				}
			}
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(scr.Root)
	sort.Strings(out)
	return out
}

// CmcpShowScreen switches the LOCALLY displayed screen of the current console (the console-switch column of the themes): the screen is
// one of the built-in std.* screens (or a screen of the console's pack); the packages it needs are subscribed explicitly (ref counted
// under the owner "nav", released when switching to a screen that needs fewer). The choice lasts until the console changes.
func (e *Engine) CmcpShowScreen(id, screen string) error {
	cm, err := e.cmcpOf(id)
	if err != nil {
		return err
	}
	ui := cm.c.UI()
	if ui.Console == nil {
		return fmt.Errorf("engine: no console is manned")
	}
	var scr *pack.Screen
	if strings.HasPrefix(screen, "std.") {
		scr = pack.Builtin(screen)
	} else if pid, sid, ok := strings.Cut(screen, "/"); ok {
		ss := cm.screens()
		ss.mu.Lock()
		if p := ss.packs[pid]; p != nil {
			scr = p.Screens[sid]
		}
		ss.mu.Unlock()
	}
	if scr == nil {
		return fmt.Errorf("engine: no screen %q", screen)
	}
	implicit := map[string]bool{}
	for _, sb := range ui.Subs {
		if sb.Source == "console" || sb.Source == "always" {
			implicit[sb.Pkg] = true
		}
	}
	var subs []msg.SubSpec
	for _, p := range screenPackages(scr) {
		if !implicit[p] {
			subs = append(subs, msg.SubSpec{Pkg: p})
		}
	}
	ss := cm.screens()
	ss.mu.Lock()
	ss.override, ss.overFor = screen, consoleID(ui)
	ss.mu.Unlock()
	err = e.CmcpAcquire(id, "nav", subs)
	cm.schedule()
	return err
}
