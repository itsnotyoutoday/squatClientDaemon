package tf

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

// Host is everything a script can touch. The embedding engine implements it;
// pkg/tf itself performs no I/O. All methods are called with the interpreter
// locked: they must not call back into the Interp synchronously.
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
	// Now is the injectable clock; all script time derives from it.
	Now() time.Time
	// Schedule runs fn after d. The returned cancel stops it. fn may be called
	// from any goroutine; the Interp serialises it.
	Schedule(d time.Duration, fn func()) (cancel func())

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

	// Files. The interpreter has already applied /restrict before calling.
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, appendMode bool) error
	ListDir(path string) ([]string, error)

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

// InputBuffer is an optional extension a Host may implement to expose the
// GUI input line to scripts (the kb* functions, /input, and the editing keys
// of /dokey). Positions are character (rune) indexes. A Host that does not
// implement it still works: the interpreter then keeps a private shadow
// buffer so scripts that only talk to themselves behave consistently.
type InputBuffer interface {
	// InputBuffer returns the current input line and the cursor position.
	InputBuffer() (text string, cursor int)
	// SetInputBuffer replaces the input line and moves the cursor.
	SetInputBuffer(text string, cursor int)
}

// UIControl is an optional Host extension for terminal-style screen control
// that has a GUI meaning: the split input/status layout (/set visual),
// repaint (/dokey redraw) and minimising the window (/suspend).
type UIControl interface {
	SetVisual(on bool)
	Redraw()
	Suspend()
}

// NopHost implements Host with no-ops (Now returns the wall clock). Embed it
// in a test or adapter and override what matters.
type NopHost struct{}

func (NopHost) Echo(string, []proto.Span)             {}
func (NopHost) SendToWorld(string, string) error      { return nil }
func (NopHost) Notify(string, string)                 {}
func (NopHost) Now() time.Time                        { return time.Now() }
func (NopHost) Schedule(time.Duration, func()) func() { return func() {} }
func (NopHost) Connect(string) error                  { return errors.New("not supported") }
func (NopHost) Disconnect(string) error               { return errors.New("not supported") }
func (NopHost) Foreground(string) error               { return errors.New("not supported") }
func (NopHost) WorldInfo(string) (WorldInfo, bool)    { return WorldInfo{}, false }
func (NopHost) ListWorlds() []WorldInfo               { return nil }
func (NopHost) AddWorld(WorldInfo) error              { return errors.New("not supported") }
func (NopHost) RemoveWorld(string) error              { return errors.New("not supported") }
func (NopHost) SaveWorlds() error                     { return errors.New("not supported") }
func (NopHost) IsConnected(string) bool               { return false }
func (NopHost) ActiveCount() int                      { return 0 }
func (NopHost) ForegroundWorld() string               { return "" }
func (NopHost) ReadFile(string) ([]byte, error)       { return nil, errors.New("file access not supported") }
func (NopHost) WriteFile(string, []byte, bool) error  { return errors.New("file access not supported") }
func (NopHost) ListDir(string) ([]string, error)      { return nil, errors.New("file access not supported") }
func (NopHost) Log(string, bool, string) error        { return errors.New("not supported") }
func (NopHost) Bell()                                 {}
func (NopHost) SetPrompt(string, string)              {}
func (NopHost) Input(string, string)                  {}
func (NopHost) DoKey(string, string)                  {}
func (NopHost) LocalEcho(string, bool)                {}
func (NopHost) Limit(string, string, string)          {}
func (NopHost) Recall(string, string)                 {}
func (NopHost) Status(string, string, ...string)      {}
func (NopHost) Quit()                                 {}
func (NopHost) Screen() (int, int)                    { return 80, 24 }

// Restrict is the /restrict level. Higher levels forbid more; it can only be
// raised from scripts. Shell access does not exist in pkg/tf at all.
type Restrict int

const (
	// RestrictShell (the zero value): shell forms are unavailable; files and
	// world operations work.
	RestrictShell Restrict = iota
	// RestrictFile additionally forbids file access (/load /save /quote 'file).
	RestrictFile
	// RestrictWorld additionally forbids world/connection operations.
	RestrictWorld
)

// Options configures an Interp. The zero value is usable.
type Options struct {
	// MaxSteps bounds statements+loop iterations+function calls per top-level
	// run (default 50000).
	MaxSteps int
	// MaxTime is the wall budget per top-level run measured on the Host clock
	// (default 2s; negative disables).
	MaxTime time.Duration
	// MaxDepth bounds nested macro/eval calls (default 64).
	MaxDepth int
	// Restrict is the initial /restrict level.
	Restrict Restrict
	// Rand supplies randomness (n>0: [0,n)); default is seeded from the host clock.
	Rand func(n int64) int64
	// Version is reported by /version and world "version" strings.
	Version string
}

// LineResult is the outcome of running the trigger stage over one line.
type LineResult struct {
	// Gag means the line must not be displayed.
	Gag bool
	// Spans is the line's content with hilites/substitutions applied.
	Spans []proto.Span
	// Fired lists the names of macros that matched (trigger order).
	Fired []string
	// Live echoes the live flag the result was computed with. When false only
	// display effects (Gag, Spans) were computed; nothing was executed.
	Live bool
}

// BudgetError is returned when a script run exceeds its step or time budget.
type BudgetError struct{ Reason string }

func (e *BudgetError) Error() string { return "script budget exceeded: " + e.Reason }
