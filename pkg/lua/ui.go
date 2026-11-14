package lua

import (
	glua "github.com/yuin/gopher-lua"
)

// squat.ui: windows and panels a script opens on demand. The runtime only keeps them as data (the variable
// "ui.windows", a map from id to spec); the SquatClient window draws them (docs/LAYOUTS.md, <LuaUI />).
// A spec is a table: id, title, kind ("window" floats, "panel" docks), x/y/w/h or dock, and body, a tree of
// nodes {type = "column"|"row"|"text"|"heading"|"table"|"list"|"button"|"gauge"|"input"|"tabs"|...,
// children = {...}, ...props}. Buttons carry send = "text" — the text goes through the normal input
// pipeline, so a Lua alias can answer it.

const (
	uiVar        = "ui.windows"
	maxUIWindows = 32
)

func (r *Runtime) uiMap() map[string]any {
	m, _ := r.vars[uiVar].(map[string]any)
	cp := make(map[string]any, len(m)+1)
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

// closeUIOwned removes the windows a script opened (it was unloaded). Caller holds mu.
func (r *Runtime) closeUIOwned(owner string) {
	m := r.uiMap()
	n := len(m)
	for id, v := range m {
		if sp, ok := v.(map[string]any); ok && sp["owner"] == owner {
			delete(m, id)
		}
	}
	if len(m) != n {
		_ = r.setVar(uiVar, orNil(m))
	}
}

func orNil(m map[string]any) any {
	if len(m) == 0 {
		return nil
	}
	return m
}

func (r *Runtime) uiTable(L *glua.LState, so *scriptEnv) *glua.LTable {
	t := L.NewTable()
	put := func(kind string, defaults map[string]any) glua.LGFunction {
		return func(L *glua.LState) int {
			v, err := fromLua(L.CheckTable(1))
			if err != nil {
				L.ArgError(1, err.Error())
			}
			spec, _ := v.(map[string]any)
			id, _ := spec["id"].(string)
			if id == "" || len(id) > 64 {
				L.ArgError(1, "ui: id (a short string) is required")
			}
			if !r.cur.live {
				L.Push(glua.LFalse)
				return 1
			}
			m := r.uiMap()
			if _, exists := m[id]; !exists && len(m) >= maxUIWindows {
				L.RaiseError("ui: too many windows (max %d)", maxUIWindows)
			}
			if spec["kind"] == nil {
				spec["kind"] = kind
			}
			for k, d := range defaults {
				if spec[k] == nil {
					spec[k] = d
				}
			}
			spec["owner"] = so.name
			m[id] = spec
			if err := r.setVar(uiVar, m); err != nil {
				L.RaiseError("ui: %s", err.Error())
			}
			L.Push(glua.LString(id))
			return 1
		}
	}
	t.RawSetString("window", L.NewFunction(put("window", nil)))
	t.RawSetString("open", L.NewFunction(put("window", nil)))
	t.RawSetString("panel", L.NewFunction(put("panel", map[string]any{"dock": "right"})))
	t.RawSetString("update", L.NewFunction(func(L *glua.LState) int {
		id := L.CheckString(1)
		pv, err := fromLua(L.CheckTable(2))
		if err != nil {
			L.ArgError(2, err.Error())
		}
		patch, _ := pv.(map[string]any)
		if !r.cur.live {
			L.Push(glua.LFalse)
			return 1
		}
		m := r.uiMap()
		cur, ok := m[id].(map[string]any)
		if !ok {
			L.Push(glua.LFalse)
			return 1
		}
		next := make(map[string]any, len(cur)+len(patch))
		for k, v := range cur {
			next[k] = v
		}
		for k, v := range patch {
			if k != "id" && k != "owner" {
				next[k] = v
			}
		}
		m[id] = next
		if err := r.setVar(uiVar, m); err != nil {
			L.RaiseError("ui: %s", err.Error())
		}
		L.Push(glua.LTrue)
		return 1
	}))
	t.RawSetString("close", L.NewFunction(func(L *glua.LState) int {
		id := L.CheckString(1)
		if !r.cur.live {
			L.Push(glua.LFalse)
			return 1
		}
		m := r.uiMap()
		_, had := m[id]
		delete(m, id)
		if had {
			if err := r.setVar(uiVar, orNil(m)); err != nil {
				L.RaiseError("ui: %s", err.Error())
			}
		}
		L.Push(glua.LBool(had))
		return 1
	}))
	t.RawSetString("list", L.NewFunction(func(L *glua.LState) int {
		out := L.NewTable()
		for _, k := range sortedKeys(r.uiMap()) {
			out.Append(glua.LString(k))
		}
		L.Push(out)
		return 1
	}))
	t.RawSetString("get", L.NewFunction(func(L *glua.LState) int {
		if v, ok := r.uiMap()[L.CheckString(1)]; ok {
			L.Push(toLua(L, v))
		} else {
			L.Push(glua.LNil)
		}
		return 1
	}))
	return t
}
