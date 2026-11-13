package lua

import (
	"strings"
	"time"

	glua "github.com/yuin/gopher-lua"
)

// safeBase lists the base-library globals copied into every script environment.
// Deliberately absent: dofile, loadfile, load/loadstring (replaced by env-bound
// versions), collectgarbage (stubbed), getfenv/setfenv, module/require
// (replaced), newproxy, print (replaced by a logging version), _printregs, and
// the debug/io/package libraries (never opened).
var safeBase = []string{
	"assert", "error", "ipairs", "next", "pairs", "pcall", "rawequal", "rawget",
	"rawset", "select", "setmetatable", "tonumber", "tostring", "type", "unpack",
	"xpcall", "_VERSION",
}

// newState builds a fresh sandboxed Lua state with the safe libraries opened
// and pre-sanitised, and records the pristine libs used to seed environments.
func (r *Runtime) newState() *glua.LState {
	L := glua.NewState(glua.Options{
		SkipOpenLibs:        true,
		CallStackSize:       r.opts.CallStackSize,
		RegistrySize:        5120,
		RegistryMaxSize:     r.opts.RegistryMaxSize,
		RegistryGrowStep:    64,
		MinimizeStackMemory: true,
	})
	open := func(name string, f glua.LGFunction) {
		L.Push(L.NewFunction(f))
		L.Push(glua.LString(name))
		L.Call(1, 0)
	}
	open(glua.BaseLibName, glua.OpenBase)
	open(glua.TabLibName, glua.OpenTable)
	open(glua.StringLibName, glua.OpenString)
	open(glua.MathLibName, glua.OpenMath)
	open(glua.OsLibName, glua.OpenOs)
	open(glua.CoroutineLibName, glua.OpenCoroutine)

	g := L.Get(glua.GlobalsIndex).(*glua.LTable)
	r.base = map[string]glua.LValue{}
	for _, n := range safeBase {
		r.base[n] = g.RawGetString(n)
	}
	// Strip everything unsafe from the real globals too (defence in depth: no
	// script environment inherits from them, but chunks must never see them).
	for _, n := range []string{"dofile", "loadfile", "load", "loadstring", "collectgarbage",
		"getfenv", "setfenv", "module", "require", "newproxy", "print", "_printregs", "getmetatable"} {
		g.RawSetString(n, glua.LNil)
	}

	// getmetatable must not expose the shared string metatable.
	gm := L.NewFunction(func(L *glua.LState) int {
		v := L.CheckAny(1)
		if _, ok := v.(glua.LString); ok {
			L.Push(glua.LNil)
			return 1
		}
		L.Push(L.GetMetatable(v))
		return 1
	})
	r.base["getmetatable"] = gm

	r.libs = map[string]*glua.LTable{}
	// string: drop dump, cap rep. Mutating the original is intentional: the
	// string metatable's __index points at it, and scripts cannot reach it.
	str := g.RawGetString("string").(*glua.LTable)
	str.RawSetString("dump", glua.LNil)
	maxRep := r.opts.MaxStringRep
	origRep := str.RawGetString("rep").(*glua.LFunction)
	str.RawSetString("rep", L.NewFunction(func(L *glua.LState) int {
		s, n := L.CheckString(1), L.CheckInt(2)
		if n > 0 && len(s) > 0 && len(s)*n > maxRep || n > 0 && len(s)*n < 0 {
			L.RaiseError("string.rep: result exceeds %d bytes", maxRep)
		}
		L.Push(origRep)
		L.Push(glua.LString(s))
		L.Push(glua.LNumber(n))
		L.Call(2, 1)
		return 1
	}))
	r.libs["string"] = str
	r.libs["table"] = g.RawGetString("table").(*glua.LTable)
	r.libs["math"] = g.RawGetString("math").(*glua.LTable)
	r.libs["coroutine"] = g.RawGetString("coroutine").(*glua.LTable)

	// os: only time/date/clock/difftime, driven by the Host clock.
	osOrig := g.RawGetString("os").(*glua.LTable)
	origTime := osOrig.RawGetString("time").(*glua.LFunction)
	origDate := osOrig.RawGetString("date").(*glua.LFunction)
	osT := L.NewTable()
	osT.RawSetString("difftime", osOrig.RawGetString("difftime"))
	osT.RawSetString("time", L.NewFunction(func(L *glua.LState) int {
		if L.GetTop() == 0 {
			L.Push(glua.LNumber(r.host.Now().Unix()))
			return 1
		}
		L.Push(origTime)
		L.Push(L.Get(1))
		L.Call(1, 1)
		return 1
	}))
	osT.RawSetString("date", L.NewFunction(func(L *glua.LState) int {
		f := L.OptString(1, "%c")
		var ts int64
		if L.GetTop() >= 2 {
			ts = L.CheckInt64(2)
		} else {
			ts = r.host.Now().Unix()
		}
		L.Push(origDate)
		L.Push(glua.LString(f))
		L.Push(glua.LNumber(ts))
		L.Call(2, 1)
		return 1
	}))
	osT.RawSetString("clock", L.NewFunction(func(L *glua.LState) int {
		L.Push(glua.LNumber(r.host.Now().Sub(r.started).Seconds()))
		return 1
	}))
	r.libs["os"] = osT
	g.RawSetString("os", glua.LNil)
	g.RawSetString("string", glua.LNil)
	g.RawSetString("table", glua.LNil)
	g.RawSetString("math", glua.LNil)
	g.RawSetString("coroutine", glua.LNil)
	return L
}

