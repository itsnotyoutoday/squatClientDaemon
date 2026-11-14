package tfengine

import (
	"errors"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// WorldInfo describes a world of the world book (TF "world" definition). The
// client's world store is the source of truth: the engine's own world
// database is kept in step with it (worlds.go).
type WorldInfo struct {
	Name      string
	Host      string
	Port      string
	Character string
	Password  string // never filled from the client's store; only a script's /addworld sets it
	Type      string // e.g. "tiny", "lp"; informational
	TLS       bool
	Temp      bool // a "(unnamedN)" world made by /connect host port
}

// ConnectOptions are the options of /connect.
type ConnectOptions struct {
	NoLogin, Quiet, TLS, Foreground, Background bool
}

// SockState is the state of a connection.
type SockState int

// Connection states.
const (
	SockConnecting SockState = iota
	SockConnected
	SockDead
)

// SocketInfo describes one open connection (a session).
type SocketInfo struct {
	World      string // the session's script name
	Host, Port string
	State      SockState
	TLS        bool
	Unseen     int // unread lines
	LastRecv   time.Time
	LastSend   time.Time
}

// StatusField is one field of the status bar.
type StatusField struct {
	Name  string
	Text  string
	Attrs string
}

// Host is what the client engine provides to scripts. All methods are called
// on the interpreter's goroutine: they must not call back into the Engine
// synchronously (use Engine.Post to defer work).
//
// Embed NopHost to get no-op defaults and override only what you need.
type Host interface {
	// Echo displays spans locally in the given world's window ("" = the
	// foreground/system window).
	Echo(world string, spans []proto.Span)
	// DisplayLine shows a line as if the world had sent it (fake_recv()).
	DisplayLine(world, text string)
	// SendToWorld sends one line of text to the world's connection.
	SendToWorld(world, text string) error
	// Notify raises a desktop/system notification.
	Notify(title, body string)
	// Now is the clock; all script time derives from it.
	Now() time.Time

	// Connections. The world is already defined in the world database; w is
	// its definition (a temporary one for /connect host port).
	Connect(w WorldInfo, o ConnectOptions) error
	Disconnect(world string) error
	Foreground(world string, quiet bool) error // "" = no world in front
	ForegroundWorld() string
	Sockets() []SocketInfo
	LastKeyboard() time.Time

	// The world book. Worlds is the source of truth; the others persist a
	// change a script made.
	Worlds() []WorldInfo
	PutWorld(w WorldInfo) error
	DeleteWorld(name string) error
	RenameWorld(old, name string) error
	SaveWorlds() error

	// Display and window.
	Bell()
	Screen() (cols, lines int)
	Input(text string)               // /input: insert text into the input line (never sent)
	LocalEcho(world string, on bool) // /localecho
	SetPrompt(world, text string)
	SetStatus(fields []StatusField) // the status bar as the engine renders it
	// SetLimit installs /limit's filter for a world (nil = none) and returns
	// how many lines of its scrollback are visible under it.
	SetLimit(world string, hidden func(line, attrs string) bool) (visible int)
	// Var reports a display-related TF variable that changed (wrap, wrapsize,
	// wrapspace, wrappunct, visual, more, ...).
	Var(name, value string)
	Dialog(name string) // open a GUI window: "worlds"
	// KeyBindings reports the key sequences scripts have bound whenever they change,
	// so the input line knows which keystrokes to forward to Engine.Key.
	KeyBindings(seqs []string)
	Suspend() // /suspend
	Quit()    // /quit
}

// HelpProvider is optionally implemented by a Host that has a help browser;
// without it /help lists the commands.
type HelpProvider interface{ Help(world, topic string) }

// WorldRenamer is kept for hosts written against the previous interface; the
// engine's /renameworld is Host.RenameWorld now.

// NopHost implements Host with no-ops (Now returns the wall clock). Embed it
// in a test or adapter and override what matters.
type NopHost struct{}

func (NopHost) Echo(string, []proto.Span)        {}
func (NopHost) DisplayLine(string, string)       {}
func (NopHost) SendToWorld(string, string) error { return nil }
func (NopHost) Notify(string, string)            {}
func (NopHost) Now() time.Time                   { return time.Now() }
func (NopHost) Connect(WorldInfo, ConnectOptions) error {
	return errors.New("not supported")
}
func (NopHost) Disconnect(string) error          { return errors.New("not supported") }
func (NopHost) Foreground(string, bool) error    { return errors.New("not supported") }
func (NopHost) ForegroundWorld() string          { return "" }
func (NopHost) Sockets() []SocketInfo            { return nil }
func (NopHost) LastKeyboard() time.Time          { return time.Time{} }
func (NopHost) Worlds() []WorldInfo              { return nil }
func (NopHost) PutWorld(WorldInfo) error         { return nil }
func (NopHost) DeleteWorld(string) error         { return nil }
func (NopHost) RenameWorld(string, string) error { return nil }
func (NopHost) SaveWorlds() error                { return nil }
func (NopHost) Bell()                            {}
func (NopHost) Screen() (int, int)               { return 80, 24 }
func (NopHost) Input(string)                     {}
func (NopHost) LocalEcho(string, bool)           {}
func (NopHost) SetPrompt(string, string)         {}
func (NopHost) SetStatus([]StatusField)          {}
func (NopHost) SetLimit(string, func(string, string) bool) int {
	return 1
}
func (NopHost) Var(string, string)   {}
func (NopHost) Dialog(string)        {}
func (NopHost) KeyBindings([]string) {}
func (NopHost) Suspend()             {}
func (NopHost) Quit()                {}
