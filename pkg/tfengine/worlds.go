package tfengine

import (
	"strings"

	"github.com/itsnotyoutoday/gtfparser"
)

// The client's world store is the source of truth for the world book. The
// engine keeps its own world database (it backs /listworlds, /connect name,
// world_info(), %{world_*}); this file keeps the two in step:
//
//   - before every call, if the host's list changed, the changes are defined
//     in (or removed from) the engine's database (syncWorlds);
//   - after every call, what a script changed in the engine's database
//     (/addworld, /unworld, /renameworld) is pushed to the host, which persists
//     it (reconcileWorlds).

func fold(s string) string { return strings.ToLower(s) }

func worldSig(ws []WorldInfo) string {
	var b strings.Builder
	for _, w := range ws {
		b.WriteString(fold(w.Name) + "\x00" + w.Host + "\x00" + w.Port + "\x00" + w.Character + "\x00" + w.Type + "\x00")
		if w.TLS {
			b.WriteByte('x')
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func toCore(w WorldInfo) gtfparser.WorldInfo {
	return gtfparser.WorldInfo{Name: w.Name, Type: w.Type, Host: w.Host, Port: w.Port, Character: w.Character,
		Password: w.Password, SSL: w.TLS, Temp: w.Temp}
}

func fromCore(w gtfparser.WorldInfo) WorldInfo {
	return WorldInfo{Name: w.Name, Type: w.Type, Host: w.Host, Port: w.Port, Character: w.Character,
		Password: w.Password, TLS: w.SSL, Temp: w.Temp}
}

func sameWorld(a, b gtfparser.WorldInfo) bool {
	return a.Host == b.Host && a.Port == b.Port && a.Character == b.Character && a.Type == b.Type && a.SSL == b.SSL
}

// syncWorlds brings the engine's database up to date with the host's list.
func (a *Engine) syncWorlds(in *gtfparser.Interp) {
	ws := a.host.Worlds()
	sig := worldSig(ws)
	if sig == a.worldSig {
		return
	}
	a.muted = true
	defer func() { a.muted = false }()
	now := map[string]bool{}
	for _, w := range ws {
		now[fold(w.Name)] = true
		cw := toCore(w)
		if old, ok := in.World(w.Name); ok && sameWorld(old, cw) {
			continue
		}
		in.DefineWorld(cw)
	}
	for n := range a.synced {
		if !now[n] {
			in.RemoveWorld(n)
		}
	}
	a.synced = now
	a.worldSig = sig
}

// reconcileWorlds pushes what a script changed in the engine's database to
// the host.
func (a *Engine) reconcileWorlds(in *gtfparser.Interp) {
	renames := a.renames
	a.renames = nil
	for _, r := range renames {
		if err := a.host.RenameWorld(r[0], r[1]); err != nil {
			a.warn("/renameworld: " + err.Error())
		}
		delete(a.synced, fold(r[0]))
	}
	core := in.Worlds()
	host := map[string]WorldInfo{}
	for _, w := range a.host.Worlds() {
		host[fold(w.Name)] = w
	}
	changed := len(renames) > 0
	present := map[string]bool{}
	for _, w := range core {
		if w.Name == "default" || w.Temp {
			continue
		}
		present[fold(w.Name)] = true
		hw, ok := host[fold(w.Name)]
		if ok && sameWorld(w, toCore(hw)) {
			a.synced[fold(w.Name)] = true
			continue
		}
		if err := a.host.PutWorld(fromCore(w)); err != nil {
			a.warn("world " + w.Name + ": " + err.Error())
			continue
		}
		a.synced[fold(w.Name)] = true
		changed = true
	}
	for n := range a.synced {
		if !present[n] {
			if err := a.host.DeleteWorld(n); err != nil {
				a.warn("world " + n + ": " + err.Error())
			}
			delete(a.synced, n)
			changed = true
		}
	}
	if changed {
		a.worldSig = worldSig(a.host.Worlds())
	}
}
