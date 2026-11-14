package lua

import (
	"encoding/json"
	"fmt"

	glua "github.com/yuin/gopher-lua"
)

// The variable store is owned by the Runtime (and through it the engine):
// JSON-compatible values keyed by string, shared by all scripts, surviving
// Reload, exportable and importable so it can be synced or persisted.

func (r *Runtime) checkVarKey(k string) error {
	if k == "" || len(k) > 256 {
		return fmt.Errorf("variable name must be 1-256 bytes")
	}
	return nil
}

// setVar stores v (JSON-compatible). nil deletes. Caller holds mu.
func (r *Runtime) setVar(k string, v any) error {
	if err := r.checkVarKey(k); err != nil {
		return err
	}
	if v == nil {
		delete(r.vars, k)
		if r.opts.OnVar != nil {
			r.opts.OnVar(k, nil)
		}
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > r.opts.MaxVarBytes {
		return fmt.Errorf("variable %q larger than %d bytes", k, r.opts.MaxVarBytes)
	}
	if _, exists := r.vars[k]; !exists && len(r.vars) >= r.opts.MaxVars {
		return fmt.Errorf("too many variables (max %d)", r.opts.MaxVars)
	}
	var norm any
	if err := json.Unmarshal(b, &norm); err != nil {
		return err
	}
	r.vars[k] = norm
	if r.opts.OnVar != nil {
		r.opts.OnVar(k, deepCopy(norm))
	}
	return nil
}

// SetVar sets a variable from Go (nil deletes it).
func (r *Runtime) SetVar(key string, v any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.setVar(key, v)
}

// GetVar returns a deep copy of a variable.
func (r *Runtime) GetVar(key string) (any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.vars[key]
	if !ok {
		return nil, false
	}
	return deepCopy(v), true
}

func deepCopy(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// Vars returns a deep copy of the whole store.
func (r *Runtime) Vars() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]any, len(r.vars))
	for k, v := range r.vars {
		out[k] = deepCopy(v)
	}
	return out
}

// ExportVars serialises the store as a JSON object (keys sorted).
func (r *Runtime) ExportVars() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return json.Marshal(r.vars)
}

// ImportVars replaces the store with the JSON object in data. On error the
// store is left unchanged.
func (r *Runtime) ImportVars(data []byte) error {
	var in map[string]any
	if err := json.Unmarshal(data, &in); err != nil {
		return fmt.Errorf("lua: import vars: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	saved := r.vars
	r.vars = map[string]any{}
	for k, v := range in {
		if err := r.setVar(k, v); err != nil {
			r.vars = saved
			return fmt.Errorf("lua: import vars: %w", err)
		}
	}
	return nil
}

// varTable builds squat.var: get(name[, default]), set(name, value), del(name),
// keys(). set with nil deletes. Values must be JSON-compatible.
func (r *Runtime) varTable(L *glua.LState) *glua.LTable {
	t := L.NewTable()
	t.RawSetString("get", L.NewFunction(func(L *glua.LState) int {
		k := L.CheckString(1)
		v, ok := r.vars[k]
		if !ok {
			if L.GetTop() >= 2 {
				L.Push(L.Get(2))
			} else {
				L.Push(glua.LNil)
			}
			return 1
		}
		L.Push(toLua(L, v))
		return 1
	}))
	t.RawSetString("set", L.NewFunction(func(L *glua.LState) int {
		k := L.CheckString(1)
		gv, err := fromLua(L.CheckAny(2))
		if err != nil {
			L.ArgError(2, err.Error())
		}
		if !r.cur.live {
			L.Push(glua.LFalse)
			return 1
		}
		if err := r.setVar(k, gv); err != nil {
			L.RaiseError("var.set: %s", err.Error())
		}
		L.Push(glua.LTrue)
		return 1
	}))
	t.RawSetString("del", L.NewFunction(func(L *glua.LState) int {
		k := L.CheckString(1)
		if !r.cur.live {
			L.Push(glua.LFalse)
			return 1
		}
		_, had := r.vars[k]
		delete(r.vars, k)
		L.Push(glua.LBool(had))
		return 1
	}))
	t.RawSetString("keys", L.NewFunction(func(L *glua.LState) int {
		out := L.NewTable()
		for _, k := range sortedKeys(r.vars) {
			out.Append(glua.LString(k))
		}
		L.Push(out)
		return 1
	}))
	return t
}
