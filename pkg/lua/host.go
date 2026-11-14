package lua

import (
	"errors"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/proto"
)

// LogLevel classifies a message passed to Host.Log.
type LogLevel int

// Log levels.
const (
	LogInfo LogLevel = iota
	LogWarn
	LogError
)

func (l LogLevel) String() string {
	switch l {
	case LogWarn:
		return "warn"
	case LogError:
		return "error"
	}
	return "info"
}

// Host is everything a script may touch. The engine adapts its real services
// to this interface; tests use a fake. All methods are called with the
// Runtime lock held (see the package documentation).
type Host interface {
	// Send sends one line of input to the named world's connection.
	Send(world, text string) error
	// Echo displays spans locally in the named world's output (not sent).
	Echo(world string, spans []proto.Span)
	// Notify raises a user notification.
	Notify(title, body string)
	// Log records a script message. plugin is the originating plugin or
	// script name ("" when unknown). Script errors arrive with LogError.
	Log(level LogLevel, plugin, text string)
	// Now is the injectable clock used for squat.now(), os.time and os.date.
	Now() time.Time
	// Schedule arranges for fn to be called once after d on any goroutine and
	// returns a cancel function. The Runtime serialises fn with all other
	// Lua execution. cancel must not block on a running fn.
	Schedule(d time.Duration, fn func()) (cancel func())
	// RunTF runs one line of TinyFugue input in the sibling TF engine.
	RunTF(world, line string) error
	// WorldName maps a world id (as passed to OnLine etc.) to a display name.
	WorldName(world string) string
	// ReadScript reads a script file by slash-separated name relative to the
	// scripts root (used by LoadFile, plugin loading and require).
	ReadScript(name string) ([]byte, error)
	// ListScripts lists all script-root files as slash-separated names
	// (e.g. "heal.lua", "combat/plugin.json", "combat/main.lua").
	ListScripts() ([]string, error)
}

// Options tunes a Runtime. The zero value is usable; zero fields take the
// defaults noted.
type Options struct {
	// MaxInstructions per Lua call (default 5,000,000). Counted per VM
	// instruction of the main thread. Set negative to disable.
	MaxInstructions int64
	// Timeout is the wall-clock limit per Lua call (default 500ms). Real time,
	// not Host.Now. Negative disables.
	Timeout time.Duration
	// MaxHeapGrowth is the process heap growth, in bytes, tolerated during one
	// call before it is aborted (default 128 MiB). A heuristic: other goroutines'
	// allocations count too. Negative disables.
	MaxHeapGrowth int64
	// MaxStringRep caps the result of string.rep in bytes (default 4 MiB).
	MaxStringRep int
	// CallStackSize bounds Lua call depth (default 200).
	CallStackSize int
	// RegistryMaxSize bounds the Lua value stack in slots (default 65536).
	RegistryMaxSize int
	// MinTimerInterval is the smallest repeat/delay a script may schedule
	// (default 50ms). MaxTimers bounds live timers (default 512).
	MinTimerInterval time.Duration
	MaxTimers        int
	// MaxVarBytes bounds one stored variable's JSON size (default 64 KiB);
	// MaxVars bounds the number of variables (default 10000).
	MaxVarBytes int
	MaxVars     int
	// OnVar, if set, is called after a variable is set or deleted (value nil), by a script or from Go.
	// It runs with the Runtime's lock held: it must not call back into the Runtime, and should hand
	// the change on (a channel, a queue) and return.
	OnVar func(name string, value any)
}

func (o Options) withDefaults() Options {
	if o.MaxInstructions == 0 {
		o.MaxInstructions = 5_000_000
	}
	if o.Timeout == 0 {
		o.Timeout = 500 * time.Millisecond
	}
	if o.MaxHeapGrowth == 0 {
		o.MaxHeapGrowth = 128 << 20
	}
	if o.MaxStringRep == 0 {
		o.MaxStringRep = 4 << 20
	}
	if o.CallStackSize == 0 {
		o.CallStackSize = 200
	}
	if o.RegistryMaxSize == 0 {
		o.RegistryMaxSize = 65536
	}
	if o.MinTimerInterval == 0 {
		o.MinTimerInterval = 50 * time.Millisecond
	}
	if o.MaxTimers == 0 {
		o.MaxTimers = 512
	}
	if o.MaxVarBytes == 0 {
		o.MaxVarBytes = 64 << 10
	}
	if o.MaxVars == 0 {
		o.MaxVars = 10000
	}
	return o
}

// LineResult is the display outcome of running a line through the scripts.
type LineResult struct {
	// Gag means the line must not be displayed.
	Gag bool
	// Spans is the (possibly re-styled) line content to display. It is always
	// set: when no script changed anything it equals the input spans.
	Spans []proto.Span
	// Modified reports whether any script changed Spans.
	Modified bool
}

// Errors returned (wrapped) by script execution.
var (
	ErrBudget  = errors.New("lua: instruction budget exceeded")
	ErrTimeout = errors.New("lua: wall-clock timeout exceeded")
	ErrMemory  = errors.New("lua: heap growth limit exceeded")
	ErrClosed  = errors.New("lua: runtime closed")
)

var errBinaryChunk = errors.New("attempt to load a binary chunk")
