package lua

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	glua "github.com/yuin/gopher-lua"
)

// Manifest is the optional plugin.json of a directory plugin.
type Manifest struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Enabled     *bool  `json:"enabled"` // nil = enabled
	Main        string `json:"main"`    // entry file, default main.lua then init.lua
}

// PluginInfo reports one discovered plugin.
type PluginInfo struct {
	Name        string
	Version     string
	Description string
	Enabled     bool
	Main        string // script name read through Host.ReadScript
	Loaded      bool
	Err         string // load error, if any
}

// ParseManifest decodes a plugin.json.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("plugin.json: %w", err)
	}
	return m, nil
}

// LoadPlugins discovers plugins through Host.ListScripts/ReadScript and runs
// each in its own environment. Layouts:
//
//	heal.lua                     single-file plugin named "heal"
//	combat/plugin.json           directory plugin (manifest optional)
//	combat/main.lua              entry ("main" in the manifest overrides)
//	combat/util.lua              require("util") from inside the plugin
//
// A disabled plugin (manifest "enabled": false) is listed but not run. Errors
// are isolated per plugin: a failing plugin is reported via Host.Log and in
// the returned PluginInfo, and the others still load. Reload repeats this.
func (r *Runtime) LoadPlugins() ([]PluginInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	r.addUnit(unit{kind: "plugins"})
	return r.discoverAndLoadPlugins()
}

// Plugins returns the status of the plugins from the last load.
func (r *Runtime) Plugins() []PluginInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]PluginInfo(nil), r.plugins...)
}

type pluginSpec struct {
	info PluginInfo
	dir  string
}

func (r *Runtime) discoverAndLoadPlugins() ([]PluginInfo, error) {
	files, err := r.host.ListScripts()
	if err != nil {
		err = fmt.Errorf("lua: list scripts: %w", err)
		r.logf(LogError, "", "%v", err)
		return nil, err
	}
	sort.Strings(files)
	have := map[string]bool{}
	for _, f := range files {
		have[f] = true
	}
	var specs []pluginSpec
	seen := map[string]bool{}
	add := func(s pluginSpec) {
		if seen[s.info.Name] {
			r.logf(LogWarn, s.info.Name, "duplicate plugin name %q ignored", s.info.Name)
			return
		}
		seen[s.info.Name] = true
		specs = append(specs, s)
	}
	dirs := map[string]bool{}
	for _, f := range files {
		d, rest, nested := strings.Cut(f, "/")
		if !nested {
			if strings.HasSuffix(f, ".lua") {
				add(pluginSpec{info: PluginInfo{Name: strings.TrimSuffix(f, ".lua"), Enabled: true, Main: f}})
			}
			continue
		}
		if dirs[d] {
			continue
		}
		if rest == "plugin.json" || rest == "main.lua" || rest == "init.lua" {
			dirs[d] = true
			info := PluginInfo{Name: d, Enabled: true}
			if have[d+"/plugin.json"] {
				data, err := r.host.ReadScript(d + "/plugin.json")
				if err == nil {
					var m Manifest
					if m, err = ParseManifest(data); err == nil {
						if m.Name != "" {
							info.Name = m.Name
						}
						info.Version, info.Description = m.Version, m.Description
						info.Enabled = m.Enabled == nil || *m.Enabled
						if m.Main != "" {
							info.Main = d + "/" + path.Clean(m.Main)
						}
					}
				}
				if err != nil {
					info.Err = err.Error()
					r.logf(LogError, d, "%v", err)
				}
			}
			if info.Main == "" {
				switch {
				case have[d+"/main.lua"]:
					info.Main = d + "/main.lua"
				case have[d+"/init.lua"]:
					info.Main = d + "/init.lua"
				}
			}
			add(pluginSpec{info: info, dir: d})
		}
	}
	sort.SliceStable(specs, func(i, j int) bool { return specs[i].info.Name < specs[j].info.Name })

	var errs []error
	r.plugins = r.plugins[:0]
	for _, sp := range specs {
		info := sp.info
		switch {
		case info.Err != "":
		case !info.Enabled:
		case info.Main == "" || strings.Contains(info.Main, ".."):
			info.Err = "no entry script (main.lua)"
			r.logf(LogError, info.Name, "%s", info.Err)
		default:
			data, err := r.host.ReadScript(info.Main)
			if err == nil {
				r.removeOwner(info.Name)
				so := r.newEnv(info.Name, sp.dir)
				err = r.runIn(so, "@"+info.Main, string(data))
			} else {
				r.logf(LogError, info.Name, "read %s: %v", info.Main, err)
			}
			if err != nil {
				info.Err = err.Error()
				errs = append(errs, err)
			} else {
				info.Loaded = true
			}
		}
		r.plugins = append(r.plugins, info)
	}
	return append([]PluginInfo(nil), r.plugins...), errors.Join(errs...)
}

var moduleName = regexp.MustCompile(`^[A-Za-z0-9_]+(\.[A-Za-z0-9_-]+)*$`)

// requireFn builds the controlled `require` for a script environment. Module
// names are dotted identifiers (a.b -> a/b.lua, or a/b/init.lua) resolved
// relative to the plugin's own directory through Host.ReadScript; native
// modules, absolute paths and ".." are impossible by construction. Modules run
// in the requiring plugin's environment and are cached per plugin.
func (r *Runtime) requireFn(so *scriptEnv) glua.LGFunction {
	return func(L *glua.LState) int {
		name := L.CheckString(1)
		if !moduleName.MatchString(name) || strings.Contains(name, "..") {
			L.RaiseError("require: invalid module name %q", name)
		}
		if v, ok := so.modules[name]; ok {
			L.Push(v)
			return 1
		}
		if so.loading[name] {
			L.RaiseError("require: circular dependency on %q", name)
		}
		rel := strings.ReplaceAll(name, ".", "/")
		var data []byte
		var err error
		var tried []string
		for _, cand := range []string{rel + ".lua", rel + "/init.lua"} {
			full := cand
			if so.dir != "" {
				full = so.dir + "/" + cand
			}
			tried = append(tried, full)
			if data, err = r.host.ReadScript(full); err == nil {
				break
			}
		}
		if err != nil {
			L.RaiseError("module %q not found (tried %s)", name, strings.Join(tried, ", "))
		}
		if strings.HasPrefix(string(data), "\x1b") {
			L.RaiseError("require: binary chunk refused")
		}
		fn, lerr := L.Load(strings.NewReader(string(data)), "@"+name)
		if lerr != nil {
			L.RaiseError("require %q: %s", name, lerr.Error())
		}
		fn.Env = so.env
		so.loading[name] = true
		defer delete(so.loading, name)
		L.Push(fn)
		L.Push(glua.LString(name))
		L.Call(1, 1)
		ret := L.Get(-1)
		L.Pop(1)
		if ret == glua.LNil {
			ret = glua.LTrue
		}
		so.modules[name] = ret
		L.Push(ret)
		return 1
	}
}
