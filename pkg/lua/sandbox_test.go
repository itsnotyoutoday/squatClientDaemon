package lua

import (
	"strings"
	"testing"
	"time"
)

func TestSandboxEscapes(t *testing.T) {
	cases := map[string]string{
		"io":           `return io.open("/etc/passwd")`,
		"os.execute":   `os.execute("id")`,
		"os.remove":    `os.remove("/tmp/x")`,
		"os.getenv":    `return os.getenv("HOME")`,
		"os.exit":      `os.exit(1)`,
		"dofile":       `dofile("/etc/passwd")`,
		"loadfile":     `loadfile("/etc/passwd")`,
		"package":      `return package.loaded`,
		"debug":        `return debug.getinfo(1)`,
		"string.dump":  `return string.dump(function() end)`,
		"dump method":  `return ("x"):dump()`,
		"getfenv":      `return getfenv(0)`,
		"setfenv":      `setfenv(1, {})`,
		"newproxy":     `return newproxy()`,
		"module":       `module("x")`,
		"strmeta":      `getmetatable("").__index.evil = 1`,
		"require abs":  `require("/etc/passwd")`,
		"require dots": `require("..")`,
		"require trav": `require("../../etc/passwd")`,
		"require os":   `require("os")`,
		"require io":   `require("io")`,
		"binary chunk": `local f = assert(loadstring("\27Lua")) f()`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			r, h := newRT(t, Options{})
			err := r.LoadString("t", src)
			if err == nil {
				t.Fatalf("expected error, logs: %s", h.logText())
			}
		})
	}
}

func TestSandboxLoadstringStaysInEnv(t *testing.T) {
	r, _ := newRT(t, Options{})
	mustLoad(t, r, `
	  leak = 1
	  local f = loadstring("seen_leak = leak; planted = 7")
	  f()
	  assert(seen_leak == 1 and planted == 7)
	  assert(loadstring("io.open('x')") ~= nil)
	  assert(not pcall(loadstring("return io.open('x')")))
	`)
	if err := r.Exec("w", `assert(leak == nil and planted == nil)`); err != nil {
		t.Fatalf("env leaked: %v", err)
	}
}

func TestSandboxLibsNotShared(t *testing.T) {
	r, _ := newRT(t, Options{})
	if err := r.LoadString("a", `string.shout = function(s) return s:upper() end; table.x = 1; math.pi = 3`); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadString("b", `assert(string.shout == nil and table.x == nil and math.pi > 3.14)`); err != nil {
		t.Fatalf("lib mutation leaked: %v", err)
	}
}

func TestStringRepCapped(t *testing.T) {
	r, _ := newRT(t, Options{MaxStringRep: 1000})
	if err := r.LoadString("t", `local s = string.rep("x", 1000); assert(#s == 1000)`); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadString("t", `return string.rep("abc", 1e9)`); err == nil {
		t.Fatal("expected cap error")
	}
	if err := r.LoadString("t", `return ("x"):rep(2000)`); err == nil {
		t.Fatal("expected cap error on method form")
	}
}

func TestOsRestrictedAndClockInjected(t *testing.T) {
	r, h := newRT(t, Options{})
	mustLoad(t, r, `
	  assert(os.time() == `+"1767323045"+`)
	  assert(os.date("!%Y-%m-%d %H:%M:%S") == "2026-01-02 03:04:05")
	  assert(os.clock() == 0)
	  assert(os.difftime(5, 3) == 2)
	  assert(os.time{year=2000, month=1, day=1, hour=12} > 0)
	  assert(os.tmpname == nil and os.setenv == nil and os.setlocale == nil)
	`)
	h.advance(1500 * time.Millisecond)
	if err := r.Exec("", `assert(os.clock() == 1.5)`); err != nil {
		t.Fatal(err)
	}
}

func TestRunawayLoopAborts(t *testing.T) {
	for name, src := range map[string]string{
		"while":      `while true do end`,
		"pcall wrap": `while true do pcall(function() while true do end end) end`,
		"recursion":  `local function f() return f() + 1 end f()`,
		"coroutine":  `local co = coroutine.wrap(function() while true do end end) co()`,
		"table grow": `local t = {} for i = 1, 1e12 do t[i] = i end`,
		"sort cmp":   `local t = {} for i=1,2000 do t[i]=-i end table.sort(t, function(a,b) while true do end end)`,
	} {
		t.Run(name, func(t *testing.T) {
			r, h := newRT(t, Options{MaxInstructions: 200000, Timeout: 300 * time.Millisecond})
			start := time.Now()
			err := r.LoadString("t", src)
			if err == nil {
				t.Fatal("expected abort error")
			}
			if time.Since(start) > 3*time.Second {
				t.Fatalf("abort too slow: %v", time.Since(start))
			}
			if len(h.errorsLogged()) == 0 {
				t.Fatal("abort not reported via Host.Log")
			}
			// Runtime still usable afterwards.
			if err := r.LoadString("ok", `squat.log("alive")`); err != nil {
				t.Fatalf("runtime unusable after abort: %v", err)
			}
			if !strings.Contains(h.logText(), "alive") {
				t.Fatal("follow-up script did not run")
			}
		})
	}
}

func TestBudgetErrorKinds(t *testing.T) {
	r, _ := newRT(t, Options{MaxInstructions: 10000, Timeout: -1})
	err := r.LoadString("t", `while true do end`)
	if err == nil || !strings.Contains(err.Error(), ErrBudget.Error()) {
		t.Fatalf("want budget error, got %v", err)
	}
	r2, _ := newRT(t, Options{MaxInstructions: -1, Timeout: 50 * time.Millisecond})
	err = r2.LoadString("t", `while true do end`)
	if err == nil || !strings.Contains(err.Error(), ErrTimeout.Error()) {
		t.Fatalf("want timeout error, got %v", err)
	}
}

func TestRunawayHandlerDoesNotBlockOthers(t *testing.T) {
	r, h := newRT(t, Options{MaxInstructions: 100000})
	mustLoad(t, r, `
	  squat.on("line", function(l) while true do end end, 10)
	  squat.on("line", function(l) return {gag = true} end, 0)
	`)
	res := r.OnLine("w", line("hello"), true)
	if !res.Gag {
		t.Fatal("later handler must still run after a runaway one")
	}
	if len(h.errorsLogged()) != 1 {
		t.Fatalf("want 1 error, got %v", h.errorsLogged())
	}
}

func TestPanicRecovery(t *testing.T) {
	r, h := newRT(t, Options{})
	h.sendHook = func() { panic("host exploded") }
	mustLoad(t, r, `squat.on("line", function(l) squat.send("x") end)`)
	r.OnLine("w", line("a"), true)
	if !strings.Contains(strings.Join(h.errorsLogged(), "|"), "host exploded") {
		t.Fatalf("panic not reported: %v", h.errorsLogged())
	}
	h.sendHook = nil
	r.OnLine("w", line("b"), true)
	if len(h.sends) != 1 {
		t.Fatalf("runtime not usable after panic: %v", h.sends)
	}
}

func TestHugeAllocationAborts(t *testing.T) {
	r, _ := newRT(t, Options{MaxInstructions: -1, Timeout: 2 * time.Second, MaxHeapGrowth: 32 << 20, MaxStringRep: 1 << 20})
	err := r.LoadString("t", `local t = {} while true do t[#t+1] = {1,2,3,4,5,6,7,8} end`)
	if err == nil {
		t.Fatal("expected abort")
	}
}
