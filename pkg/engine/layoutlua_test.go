package engine_test

import (
	"sync"
	"testing"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/engine"
)

func TestLayoutLuaVarsBothWays(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]any{}
	e, _ := newEngine(t, func(o *engine.Options) {
		o.OnLuaVar = func(n string, v any) { mu.Lock(); seen[n] = v; mu.Unlock() }
	})
	// from Go (a layout's setVar)
	if err := e.SetLuaVar("hp", 5.0); err != nil {
		t.Fatal(err)
	}
	if e.LuaVars()["hp"] != 5.0 {
		t.Fatalf("vars: %v", e.LuaVars())
	}
	// from the layout's Lua block
	if err := e.SetLayoutLua("a", `squat.var.set("mode", "fight")`); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if seen["mode"] != "fight" || seen["hp"] != 5.0 {
		t.Fatalf("observer saw %v", seen)
	}
	mu.Unlock()
	// switching layouts unloads; clearing works
	if err := e.SetLayoutLua("", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.SetLuaVar("hp", nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if v, ok := seen["hp"]; !ok || v != nil {
		t.Fatalf("delete not observed: %v", seen)
	}
}
