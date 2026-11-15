package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/itsnotyoutoday/cmcp/go/fetch"
	"github.com/itsnotyoutoday/cmcp/go/msg"
	"github.com/itsnotyoutoday/cmcp/go/pack"
	cs "github.com/itsnotyoutoday/cmcp/go/session"
)

// CMCP 2 definitions (spec 20.3): the game announces a definition document (scopes, context types, layouts) by hash; this client
// either already holds it (the pack cache, listed in Cobra.Hello.defs) or the core asks for it (NoteDefFetch) and the engine fetches
// the blob. A definition is data, not code: there is no consent prompt, every other check (origin, hash, size) is the same.

// holdCachedDefs hands the definitions the cache holds for this host to a new session: they are listed in Cobra.Hello.defs and are
// applied without a transfer when the server announces the same hash.
func (cm *cmcpSess) holdCachedDefs() {
	st := cm.s.e.packStore()
	if st == nil {
		return
	}
	host := cm.s.world.Host
	for _, d := range st.cache.RecentDefs(host, 16) {
		if raw, _, ok := st.cache.Get(host, d.SHA256); ok {
			if err := cm.c.HoldDefinition(raw); err != nil {
				st.cache.Remove(host, d.SHA256)
			}
		}
	}
}

// defFetch fetches a definition the server pointed to (Cobra.Defs.Ref; the core already checked the url against Welcome.assets.routes).
func (cm *cmcpSess) defFetch(b fetch.Blob) {
	f := fetch.NewFetcher(cm.fetchPolicy())
	go func() {
		ctx, cancel := context.WithTimeout(cm.s.e.ctx, fetch.PackTotal)
		defer cancel()
		raw, err := f.Fetch(ctx, b)
		now := cm.s.e.now()
		if err != nil {
			cm.clog("definition fetch failed: %v", err)
			cm.c.DefinitionFailed(now, err.Error())
		} else if err := cm.c.DefinitionAvailable(now, raw); err != nil {
			cm.clog("definition refused: %v", err)
		}
		cm.flush()
	}()
}

// defApplied stores a verified definition in the pack cache under its own kind, and asks for the layout packs it lists.
func (cm *cmcpSess) defApplied(n cs.Note) {
	if st := cm.s.e.packStore(); st != nil && n.Def != nil && n.DefBytes != nil {
		host := cm.s.world.Host
		_ = st.cache.Put(host, fetch.Meta{ID: n.Def.ID, Version: n.Def.Version, Kind: fetch.KindDef, SHA256: n.Def.SHA256, Size: len(n.DefBytes), Fetched: time.Now(), Host: host}, n.DefBytes)
	}
	cm.defPacks()
}

// defPacks asks for every layout pack the definition lists and the game announced: the screens of its context types are in them.
func (cm *cmcpSess) defPacks() {
	ui := cm.c.UI()
	if !ui.V2 || ui.Definition == nil {
		return
	}
	for _, lp := range ui.Definition.Layouts {
		cm.screens().mu.Lock()
		have := cm.screens().packs[lp.ID] != nil
		cm.screens().mu.Unlock()
		if !have {
			cm.c.WantPack(lp.ID)
		}
	}
}

