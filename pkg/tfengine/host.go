package tfengine

import (
	"errors"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// WorldInfo describes a defined world (TF "world" definition).
type WorldInfo struct {
	Name      string
	Host      string
	Port      string
	Character string
	Password  string
	Type      string // e.g. "tiny", "lp"; informational
	TLS       bool
	Connected bool
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
	// SendToWorld sends one line of text to the world's connection.
	SendToWorld(world, text string) error
	// Notify raises a desktop/system notification.
	Notify(title, body string)
	// Now is the clock; all script time derives from it.
	Now() time.Time

	// World and connection operations.
	Connect(world string) error
	Disconnect(world string) error
	Foreground(world string) error
	WorldInfo(world string) (WorldInfo, bool)
	ListWorlds() []WorldInfo
	AddWorld(w WorldInfo) error
	RemoveWorld(name string) error
	SaveWorlds() error
	IsConnected(world string) bool
	ActiveCount() int // worlds with unseen activity
	ForegroundWorld() string

	// Display/UI operations that scripts may request.
	Log(world string, on bool, path string) error
	Bell()
	SetPrompt(world, text string)
	Input(world, text string)             // /input: insert text into the input line
	DoKey(world, name string)             // /dokey
	LocalEcho(world string, on bool)      // /localecho
	Limit(world, op, pattern string)      // /limit /unlimit /relimit: op = "limit"|"unlimit"|"relimit"
	Recall(world, args string)            // /recall (recorded by the host)
	Status(op, field string, a ...string) // /status_add /status_edit /status_rm
	Screen() (cols, lines int)
	Quit() // /exit /quit
}

// NopHost implements Host with no-ops (Now returns the wall clock). Embed it
// in a test or adapter and override what matters.
type NopHost struct{}

func (NopHost) Echo(string, []proto.Span)          {}
func (NopHost) SendToWorld(string, string) error   { return nil }
func (NopHost) Notify(string, string)              {}
func (NopHost) Now() time.Time                     { return time.Now() }
func (NopHost) Connect(string) error               { return errors.New("not supported") }
func (NopHost) Disconnect(string) error            { return errors.New("not supported") }
func (NopHost) Foreground(string) error            { return errors.New("not supported") }
func (NopHost) WorldInfo(string) (WorldInfo, bool) { return WorldInfo{}, false }
func (NopHost) ListWorlds() []WorldInfo            { return nil }
func (NopHost) AddWorld(WorldInfo) error           { return errors.New("not supported") }
func (NopHost) RemoveWorld(string) error           { return errors.New("not supported") }
func (NopHost) SaveWorlds() error                  { return errors.New("not supported") }
func (NopHost) IsConnected(string) bool            { return false }
func (NopHost) ActiveCount() int                   { return 0 }
func (NopHost) ForegroundWorld() string            { return "" }
func (NopHost) Log(string, bool, string) error     { return errors.New("not supported") }
func (NopHost) Bell()                              {}
func (NopHost) SetPrompt(string, string)           {}
func (NopHost) Input(string, string)               {}
func (NopHost) DoKey(string, string)               {}
func (NopHost) LocalEcho(string, bool)             {}
func (NopHost) Limit(string, string, string)       {}
func (NopHost) Recall(string, string)              {}
func (NopHost) Status(string, string, ...string)   {}
func (NopHost) Screen() (int, int)                 { return 80, 24 }
func (NopHost) Quit()                              {}
