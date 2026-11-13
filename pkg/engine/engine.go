package engine

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/conn"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/lua"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
	"github.com/itsnotyoutoday/squatClientDaemon/pkg/tf"
)

// Errors.
var (
	ErrNoSession = errors.New("engine: no such session")
	ErrClosed    = errors.New("engine: closed")
	ErrNoWorld   = errors.New("engine: no such world")
)

// Login is the automation run on connect. Template placeholders: {name}
// (CharName), {password}, {world}. The password is obtained at connect time
// (Dial's argument or Options.PasswordFor) and is never stored by the engine.
type Login struct {
	Template string
	CharName string
	Delay    time.Duration // wait after the connection is established
}

// World is a connection target.
type World struct {
	ID        string
	Name      string
	Host      string
	Port      int
	TLS       bool
	TLSVerify string // "strict" (default) | "insecure"
	Encoding  string // "utf-8" (default) | "latin1"
	Login     *Login
}

// SessionInfo is proto.SessionInfo plus the engine-side extras the UI needs.
type SessionInfo struct {
	proto.SessionInfo
	WorldID string `json:"worldId,omitempty"`
	Via     string `json:"via"` // "direct"
}

// Options configures an Engine. The zero value is usable.
type Options struct {
	// ConfigDir holds startup.tf, plugins/ and logs/ (default ~/.squatclient).
	ConfigDir   string
	StartupFile string // default ConfigDir/startup.tf
	PluginsDir  string // default ConfigDir/plugins
	LogsDir     string // default ConfigDir/logs (targets of /log)
	// SessionLogDir, when set, keeps each session's scrollback on disk too
	// (one sub-directory per session). Empty = memory only.
	SessionLogDir string
	RingSize      int // in-memory lines per session (default 20000)

	Caps     proto.Caps // zero value = proto.DefaultCaps()
	Restrict tf.Restrict
	TF       tf.Options
	Lua      lua.Options

	Clock       func() time.Time // default time.Now
	MaxQueue    int              // per-subscriber event cap (default 100000), see package doc
	DialTimeout time.Duration
	TLSConfig   *tls.Config // base TLS config for conn (tests: RootCAs)
	Version     string

	// Dialer opens a transport; default conn.Open. See Transport.
	Dialer func(ctx context.Context, cfg conn.Config) (Transport, error)

	// PasswordFor returns the password for a world id when a world with a
	// login template is dialled by name (/connect). Called at connect time.
	PasswordFor func(worldID string) string
	// Notify raises a desktop notification; default: a notice event.
	Notify func(title, body string)
	// World book persistence hooks (TF /addworld /unworld /saveworld).
	OnWorldAdd    func(World) error
	OnWorldRemove func(name string) error
	OnSaveWorlds  func() error

	Logger *slog.Logger
}

// Engine is the brain. Create with New, release with Close.
type Engine struct {
	opts Options
	log  *slog.Logger
	tf   *tf.Interp
	lua  *lua.Runtime
	bus  *bus

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.RWMutex
	sessions map[string]*session
	order    []string
	fg       string
	nextID   int
	worlds   []World
	timers   map[*time.Timer]struct{}
	caps     proto.Caps
	closed   bool
}

