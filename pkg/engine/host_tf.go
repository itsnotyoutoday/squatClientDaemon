package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/ansi"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tf"
)

// tfHost adapts the engine to tf.Host. TF "world" arguments are the
// session's script name (the world name, "#2" suffixed for duplicates) or a
// session id; "" is the foreground session. Callbacks are made with the
// interpreter locked and never call back into it.
type tfHost struct{ e *Engine }

func (h *tfHost) Echo(world string, spans []proto.Span) {
	if s := h.e.resolve(world); s != nil {
		s.addLine(proto.KindSystem, spans)
		return
	}
	h.e.bus.publish(Event{Kind: EvNotice, Level: LevelInfo, Text: ansi.PlainText(spans)})
}

func (h *tfHost) SendToWorld(world, text string) error {
	s := h.e.resolve(world)
	if s == nil {
		return fmt.Errorf("no such world %q", world)
	}
	return friendlySendErr(s.sendText(text, s.userSend.Load() > 0))
}

func (h *tfHost) Notify(title, body string) { h.e.notify(title, body) }
func (h *tfHost) Now() time.Time            { return h.e.now() }
func (h *tfHost) Schedule(d time.Duration, fn func()) func() {
	return h.e.schedule(d, fn)
}

func (e *Engine) notify(title, body string) {
	if e.opts.Notify != nil {
		e.opts.Notify(title, body)
		return
	}
	e.bus.publish(Event{Kind: EvNotice, Level: LevelInfo, Text: title + ": " + body})
}

func (h *tfHost) Connect(world string) error {
	w, ok := h.e.findWorld(world)
	if !ok {
		f := strings.Fields(world)
		if len(f) == 2 {
			if p, err := strconv.Atoi(f[1]); err == nil {
				_, err := h.e.Dial(World{Name: world, Host: f[0], Port: p}, "")
				return err
			}
		}
		return fmt.Errorf("%w: %s", ErrNoWorld, world)
	}
	pw := ""
	if w.Login != nil && h.e.opts.PasswordFor != nil {
		pw = h.e.opts.PasswordFor(w.ID)
	}
	_, err := h.e.Dial(w, pw)
	return err
}

func (h *tfHost) Disconnect(world string) error {
	s := h.e.resolve(world)
	if s == nil {
		return ErrNoSession
	}
	s.tr.Close()
	return nil
}

func (h *tfHost) Foreground(world string) error {
	s := h.e.resolve(world)
	if s == nil {
		return ErrNoSession
	}
	return h.e.SetForeground(s.id)
}

func toTFWorld(w World, connected bool) tf.WorldInfo {
	wi := tf.WorldInfo{Name: w.Name, Host: w.Host, Port: strconv.Itoa(w.Port), TLS: w.TLS, Connected: connected}
	if w.Login != nil {
		wi.Character = w.Login.CharName
		// the password is deliberately never exposed to scripts
	}
	return wi
}

func (h *tfHost) WorldInfo(world string) (tf.WorldInfo, bool) {
	if w, ok := h.e.findWorld(world); ok {
		return toTFWorld(w, h.IsConnected(w.Name)), true
	}
	if s := h.e.resolve(world); s != nil {
		return toTFWorld(s.world, !s.isEnded()), true
	}
	return tf.WorldInfo{}, false
}

func (h *tfHost) ListWorlds() []tf.WorldInfo {
	var out []tf.WorldInfo
	for _, w := range h.e.Worlds() {
		out = append(out, toTFWorld(w, h.IsConnected(w.Name)))
	}
	return out
}

func (h *tfHost) AddWorld(wi tf.WorldInfo) error {
	port, err := strconv.Atoi(wi.Port)
	if err != nil || port <= 0 || port > 65535 {
		return fmt.Errorf("bad port %q", wi.Port)
	}
	w := World{ID: "tf:" + wi.Name, Name: wi.Name, Host: wi.Host, Port: port, TLS: wi.TLS}
	if wi.Character != "" {
		w.Login = &Login{Template: "connect {name} {password}", CharName: wi.Character}
	}
	if h.e.opts.OnWorldAdd != nil {
		if err := h.e.opts.OnWorldAdd(w); err != nil {
			return err
		}
	}
	h.e.mu.Lock()
	for i, old := range h.e.worlds {
		if strings.EqualFold(old.Name, w.Name) {
			w.ID = old.ID
			h.e.worlds[i] = w
			h.e.mu.Unlock()
			return nil
		}
	}
	h.e.worlds = append(h.e.worlds, w)
	h.e.mu.Unlock()
	return nil
}

