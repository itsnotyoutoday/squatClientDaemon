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
	// package: a command real tf defines in its library, or a SquatClient
	// extension) or "-".
	Where string
	// GUI says what the command does in the SquatClient window, when that is
	// more than text ("" = it prints text like tf does).
	GUI  string
	Note string
}

// CommandStatuses lists every real tf builtin command this client knows about.
func CommandStatuses() []CommandStatus { return commandStatuses }

func s(name string, st Status, where, note string) CommandStatus {
	return CommandStatus{Name: name, Status: st, Where: where, Note: note}
}

func g(name string, st Status, where, gui, note string) CommandStatus {
	return CommandStatus{Name: name, Status: st, Where: where, GUI: gui, Note: note}
}

// The engine-implemented commands are marked "engine": the engine keeps their
// state (history, worlds, status rows, key bindings, filters) and this client
// feeds it and displays it (corehost.go). A "mapped" command also does
// something in the window; "GUI" says what, and docs/COMMAND-STATUS.md lists
// the text form that scripts, pipes and $(...) always get.
var commandStatuses = []CommandStatus{
	g("beep", Mapped, "engine", "the window's bell", "text form: none (it has no output)"),
	s("bind", Works, "engine", "bindings run from the input line (Engine.Key), see /dokey"),
	s("break", Works, "engine", ""),
	s("calllua", Unsupported, "-", "real tf's embedded Lua; SquatClient has its own Lua (/lua, squat.*)"),
	g("connect", Mapped, "engine", "opens a session (a tab) from the world book, or by host and port; -x TLS, -f/-b foreground or background", ""),
	s("control", Unsupported, "-", "TinyFugue-squatter keyboard control: use the Keyboard control of the tab"),
	g("core", Unsupported, "engine", "", "there is no core dump to make in a GUI client; the command says so"),
	g("dc", Mapped, "engine", "closes a session", "-all closes every one"),
	s("def", Works, "engine", "plus the SquatClient extension -g (gag)"),
	g("dokey", Mapped, "engine", "editing keys act on the input line when a key binding runs (Engine.Key); display keys (REDRAW, REFRESH, CLEAR) have nothing to do in a window", ""),
	s("edit", Works, "engine", ""),
	s("eval", Works, "engine", ""),
	s("exit", Works, "engine", "ends a /load, as in real tf (it used to quit the client; use /quit)"),
	s("export", Works, "engine", ""),
	s("features", Works, "engine", ""),
	g("fg", Mapped, "engine", "brings a session (tab) to the front; -q quiet", ""),
	s("for", Works, "engine", ""),
	s("gag", Works, "engine", ""),
	g("help", Mapped, "engine", "opens the help browser (typed /help is handled by the window; from a script it asks the window to open it)", ""),
	s("hilite", Works, "engine", ""),
	s("histsize", Works, "engine", ""),
	s("hook", Works, "engine", ""),
	g("input", Mapped, "engine", "inserts the text into the input line (it is never sent)", ""),
	s("kill", Works, "engine", ""),
	s("lcd", Works, "engine", "subject to /restrict file"),
	s("let", Works, "engine", ""),
	g("limit", Mapped, "engine", "filters the session's output and scrollback", "the filter is the engine's; the window re-renders under it"),
	s("list", Works, "engine", ""),
	g("listsockets", Mapped, "engine", "the open sessions, in the engine's socket table", ""),
	s("liststreams", Works, "engine", ""),
	s("listvar", Works, "engine", ""),
	g("listworlds", Mapped, "engine", "typed alone it opens the Worlds dialog; with any argument (e.g. /listworlds *, -s, -c) and in scripts, pipes and $(...) it prints the table", "the client's world book is the source of the list"),
	s("load", Works, "engine", "relative names are searched in the current directory, then the config directory"),
	s("loaded", Works, "engine", ""),
	s("loadlua", Unsupported, "-", "real tf's embedded Lua"),
	s("loadworld", Works, "client", "tf's library defines it as /load of WORLDFILE"),
	g("localecho", Mapped, "engine", "local echo of typed text for the session", ""),
	s("log", Works, "engine", "relative file names go under the logs directory"),
	s("ps", Works, "engine", ""),
	s("purge", Works, "engine", ""),
	s("purgelua", Unsupported, "-", "real tf's embedded Lua"),
	s("python", Unsupported, "-", "real tf's embedded Python"),
	s("python_call", Unsupported, "-", "real tf's embedded Python"),
	s("python_kill", Unsupported, "-", "real tf's embedded Python"),
	s("python_load", Unsupported, "-", "real tf's embedded Python"),
	g("quit", Mapped, "engine", "quits the client", ""),
	s("quote", Works, "engine", "shell forms subject to /restrict shell"),
	s("reboot", Unsupported, "-", "a GUI client does not re-exec itself; the engine says so"),
	g("recall", Works, "engine", "", "history of received, typed and local lines; prints text (the window's own history browser, Ctrl-R, searches what you sent)"),
	s("recordline", Works, "engine", ""),
	g("relimit", Mapped, "engine", "the scrollback filter of the window", ""),
	s("renameworld", Works, "engine", "renames the world in the client's world book too"),
	s("repeat", Works, "engine", ""),
	s("require", Works, "engine", ""),
	s("restrict", Works, "engine", "raises the restriction level (the desktop starts unrestricted)"),
	s("save", Works, "engine", "writes /def lines to a file"),
	s("saveworld", Works, "engine", "writes a world file; the client's own book is saved by the app"),
	s("send", Works, "engine", ""),
	s("set", Works, "engine", "wrap, wrapsize, wrapspace, wrappunct and visual drive the output window"),
	s("setenv", Works, "engine", ""),
	s("sh", Works, "engine", "subject to /restrict shell"),
	s("shift", Works, "engine", ""),
	s("squat", Unsupported, "-", "that is the TinyFugue-side bridge command; here it would be circular"),
	g("status_add", Mapped, "engine", "adds a field to the status bar", ""),
	g("status_clear", Mapped, "engine", "the status bar", ""),
	g("status_edit", Mapped, "engine", "the status bar", ""),
	g("status_rm", Mapped, "engine", "the status bar", ""),
	g("suspend", Mapped, "engine", "detaches squatter sessions and minimises the window", ""),
	s("test", Works, "engine", ""),
	s("trig", Works, "engine", ""),
	s("trigger", Works, "engine", ""),
	s("trigpc", Unsupported, "-", "not in the engine yet"),
	s("unbind", Works, "engine", ""),
	s("undef", Works, "engine", ""),
	s("undefn", Works, "engine", ""),
	g("unlimit", Mapped, "engine", "the scrollback filter of the window", ""),
	s("unset", Works, "engine", ""),
	s("unworld", Works, "engine", "removes the world from the client's world book too"),
	s("version", Works, "engine", ""),
	s("watchdog", Works, "engine", ""),
	s("watchname", Works, "engine", ""),
	// library commands real tf defines in lib/tf (stdlib.tf) that the client supplies itself, so they
	// work without the library; the library's own definitions take over when it is loaded
	g("addworld", Mapped, "client", "typed alone it opens the Worlds dialog; with arguments it defines the world in the client's book", "tf's library defines it as a macro over addworld()"),
	g("world", Mapped, "client", "connects if needed and brings the session to the front; typed alone it opens the Worlds dialog", "SquatClient extension"),
	s("untrig", Works, "client", "library macro in real tf"),
	s("undeft", Works, "client", "library macro in real tf"),
	s("unhook", Works, "client", "library macro in real tf"),
	s("nogag", Works, "client", "library macro in real tf"),
	s("nohilite", Works, "client", "library macro in real tf"),
	s("partial", Works, "client", "library macro in real tf"),
	g("wrap", Mapped, "client", "wraps the output window at the width, or on/off", "library macro in real tf: sets wrap and wrapsize"),
	g("nowrap", Mapped, "client", "turns wrapping off in the output window", "library macro in real tf"),
	g("wrapspace", Mapped, "client", "indent of wrapped lines in the output window", "library macro in real tf"),
	g("visual", Mapped, "client", "the window is always split into output, status bar and input line; this records the setting", "library macro in real tf"),
	s("notify", Works, "client", "SquatClient extension: desktop notification"),
}