// New creates an Engine. It performs no I/O; call LoadScripts to load the
// startup file and plugins.
func New(o Options) *Engine {
	if o.ConfigDir == "" {
		if h, err := os.UserHomeDir(); err == nil {
			o.ConfigDir = filepath.Join(h, ".squatclient")
		}
	}
	if o.StartupFile == "" {
		o.StartupFile = filepath.Join(o.ConfigDir, "startup.tf")
	}
	if o.PluginsDir == "" {
		o.PluginsDir = filepath.Join(o.ConfigDir, "plugins")
	}
	if o.LogsDir == "" {
		o.LogsDir = filepath.Join(o.ConfigDir, "logs")
	}
	if o.RingSize <= 0 {
		o.RingSize = 20000
	}
	if o.Caps == (proto.Caps{}) {
		o.Caps = proto.DefaultCaps()
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.MaxQueue <= 0 {
		o.MaxQueue = 100000
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	e := &Engine{
		opts: o, log: o.Logger, bus: newBus(o.MaxQueue),
		sessions: map[string]*session{}, timers: map[*time.Timer]struct{}{}, caps: o.Caps,
	}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	topts := o.TF
	topts.Restrict = o.Restrict
	if topts.Version == "" {
		topts.Version = o.Version
	}
	e.tf = tf.New(&tfHost{e}, topts)
	e.lua = lua.New(&luaHost{e}, o.Lua)
	return e
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Subscribe returns the event channel and a cancel func. Call cancel when
// done (it is idempotent). The channel is closed after cancel, after Close,
// or when the subscriber fell Options.MaxQueue events behind.
func (e *Engine) Subscribe() (<-chan Event, func()) { return e.bus.subscribe() }

// Close ends every session, stops timers and closes the event channels.
func (e *Engine) Close() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	ss := make([]*session, 0, len(e.sessions))
	for _, s := range e.sessions {
		ss = append(ss, s)
	}
	for t := range e.timers {
		t.Stop()
	}
	e.timers = map[*time.Timer]struct{}{}
	e.mu.Unlock()
	e.cancel()
	for _, s := range ss {
		s.shutdown()
		s.closeStore()
	}
	e.lua.Close()
	e.bus.shutdown()
}

func (e *Engine) now() time.Time { return e.opts.Clock() }

// schedule runs fn after d unless the engine is closed first.
func (e *Engine) schedule(d time.Duration, fn func()) (cancel func()) {
	var t *time.Timer
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return func() {}
	}
	t = time.AfterFunc(d, func() {
		e.mu.Lock()
		delete(e.timers, t)
		closed := e.closed
		e.mu.Unlock()
		if !closed {
			fn()
		}
	})
	e.timers[t] = struct{}{}
	e.mu.Unlock()
	return func() {
		t.Stop()
		e.mu.Lock()
		delete(e.timers, t)
		e.mu.Unlock()
	}
}

// ---- worlds ------------------------------------------------------------

// SetWorlds replaces the world book used by /connect and friends.
func (e *Engine) SetWorlds(ws []World) {
	e.mu.Lock()
	e.worlds = append([]World(nil), ws...)
	e.mu.Unlock()
}

// Worlds returns a copy of the world book.
func (e *Engine) Worlds() []World {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]World(nil), e.worlds...)
}

func (e *Engine) findWorld(name string) (World, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, w := range e.worlds {
		if w.ID == name || strings.EqualFold(w.Name, name) {
			return w, true
		}
	}
	return World{}, false
}

// ---- sessions ----------------------------------------------------------

// Sessions lists all sessions in creation order.
func (e *Engine) Sessions() []SessionInfo {
	e.mu.RLock()
	ss := make([]*session, 0, len(e.order))
	for _, id := range e.order {
		ss = append(ss, e.sessions[id])
	}
	e.mu.RUnlock()
	out := make([]SessionInfo, len(ss))
	for i, s := range ss {
		out[i] = s.snapshot()
	}
	return out
}

// Session returns one session's info.
func (e *Engine) Session(id string) (SessionInfo, error) {
	s := e.get(id)
	if s == nil {
		return SessionInfo{}, ErrNoSession
	}
	return s.snapshot(), nil
}

func (e *Engine) get(id string) *session {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.sessions[id]
}

// resolve maps "" (foreground), a session id, or a script-visible world name
// to a session. For names, a live session is preferred.
func (e *Engine) resolve(w string) *session {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if w == "" {
		return e.sessions[e.fg]
	}
	if s := e.sessions[w]; s != nil {
		return s
	}
	var best *session
	for _, id := range e.order {
		s := e.sessions[id]
		if s.tfName == w || strings.EqualFold(s.world.Name, w) {
			if best == nil || (best.isEnded() && !s.isEnded()) {
				best = s
			}
		}
	}
	return best
}

// Foreground returns the foreground session id ("" if none).
func (e *Engine) Foreground() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.fg
}

// SetForeground selects the foreground session.
func (e *Engine) SetForeground(id string) error {
	e.mu.Lock()
	if e.sessions[id] == nil {
		e.mu.Unlock()
		return ErrNoSession
	}
	changed := e.fg != id
	e.fg = id
	e.mu.Unlock()
	if changed {
		e.bus.publish(Event{Kind: EvForeground, Session: id})
	}
	return nil
}

