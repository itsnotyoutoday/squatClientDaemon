package engine

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/ansi"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tfengine"
)

// tfHost adapts the engine to tfengine.Host. TF "world" arguments are the
// session's script name (the world name, "#2" suffixed for duplicates) or a
// session id; "" is the foreground session. Callbacks are made on the TF
// interpreter's goroutine and never call back into it.
type tfHost struct{ e *Engine }

func (h *tfHost) Echo(world string, spans []proto.Span) {
	if s := h.e.resolve(world); s != nil {
		s.addLine(proto.KindSystem, spans)
		return
	}
	h.e.bus.publish(Event{Kind: EvNotice, Level: LevelInfo, Text: ansi.PlainText(spans)})
}

// DisplayLine shows fake_recv()'s line as a game line.
func (h *tfHost) DisplayLine(world, text string) {
	if s := h.e.resolve(world); s != nil {
		s.addLine(proto.KindGame, []proto.Span{{S: text}})
	}
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

func (e *Engine) notify(title, body string) {
	if e.opts.Notify != nil {
		e.opts.Notify(title, body)
		return
	}
	e.bus.publish(Event{Kind: EvNotice, Level: LevelInfo, Text: title + ": " + body})
}

// ---- connections ----

func (h *tfHost) Connect(w tfengine.WorldInfo, o tfengine.ConnectOptions) error {
	bw, ok := h.e.findWorld(w.Name)
	if !ok {
		port, err := strconv.Atoi(w.Port)
		if err != nil || w.Host == "" {
			return fmt.Errorf("%w: %s", ErrNoWorld, w.Name)
		}
		bw = World{Name: w.Name, Host: w.Host, Port: port, TLS: w.TLS || o.TLS}
	}
	if o.TLS {
		bw.TLS = true
	}
	pw := ""
	if bw.Login != nil && !o.NoLogin && h.e.opts.PasswordFor != nil {
		pw = h.e.opts.PasswordFor(bw.ID)
	}
	if o.NoLogin {
		bw.Login = nil
	}
	si, err := h.e.Dial(bw, pw)
	if err == nil && !o.Background && !o.Quiet {
		_ = h.e.SetForeground(si.ID)
	}
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

func (h *tfHost) Foreground(world string, quiet bool) error {
	if world == "" {
		return nil // "no world in front": a GUI always has the tab that is showing
	}
	s := h.e.resolve(world)
	if s == nil {
		return ErrNoSession
	}
	return h.e.SetForeground(s.id)
}

func (h *tfHost) ForegroundWorld() string {
	if s := h.e.resolve(""); s != nil {
		return s.tfName
	}
	return ""
}

func (h *tfHost) Sockets() []tfengine.SocketInfo {
	h.e.mu.RLock()
	ss := make([]*session, 0, len(h.e.order))
	for _, id := range h.e.order {
		ss = append(ss, h.e.sessions[id])
	}
	fg := h.e.fg
	h.e.mu.RUnlock()
	// the foreground socket first: it is what /fg with no argument leaves alone
	var out []tfengine.SocketInfo
	for _, s := range ss {
		s.mu.Lock()
		info := s.info
		unread := len(s.unread)
		s.mu.Unlock()
		st := tfengine.SockConnected
		switch info.State {
		case proto.StateConnecting:
			st = tfengine.SockConnecting
		case proto.StateEnded:
			st = tfengine.SockDead
		}
		si := tfengine.SocketInfo{World: s.tfName, Host: info.Host, Port: strconv.Itoa(info.Port), State: st,
			TLS: info.TLS, Unseen: unread}
		if s.id == fg {
			out = append([]tfengine.SocketInfo{si}, out...)
		} else {
			out = append(out, si)
		}
	}
	return out
}

func (h *tfHost) LastKeyboard() time.Time {
	if ns := h.e.lastKey.Load(); ns != 0 {
		return time.Unix(0, ns)
	}
	return time.Time{}
}

// ---- the world book (the client's store is the source of truth) ----

func toTFWorld(w World) tfengine.WorldInfo {
	wi := tfengine.WorldInfo{Name: w.Name, Host: w.Host, Port: strconv.Itoa(w.Port), TLS: w.TLS}
	if w.Login != nil {
		wi.Character = w.Login.CharName
		// the password is deliberately never exposed to scripts
	}
	return wi
}

func (h *tfHost) Worlds() []tfengine.WorldInfo {
	var out []tfengine.WorldInfo
	for _, w := range h.e.Worlds() {
		if w.Host == "" { // a world held by a TinyFugue squatter record has no address of its own
			continue
		}
		out = append(out, toTFWorld(w))
	}
	return out
}

func (h *tfHost) PutWorld(wi tfengine.WorldInfo) error {
	port, err := strconv.Atoi(wi.Port)
	if err != nil || port <= 0 || port > 65535 {
		return fmt.Errorf("bad port %q", wi.Port)
	}
	w := World{ID: "tf:" + wi.Name, Name: wi.Name, Host: wi.Host, Port: port, TLS: wi.TLS}
	if wi.Character != "" {
		w.Login = &Login{Template: "connect {name} {password}", CharName: wi.Character}
	}
	h.e.mu.Lock()
	for i, old := range h.e.worlds {
		if strings.EqualFold(old.Name, w.Name) {
			w.ID, w.Via, w.Encoding, w.TLSVerify, w.GameType = old.ID, old.Via, old.Encoding, old.TLSVerify, old.GameType
			if w.Login == nil {
				w.Login = old.Login
			}
			h.e.worlds[i] = w
			h.e.mu.Unlock()
			return h.worldAdded(w)
		}
	}
	h.e.worlds = append(h.e.worlds, w)
	h.e.mu.Unlock()
	return h.worldAdded(w)
}

func (h *tfHost) worldAdded(w World) error {
	if h.e.opts.OnWorldAdd != nil {
		return h.e.opts.OnWorldAdd(w)
	}
	return nil
}

func (h *tfHost) DeleteWorld(name string) error {
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

// RenameWorld renames a world of the book (open sessions keep their script names).
func (h *tfHost) RenameWorld(old, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("empty name")
	}
	h.e.mu.Lock()
	idx := -1
	for i, w := range h.e.worlds {
		if strings.EqualFold(w.Name, name) {
			h.e.mu.Unlock()
			return fmt.Errorf("a world named %q already exists", name)
		}
		if strings.EqualFold(w.Name, old) {
			idx = i
		}
	}
	if idx < 0 {
		h.e.mu.Unlock()
		return nil // a temporary world (/connect host port) that is not in the book: nothing to rename
	}
	w := h.e.worlds[idx]
	w.Name = name
	h.e.worlds[idx] = w
	h.e.mu.Unlock()
	return h.worldAdded(w)
}

func (h *tfHost) SaveWorlds() error {
	if h.e.opts.OnSaveWorlds != nil {
		return h.e.opts.OnSaveWorlds()
	}
	return nil
}

// ---- display and window ----

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

// Input (/input) asks the UI to insert text into the input line (EvWindow "input:<text>").
func (h *tfHost) Input(text string) {
	h.e.bus.publish(Event{Kind: EvWindow, Text: "input:" + text})
}

func (h *tfHost) LocalEcho(world string, on bool) {
	if s := h.e.resolve(world); s != nil {
		s.mu.Lock()
		s.localEcho = on
		s.mu.Unlock()
	}
}

// SetStatus puts the engine's rendered status fields on every session's status bar.
func (h *tfHost) SetStatus(fields []tfengine.StatusField) {
	h.e.mu.RLock()
	ss := make([]*session, 0, len(h.e.sessions))
	for _, s := range h.e.sessions {
		ss = append(ss, s)
	}
	h.e.mu.RUnlock()
	for _, s := range ss {
		s.setTFStatus(fields)
	}
}

func (h *tfHost) SetLimit(world string, hidden func(line, attrs string) bool) int {
	if s := h.e.resolve(world); s != nil {
		return s.setHidden(hidden)
	}
	return 0
}

func (h *tfHost) Screen() (int, int) {
	if s := h.e.resolve(""); s != nil {
		return s.size()
	}
	return 80, 24
}

// Var reports a display-related TF variable (wrap, wrapsize, ...) to the UI (EvWindow "var:<name>=<value>").
func (h *tfHost) Var(name, value string) {
	h.e.bus.publish(Event{Kind: EvWindow, Text: "var:" + name + "=" + value})
}

// Dialog asks the UI to open a window (EvWindow "dialog:<name>").
func (h *tfHost) Dialog(name string) { h.e.bus.publish(Event{Kind: EvWindow, Text: "dialog:" + name}) }

// Help implements /help for scripts: the UI owns the help browser, so the engine asks it to open
// (EvWindow "help:<topic>"). Typed /help is handled by the UI itself before it reaches the engine.
func (h *tfHost) Help(world, topic string) {
	h.e.bus.publish(Event{Kind: EvWindow, Text: "help:" + topic})
}

// Suspend is /suspend from a script: detach squatter sessions and minimise the window.
func (h *tfHost) Suspend() { go h.e.Suspend() }

func (h *tfHost) Quit() { h.e.bus.publish(Event{Kind: EvQuit}) }

// KeyBindings tells the UI which keys scripts have bound (EvWindow "keys:<seq>\n<seq>...").
func (h *tfHost) KeyBindings(seqs []string) {
	h.e.bus.publish(Event{Kind: EvWindow, Text: "keys:" + strings.Join(seqs, "\n")})
}
