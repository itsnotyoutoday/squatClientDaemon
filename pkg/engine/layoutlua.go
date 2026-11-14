package engine

import "sync"

// Layouts (docs/LAYOUTS.md) talk to Lua through the variable store (squat.var) and a Lua block of their
// own: the layout's <lua> code is loaded as a script tied to the layout and unloaded when another layout
// is chosen.

var layoutLuaMu sync.Mutex

// LuaVars returns a copy of every Lua variable (squat.var).
func (e *Engine) LuaVars() map[string]any { return e.lua.Vars() }

// SetLuaVar sets a Lua variable from outside Lua (a layout's setVar); nil deletes it.
func (e *Engine) SetLuaVar(name string, value any) error { return e.lua.SetVar(name, value) }

// SetLayoutLua loads the Lua block of the layout id (replacing the previous layout's). An empty id, or
// empty code, just unloads. Errors are also shown to the user through the normal script log.
func (e *Engine) SetLayoutLua(id, code string) error {
	layoutLuaMu.Lock()
	defer layoutLuaMu.Unlock()
	if e.layoutLua != "" {
		e.lua.Unload(e.layoutLua)
		e.layoutLua = ""
	}
	if id == "" || code == "" {
		return nil
	}
	name := "layout:" + id
	e.layoutLua = name
	return e.lua.LoadString(name, code)
}