// Dial opens a session to w. password is used for the world's login template
// and is not retained after the login line has been sent. The first session
// becomes the foreground session. Dial returns at once; connection progress
// arrives as session events (state connecting -> connected | ended).
func (e *Engine) Dial(w World, password string) (SessionInfo, error) {
	if w.Name == "" {
		w.Name = fmt.Sprintf("%s:%d", w.Host, w.Port)
	}
	cfg := conn.Config{
		World: proto.World{Name: w.Name, Host: w.Host, Port: w.Port, TLS: w.TLS,
			TLSVerify: orDefault(w.TLSVerify, "strict"), Encoding: orDefault(w.Encoding, "utf-8")},
		Caps: e.getCaps(), Version: e.opts.Version, DialTimeout: e.opts.DialTimeout, TLSConfig: e.opts.TLSConfig,
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return SessionInfo{}, ErrClosed
	}
	e.nextID++
	id := fmt.Sprintf("s%d", e.nextID)
	tfName := e.uniqueName(w.Name)
	e.mu.Unlock()

	s, err := newSession(e, id, tfName, w, cfg, loginLine(w, password))
	if err != nil {
		return SessionInfo{}, err
	}
	e.mu.Lock()
	e.sessions[id] = s
	e.order = append(e.order, id)
	first := e.fg == ""
	if first {
		e.fg = id
	}
	e.mu.Unlock()
	if first {
		e.bus.publish(Event{Kind: EvForeground, Session: id})
	}
	if err := s.start(); err != nil {
		e.remove(s)
		return SessionInfo{}, err
	}
	return s.snapshot(), nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// uniqueName returns a script-visible name not used by any session. e.mu held.
func (e *Engine) uniqueName(base string) string {
	name := base
	for n := 2; ; n++ {
		taken := false
		for _, s := range e.sessions {
			if s.tfName == name {
				taken = true
				break
			}
		}
		if !taken {
			return name
		}
		name = fmt.Sprintf("%s#%d", base, n)
	}
}

func loginLine(w World, password string) string {
	if w.Login == nil || w.Login.Template == "" {
		return ""
	}
	return strings.NewReplacer("{name}", w.Login.CharName, "{password}", password, "{world}", w.Name).Replace(w.Login.Template)
}

// Disconnect closes the session's connection (it ends with client_quit and
// stays listed until Dismiss).
func (e *Engine) Disconnect(id string) error {
	s := e.get(id)
	if s == nil {
		return ErrNoSession
	}
	s.tr.Close()
	return nil
}

// Dismiss disconnects if needed and removes the session and its scrollback.
func (e *Engine) Dismiss(id string) error {
	s := e.get(id)
	if s == nil {
		return ErrNoSession
	}
	s.shutdown()
	e.remove(s)
	return nil
}

func (e *Engine) remove(s *session) {
	e.mu.Lock()
	delete(e.sessions, s.id)
	for i, id := range e.order {
		if id == s.id {
			e.order = append(e.order[:i], e.order[i+1:]...)
			break
		}
	}
	wasFg := e.fg == s.id
	if wasFg {
		e.fg = ""
		if n := len(e.order); n > 0 {
			e.fg = e.order[n-1]
		}
	}
	nfg := e.fg
	e.mu.Unlock()
	s.closeStore()
	e.bus.publish(Event{Kind: EvSessionRemoved, Session: s.id})
	if wasFg && nfg != "" {
		e.bus.publish(Event{Kind: EvForeground, Session: nfg})
	}
}

// Resize informs the game (NAWS) of the output size.
func (e *Engine) Resize(id string, cols, rows int) error {
	s := e.get(id)
	if s == nil {
		return ErrNoSession
	}
	s.setSize(cols, rows)
	return nil
}

// SetCaps changes the advertised capabilities for new sessions and, at the
// next TTYPE exchange, existing ones.
func (e *Engine) SetCaps(c proto.Caps) {
	e.mu.Lock()
	e.caps = c
	ss := make([]*session, 0, len(e.sessions))
	for _, s := range e.sessions {
		ss = append(ss, s)
	}
	e.mu.Unlock()
	for _, s := range ss {
		s.tr.SetCaps(c)
	}
}

func (e *Engine) getCaps() proto.Caps {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.caps
}

// notice shows a client-side message: as a stored system line when it belongs
// to a session, otherwise as a notice event.
func (e *Engine) notice(session, level, text string) {
	if s := e.get(session); s != nil {
		s.systemLine(text)
		return
	}
	e.bus.publish(Event{Kind: EvNotice, Level: level, Text: text})
}
