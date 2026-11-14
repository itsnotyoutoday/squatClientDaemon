package tfengine

// Status says how this client answers one real tf builtin.
type Status string

const (
	// Works: implemented, with real tf's meaning.
	Works Status = "works"
	// Mapped: answered by a SquatClient feature (a window, a dialog, the
	// session list) instead of a terminal one.
	Mapped Status = "mapped"
	// Unsupported: not available in this client; the command says so when it
	// is used (never silently).
	Unsupported Status = "unsupported"
)

// CommandStatus is one row of docs/COMMAND-STATUS.md.
type CommandStatus struct {
	Name   string
	Status Status
	// Where says who implements it: "engine" (gtfparser), "client" (this
	// package, against the client's data) or "-".
	Where string
	Note  string
}

// CommandStatuses lists every real tf builtin command this client knows about.
func CommandStatuses() []CommandStatus { return commandStatuses }

func s(name string, st Status, where, note string) CommandStatus {
	return CommandStatus{name, st, where, note}
}

// The engine-implemented commands are marked "engine"; if the engine later
// implements one this package registers (the world book, history, ...), the
// client's registration still wins until it is removed from commands.go.
var commandStatuses = []CommandStatus{
	s("beep", Mapped, "client", "bell / audio cue of the window"),
	s("bind", Works, "engine", "defines and lists bindings; the input line does not run TF key bindings yet"),
	s("break", Works, "engine", ""),
	s("calllua", Unsupported, "-", "real tf's embedded Lua; SquatClient has its own Lua (/lua, squat.*)"),
	s("connect", Works, "client", "opens a session from the world book, or by host and port"),
	s("control", Unsupported, "-", "TinyFugue-squatter keyboard control: use the Keyboard control of the tab"),
	s("core", Unsupported, "-", "no core dump in a GUI client"),
	s("dc", Works, "client", "closes the session of a world"),
	s("def", Works, "engine", "plus the client extension -g (gag)"),
	s("dokey", Unsupported, "client", "the input line's editing keys belong to the UI; the command says so"),
	s("edit", Unsupported, "-", "macro editing is done in the editor / script dialogs"),
	s("eval", Works, "engine", ""),
	s("exit", Works, "engine", "ends a /load, as in real tf (it used to quit the client; use /quit)"),
	s("export", Works, "engine", ""),
	s("features", Works, "client", ""),
	s("fg", Works, "client", "brings a session to the front"),
	s("for", Works, "engine", ""),
	s("gag", Works, "engine", ""),
	s("help", Mapped, "client", "opens the help browser"),
	s("hilite", Works, "engine", ""),
	s("histsize", Works, "client", "sets the variable; the scrollback size is a client setting"),
	s("hook", Works, "engine", ""),
	s("input", Mapped, "client", "inserts text into the input line (it is never sent)"),
	s("kill", Works, "engine", ""),
	s("lcd", Works, "engine", "subject to /restrict file"),
	s("let", Works, "engine", ""),
	s("limit", Mapped, "client", "the scrollback filter of the window"),
	s("list", Works, "engine", ""),
	s("listsockets", Works, "client", "lists the open sessions"),
	s("liststreams", Unsupported, "-", "the engine does not list tfopen streams yet"),
	s("listvar", Works, "engine", ""),
	s("listworlds", Works, "client", "lists the world book"),
	s("load", Works, "engine", "relative names are searched in the current directory, then the config directory"),
	s("loaded", Works, "engine", ""),
	s("loadlua", Unsupported, "-", "real tf's embedded Lua"),
	s("loadworld", Mapped, "client", "the world book is edited in the Worlds dialog; there is no file to load"),
	s("localecho", Mapped, "client", "local echo of typed text for the session"),
	s("log", Works, "client", "writes the session log under the logs directory"),
	s("ps", Works, "engine", ""),
	s("purge", Works, "engine", ""),
	s("purgelua", Unsupported, "-", "real tf's embedded Lua"),
	s("python", Unsupported, "-", "real tf's embedded Python"),
	s("python_call", Unsupported, "-", "real tf's embedded Python"),
	s("python_kill", Unsupported, "-", "real tf's embedded Python"),
	s("python_load", Unsupported, "-", "real tf's embedded Python"),
	s("quit", Works, "client", "quits the client"),
	s("quote", Works, "engine", "shell forms subject to /restrict shell"),
	s("reboot", Unsupported, "-", "no process to re-exec in a GUI client"),
	s("recall", Mapped, "client", "searches the session's scrollback"),
	s("recordline", Unsupported, "-", "history is recorded by the client's scrollback"),
	s("relimit", Mapped, "client", "the scrollback filter of the window"),
	s("renameworld", Works, "client", "renames a world in the world book"),
	s("repeat", Works, "engine", ""),
	s("require", Works, "engine", ""),
	s("restrict", Works, "engine", "raises the restriction level; squatterd starts at shell"),
	s("save", Works, "client", "writes /def lines (relative to the current directory); moves to the engine when it has its own"),
	s("saveworld", Works, "client", "saves the world book"),
	s("send", Works, "engine", ""),
	s("set", Works, "engine", ""),
	s("setenv", Works, "engine", ""),
	s("sh", Works, "engine", "subject to /restrict shell"),
	s("shift", Works, "engine", ""),
	s("squat", Unsupported, "-", "that is the TinyFugue-side bridge command; here it would be circular"),
	s("status_add", Mapped, "client", "status bar fields"),
	s("status_clear", Mapped, "client", "status bar fields"),
	s("status_edit", Mapped, "client", "status bar fields"),
	s("status_rm", Mapped, "client", "status bar fields"),
	s("suspend", Mapped, "client", "type /suspend alone: detaches squatter sessions and minimises the window"),
	s("test", Works, "engine", ""),
	s("trig", Works, "engine", ""),
	s("trigger", Works, "engine", ""),
	s("trigpc", Unsupported, "-", "not in the engine yet"),
	s("unbind", Works, "engine", ""),
	s("undef", Works, "engine", ""),
	s("undefn", Works, "client", ""),
	s("unlimit", Mapped, "client", "the scrollback filter of the window"),
	s("unset", Works, "engine", ""),
	s("unworld", Works, "client", "removes a world from the book"),
	s("version", Works, "engine", ""),
	s("watchdog", Unsupported, "-", "idle watching needs the terminal's input clock"),
	s("watchname", Unsupported, "-", "idle watching needs the terminal's input clock"),
	// commands that real tf has as library macros (lib/tf/stdlib.tf) and that
	// SquatClient always had built in
	s("untrig", Works, "client", "library macro in real tf"),
	s("undeft", Works, "client", "library macro in real tf"),
	s("unhook", Works, "client", "library macro in real tf"),
	s("nogag", Works, "client", "library macro in real tf"),
	s("nohilite", Works, "client", "library macro in real tf"),
	s("partial", Works, "client", "library macro in real tf"),
	s("world", Works, "client", "connect if needed and bring to the front (SquatClient extension)"),
	s("addworld", Works, "client", ""),
	s("notify", Works, "client", "SquatClient extension: desktop notification"),
}