func (h *tfHost) RemoveWorld(name string) error {
	if h.e.opts.OnWorldRemove != nil {
		if err := h.e.opts.OnWorldRemove(name); err != nil {
			return err
		}
	}
	h.e.mu.Lock()
	defer h.e.mu.Unlock()
	for i, w := range h.e.worlds {
		if strings.EqualFold(w.Name, name) {
			h.e.worlds = append(h.e.worlds[:i], h.e.worlds[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrNoWorld, name)
}

func (h *tfHost) SaveWorlds() error {
	if h.e.opts.OnSaveWorlds != nil {
		return h.e.opts.OnSaveWorlds()
	}
	return nil
}

func (h *tfHost) IsConnected(world string) bool {
	h.e.mu.RLock()
	ss := make([]*session, 0, len(h.e.order))
	for _, id := range h.e.order {
		ss = append(ss, h.e.sessions[id])
	}
	h.e.mu.RUnlock()
	for _, s := range ss {
		if (s.tfName == world || s.id == world || strings.EqualFold(s.world.Name, world)) && !s.isEnded() {
			return true
		}
	}
	return false
}

func (h *tfHost) ActiveCount() int {
	n := 0
	for _, si := range h.e.Sessions() {
		if si.Unread > 0 {
			n++
		}
	}
	return n
}

func (h *tfHost) ForegroundWorld() string {
	if s := h.e.resolve(""); s != nil {
		return s.tfName
	}
	return ""
}

// ---- files (the interpreter has applied /restrict already) ----

func (h *tfHost) ReadFile(path string) ([]byte, error) { return os.ReadFile(h.e.resolvePath(path)) }

func (h *tfHost) WriteFile(path string, data []byte, appendMode bool) error {
	p := h.e.resolvePath(path)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendMode {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(p, flag, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (h *tfHost) ListDir(path string) ([]string, error) {
	ents, err := os.ReadDir(h.e.resolvePath(path))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out, nil
}

// ---- display / UI operations ----

// Log implements /log: relative paths go under Options.LogsDir.
func (h *tfHost) Log(world string, on bool, path string) error {
	s := h.e.resolve(world)
	if s == nil {
		return ErrNoSession
	}
	if on && !filepath.IsAbs(path) && !strings.HasPrefix(path, "~/") {
		path = filepath.Join(h.e.opts.LogsDir, filepath.Base(path))
	} else if on {
		path = h.e.resolvePath(path)
	}
	return s.setLog(on, path)
}

func (h *tfHost) Bell() {
	if s := h.e.resolve(""); s != nil {
		h.e.bus.publish(Event{Kind: EvBell, Session: s.id})
	}
}

func (h *tfHost) SetPrompt(world, text string) {
	if s := h.e.resolve(world); s != nil {
		s.setStatusText("prompt", text)
	}
}

// Input (/input) has no UI path yet: ignored. DoKey is likewise unsupported.
func (h *tfHost) Input(string, string) {}
func (h *tfHost) DoKey(string, string) {}

func (h *tfHost) LocalEcho(world string, on bool) {
	if s := h.e.resolve(world); s != nil {
		s.mu.Lock()
		s.localEcho = on
		s.mu.Unlock()
	}
}

func (h *tfHost) Limit(world, op, pattern string) {
	s := h.e.resolve(world)
	if s == nil {
		return
	}
	switch op {
	case "unlimit":
		s.setLimit(nil)
	case "relimit":
		s.mu.Lock()
		re := s.limit
		s.mu.Unlock()
		s.setLimit(re)
	default:
		re, err := globRegexp(pattern)
		if err != nil || pattern == "" {
			s.systemLine("% /limit: bad pattern")
			return
		}
		s.setLimit(re)
	}
}

func (h *tfHost) Recall(world, args string) {
	if s := h.e.resolve(world); s != nil {
		s.recall(args)
	}
}

func (h *tfHost) Status(op, field string, a ...string) {
	if s := h.e.resolve(""); s != nil {
		s.statusOp(op, field, a)
	}
}

func (h *tfHost) Screen() (int, int) {
	if s := h.e.resolve(""); s != nil {
		return s.size()
	}
	return 80, 24
}

func (h *tfHost) Quit() { h.e.bus.publish(Event{Kind: EvQuit}) }
