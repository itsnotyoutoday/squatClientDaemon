package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/itsnotyoutoday/cmcp/go/pack"
	cs "github.com/itsnotyoutoday/cmcp/go/session"
)

// Panes (CMCP 2, spec 20.14): a context the game wants on a panel, dock or float is shown IN ADDITION to the main screen, one pane each, while it is on the
// stack. The core says which contexts and where (UI.Panes); the engine binds each one's layout like the main screen's, without the text port (the main
// text pane is not drawn twice), and the window draws the pane. Where, how big and with what chrome is the window's choice.

// CmcpPane is one off-screen context with its bound layout.
type CmcpPane struct {
	Surface   string         `json:"surface"` // panel | dock | float
	Key       string         `json:"key"`
	Ctx       int64          `json:"ctx"`
	Type      string         `json:"type"`
	Scope     string         `json:"scope"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Title     string         `json:"title,omitempty"`
	Layout    string         `json:"layout"`
	Dock      string         `json:"dock,omitempty"` // hints from the type: left | right | top | bottom, resizable, size
	Resizable bool           `json:"resizable,omitempty"`
	Size      map[string]any `json:"size,omitempty"`
	Readonly  bool           `json:"readonly,omitempty"`
	Commands  []string       `json:"commands,omitempty"`
	Tree      *CmcpTree      `json:"tree"` // nil: the layout is not available yet (its pack is still coming)
}

// paneState is one pane's own screen state: the bound tree for events, and its $ui.
type paneState struct {
	ui      map[string]any
	last    *pack.BoundNode
	lastEnv *pack.Env
	lastScr *pack.Screen
	layout  string
}

// paneTrees binds every off-screen context of the snapshot. Panes whose key left the stack are forgotten.
func (cm *cmcpSess) paneTrees(ui *cs.UI) []CmcpPane {
	ss := cm.screens()
	ss.mu.Lock()
	if ss.panes == nil {
		ss.panes = map[string]*paneState{}
	}
	ss.mu.Unlock()
	var out []CmcpPane
	live := map[string]bool{}
	if ui.V2 && !ui.TextClient && ui.State == cs.StateReady {
		surfaces := make([]string, 0, len(ui.Panes))
		for s := range ui.Panes {
			surfaces = append(surfaces, s)
		}
		sort.Strings(surfaces)
		for _, sf := range surfaces {
			for _, pv := range ui.Panes[sf] {
				live[pv.Key] = true
				p := CmcpPane{Surface: sf, Key: pv.Key, Ctx: pv.Ctx, Type: pv.Type, Scope: pv.Scope, ID: pv.ID, Name: pv.Name, Title: pv.Title, Layout: pv.Layout,
					Dock: pv.Dock, Resizable: pv.Resizable, Size: pv.Size, Readonly: pv.Readonly, Commands: pv.Commands}
				p.Tree = cm.paneTree(ui, pv)
				out = append(out, p)
			}
		}
	}
	ss.mu.Lock()
	for k := range ss.panes {
		if !live[k] {
			delete(ss.panes, k)
		}
	}
	ss.mu.Unlock()
	return out
}

// paneTree binds one pane's layout. It never takes the client down: a defect in evaluation leaves the pane without a tree.
func (cm *cmcpSess) paneTree(ui *cs.UI, pv cs.PaneView) (out *CmcpTree) {
	defer func() {
		if r := recover(); r != nil {
			cm.s.systemLine(fmt.Sprintf("cmcp: pane %s evaluation failed: %v", pv.Key, r))
			out = nil
		}
	}()
	id := cm.layoutScreenIDFor(ui, pv.Layout, pv.Key, pv.ID)
	if id == "" {
		return nil
	}
	scr := cm.screenByID(id)
	if scr == nil {
		return nil
	}
	ss := cm.screens()
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ps := ss.panes[pv.Key]
	if ps == nil || ps.layout != id {
		ps = &paneState{ui: map[string]any{}, layout: id}
		ss.panes[pv.Key] = ps
	}
	env := pack.EnvFromUI(ui, ps.ui)
	ctx := map[string]any{"key": pv.Key, "type": pv.Type, "scope": pv.Scope, "id": pv.ID, "layout": pv.Layout, "data": map[string]any{}}
	if pv.Name != "" {
		ctx["name"] = pv.Name
	}
	if pv.Title != "" {
		ctx["title"] = pv.Title
	}
	if pv.Data != nil {
		ctx["data"] = pv.Data
	}
	env.Ctx = ctx // $ctx.data.* of THIS context, not the active one
	b := scr.Bind(env, pack.Caps{Has: cmcpWidgets, Class: "gpu"})
	themeID, themeSrc := cm.theme(ui, scr)
	cm.s.e.cmcpMu.Lock()
	tf := cm.s.e.cmcpRender.Tokens
	cm.s.e.cmcpMu.Unlock()
	var tt map[string]any
	if tf != nil {
		tt = tf(themeID)
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
	ps.last, ps.lastEnv, ps.lastScr = b, env, scr
	title := pv.Title
	if title == "" {
		title = scr.Title
	}
	return &CmcpTree{Session: cm.s.id, Generation: ui.Generation, Screen: id, Title: title, Theme: themeID, ThemeSource: themeSrc, AlertMode: cm.alertModeNow(),
		Tokens: toks, PackKey: scr.Pack.SHA256 + "/" + themeID + "@" + themeSrc, Exhausted: b.Exhausted, Root: dtoNodeX(b, scr, ui, cs.OmitTextport(pv.Surface)), UI: copyMap(ps.ui)}
}

// screenByID finds a built-in or loaded pack screen by its id ("std.x" or "pack/screen").
func (cm *cmcpSess) screenByID(id string) *pack.Screen {
	if strings.HasPrefix(id, "std.") {
		return pack.Builtin(id)
	}
	pid, sid, ok := strings.Cut(id, "/")
	if !ok {
		return nil
	}
	ss := cm.screens()
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if p := ss.packs[pid]; p != nil {
		return p.Screens[sid]
	}
	return nil
}

// layoutScreenIDFor resolves a layout reference of a pane context: "by:<field>" looks the screen up by the context's id (or the part of its key after
// the colon), as for the main screen.
func (cm *cmcpSess) layoutScreenIDFor(ui *cs.UI, layout, key, id string) string {
	if !strings.HasPrefix(layout, "by:") {
		return layout
	}
	v := id // by:id; for by:console and by:room the part of the key after the colon (the id when there is none)
	if layout == "by:console" || layout == "by:room" {
		if i := strings.IndexByte(key, ':'); i >= 0 {
			v = key[i+1:]
		}
	}
	if v == "" {
		return ""
	}
	if pack.Builtin("std."+v) != nil {
		return "std." + v
	}
	ss := cm.screens()
	ss.mu.Lock()
	defer ss.mu.Unlock()
	for pid, p := range ss.packs {
		if p.Screens[v] != nil {
			return pid + "/" + v
		}
	}
	return ""
}

// CmcpPaneEvent is CmcpEvent for a widget of a pane's layout (key names the pane's context).
func (e *Engine) CmcpPaneEvent(id, key, path, event string, value any) (CmcpEventResult, error) {
	cm, err := e.cmcpOf(id)
	if err != nil {
		return CmcpEventResult{}, err
	}
	ss := cm.screens()
	ss.mu.Lock()
	ps := ss.panes[key]
	var node *pack.BoundNode
	var env *pack.Env
	var ui map[string]any
	if ps != nil && ps.last != nil {
		ps.last.Walk(func(b *pack.BoundNode) {
			if node == nil && b.Node.Path == path {
				node = b
			}
		})
		env, ui = ps.lastEnv, ps.ui
	}
	ss.mu.Unlock()
	if node == nil || env == nil {
		return CmcpEventResult{}, fmt.Errorf("engine: no such widget %q in pane %q", path, key)
	}
	if !node.Enabled {
		return CmcpEventResult{}, fmt.Errorf("engine: widget %q is disabled", path)
	}
	ss.mu.Lock()
	res := node.Node.Perform(event, value, env, ui, cm.c, e.now())
	ss.mu.Unlock()
	return cm.eventResult(res), nil
}
