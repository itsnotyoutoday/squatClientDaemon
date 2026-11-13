package engine

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/lua"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// luaHost adapts the engine to lua.Host. Lua "world" arguments are session
// ids (names also resolve). Methods run with the Lua runtime locked.
type luaHost struct{ e *Engine }

func (h *luaHost) Send(world, text string) error {
	s := h.e.resolve(world)
	if s == nil {
		return fmt.Errorf("no such world %q", world)
	}
	return friendlySendErr(s.sendText(text, false))
}

func (h *luaHost) Echo(world string, spans []proto.Span) { (&tfHost{h.e}).Echo(world, spans) }
func (h *luaHost) Notify(title, body string)             { h.e.notify(title, body) }
func (h *luaHost) Now() time.Time                        { return h.e.now() }
func (h *luaHost) Schedule(d time.Duration, fn func()) func() {
	return h.e.schedule(d, fn)
}

// Log records script messages; warnings and errors are shown to the user in
// the foreground session (or as a notice).
func (h *luaHost) Log(level lua.LogLevel, plugin, text string) {
	h.e.log.Info("lua", "level", level.String(), "plugin", plugin, "msg", text)
	if level == lua.LogInfo {
		return
	}
	prefix := "[lua] "
	if plugin != "" {
		prefix = "[lua:" + plugin + "] "
	}
	lv := LevelWarn
	if level == lua.LogError {
		lv = LevelError
	}
	if s := h.e.resolve(""); s != nil {
		s.systemLine(prefix + text)
		return
	}
	h.e.bus.publish(Event{Kind: EvNotice, Level: lv, Text: prefix + text})
}

// RunTF cross-wires squat.tf: "/lua code" re-enters the Lua console through
// ExecInCallback (documented round trip); everything else runs in TF.
func (h *luaHost) RunTF(world, line string) error {
	t := strings.TrimSpace(line)
	if t == "/lua" || strings.HasPrefix(t, "/lua ") {
		return h.e.lua.ExecInCallback(strings.TrimSpace(strings.TrimPrefix(t, "/lua")))
	}
	name := world
	if s := h.e.resolve(world); s != nil {
		name = s.tfName
	}
	return h.e.tf.Exec(name, line)
}

func (h *luaHost) WorldName(world string) string {
	if s := h.e.resolve(world); s != nil {
		return s.tfName
	}
	return world
}

func (h *luaHost) ReadScript(name string) ([]byte, error) {
	clean := path.Clean("/" + name)[1:]
	if clean == "" || strings.Contains(clean, "..") {
		return nil, fs.ErrNotExist
	}
	return os.ReadFile(filepath.Join(h.e.opts.PluginsDir, filepath.FromSlash(clean)))
}

func (h *luaHost) ListScripts() ([]string, error) {
	root := h.e.opts.PluginsDir
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out, err
}