func cloneTable(L *glua.LState, src *glua.LTable) *glua.LTable {
	dst := L.CreateTable(0, 16)
	src.ForEach(func(k, v glua.LValue) { dst.RawSet(k, v) })
	return dst
}

// scriptEnv is one plugin/script's private global environment.
type scriptEnv struct {
	name    string
	dir     string // plugin root for require ("" = scripts root)
	env     *glua.LTable
	modules map[string]glua.LValue
	loading map[string]bool
}

// newEnv creates a fresh environment table: cloned libraries (so a script
// altering `string` or `table` cannot affect others), the safe base functions,
// env-bound loadstring/load, a controlled require, a logging print and the
// plugin's own `squat` API table.
func (r *Runtime) newEnv(name, dir string) *scriptEnv {
	L := r.L
	so := &scriptEnv{name: name, dir: dir, modules: map[string]glua.LValue{}, loading: map[string]bool{}}
	env := L.CreateTable(0, 48)
	so.env = env
	for n, v := range r.base {
		env.RawSetString(n, v)
	}
	for n, lib := range r.libs {
		env.RawSetString(n, cloneTable(L, lib))
	}
	env.RawSetString("_G", env)
	env.RawSetString("collectgarbage", L.NewFunction(func(L *glua.LState) int {
		L.Push(glua.LNumber(0))
		return 1
	}))
	env.RawSetString("print", L.NewFunction(func(L *glua.LState) int {
		parts := make([]string, 0, L.GetTop())
		for i := 1; i <= L.GetTop(); i++ {
			parts = append(parts, L.ToStringMeta(L.Get(i)).String())
		}
		r.logf(LogInfo, so.name, "%s", strings.Join(parts, "\t"))
		return 0
	}))
	compile := func(L *glua.LState, src, chunk string) (*glua.LFunction, error) {
		if strings.HasPrefix(src, "\x1b") {
			return nil, errBinaryChunk
		}
		fn, err := L.Load(strings.NewReader(src), chunk)
		if err != nil {
			return nil, err
		}
		fn.Env = env
		return fn, nil
	}
	env.RawSetString("loadstring", L.NewFunction(func(L *glua.LState) int {
		fn, err := compile(L, L.CheckString(1), L.OptString(2, "=(loadstring)"))
		if err != nil {
			L.Push(glua.LNil)
			L.Push(glua.LString(err.Error()))
			return 2
		}
		L.Push(fn)
		return 1
	}))
	env.RawSetString("load", L.NewFunction(func(L *glua.LState) int {
		reader := L.CheckFunction(1)
		var sb strings.Builder
		for {
			L.Push(reader)
			L.Call(0, 1)
			v := L.Get(-1)
			L.Pop(1)
			s, ok := v.(glua.LString)
			if !ok || s == "" {
				break
			}
			sb.WriteString(string(s))
			if sb.Len() > 1<<20 {
				L.RaiseError("load: chunk too large")
			}
		}
		fn, err := compile(L, sb.String(), L.OptString(2, "=(load)"))
		if err != nil {
			L.Push(glua.LNil)
			L.Push(glua.LString(err.Error()))
			return 2
		}
		L.Push(fn)
		return 1
	}))
	env.RawSetString("require", L.NewFunction(r.requireFn(so)))
	env.RawSetString("squat", r.buildSquat(so))
	return so
}

// now exposes the host clock as float seconds.
func (r *Runtime) nowSeconds() float64 {
	t := r.host.Now()
	return float64(t.UnixNano()) / float64(time.Second)
}