// layoutScreenID resolves a v2 layout reference to the screen to draw: "std.<name>" and "<pack>/<screen>" are as in v1; "by:<field>"
// (the layout is looked up by the context's id, for example by:console on the context "helm") means the built-in std.<value>, else a
// screen of that name in a loaded pack.
func (cm *cmcpSess) layoutScreenID(ui *cs.UI, id string) string {
	if !strings.HasPrefix(id, "by:") || ui.LayoutByValue == "" {
		return id
	}
	v := ui.LayoutByValue
	if fb := ui.LayoutFallback; fb != "" && pack.Builtin(fb) != nil { // the core's std.<value>
		return fb
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

func (cm *cmcpSess) defStatusLine(ui *cs.UI) string {
	if !ui.V2 {
		return ""
	}
	if ui.TextClient {
		return fmt.Sprintf("definition refused (%s): text mode", ui.TextReason)
	}
	if ui.Def == nil {
		return "waiting for the game's definition"
	}
	return fmt.Sprintf("definition %s %s", ui.Def.ID, ui.Def.Version)
}

// cmcpGate is the Core.Supports.Set list the telnet layer declares: the versions the configured client speaks ("Cobra 2" for a v2 client, spec
// 20.2.1; both when the client was set up to bridge), as JSON. "" = the telnet layer's default.
func (e *Engine) cmcpGate(world string) string {
	e.cmcpMu.Lock()
	mk := e.cmcpCfg
	e.cmcpMu.Unlock()
	if mk == nil {
		return ""
	}
	cfg := e.cmcpConfigFor(mk, world)
	if len(cfg.Protos) == 0 {
		return ""
	}
	ps := append([]int(nil), cfg.Protos...)
	sort.Sort(sort.Reverse(sort.IntSlice(ps)))
	var g []string
	for _, p := range ps {
		g = append(g, msg.Gate(p))
	}
	b, _ := json.Marshal(g)
	return string(b)
}

// SetWorldCmcpProto sets which CMCP version each world declares: world name -> 1 or 2 (a world not in the map uses the version of the client's
// config). A world speaks exactly one version; the client never declares both for one world.
func (e *Engine) SetWorldCmcpProto(m map[string]int) {
	c := make(map[string]int, len(m))
	for k, v := range m {
		if v == 1 || v == 2 {
			c[strings.ToLower(strings.TrimSpace(k))] = v
		}
	}
	e.cmcpMu.Lock()
	e.worldCmcpProto = c
	e.cmcpMu.Unlock()
}

// cmcpConfigFor builds the Hello description for a session of the named world: the client's config, with the world's own protocol version.
func (e *Engine) cmcpConfigFor(mk func() cs.Config, world string) cs.Config {
	cfg := mk()
	e.cmcpMu.Lock()
	p := e.worldCmcpProto[strings.ToLower(strings.TrimSpace(world))]
	e.cmcpMu.Unlock()
	if p != 0 {
		cfg.Protos = []int{p}
	}
	return cfg
}

// statusDoc is the document the frame's status strip shows (name, alert, hull, core, lock). v1 names it (Space.Ship.Status); in v2 it is the
// always-subscribed package of a scope that has an owner, as the definition says (spec 20.10), so a game with another name for it, or no ship
// at all, needs no client change. Nil when there is none.
func statusDoc(ui *cs.UI) map[string]any {
	if !ui.V2 {
		st, _ := ui.Docs[msg.PkgShipStatus].(map[string]any)
		return st
	}
	if ui.Definition == nil {
		return nil
	}
	var others []string
	for n := range ui.Scopes {
		if n != ui.CtxScope {
			others = append(others, n)
		}
	}
	sort.Strings(others)
	scopes := others
	if ui.CtxScope != "" { // the active context's own scope first
		scopes = append([]string{ui.CtxScope}, others...)
	}
	for _, n := range scopes {
		if !ui.HasOwner(n) {
			continue
		}
		for _, pkg := range ui.Definition.AlwaysPackages(n) {
			if st, ok := ui.Docs[pkg].(map[string]any); ok {
				return st
			}
		}
	}
	return nil
}

// announcedPacks is every pack the game announced for this session: Welcome.packs, the ship's packs (v1) and the packs a scope's owner brings
// (v2: a ship's own layout pack and theme), once each. Themes, layouts and sounds are looked up here, not in one of those lists.
func announcedPacks(ui *cs.UI) []msg.PackRef {
	var out []msg.PackRef
	seen := map[string]bool{}
	add := func(ps []msg.PackRef) {
		for _, p := range ps {
			if !seen[p.SHA256] {
				seen[p.SHA256] = true
				out = append(out, p)
			}
		}
	}
	if ui.Welcome != nil {
		add(ui.Welcome.Packs)
	}
	add(ui.Ship.Packs)
	names := make([]string, 0, len(ui.Scopes))
	for n := range ui.Scopes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if o := ui.Scopes[n].Owner; o != nil {
			add(o.Packs)
		}
	}
	return out
}

// SetWorldDevHTTP marks the worlds that are local development games: for them (and only when their host is loopback, which the fetch policy checks)
// the definition, packs and sounds the game announces may come over plain http from the origin of its own Welcome. It is the per-world form of
// SQUATCLIENT_DEV_LOOPBACK_HTTP=1; every other check (hash, size, origin announced) is unchanged.
func (e *Engine) SetWorldDevHTTP(m map[string]bool) {
	c := make(map[string]bool, len(m))
	for k, v := range m {
		if v {
			c[strings.ToLower(strings.TrimSpace(k))] = true
		}
	}
	e.cmcpMu.Lock()
	e.worldDevHTTP = c
	e.cmcpMu.Unlock()
}

func (e *Engine) devHTTPFor(world string) bool {
	e.cmcpMu.Lock()
	defer e.cmcpMu.Unlock()
	return e.worldDevHTTP[strings.ToLower(strings.TrimSpace(world))]
}
