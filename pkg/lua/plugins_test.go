package lua

import (
	"strings"
	"testing"
)

func TestPluginDiscoveryAndIsolation(t *testing.T) {
	r, h := newRT(t, Options{})
	h.files = map[string]string{
		"a.lua":               `shared = "A"; squat.on("line", function(l) squat.send("a sees " .. tostring(shared)) end)`,
		"b.lua":               `assert(shared == nil, "global leaked from a"); shared = "B"`,
		"broken.lua":          `this is not lua`,
		"boom.lua":            `error("kaboom")`,
		"combat/plugin.json":  `{"name":"combat","version":"1.2","description":"d"}`,
		"combat/main.lua":     `local u = require("util"); squat.on("line", function() squat.send(u.name) end)`,
		"combat/util.lua":     `return {name = "util-combat"}`,
		"off/plugin.json":     `{"enabled": false}`,
		"off/main.lua":        `squat.send("should not load", "w")`,
		"noentry/plugin.json": `{}`,
	}
	infos, err := r.LoadPlugins()
	if err == nil {
		t.Fatal("expected aggregated error for broken plugins")
	}
	by := map[string]PluginInfo{}
	for _, i := range infos {
		by[i.Name] = i
	}
	if !by["a"].Loaded || !by["b"].Loaded || !by["combat"].Loaded {
		t.Fatalf("infos: %+v", infos)
	}
	if by["broken"].Loaded || by["broken"].Err == "" || by["boom"].Loaded || by["noentry"].Err == "" {
		t.Fatalf("broken plugins must be reported: %+v", infos)
	}
	if by["off"].Enabled || by["off"].Loaded {
		t.Fatalf("disabled plugin loaded: %+v", by["off"])
	}
	if by["combat"].Version != "1.2" || by["combat"].Description != "d" {
		t.Fatalf("manifest: %+v", by["combat"])
	}
	r.OnLine("w", line("x"), true)
	got := map[string]bool{}
	for _, s := range h.sends {
		got[s.text] = true
	}
	if !got["a sees A"] || !got["util-combat"] || len(h.sends) != 2 {
		t.Fatalf("sends = %v", h.sends)
	}
	if len(r.Plugins()) != len(infos) {
		t.Fatal("Plugins() mismatch")
	}
}

func TestRequireIsPluginLocal(t *testing.T) {
	r, h := newRT(t, Options{})
	h.files = map[string]string{
		"p1/main.lua":     `local m = require("lib.deep"); assert(m == "deep1"); assert(require("lib.deep") == m)`,
		"p1/lib/deep.lua": `return "deep1"`,
		"p2/main.lua":     `local ok = pcall(require, "lib.deep"); assert(not ok, "p2 must not see p1 modules")`,
		"p3/main.lua":     `require("cyc")`,
		"p3/cyc.lua":      `require("cyc")`,
		"secret.lua":      `squat.log("root loaded")`,
		"p4/main.lua":     `assert(not pcall(require, "secret"))`,
	}
	infos, _ := r.LoadPlugins()
	st := map[string]string{}
	for _, i := range infos {
		st[i.Name] = i.Err
	}
	if st["p1"] != "" || st["p2"] != "" || st["p4"] != "" {
		t.Fatalf("unexpected: %v", st)
	}
	if !strings.Contains(st["p3"], "circular") {
		t.Fatalf("cycle not detected: %v", st["p3"])
	}
}

func TestReloadClearsAndReruns(t *testing.T) {
	r, h := newRT(t, Options{})
	h.files = map[string]string{"a.lua": `squat.on("line", function() squat.send("v1") end)
	  squat.timer{every=1, fn=function() squat.send("tick", "w") end}`}
	if _, err := r.LoadPlugins(); err != nil {
		t.Fatal(err)
	}
	mustLoad(t, r, `squat.on("line", function() squat.send("inline") end); counter = (counter or 0) + 1; squat.var.set("c", counter)`)
	r.OnLine("w", line("x"), true)
	if len(h.sends) != 2 {
		t.Fatalf("sends=%v", h.sends)
	}
	// Edit the plugin on "disk" and reload.
	h.files["a.lua"] = `squat.on("line", function() squat.send("v2") end)`
	h.sends = nil
	if err := r.Reload(); err != nil {
		t.Fatal(err)
	}
	h.advance(5e9)
	if len(h.sends) != 0 {
		t.Fatalf("old timer survived reload: %v", h.sends)
	}
	r.OnLine("w", line("x"), true)
	var got []string
	for _, s := range h.sends {
		got = append(got, s.text)
	}
	if strings.Join(got, ",") != "v2,inline" {
		t.Fatalf("after reload: %v (old handler must be gone, new code in effect)", got)
	}
	if v, _ := r.GetVar("c"); v != 1.0 {
		t.Fatalf("global state must reset on reload, c=%v", v)
	}
}

func TestLoadStringReplacesSameName(t *testing.T) {
	r, h := newRT(t, Options{})
	if err := r.LoadString("s", `squat.on("line", function() squat.send("one") end)`); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadString("s", `squat.on("line", function() squat.send("two") end)`); err != nil {
		t.Fatal(err)
	}
	r.OnLine("w", line("x"), true)
	if len(h.sends) != 1 || h.sends[0].text != "two" {
		t.Fatalf("sends=%v", h.sends)
	}
}

func TestLoadFile(t *testing.T) {
	r, h := newRT(t, Options{})
	h.files["x.lua"] = `squat.log("from file")`
	if err := r.LoadFile("x.lua"); err != nil || !strings.Contains(h.logText(), "from file") {
		t.Fatalf("err=%v logs=%s", err, h.logText())
	}
	if err := r.LoadFile("missing.lua"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseManifest(t *testing.T) {
	m, err := ParseManifest([]byte(`{"name":"n","enabled":false,"main":"x.lua"}`))
	if err != nil || m.Name != "n" || m.Enabled == nil || *m.Enabled || m.Main != "x.lua" {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := ParseManifest([]byte(`{`)); err == nil {
		t.Fatal("expected error")
	}
}
